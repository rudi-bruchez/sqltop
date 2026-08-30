package collector

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

func TestCollectorFillsTheWindowAndFlattens(t *testing.T) {
	src := fake.New([]model.RequestSample{
		{Ref: model.RequestRef{SessionID: 52}, BlockedBy: 51},
		{Ref: model.RequestRef{SessionID: 51}},
	})
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	rows := w.Latest()
	if len(rows) != 2 {
		t.Fatalf("window holds %d rows, want 2", len(rows))
	}
	if rows[0].Ref.SessionID != 51 || rows[1].Depth != 1 {
		t.Fatalf("rows = %+v, want the blocker first and the blocked at depth 1: the collector must flatten before storing", rows)
	}
}

func TestCollectorSurvivesASourceFailure(t *testing.T) {
	src := fake.New(nil)
	src.Err = context.DeadlineExceeded
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	st := c.Status()
	if st.Connected {
		t.Fatal("a failing source must show as disconnected rather than presenting stale numbers as fresh")
	}
	if st.Message == "" {
		t.Fatal("the status bar must say what went wrong")
	}
}

func TestCollectorStopsOnContextCancel(t *testing.T) {
	c := New(fake.New(nil), window.New(time.Minute, 100), NewBudget(50, baseTiers()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return promptly when its context is cancelled")
	}
}

// countingSource wraps the fake so a test can both count calls and force one
// method to fail forever, while every other tier keeps working normally
// through the embedded fake.
type countingSource struct {
	*fake.Source
	calls int32
}

func (s *countingSource) SampleRequests(context.Context) ([]model.RequestSample, error) {
	atomic.AddInt32(&s.calls, 1)
	return nil, errors.New("boom")
}

func TestCollectorBacksOffAndDoesNotSpinOnFailure(t *testing.T) {
	src := &countingSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	// Deliberately tiny: if the requests tier retried at its own period
	// instead of backing off, 1.2 seconds at 5 ms would produce well over a
	// hundred calls.
	tiers.Requests = config.Duration(5 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	calls := atomic.LoadInt32(&src.calls)
	if calls == 0 {
		t.Fatal("SampleRequests was never called")
	}
	// Backoff starts at one second, so in 1.2 s at most two attempts fit:
	// one right away and one around the one second mark. Allow a little
	// slack for scheduling jitter without allowing anything close to the
	// hundred-plus calls an un-throttled retry would produce.
	if calls > 4 {
		t.Fatalf("SampleRequests was called %d times in 1.2 s despite failing every time: the requests tier is not backing off, it is spinning", calls)
	}
}

func TestCollectorLeavesNoGoroutinesRunning(t *testing.T) {
	before := runtime.NumGoroutine()

	c := New(fake.New(nil), window.New(time.Minute, 100), NewBudget(50, baseTiers()))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	// Give the tier goroutines a moment to actually start before asking them
	// to stop, or this would prove nothing about goroutines that never ran.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	deadline := time.Now().Add(time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count grew from %d to %d and never settled back down: the collector leaked a goroutine", before, now)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollectorBudgetThrottlesUnderLoad(t *testing.T) {
	src := fake.New(nil)
	src.CostPerCall = 1000 // far more server CPU per call than the 50 ms/s limit
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(20 * time.Millisecond)
	tiers.Counters = config.Duration(20 * time.Millisecond)
	bud := NewBudget(50, tiers)
	c := New(src, w, bud)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	if _, level, _ := bud.State(); level == 0 {
		t.Fatal("the budget never escalated despite the source charging far more than the limit: Cost is not reaching Observe")
	}
	if got, base := bud.Period(model.TierSpace), tiers.Space.Std(); got != base*2 {
		t.Fatalf("space period = %v, want %v: throttling must actually change what Period returns", got, base*2)
	}
}

// TestCollectorSurfacesTheFullRecoveryMessage crosses the seam between
// Budget and Collector that let a message go unreachable by construction:
// Budget.reviseLocked writes "back inside the observation budget, full
// refresh rate restored" at the exact same moment it drops the level to 0,
// but messageLocked used to hand that message to the status bar only when
// level > 0, which is never true right when that particular message is set.
// Budget's own tests (TestRecoversOneStepAtATime and friends) assert
// levels, never message text, and Collector's tests that do check Message
// use a permanently failing tier or Cost, never a budget that recovers all
// the way to 0 - so neither side's test suite could have caught this on its
// own. Driven with feed rather than a live Collector.Run, since the real
// timings involved (an escalate cooldown, then a thirty second recovery
// period) are too slow to wait out in a test.
func TestCollectorSurfacesTheFullRecoveryMessage(t *testing.T) {
	tiers := baseTiers()
	bud := NewBudget(50, tiers)
	var total int64
	now := time.Now()

	// Escalate: 15 s over budget is enough to reach level 1 (see
	// TestOverBudgetDegradesSpaceFirst for the same shape).
	feed(bud, &total, now, 15, 80)
	if _, level, _ := bud.State(); level == 0 {
		t.Fatal("the budget never escalated; the rest of this test proves nothing without a level to recover from")
	}

	// Recover all the way to 0: over thirty seconds of quiet readings,
	// comfortably past recoveryPeriod plus the window-turnover delay
	// TestRecoversOneStepAtATime documents, so the level actually reaches 0
	// rather than stopping at 1.
	feed(bud, &total, now.Add(20*time.Second), 60, 5)
	if _, level, msg := bud.State(); level != 0 {
		t.Fatalf("level = %d after a long quiet period, want 0 (msg = %q); this test needs a full recovery to exercise the bug", level, msg)
	}

	c := New(fake.New(nil), window.New(time.Minute, 1000), bud)
	got := c.Status().Message
	want := "back inside the observation budget, full refresh rate restored"
	if got != want {
		t.Fatalf("Status().Message = %q, want %q: a DBA watching a server recover must see this, not the warning simply vanishing", got, want)
	}
}

// counterSampleFailsSource makes SampleServer fail for the counters tier
// specifically while every other call, Cost included, keeps working. It
// exists to prove Cost/Observe run independently of that one query's
// outcome: fix for the review's Critical finding, where the budget went
// blind the moment the counters tier's own dashboard query started failing.
type counterSampleFailsSource struct {
	*fake.Source
	cum int64
}

func (s *counterSampleFailsSource) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	if tier == model.TierCounters {
		return model.ServerSample{}, errors.New("counters boom")
	}
	return s.Source.SampleServer(ctx, tier)
}

// Cost reports a cost far above any reasonable budget, growing regardless of
// whether SampleServer succeeded, exactly as the review's driven repro did:
// 5000 ms per call against a 50 ms/s limit.
func (s *counterSampleFailsSource) Cost(context.Context) (model.Cost, error) {
	cum := atomic.AddInt64(&s.cum, 5000)
	return model.Cost{At: time.Now(), CPUMs: cum}, nil
}

func TestCollectorKeepsChargingTheBudgetWhenCountersSamplingFails(t *testing.T) {
	src := &counterSampleFailsSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 1000)
	bud := NewBudget(50, baseTiers())
	c := New(src, w, bud)

	// SampleServer(TierCounters) fails on every attempt, so the counters
	// tier backs off: floor is one second (its configured period, 1 s by
	// default, is not below minBackoff), so the run needs to span at least
	// two attempts a second apart to get the two Cost readings a rate needs.
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	used, level, msg := bud.State()
	t.Logf("counters SampleServer failing throughout a 1.3 s run, Cost charging 5000 ms/call: budget used=%.0f ms/s level=%d msg=%q", used, level, msg)
	if level == 0 {
		t.Fatal("the budget never escalated even though Cost kept reporting far more than the limit: a failing SampleServer(TierCounters) must not stop Cost/Observe from running")
	}
}

// spaceSampleFailsSource makes SampleServer fail for the space tier only,
// permanently, while everything else stays healthy. It exists to prove a
// permanently broken tier's error survives every other tier's success:
// fix for the review's first Important finding.
type spaceSampleFailsSource struct {
	*fake.Source
}

func (s *spaceSampleFailsSource) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	if tier == model.TierSpace {
		return model.ServerSample{}, errors.New("space boom")
	}
	return s.Source.SampleServer(ctx, tier)
}

func TestCollectorKeepsAFailingTiersMessageVisible(t *testing.T) {
	src := &spaceSampleFailsSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(5 * time.Millisecond)
	tiers.Counters = config.Duration(5 * time.Millisecond)
	tiers.Space = config.Duration(5 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	var seen, empty int
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(2500 * time.Millisecond)
poll:
	for {
		select {
		case <-ticker.C:
			seen++
			if c.Status().Message == "" {
				empty++
			}
		case <-deadline:
			break poll
		}
	}
	<-done

	final := c.Status()
	t.Logf("polled %d times over 2.5 s, message empty %d times, final status = %+v", seen, empty, final)
	if empty > seen/10 {
		t.Fatalf("status message was empty in %d of %d polls despite a permanently failing tier: a healthy tier's success is erasing another tier's still-current error", empty, seen)
	}
}

// identifyCountingSource counts every Identify call, on top of a source that
// never succeeds at anything. It exists to prove the preflight is not
// re-run on every failed retry: fix for the review's second Important
// finding, where reidentify fired on every attempt while disconnected and
// turned the backoff meant to protect a struggling server into a source of
// load on it.
type identifyCountingSource struct {
	*fake.Source
	identifyCalls int32
}

func (s *identifyCountingSource) Identify(ctx context.Context) (model.ServerInfo, model.Capabilities, error) {
	atomic.AddInt32(&s.identifyCalls, 1)
	return s.Source.Identify(ctx)
}

func TestCollectorNeverRepeatsThePreflightOnRetry(t *testing.T) {
	inner := fake.New(nil)
	inner.Err = errors.New("connection refused")
	src := &identifyCountingSource{Source: inner}
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.Run(ctx)

	calls := atomic.LoadInt32(&src.identifyCalls)
	t.Logf("source failing throughout a 2 s run: Identify called %d time(s)", calls)
	if calls != 1 {
		t.Fatalf("Identify was called %d times against a source that never recovers, want exactly 1: retrying the preflight on every failed tier attempt is the spin section 4.4 exists to prevent", calls)
	}
}

// cpuHistoryFailsSource fails only the CPU history tier, while counting its
// attempts. It exists to prove a tier slower than the backoff floor does not
// end up retrying more often while broken than it samples while healthy:
// fix for the review's third Important finding.
type cpuHistoryFailsSource struct {
	*fake.Source
	calls int32
}

func (s *cpuHistoryFailsSource) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	if tier == model.TierCPUHistory {
		atomic.AddInt32(&s.calls, 1)
		return model.ServerSample{}, errors.New("cpu history boom")
	}
	return s.Source.SampleServer(ctx, tier)
}

func TestCollectorBackoffFloorMatchesTheTiersOwnPeriod(t *testing.T) {
	src := &cpuHistoryFailsSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	// Slower than minBackoff on purpose: a healthy run of this tier makes
	// exactly one call in the window below, at t=0. A broken run must not
	// do better than that.
	tiers.CPUHistory = config.Duration(2 * time.Second)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	calls := atomic.LoadInt32(&src.calls)
	t.Logf("cpuHistory period is 2 s; %d SampleServer(TierCPUHistory) calls landed in 1.5 s of failure", calls)
	if calls > 1 {
		t.Fatalf("SampleServer(TierCPUHistory) was called %d times in 1.5 s despite the tier's own period being 2 s: the backoff floor is not respecting a slow tier's healthy cadence", calls)
	}
}

// TestCollectorReadPathIsSafeWhileTiersAreWriting drives every tier at 1 ms
// while several goroutines hammer the public read methods, so the read path
// is actually exercised concurrently with the writers rather than only
// after Run has returned, which is what every other test in this file does.
// Meaningful only under -race.
func TestCollectorReadPathIsSafeWhileTiersAreWriting(t *testing.T) {
	src := fake.New([]model.RequestSample{{Ref: model.RequestRef{SessionID: 1}}})
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(time.Millisecond)
	tiers.Counters = config.Duration(time.Millisecond)
	tiers.Space = config.Duration(time.Millisecond)
	tiers.CPUHistory = config.Duration(time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.Status()
					_ = c.Server()
					_ = w.Latest()
					_, _, _ = w.Depth()
				}
			}
		}()
	}

	<-done
	close(stop)
	readers.Wait()
}

// TestSampleSkipsTheSourceOnceCtxIsAlreadyCancelled kills the mutation of
// removing the ctx.Err() check at the top of sample: without it, sample
// would go on to call SampleRequests (or SampleServer) against a context
// that is already done, which is exactly the extra Source call the fix
// round 2 review measured (63 ms to return instead of 24 ms, one extra call
// placed after cancel).
func TestSampleSkipsTheSourceOnceCtxIsAlreadyCancelled(t *testing.T) {
	src := &countingSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 100)
	c := New(src, w, NewBudget(50, baseTiers()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if ok := c.sample(ctx, model.TierRequests); ok {
		t.Fatal("sample must report failure once ctx is already cancelled")
	}
	// countingSource's SampleRequests override counts calls; it is not the
	// one under test here (this test overrides ctx, not the source's own
	// error), so a call reaching it at all is the mutation's signature.
	if got := atomic.LoadInt32(&src.calls); got != 0 {
		t.Fatalf("sample placed %d SampleRequests call(s) after ctx was already cancelled, want 0", got)
	}
}

// cancelDuringSampleServerSource cancels ctx itself from inside
// SampleServer, simulating cancellation landing in the gap between the
// counters tier's two calls, and counts how many times Cost is reached
// afterward.
type cancelDuringSampleServerSource struct {
	*fake.Source
	cancel    context.CancelFunc
	costCalls int32
}

func (s *cancelDuringSampleServerSource) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	s.cancel()
	return s.Source.SampleServer(ctx, tier)
}

func (s *cancelDuringSampleServerSource) Cost(ctx context.Context) (model.Cost, error) {
	atomic.AddInt32(&s.costCalls, 1)
	return s.Source.Cost(ctx)
}

// TestSampleSkipsCostOnceCtxIsCancelledMidRound kills the mutation of
// removing the second ctx.Err() check, the one guarding the counters tier's
// call to Cost after SampleServer has already returned. This is the call
// the review's 63 ms number was mostly about: a source that does not honour
// ctx cannot be interrupted mid-call, but the collector must not place a
// second, avoidable call once it already knows shutdown was asked for.
func TestSampleSkipsCostOnceCtxIsCancelledMidRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelDuringSampleServerSource{Source: fake.New(nil)}
	src.cancel = cancel
	w := window.New(time.Minute, 100)
	c := New(src, w, NewBudget(50, baseTiers()))

	c.sample(ctx, model.TierCounters)

	if got := atomic.LoadInt32(&src.costCalls); got != 0 {
		t.Fatalf("Cost was called %d time(s) after ctx was cancelled mid-round (inside SampleServer), want 0", got)
	}
}

// breaksAfterFirstSource lets SampleServer succeed until told to break, then
// fails every call from then on, so a test can pin down exactly when the
// last successful merge into Collector.figures happened.
type breaksAfterFirstSource struct {
	*fake.Source
	broken int32
}

func (s *breaksAfterFirstSource) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	if atomic.LoadInt32(&s.broken) == 1 {
		return model.ServerSample{}, errors.New("broke")
	}
	return s.Source.SampleServer(ctx, tier)
}

// TestServerAtDoesNotDriftOnceTheSourceBreaks kills the mutation of
// Server() restamping At with time.Now() at read time instead of the time
// of the sample that actually produced the figures: with that mutation, two
// reads a second apart would differ by about a second even though nothing
// could have merged in between.
func TestServerAtDoesNotDriftOnceTheSourceBreaks(t *testing.T) {
	src := &breaksAfterFirstSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 100)
	tiers := baseTiers()
	tiers.Counters = config.Duration(20 * time.Millisecond)
	tiers.Space = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	// Let at least one server-tier sample land, then break the source so no
	// further merge can ever happen again for the rest of this run. A second
	// settle wait follows the flip: SampleServer checks the flag at its own
	// top, so a call already past that check when the flag flips can still
	// land (and write figuresAt) microseconds later. Both tiers here poll
	// every 20 ms and the fake does no real work, so any such straggler is
	// long done well within this margin; without it, first below can itself
	// race a straggler's write and the test flakes on the two reads
	// differing by microseconds instead of the intended second.
	time.Sleep(100 * time.Millisecond)
	atomic.StoreInt32(&src.broken, 1)
	time.Sleep(100 * time.Millisecond)

	first := c.Server().At
	if first.IsZero() {
		t.Fatal("Server().At is still zero after the source had 100 ms to succeed at least once")
	}

	time.Sleep(time.Second)
	second := c.Server().At
	<-done

	if !second.Equal(first) {
		t.Fatalf("Server().At moved from %v to %v a second later despite every call in between having failed: it must reflect the last real sample, not the instant it is read", first, second)
	}
}

// costFailsCountingSource makes Cost fail on every call while everything
// else, SampleServer included, keeps succeeding normally, and counts how
// many times SampleServer(TierCounters) is actually called.
type costFailsCountingSource struct {
	*fake.Source
	counterCalls int32
}

func (s *costFailsCountingSource) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	if tier == model.TierCounters {
		atomic.AddInt32(&s.counterCalls, 1)
	}
	return s.Source.SampleServer(ctx, tier)
}

func (s *costFailsCountingSource) Cost(context.Context) (model.Cost, error) {
	return model.Cost{}, errors.New("cost boom")
}

// TestCollectorCostFailureDoesNotThrottleTheCountersTier kills the mutation
// of letting a Cost failure alone fail the counters tier: if it did, the
// tier would back off (floored at one second) instead of sampling at its
// configured 20 ms period, and a 1 s run would produce one or two calls
// instead of close to fifty.
func TestCollectorCostFailureDoesNotThrottleTheCountersTier(t *testing.T) {
	src := &costFailsCountingSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Counters = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	calls := atomic.LoadInt32(&src.counterCalls)
	t.Logf("Cost failing throughout a 1 s run at a 20 ms counters period: SampleServer(TierCounters) called %d times", calls)
	if calls < 30 {
		t.Fatalf("SampleServer(TierCounters) was called only %d times in 1 s at a 20 ms period despite SampleServer itself always succeeding: a failing Cost read must not throttle the tier, want close to 50", calls)
	}
}

// TestCollectorCostFailureReachesTheStatusBar kills the mutation of a Cost
// failure being swallowed instead of surfaced: it asserts the error text
// actually reaches Status().Message rather than merely trusting the comment
// above failCost's call site.
func TestCollectorCostFailureReachesTheStatusBar(t *testing.T) {
	src := &costFailsCountingSource{Source: fake.New(nil)}
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Counters = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	msg := c.Status().Message
	t.Logf("status message with Cost permanently failing: %q", msg)
	if !strings.Contains(msg, "reading own cost") {
		t.Fatalf("status message %q does not mention the cost read failure: a failing Cost must be visible in the status bar, not silent", msg)
	}
}

// TestPeriodForwardsToTheBudgetAndTracksThrottle proves Collector.Period
// (fix round 1, task 14) is not a snapshot taken once: it is a live forward
// to Budget.Period, so a caller that asks again after the budget escalates
// gets the doubled period without needing any cooperation from Collector
// beyond this one call. internal/web's stream handler is exactly such a
// caller, and this is what stands in, at this package's level, for that
// dependency actually holding.
func TestPeriodForwardsToTheBudgetAndTracksThrottle(t *testing.T) {
	tiers := baseTiers()
	bud := NewBudget(50, tiers)
	c := New(fake.New(nil), window.New(time.Minute, 1000), bud)

	if got := c.Period(model.TierRequests); got != tiers.Requests.Std() {
		t.Fatalf("Period(TierRequests) = %v, want the base period %v before anything throttles", got, tiers.Requests.Std())
	}

	// Escalate to level 3 (tier A, requests, is the last to give) exactly
	// as TestStillOverBudgetDegradesCountersThenRequests does in
	// budget_test.go, reusing the same feed helper and timing.
	var total int64
	now := time.Now()
	feed(bud, &total, now, 15, 80)
	feed(bud, &total, now.Add(15*time.Second), 15, 80)
	feed(bud, &total, now.Add(30*time.Second), 15, 80)

	want := tiers.Requests.Std() * 2
	if got := c.Period(model.TierRequests); got != want {
		t.Fatalf("Period(TierRequests) = %v after escalating to level 3, want %v (doubled): Collector.Period must track the budget live, not a value cached at construction", got, want)
	}
}
