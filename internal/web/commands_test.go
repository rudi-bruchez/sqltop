package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// commandCollector is a collector over a fake source, for the handlers that
// need one to talk to.
func commandCollector(t *testing.T) *collector.Collector {
	t.Helper()
	return collector.New(fake.New(nil), window.New(time.Minute, 1000), collector.NewBudget(50, config.Default().Tiers))
}

// commandServer is a server with a real collector behind it, so the period
// endpoint has something whose rate can actually be read back.
func commandServer(t *testing.T) *Server {
	t.Helper()
	col := commandCollector(t)
	srv, err := NewServer(col, window.New(time.Minute, 1000), config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// snapshotsInto points the s command at a temporary directory for one test.
func snapshotsInto(t *testing.T, dir string) {
	t.Helper()
	old := snapshotDir
	snapshotDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { snapshotDir = old })
}

var snapshotName = regexp.MustCompile(`^server-\d{4}-\d{2}-\d{2}-\d{6}(-\d)?\.html$`)

// TestSnapshotLandsBesideTheBinaryUnderTheAskedForName. The name is the
// request, verbatim: server-yyyy-mm-dd-hhmmss.html, in a snapshots
// directory at the executable's own location.
func TestSnapshotLandsBesideTheBinaryUnderTheAskedForName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snapshots")
	snapshotsInto(t, dir)
	srv := commandServer(t)

	body := "<!DOCTYPE html><html><body>frozen</body></html>"
	rw := httptest.NewRecorder()
	srv.snapshot(rw, httptest.NewRequest(http.MethodPost, "/api/snapshot", strings.NewReader(body)))
	if rw.Code != http.StatusOK {
		t.Fatalf("snapshot returned %d: %s", rw.Code, rw.Body)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no snapshots directory was created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("wrote %d files, want 1", len(entries))
	}
	if !snapshotName.MatchString(entries[0].Name()) {
		t.Errorf("wrote %q; the name has to be server-yyyy-mm-dd-hhmmss.html", entries[0].Name())
	}
	got, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("the file holds %q, not what was posted", got)
	}
}

// TestSnapshotNeverOverwritesOneFromTheSameSecond. The name resolves to the
// second, so two presses inside one second collide; losing the first would
// be losing a file somebody asked for.
func TestSnapshotNeverOverwritesOneFromTheSameSecond(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 30, 17, 4, 5, 0, time.UTC)

	base := at.Format("server-2006-01-02-150405")
	first, err := writeUnique(dir, base, ".html", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeUnique(dir, base, ".html", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the second snapshot of the same second overwrote the first")
	}
	if filepath.Base(first) != "server-2026-08-30-170405.html" {
		t.Errorf("first file is %q", filepath.Base(first))
	}
	if b, _ := os.ReadFile(first); string(b) != "one" {
		t.Errorf("the first file now holds %q", b)
	}
	if b, _ := os.ReadFile(second); string(b) != "two" {
		t.Errorf("the second file holds %q", b)
	}
}

func TestSnapshotRefusesWhatItShould(t *testing.T) {
	dir := t.TempDir()
	snapshotsInto(t, dir)
	srv := commandServer(t)

	t.Run("get", func(t *testing.T) {
		rw := httptest.NewRecorder()
		srv.snapshot(rw, httptest.NewRequest(http.MethodGet, "/api/snapshot", nil))
		if rw.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET returned %d, want 405", rw.Code)
		}
	})
	t.Run("empty", func(t *testing.T) {
		rw := httptest.NewRecorder()
		srv.snapshot(rw, httptest.NewRequest(http.MethodPost, "/api/snapshot", strings.NewReader("")))
		if rw.Code != http.StatusBadRequest {
			t.Errorf("an empty body returned %d, want 400", rw.Code)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		rw := httptest.NewRecorder()
		big := strings.NewReader(strings.Repeat("x", maxSnapshotBody+1))
		srv.snapshot(rw, httptest.NewRequest(http.MethodPost, "/api/snapshot", big))
		if rw.Code != http.StatusBadRequest {
			t.Errorf("an oversized body returned %d, want 400", rw.Code)
		}
	})

	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused request still wrote %d file(s)", len(entries))
	}
}

// TestPeriodChangesTheSamplingRate. The f command has to move what the
// collector actually asks the server for, not only what the browser draws:
// the sampling rate is the number the monitored instance pays for.
func TestPeriodChangesTheSamplingRate(t *testing.T) {
	srv := commandServer(t)
	before := srv.col.Period(model.TierRequests)

	rw := httptest.NewRecorder()
	srv.period(rw, httptest.NewRequest(http.MethodPost, "/api/period", strings.NewReader(`{"period":"5s"}`)))
	if rw.Code != http.StatusOK {
		t.Fatalf("period returned %d: %s", rw.Code, rw.Body)
	}
	after := srv.col.Period(model.TierRequests)
	if after == before {
		t.Fatalf("the request tier is still sampled every %s", after)
	}
	if after != 5*time.Second {
		t.Errorf("the request tier is sampled every %s, want 5s", after)
	}
}

// TestPeriodRefusesWhatTheFileWouldRefuse. A period from the interface goes
// through config.Validate, so the floor that stops a tight loop against the
// monitored server applies to a keypress exactly as it does to a typo in
// the file.
func TestPeriodRefusesWhatTheFileWouldRefuse(t *testing.T) {
	for _, body := range []string{
		`{"period":"0s"}`,
		`{"period":"1ms"}`,
		`{"period":"48h"}`,
		`{"period":"soon"}`,
		`{"period":""}`,
		`not json`,
	} {
		srv := commandServer(t)
		before := srv.col.Period(model.TierRequests)
		rw := httptest.NewRecorder()
		srv.period(rw, httptest.NewRequest(http.MethodPost, "/api/period", strings.NewReader(body)))
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400", body, rw.Code)
		}
		if got := srv.col.Period(model.TierRequests); got != before {
			t.Errorf("%s was refused and still moved the rate to %s", body, got)
		}
	}
}

func TestPeriodIsPostOnly(t *testing.T) {
	srv := commandServer(t)
	rw := httptest.NewRecorder()
	srv.period(rw, httptest.NewRequest(http.MethodGet, "/api/period", nil))
	if rw.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %d, want 405", rw.Code)
	}
}

// TestEveryCommandIsBothListedAndBound keeps the help and the keyboard in
// step. A command in the handler and not in the list is one nobody can
// discover; a command in the list and not in the handler is a printed lie.
// The same bargain as the dashboard and column catalogues.
func TestEveryCommandIsBothListedAndBound(t *testing.T) {
	src, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	// Both patterns take a whole key name, not one letter: the arrows are
	// bound as ArrowUp and ArrowDown, and a pattern that matched only single
	// letters passed over them in silence, which is the one thing this test
	// must not do.
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}\["([A-Za-z?]+)",`).FindAllStringSubmatch(between(string(src), "const COMMANDS = [", "\n];"), -1) {
		listed[m[1]] = true
	}
	bound := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}([A-Za-z?]+):`).FindAllStringSubmatch(between(string(src), "const KEYS = {", "\n};"), -1) {
		bound[m[1]] = true
	}
	if len(listed) < 7 || len(bound) < 7 {
		t.Fatalf("read %d listed and %d bound commands; there are seven", len(listed), len(bound))
	}
	if len(listed) == 0 || len(bound) == 0 {
		t.Fatalf("read %d listed and %d bound commands out of app.js; the shape this is parsed from has changed", len(listed), len(bound))
	}
	for k := range listed {
		if !bound[k] {
			t.Errorf("the help lists %q and no key is bound to it", k)
		}
	}
	for k := range bound {
		if !listed[k] {
			t.Errorf("%q is bound to a key and the help does not mention it, so nobody can find it", k)
		}
	}
}
