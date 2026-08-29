package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

//go:embed assets
var assetsFS embed.FS

// Server is the loopback HTTP server: it serves the page and the API, and
// nothing about it is reachable from the network. Spec section 4.3.
type Server struct {
	col      *collector.Collector
	win      *window.Window
	token    string
	listener net.Listener
	// push is the stream period. It follows the requests tier, so changing
	// that in the configuration file changes what the browser receives too.
	// Task 14 is what actually reads it; this task only carries it through.
	push time.Duration
}

// NewServer binds 127.0.0.1 and nothing else. There is deliberately no
// option to widen it: this interface will eventually be able to kill
// sessions on a production server, and a bind on all interfaces would hand
// that to anyone on the network. cfg.Port is the only knob, and it still
// only ever selects a port on the loopback address, never the interface.
func NewServer(c *collector.Collector, w *window.Window, cfg config.Server, push time.Duration) (*Server, error) {
	if push <= 0 {
		push = time.Second
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("web: listen: %w", err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		ln.Close()
		return nil, fmt.Errorf("web: token: %w", err)
	}
	return &Server{col: c, win: w, token: hex.EncodeToString(raw[:]), listener: ln, push: push}, nil
}

// URL is what the tool prints and opens on startup. The token travels in the
// query string here, which is the only way an address a browser opens can
// carry it. The cost is real and known, not overlooked: a URL that includes
// a secret lands in shell history, in the browser's own history, in the
// access logs of anything that proxies it, and, since this is also what
// gets logged to stderr on startup, in journald, a CI log, or whatever else
// captures this process's output with whatever permissions that carries.
// It is accepted here because the alternative, no token in the opened URL,
// would need the user to paste a header by hand before the first page
// load, and because the token still protects the one thing that matters on
// a loopback bind: another local account on the same shared machine.
// authenticate also accepts the token from a header, which is what the page
// itself uses for every request after the first, so the token appears in
// history exactly once per run rather than on every poll.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?t=%s", s.listener.Addr().String(), s.token)
}

// Close releases the bound socket without going through Serve. It exists
// for a caller that fails between NewServer and Serve, which otherwise has
// nothing that would release the listener until the process exits, and for
// tests that want to exercise Handler without ever starting Serve.
func (s *Server) Close() error {
	return s.listener.Close()
}

// route pairs a path with the handler that serves it.
type route struct {
	path    string
	handler http.Handler
}

// routes is the single place a path is registered. Handler builds the mux
// from exactly this list, and TestEveryRouteRequiresTheToken walks the same
// list, so a route added here without thinking about authentication cannot
// exist: it is either in this list, in which case Handler wraps it in
// authenticate below like everything else, or it is not registered at all.
// That closes the mistake a second, outer mux with its own unauthenticated
// route would otherwise open.
func (s *Server) routes() ([]route, error) {
	content, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, err
	}
	return []route{
		{"/", http.FileServer(http.FS(content))},
		{"/api/status", http.HandlerFunc(s.status)},
		{"/api/stream", http.HandlerFunc(s.stream)},
	}, nil
}

// Handler builds the mux fresh on every call. It is cheap (a handful of
// route registrations) and it is what lets tests exercise routing without
// going through Serve, which the interface in the brief requires.
func (s *Server) Handler() http.Handler {
	rs, err := s.routes()
	if err != nil {
		panic(err) // an embed that does not resolve is a build-time mistake
	}

	mux := http.NewServeMux()
	for _, r := range rs {
		mux.Handle(r.path, r.handler)
	}

	// No CORS headers are ever emitted. Spec section 4.3: nothing legitimate
	// is cross-origin here, since the only client this server expects is
	// the page it just served, over the loopback address, with the token
	// that page already holds.
	//
	// securityHeaders is outermost so it applies even to a request
	// authenticate refuses: an attacker's page that got this far without
	// the token still should not learn anything from response headers
	// either, and a 401 body should not carry a referrer any more than a
	// 200 one should.
	return securityHeaders(s.authenticate(mux))
}

// securityHeaders sets response headers that cost nothing today and matter
// once task 14's page exists. The token lives in the query string, so any
// external resource that page ever loads (a font, an icon, a CDN script)
// would otherwise ship it in the Referer header; no-referrer stops that.
// no-store keeps a browser or an intermediate cache from persisting a
// response that carries session data, SQL text, or the status payload's own
// token-adjacent details anywhere outside its own memory.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(rw, req)
	})
}

// windowInfo mirrors what the status bar needs to know about the retention
// window: how far back it reaches, how many samples it holds, and whether
// the sample cap rather than the retention period decided that.
type windowInfo struct {
	Oldest  time.Time `json:"oldest"`
	Samples int       `json:"samples"`
	Capped  bool      `json:"capped"`
}

// statusResponse embeds the same StatusPayload the streamed snapshots carry,
// so a client reads the same shape whether it polls /api/status once or
// watches /api/stream: no second, slightly different status vocabulary to
// keep in sync by hand.
type statusResponse struct {
	StatusPayload
	Window windowInfo `json:"window"`
}

func (s *Server) status(rw http.ResponseWriter, _ *http.Request) {
	st := s.col.Status()
	oldest, samples, capped := s.win.Depth()
	writeJSON(rw, statusResponse{
		StatusPayload: StatusPayload{
			Sqltop:    buildinfo.String(),
			Connected: st.Connected,
			Message:   st.Message,
			Instance:  st.Info.Instance,
			Version:   st.Info.ProductVersion,
			Caps:      capNames(st.Caps),
		},
		Window: windowInfo{Oldest: oldest, Samples: samples, Capped: capped},
	})
}

// stream is the route task 14 turns into the SSE feed. Wiring it here, even
// as a stub, is what lets this task's tests exercise the full route table
// and its authentication, rather than a subset of it that changes shape
// again once the stream exists.
//
// It must not be implemented as a shared Encoder handed out to every
// connection: an Encoder tracks which references a client has already been
// sent (protocol.go), and a client that joins after another one would then
// find its own reference table already marked "sent" for sessions it never
// received a Ref for, showing blank SQL cells. Whoever wires this in task 14
// must construct a fresh *Encoder per accepted connection.
func (s *Server) stream(rw http.ResponseWriter, _ *http.Request) {
	http.Error(rw, "not implemented", http.StatusNotImplemented)
}

// authenticate accepts the token from the query string, where the opened URL
// puts it, or from a header, which the page uses afterwards. It is compared
// with subtle.ConstantTimeCompare rather than Go's built-in != so the
// comparison itself takes the same time regardless of how many leading
// bytes match; TestConstantTimeCompareIsUsedForTheToken checks that this
// package still imports crypto/subtle and still calls it here, because
// nothing about a request's timing or its response is precise enough, over
// a real loopback round trip, for a behavioural test to tell a
// constant-time compare apart from a short-circuiting one on its own.
//
// It also checks the Host header before the token: see hostAllowed.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if !hostAllowed(req.Host) {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := req.URL.Query().Get("t")
		if got == "" {
			got = req.Header.Get("X-Sqltop-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, req)
	})
}

// hostAllowed defends against DNS rebinding: a browser tab that already
// holds this server's token, open against a page whose hostname's DNS
// answer changes, could otherwise have that page issue same-origin requests
// here carrying whatever Host header an attacker's domain wants. Spec
// section 4.3 does not ask for this defence, and it is not what actually
// stops that attacker: they still need the token, the loopback address, and
// an open tab to get anywhere, and the token is doing that work already.
// It is added anyway because the token is exactly the thing that leaks, out
// through browser history and, per the comment on URL, through stderr; a
// second, unrelated control is cheap now and awkward to retrofit once task
// 14's page exists to break by adding it later.
//
// Anything shaped like a loopback host is accepted: empty (no Host header
// at all, which some non-browser clients send), 127.0.0.1 and localhost,
// each with or without a port. Everything else is refused exactly like a
// missing or wrong token, so this reveals nothing beyond "unauthorized"
// either.
func hostAllowed(host string) bool {
	if host == "" {
		return true
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	return h == "127.0.0.1" || h == "localhost"
}

// shutdownGrace is how long gracefulShutdown waits for active connections to
// drain before forcing them closed. A var, not a literal, only so a test can
// shrink it and prove the force-close path fires without a real two-second
// wait; nothing else ever assigns it.
var shutdownGrace = 2 * time.Second

// gracefulShutdown asks srv to drain its connections and waits up to
// shutdownGrace for that to finish. If it does not finish in time,
// srv.Shutdown returns an error and this falls back to srv.Close, which
// drops any connections still open rather than leaving them running. Without
// that fallback, a caller of Serve sees a clean return the moment the grace
// period elapses regardless of whether anything actually stopped: a client
// still reading from an open connection, for instance task 14's SSE stream,
// would keep running past the point its caller believes the server has
// stopped. http.Server.Shutdown itself never force-closes active
// connections; that is this function's whole reason to exist rather than a
// bare call to Shutdown inline in Serve.
func gracefulShutdown(srv *http.Server) {
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		srv.Close()
	}
}

// Serve blocks until ctx is done, then shuts the server down and returns.
// The shutdown goroutine below is the only one this method starts, and
// Serve does not return until that goroutine has actually finished, not
// merely been told to: this is what keeps Serve's own goroutine budget at
// zero once it returns, an invariant worth holding outright even though
// only TestServeShutsDownCleanlyWithoutLeakingGoroutines checks it, and
// that test's own poll allows up to two seconds of slack, so it would not
// by itself have caught a version that let the goroutine finish slightly
// late.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		gracefulShutdown(srv)
	}()

	err := srv.Serve(s.listener)
	<-done // Serve has returned; wait for the watcher goroutine above to finish too, so Serve never returns while a goroutine it started is still running.
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	// The encode error, if any, arrives after the status line and part of
	// the body are already written, so there is nothing left to do about it
	// but let it go: a client that got a truncated response will fail its
	// own JSON parse, which is signal enough.
	json.NewEncoder(rw).Encode(v)
}
