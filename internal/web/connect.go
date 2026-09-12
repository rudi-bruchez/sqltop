package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// ConnectFunc tries one connection from the body the connect page posted.
// On failure it returns a *ConnectError carrying the status and the words
// to show; any other error is shown as a failed connection.
type ConnectFunc func(ctx context.Context, body []byte) (ConnectResult, error)

// ConnectResult is what the page shows after a success. DSN is redacted.
type ConnectResult struct {
	DSN        string `json:"dsn"`
	EnvWritten string `json:"env_written"`
	EnvError   string `json:"env_error"`
}

// ConnectError is a failed attempt as the page shows it.
type ConnectError struct {
	Status        int
	Message, Hint string
}

func (e *ConnectError) Error() string { return e.Message }

// connectTimeout bounds one attempt. A var only so a test can shrink it.
var connectTimeout = 30 * time.Second

var connectPage = sync.OnceValues(func() ([]byte, error) {
	return compose("assets/connect.html", "assets/connect.js")
})

// handoff lets http.Server.Shutdown stop accepting without closing the
// socket: the monitor accepts on it next, and a connection arriving between
// the two waits in the kernel's queue instead of being refused.
type handoff struct{ *net.TCPListener }

func (h handoff) Close() error { return h.SetDeadline(time.Now()) }

// connectPhase answers the connect page until one attempt succeeds.
type connectPhase struct {
	options   any
	connect   ConnectFunc
	busy      sync.Mutex // held for one attempt; TryLock makes a second a 409
	connected bool       // under busy
	done      chan struct{}
}

// AskConnection serves the connect page on l until connect succeeds once,
// then stops without closing the socket, so the monitor can serve on it
// next with the same token. It returns ctx.Err() when ctx ends first.
// docs/specs/2026-09-11-connect-page-design.md section 3.
func (l *Listener) AskConnection(ctx context.Context, options any, connect ConnectFunc) error {
	if _, err := connectPage(); err != nil {
		return err
	}
	p := &connectPhase{options: options, connect: connect, done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(p.index))
	mux.Handle("/api/connect", http.HandlerFunc(p.attempt))
	srv := &http.Server{
		Handler:           securityHeaders(requireToken(l.token, mux)),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(handoff{l.ln}) }()

	var err error
	select {
	case <-p.done:
	case <-ctx.Done():
		err = ctx.Err()
	case serr := <-served:
		return fmt.Errorf("web: connect page: %w", serr)
	}
	// Bounded: a browser leaves speculative connections idle, and net/http
	// would otherwise wait five seconds on each before letting go.
	gracefulShutdown(srv)
	<-served
	if derr := l.ln.SetDeadline(time.Time{}); derr != nil && err == nil {
		err = derr
	}
	return err
}

func (p *connectPhase) index(rw http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(rw, req)
		return
	}
	body, err := connectPage()
	if err != nil {
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Write(body)
}

func (p *connectPhase) attempt(rw http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		writeJSON(rw, p.options)
		return
	case http.MethodPost:
	default:
		rw.Header().Set("Allow", "GET, POST")
		http.Error(rw, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	if !p.busy.TryLock() {
		writeJSONStatus(rw, http.StatusConflict, map[string]string{"error": "a connection attempt is already running"})
		return
	}
	defer p.busy.Unlock()
	if p.connected {
		writeJSONStatus(rw, http.StatusConflict, map[string]string{"error": "already connected"})
		return
	}
	// A form is a few hundred bytes; the cap only stops a runaway body.
	body, err := io.ReadAll(http.MaxBytesReader(rw, req.Body, 64<<10))
	if err != nil {
		writeJSONStatus(rw, http.StatusBadRequest, map[string]string{"error": "unreadable request: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), connectTimeout)
	defer cancel()
	res, err := p.connect(ctx, body)
	if err != nil {
		var ce *ConnectError
		if !errors.As(err, &ce) {
			ce = &ConnectError{Status: http.StatusBadGateway, Message: err.Error()}
		}
		writeJSONStatus(rw, ce.Status, map[string]string{"error": ce.Message, "hint": ce.Hint})
		return
	}
	p.connected = true
	writeJSON(rw, res)
	close(p.done)
}

func writeJSONStatus(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(v)
}
