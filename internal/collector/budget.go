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
	// recoveryPeriod is how long the tool must stay quiet before giving back
	// one step. Slow on purpose, so the rate does not oscillate.
	recoveryPeriod = 30 * time.Second
	// escalateCooldown is the minimum spacing between two steps up. It reacts
	// faster than recovery, half its period, because a step up protects the
	// server while a step down is a comfort the tool can afford to be
	// cautious about. The very first step up is never subject to it: a tool
	// that is already over budget the moment it can tell must react at once.
	escalateCooldown = recoveryPeriod / 2
	maxLevel         = 3
)

// Budget turns the tool's own cost into a throttle level.
//
// The cost is server CPU, differentiated from cpu_time of the tool's own
// session. It is never a round trip: that would carry network latency, so a
// healthy server on the other side of a WAN would throttle the tool while a
// saturated local server slipped past. Spec section 10, corrected.
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
	used  float64 // most recently observed rate, ms of server CPU per second

	prev   model.Cost
	seeded bool

	lastEscalate time.Time // when the level last went up
	quietFrom    time.Time // when the current unbroken quiet streak began
}

// NewBudget builds a Budget that throttles once the tool's own server CPU
// exceeds limitMsPerSecond, falling back to base for every tier's period
// while it stays inside the budget.
func NewBudget(limitMsPerSecond int, base config.Tiers) *Budget {
	return &Budget{limit: float64(limitMsPerSecond), base: base}
}

// Observe takes the cumulative cost of the tool's own session, as read from
// the server, and differentiates it into a rate that drives the throttle.
func (b *Budget) Observe(c model.Cost) {
	b.mu.Lock()
	defer b.mu.Unlock()

	prev, seeded := b.prev, b.seeded
	b.prev, b.seeded = c, true
	if !seeded {
		return // one cumulative sample carries no rate
	}

	elapsed := c.At.Sub(prev.At).Seconds()
	if elapsed <= 0 || c.CPUMs < prev.CPUMs {
		// The session reconnected, so cpu_time restarted at zero. The delta
		// would read as a burst of free capacity if trusted; skip the sample
		// instead of inventing a rate from it.
		return
	}

	b.used = float64(c.CPUMs-prev.CPUMs) / elapsed
	b.reviseLocked(prev.At, c.At)
}

// reviseLocked moves the level by at most one step per call. Degradation
// order is deliberate: the space tier is the least valuable, the request
// grid is the tool itself and gives last.
//
// A transition's reference timestamp is the start of the interval that
// produced the rate (intervalStart), not the moment the reading arrived
// (now): the rate is an average over that whole interval, so the condition
// it reports is only known to hold as far back as intervalStart. At one
// sample a second this is why a full recoveryPeriod of quiet needs one more
// sample than its second count: the first quiet sample only proves the
// second ending at itself, and it takes intervalStart, from the sample
// before it, to start the clock.
func (b *Budget) reviseLocked(intervalStart, now time.Time) {
	if b.used > b.limit {
		b.quietFrom = time.Time{}
		if b.level < maxLevel && (b.lastEscalate.IsZero() || now.Sub(b.lastEscalate) >= escalateCooldown) {
			b.level++
			b.lastEscalate = now
			b.msg = fmt.Sprintf("observation budget exceeded (%.0f ms/s of server CPU, limit %.0f): %s",
				b.used, b.limit, degradedAt(b.level))
		}
		return
	}

	if b.level == 0 {
		b.quietFrom = time.Time{}
		return
	}
	if b.quietFrom.IsZero() {
		b.quietFrom = intervalStart
		return
	}
	if now.Sub(b.quietFrom) >= recoveryPeriod {
		b.level--
		b.quietFrom = intervalStart
		if b.level == 0 {
			b.lastEscalate = time.Time{}
			b.msg = "back inside the observation budget, full refresh rate restored"
		} else {
			b.msg = fmt.Sprintf("recovering: %s", degradedAt(b.level))
		}
	}
}

func degradedAt(level int) string {
	switch level {
	case 1:
		return "space tier slowed to half rate"
	case 2:
		return "space and counter tiers slowed to half rate"
	default:
		return "all tiers slowed to half rate, including the request grid"
	}
}

// degradedFrom names the level at which each tier starts giving. Space and
// the long CPU history both give at the first step since neither is asked
// for on every tick; counters give next; the request grid, which is the
// tool's own reason to exist, gives last.
var degradedFrom = map[model.Tier]int{
	model.TierSpace:      1,
	model.TierCPUHistory: 1,
	model.TierCounters:   2,
	model.TierRequests:   3,
}

// Period returns the interval a tier should currently use. On-demand work,
// such as plan retrieval, never asks: it only happens when a human requested
// it, and spec section 10 keeps it out of the polling loop entirely.
func (b *Budget) Period(tier model.Tier) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	base := b.baseFor(tier)
	if b.level >= degradedFrom[tier] {
		return base * 2
	}
	return base
}

func (b *Budget) baseFor(tier model.Tier) time.Duration {
	switch tier {
	case model.TierRequests:
		return b.base.Requests.Std()
	case model.TierCounters:
		return b.base.Counters.Std()
	case model.TierSpace:
		return b.base.Space.Std()
	case model.TierCPUHistory:
		return b.base.CPUHistory.Std()
	default:
		return 0
	}
}

// State reports the throttle for the status bar: the most recently observed
// rate, the current level, and a human message describing the last change,
// empty until the budget has moved at least once.
func (b *Budget) State() (usedMsPerSecond float64, level int, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.level, b.msg
}
