package collector

import (
	"context"
	"errors"
	"runtime"
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
