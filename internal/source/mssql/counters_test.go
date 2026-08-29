package mssql

import (
	"math"
	"testing"
	"time"
)

func TestFirstSampleIsUnavailableNotZero(t *testing.T) {
	s := newCounterState()
	got := s.apply(time.Now(), map[string]int64{"batch_requests_sec": 1000})

	if f := got["batch_requests_sec"]; f.Available {
		t.Fatal("a rate needs two samples; the first tick must report unavailable rather than a zero that reads as an idle server")
	}
}

func TestPerSecondIsADelta(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"batch_requests_sec": 1250})

	f := got["batch_requests_sec"]
	if !f.Available {
		t.Fatal("the second sample must produce a value")
	}
	if math.Abs(f.Value-250) > 0.01 {
		t.Fatalf("rate = %v, want 250 per second", f.Value)
	}
}

func TestPerSecondScalesByElapsedTime(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1000})
	got := s.apply(t0.Add(5*time.Second), map[string]int64{"batch_requests_sec": 2000})

	if f := got["batch_requests_sec"]; math.Abs(f.Value-200) > 0.01 {
		t.Fatalf("rate = %v, want 200 per second over five seconds, not the raw 1000 delta", f.Value)
	}
}

func TestRatioUsesItsBase(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"buffer_cache_hit_ratio": 900, "buffer_cache_hit_ratio__base": 1000})
	// Ninety hits out of a hundred lookups in the last interval.
	got := s.apply(t0.Add(time.Second), map[string]int64{"buffer_cache_hit_ratio": 990, "buffer_cache_hit_ratio__base": 1100})

	f := got["buffer_cache_hit_ratio"]
	if !f.Available {
		t.Fatal("the ratio must be available on the second sample")
	}
	if math.Abs(f.Value-90) > 0.01 {
		t.Fatalf("ratio = %v, want 90 percent for the interval, not the lifetime figure", f.Value)
	}
}

func TestRatioWithNoActivityIsUnavailable(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"buffer_cache_hit_ratio": 900, "buffer_cache_hit_ratio__base": 1000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"buffer_cache_hit_ratio": 900, "buffer_cache_hit_ratio__base": 1000})

	if f := got["buffer_cache_hit_ratio"]; f.Available {
		t.Fatal("no lookups in the interval means no ratio; reporting zero percent would claim every read missed the cache")
	}
}

func TestRawPassesThrough(t *testing.T) {
	s := newCounterState()
	got := s.apply(time.Now(), map[string]int64{"page_life_expectancy": 4200})

	f := got["page_life_expectancy"]
	if !f.Available || math.Abs(f.Value-4200) > 0.01 {
		t.Fatalf("raw counter = %+v, want 4200 available on the first sample", f)
	}
}

func TestCounterResetIsNotGarbage(t *testing.T) {
	// The instance restarted between two ticks, so the cumulative counter
	// went backwards. A negative rate is nonsense and must not be shown.
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1_000_000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"batch_requests_sec": 12})

	if f := got["batch_requests_sec"]; f.Available {
		t.Fatal("a counter that went backwards means a restart; the tick must be skipped, not reported as a negative rate")
	}
}

func TestEveryDefinitionHasAKindAndAUnit(t *testing.T) {
	for _, d := range counterDefs {
		if d.key == "" || d.object == "" || d.name == "" {
			t.Errorf("incomplete definition: %+v", d)
		}
		if d.kind == kindRatio && d.baseName == "" {
			t.Errorf("%s is a ratio and must name its base counter", d.key)
		}
	}
}

func TestCatalogueKeyAbsentFromReadingIsStillPresent(t *testing.T) {
	// A figure this server does not produce must appear in the output marked
	// unavailable, with its unit intact, rather than being omitted: the
	// interface has to tell "not supported here" from "not sampled yet".
	s := newCounterState()
	got := s.apply(time.Now(), map[string]int64{})

	f, ok := got["page_life_expectancy"]
	if !ok {
		t.Fatal("key missing entirely; an absent figure must still have a key")
	}
	if f.Available {
		t.Fatal("a figure absent from the reading cannot be available")
	}
	if f.Unit != "s" {
		t.Fatalf("unit = %q, want \"s\" so the interface can render a placeholder with its unit", f.Unit)
	}
}

func TestGenuineZeroRateIsAvailable(t *testing.T) {
	// The counter did not move but time did. That is a real zero, not a
	// missing measurement, and the two must not look the same.
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 5000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"batch_requests_sec": 5000})

	f := got["batch_requests_sec"]
	if !f.Available {
		t.Fatal("an idle server produces a real zero rate, which must be available")
	}
	if f.Value != 0 {
		t.Fatalf("rate = %v, want 0", f.Value)
	}
}

func TestCounterThatVanishesAndReturnsDoesNotInventARate(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1000})
	s.apply(t0.Add(time.Second), map[string]int64{"batch_requests_sec": 1100})
	// The counter disappears from one reading.
	s.apply(t0.Add(2*time.Second), map[string]int64{})
	// And comes back much later, far ahead of where it was.
	got := s.apply(t0.Add(3*time.Second), map[string]int64{"batch_requests_sec": 9000})

	if f := got["batch_requests_sec"]; f.Available {
		t.Fatalf("rate = %v reported as available; a counter that lost its history must report unavailable for one tick rather than differentiate against a stale value", f.Value)
	}
}
