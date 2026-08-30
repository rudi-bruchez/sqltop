// Package window keeps the rolling history the whole interface reads from.
package window

import (
	"sort"
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

// SessionStatements is what one session has been seen doing over the whole
// window, grouped by statement. It costs the monitored server nothing: every
// sample it reads is already here, which is the point of keeping a window at
// all and, until this, the part of it nothing reached.
//
// Statements are identified by what makes one distinct on a session: the
// text, and the login, host and program that ran it. The last three are in
// the key because SQL Server reuses session ids freely, so two unrelated
// logins can hold the same number inside one window, and folding them
// together would invent a history that never happened. Showing them as
// columns is what makes that visible on screen.
//
// The read lock is held for the whole walk, which blocks Append. At the
// default fifteen minutes and eight hundred rows a tick that is a few
// hundred thousand integer comparisons, and it happens when a person presses
// a key rather than on a timer.
func (w *Window) SessionStatements(spid int64) []model.StatementSeen {
	w.mu.RLock()
	defer w.mu.RUnlock()

	type acc struct {
		st    model.StatementSeen
		waits map[string]int
	}
	byKey := map[string]*acc{}
	var order []string

	for _, t := range w.ticks {
		for _, r := range t.rows {
			if r.Ref.SessionID != spid {
				continue
			}
			key := r.Login + "\x00" + r.Host + "\x00" + r.Program + "\x00" + r.SQLText
			a := byKey[key]
			if a == nil {
				a = &acc{
					st: model.StatementSeen{
						SessionID: spid, Login: r.Login, Host: r.Host, Program: r.Program,
						Database: r.Database, Command: r.Command, SQLText: r.SQLText,
						FirstAt: t.at,
					},
					waits: map[string]int{},
				}
				byKey[key] = a
				order = append(order, key)
			}
			a.st.LastAt = t.at
			a.st.Samples++
			if r.ElapsedMs > a.st.MaxElapsedMs {
				a.st.MaxElapsedMs = r.ElapsedMs
			}
			if r.CPUMs > a.st.MaxCPUMs {
				a.st.MaxCPUMs = r.CPUMs
			}
			if r.LogicalReads > a.st.MaxReads {
				a.st.MaxReads = r.LogicalReads
			}
			if r.WaitType != "" {
				a.waits[r.WaitType]++
			}
		}
	}

	out := make([]model.StatementSeen, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		for wt, n := range a.waits {
			// Ties break on the name so the answer does not move between
			// two calls over the same data.
			if n > a.st.TopWaitSamples || (n == a.st.TopWaitSamples && wt < a.st.TopWait) {
				a.st.TopWait, a.st.TopWaitSamples = wt, n
			}
		}
		out = append(out, a.st)
	}
	// Most recently seen first: the statement a person is asking about is
	// almost always the last one.
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}
