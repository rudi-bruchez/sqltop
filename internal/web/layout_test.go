package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/rudi-bruchez/sqltop/internal/config"
)

// layoutServer builds a server whose configuration lives in a temporary
// file, so a save has somewhere real to land.
func layoutServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sqltop.yaml")
	cfg := config.Default()
	cfg.Path = path
	if err := os.WriteFile(path, []byte("retention: 15m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(nil, nil, config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv.WithConfig(cfg), path
}

func postLayout(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/layout", strings.NewReader(body))
	rw := httptest.NewRecorder()
	srv.layout(rw, req)
	return rw
}

// TestSavedLayoutReachesTheFileAndTheNextClient is the whole point of the
// endpoint: spec section 8.2 puts a layout in the configuration file rather
// than in browser storage so it survives a change of browser and can be
// handed to a colleague. A save that only changed the running process would
// look identical until the next restart.
func TestSavedLayoutReachesTheFileAndTheNextClient(t *testing.T) {
	srv, path := layoutServer(t)

	rw := postLayout(t, srv, `{"view":"requests","columns":[
		{"field":"sql_text","show":true,"width":300},
		{"field":"spid","show":true,"width":60},
		{"field":"host","show":false,"width":95}]}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", rw.Code, rw.Body)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back config.Config
	if err := yaml.Unmarshal(b, &back); err != nil {
		t.Fatalf("the file the endpoint wrote does not parse: %v\n%s", err, b)
	}
	got := back.Layouts["default"].Views["requests"].Columns
	if len(got) < 3 || got[0].Field != "sql_text" || got[1].Field != "spid" {
		t.Fatalf("the saved order is %v; the posted order was sql_text, spid, host", fields(got))
	}
	if got[0].Width != 300 {
		t.Errorf("sql_text saved with width %d, want 300", got[0].Width)
	}
	if got[2].Show == nil || *got[2].Show {
		t.Errorf("host was posted switched off and the file says %v", got[2].Show)
	}

	// The running server has to agree with the file it just wrote, or the
	// next browser to connect gets the old layout back.
	grid := viewColumns(srv.gridColumns(), "requests")
	if len(grid) == 0 || grid[0].Field != "sql_text" {
		t.Fatalf("the next client would be sent %v; the saved order starts with sql_text", gridFields(grid))
	}
	for _, c := range grid {
		if c.Field == "host" && c.Show {
			t.Error("host is sent as shown although the saved layout switches it off")
		}
	}
}

// TestLayoutRefusesWhatTheCatalogueDoesNotKnow. The endpoint is behind the
// token like every other route, so this is not the trust boundary; it is
// there so a stale or buggy interface cannot quietly put a field into a
// file that outlives it and that a person reads later.
func TestLayoutRefusesWhatTheCatalogueDoesNotKnow(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown view", `{"view":"nowhere","columns":[{"field":"spid","show":true}]}`},
		{"unknown column", `{"view":"requests","columns":[{"field":"drop_table","show":true}]}`},
		{"column listed twice", `{"view":"requests","columns":[{"field":"spid","show":true},{"field":"spid","show":false}]}`},
		{"width outside the range", `{"view":"requests","columns":[{"field":"spid","show":true,"width":99999}]}`},
		{"not json", `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, path := layoutServer(t)
			before, _ := os.ReadFile(path)
			if rw := postLayout(t, srv, tc.body); rw.Code != http.StatusBadRequest {
				t.Fatalf("returned %d, want 400: %s", rw.Code, rw.Body)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Errorf("the configuration file was rewritten by a request that was refused:\n%s", after)
			}
		})
	}
}

// TestLayoutIsPostOnly. A GET that saved would be reachable from any link,
// image or prefetch a page could be made to issue.
func TestLayoutIsPostOnly(t *testing.T) {
	srv, _ := layoutServer(t)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rw := httptest.NewRecorder()
		srv.layout(rw, httptest.NewRequest(m, "/api/layout", nil))
		if rw.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s returned %d, want 405", m, rw.Code)
		}
	}
}

// TestLayoutBodyIsBounded: without a cap, one request can make the process
// read as much as it is fed.
func TestLayoutBodyIsBounded(t *testing.T) {
	srv, _ := layoutServer(t)
	huge := `{"view":"requests","columns":[` + strings.Repeat(`{"field":"spid","show":true},`, 20000) + `{"field":"spid"}]}`
	if len(huge) < maxLayoutBody {
		t.Fatalf("the test body is %d bytes, which is inside the %d byte cap it is meant to exceed", len(huge), maxLayoutBody)
	}
	if rw := postLayout(t, srv, huge); rw.Code != http.StatusBadRequest {
		t.Errorf("an oversized body returned %d, want 400", rw.Code)
	}
}

// TestSavedLayoutSurvivesAReadBack: what the endpoint writes has to be what
// Load accepts, or the tool writes a file it then refuses to start on.
func TestSavedLayoutSurvivesAReadBack(t *testing.T) {
	srv, path := layoutServer(t)
	if rw := postLayout(t, srv, `{"view":"requests","columns":[{"field":"cpu_ms","show":true,"width":90},{"field":"spid","show":false,"width":60}]}`); rw.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", rw.Code, rw.Body)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the tool cannot load the file it just wrote: %v", err)
	}
	cols := cfg.Columns("requests")
	if len(cols) == 0 || cols[0].Field != "cpu_ms" {
		t.Fatalf("reloaded order starts with %v, want cpu_ms first", fields(cols))
	}
	for _, c := range cols {
		if c.Field == "spid" && (c.Show == nil || *c.Show) {
			t.Error("spid was saved switched off and comes back on")
		}
	}
}

func fields(cols []config.ViewColumn) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Field
	}
	return out
}

// viewColumns picks one view out of what a client would be sent.
func viewColumns(views []GridView, id string) []GridCol {
	for _, v := range views {
		if v.ID == id {
			return v.Columns
		}
	}
	return nil
}

func gridFields(cols []GridCol) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Field
	}
	return out
}
