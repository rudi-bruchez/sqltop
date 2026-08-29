// Package collector schedules sampling and keeps the tool inside its
// observation budget.
package collector

import (
	"fmt"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

const (
	// budgetWindow is the sliding window spec section 10 measures
	// consumption over. A single inter-sample delta is not consumption: tier
	// C alone (tempdb space, version store, memory clerks, scheduler detail)
	// is a five-second burst by construction, and tier D adds a once-a-minute
	// spike on top of it. Reacting to one delta throttles the tool's own
	// sampling rhythm instead of the server's actual load.
	budgetWindow = 10 * time.Second

	// recoveryPeriod is how long the window average must stay inside the
	// budget before giving back one step. Slow on purpose, so the rate does
	// not oscillate.
	recoveryPeriod = 30 * time.Second

	// escalateCooldown is the minimum spacing between two steps up, after
	// the first. It is not derived from recoveryPeriod: raising that to damp
	// oscillation on the way down must not silently make escalation slower
	// to protect the server too, which is the opposite of what it is for.
	// The real constraint is that a step's own effect has to become visible
	// before another step is justified: doubling a tier's period only
	// changes future samples, and that change only fully displaces the old
	// rate from the window once the window has turned over past a sample
	// taken at the new, slower cadence. The space tier gives first and has
	// the longest base period this throttles, five seconds, doubled to ten;
	// a window turning over after that is twenty seconds, with no margin
	// borrowed from any other constant.
	escalateCooldown = budgetWindow + 10*time.Second

	maxLevel = 3
)

// interval is one differentiated reading: the tool's own server CPU cost
// between two consecutive Observe calls, kept for as long as it falls inside
// budgetWindow.
type interval struct {
	start time.Time
	end   time.Time
	ms    float64
}

// Budget turns the tool's own cost into a throttle level.
//
// The cost is server CPU, differentiated from cpu_time of the tool's own
// session. It is never a round trip: that would carry network latency, so a
// healthy server on the other side of a WAN would throttle the tool while a
// saturated local server slipped past. The decision itself is taken over a
// sliding ten second window, not a single reading, because tier C and tier D
// are bursty by construction and a single burst is not sustained load. Spec
// section 10.
//
// Observe is called from the goroutine that samples the tool's own session
// cost (the counters tier, which already visits sys.dm_exec_sessions once a
// second). Period is called from each tier's own goroutine to decide its
// next interval, and State is called from whatever renders the status bar.
// That is more than one goroutine touching shared state, so it is behind one
// mutex, held only for the arithmetic, never across a channel send or a
// network call.
type Budget struct {
	mu sync.Mutex

	limit float64
	base  config.Tiers

	level int
	msg   string

	window []interval // trailing budgetWindow of differentiated readings

	prev   model.Cost
	seeded bool

	lastEscalate time.Time // when the level last went up
	quietFrom    time.Time // when the current unbroken quiet streak began
}

// NewBudget builds a Budget that throttles once the tool's own server CPU,
// averaged over a sliding ten second window, exceeds limitMsPerSecond,
// falling back to base for every tier's period while it stays inside it.
func NewBudget(limitMsPerSecond int, base config.Tiers) *Budget {
	return &Budget{limit: float64(limitMsPerSecond), base: base}
}

// Observe takes the cumulative cost of the tool's own session, as read from
// the server, differentiates it into a reading, and folds that reading into
// the sliding window the throttle decides from.
func (b *Budget) Observe(c model.Cost) {
	b.mu.Lock()
	defer b.mu.Unlock()

	prev, seeded := b.prev, b.seeded
	b.prev, b.seeded = c, true
	if !seeded {
		return // one cumulative sample carries no rate
	}

	if c.At.Sub(prev.At) <= 0 || c.CPUMs < prev.CPUMs {
		// The session reconnected, so cpu_time restarted at zero. The delta
		// would read as a burst of free capacity if trusted; skip the
		// sample rather than fold a negative cost into the window.
		return
	}

	b.window = append(b.window, interval{start: prev.At, end: c.At, ms: float64(c.CPUMs - prev.CPUMs)})
	cutoff := c.At.Add(-budgetWindow)
	for len(b.window) > 0 && !b.window[0].end.After(cutoff) {
		b.window = b.window[1:]
	}

	b.reviseLocked(c.At)
}

// windowAverage is the server CPU milliseconds per second across the
// currently kept window, time-weighted rather than a plain mean of readings,
// since nothing requires Observe to be called at an exact one second
// cadence.
func (b *Budget) windowAverage() (float64, bool) {
	if len(b.window) == 0 {
		return 0, false
	}
	var ms, secs float64
	for _, iv := range b.window {
		ms += iv.ms
		secs += iv.end.Sub(iv.start).Seconds()
	}
	if secs <= 0 {
		return 0, false
	}
	return ms / secs, true
}

// reviseLocked moves the level by at most one step per call. Degradation
// order is deliberate: tier C is the least valuable, tier A is the request
// grid the tool exists to show and gives last. Tier D never gives at all; see
// degradedFrom.
//
// A transition's reference timestamp is deliberately not always "now". When
// the window first drops back under the limit, the earliest instant that
// average has evidence for is the start of its oldest surviving reading, so
// quiet is credited from there rather than from the moment the average
// crossed the line, which the average itself already vouches for. Once a
// recovery step actually fires, that credit is spent: the next step's clock
// starts fresh at the moment of the step, not backdated again, or every step
// after the first would complete in less than a full recoveryPeriod.
func (b *Budget) reviseLocked(now time.Time) {
	avg, ok := b.windowAverage()
	if !ok {
		return
	}

	if avg > b.limit {
		b.quietFrom = time.Time{}
		if b.level < maxLevel && (b.lastEscalate.IsZero() || now.Sub(b.lastEscalate) >= escalateCooldown) {
			b.level++
			b.lastEscalate = now
			b.msg = fmt.Sprintf("observation budget exceeded (%.0f ms/s of server CPU over the last %s, limit %.0f): %s",
				avg, budgetWindow, b.limit, degradedAt(b.level))
		}
		return
	}

	if b.level == 0 {
		b.quietFrom = time.Time{}
		return
	}
	if b.quietFrom.IsZero() {
		b.quietFrom = b.window[0].start
		return
	}
	if now.Sub(b.quietFrom) >= recoveryPeriod {
		b.level--
		b.quietFrom = now
		// Share the hysteresis with escalation: a burst right after a
		// recovery step, including the step back to zero, must not walk the
		// level straight back up before its own cooldown has had a chance
		// to pass.
		b.lastEscalate = now
		if b.level == 0 {
			b.msg = "back inside the observation budget, full refresh rate restored"
		} else {
			b.msg = fmt.Sprintf("recovering: %s", degradedAt(b.level))
		}
	}
}

func degradedAt(level int) string {
	switch level {
	case 1:
		return "tier C (space, tempdb, version store) slowed to half rate"
	case 2:
		return "tier C and tier B (performance counters) slowed to half rate"
	default:
		return "tier C, tier B and tier A (the request grid) all slowed to half rate"
	}
}

// Period returns the interval a tier should currently use. On-demand work,
// such as plan retrieval, never asks: it only happens when a human requested
// it, and spec section 10 keeps it out of the polling loop entirely.
func (b *Budget) Period(tier model.Tier) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	base := baseFor(b.base, tier)
	threshold, throttled := degradedFrom(tier)
	if throttled && b.level >= threshold {
		return base * 2
	}
	return base
}

// baseFor and degradedFrom together are the one place spec section 10's
// degradation order (tier C, then tier B, then tier A) and its exception
// (tier D is never throttled) are expressed. Both panic on a tier outside
// model.Tier's four values: the enum is closed, so reaching the default case
// is a programming error to surface loudly, not a runtime possibility to
// degrade gracefully from (a silent zero-value period would panic anyway,
// one layer up, the first time it reached time.NewTicker).
func baseFor(base config.Tiers, tier model.Tier) time.Duration {
	switch tier {
	case model.TierRequests:
		return base.Requests.Std()
	case model.TierCounters:
		return base.Counters.Std()
	case model.TierSpace:
		return base.Space.Std()
	case model.TierCPUHistory:
		return base.CPUHistory.Std()
	default:
		panic(fmt.Sprintf("collector: unknown tier %v", tier))
	}
}

func degradedFrom(tier model.Tier) (level int, throttled bool) {
	switch tier {
	case model.TierSpace: // tier C
		return 1, true
	case model.TierCounters: // tier B
		return 2, true
	case model.TierRequests: // tier A
		return 3, true
	case model.TierCPUHistory: // tier D
		// Spec section 10's degradation order is C, then B, then A. Tier D
		// is not in it: the engine only produces it once a minute regardless
		// of how often the tool asks, so halving that period saves nothing
		// and only starves a chart the operator is looking at.
		return 0, false
	default:
		panic(fmt.Sprintf("collector: unknown tier %v", tier))
	}
}

// State reports the throttle for the status bar: the server CPU currently
// averaged over the sliding window, the level, and a message describing the
// last change, empty until the budget has moved at least once.
func (b *Budget) State() (usedMsPerSecond float64, level int, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	avg, _ := b.windowAverage()
	return avg, b.level, b.msg
}
