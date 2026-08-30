package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

func TestStreamPushesSnapshots(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", ct)
	}

	var sawEvent, sawData bool
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && !(sawEvent && sawData) {
		line := sc.Text()
		if line == "event: snapshot" {
			sawEvent = true
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"rows"`) {
			sawData = true
		}
	}
	if !sawEvent || !sawData {
		t.Fatal("the stream must emit a snapshot event carrying rows within a few seconds")
	}
}

func TestStreamRequiresTheToken(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// readOneSnapshot advances sc past the "event: snapshot" line and returns the
// decoded payload from the following "data: " line. It leaves the scanner
// positioned right after that event, so a second call reads the next one.
func readOneSnapshot(t *testing.T, sc *bufio.Scanner) SnapshotPayload {
	t.Helper()
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			var p SnapshotPayload
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p); err != nil {
				t.Fatalf("decoding snapshot: %v", err)
			}
			return p
		}
	}
	t.Fatal("stream ended before a snapshot arrived")
	return SnapshotPayload{}
}

// TestStreamPushesAtTheConfiguredPeriod proves the tick period isn't just
// "eventually", but actually follows the collector's own requests-tier
// period (testTiers sets it to 80ms; fix round 1, task 14 made stream.go
// read that live from s.col.Period rather than a value the server used to
// cache). The first send happens synchronously on connect, so the gap
// measured here is between the second and third events, both driven by
// the timer.
func TestStreamPushesAtTheConfiguredPeriod(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	readOneSnapshot(t, sc) // the immediate, synchronous send on connect
	t0 := time.Now()
	readOneSnapshot(t, sc)
	t1 := time.Now()
	readOneSnapshot(t, sc)
	t2 := time.Now()

	want := s.col.Period(model.TierRequests)
	gap1, gap2 := t1.Sub(t0), t2.Sub(t1)
	// Generous bounds: this asserts the timer is driving the sends, not
	// that CI hardware hits 80ms on the nose.
	for _, gap := range []time.Duration{gap1, gap2} {
		if gap < want/2 || gap > want*4 {
			t.Fatalf("gap between ticks = %v, want roughly the configured period %v", gap, want)
		}
	}
}

// TestStreamHandlerReturnsWhenClientDisconnects proves, by counting
// goroutines rather than assuming, that a client going away frees the
// goroutine net/http parked in the handler for it. Without this, a browser
// tab closed without triggering a graceful shutdown (the ordinary case: the
// user just navigates away) would leak one goroutine, and a ticker, per
// visit for as long as the process runs.
func TestStreamHandlerReturnsWhenClientDisconnects(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// Warm up the server and let any setup goroutines settle before taking
	// the baseline, so the comparison below is about the stream connection
	// alone.
	warmResp, err := http.Get(srv.URL + "/api/status?t=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, warmResp.Body)
	warmResp.Body.Close()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	readOneSnapshot(t, sc) // prove the handler actually started streaming

	// This is what a browser tab closing looks like from the server's side:
	// the client goes away without any cooperation from the handler.
	cancel()
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want <= %d (baseline) after the client disconnected", after, before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStreamGivesEachClientItsOwnReferenceTable is the regression test for
// the bug server.go's doc comment on the old stream stub warns about: a
// shared Encoder marks a reference "sent" the first time any client
// receives it, so a second client that joins afterwards would never get
// that reference at all and would render blank SQL, program, login and
// host cells for that session. Each connection here gets a fresh *Encoder
// (stream.go), so both clients must see the reference on their own first
// event.
func TestStreamGivesEachClientItsOwnReferenceTable(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	connect := func() (*http.Response, context.CancelFunc) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, cancel
	}

	respA, cancelA := connect()
	defer cancelA()
	scA := bufio.NewScanner(respA.Body)
	payloadA := readOneSnapshot(t, scA)
	respA.Body.Close()

	// The reference key is a fingerprint no client-side code computes on
	// its own; the test window holds exactly one session, so the single
	// entry in Refs is it.
	var keyA string
	var got Ref
	for k, v := range payloadA.Refs {
		keyA = k
		got = v
	}
	if keyA == "" || got.SQL != "SELECT 1" {
		t.Fatalf("client A's first snapshot Refs = %+v, want a reference for session 51 carrying its SQL text", payloadA.Refs)
	}

	// Client B connects after A has already consumed this reference. Under
	// a shared encoder this Refs map would come back empty.
	respB, cancelB := connect()
	defer cancelB()
	scB := bufio.NewScanner(respB.Body)
	payloadB := readOneSnapshot(t, scB)
	respB.Body.Close()

	gotB, ok := payloadB.Refs[keyA]
	if !ok {
		t.Fatalf("client B's first snapshot has no reference for key %q; Refs = %+v: a shared Encoder would produce exactly this blank grid for the second client", keyA, payloadB.Refs)
	}
	if gotB.SQL != "SELECT 1" {
		t.Fatalf("client B's reference SQL = %q, want %q", gotB.SQL, "SELECT 1")
	}
}

// TestServeCancelsInFlightStreamRequestsPromptlyOnShutdown proves
// stream.go's own claim: the handler returns as soon as the request
// context is cancelled, without waiting on Serve's shutdownGrace fallback
// (fix round 1, task 14). Before Serve's http.Server carried a
// BaseContext, http.Server.Shutdown did not cancel any request context on
// its own, so this same scenario, a stream open when shutdown is asked
// for, only unblocked once shutdownGrace's force-close fired: 2 seconds by
// default, one held-open browser tab away from every Ctrl-C hanging that
// long. shutdownGrace is left at its real default here on purpose, so a
// regression back to the force-close path would show up as this test
// timing out near 2s rather than passing early on a shrunk grace period
// that was never really exercised.
func TestServeCancelsInFlightStreamRequestsPromptlyOnShutdown(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	streamURL := "http://" + s.listener.Addr().String() + "/api/stream?t=" + s.token
	var resp *http.Response
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(streamURL)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("could not connect to the stream: %v", err)
	}
	sc := bufio.NewScanner(resp.Body)
	readOneSnapshot(t, sc) // prove the stream is actually open, not merely accepted

	t0 := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("Serve did not return with a stream open: BaseContext is not cancelling in-flight request contexts on shutdown")
	}
	if elapsed := time.Since(t0); elapsed > 500*time.Millisecond {
		t.Fatalf("Serve took %v to return with a stream open, want well under shutdownGrace (%v): the request context is not being cancelled promptly by shutdown", elapsed, shutdownGrace)
	}
	resp.Body.Close()
}

// feedBudget drives bud with one-second ticks of msPerSecond, starting from
// *total (which it advances across calls, matching the collector package's
// own feed helper in budget_test.go: a fresh local counter per call would
// make the next call's first reading smaller than the previous one, which
// Budget.Observe correctly reads as a session reset and skips).
func feedBudget(bud *collector.Budget, total *int64, start time.Time, seconds int, msPerSecond int64) {
	for i := 1; i <= seconds; i++ {
		*total += msPerSecond
		bud.Observe(model.Cost{At: start.Add(time.Duration(i) * time.Second), CPUMs: *total})
	}
}

// TestStreamPeriodFollowsTheThrottleMidConnection is the regression test
// for the bug this fix round reported: the stream used to read its period
// once, at connect time, while the collector's own tier goroutines re-read
// Budget.Period on every iteration, so under budget pressure tier A would
// double and the stream would keep re-marshalling and resending a window
// that was no longer moving at the old rate. It holds one connection open
// across the whole test and escalates the budget while that connection is
// live, which is what a busy server actually looks like: the throttle
// engaging mid-session, not at connect time.
//
// The tick immediately after Observe pushes the budget over its limit
// still reflects the period the in-flight timer was already primed with;
// only the Reset that follows that tick picks up the freshly escalated
// value. So this reads one baseline gap, escalates, then reads two more
// gaps and asserts the second of those two, not the first, has widened.
func TestStreamPeriodFollowsTheThrottleMidConnection(t *testing.T) {
	w := window.New(time.Minute, 1000)
	w.Append(time.Now(), []model.RequestSample{{Ref: model.RequestRef{SessionID: 51}, SQLText: "SELECT 1"}})

	tiers := testTiers()
	tiers.Requests = config.Duration(80 * time.Millisecond)
	tiers.Counters = config.Duration(80 * time.Millisecond)
	tiers.Space = config.Duration(80 * time.Millisecond)
	bud := collector.NewBudget(50, tiers)
	c := collector.New(fake.New(nil), w, bud)

	s, err := NewServer(c, w, config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)

	readOneSnapshot(t, sc) // the synchronous send on connect
	base0 := time.Now()
	readOneSnapshot(t, sc)
	base1 := time.Now()
	baseGap := base1.Sub(base0)
	if baseGap > 200*time.Millisecond {
		t.Fatalf("test setup: baseline gap = %v, want well under 200ms before any escalation", baseGap)
	}

	// Escalate to level 3 (tier A, requests) exactly as
	// TestStillOverBudgetDegradesCountersThenRequests does in
	// budget_test.go, while the connection above is still open.
	var total int64
	now := time.Now()
	feedBudget(bud, &total, now, 15, 80)
	feedBudget(bud, &total, now.Add(15*time.Second), 15, 80)
	feedBudget(bud, &total, now.Add(30*time.Second), 15, 80)

	want := tiers.Requests.Std() * 2
	if got := c.Period(model.TierRequests); got != want {
		t.Fatalf("test setup: c.Period(TierRequests) = %v, want %v after escalating to level 3", got, want)
	}

	readOneSnapshot(t, sc) // still the pre-escalation timer, already in flight
	t1 := time.Now()
	readOneSnapshot(t, sc) // this one was Reset with the escalated period
	t2 := time.Now()

	gap := t2.Sub(t1)
	if gap < want/2 {
		t.Fatalf("gap after escalation = %v, want roughly %v (doubled): the stream must re-read the collector's throttled period on every tick, not the period observed when the connection opened", gap, want)
	}
}

// TestStreamCadenceFollowsThePeriodEndpoint closes the loop the f command
// walks: a keypress posts to /api/period, the endpoint moves the request
// tier's base, and the stream, which re-reads that period on every tick,
// starts pushing at the new rate on the connection that is already open.
// Each of those three steps had a test and the whole path did not, which is
// exactly the shape somebody doubts when the interface looks wrong.
func TestStreamCadenceFollowsThePeriodEndpoint(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	readOneSnapshot(t, sc) // the send on connect

	before := s.col.Period(model.TierRequests)
	slower := before * 6
	body := fmt.Sprintf(`{"period":%q}`, slower)
	pr, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/period?t="+s.token, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pres, err := http.DefaultClient.Do(pr)
	if err != nil {
		t.Fatal(err)
	}
	pres.Body.Close()
	if pres.StatusCode != http.StatusOK {
		t.Fatalf("period returned %d", pres.StatusCode)
	}

	// The tick already in flight was armed at the old period, so one is
	// allowed to arrive early; the two after it must not.
	readOneSnapshot(t, sc)
	t0 := time.Now()
	readOneSnapshot(t, sc)
	gap := time.Since(t0)
	if gap < slower/2 {
		t.Errorf("the stream is still pushing every %v after the period was set to %v; the f command would look like it did nothing", gap, slower)
	}

	// And the status the client reads has to agree, or the footer says one
	// rate while the data arrives at another.
	if got := readOneSnapshot(t, sc).Status.PeriodMs; got != slower.Milliseconds() {
		t.Errorf("the snapshot reports a period of %d ms, want %d", got, slower.Milliseconds())
	}
}
