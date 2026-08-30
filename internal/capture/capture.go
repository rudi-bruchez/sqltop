// Package capture owns the life of one session capture: when it starts, the
// seven things that end it, the goroutine that drains it while nobody is
// looking, and the JSON Lines file it leaves behind.
package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/outdir"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

// recentKept bounds what the panel is handed on each poll. The file keeps
// everything; ten minutes of a chatty session would otherwise be shipped
// whole every second.
const recentKept = 500

// lostAfter failed polls in a row. A single failure is not an ending: the
// source replaces a dead connection by itself and the next tick retries.
// Thirty is a minute at the default interval.
const lostAfter = 30

// Manager runs at most one capture at a time. The version and the instance
// are held because the file's header record names both and nothing below
// this package knows them.
type Manager struct {
	src      source.Capturer
	version  string
	instance string

	// Replaced in tests, where waiting ten real minutes is not an option.
	dir      func() (string, error)
	interval time.Duration
	cap      time.Duration

	// One toggle at a time: two tabs pressing the key together would
	// otherwise leave a capture running with nothing holding it.
	toggling sync.Mutex

	mu   sync.Mutex
	run  *run
	st   model.CaptureState
	rows []model.CapturedStatement
}

// run is one capture. Everything but reason belongs to the drain goroutine;
// reason is written by Stop under the manager's mutex.
type run struct {
	handle source.CaptureHandle
	login  time.Time
	cancel context.CancelFunc
	done   chan struct{}
	file   *os.File
	enc    *json.Encoder

	mark       int64
	statements int
	missed     int64
	dropped    int64
	unknown    bool

	reason model.StopReason
}

func New(c source.Capturer, version, instance string) *Manager {
	return &Manager{
		src:      c,
		version:  version,
		instance: instance,
		dir:      func() (string, error) { return outdir.Beside("traces") },
		interval: 2 * time.Second,
		cap:      10 * time.Minute,
	}
}

// Toggle starts a capture on spid, or stops the running one when it is
// already watching that session.
func (m *Manager) Toggle(ctx context.Context, spid int64) error {
	m.toggling.Lock()
	defer m.toggling.Unlock()

	m.mu.Lock()
	running := m.run
	m.mu.Unlock()
	if running != nil {
		if err := m.Stop(ctx, model.StopByKey); err != nil {
			return err
		}
		if running.handle.SessionID == spid {
			return nil
		}
	}
	return m.start(ctx, spid)
}

func (m *Manager) start(ctx context.Context, spid int64) error {
	login, ok, err := m.src.WatchedSession(ctx, spid)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %d is no longer running", spid)
	}
	dir, err := m.dir()
	if err != nil {
		return err
	}
	h, err := m.src.StartCapture(ctx, spid)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("capture-%d-%s", spid, h.Started.Format("2006-01-02-150405"))
	f, path, err := outdir.Create(dir, base, ".jsonl")
	if err != nil {
		_ = m.src.StopCapture(ctx, h)
		return err
	}
	r := &run{handle: h, login: login, done: make(chan struct{}), file: f, enc: json.NewEncoder(f)}
	r.write(headerRecord{
		Record:    "header",
		Version:   m.version,
		Instance:  m.instance,
		SessionID: spid,
		StartedAt: h.Started,
	})

	// Not the caller's context: a capture outlives the request that asked
	// for it, and only Stop ends it.
	dctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	m.mu.Lock()
	m.run = r
	m.rows = nil
	m.st = model.CaptureState{Active: true, SessionID: spid, StartedAt: h.Started, File: path}
	interval, limit := m.interval, m.cap
	m.mu.Unlock()

	go m.drain(dctx, r, interval, limit)
	return nil
}

// Stop ends the running capture, if there is one, and is safe to call twice
// and from the drain itself. The run is taken and cleared under the mutex,
// which is released before the wait: waiting while holding it would deadlock
// against a State call the drain is blocked behind.
func (m *Manager) Stop(ctx context.Context, reason model.StopReason) error {
	m.mu.Lock()
	r := m.run
	if r == nil {
		m.mu.Unlock()
		return nil
	}
	m.run = nil
	r.reason = reason
	m.st.Active = false
	m.st.Stopped = reason.String()
	m.mu.Unlock()

	r.cancel()
	<-r.done
	return m.src.StopCapture(ctx, r.handle)
}

// State is answered whether or not a capture is running, because "ended
// because the session was reused" is what a reader needs and an empty panel
// is not.
func (m *Manager) State(ctx context.Context) model.CaptureState {
	m.mu.Lock()
	st := m.st
	m.mu.Unlock()

	if ok, why, err := m.src.CanCapture(ctx); err != nil {
		st.Why = err.Error()
	} else {
		st.Available, st.Why = ok, why
	}
	others, err := m.src.RunningCaptures(ctx)
	if err != nil || others == nil {
		others = []model.CaptureNote{}
	}
	st.Others = others
	return st
}

// Recent is the statements the panel shows, copied under the mutex the drain
// appends under.
func (m *Manager) Recent() []model.CapturedStatement {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.CapturedStatement(nil), m.rows...)
}

// drain reads the capture whether or not the panel is open. A capture nobody
// drains fills its buffer and loses events in silence.
func (m *Manager) drain(ctx context.Context, r *run, interval, limit time.Duration) {
	defer close(r.done)
	t := time.NewTicker(interval)
	defer t.Stop()

	fails, ending := 0, false
	for {
		select {
		case <-ctx.Done():
			m.finish(r)
			return
		case <-t.C:
		}
		if ending {
			continue
		}
		if reason, over := m.tick(ctx, r, limit, &fails); over {
			ending = true
			// From a fresh goroutine: Stop waits on this one.
			go m.Stop(context.Background(), reason)
		}
	}
}

// tick does one read and reports the reason to end on, if this is one of the
// ends the drain itself can see.
func (m *Manager) tick(ctx context.Context, r *run, limit time.Duration, fails *int) (model.StopReason, bool) {
	if time.Since(r.handle.Started) >= limit {
		return model.StopByTimeCap, true
	}
	if login, ok, err := m.src.WatchedSession(ctx, r.handle.SessionID); err == nil {
		if !ok {
			return model.StopBySessionGone, true
		}
		if !login.Equal(r.login) {
			return model.StopBySessionReused, true
		}
	}

	rows, prog, err := m.src.PollCapture(ctx, r.handle, r.mark)
	if err != nil {
		*fails++
		if *fails >= lostAfter {
			return model.StopByServerLost, true
		}
		return model.StopNotStopped, false
	}
	*fails = 0

	// Seen, never Total: on a truncated read Total steps over the tail the
	// document could not carry, and nothing comes back for it.
	r.mark = prog.Seen
	for _, s := range rows {
		r.write(eventRecord{Record: "event", CapturedStatement: s})
	}
	r.statements += len(rows)
	if prog.Missed > 0 || prog.Dropped > r.dropped {
		r.write(gapRecord{
			Record:  "gap",
			At:      time.Now(),
			Lost:    prog.Missed,
			Dropped: prog.Dropped - r.dropped,
		})
	}
	r.missed += prog.Missed
	r.dropped = prog.Dropped
	r.unknown = r.unknown || prog.Truncated

	m.mu.Lock()
	// Only while this is still the running capture: a drain finishing
	// beside a capture that has already started must not write into it.
	if m.run == r {
		m.rows = append(m.rows, rows...)
		if len(m.rows) > recentKept {
			m.rows = append([]model.CapturedStatement(nil), m.rows[len(m.rows)-recentKept:]...)
		}
		m.st.Statements = r.statements
		m.st.Missed = r.missed
		m.st.Dropped = r.dropped
		m.st.Unknown = r.unknown
	}
	m.mu.Unlock()
	return model.StopNotStopped, false
}

func (m *Manager) finish(r *run) {
	m.mu.Lock()
	reason := r.reason
	m.mu.Unlock()

	r.write(endRecord{
		Record:     "end",
		At:         time.Now(),
		Reason:     reason.String(),
		Statements: r.statements,
		Missed:     r.missed,
		Dropped:    r.dropped,
		Unknown:    r.unknown,
	})
	r.file.Close()
}

// The record field, not kind: the statement already spends kind on batch
// versus rpc, and two keys of one name in a flat object are not a parse
// error but something worse, a decoder keeping the last of them.
type headerRecord struct {
	Record    string    `json:"record"`
	Version   string    `json:"version"`
	Instance  string    `json:"instance"`
	SessionID int64     `json:"session_id"`
	StartedAt time.Time `json:"started_at"`
}

// The embedded statement flattens into the same object, so a line stays one
// self-describing record.
type eventRecord struct {
	Record string `json:"record"`
	model.CapturedStatement
}

// Two losses, never collapsed: lost passed through the buffer between two
// reads, dropped never reached it.
type gapRecord struct {
	Record  string    `json:"record"`
	At      time.Time `json:"at"`
	Lost    int64     `json:"lost,omitempty"`
	Dropped int64     `json:"dropped,omitempty"`
}

type endRecord struct {
	Record     string    `json:"record"`
	At         time.Time `json:"at"`
	Reason     string    `json:"reason"`
	Statements int       `json:"statements"`
	Missed     int64     `json:"missed"`
	Dropped    int64     `json:"dropped"`
	Unknown    bool      `json:"unknown"`
}

// write keeps going after a write error: the trace is a by-product, and
// losing the capture over a full disk would cost the user the session too.
func (r *run) write(v any) {
	_ = r.enc.Encode(v)
}
