// Package window keeps the rolling history the whole interface reads from.
package window

import (
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

type tick struct {
	at   time.Time
	rows []model.RequestSample
}

// Window holds recent ticks, bounded both by age and by total sample count.
// One mutex, no cleverness: the tool waits on the network, not on this.
type Window struct {
	mu        sync.RWMutex
	ticks     []tick
	samples   int
	retention time.Duration
	maxSample int
	capped    bool
}

func New(retention time.Duration, maxSamples int) *Window {
	return &Window{retention: retention, maxSample: maxSamples}
}

// Append takes ownership of rows: the window keeps the slice rather than
// copying it, so the caller must not touch it afterwards. The collector
// satisfies this by passing a freshly built slice on every tick.
func (w *Window) Append(at time.Time, rows []model.RequestSample) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ticks = append(w.ticks, tick{at: at, rows: rows})
	w.samples += len(rows)
	w.evictLocked(at)
}

// evictLocked drops the oldest ticks until both bounds are satisfied. Age
// first, because that is the bound the user asked for; the count cap is the
// safety net that keeps memory bounded on a busy server.
func (w *Window) evictLocked(now time.Time) {
	cutoff := now.Add(-w.retention)
	drop := 0
	for drop < len(w.ticks) && w.ticks[drop].at.Before(cutoff) {
		w.samples -= len(w.ticks[drop].rows)
		drop++
	}

	w.capped = false
	for drop < len(w.ticks) && w.samples > w.maxSample {
		w.samples -= len(w.ticks[drop].rows)
		drop++
		w.capped = true
	}

	if drop > 0 {
		w.ticks = append([]tick(nil), w.ticks[drop:]...)
	}
}

// Latest returns a copy. Handing out the live backing slice would let any
// caller corrupt the window by sorting or mutating in place, which would
// undermine the mutex that makes this structure safe in the first place.
func (w *Window) Latest() []model.RequestSample {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.ticks) == 0 {
		return nil
	}
	rows := w.ticks[len(w.ticks)-1].rows
	out := make([]model.RequestSample, len(rows))
	copy(out, rows)
	return out
}

func (w *Window) History(ref model.RequestRef) []model.RequestSample {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var out []model.RequestSample
	for _, t := range w.ticks {
		for _, r := range t.rows {
			if r.Ref == ref {
				out = append(out, r)
			}
		}
	}
	return out
}

// Depth reports what the window actually holds, which the status bar shows.
// capped is true when the sample cap, rather than the retention period, is
// what decided the oldest sample: the window is then shorter than asked and
// the user should be able to see that.
func (w *Window) Depth() (oldest time.Time, samples int, capped bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.ticks) == 0 {
		return time.Time{}, 0, false
	}
	return w.ticks[0].at, w.samples, w.capped
}
