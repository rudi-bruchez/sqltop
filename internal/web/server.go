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
// a secret lands in shell history, in the browser's own history, and in the
// access logs of anything that proxies it. It is accepted here because the
// alternative, no token in the opened URL, would need the user to paste a
// header by hand before the first page load, and because the token still
// protects the one thing that matters on a loopback bind: another local
// account on the same shared machine. authenticate also accepts the token
// from a header, which is what the page itself uses for every request after
// the first, so the token appears in history exactly once per run rather
// than on every poll.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?t=%s", s.listener.Addr().String(), s.token)
}

// Handler builds the mux fresh on every call. It is cheap (a handful of
// route registrations) and it is what lets tests exercise routing without
// going through Serve, which the interface in the brief requires.
func (s *Server) Handler() http.Handler {
	content, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err) // an embed that does not resolve is a build-time mistake
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(content)))
	mux.HandleFunc("/api/status", s.status)
	mux.HandleFunc("/api/stream", s.stream)

	// No CORS headers are ever emitted. Spec section 4.3: nothing legitimate
	// is cross-origin here, since the only client this server expects is
	// the page it just served, over the loopback address, with the token
	// that page already holds.
	return s.authenticate(mux)
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
// puts it, or from a header, which the page uses afterwards. Compared in
// constant time so that a near-miss token and a wildly wrong one take the
// same time and produce the same response: nothing about the reply lets a
// caller narrow down the token by trial and error.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
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

// Serve blocks until ctx is done, then shuts the server down and returns.
// The shutdown goroutine below is the only one this method starts, and it
// always exits: either ctx is done and it runs Shutdown once, or Serve
// returns first for some other reason and the process either exits (normal
// shutdown path, ctx gets cancelled too) or the caller's ctx is eventually
// cancelled by whoever owns it, at which point this goroutine's blocking
// receive on ctx.Done unblocks and it returns immediately since Shutdown on
// an already-closed server is a cheap no-op. In the ordinary lifecycle used
// throughout this codebase, one ctx is created per run and cancelled exactly
// once, so this never lingers past that cancellation.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
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
	json.NewEncoder(rw).Encode(v)
}
