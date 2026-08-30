package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
