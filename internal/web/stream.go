package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// writeTimeout bounds one snapshot write. Generous by the standards of a
// loopback socket, where a write that has not completed in this long is not
// slow but stuck: the reader is gone, or is not reading. Short enough that
// a stuck connection costs one goroutine for seconds rather than for as
// long as the kernel is willing to wait.
const writeTimeout = 10 * time.Second

// stream pushes a snapshot per tick over server-sent events. One Encoder per
// client: server.go's comment on the former stub explains why a shared one
// is wrong (the second client would never receive the references the first
// one already consumed, leaving its grid blank for SQL text, program,
// login and host on every session, permanently for a long-running query).
//
// The handler returns as soon as req.Context() is cancelled, whether that
// is the client disconnecting or Serve's own shutdown asking for it:
// Serve's BaseContext ties every request's context to the same one its
// caller cancels, so this fires immediately on shutdown rather than
// waiting on shutdownGrace's force-close (fix round 1, task 14; see the
// comment on Serve).
//
// rw.Write is not interruptible by req.Context() being cancelled, so a
// client that opens the connection and never reads from it would park this
// goroutine inside Write until the kernel's own send buffer gave up, which
// can be far longer than shutdownGrace. writeTimeout bounds that: a
// deadline is set before every write and the handler returns when one
// expires, dropping the connection rather than the goroutine. Written down
// as debt for a release and taken here after an external reviewer counted
// it as a way for a local client to pin server goroutines, which it is.
func (s *Server) stream(rw http.ResponseWriter, req *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	// no-store as well as no-cache: securityHeaders already set no-store on
	// every response, forbidding storage outright, and this handler used to
	// overwrite that with no-cache alone, which only requires revalidation
	// before reuse rather than forbidding it. This is the one response that
	// carries every visible session's SQL text on every tick, so silently
	// loosening the middleware's own header here was correct only by
	// accident (fix round 1, task 14). no-cache stays too: it is still the
	// conventional pairing for a stream a proxy must not buffer.
	rw.Header().Set("Cache-Control", "no-store, no-cache")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("X-Accel-Buffering", "no")

	// ResponseController is how a handler reaches the deadline machinery
	// without knowing what kind of ResponseWriter it has. SetWriteDeadline
	// returns ErrNotSupported on a writer that cannot do it, which is not a
	// reason to refuse the connection: the stream still works, it just
	// keeps the unbounded write it had before.
	rc := http.NewResponseController(rw)

	enc := NewEncoder()
	send := func() bool {
		payload := enc.Snapshot(s.win.Latest(), s.col.Server().Figures, s.col.Status())
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		// Set before every write and never cleared: a deadline left over
		// from the previous tick would expire during this one.
		if err := rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := rw.Write([]byte("event: snapshot\ndata: " + string(b) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	// The period is read again on every tick via s.col.Period, never cached
	// in a local variable across iterations: the collector's own tier
	// goroutines re-read Budget.Period on every one of their iterations
	// too, since the throttle can change it between two of them, and a
	// value read once here at connect time would silently stop following
	// tier A once the budget escalated (fix round 1, task 14). A
	// time.Timer, reset with the freshly read period after every send,
	// rather than a time.Ticker built from one fixed duration: a Ticker has
	// no way to change its own period once running.
	t := time.NewTimer(s.col.Period(model.TierRequests))
	defer t.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case <-t.C:
			if !send() {
				return
			}
			t.Reset(s.col.Period(model.TierRequests))
		}
	}
}
