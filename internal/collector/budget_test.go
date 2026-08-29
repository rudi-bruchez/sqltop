package collector

import (
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

func feed(b *Budget, start time.Time, seconds int, msPerSecond int64) {
	var total int64
	for i := 1; i <= seconds; i++ {
		total += msPerSecond
		b.Observe(model.Cost{At: start.Add(time.Duration(i) * time.Second), CPUMs: total})
	}
}

func TestUnderBudgetKeepsBasePeriods(t *testing.T) {
	b := NewBudget(50, baseTiers())
	feed(b, time.Now(), 15, 6)

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

func TestOverBudgetDegradesSpaceFirst(t *testing.T) {
	b := NewBudget(50, baseTiers())
	feed(b, time.Now(), 15, 80)

	if got := b.Period(model.TierSpace); got != 10*time.Second {
		t.Fatalf("space period = %v, want 10s: the least valuable tier slows first", got)
	}
	if got := b.Period(model.TierRequests); got != time.Second {
		t.Fatalf("requests period = %v, want 1s untouched at level 1: the grid is the tool", got)
	}
	if _, _, msg := b.State(); msg == "" {
		t.Fatal("every change must be announced in the status bar")
	}
}

func TestStillOverBudgetDegradesCountersThenRequests(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	feed(b, now, 15, 80)
	feed(b, now.Add(15*time.Second), 15, 80)
	if got := b.Period(model.TierCounters); got != 2*time.Second {
		t.Fatalf("counters period = %v, want 2s at level 2", got)
	}
	feed(b, now.Add(30*time.Second), 15, 80)
	if got := b.Period(model.TierRequests); got != 2*time.Second {
		t.Fatalf("requests period = %v, want 2s at level 3, the last thing to give", got)
	}
}

func TestRecoversOneStepAtATime(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	feed(b, now, 15, 80)
	feed(b, now.Add(15*time.Second), 15, 80)
	if _, level, _ := b.State(); level != 2 {
		t.Fatalf("level = %d, want 2 before recovery", level)
	}

	feed(b, now.Add(30*time.Second), 31, 5)
	if _, level, _ := b.State(); level != 1 {
		t.Fatalf("level = %d, want 1: recovery is one step per thirty quiet seconds, not a jump back", level)
	}
}

func TestCounterResetDoesNotThrottle(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	b.Observe(model.Cost{At: now, CPUMs: 1_000_000})
	b.Observe(model.Cost{At: now.Add(time.Second), CPUMs: 5})

	if _, level, _ := b.State(); level != 0 {
		t.Fatal("a reconnection resets cpu_time; a negative delta must be ignored, not read as free capacity or as a spike")
	}
}
