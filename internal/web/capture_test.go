package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// newTestServer's source cannot capture, which is the unavailable case. This
// one can, and hands the fake back so a test can say what the instance is
// already running.
func newCapturingTestServer(t *testing.T) (*Server, *fake.Capturing) {
	t.Helper()
	f := fake.NewCapturing(nil)
	w := window.New(time.Minute, 1000)
	c := collector.New(f, w, collector.NewBudget(50, testTiers()))

	s, err := NewServer(c, w, config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A capture left running outlives the test: its drain goroutine
		// keeps polling and its trace file stays open.
		if m := c.Captures(); m != nil {
			m.Stop(context.Background(), model.StopByShutdown)
		}
		s.Close()
	})
	return s, f
}

type captureResponse struct {
	State model.CaptureState        `json:"state"`
	Rows  []model.CapturedStatement `json:"rows"`
}

func getCapture(t *testing.T, s *Server) captureResponse {
	t.Helper()
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, loopbackRequest(http.MethodGet, "/api/capture?t="+s.token))
	if rw.Code != http.StatusOK {
		t.Fatalf("GET /api/capture returned %d: %s", rw.Code, rw.Body)
	}
	var got captureResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding /api/capture: %v", err)
	}
	return got
}

func TestCaptureEndpointTogglesAndReports(t *testing.T) {
	s, _ := newCapturingTestServer(t)

	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, loopbackRequest(http.MethodPost, "/api/capture?spid=51&t="+s.token))
	if rw.Code != http.StatusOK {
		t.Fatalf("POST returned %d: %s", rw.Code, rw.Body)
	}

	got := getCapture(t, s)
	if !got.State.Active || got.State.SessionID != 51 {
		t.Errorf("state is %+v, want an active capture on 51", got.State)
	}
	if !got.State.Available {
		t.Error("a source that can capture reported capture unavailable")
	}
	if got.Rows == nil {
		t.Error("rows is null; the panel iterates it")
	}
}

func TestCaptureEndpointRefusesAMissingSpid(t *testing.T) {
	s, _ := newCapturingTestServer(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, loopbackRequest(http.MethodPost, "/api/capture?t="+s.token))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("POST with no spid returned %d, want 400", rw.Code)
	}
}

func TestCaptureEndpointSaysWhyWhenUnavailable(t *testing.T) {
	// A greyed key with no explanation is the failure this endpoint exists
	// to avoid, so a source with no Capturer answers 200 and a reason.
	s := newTestServer(t)
	got := getCapture(t, s)
	if got.State.Available {
		t.Fatal("a source with no Capturer reported capture available")
	}
	if got.State.Why == "" {
		t.Error("unavailable with no reason")
	}
	if got.Rows == nil {
		t.Error("rows is null; the panel iterates it")
	}
}

func TestCaptureEndpointRefusesToggleWhenUnavailable(t *testing.T) {
	s := newTestServer(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, loopbackRequest(http.MethodPost, "/api/capture?spid=51&t="+s.token))
	if rw.Code != http.StatusOK {
		t.Fatalf("POST returned %d, want the same 200 and reason a GET gets", rw.Code)
	}
}

func TestOtherCapturesReachTheState(t *testing.T) {
	// Others is what warns a second watcher it is doubling the dispatch
	// cost on the monitored workload, and nothing else will tell them.
	s, f := newCapturingTestServer(t)
	since := time.Now().Add(-time.Minute)
	f.Running = []model.CaptureNote{{SessionID: 99, Since: since}}

	got := getCapture(t, s)
	if len(got.State.Others) != 1 || got.State.Others[0].SessionID != 99 {
		t.Fatalf("others is %+v, want the capture on 99 the instance is already running", got.State.Others)
	}
}

// startCapture toggles a capture on spid through the endpoint, which is the
// only way in from outside this package's own handlers.
func startCapture(t *testing.T, s *Server, spid string) {
	t.Helper()
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, loopbackRequest(http.MethodPost, "/api/capture?spid="+spid+"&t="+s.token))
	if rw.Code != http.StatusOK {
		t.Fatalf("POST /api/capture returned %d: %s", rw.Code, rw.Body)
	}
}

// openStream connects a client to the event stream and reads its first
// snapshot, so the handler has certainly registered itself by the time this
// returns. The returned function disconnects it.
func openStream(t *testing.T, base, token string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/stream?t="+token, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	readOneSnapshot(t, bufio.NewScanner(resp.Body))
	return func() {
		cancel()
		resp.Body.Close()
	}
}

// shortGrace makes the browser-gone timer testable without a thirty second
// wait.
func shortGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := captureGrace
	captureGrace = d
	t.Cleanup(func() { captureGrace = old })
}

func waitForStop(t *testing.T, f *fake.Capturing, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if len(f.Stopped()) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return len(f.Stopped()) > 0
}

func TestShutdownStopsARunningCapture(t *testing.T) {
	// A capture surviving the process is exactly the residue this design is
	// arranged around not producing.
	s, f := newCapturingTestServer(t)
	startCapture(t, s, "51")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if got := f.Stopped(); len(got) != 1 {
		t.Fatalf("shutdown dropped %d event sessions, want the one that was running", len(got))
	}
	if st := getCapture(t, s).State; st.Active || st.Stopped != model.StopByShutdown.String() {
		t.Errorf("state after shutdown is %+v, want stopped by shutdown", st)
	}
}

func TestTheLastBrowserLeavingStopsTheCapture(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	s, f := newCapturingTestServer(t)
	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	disconnect := openStream(t, httpSrv.URL, s.token)
	startCapture(t, s, "51")
	disconnect()

	if !waitForStop(t, f, 5*time.Second) {
		t.Fatal("the last browser left and the event session is still running on the server")
	}
	if st := getCapture(t, s).State; st.Stopped != model.StopByBrowserGone.String() {
		t.Errorf("stopped for %q, want the browser having gone", st.Stopped)
	}
}

func TestAReloadDoesNotStopTheCapture(t *testing.T) {
	// A reload is a disconnection, which is the whole reason the grace
	// period exists rather than an immediate stop.
	shortGrace(t, 300*time.Millisecond)
	s, f := newCapturingTestServer(t)
	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	disconnect := openStream(t, httpSrv.URL, s.token)
	startCapture(t, s, "51")
	disconnect()
	reconnected := openStream(t, httpSrv.URL, s.token)
	defer reconnected()

	time.Sleep(3 * captureGrace)
	if got := f.Stopped(); len(got) != 0 {
		t.Fatal("reloading the page killed the capture")
	}
	if st := getCapture(t, s).State; !st.Active {
		t.Errorf("state after a reload is %+v, want the capture still running", st)
	}
}

func TestPressingCWithoutTheFlagOpensThePanelAndSaysSo(t *testing.T) {
	// Without -capture the key has one job: open the panel and name the
	// flag. An error status cannot do that, because the panel only opens on
	// a 200 and the reason would fade with the message that carried it.
	s, f := newCapturingTestServer(t)
	f.Refuse = "capture is off; start sqltop with -capture to allow it"

	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, loopbackRequest(http.MethodPost, "/api/capture?spid=51&t="+s.token))
	if rw.Code != http.StatusOK {
		t.Fatalf("POST returned %d, want the 200 that opens the panel: %s", rw.Code, rw.Body)
	}

	got := getCapture(t, s)
	if got.State.Available || got.State.Active {
		t.Errorf("state is %+v, want unavailable and inactive", got.State)
	}
	if !strings.Contains(got.State.Why, "-capture") {
		t.Errorf("the reason is %q; it has to name the flag", got.State.Why)
	}
	if n := len(f.Started()); n != 0 {
		t.Errorf("the source was asked to start %d captures with the flag off", n)
	}
}
