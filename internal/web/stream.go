package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

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
// rw.Write below is not itself interruptible by req.Context() being
// cancelled: a client that opens the connection and then never reads from
// it (not the ordinary case, but not impossible either) parks this
// goroutine inside that Write call until the kernel's own TCP send buffer
// gives up, which can be much longer than shutdownGrace during normal
// operation, and Serve's force-close is what actually bounds it on
// shutdown. Nothing here sets a per-write deadline to bound it any other
// time; that is debt, not an oversight, and the way out is
// rw.(http.ResponseController).SetWriteDeadline before each Write, reset
// every tick.
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

	enc := NewEncoder()
	send := func() bool {
		payload := enc.Snapshot(s.win.Latest(), s.col.Server().Figures, s.col.Status())
		b, err := json.Marshal(payload)
		if err != nil {
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
