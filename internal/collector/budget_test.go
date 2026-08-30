package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

func baseTiers() config.Tiers {
	return config.Tiers{
		Requests:   config.Duration(time.Second),
		Counters:   config.Duration(time.Second),
		Space:      config.Duration(5 * time.Second),
		CPUHistory: config.Duration(time.Minute),
		LivePlan:   config.Duration(2 * time.Second),
	}
}

// feed drives b with seconds one-second ticks of msPerSecond, starting from
// *total (which it advances). total is carried across calls on purpose: a
// fresh local counter on every call would make the second call's first
// reading smaller than the first call's last one, which Observe correctly
// reads as a session reset and skips, silently losing a sample.
func feed(b *Budget, total *int64, start time.Time, seconds int, msPerSecond int64) {
	for i := 1; i <= seconds; i++ {
		*total += msPerSecond
		b.Observe(model.Cost{At: start.Add(time.Duration(i) * time.Second), CPUMs: *total})
	}
}

func TestUnderBudgetKeepsBasePeriods(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	feed(b, &total, time.Now(), 15, 6)

	if got := b.Period(model.TierSpace); got != 5*time.Second {
		t.Fatalf("space period = %v, want the base 5s while under budget", got)
	}
	used, level, _ := b.State()
	if level != 0 {
		t.Fatalf("level = %d, want 0", level)
	}
	if used < 5 || used > 7 {
		t.Fatalf("used = %v ms/s, want about 6", used)
	}
}

func TestFirstObservationCannotThrottle(t *testing.T) {
	b := NewBudget(50, baseTiers())
	// A single cumulative reading carries no rate; a huge one must not be
	// mistaken for a huge rate.
	b.Observe(model.Cost{At: time.Now(), CPUMs: 900_000})

	if _, level, _ := b.State(); level != 0 {
		t.Fatalf("level = %d, want 0: one cumulative sample is not a rate", level)
	}
}

func TestExactlyAtBudgetDoesNotEscalate(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	// Consumption exactly at the limit has not exceeded it: spec section 10
	// says "when the budget is exceeded", not "reached".
	feed(b, &total, time.Now(), 15, 50)

	if _, level, _ := b.State(); level != 0 {
		t.Fatalf("level = %d, want 0: exactly at budget is not over budget", level)
	}
}

func TestOverBudgetDegradesSpaceFirst(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	feed(b, &total, time.Now(), 15, 80)

	if got := b.Period(model.TierSpace); got != 10*time.Second {
		t.Fatalf("space period = %v, want 10s: the least valuable tier slows first", got)
	}
	if got := b.Period(model.TierRequests); got != time.Second {
		t.Fatalf("requests period = %v, want 1s untouched at level 1: the grid is the tool", got)
	}
	if _, _, msg := b.State(); !strings.Contains(msg, "tier C") {
		t.Fatalf("message = %q, want it to name tier C: section 10 requires naming which tier slowed and why", msg)
	}
}

func TestStillOverBudgetDegradesCountersThenRequests(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()
	feed(b, &total, now, 15, 80)
	feed(b, &total, now.Add(15*time.Second), 15, 80)
	if got := b.Period(model.TierCounters); got != 2*time.Second {
		t.Fatalf("counters period = %v, want 2s at level 2", got)
	}
	feed(b, &total, now.Add(30*time.Second), 15, 80)
	if got := b.Period(model.TierRequests); got != 2*time.Second {
		t.Fatalf("requests period = %v, want 2s at level 3, the last thing to give", got)
	}
}

func TestCPUHistoryNeverThrottles(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	// Sustain heavy overage well past the third escalation, so the level is
	// pinned at maxLevel with room to spare.
	feed(b, &total, time.Now(), 90, 200)

	if _, level, _ := b.State(); level != maxLevel {
		t.Fatalf("test setup: level = %d, want %d (max)", level, maxLevel)
	}
	if got := b.Period(model.TierCPUHistory); got != time.Minute {
		t.Fatalf("CPU history period = %v, want the base 1m untouched even at max level: it already runs once a minute, halving it saves nothing", got)
	}
}

func TestRecoversOneStepAtATime(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()
	feed(b, &total, now, 15, 80)
	feed(b, &total, now.Add(15*time.Second), 15, 80)
	if _, level, _ := b.State(); level != 2 {
		t.Fatalf("level = %d, want 2 before recovery", level)
	}

	// The window's original tick counts (31 quiet ticks) came from a
	// window-less design and no longer apply: with a sliding window, the
	// average does not fall below the limit the instant the feed goes
	// quiet, it falls once enough quiet readings have displaced the last
	// overage readings from the trailing ten seconds, and then the
	// recovery clock itself credits from the oldest reading still in that
	// window rather than from the moment the average crossed the line.
	//
	// Under these constants the first step (level 2 to 1) lands on quiet
	// tick 24 and the second (level 1 to 0) on quiet tick 54, thirty
	// seconds later, matching recoveryPeriod once the first step's own
	// window-turnover delay is behind it. Forty quiet ticks is 16 clear of
	// the first step and 14 clear of the second: enough margin on both
	// sides that this is a genuine two-sided assertion (at least one step,
	// at most one), not a boundary test, which the arithmetic above is
	// deliberately too specific to constant values to want pinned exactly.
	feed(b, &total, now.Add(30*time.Second), 40, 5)
	if _, level, _ := b.State(); level != 1 {
		t.Fatalf("level = %d, want 1: recovery is one step per thirty quiet seconds, not a jump back", level)
	}
}

func TestCounterResetDoesNotThrottle(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()
	feed(b, &total, now, 15, 80) // drive it over budget first, so the reset has something to spare us from
	usedBefore, levelBefore, _ := b.State()
	if levelBefore == 0 {
		t.Fatal("test setup: the feed above should already have escalated")
	}
	windowLenBefore := len(b.window)

	// The session reconnected: cpu_time restarts far below where it was.
	b.Observe(model.Cost{At: now.Add(16 * time.Second), CPUMs: 5})

	// The reset must be skipped outright, not folded in as a reading of
	// zero cost: at this instant the window still averages well over the
	// limit either way, so level and quietFrom alone cannot tell the two
	// apart. The window itself can: a folded-in zero-cost reading is still
	// one more interval appended (and would pull the average down, however
	// slightly, the moment it entered the window).
	if len(b.window) != windowLenBefore {
		t.Fatalf("window length = %d after a reset, want unchanged %d: the reset must not be folded in as a zero-cost interval", len(b.window), windowLenBefore)
	}
	if used, _, _ := b.State(); used != usedBefore {
		t.Fatalf("used = %v after a reset, want unchanged %v: a folded-in zero-cost reading would have moved it", used, usedBefore)
	}
	if _, level, _ := b.State(); level != levelBefore {
		t.Fatalf("level = %d after a reset, want unchanged %d: a reconnection must not be read as a spike or as free capacity", level, levelBefore)
	}
	if !b.quietFrom.IsZero() {
		t.Fatal("a reset must not start the quiet clock: it carries no rate, quiet or otherwise, and was skipped rather than folded into the window")
	}
}

func TestEscalationCooldownSurvivesARecoveryStep(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()

	feed(b, &total, now, 15, 80)
	if _, level, _ := b.State(); level != 1 {
		t.Fatalf("test setup: level = %d, want 1", level)
	}

	// Feed quiet ticks one at a time until it actually recovers to level 0,
	// recording exactly when that happens. This is deliberately not assumed
	// to be a fixed number of ticks after the quiet feed starts: the
	// recovery clock credits from the oldest reading still in the window
	// (see reviseLocked), so the exact moment depends on that dynamic, not
	// on a round count of seconds.
	at := now.Add(15 * time.Second)
	var recoveredAt time.Time
	for i := 0; i < 120; i++ {
		at = at.Add(time.Second)
		total += 5
		b.Observe(model.Cost{At: at, CPUMs: total})
		if _, level, _ := b.State(); level == 0 {
			recoveredAt = at
			break
		}
	}
	if recoveredAt.IsZero() {
		t.Fatal("test setup: never recovered to level 0 within 120 quiet ticks")
	}

	// Immediately overload again, stopping one tick short of a full
	// escalateCooldown measured from the recovery moment. Without sharing
	// the hysteresis, lastEscalate would still hold the original escalation
	// from fifteen seconds into the test and this would fire almost at
	// once, as soon as the window itself carries enough of the burst.
	at = recoveredAt
	for i := 0; i < int(escalateCooldown.Seconds())-1; i++ {
		at = at.Add(time.Second)
		total += 200
		b.Observe(model.Cost{At: at, CPUMs: total})
	}
	if _, level, _ := b.State(); level != 0 {
		t.Fatalf("level = %d, want 0: a burst right after recovering must not walk the level straight back up before its own cooldown passes", level)
	}

	// Push past the cooldown boundary and confirm it does fire once due.
	for i := 0; i < 5; i++ {
		at = at.Add(time.Second)
		total += 200
		b.Observe(model.Cost{At: at, CPUMs: total})
	}
	if _, level, _ := b.State(); level == 0 {
		t.Fatal("level = 0, want an escalation once the cooldown from the recovery step has actually passed")
	}
}

func TestStateReportsWindowAverageNotLastReading(t *testing.T) {
	b := NewBudget(1000, baseTiers()) // limit set high enough that nothing here throttles; this test is only about State()'s number
	now := time.Now()
	var total int64

	// The first Observe only seeds; the window then holds four readings at
	// 90, 10, 90, 10 ms/s. The last reading alone (10) is nowhere near the
	// window's average (50): if State() ever regresses to reporting only
	// the most recent interval, this is the assertion that catches it.
	deltas := []int64{10, 90, 10, 90, 10}
	at := now
	for _, d := range deltas {
		total += d
		at = at.Add(time.Second)
		b.Observe(model.Cost{At: at, CPUMs: total})
	}

	want := (90.0 + 10.0 + 90.0 + 10.0) / 4.0
	if used, _, _ := b.State(); used != want {
		t.Fatalf("used = %v, want the window average %v, not just the last reading (%v ms/s)", used, want, deltas[len(deltas)-1])
	}
}

func TestWindowAverageIsTimeWeighted(t *testing.T) {
	b := NewBudget(1000, baseTiers())
	now := time.Now()

	// Two readings of unequal duration: one second at 100 ms/s, then four
	// seconds at 10 ms/s. A plain mean of the two per-reading rates gives
	// 55; the correct, time-weighted answer gives the true rate over the
	// five seconds actually observed, 28. Every other test in this file
	// samples at a uniform one second, where the two computations agree,
	// which is exactly why this case exists.
	b.Observe(model.Cost{At: now, CPUMs: 0})
	b.Observe(model.Cost{At: now.Add(1 * time.Second), CPUMs: 100})
	b.Observe(model.Cost{At: now.Add(5 * time.Second), CPUMs: 140})

	want := 140.0 / 5.0
	plainMean := (100.0 + 10.0) / 2.0
	if used, _, _ := b.State(); used != want {
		t.Fatalf("used = %v, want the time-weighted average %v (a plain mean of the two readings would give %v)", used, want, plainMean)
	}
}

func TestSubSecondCadenceEscalatesOnTrueRate(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	var total int64

	// Sample five times a second: each 200ms interval costs 16ms, which
	// read as "ms per reading" looks nowhere near the 50 ms/s limit, but
	// the true rate, 16ms every 200ms, is 80 ms/s. A design that weights by
	// reading count instead of elapsed time would under-react here.
	at := now
	b.Observe(model.Cost{At: at, CPUMs: 0})
	for i := 0; i < 60; i++ {
		at = at.Add(200 * time.Millisecond)
		total += 16
		b.Observe(model.Cost{At: at, CPUMs: total})
	}

	if _, level, _ := b.State(); level == 0 {
		t.Fatal("level = 0, want throttled: sub-second sampling at a true rate of 80 ms/s must not be diluted by reading count")
	}
}

func TestGapLongerThanWindowDropsStaleReadings(t *testing.T) {
	b := NewBudget(1000, baseTiers())
	now := time.Now()
	var total int64

	// Prime the window with heavy readings, then go silent for longer than
	// budgetWindow before the next Observe. The single interval spanning
	// that silence is counted whole, not prorated (a deliberate choice: see
	// the package doc), so once it lands, every one of the earlier heavy
	// readings must have aged out, leaving only this one interval in the
	// window and used equal to its own rate.
	feed(b, &total, now, 5, 500)
	gapEnd := now.Add(5 * time.Second).Add(2 * budgetWindow)
	total += 60
	b.Observe(model.Cost{At: gapEnd, CPUMs: total})

	if got := len(b.window); got != 1 {
		t.Fatalf("window length = %d, want 1: a gap longer than the window must age out every reading from before it", got)
	}
	want := 60.0 / (2 * budgetWindow).Seconds()
	if used, _, _ := b.State(); used != want {
		t.Fatalf("used = %v, want %v: only the interval spanning the present should remain", used, want)
	}
}

func TestBaseForPanicsOnUnknownTier(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("baseFor did not panic on a tier outside model.Tier's four values")
		}
	}()
	baseFor(baseTiers(), model.Tier(99))
}

func TestDegradedFromPanicsOnUnknownTier(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("degradedFrom did not panic on a tier outside model.Tier's four values")
		}
	}()
	degradedFrom(model.Tier(99))
}
