package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// fresh sends every request on a new connection, so no test is answered on a
// kept-alive socket by whichever server happened to accept it first.
var fresh = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}

var testOptions = map[string]any{
	"methods":  []map[string]any{{"id": "sql", "label": "SQL Server login", "fields": []string{"login", "password"}}},
	"env_path": "/somewhere/.env",
}

// askConnection starts a connect phase on a fresh listener. The channel
// receives what AskConnection returns.
func askConnection(t *testing.T, connect ConnectFunc) (*Listener, string, <-chan error, context.CancelFunc) {
	t.Helper()
	l, err := Listen(config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		errc <- l.AskConnection(ctx, testOptions, connect)
		close(returned)
	}()
	// A phase still running when its test ends shuts down afterwards, and
	// its shutdown reads shutdownGrace, which a later test writes. Wait for it.
	t.Cleanup(func() {
		cancel()
		<-returned
	})
	return l, "http://" + l.ln.Addr().String(), errc, cancel
}

func postJSON(t *testing.T, url, body string) (int, map[string]string) {
	t.Helper()
	res, err := fresh.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func succeed(context.Context, []byte) (ConnectResult, error) {
	return ConnectResult{DSN: "sqlserver://sa:xxxxx@db01"}, nil
}

func TestConnectPhaseNeedsTheToken(t *testing.T) {
	called := 0
	l, base, _, _ := askConnection(t, func(context.Context, []byte) (ConnectResult, error) {
		called++
		return ConnectResult{}, nil
	})
	for _, path := range []string{"/", "/api/connect"} {
		res, err := fresh.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, res.StatusCode)
		}
		req, _ := http.NewRequest(http.MethodGet, base+path+"?t="+l.token, nil)
		req.Host = "evil.attacker.example"
		res, err = fresh.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with a foreign Host = %d, want 401", path, res.StatusCode)
		}
	}
	if code, _ := postJSON(t, base+"/api/connect", `{}`); code != http.StatusUnauthorized {
		t.Errorf("POST without a token = %d, want 401", code)
	}
	if called != 0 {
		t.Errorf("the attempt ran %d times for requests without a token", called)
	}
}

func TestConnectPhaseServesThePageAndTheOptions(t *testing.T) {
	l, base, _, _ := askConnection(t, succeed)
	q := "?t=" + l.token

	res, err := fresh.Get(base + "/" + q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="connectForm"`) {
		t.Fatalf("GET / = %d without the form", res.StatusCode)
	}
	// The one page that takes a password carries the monitor's headers.
	if res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the connect page is served with Referrer-Policy %q and Cache-Control %q",
			res.Header.Get("Referrer-Policy"), res.Header.Get("Cache-Control"))
	}
	for _, sub := range []string{`href="style.css"`, `src="connect.js"`} {
		if strings.Contains(string(body), sub) {
			t.Errorf("the page still asks the browser for %s, which a relative URL fetches without the token", sub)
		}
	}

	res, err = fresh.Get(base + "/api/connect" + q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"env_path":"/somewhere/.env"`) {
		t.Errorf("the options came back as %s", body)
	}

	// The page polls this route to learn that the monitor has taken over.
	res, err = fresh.Get(base + "/api/status" + q)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/status during the connect phase = %d, want 404", res.StatusCode)
	}
}

func TestConnectRefusesOtherMethods(t *testing.T) {
	l, base, _, _ := askConnection(t, succeed)
	req, _ := http.NewRequest(http.MethodPut, base+"/api/connect?t="+l.token, strings.NewReader("{}"))
	res, err := fresh.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want 405", res.StatusCode)
	}
}

func TestSecondAttemptWhileOneRunsIsRefusedAtOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l, base, done, _ := askConnection(t, func(context.Context, []byte) (ConnectResult, error) {
		close(entered)
		<-release
		return ConnectResult{DSN: "x"}, nil
	})
	url := base + "/api/connect?t=" + l.token
	first := make(chan int, 1)
	go func() {
		// Not postJSON: t.Fatal must not be called from this goroutine.
		res, err := fresh.Post(url, "application/json", strings.NewReader(`{}`))
		if err != nil {
			first <- -1
			return
		}
		res.Body.Close()
		first <- res.StatusCode
	}()
	<-entered
	start := time.Now()
	code, body := postJSON(t, url, `{}`)
	if code != http.StatusConflict {
		t.Errorf("second POST = %d, want 409", code)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("the second POST waited %v for the first; it must be refused at once", el)
	}
	if body["error"] == "" {
		t.Error("the 409 carries no message")
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("first POST = %d, want 200", code)
	}
	if err := <-done; err != nil {
		t.Errorf("AskConnection returned %v after a success", err)
	}
}

func TestConnectErrorReachesThePage(t *testing.T) {
	calls := 0
	l, base, _, _ := askConnection(t, func(context.Context, []byte) (ConnectResult, error) {
		calls++
		if calls == 1 {
			return ConnectResult{}, &ConnectError{Status: http.StatusBadGateway, Message: "boom", Hint: "try x"}
		}
		return ConnectResult{}, errors.New("plain")
	})
	url := base + "/api/connect?t=" + l.token
	code, body := postJSON(t, url, `{}`)
	if code != http.StatusBadGateway || body["error"] != "boom" || body["hint"] != "try x" {
		t.Errorf("a ConnectError came out as %d %v", code, body)
	}
	code, body = postJSON(t, url, `{}`)
	if code != http.StatusBadGateway || body["error"] != "plain" {
		t.Errorf("a plain error came out as %d %v", code, body)
	}
}

func TestAttemptHasADeadline(t *testing.T) {
	old := connectTimeout
	connectTimeout = 100 * time.Millisecond
	t.Cleanup(func() { connectTimeout = old })
	l, base, _, _ := askConnection(t, func(ctx context.Context, _ []byte) (ConnectResult, error) {
		<-ctx.Done()
		return ConnectResult{}, ctx.Err()
	})
	start := time.Now()
	code, _ := postJSON(t, base+"/api/connect?t="+l.token, `{}`)
	if code != http.StatusBadGateway {
		t.Errorf("an attempt past its deadline = %d, want 502", code)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("the attempt ran %v; its context should have ended it at 100ms", el)
	}
}

// TestHandoverAnswersARequestSentBetweenTheTwoServers is the claim the
// design rests on: while nobody accepts, a request waits in the kernel's
// queue on the socket that never closed, and the monitor answers it.
func TestHandoverAnswersARequestSentBetweenTheTwoServers(t *testing.T) {
	l, base, done, _ := askConnection(t, succeed)
	if code, body := postJSON(t, base+"/api/connect?t="+l.token, `{}`); code != http.StatusOK || body["dsn"] == "" {
		t.Fatalf("POST = %d %v", code, body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AskConnection did not return after a successful attempt")
	}

	got := make(chan int, 1)
	go func() {
		res, err := fresh.Get(base + "/api/status?t=" + l.token)
		if err != nil {
			got <- -1
			return
		}
		res.Body.Close()
		got <- res.StatusCode
	}()
	time.Sleep(200 * time.Millisecond) // the request is queued; nobody accepts yet

	w := window.New(time.Minute, 1000)
	c := collector.New(fake.New(nil), w, collector.NewBudget(50, testTiers()))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = NewServerOn(c, w, l).Serve(ctx)
	}()
	// Serve's shutdown reads shutdownGrace, which the next test writes: it
	// must be over before this test is.
	defer func() {
		cancel()
		<-served
	}()

	select {
	case code := <-got:
		if code != http.StatusOK {
			t.Errorf("the request sent during the handover got %d, want 200 from the monitor", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request sent during the handover was never answered")
	}
}

// TestHandoverIsNotHeldByAnIdleConnection: net/http holds Shutdown five
// seconds for a connection that has sent nothing, which is what a browser's
// speculative connection is. The grace cuts it short.
func TestHandoverIsNotHeldByAnIdleConnection(t *testing.T) {
	old := shutdownGrace
	shutdownGrace = 200 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })

	l, base, done, _ := askConnection(t, succeed)
	idle, err := net.Dial("tcp", l.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	time.Sleep(100 * time.Millisecond) // accepted, and silent

	if code, _ := postJSON(t, base+"/api/connect?t="+l.token, `{}`); code != http.StatusOK {
		t.Fatalf("POST = %d", code)
	}
	start := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if el := time.Since(start); el > 1500*time.Millisecond {
			t.Errorf("the handover took %v with an idle connection open; the grace is %v", el, shutdownGrace)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("AskConnection did not return")
	}
}

func TestAskConnectionReturnsWhenCancelled(t *testing.T) {
	_, _, done, cancel := askConnection(t, succeed)
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("AskConnection returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AskConnection did not return after its context was cancelled")
	}
}
