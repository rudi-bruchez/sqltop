package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// TestEndToEndInABrowser loads the real page in a real browser and drives
// the things only a browser can answer for.
//
// It exists because of an asymmetry this package could not otherwise close:
// the Go side has a hundred and eighty tests and the JavaScript side had
// two functions reachable from any of them, while 0.2 was mostly a release
// of interface. Everything below had been verified once, by hand, by
// someone driving a browser and reading the answers. This is that, kept.
//
// It is also the only kind of check that finds the class of bug that has
// actually shipped here: a page whose stylesheet 401s because a relative
// URL does not inherit a query string, and a favicon request nobody wrote
// that gets refused for want of a token. curl finds neither.
//
// Hermetic on purpose: a fake source, no container, no network. It skips
// when chromium or deno is missing rather than failing, on the same terms
// as the linter gate, so a machine without them still builds and tests.
func TestEndToEndInABrowser(t *testing.T) {
	chrome := lookChromium()
	if chrome == "" {
		t.Skip("no chromium-browser or chromium on PATH; this test drives the real page in a real browser")
	}
	deno := findDeno()
	if deno == "" {
		t.Skip("deno not installed; the DevTools protocol needs a WebSocket and Go's standard library has none")
	}

	srv, stop := browserTestServer(t)
	defer stop()

	// Not t.TempDir: chromium goes on writing into its profile while it
	// shuts down, and the framework's cleanup runs into a directory that
	// is not empty yet and fails the test over nothing. This one is
	// removed after the process is confirmed gone, and a leftover
	// directory in the system temp is not worth failing a test for.
	profile, err := os.MkdirTemp("", "sqltop-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome,
		"--headless", "--disable-gpu", "--no-sandbox",
		"--window-size=1600,1000",
		"--remote-debugging-port=0",
		"--user-data-dir="+profile,
		"about:blank")
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start %s: %v", chrome, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.RemoveAll(profile)
	}()

	port, err := devToolsPort(profile, 30*time.Second)
	if err != nil {
		t.Fatalf("%v; chromium did not report a debugging port", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	driver := filepath.Join("testdata", "e2e-driver.js")
	run := exec.CommandContext(ctx, deno, "run", "--quiet", "--allow-net=127.0.0.1", driver, srv.URL(), port)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}

	var got e2eResult
	line := lastJSONLine(string(out))
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("could not read the driver's report: %v\nraw output:\n%s", err, out)
	}

	t.Logf("observed: %d rows, %d headers, %d tiles in %d groups, styles %d, links %d, scripts %d",
		got.RowsSeen, got.Page.Headers, got.Page.Tiles, len(got.Page.Groups),
		got.Page.StyleTags, got.Page.LinkTags, got.Page.ScriptSrc)
	t.Logf("filter: %d of %d rows, then %d with a second column; anchor kept=%v visible=%v, scroll %d to %d",
		got.FilterOne.Rows, got.FilterOne.Total, got.FilterAnd.Rows,
		got.AnchorKeeps.StillPresent, got.AnchorKeeps.Visible,
		got.AnchorKeeps.ScrollBefore, got.AnchorKeeps.ScrollAfter)
	t.Logf("fold: grid height %d then %d then %d", got.Fold.Before, got.Fold.AfterGroup, got.Fold.AfterAll)

	if len(got.Problems) > 0 {
		t.Errorf("the browser reported %d problem(s) loading or running the page:\n  %s",
			len(got.Problems), strings.Join(got.Problems, "\n  "))
	}
	if got.RowsSeen == 0 {
		t.Fatal("no rows ever arrived; the page never received a snapshot, so nothing below means anything")
	}

	// The page must fetch nothing. Everything is composed inline because a
	// relative URL does not carry the token, and the icon is a data URI
	// because a browser asks for /favicon.ico unprompted.
	if got.Page.LinkTags != 0 || got.Page.ScriptSrc != 0 {
		t.Errorf("the page fetches subresources: %d stylesheets, %d scripts; both would be refused for want of a token",
			got.Page.LinkTags, got.Page.ScriptSrc)
	}
	if got.Page.StyleTags == 0 {
		t.Error("no inline <style>; the stylesheet did not make it into the document")
	}
	if !got.Page.IconIsData {
		t.Error("the icon is not a data URI, so the browser will request /favicon.ico and be refused")
	}

	// Layout: one viewport-high column. The grid used to be a fixed 78vh,
	// which made the whole page scroll once anything sat above it, and on
	// a short window it rendered at exactly zero pixels.
	if got.Page.PageScrolls {
		t.Error("the page scrolls as a whole; the grid should be the only thing that scrolls")
	}
	if !got.Page.StatusBarVisible {
		t.Error("the status bar is off screen")
	}

	if got.Page.Headers != got.Page.SortButtons || got.Page.Headers != got.Page.FilterBoxes {
		t.Errorf("header, sort buttons and filter boxes disagree: %d / %d / %d; every column needs all three",
			got.Page.Headers, got.Page.SortButtons, got.Page.FilterBoxes)
	}
	if got.Page.Tiles == 0 || len(got.Page.Groups) == 0 {
		t.Errorf("dashboard rendered %d tiles in %d groups", got.Page.Tiles, len(got.Page.Groups))
	}

	// The honesty rule, end to end, on three figures that reach the page by
	// three different routes.
	if got.Honesty.Available == nil || got.Honesty.Available.NA {
		t.Errorf("page_life_expectancy = %+v; the source reports it available and the tile says n/a", got.Honesty.Available)
	}
	if got.Honesty.Unavailable == nil || !got.Honesty.Unavailable.NA {
		t.Errorf("other_cpu_percent = %+v; the source reports it unavailable and the tile shows a number", got.Honesty.Unavailable)
	}
	if got.Honesty.Absent == nil || !got.Honesty.Absent.NA {
		t.Errorf("buffer_pool_mb = %+v; the source never sends it and the tile shows a number", got.Honesty.Absent)
	}

	// Spec section 6's first row: instance, host, edition, version, uptime.
	// The version was there before and sat dimmed next to the instance
	// name, which is where nobody looked for it.
	for _, want := range []string{"host", "edition", "version", "deployment", "uptime"} {
		if got.Identity[want] == "" {
			t.Errorf("the server information row shows no %s; it has %v", want, got.Identity)
		}
	}
	if v := got.Identity["version"]; v != "" && !strings.Contains(v, ".") {
		t.Errorf("version reads %q; the full product version is what identifies a build", v)
	}
	if d := got.Identity["deployment"]; d != string(model.DeploymentOnPremisesOrVM) {
		t.Errorf("deployment reads %q, want %q", d, model.DeploymentOnPremisesOrVM)
	}

	// The cross inside a filter box: absent until there is something to
	// clear, and clearing through it leaves the same state as clearing by
	// hand rather than a half-cleared one.
	if !got.ClearButton.HiddenWhenEmpty {
		t.Error("the clear cross is visible on an empty filter box")
	}
	if !got.ClearButton.ShownWhenFiltered {
		t.Error("the clear cross stays hidden while a filter is set, so there is no way to clear it but backspace")
	}
	if got.ClearedByButton.BoxValue != "" || got.ClearedByButton.BoxMarked || !got.ClearedByButton.ButtonHidden {
		t.Errorf("clearing through the cross left value=%q marked=%v buttonHidden=%v; the box, its highlight and the cross have to agree",
			got.ClearedByButton.BoxValue, got.ClearedByButton.BoxMarked, got.ClearedByButton.ButtonHidden)
	}
	if got.ClearedByButton.Rows != got.ClearedByButton.Total {
		t.Errorf("clearing through the cross left %d of %d rows; it has to actually drop the filter", got.ClearedByButton.Rows, got.ClearedByButton.Total)
	}

	// The honesty rule needs a figure that goes away after having had a
	// value: a tile that quietly kept its last reading is invisible to any
	// single observation, because a stale number still looks like a number.
	if got.Flipping.WithValue == 0 || got.Flipping.Greyed == 0 {
		t.Errorf("the alternating figure was seen with a value %d times and greyed %d times; the watch has to catch it in both states or it proves nothing",
			got.Flipping.WithValue, got.Flipping.Greyed)
	}
	if len(got.Flipping.StaleWhileGreyed) > 0 {
		t.Errorf("a tile marked unavailable still showed %q; an unavailable figure never falls back to the last value it had, which is the most convincing lie this page could tell",
			got.Flipping.StaleWhileGreyed)
	}

	// Folding hands height back to the grid, which is the point of it.
	if !(got.Fold.AfterGroup > got.Fold.Before && got.Fold.AfterAll > got.Fold.AfterGroup) {
		t.Errorf("folding did not give the grid its height back: %d before, %d after one group, %d after the whole dashboard",
			got.Fold.Before, got.Fold.AfterGroup, got.Fold.AfterAll)
	}

	// Sort: ascending, descending, then cleared.
	if len(got.Sort) != 3 {
		t.Fatalf("expected three clicks, got %d", len(got.Sort))
	}
	if got.Sort[0].Dir != 1 || !got.Sort[0].Ordered {
		t.Errorf("first click: dir %d, ordered %v; want ascending and sorted", got.Sort[0].Dir, got.Sort[0].Ordered)
	}
	if got.Sort[1].Dir != -1 || !got.Sort[1].Ordered {
		t.Errorf("second click: dir %d, ordered %v; want descending and sorted", got.Sort[1].Dir, got.Sort[1].Ordered)
	}
	if got.Sort[2].Dir != 0 || got.Sort[2].Field != "" {
		t.Errorf("third click: dir %d, field %q; a third click has to clear the sort, because unsorted is the server's own order",
			got.Sort[2].Dir, got.Sort[2].Field)
	}

	// Filter: narrows, marks its box, and combines with AND.
	if got.FilterOne.Rows == 0 || got.FilterOne.Rows >= got.FilterOne.Total {
		t.Errorf("filtering left %d of %d rows; it narrowed nothing", got.FilterOne.Rows, got.FilterOne.Total)
	}
	if !got.FilterOne.AllMatch {
		t.Error("a row that does not match the filter survived it")
	}
	if !got.FilterOne.BoxMarked {
		t.Error("an active filter box is not marked, so a grid that looks empty is a mystery")
	}
	if !strings.Contains(got.FilterOne.RowCount, "of") {
		t.Errorf("row count reads %q while filtered; it has to say how many of how many", got.FilterOne.RowCount)
	}
	if got.FilterAnd.Rows > got.FilterOne.Rows || !got.FilterAnd.AllMatch {
		t.Errorf("a second filter on another column left %d rows against %d, allMatch %v; filters on different columns combine with AND",
			got.FilterAnd.Rows, got.FilterOne.Rows, got.FilterAnd.AllMatch)
	}
	if got.FilterCleared.Rows != got.FilterCleared.Total {
		t.Errorf("clearing every filter left %d of %d rows", got.FilterCleared.Rows, got.FilterCleared.Total)
	}

	// The anchoring rule, both branches.
	if !got.AnchorKeeps.StillPresent {
		t.Errorf("the selected row did not survive a filter on its own database %q; the check below cannot mean anything", got.AnchorKeeps.FilteredOn)
	} else {
		if !got.AnchorKeeps.Marked || !got.AnchorKeeps.Visible {
			t.Errorf("after filtering, the selected row is marked=%v visible=%v, scroll %d to %d; spec 8.1 re-anchors on it",
				got.AnchorKeeps.Marked, got.AnchorKeeps.Visible, got.AnchorKeeps.ScrollBefore, got.AnchorKeeps.ScrollAfter)
		}
	}
	if got.AnchorDrops.Rows != 0 {
		t.Errorf("a filter matching nothing left %d rows", got.AnchorDrops.Rows)
	}
	if got.AnchorDrops.ScrollAfter != 0 {
		t.Errorf("scroll is %d after the selected row stopped matching; it goes to the top", got.AnchorDrops.ScrollAfter)
	}
}

type e2eCell struct {
	Text string `json:"text"`
	NA   bool   `json:"na"`
}

type e2eResult struct {
	Problems []string `json:"problems"`
	RowsSeen int      `json:"rowsSeen"`
	Page     struct {
		LinkTags         int      `json:"linkTags"`
		ScriptSrc        int      `json:"scriptSrc"`
		StyleTags        int      `json:"styleTags"`
		IconIsData       bool     `json:"iconIsData"`
		Headers          int      `json:"headers"`
		FilterBoxes      int      `json:"filterBoxes"`
		SortButtons      int      `json:"sortButtons"`
		Groups           []string `json:"groups"`
		Tiles            int      `json:"tiles"`
		PageScrolls      bool     `json:"pageScrolls"`
		StatusBarVisible bool     `json:"statusBarVisible"`
	} `json:"page"`
	Honesty struct {
		Available   *e2eCell `json:"available"`
		Unavailable *e2eCell `json:"unavailable"`
		Absent      *e2eCell `json:"absent"`
	} `json:"honesty"`
	Fold struct {
		Before     int `json:"before"`
		AfterGroup int `json:"afterGroup"`
		AfterAll   int `json:"afterAll"`
	} `json:"fold"`
	Sort []struct {
		Dir     int    `json:"dir"`
		Ordered bool   `json:"ordered"`
		Field   string `json:"field"`
	} `json:"sort"`
	FilterOne struct {
		Rows      int    `json:"rows"`
		Total     int    `json:"total"`
		AllMatch  bool   `json:"allMatch"`
		BoxMarked bool   `json:"boxMarked"`
		RowCount  string `json:"rowCount"`
	} `json:"filterOne"`
	FilterAnd struct {
		Rows     int  `json:"rows"`
		AllMatch bool `json:"allMatch"`
	} `json:"filterAnd"`
	FilterCleared struct {
		Rows  int `json:"rows"`
		Total int `json:"total"`
	} `json:"filterCleared"`
	AnchorKeeps struct {
		FilteredOn   string `json:"filteredOn"`
		Rows         int    `json:"rows"`
		ScrollBefore int    `json:"scrollBefore"`
		ScrollAfter  int    `json:"scrollAfter"`
		StillPresent bool   `json:"stillPresent"`
		Marked       bool   `json:"marked"`
		Visible      bool   `json:"visible"`
	} `json:"anchorKeeps"`
	Identity    map[string]string `json:"identity"`
	ClearButton struct {
		HiddenWhenEmpty   bool `json:"hiddenWhenEmpty"`
		ShownWhenFiltered bool `json:"shownWhenFiltered"`
	} `json:"clearButton"`
	ClearedByButton struct {
		BoxValue     string `json:"boxValue"`
		BoxMarked    bool   `json:"boxMarked"`
		ButtonHidden bool   `json:"buttonHidden"`
		Rows         int    `json:"rows"`
		Total        int    `json:"total"`
	} `json:"clearedByButton"`
	Flipping struct {
		WithValue        int      `json:"withValue"`
		Greyed           int      `json:"greyed"`
		StaleWhileGreyed []string `json:"staleWhileGreyed"`
	} `json:"flipping"`
	AnchorDrops struct {
		Rows        int `json:"rows"`
		ScrollAfter int `json:"scrollAfter"`
	} `json:"anchorDrops"`
}

// browserTestServer serves the real page from a fake source: enough rows to
// fill more than one screen, spread over several databases and a wide range
// of CPU so sorting and filtering have something to bite on, and three
// dashboard figures chosen to exercise the three states a tile can be in.
func browserTestServer(t *testing.T) (*Server, func()) {
	t.Helper()

	dbs := []string{"alpha", "beta", "gamma", "delta"}
	rows := make([]model.RequestSample, 200)
	for i := range rows {
		rows[i] = model.RequestSample{
			At:       time.Now(),
			Ref:      model.RequestRef{SessionID: int64(51 + i)},
			Status:   "running",
			Database: dbs[i%len(dbs)],
			Login:    "svc",
			Host:     "APP01",
			Program:  "sqltop e2e",
			Command:  "SELECT",
			// Deliberately not monotonic in row order, so "sorted" cannot
			// be true by accident.
			CPUMs:     int64((i * 7919) % 10000),
			ElapsedMs: int64(1000 + i),
			SQLText:   fmt.Sprintf("SELECT %d FROM dbo.T", i),
		}
	}

	src := fake.New(rows)
	src.Info = model.ServerInfo{
		Instance: "e2e", Host: "e2e-host", Edition: "Developer Edition (64-bit)",
		ProductVersion: "16.0.0.0", MajorVersion: 16, StartedAt: time.Now().Add(-3 * time.Hour),
		Deployment: model.DeploymentOnPremisesOrVM,
	}
	// buffer_cache_hit_ratio alternates between a reading and nothing, so
	// the driver can watch a tile lose its value rather than only ever see
	// one that never had one.
	src.AlternateFigure = "buffer_cache_hit_ratio"
	src.Figures = map[string]model.Figure{
		"page_life_expectancy": {Value: 1234, Unit: "s", Available: true},
		"other_cpu_percent":    {Unit: "%", Available: false},
		// buffer_pool_mb is deliberately absent: a key the page names and
		// the server never sends must render exactly like an unavailable
		// one.
	}

	w := window.New(time.Minute, 5000)
	w.Append(time.Now(), rows)

	tiers := config.Default().Tiers
	tiers.Requests = config.Duration(200 * time.Millisecond)
	c := collector.New(src, w, collector.NewBudget(50, tiers))

	srv, err := NewServer(c, w, config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	go func() { _ = srv.Serve(ctx) }()
	return srv, func() {
		cancel()
		_ = srv.Close()
	}
}

func lookChromium() string {
	for _, n := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// devToolsPort reads the port chromium chose. Asking for port 0 and reading
// it back beats picking a free port here, which races with anything else on
// the machine that wants one.
func devToolsPort(profile string, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	path := filepath.Join(profile, "DevToolsActivePort")
	for time.Now().Before(deadline) {
		f, err := os.Open(path)
		if err == nil {
			sc := bufio.NewScanner(f)
			ok := sc.Scan()
			port := strings.TrimSpace(sc.Text())
			f.Close()
			if ok && port != "" && port != "0" {
				return port, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("no DevToolsActivePort in %s after %s", profile, wait)
}

// lastJSONLine picks the driver's report out of whatever else reached the
// pipe. Deno is quiet with --quiet, but a warning on stderr must not be
// mistaken for the report.
func lastJSONLine(s string) string {
	for _, line := range reverse(strings.Split(strings.TrimSpace(s), "\n")) {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func reverse(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
