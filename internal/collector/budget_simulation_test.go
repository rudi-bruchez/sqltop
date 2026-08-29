package collector

import (
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// TestSimulationHealthyBurstyWorkloadNeverThrottles drives the budget
// through ten minutes of a realistic collection pattern: tier A and tier B
// (the request grid and the counters) running every second at a steady cost,
// tier C (space, tempdb, version store, scheduler detail) adding a burst
// every five seconds, and tier D (CPU history) adding a burst once a minute.
// True average consumption stays comfortably under the fifty ms/s budget;
// this must never throttle, because the mechanism the assertions on their
// own cannot show is whether the window smooths tier C's five-second rhythm
// or reacts to it as if it were sustained overload.
func TestSimulationHealthyBurstyWorkloadNeverThrottles(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()

	const (
		baseline = 20  // tier A + tier B combined, ms per second, every tick
		tierC    = 45  // ms, added every 5th tick
		tierD    = 120 // ms, added once every 60th tick
	)

	var sumMs int64
	const ticks = 600 // ten minutes at one sample per second
	for i := 1; i <= ticks; i++ {
		delta := int64(baseline)
		if i%5 == 0 {
			delta += tierC
		}
		if i%60 == 0 {
			delta += tierD
		}
		sumMs += delta
		total += delta
		at := now.Add(time.Duration(i) * time.Second)
		b.Observe(model.Cost{At: at, CPUMs: total})

		used, level, msg := b.State()
		if i%30 == 0 || level != 0 {
			t.Logf("tick %4d (+%4ds) delta=%3d used=%.1f level=%d msg=%q", i, i, delta, used, level, msg)
		}
		if level != 0 {
			t.Fatalf("tick %d: level = %d, want 0: a healthy bursty workload at %.0f%% of budget must not throttle",
				i, level, 100*float64(sumMs)/float64(ticks)/50)
		}
	}
	trueAverage := float64(sumMs) / float64(ticks)
	t.Logf("true average over %d ticks: %.1f ms/s (%.0f%% of the 50 ms/s budget)", ticks, trueAverage, 100*trueAverage/50)
	if trueAverage >= 50 {
		t.Fatalf("test setup: true average %.1f ms/s is not actually under budget", trueAverage)
	}
}

// TestSimulationSingleSpikeIsAbsorbed drives the budget through two minutes
// of near-idle load with one single one-second spike in the middle, sized so
// that diluted across the ten second window it still stays under budget.
// This is the case a round-trip or single-sample design cannot pass: a spike
// that big read as an instantaneous rate is nine times the limit.
func TestSimulationSingleSpikeIsAbsorbed(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()

	const (
		idle      = 2   // ms/s baseline, negligible
		spikeAt   = 30  // tick the spike lands on
		spikeSize = 450 // ms added on that single tick alone
	)

	for i := 1; i <= 120; i++ {
		delta := int64(idle)
		if i == spikeAt {
			delta += spikeSize
		}
		total += delta
		at := now.Add(time.Duration(i) * time.Second)
		b.Observe(model.Cost{At: at, CPUMs: total})

		used, level, msg := b.State()
		if i == spikeAt || (i > spikeAt && i < spikeAt+12) || level != 0 {
			t.Logf("tick %3d (+%3ds) delta=%3d used=%.1f level=%d msg=%q", i, i, delta, used, level, msg)
		}
		if level != 0 {
			t.Fatalf("tick %d: level = %d, want 0: a single spike diluted across a ten second window must not throttle", i, level)
		}
	}
}

// TestSimulationSustainedOverloadEscalatesThenRecovers drives the budget
// through a genuine sustained overload, five times the limit for ninety
// seconds, then a long quiet period. It must escalate through all three
// levels and, once actually quiet, give every step back.
func TestSimulationSustainedOverloadEscalatesThenRecovers(t *testing.T) {
	b := NewBudget(50, baseTiers())
	var total int64
	now := time.Now()

	at := now
	lastLevel := 0
	for i := 1; i <= 90; i++ {
		total += 250
		at = at.Add(time.Second)
		b.Observe(model.Cost{At: at, CPUMs: total})
		if used, level, msg := b.State(); level != lastLevel {
			t.Logf("overload tick %3d (+%3ds) used=%.1f level=%d msg=%q", i, i, used, level, msg)
			lastLevel = level
		}
	}
	if _, level, _ := b.State(); level != maxLevel {
		t.Fatalf("level = %d after 90s of sustained overload, want %d (max)", level, maxLevel)
	}

	lastLevel = maxLevel
	for i := 1; i <= 200; i++ {
		total += 2
		at = at.Add(time.Second)
		b.Observe(model.Cost{At: at, CPUMs: total})
		if used, level, msg := b.State(); level != lastLevel {
			t.Logf("recovery tick %3d (+%3ds) used=%.1f level=%d msg=%q", i, i, used, level, msg)
			lastLevel = level
		}
		if lastLevel == 0 {
			break
		}
	}
	if _, level, _ := b.State(); level != 0 {
		t.Fatalf("level = %d after a long quiet period, want 0: sustained overload must fully recover once it genuinely stops", level)
	}
}
