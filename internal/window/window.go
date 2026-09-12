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

// Statements is what the whole server has been seen running over the window,
// grouped by statement shape. Spec section 7's queries view. Like
// SessionStatements it costs the monitored server nothing, and unlike it this
// walk runs on a poll rather than on a keypress, which is why the browser
// holds it to a floor well above the grid's period: the read lock is held for
// the whole walk and blocks Append.
//
// max caps what comes back, ordered by total CPU. The list view draws one row
// per entry with no virtualisation, and a busy server can hold thousands of
// distinct shapes in fifteen minutes; the caller is told the full count so it
// can say that the list is a top and not the whole story.
func (w *Window) Statements(max int) (out []model.QuerySeen, total int) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	type run struct {
		// tick is the index of the last tick this run was seen in. A gap of
		// more than one means the previous run ended and this is a new one.
		tick       int
		cpu        int64
		elapsed    int64
		elapsedSum int64 // closed runs only, for the average
	}
	type acc struct {
		q        model.QuerySeen
		waits    map[string]int
		sessions map[int64]bool
		// runs is keyed by session: two sessions running the same shape at
		// the same instant are two runs, and neither interrupts the other.
		runs map[int64]*run
	}
	byKey := map[string]*acc{}
	var order []string

	// endRun folds a finished run into its group. A run's contribution is its
	// maximum, never the sum of its samples: the engine's request counters
	// are cumulative, so every sample already contains the ones before it.
	endRun := func(a *acc, r *run) {
		a.q.Runs++
		a.q.TotalCPUMs += r.cpu
		r.elapsedSum += r.elapsed
		if r.elapsed > a.q.MaxElapsedMs {
			a.q.MaxElapsedMs = r.elapsed
		}
	}

	for i, t := range w.ticks {
		// How long ago the previous tick was. A request already alive then has
		// been running at least this long, which is what tells a run that
		// continues from a second execution of the same shape on the same
		// session. Consecutive ticks alone are not enough: under the budget
		// governor the period stretches to seconds, and most statements are
		// caught once per execution.
		var gapMs int64
		if i > 0 {
			gapMs = t.at.Sub(w.ticks[i-1].at).Milliseconds()
		}

		for _, r := range t.rows {
			// The hash folds literals together, which is what this view is
			// for. It is empty for some statements, and the text has to stand
			// in there or every one of them folds into a single meaningless
			// row.
			shape := r.QueryHash
			if shape == "" {
				shape = "\x01" + r.SQLText
			}
			key := r.Database + "\x00" + shape
			a := byKey[key]
			if a == nil {
				a = &acc{
					q:        model.QuerySeen{QueryHash: r.QueryHash, Database: r.Database, Command: r.Command, FirstAt: t.at},
					waits:    map[string]int{},
					sessions: map[int64]bool{},
					runs:     map[int64]*run{},
				}
				byKey[key] = a
				order = append(order, key)
			}

			spid := r.Ref.SessionID
			cur := a.runs[spid]
			if cur == nil {
				cur = &run{tick: i}
				a.runs[spid] = cur
			} else if cur.tick != i && !(cur.tick == i-1 && r.ElapsedMs >= gapMs) {
				endRun(a, cur)
				*cur = run{tick: i, elapsedSum: cur.elapsedSum}
			}
			cur.tick = i
			if r.CPUMs > cur.cpu {
				cur.cpu = r.CPUMs
			}
			if r.ElapsedMs > cur.elapsed {
				cur.elapsed = r.ElapsedMs
			}

			a.q.LastAt = t.at
			a.q.Samples++
			a.q.SQLText = r.SQLText
			a.sessions[spid] = true
			if r.WaitType != "" {
				a.waits[r.WaitType]++
			}
		}
	}

	out = make([]model.QuerySeen, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		for _, r := range a.runs {
			endRun(a, r) // every run still open when the window ended
		}
		var elapsed int64
		for _, r := range a.runs {
			elapsed += r.elapsedSum
		}
		if a.q.Runs > 0 {
			a.q.AvgElapsedMs = elapsed / int64(a.q.Runs)
		}
		a.q.Sessions = len(a.sessions)
		for wt, n := range a.waits {
			// Ties break on the name so the answer does not move between two
			// calls over the same data.
			if n > a.q.TopWaitSamples || (n == a.q.TopWaitSamples && wt < a.q.TopWait) {
				a.q.TopWait, a.q.TopWaitSamples = wt, n
			}
		}
		out = append(out, a.q)
	}

	// The expensive first, which is the question this view answers. Ties break
	// on the most recently seen so the order is stable between two calls.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalCPUMs != out[j].TotalCPUMs {
			return out[i].TotalCPUMs > out[j].TotalCPUMs
		}
		return out[i].LastAt.After(out[j].LastAt)
	})
	total = len(out)
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, total
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
