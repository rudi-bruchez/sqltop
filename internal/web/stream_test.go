package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
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
// "eventually", but actually follows s.push (which newTestServer sets to
// 200ms, mirroring the requests tier per server.go's doc comment on push).
// The first send happens synchronously on connect, so the gap measured here
// is between the second and third events, both driven by the ticker.
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

	gap1, gap2 := t1.Sub(t0), t2.Sub(t1)
	// Generous bounds: this asserts the ticker is driving the sends, not
	// that CI hardware hits 200ms on the nose.
	for _, gap := range []time.Duration{gap1, gap2} {
		if gap < s.push/2 || gap > s.push*4 {
			t.Fatalf("gap between ticks = %v, want roughly the configured period %v", gap, s.push)
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
