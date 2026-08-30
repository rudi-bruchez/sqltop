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

	// escalateCooldown is the minimum spacing between two steps up, after
	// the first. It is not derived from recoveryPeriod: raising that to
	// damp oscillation on the way down must not silently make escalation
	// slower to protect the server too, which is the opposite of what it
	// is for.
	//
	// The constraint is that a step's own effect has to become visible
	// before another step is justified. Doubling a tier's period only
	// changes future samples, and that change fully displaces the old rate
	// from the window only once the window has turned over past a sample
	// taken at the new, slower cadence. So it is the window plus one
	// throttled period of the slowest tier this throttles, which is the
	// space tier.
	//
	// It is a field rather than a constant because config.Tiers.Space is
	// configurable, and a constant baked from the 5 s default stopped
	// outlasting the delay it was derived from as soon as anybody set the
	// space tier slower. It was written down as debt in exactly those
	// words, and an external reviewer read the note and asked why it was
	// still a constant. Fair question.
	escalateCooldown time.Duration
}

// NewBudget builds a Budget that throttles once the tool's own server CPU,
// averaged over a sliding ten second window, exceeds limitMsPerSecond,
// falling back to base for every tier's period while it stays inside it.
func NewBudget(limitMsPerSecond int, base config.Tiers) *Budget {
	// One throttled space period past the window, computed from the space
	// period this Budget was actually given. See the field's own comment.
	return &Budget{
		limit:            float64(limitMsPerSecond),
		base:             base,
		escalateCooldown: budgetWindow + 2*base.Space.Std(),
	}
}

// Observe takes the cumulative cost of the tool's own session, as read from
// the server, differentiates it into a reading, and folds that reading into
// the sliding window the throttle decides from.
//
// Debt: c.LogicalReads is read from the server on every counters tick (see
// mssql.Source.Cost) and never looked at, here or anywhere else. Spec
// section 10 defines the observation budget in server CPU milliseconds
// only, which is what this whole type throttles on, so there was never a
// second budget for it to feed. The way out, if a second cost dimension is
// ever wanted, is a twin of windowAverage differentiating LogicalReads the
// same way CPUMs already is, surfaced next to CostMsPerSecond in
// collector.Status; until then it stays collected but unconsumed rather
// than dropped from model.Cost, since Source.Cost's own round trip already
// reads it as part of the same row and dropping the field would be a wire
// change for nothing.
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
		if b.level < maxLevel && (b.lastEscalate.IsZero() || now.Sub(b.lastEscalate) >= b.escalateCooldown) {
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

// SetBase changes a tier's base period while the tool is running: spec
// section 7's f command. It moves the value the throttle multiplies, never
// the throttle's own decision, so a period chosen by hand is still halved
// when the budget is exceeded rather than overriding the one control that
// protects the monitored server.
//
// escalateCooldown is recomputed because it is derived from the space
// period; leaving it behind would silently outlast the delay it was
// derived from, which is the mistake its own comment records.
func (b *Budget) SetBase(tier model.Tier, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch tier {
	case model.TierRequests:
		b.base.Requests = config.Duration(d)
	case model.TierCounters:
		b.base.Counters = config.Duration(d)
	case model.TierSpace:
		b.base.Space = config.Duration(d)
	case model.TierCPUHistory:
		b.base.CPUHistory = config.Duration(d)
	default:
		panic(fmt.Sprintf("collector: unknown tier %v", tier))
	}
	b.escalateCooldown = budgetWindow + 2*b.base.Space.Std()
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
