// Package collector schedules sampling and keeps the tool inside its
// observation budget.
package collector

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// minBackoff and maxBackoff bound the retry interval a tier goroutine falls
// back to once its source calls start failing. Spec section 4.4: a dropped
// connection is normal, not exceptional, and the collector must keep asking
// without hammering the network. The floor a given tier actually uses is at
// least its own configured period, never the bare minBackoff below: a tier
// whose healthy cadence is slower than a second must not poll more often
// while broken than it does while working. See loop.
const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// allTiers is every tier the collector drives, one goroutine each. Requests
// is sampled through SampleRequests; the rest go through SampleServer.
var allTiers = []model.Tier{model.TierRequests, model.TierCounters, model.TierSpace, model.TierCPUHistory}

// Status is what the interface reads to render the status bar.
type Status struct {
	Connected bool
	Message   string
	Info      model.ServerInfo
	Caps      model.Capabilities
}

// Collector drives one Source: one goroutine per tier, each on its own
// period from Budget, feeding a shared retention window and a shared map of
// dashboard figures. Boring concurrency, spec section 2.1: no worker pool
// and no scheduler of our own, just goroutines and one mutex around the
// state several of them touch.
//
// Failure state is kept per tier rather than as one collector-wide flag: the
// four tiers fail independently (a permissions gap on one DMV does not mean
// the others stopped working), and a healthy tier's own success must not be
// allowed to erase a different tier's still-current error out of the status
// bar.
type Collector struct {
	src source.Source
	win *window.Window
	bud *Budget

	mu        sync.RWMutex
	figures   map[string]model.Figure
	figuresAt time.Time // stamp of the most recent SampleServer call that actually merged something

	info model.ServerInfo
	caps model.Capabilities

	identifyErr string                // message from the last Identify, cleared by the first tier to succeed since
	tierErr     map[model.Tier]string // one entry per tier currently failing; absent means healthy
	costErr     string                // message from the last failed Cost read, independent of tierErr
}

func New(src source.Source, w *window.Window, b *Budget) *Collector {
	return &Collector{src: src, win: w, bud: b, figures: map[string]model.Figure{}, tierErr: map[model.Tier]string{}}
}

// Run blocks until ctx is done, driving one goroutine per tier. It returns
// ctx.Err() once every tier goroutine has actually returned, not merely been
// asked to.
func (c *Collector) Run(ctx context.Context) error {
	// Identify is run once up front so the server tiers build their queries
	// from a real version and real capabilities rather than guessing. If it
	// fails here, the tiers still start: SampleRequests works on a
	// conservative query even without Identify.
	//
	// Debt against spec section 4.4: three of its sentences are not done.
	// The status bar carries no next-attempt time, the retention window is
	// never marked stale while disconnected, and this preflight is not
	// re-run on an actual reconnection (only the retry backoff in loop is
	// done). All three were left because an earlier attempt at the third
	// re-ran Identify on every failed retry rather than only on a genuine
	// reconnect, which loaded a server that was merely refusing queries
	// instead of protecting it, the very spin 4.4's backoff exists to
	// prevent. The way out is a real reconnect signal, for instance the
	// first tier success after a run of failures, that fires Identify once
	// and that a next-attempt time and a stale marker could also hang off;
	// nothing here tracks that signal yet.
	info, caps, err := c.src.Identify(ctx)
	c.mu.Lock()
	c.info, c.caps = info, caps
	if err != nil {
		c.identifyErr = "identify: " + err.Error()
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	for _, tier := range allTiers {
		wg.Add(1)
		go func(tier model.Tier) {
			defer wg.Done()
			c.loop(ctx, tier)
		}(tier)
	}
	wg.Wait()
	return ctx.Err()
}

// loop drives one tier for as long as ctx is alive. The period is re-read
// from Budget on every iteration, which is how a throttle decision reaches a
// tier already running. On failure it backs off instead of retrying at the
// tier's own period: starting from that period (floored at one second),
// doubling on each consecutive failure up to a thirty second ceiling (or the
// tier's own period again, if that is itself slower than thirty seconds),
// and resetting the moment a sample succeeds again. Spec section 4.4.
func (c *Collector) loop(ctx context.Context, tier model.Tier) {
	var backoff time.Duration
	for {
		start := time.Now()
		ok := c.sample(ctx, tier)
		period := c.bud.Period(tier)

		var wait time.Duration
		if ok {
			backoff = 0
			wait = period - time.Since(start)
			if wait < 0 {
				wait = 0
			}
		} else {
			floor := period
			if floor < minBackoff {
				floor = minBackoff
			}
			ceiling := maxBackoff
			if ceiling < floor {
				// A tier whose own period already exceeds the ceiling (tier
				// D's default is a minute) must not be pulled down to
				// thirty seconds: that would make it retry a broken source
				// more often than it ever samples a healthy one.
				ceiling = floor
			}
			if backoff == 0 {
				backoff = floor
			} else if backoff < ceiling {
				backoff *= 2
				if backoff > ceiling {
					backoff = ceiling
				}
			}
			wait = backoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// sample runs one tier's collection attempt and reports whether it
// succeeded, which is what loop uses to decide between the tier's normal
// period and a backoff.
func (c *Collector) sample(ctx context.Context, tier model.Tier) bool {
	// A source that ignores ctx mid-call cannot be interrupted once the call
	// is placed, but this at least stops the collector from placing one it
	// already knows is pointless, which matters most for the counters tier
	// below: it makes two calls a round, and the second must not go out
	// once shutdown has already been asked for.
	if ctx.Err() != nil {
		return false
	}

	if tier == model.TierRequests {
		rows, err := c.src.SampleRequests(ctx)
		if err != nil {
			c.fail(tier, "sampling requests: "+err.Error())
			return false
		}
		// The tick is stamped with the sample's own time, not the
		// collector's. Two clocks would drift apart under load, and the one
		// that matters for age eviction is the one the rows carry.
		at := time.Now()
		if len(rows) > 0 && !rows[0].At.IsZero() {
			at = rows[0].At
		}
		// Flattening is engine-neutral, so it happens here rather than in
		// any source. Spec section 4.
		c.win.Append(at, window.Flatten(rows))
		c.ok(tier)
		return true
	}

	sample, err := c.src.SampleServer(ctx, tier)
	ok := err == nil
	if err != nil {
		c.fail(tier, "sampling "+tier.String()+": "+err.Error())
	} else {
		at := sample.At
		if at.IsZero() {
			at = time.Now()
		}
		c.mu.Lock()
		for k, v := range sample.Figures {
			c.figures[k] = v
		}
		c.figuresAt = at
		c.mu.Unlock()
		c.ok(tier)
	}

	if tier == model.TierCounters && ctx.Err() == nil {
		// The tool's own cost is read on the same goroutine and the same
		// cadence that already visits sys.dm_exec_sessions for the counters
		// tier, rather than from a goroutine of its own: see the Budget
		// type's doc comment in budget.go. It is read unconditionally, not
		// only when SampleServer above succeeded: the two are separate
		// queries on the same connection, and the budget must not go blind
		// just because one of them failed this round. Nor does a failure
		// here override what SampleServer already decided about the tier's
		// own success, in either direction; it is its own signal.
		cost, costErr := c.src.Cost(ctx)
		if costErr != nil {
			// Never swallowed: without this reading, the budget stops
			// updating and the throttle stops reacting, which must be
			// visible rather than silent.
			c.failCost(costErr)
		} else {
			c.bud.Observe(cost)
			c.okCost()
		}
	}

	return ok
}

func (c *Collector) fail(tier model.Tier, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tierErr[tier] = msg
}

func (c *Collector) ok(tier model.Tier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tierErr, tier)
	// Any tier succeeding is evidence the source answers now, which makes a
	// stale complaint from the one-off preflight at the top of Run obsolete
	// even though that preflight itself never runs again.
	c.identifyErr = ""
}

func (c *Collector) failCost(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.costErr = "reading own cost: " + err.Error()
}

func (c *Collector) okCost() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.costErr = ""
}

// Server returns the merged latest figures across every server tier, stamped
// with the time of the most recent sample that actually contributed to them
// rather than the current instant: a tier that has been failing for a while
// must not have its last good figures re-labelled as fresh on every call.
//
// That stamp is still one timestamp for the whole map, not one per figure:
// counters, space and CPU history run on different periods, so a figure from
// the five-second tier can be a few seconds staler than one from the
// one-second tier even when both are healthy, and this cannot tell them
// apart. Doing better needs a timestamp on model.Figure itself, which it
// does not carry; that is a UI-plan change, not a collector one.
//
// At is the zero time.Time until the first server-tier sample has actually
// succeeded. Nothing downstream reads it yet, but a caller computing an age
// from a zero time would render an age of about two thousand years, so this
// is written down for whoever adds that reader.
func (c *Collector) Server() model.ServerSample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := model.ServerSample{At: c.figuresAt, Figures: make(map[string]model.Figure, len(c.figures))}
	for k, v := range c.figures {
		out.Figures[k] = v
	}
	return out
}

func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Status{
		Connected: c.identifyErr == "" && len(c.tierErr) == 0,
		Message:   c.messageLocked(),
		Info:      c.info,
		Caps:      c.caps,
	}
}

// messageLocked assembles the status bar text from whatever is currently
// wrong, in order of how urgent it is: the initial preflight, then each
// tier's own error in a fixed order, then the cost reader, and only once all
// of that is silent does a budget throttle get to explain itself. Must be
// called with mu held, at least for reading.
func (c *Collector) messageLocked() string {
	var parts []string
	if c.identifyErr != "" {
		parts = append(parts, c.identifyErr)
	}
	for _, t := range allTiers {
		if msg, failing := c.tierErr[t]; failing {
			parts = append(parts, msg)
		}
	}
	if c.costErr != "" {
		parts = append(parts, c.costErr)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}
	if _, level, msg := c.bud.State(); level > 0 {
		return msg
	}
	return ""
}
