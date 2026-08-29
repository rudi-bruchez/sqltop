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
	// Fifty quiet ticks comfortably clears both the window's turnover and a
	// full recoveryPeriod counted from that credited start; a boundary test
	// on that arithmetic would be brittle across constant changes and is
	// deliberately not attempted here.
	feed(b, &total, now.Add(30*time.Second), 50, 5)
	if _, level, _ := b.State(); level != 1 {
		t.Fatalf("level = %d, want 1: recovery is one step per thirty quiet seconds, not a jump back", level)
	}
}

func TestCounterResetDoesNotThrottle(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()
	feed(b, &total, now, 15, 80) // drive it over budget first, so the reset has something to spare us from
	_, levelBefore, _ := b.State()
	if levelBefore == 0 {
		t.Fatal("test setup: the feed above should already have escalated")
	}

	// The session reconnected: cpu_time restarts far below where it was.
	b.Observe(model.Cost{At: now.Add(16 * time.Second), CPUMs: 5})

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
