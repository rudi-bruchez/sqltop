package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

//go:embed assets
var assetsFS embed.FS

// Server is the loopback HTTP server: it serves the page and the API, and
// nothing about it is reachable from the network. Spec section 4.3.
//
// It no longer carries its own stream period. Fix round 1, task 14: a value
// read once here and handed to stream.go could not follow the collector's
// own throttle, which re-reads Budget.Period on every tier iteration; the
// stream now asks col.Period(model.TierRequests) on every tick instead, so
// there is nothing left for this struct to cache.
type Server struct {
	col      *collector.Collector
	win      *window.Window
	token    string
	listener net.Listener
	// mu guards cfg, dash and grid, which the layout endpoint rewrites
	// while stream goroutines are reading them.
	mu sync.RWMutex
	// saveMu serialises writes of the configuration file against each
	// other. Separate from mu on purpose: mu guards fields that a stream
	// reads on every connection, and holding it across a file write would
	// make every new client wait on a disk.
	saveMu sync.Mutex
	cfg    config.Config
	dash   []DashGroup
	grid   []GridView
}

// WithConfig gives the server the resolved configuration: what the
// dashboard shows, which grid columns are drawn in what order, and the file
// to write back to when the interface saves a layout. A server built
// without one still serves the full catalogue, which is what the tests and
// a defaults-only run want.
func (s *Server) WithConfig(cfg config.Config) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.dash = resolveDashboard(cfg.Dashboard())
	s.grid = resolveAllGrids(cfg)
	return s
}

// NewServer binds 127.0.0.1 and nothing else. There is deliberately no
// option to widen it: this interface will eventually be able to kill
// sessions on a production server, and a bind on all interfaces would hand
// that to anyone on the network. cfg.Port is the only knob, and it still
// only ever selects a port on the loopback address, never the interface.
func NewServer(c *collector.Collector, w *window.Window, cfg config.Server) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("web: listen: %w", err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		ln.Close()
		return nil, fmt.Errorf("web: token: %w", err)
	}
	// Built with the defaults rather than with a zero Config: every path
	// that reads the configuration (the dashboard, the grid columns, the
	// two endpoints that validate against it) then has one shape to handle
	// instead of two, and a test server behaves like a real one that
	// happened to find no file.
	srv := &Server{col: c, win: w, token: hex.EncodeToString(raw[:]), listener: ln}
	return srv.WithConfig(config.Default()), nil
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
//
// authenticate also accepts the token from an X-Sqltop-Token header, but
// the shipped page never sends one: EventSource, which is what it uses for
// /api/stream, cannot set arbitrary headers, and index composes the CSS and
// JavaScript into the document it returns rather than asking the browser to
// fetch them separately (fix round 1, task 14), so there is no second
// request left that could use one either. The header path exists for a
// non-browser client, a script or curl, that would rather not put the
// token in a URL at all; it is not what keeps the token out of history
// today.
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

// dashboard and gridColumns are what a new client is told to draw. Both are
// resolved once by WithConfig, which NewServer always calls, so there is no
// unconfigured case to fall back from.
func (s *Server) dashboard() []DashGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dash
}

func (s *Server) gridColumns() []GridView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.grid
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
	if _, err := page(); err != nil {
		return nil, err
	}
	return []route{
		{"/", http.HandlerFunc(s.index)},
		{"/api/status", http.HandlerFunc(s.status)},
		{"/api/stream", http.HandlerFunc(s.stream)},
		{"/api/layout", http.HandlerFunc(s.layout)},
		{"/api/period", http.HandlerFunc(s.period)},
		{"/api/snapshot", http.HandlerFunc(s.snapshot)},
		{"/api/sessions", http.HandlerFunc(s.sessions)},
		{"/api/transactions", http.HandlerFunc(s.transactions)},
		{"/api/logs", http.HandlerFunc(s.logs)},
		{"/api/plan", http.HandlerFunc(s.plan)},
		{"/api/plansave", http.HandlerFunc(s.plansave)},
		{"/api/history", http.HandlerFunc(s.history)},
		{"/api/sessionwaits", http.HandlerFunc(s.sessionwaits)},
		{"/api/capture", http.HandlerFunc(s.capture)},
	}, nil
}

// composePage builds the single response index serves: index.html with
// style.css and app.js substituted in as inline <style> and <script>
// elements, in place of the <link href="style.css"> and <script
// src="app.js"> tags the files sit under on disk (fix round 1, task 14).
//
// The relative URLs those tags used to carry are exactly what broke the
// page: a relative reference does not inherit the base URL's query string,
// so a browser that requested them separately sent GET /style.css and GET
// /app.js with no token at all, authenticate refused both with 401, and
// the reviewer who clicked the printed link got a bare unstyled heading
// with no grid and no stream, verified against a real browser rather than
// curl's explicit ?t=. Composing everything into one response removes the
// subresources rather than exempting them from the token: an exemption
// would have worked too, since they carry no server data, but the other
// way out the brief allowed, a cookie, is worse than either. Cookies are
// not port-scoped, so any other local page open on 127.0.0.1 would have
// the browser attach this one, handing the token to whatever that page
// wanted to do with it.
//
// style.css and app.js stay separate files on disk rather than moving into
// this string: app_assets_test.go reads assets/app.js directly (its own
// regression guard, unrelated to this one), and editing either a
// stylesheet or a two hundred line script inside a Go string literal is
// its own kind of mistake
// waiting to happen.
func composePage() ([]byte, error) {
	html, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		return nil, err
	}
	css, err := assetsFS.ReadFile("assets/style.css")
	if err != nil {
		return nil, err
	}
	js, err := assetsFS.ReadFile("assets/app.js")
	if err != nil {
		return nil, err
	}

	link := []byte(`<link rel="stylesheet" href="style.css">`)
	if !bytes.Contains(html, link) {
		return nil, fmt.Errorf("web: assets/index.html does not carry the expected stylesheet link")
	}
	styled := append([]byte("<style>\n"), css...)
	styled = append(styled, []byte("\n</style>")...)
	html = bytes.Replace(html, link, styled, 1)

	script := []byte(`<script src="app.js"></script>`)
	if !bytes.Contains(html, script) {
		return nil, fmt.Errorf("web: assets/index.html does not carry the expected script tag")
	}
	inlined := append([]byte("<script>\n"), js...)
	inlined = append(inlined, []byte("\n</script>")...)
	html = bytes.Replace(html, script, inlined, 1)

	return html, nil
}

// page memoises composePage: the embedded assets never change while the
// process runs, so every request shares one composed response instead of
// redoing the same substitution per connection. sync.OnceValues, not a
// plain package-level []byte, so a compose failure (an index.html that
// lost its link or script tag) is still reported as an error the first
// time anything asks, from routes, rather than panicking at package init
// before main has a chance to log anything.
var page = sync.OnceValues(composePage)

// index serves the single composed page at "/". Anything else falls
// through to a 404: there is no longer a directory of static files behind
// this route for a stray path to resolve against.
func (s *Server) index(rw http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(rw, req)
		return
	}
	body, err := page()
	if err != nil {
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Write(body)
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
		StatusPayload: newStatusPayload(st),
		Window:        windowInfo{Oldest: oldest, Samples: samples, Capped: capped},
	})
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
// at all, which some non-browser clients send), 127.0.0.1, localhost and the
// IPv6 loopback ::1, each with or without a port. The server itself binds
// IPv4 only (spec section 4.3), so ::1 is not a second address anything can
// actually reach it on; it is accepted because a user who types the
// bracketed IPv6 loopback, or whose environment resolves localhost over
// IPv6 first, would otherwise get an unexplained unauthorized for a host
// that was never a hole to begin with. Everything else is refused exactly
// like a missing or wrong token, so this reveals nothing beyond
// "unauthorized" either.
func hostAllowed(host string) bool {
	if host == "" {
		return true
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	} else if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
		// A bracketed IPv6 literal with no port, e.g. "[::1]": SplitHostPort
		// requires a port to parse it at all, so this is the one shape it
		// does not already strip for us above.
		h = host[1 : len(host)-1]
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
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
//
// BaseContext ties every request's context to ctx, the same one this
// method's caller cancels to ask for shutdown (fix round 1, task 14).
// Without it, http.Server.Shutdown does not cancel any request context on
// its own; stream.go's handler only found out its connection was going
// away once a write to the now-closing socket failed, or, if the client
// happened to be idle at exactly that moment, once shutdownGrace's
// force-close fired, up to two full seconds later. Deriving every request
// context from ctx makes stream.go's own claim (it returns as soon as
// req.Context() is cancelled) true the moment shutdown is asked for,
// rather than up to shutdownGrace later: a Ctrl-C with a browser tab open
// no longer has to wait out the grace period at all.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
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
