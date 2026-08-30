package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// loopbackRequest builds a request that would come from the page itself: a
// loopback Host header, so tests below that are not specifically about the
// Host check isolate the thing they actually test. httptest.NewRequest
// defaults Host to "example.com", which hostAllowed would now (correctly)
// refuse on its own and so must not be left in place by accident in a test
// that means to be checking something else, like the token.
func loopbackRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1"
	return req
}

// testTiers is config.Default().Tiers with a fast requests period, so the
// stream tests below do not have to wait out the real one-second default:
// fix round 1, task 14 made stream.go read the collector's own configured
// period on every tick rather than a value passed in separately, so this
// fixture's tiers are now the one and only place that period comes from.
func testTiers() config.Tiers {
	tiers := config.Default().Tiers
	tiers.Requests = config.Duration(80 * time.Millisecond)
	return tiers
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	w := window.New(time.Minute, 1000)
	w.Append(time.Now(), []model.RequestSample{{Ref: model.RequestRef{SessionID: 51}, SQLText: "SELECT 1"}})
	c := collector.New(fake.New(nil), w, collector.NewBudget(50, testTiers()))

	s, err := NewServer(c, w, config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTokenIsRequired(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a token", rec.Code)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t=wrong"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a wrong token", rec.Code)
	}
}

func TestCorrectTokenIsAccepted(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t="+s.token))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the run's token", rec.Code)
	}
}

// TestStatusEndpointCarriesTheObservationCost crosses the seam between
// Budget, which has always computed this figure, and the /api/status
// response, which used to have no field for it at all: spec section 10
// says an instrument that claims to bound its own cost should show it,
// and before this fix the only place the number reached the browser was
// interpolated into the throttle message, which does not render until the
// tool is already throttled.
func TestStatusEndpointCarriesTheObservationCost(t *testing.T) {
	w := window.New(time.Minute, 1000)
	bud := collector.NewBudget(50, testTiers())
	now := time.Now()
	bud.Observe(model.Cost{At: now, CPUMs: 0})
	bud.Observe(model.Cost{At: now.Add(time.Second), CPUMs: 80})
	c := collector.New(fake.New(nil), w, bud)

	s, err := NewServer(c, w, config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t="+s.token))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding /api/status: %v", err)
	}
	if got.CostMsPerSecond != 80 {
		t.Fatalf("costMsPerSecond = %v, want 80 (the observed server CPU per second)", got.CostMsPerSecond)
	}
}

// TestIndexPageHasNoSeparatelyFetchedSubresource is the fix round 1
// regression test for the critical bug a reviewer found by actually
// opening the printed URL in a browser rather than curling it with an
// explicit ?t=: a relative URL, <link href="style.css"> or <script
// src="app.js">, does not inherit the page's own query string, so the
// browser requested both with no token, authenticate refused both with
// 401, and the page that loaded was a bare unstyled heading with no grid
// and no stream. Every check this package ran before this fix round went
// through curl with the token spelled out on every request, which is
// exactly the method that cannot see this failure.
//
// index now composes style.css and app.js into the document it returns,
// so this asserts the page declares no subresource a browser would have to
// fetch on its own, rather than fetching whatever it does declare and
// requiring 200 on each: the chosen fix is that there is nothing left to
// fetch.
func TestIndexPageHasNoSeparatelyFetchedSubresource(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/?t="+s.token))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, needle := range []string{`href="style.css"`, `src="app.js"`, `<link rel="stylesheet"`} {
		if strings.Contains(body, needle) {
			t.Fatalf("index page still references %q as a separate URL: a relative reference does not inherit the ?t= query string, so a browser would request it unauthenticated and get 401", needle)
		}
	}
	if !strings.Contains(body, "<style>") || !strings.Contains(body, ":root") {
		t.Fatal("index page does not appear to carry style.css inlined")
	}
	if !strings.Contains(body, "<script>") || !strings.Contains(body, "gridScroll") {
		t.Fatal("index page does not appear to carry app.js inlined")
	}
}

// TestIndexPageServesOnlyTheRootPath proves the composed page is not
// silently reachable, and therefore servable without the substitutions
// above, from some other path a stray request might hit now that "/" is a
// handler function rather than a static file server with its own
// per-file routing.
func TestIndexPageServesOnlyTheRootPath(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/nonexistent?t="+s.token))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a path other than /", rec.Code)
	}
}

func TestURLIsLoopbackAndCarriesTheToken(t *testing.T) {
	s := newTestServer(t)
	u := s.URL()

	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatalf("URL = %q, want a loopback address: this interface will be able to kill sessions", u)
	}
	if !strings.Contains(u, s.token) {
		t.Fatalf("URL = %q, want the run's token in it", u)
	}
}

func TestListenerIsBoundToLoopback(t *testing.T) {
	s := newTestServer(t)
	if addr := s.listener.Addr().String(); !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("bound to %q, want 127.0.0.1 only", addr)
	}
}

// TestListenerRefusesAnyOtherAddress goes further than the string-prefix
// check above: it proves, by inspecting the actual socket address rather
// than a formatted string, that the bind is the specific IPv4 loopback
// address and not a wildcard (0.0.0.0, ::) or the IPv6 loopback form, either
// of which could still surprise someone reasoning only about "loopback".
// There is no other interface reachable in a sandboxed test environment to
// dial against and observe a refusal from, so this is the faithful way to
// prove non-reachability from anywhere but 127.0.0.1: the kernel simply
// never hands this socket a packet that did not arrive on that address.
func TestListenerRefusesAnyOtherAddress(t *testing.T) {
	s := newTestServer(t)
	tcp, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", s.listener.Addr())
	}
	if !tcp.IP.IsLoopback() {
		t.Fatalf("listener IP = %v, want a loopback address", tcp.IP)
	}
	if tcp.IP.String() != "127.0.0.1" {
		t.Fatalf("listener IP = %v, want exactly 127.0.0.1, not a wildcard or ::1", tcp.IP)
	}
}

// TestWrongTokenDoesNotRevealCloseness proves the refusal itself carries no
// signal about how close a guess was: a token wrong in its last hex digit
// gets the exact same status, body and headers as a token wrong in every
// digit. Constant-time comparison in authenticate is what makes this true;
// this test is what keeps it true if that ever regresses to a
// short-circuiting compare.
func TestWrongTokenDoesNotRevealCloseness(t *testing.T) {
	s := newTestServer(t)

	near := s.token[:len(s.token)-1] + flipHexDigit(s.token[len(s.token)-1])
	far := strings.Repeat("0", len(s.token))
	if far == s.token {
		far = strings.Repeat("1", len(s.token))
	}

	recNear := httptest.NewRecorder()
	s.Handler().ServeHTTP(recNear, loopbackRequest(http.MethodGet, "/api/status?t="+near))

	recFar := httptest.NewRecorder()
	s.Handler().ServeHTTP(recFar, loopbackRequest(http.MethodGet, "/api/status?t="+far))

	if recNear.Code != http.StatusUnauthorized || recFar.Code != http.StatusUnauthorized {
		t.Fatalf("codes = %d, %d, want 401 for both", recNear.Code, recFar.Code)
	}
	if recNear.Body.String() != recFar.Body.String() {
		t.Fatalf("bodies differ between a near-miss and a far-miss token: %q vs %q", recNear.Body.String(), recFar.Body.String())
	}
	if recNear.Header().Get("Content-Type") != recFar.Header().Get("Content-Type") {
		t.Fatal("content type differs between a near-miss and a far-miss token")
	}
}

func flipHexDigit(d byte) string {
	if d == '0' {
		return "1"
	}
	return "0"
}

// TestConcurrentClientsGetCorrectAnswers drives many goroutines at the same
// handler at once, half with the run's token and half without, and checks
// each gets the answer its own request deserves. Run with -race, this is
// what actually exercises the shared state authenticate and status touch
// (s.token, the collector and the window), rather than trusting that a
// single sequential test generalises.
func TestConcurrentClientsGetCorrectAnswers(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	const clients = 25
	var wg sync.WaitGroup
	errs := make(chan string, clients*2)

	for i := 0; i < clients; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t="+s.token))
			if rec.Code != http.StatusOK {
				errs <- "authenticated request got " + rec.Result().Status
			}
		}()
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t=wrong"))
			if rec.Code != http.StatusUnauthorized {
				errs <- "unauthenticated request got " + rec.Result().Status
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestServeShutsDownCleanlyWithoutLeakingGoroutines proves, by counting
// rather than assuming, that whatever Serve starts it also stops: it counts
// goroutines before starting the server, drives one real request through the
// real listener to prove the server actually came up, cancels the context,
// and waits for the goroutine count to come back down to where it started.
func TestServeShutsDownCleanlyWithoutLeakingGoroutines(t *testing.T) {
	s := newTestServer(t)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	var err error
	for time.Now().Before(deadline) {
		resp, err = http.Get(s.URL())
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never accepted a connection: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from a live server", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}

	deadline = time.Now().Add(2 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked: before = %d, after = %d", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGracefulShutdownForceClosesConnectionsPastTheGracePeriod proves the
// fallback in gracefulShutdown actually fires: a handler that never returns
// on its own (task 14's SSE stream will not either, for as long as a
// browser tab keeps reading) must not leave gracefulShutdown, and therefore
// Serve, hanging on http.Server.Shutdown's promise to only ever close idle
// connections. shutdownGrace is shrunk to keep this fast; without the
// srv.Close fallback this test would hang until the real test binary
// timeout rather than fail promptly.
func TestGracefulShutdownForceClosesConnectionsPastTheGracePeriod(t *testing.T) {
	oldGrace := shutdownGrace
	shutdownGrace = 50 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = oldGrace })

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})

	srv := &http.Server{
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(http.StatusOK)
			if f, ok := rw.(http.Flusher); ok {
				f.Flush()
			}
			<-block // held open past shutdownGrace on purpose
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	// Sized well past the response headers so one read drains all of them;
	// otherwise a second read after shutdown could still succeed on
	// leftover buffered header bytes rather than actually proving the
	// connection is closed.
	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("no response headers from the handler before shutdown: %v", err)
	}

	done := make(chan struct{})
	go func() {
		gracefulShutdown(srv)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gracefulShutdown did not return: it must force-close a connection still open past the grace period, not wait on it forever")
	}

	// The forced close must actually have ended the connection. A plain
	// read-deadline timeout is not proof of that: a still-open connection
	// with nothing arriving times out too, and a fallback-free version of
	// gracefulShutdown (bare srv.Shutdown, no srv.Close) still returns
	// promptly once its own context expires, it just leaves the connection
	// alive. So this must tell "closed" apart from "open but silent": only
	// a non-timeout error (EOF, or a reset) counts as the connection having
	// actually been force-closed. gracefulShutdown returning also only
	// means srv.Close was called, not that the FIN has necessarily reached
	// this end of the socket yet, so give that a moment first.
	time.Sleep(100 * time.Millisecond)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("connection is still open after gracefulShutdown returned")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection is still open after gracefulShutdown returned (read timed out rather than the connection closing)")
	}
}

// TestConstantTimeCompareIsUsedForTheToken guards a claim the comment on
// authenticate makes: that the token compare is constant time. No
// behavioural test from outside the package can tell subtle.ConstantTimeCompare
// apart from Go's built-in != on a real loopback round trip, tens of
// microseconds of jitter swallow a sub-100-nanosecond timing difference, so
// TestWrongTokenDoesNotRevealCloseness above would stay green even if
// authenticate were rewritten to use != directly. This reads the source
// instead. Unusual for a Go test, but it is the only way to make the
// comment's claim something this suite actually holds rather than merely
// asserts.
func TestConstantTimeCompareIsUsedForTheToken(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"crypto/subtle"`) {
		t.Fatal("server.go no longer imports crypto/subtle: the token compare must stay constant time")
	}
	if !strings.Contains(string(src), "subtle.ConstantTimeCompare(") {
		t.Fatal("server.go no longer calls subtle.ConstantTimeCompare: the token compare must stay constant time")
	}
}

// TestTokenIsRandomAnd128Bits is the one control spec section 4.3 names on
// its own: a per-run token. Nothing before this checked that two servers
// actually get different tokens, or that the token has the length NewServer's
// own doc comment claims (16 bytes of crypto/rand, hex-encoded to 32
// characters); a token pinned to a fixed string, or cut to a handful of
// bits, would have left every other test in this file green.
func TestTokenIsRandomAnd128Bits(t *testing.T) {
	a := newTestServer(t)
	b := newTestServer(t)

	if len(a.token) != 32 {
		t.Fatalf("token length = %d hex characters, want 32 (128 bits)", len(a.token))
	}
	if a.token == b.token {
		t.Fatal("two servers produced the same token")
	}
}

// TestEveryRouteRequiresTheToken walks every path Server.routes registers
// and checks each refuses a request without a token. This is a completeness
// test, not a proof: the reviewer's PoC for the mistake it guards against
// added a route on an outer mux that wrapped Handler's own return value, a
// route this test would not see because it walks routes(), not the mux
// authenticate wraps. What it does catch is the more likely version of the
// same mistake: a new path added inside routes() itself, or added to
// Handler's mux without going through securityHeaders(s.authenticate(...)),
// since either would still show up here as a 200.
func TestEveryRouteRequiresTheToken(t *testing.T) {
	s := newTestServer(t)
	rs, err := s.routes()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("routes() returned no routes to check")
	}

	h := s.Handler()
	for _, r := range rs {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, loopbackRequest(http.MethodGet, r.path))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", r.path, rec.Code)
		}
	}
}

// TestHostHeaderOutsideLoopbackIsRefused proves the DNS-rebinding defence:
// a request that otherwise carries the correct token is still refused when
// its Host header names something other than a loopback address.
func TestHostHeaderOutsideLoopbackIsRefused(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status?t="+s.token, nil)
	req.Host = "evil.attacker.example"

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a Host header outside the loopback allowlist", rec.Code)
	}
}

// TestHostHeaderVariantsAreAccepted checks the allowlist is neither empty
// nor accidentally narrower than spec: no Host header at all, 127.0.0.1,
// localhost and the bracketed IPv6 loopback, each with and without a port,
// must all still work. The server binds IPv4 only, so ::1 is not a second
// address anything can reach it on, but a user who types it, or whose
// environment resolves localhost over IPv6 first, must not get an
// unexplained unauthorized for a host that was never a hole.
func TestHostHeaderVariantsAreAccepted(t *testing.T) {
	s := newTestServer(t)
	for _, host := range []string{"", "127.0.0.1", "127.0.0.1:8420", "localhost", "localhost:8420", "[::1]", "[::1]:8420"} {
		req := httptest.NewRequest(http.MethodGet, "/api/status?t="+s.token, nil)
		req.Host = host

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Host = %q: status = %d, want 200", host, rec.Code)
		}
	}
}
