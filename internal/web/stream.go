package web

import (
	"encoding/json"
	"net/http"
	"time"
)

// stream pushes a snapshot per tick over server-sent events. One Encoder per
// client: server.go's comment on the former stub explains why a shared one
// is wrong (the second client would never receive the references the first
// one already consumed, leaving its grid blank for SQL text, program,
// login and host on every session, permanently for a long-running query).
//
// The handler returns as soon as req.Context() is cancelled, whether that is
// the client disconnecting or Serve force-closing the connection past its
// shutdown grace period: nothing here waits to be killed from outside.
func (s *Server) stream(rw http.ResponseWriter, req *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
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
	t := time.NewTicker(s.push)
	defer t.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case <-t.C:
			if !send() {
				return
			}
		}
	}
}
