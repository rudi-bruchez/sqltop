// Package collector schedules sampling and keeps the tool inside its
// observation budget.
package collector

import (
	"context"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// minBackoff and maxBackoff bound the retry interval a tier goroutine falls
// back to once its source calls start failing. Spec section 4.4: a dropped
// connection is normal, not exceptional, and the collector must keep asking
// without hammering the network.
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
type Collector struct {
	src source.Source
	win *window.Window
	bud *Budget

	mu          sync.RWMutex
	figures     map[string]model.Figure
	status      Status
	identifying bool // guards concurrent re-preflight attempts, see reidentify
}

func New(src source.Source, w *window.Window, b *Budget) *Collector {
	return &Collector{src: src, win: w, bud: b, figures: map[string]model.Figure{}}
}

// Run blocks until ctx is done, driving one goroutine per tier. It returns
// ctx.Err() once every tier goroutine has actually returned, not merely been
// asked to.
func (c *Collector) Run(ctx context.Context) error {
	// Identify is run once up front so the server tiers build their queries
	// from a real version and real capabilities rather than guessing. If it
	// fails here, the tiers still start: SampleRequests works on a
	// conservative query even without Identify, and each tier retries the
	// preflight itself once it notices the tool is disconnected, in sample.
	info, caps, err := c.src.Identify(ctx)
	c.mu.Lock()
	c.status = Status{Connected: err == nil, Info: info, Caps: caps}
	if err != nil {
		c.status.Message = "identify: " + err.Error()
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
// tier's own (possibly sub-second) period: from one second, doubling, up to
// a thirty second ceiling, resetting to zero the moment a sample succeeds
// again. Spec section 4.4.
func (c *Collector) loop(ctx context.Context, tier model.Tier) {
	var backoff time.Duration
	for {
		start := time.Now()
		ok := c.sample(ctx, tier)

		var wait time.Duration
		if ok {
			backoff = 0
			wait = c.bud.Period(tier) - time.Since(start)
			if wait < 0 {
				wait = 0
			}
		} else {
			if backoff == 0 {
				backoff = minBackoff
			} else if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
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
	// A tier waking up while the tool is disconnected re-runs the preflight
	// before doing anything else: the server may have come back as a
	// different version, or with different rights, after a failover. Spec
	// section 4.4.
	if c.disconnected() {
		c.reidentify(ctx)
	}

	if tier == model.TierRequests {
		rows, err := c.src.SampleRequests(ctx)
		if err != nil {
			c.fail("sampling requests: " + err.Error())
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
		c.ok()
		return true
	}

	sample, err := c.src.SampleServer(ctx, tier)
	if err != nil {
		c.fail("sampling " + tier.String() + ": " + err.Error())
		return false
	}
	c.mu.Lock()
	for k, v := range sample.Figures {
		c.figures[k] = v
	}
	c.mu.Unlock()
	c.ok()

	if tier != model.TierCounters {
		return true
	}

	// The tool's own cost is read from the same goroutine and the same
	// once-a-second cadence that already visits sys.dm_exec_sessions for the
	// counters tier, rather than from a goroutine of its own: see the doc
	// comment on Budget.Observe in budget.go. Spec section 10.
	cost, err := c.src.Cost(ctx)
	if err != nil {
		// Never swallowed: without this reading, the budget stops updating
		// and the throttle stops reacting, which must be visible rather
		// than silent.
		c.fail("reading own cost: " + err.Error())
		return false
	}
	c.bud.Observe(cost)
	return true
}

func (c *Collector) disconnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.status.Connected
}

// reidentify re-runs the preflight. Several tiers can notice the tool is
// disconnected in the same instant; identifying makes only the first of them
// actually call into the source, since Identify is a network round trip and
// the others gain nothing by repeating it in the same breath. The call to
// the source itself happens with no lock held.
func (c *Collector) reidentify(ctx context.Context) {
	c.mu.Lock()
	if c.identifying {
		c.mu.Unlock()
		return
	}
	c.identifying = true
	c.mu.Unlock()

	info, caps, err := c.src.Identify(ctx)

	c.mu.Lock()
	c.identifying = false
	if err == nil {
		c.status.Info = info
		c.status.Caps = caps
	}
	c.mu.Unlock()
}

func (c *Collector) ok() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = true
	if _, level, msg := c.bud.State(); level > 0 {
		c.status.Message = msg
	} else {
		c.status.Message = ""
	}
}

func (c *Collector) fail(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = false
	c.status.Message = msg
}

// Server returns the merged latest figures across every server tier.
func (c *Collector) Server() model.ServerSample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := model.ServerSample{At: time.Now(), Figures: make(map[string]model.Figure, len(c.figures))}
	for k, v := range c.figures {
		out.Figures[k] = v
	}
	return out
}

func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}
