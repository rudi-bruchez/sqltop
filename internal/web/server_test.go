package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
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

func newTestServer(t *testing.T) *Server {
	t.Helper()
	w := window.New(time.Minute, 1000)
	w.Append(time.Now(), []model.RequestSample{{Ref: model.RequestRef{SessionID: 51}, SQLText: "SELECT 1"}})
	c := collector.New(fake.New(nil), w, collector.NewBudget(50, config.Default().Tiers))

	s, err := NewServer(c, w, config.Server{Port: 0}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.listener.Close() })
	return s
}

func TestTokenIsRequired(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a token", rec.Code)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?t=wrong", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a wrong token", rec.Code)
	}
}

func TestCorrectTokenIsAccepted(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?t="+s.token, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the run's token", rec.Code)
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
	s.Handler().ServeHTTP(recNear, httptest.NewRequest(http.MethodGet, "/api/status?t="+near, nil))

	recFar := httptest.NewRecorder()
	s.Handler().ServeHTTP(recFar, httptest.NewRequest(http.MethodGet, "/api/status?t="+far, nil))

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
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?t="+s.token, nil))
			if rec.Code != http.StatusOK {
				errs <- "authenticated request got " + rec.Result().Status
			}
		}()
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?t=wrong", nil))
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
