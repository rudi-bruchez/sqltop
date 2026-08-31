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

	srv, snapDir, stop := browserTestServer(t)
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

	// The configuration reaches the screen: a tile switched off is absent,
	// its neighbours are not, and a group configured folded starts folded.
	// A switch that renders anyway is a switch that lies.
	if got.Page.HasPlanCache {
		t.Error("plan_cache_mb was switched off in the configuration and its tile is on the page")
	}
	if !got.Page.HasBufferPool {
		t.Error("buffer_pool_mb is missing although only its neighbour was switched off")
	}
	if !got.Page.MemoryFolded {
		t.Error("the memory group was configured folded and is open")
	}

	// The column selection of spec section 8.2, from the file to the
	// screen: a column switched off is absent, one moved to the front of
	// the list is the first heading, and the panel still offers the hidden
	// one so it can be brought back.
	if len(got.Columns.Configured) == 0 {
		t.Fatal("the grid drew no headings at all")
	}
	if got.Columns.Configured[0] != "cpu_ms" {
		t.Errorf("the first heading is %q; the configuration puts cpu_ms first", got.Columns.Configured[0])
	}
	if idx(got.Columns.Configured, "host") >= 0 {
		t.Error("host was switched off in the configuration and the grid draws it")
	}
	if idx(got.Columns.Configured, "database") < 0 {
		t.Error("database is missing although only its neighbour was switched off")
	}
	if idx(got.Columns.Configured, "percent_complete") >= 0 {
		t.Error("percent_complete is off by default in the catalogue and the grid draws it")
	}
	if on, listed := panelState(got.Columns.Panel, "host"); !listed || on {
		t.Errorf("the column panel offers host as listed=%v checked=%v; a hidden column must still be there to switch back on", listed, on)
	}
	if on, listed := panelState(got.Columns.Panel, "cpu_ms"); !listed || !on {
		t.Errorf("the column panel offers cpu_ms as listed=%v checked=%v", listed, on)
	}

	// Dragging a heading moves the column; switching one back on changes
	// the column count, and the row pool has to be rebuilt to match. A pool
	// still holding the old cell count leaves every row one column short of
	// its own header.
	if i, j := idx(got.Columns.AfterDrag, "database"), idx(got.Columns.AfterDrag, "spid"); j != i+1 {
		t.Errorf("after dragging spid onto database the order is %v; spid should sit immediately after database", got.Columns.AfterDrag)
	}
	if idx(got.Columns.AfterShow, "host") < 0 {
		t.Errorf("ticking host in the column panel did not bring it back; the order is %v", got.Columns.AfterShow)
	}
	if got.Columns.PoolCells != len(got.Columns.AfterShow) {
		t.Errorf("after switching a column back on the row pool has %d cells per row and the header has %d columns (%d rows in view)", got.Columns.PoolCells, len(got.Columns.AfterShow), got.Columns.Rows)
	}

	if len(got.HiddenIsHidden) > 0 {
		t.Errorf("these elements carry hidden and are still drawn: %v; a class that sets display beats the user agent's [hidden] rule on specificity", got.HiddenIsHidden)
	}

	// Column geometry. A floor is a floor: a narrow column has to come out
	// narrow, and the surplus a wide window leaves has to land on the one
	// column with something to say rather than being shared out equally.
	// Measured rather than eyeballed, because the failure looks like a
	// design decision: seventeen of eighteen columns at exactly 178 px.
	widest, widestField := 0, ""
	for f, w := range got.Widths.Rendered {
		if f != "sql_text" && w > widest {
			widest, widestField = w, f
		}
	}
	t.Logf("columns: spid %d px, cpu_ms %d px, widest other %s %d px, sql_text %d px",
		got.Widths.Rendered["spid"], got.Widths.Rendered["cpu_ms"], widestField, widest, got.Widths.Rendered["sql_text"])
	if w := got.Widths.Rendered["spid"]; w > 60 {
		t.Errorf("the spid column renders at %d px; its heading is %d px and its floor is 48", w, got.Widths.Headings["spid"])
	}
	if w := got.Widths.Rendered["cpu_ms"]; w > 90 {
		t.Errorf("the cpu ms column renders at %d px; a millisecond figure does not need it", w)
	}
	if got.Widths.Rendered["sql_text"] < 2*widest {
		t.Errorf("sql_text renders at %d px against %d for %s; the window's surplus is being shared out rather than given to the column that can use it",
			got.Widths.Rendered["sql_text"], widest, widestField)
	}

	// Every visible column of a real row draws something. The fixture gives
	// each of these a value, so an empty cell here is the renderer losing
	// it, not the server withholding it.
	for _, field := range []string{"spid", "status", "database", "login", "program", "command", "cpu_ms", "elapsed", "sql_text"} {
		if got.Cells[field] == "" {
			t.Errorf("column %q drew an empty cell on a row that has a value for it; the whole row is %v", field, got.Cells)
		}
	}

	// The views of spec section 7: one tab each, and the three that are
	// not projections of the retention window fetch their own data only
	// while their tab is open.
	if want := []string{"requests", "blocking", "sessions", "transactions", "logs"}; !equalStrings(got.Views.Tabs, want) {
		t.Errorf("the tab bar shows %v, want %v", got.Views.Tabs, want)
	}
	if got.Views.BarGap.Items < 3 || got.Views.BarGap.Min < 8 {
		t.Errorf("the status bar's %d items are %d px apart at their closest; they run into each other below about 8", got.Views.BarGap.Items, got.Views.BarGap.Min)
	}

	// A command nobody can discover is a command nobody uses, so the keys
	// are on the page rather than only behind h.
	if want := []string{"t", "e", "d", "y", "n", "c", "s", "p", "f", "h"}; !equalStrings(got.Views.Hints, want) {
		t.Errorf("the command strip shows %v, want %v", got.Views.Hints, want)
	}
	if got.Views.Blocking.Rows == 0 || got.Views.Blocking.Rows >= got.Views.Blocking.Total {
		t.Errorf("the blocking view shows %d of %d rows; it should keep the chains and drop the rest", got.Views.Blocking.Rows, got.Views.Blocking.Total)
	}
	if !got.Views.Blocking.AllInAChain {
		t.Error("the blocking view is showing a row that is neither blocked nor blocking")
	}
	if !got.Views.Blocking.DepthShown {
		t.Error("the blocking view does not show the depth column, which the catalogue turns on for it and off for requests")
	}
	if !got.Views.Sessions.Visible || !got.Views.Sessions.GridHidden {
		t.Errorf("u left the sessions panel visible=%v with the grid hidden=%v", got.Views.Sessions.Visible, got.Views.Sessions.GridHidden)
	}
	if got.Views.Sessions.Rows != 2 {
		t.Errorf("the sessions view drew %d rows from a fixture of 2: %v", got.Views.Sessions.Rows, got.Views.Sessions.FirstRow)
	}
	if len(got.Views.Sessions.Headings) == 0 || got.Views.Sessions.Headings[0] != "spid" {
		t.Errorf("the sessions table's headings are %v", got.Views.Sessions.Headings)
	}
	// Every cell of a row on one line. A stylesheet rule that takes a cell
	// out of table-cell layout leaves the text and the cell count right and
	// scatters the row down the page, which no other assertion here sees.
	if got.Views.Sessions.RowLines != 1 {
		t.Errorf("the first session row's cells sit on %d different lines and the row is %d px tall; they belong on one",
			got.Views.Sessions.RowLines, got.Views.Sessions.RowHeight)
	}

	// The durations are formatted, not printed raw: 900 seconds of open
	// transaction has to read as a duration or the column is useless.
	if !containsString(got.Views.Sessions.FirstRow, "15m 00s") {
		t.Errorf("the first session row is %v; a 900 second transaction should read as a duration", got.Views.Sessions.FirstRow)
	}
	if got.Views.Transactions.Tables != 2 {
		t.Errorf("the transactions view drew %d tables; it shows the transactions and the locks they hold", got.Views.Transactions.Tables)
	}
	if got.Views.Transactions.TranRows != 1 || got.Views.Transactions.LockRows != 2 {
		t.Errorf("the transactions view drew %d transactions and %d lock groups, want 1 and 2", got.Views.Transactions.TranRows, got.Views.Transactions.LockRows)
	}
	if !strings.Contains(got.Views.Transactions.LockText, "Orders") {
		t.Error("the lock table does not name the locked object, which is the question that view answers")
	}
	if !got.Views.Logs.Visible || got.Views.Logs.Rows != 2 {
		t.Errorf("l left the log panel visible=%v with %d rows", got.Views.Logs.Visible, got.Views.Logs.Rows)
	}
	if !strings.Contains(got.Views.Logs.Text, "LOG_BACKUP") {
		t.Error("the log view does not show what is stopping the log being reused, which is the answer somebody looking at a full log wants")
	}
	if got.Views.PanelFollows.Which != "logs" || containsString(got.Views.PanelFollows.Fields, "sql_text") {
		t.Errorf("the column panel says %q and offers %v while the log view is on screen", got.Views.PanelFollows.Which, got.Views.PanelFollows.Fields)
	}
	if !got.Views.BackToGrid {
		t.Error("r did not bring the unfiltered grid back")
	}

	// Geometry, on every view rather than on the one that was reported.
	// Three defects have shipped that every property-reading assertion here
	// was blind to: cells dropping out of their rows, a window's surplus
	// shared out equally, and hidden not hiding. All three are visible in
	// these four numbers and in none of the others this test collects.
	for _, view := range []string{"requests", "blocking", "sessions", "transactions", "logs"} {
		tables := got.Views.Geometry[view]
		if len(tables) == 0 {
			t.Errorf("the %s view drew no table to measure", view)
			continue
		}
		for i, g := range tables {
			where := fmt.Sprintf("%s view, table %d of %d", view, i+1, len(tables))
			if g.Cols == 0 {
				t.Errorf("%s: no columns", where)
				continue
			}
			if g.HeadLines != 1 {
				t.Errorf("%s: the headings sit on %d lines", where, g.HeadLines)
			}
			if g.RowLines != 1 {
				t.Errorf("%s: a row's cells sit on %d lines and the row is %d px tall", where, g.RowLines, g.RowHeight)
			}
			if g.RowHeight == 0 || g.RowHeight > 30 {
				t.Errorf("%s: a row is %d px tall; one line of this table is about 22", where, g.RowHeight)
			}
			if g.Narrowest > 90 {
				t.Errorf("%s: its narrowest column is %d px, so nothing is coming out narrow", where, g.Narrowest)
			}
			if g.Widest < 2*g.Second {
				t.Errorf("%s: widest column %d px against %d for the next; the window's surplus is being shared out rather than given to one column",
					where, g.Widest, g.Second)
			}
		}
	}

	// The arrow keys walk the grid, and the scroll follows the selection
	// into rows the virtualised table has not drawn yet.
	a := got.Arrows
	if a.First != 0 {
		t.Errorf("the first ArrowDown selected row %d; with nothing selected it takes the top of the list", a.First)
	}
	if a.After40 != 40 {
		t.Errorf("forty more presses reached row %d, want 40", a.After40)
	}
	if a.AfterUp != 38 {
		t.Errorf("two ArrowUp from row 40 reached %d", a.AfterUp)
	}
	if a.Scrolled == 0 || !a.Visible {
		t.Errorf("after walking past the visible window the grid is scrolled to %d and the selected row is visible=%v (row %d..%d, pane %d..%d)",
			a.Scrolled, a.Visible, a.RowTop, a.RowBottom, a.HeadBottom, a.PaneBottom)
	}
	if a.AtEnd != a.Last {
		t.Errorf("pressing past the bottom reached row %d of %d; it stops there, it does not wrap", a.AtEnd, a.Last)
	}
	if a.AtStart != 0 {
		t.Errorf("pressing past the top reached row %d; it stops at the first", a.AtStart)
	}
	if a.MovedWhileTyping {
		t.Error("an arrow key typed into a filter box moved the grid selection; the caret has to be able to move")
	}

	// The single-keypress commands of spec section 7.
	if !got.Commands.Help.Open || got.Commands.Help.Entries == 0 {
		t.Errorf("h left the help dialog open=%v with %d entries", got.Commands.Help.Open, got.Commands.Help.Entries)
	}
	if !got.Commands.HelpClosed {
		t.Error("h a second time did not close the help dialog")
	}
	if got.Commands.PausedByTyping {
		t.Error("typing p into a filter box paused the display; the letters have to reach the box")
	}
	if !got.Commands.Pause.On || !got.Commands.Pause.Marked {
		t.Errorf("p left paused=%v with the marker shown=%v", got.Commands.Pause.On, got.Commands.Pause.Marked)
	}
	if got.Commands.Pause.Before != got.Commands.Pause.After {
		t.Errorf("the display went on updating while paused: tick %q became %q", got.Commands.Pause.Before, got.Commands.Pause.After)
	}
	if got.Commands.Resumed.On {
		t.Error("p a second time did not resume")
	}
	if got.Commands.Resumed.Seq == got.Commands.Pause.After {
		t.Errorf("the display is still frozen after resuming: tick %q", got.Commands.Resumed.Seq)
	}
	// The statement panel: t shows the selected row's statement under the
	// grid, keeps its lines, colours its parts, and gives the height back
	// when it closes.
	d := got.Commands.Detail
	if !d.Shown || d.Closed != true {
		t.Errorf("t left the panel shown=%v and a second press closed=%v", d.Shown, d.Closed)
	}
	if d.GridAfter >= d.GridBefore {
		t.Errorf("the grid was %d px tall and is %d px with the panel open; the panel has to take its height from somewhere", d.GridBefore, d.GridAfter)
	}
	if d.GridBack <= d.GridAfter {
		t.Errorf("the grid did not get its height back on closing: %d px, against %d with the panel open", d.GridBack, d.GridAfter)
	}
	if d.Lines < 4 {
		t.Errorf("the statement drew on %d lines; the fixture's is four, and a grid cell already shows it on one", d.Lines)
	}
	if d.Keywords < 3 || d.Numbers < 1 || d.Strings < 1 || d.Comments < 1 {
		t.Errorf("highlighting found %d keywords, %d numbers, %d strings, %d comments in %q",
			d.Keywords, d.Numbers, d.Strings, d.Comments, d.Text)
	}
	if d.Scripts != 0 {
		t.Errorf("the statement's markup became %d element(s); it came off a server and belongs in the page as text", d.Scripts)
	}
	if !strings.Contains(d.Text, "<script>") {
		t.Errorf("the panel shows %q; the statement's text must survive verbatim", d.Text)
	}
	if !strings.HasPrefix(d.Who, "spid ") {
		t.Errorf("the panel's heading reads %q; it names the row it is showing", d.Who)
	}

	// The plan panel: e follows the selected request through its plan, in
	// the same space the statement uses, and neither is shown at once.
	pl := got.Commands.Plan
	if !pl.Shown || !pl.PlanVisible || !pl.SQLHidden {
		t.Errorf("e left the panel shown=%v with the plan visible=%v and the statement hidden=%v", pl.Shown, pl.PlanVisible, pl.SQLHidden)
	}
	if pl.Operators != 3 {
		t.Errorf("the plan panel drew %d operators from a fixture of 3", pl.Operators)
	}
	if pl.RowLines != 1 {
		t.Errorf("the plan panel's first row sits on %d lines", pl.RowLines)
	}
	if len(pl.Headings) == 0 || pl.Headings[0] != "node" {
		t.Errorf("the plan panel's headings are %v", pl.Headings)
	}
	// An operator at three times its estimate has to read as three hundred
	// per cent: capping it at a hundred would hide the thing worth seeing.
	if len(pl.Cells) < 3 || !containsString(pl.Cells[2], "300 %") {
		t.Errorf("the operator at 90000 rows against an estimate of 30000 reads %v", pl.Cells)
	}
	// y: what the selected session has been seen running, out of the
	// retention window, with no query at all.
	hy := got.Commands.History
	// Asserted on content, not on a row count: an empty list still draws
	// one row saying so, and counting rows called that a history. The
	// fixture's statements all select from dbo.T.
	if len(hy.Cells) == 0 || !anyContains(hy.Cells[0], "FROM dbo.T") {
		t.Errorf("y drew %v for a session the window has samples of; the heading reads %q", hy.Cells, hy.Who)
	}
	if !anyContains(hy.Cells[0], "svc") {
		t.Errorf("the history row does not name the login that ran it: %v", hy.Cells[0])
	}
	if hy.RowLines != 1 {
		t.Errorf("the history panel's first row sits on %d lines", hy.RowLines)
	}
	if len(hy.Headings) == 0 || hy.Headings[0] != "last seen" {
		t.Errorf("the history panel's headings are %v", hy.Headings)
	}
	// The two clocks belong in the heading: they are what makes the list
	// readable on a pooled connection.
	if !strings.Contains(hy.Who, "connected") {
		t.Errorf("the history heading reads %q and does not say how long the connection has been open", hy.Who)
	}

	// n: what it has waited on since its last reset.
	wt := got.Commands.Waits
	if wt.Rows != 2 {
		t.Errorf("n drew %d wait types from a fixture of 2; the heading reads %q", wt.Rows, wt.Who)
	}
	if !wt.SQLHidden {
		t.Error("the waits panel left the statement showing underneath it")
	}
	if len(wt.Cells) < 1 || !containsString(wt.Cells[0], "LCK_M_X") || !containsString(wt.Cells[0], "80.0 %") {
		t.Errorf("the first wait row is %v; the fixture's is LCK_M_X at 80 per cent", wt.Cells)
	}
	if !strings.Contains(wt.Who, "since its last reset") {
		t.Errorf("the waits heading reads %q and does not say what window the numbers cover", wt.Who)
	}

	if !strings.HasPrefix(got.Commands.PlanSaved, "live plan written to ") {
		t.Errorf("d reported %q", got.Commands.PlanSaved)
	}

	if !strings.HasPrefix(got.Commands.SnapshotMessage, "snapshot written to ") {
		t.Errorf("s reported %q", got.Commands.SnapshotMessage)
	}
	if !strings.Contains(got.Commands.Rate, "1 s") {
		t.Errorf("after f the status bar shows the rate as %q, want the 1 s the ladder steps to first", got.Commands.Rate)
	}

	// c, the capture panel. This fixture's source cannot capture, so what
	// is on screen is the unavailable state, which is the state most
	// operators meet first and the one a silent panel would ruin.
	t.Logf("capture: head %q, %d columns %v, first %g px, list %g px tall, %d rows, notice %q",
		got.CaptureHead, len(got.CaptureHeadings), got.CaptureHeadings, got.CaptureFirstColWidth,
		got.CaptureListHeight, got.CaptureRows, got.CaptureNotice)
	if got.CaptureHead == "" {
		t.Error("the capture panel opened with no header; a panel that explains nothing is the failure this feature is built around")
	}
	if got.CaptureFirstColWidth < 40 {
		t.Errorf("the first capture column is %g px wide; the table did not lay out", got.CaptureFirstColWidth)
	}
	if got.CaptureListHeight < 20 {
		t.Errorf("the capture table is %g px tall", got.CaptureListHeight)
	}
	if !got.HelpMentionsCapture {
		t.Error("c is bound but absent from the help; a key nobody can discover is a key nobody uses")
	}
	// The same table with a statement in it. The panel above is drawn from
	// an empty list, and every column keeps its catalogue floor whatever the
	// cell registry holds, so nothing on screen here can tell a registry of
	// entries carrying text from one of bare functions. That one throws the
	// first time a capture returns a row.
	if got.CaptureDrawn.FirstColWidth < 40 || got.CaptureDrawn.RowLines != 1 {
		t.Errorf("a captured statement drew a first column %g px wide over %d lines: %v %s",
			got.CaptureDrawn.FirstColWidth, got.CaptureDrawn.RowLines, got.CaptureDrawn.Cells, got.CaptureDrawn.Error)
	}
	if len(got.CaptureDrawn.Cells) == 0 || got.CaptureDrawn.Cells[0] == "" {
		t.Errorf("the drawn capture row is %v; its cells come from the registry and are empty", got.CaptureDrawn.Cells)
	}

	// The saved file is the state, not the scroll position: the grid is
	// virtualised, so the document holds about forty rows of however many
	// the view has, and a snapshot of the DOM would silently be a snapshot
	// of what happened to be on screen.
	checkSnapshotFile(t, snapDir, got.Commands.RowsWhenSaved)

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
		HasPlanCache     bool     `json:"hasPlanCache"`
		HasBufferPool    bool     `json:"hasBufferPool"`
		MemoryFolded     bool     `json:"memoryFolded"`
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
	Columns struct {
		Configured []string `json:"configured"`
		Panel      []struct {
			F  string `json:"f"`
			On bool   `json:"on"`
		} `json:"panel"`
		AfterDrag []string `json:"afterDrag"`
		PoolCells int      `json:"poolCells"`
		Rows      int      `json:"rows"`
		AfterShow []string `json:"afterShow"`
	} `json:"columns"`
	HiddenIsHidden []string          `json:"hiddenIsHidden"`
	Cells          map[string]string `json:"cells"`
	Widths         struct {
		Char     float64        `json:"char"`
		Headings map[string]int `json:"headings"`
		Rendered map[string]int `json:"rendered"`
	} `json:"widths"`
	Views struct {
		Tabs   []string `json:"tabs"`
		Hints  []string `json:"hints"`
		BarGap struct {
			Items int `json:"items"`
			Min   int `json:"min"`
		} `json:"barGap"`
		Blocking struct {
			Rows        int  `json:"rows"`
			Total       int  `json:"total"`
			GridVisible bool `json:"gridVisible"`
			AllInAChain bool `json:"allInAChain"`
			DepthShown  bool `json:"depthShown"`
		} `json:"blocking"`
		Sessions struct {
			Visible    bool     `json:"visible"`
			GridHidden bool     `json:"gridHidden"`
			Headings   []string `json:"headings"`
			Rows       int      `json:"rows"`
			FirstRow   []string `json:"firstRow"`
			RowLines   int      `json:"rowLines"`
			RowHeight  int      `json:"rowHeight"`
		} `json:"sessions"`
		Transactions struct {
			Visible  bool   `json:"visible"`
			Tables   int    `json:"tables"`
			TranRows int    `json:"tranRows"`
			LockRows int    `json:"lockRows"`
			LockText string `json:"lockText"`
		} `json:"transactions"`
		Logs struct {
			Visible bool   `json:"visible"`
			Rows    int    `json:"rows"`
			Text    string `json:"text"`
		} `json:"logs"`
		PanelFollows struct {
			Which  string   `json:"which"`
			Fields []string `json:"fields"`
		} `json:"panelFollows"`
		BackToGrid bool                      `json:"backToGrid"`
		Geometry   map[string][]e2eTableGeom `json:"geometry"`
	} `json:"views"`
	Arrows struct {
		First            int  `json:"first"`
		After40          int  `json:"after40"`
		AfterUp          int  `json:"afterUp"`
		AtEnd            int  `json:"atEnd"`
		AtStart          int  `json:"atStart"`
		Last             int  `json:"last"`
		Scrolled         int  `json:"scrolled"`
		Visible          bool `json:"visible"`
		MovedWhileTyping bool `json:"movedWhileTyping"`
		RowTop           int  `json:"rowTop"`
		RowBottom        int  `json:"rowBottom"`
		HeadBottom       int  `json:"headBottom"`
		PaneBottom       int  `json:"paneBottom"`
	} `json:"arrows"`
	Commands struct {
		Help struct {
			Open    bool `json:"open"`
			Entries int  `json:"entries"`
		} `json:"help"`
		HelpClosed     bool `json:"helpClosed"`
		PausedByTyping bool `json:"pausedByTyping"`
		Pause          struct {
			On     bool   `json:"on"`
			Marked bool   `json:"marked"`
			Before string `json:"before"`
			After  string `json:"after"`
		} `json:"pause"`
		Resumed struct {
			On  bool   `json:"on"`
			Seq string `json:"seq"`
		} `json:"resumed"`
		Detail struct {
			Shown      bool   `json:"shown"`
			GridBefore int    `json:"gridBefore"`
			GridAfter  int    `json:"gridAfter"`
			GridBack   int    `json:"gridBack"`
			Closed     bool   `json:"closed"`
			Who        string `json:"who"`
			Lines      int    `json:"lines"`
			Keywords   int    `json:"keywords"`
			Numbers    int    `json:"numbers"`
			Strings    int    `json:"strings"`
			Comments   int    `json:"comments"`
			Scripts    int    `json:"scripts"`
			Text       string `json:"text"`
		} `json:"detail"`
		Plan struct {
			Shown       bool       `json:"shown"`
			PlanVisible bool       `json:"planVisible"`
			SQLHidden   bool       `json:"sqlHidden"`
			Who         string     `json:"who"`
			Operators   int        `json:"operators"`
			Headings    []string   `json:"headings"`
			RowLines    int        `json:"rowLines"`
			Cells       [][]string `json:"cells"`
		} `json:"plan"`
		History struct {
			Who      string     `json:"who"`
			Rows     int        `json:"rows"`
			Headings []string   `json:"headings"`
			RowLines int        `json:"rowLines"`
			Cells    [][]string `json:"cells"`
		} `json:"history"`
		Waits struct {
			Who       string     `json:"who"`
			Rows      int        `json:"rows"`
			SQLHidden bool       `json:"sqlHidden"`
			Cells     [][]string `json:"cells"`
		} `json:"waits"`
		PlanSaved       string `json:"planSaved"`
		SnapshotMessage string `json:"snapshotMessage"`
		RowsWhenSaved   int    `json:"rowsWhenSaved"`
		Rate            string `json:"rate"`
	} `json:"commands"`
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
	CaptureHead          string   `json:"captureHead"`
	CaptureHeadings      []string `json:"captureHeadings"`
	CaptureFirstColWidth float64  `json:"captureFirstColWidth"`
	CaptureListHeight    float64  `json:"captureListHeight"`
	CaptureRows          int      `json:"captureRows"`
	CaptureNotice        string   `json:"captureNotice"`
	HelpMentionsCapture  bool     `json:"helpMentionsCapture"`
	CaptureDrawn         struct {
		FirstColWidth float64  `json:"firstColWidth"`
		RowLines      int      `json:"rowLines"`
		Cells         []string `json:"cells"`
		Error         string   `json:"error"`
	} `json:"captureDrawn"`
}

// browserTestServer serves the real page from a fake source: enough rows to
// fill more than one screen, spread over several databases and a wide range
// of CPU so sorting and filtering have something to bite on, and three
// dashboard figures chosen to exercise the three states a tile can be in.
func browserTestServer(t *testing.T) (*Server, string, func()) {
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
			// Multiline, with a comment, a number, a keyword and a string
			// literal carrying markup: the statement panel has to keep the
			// lines, colour the parts, and escape what came off the server.
			SQLText: fmt.Sprintf("SELECT %d,\n       '<script>alert(1)</script>' AS x\nFROM dbo.T -- a note\nWHERE id > 100", i),
		}
	}
	// One chain of three, so the blocking view has something to keep and
	// 197 rows to drop. Without it that view would be empty and every
	// assertion about it would pass on a page that filtered nothing.
	rows[1].BlockedBy = rows[0].Ref.SessionID
	rows[1].Depth = 1
	rows[2].BlockedBy = rows[1].Ref.SessionID
	rows[2].Depth = 2

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

	// The three on-demand views. Small fixtures: what is under test is the
	// tab, the request it makes and the table it draws, not the arithmetic
	// of a server that is not here.
	src.SessionRows = []model.SessionSample{
		// Connected for an hour and handed out by the pool a minute ago:
		// the two clocks that were one wrong one.
		{SessionID: 51, Login: "svc", Host: "APP01", Program: "sqltop e2e", Status: "sleeping",
			Database: "alpha", ConnectedSec: 3600, SinceResetSec: 60, IdleSec: 120, OpenTran: 1, TranSec: 900,
			CPUMs: 4200, Reads: 99, Writes: 3, MemoryMB: 1.5},
		// Never handed back, so both clocks agree and the column is blank.
		{SessionID: 52, Login: "reporting", Host: "BI02", Program: "SSMS", Status: "running",
			Database: "beta", ConnectedSec: 60, SinceResetSec: 60, CPUMs: 12},
	}
	src.TranRows = []model.TransactionSample{
		{TransactionID: 90210, SessionID: 51, Name: "user_transaction", ElapsedSec: 900,
			Type: "read/write", State: "active", Database: "alpha", Databases: 1,
			LogBytes: 3 << 20, LogRecords: 412},
	}
	src.LockRows = []model.LockSample{
		{SessionID: 51, Database: "alpha", ResourceType: "OBJECT", Object: "Orders",
			Mode: "IX", Status: "GRANT", Count: 1},
		{SessionID: 51, Database: "alpha", ResourceType: "PAGE", Mode: "IX", Status: "GRANT", Count: 17},
	}
	src.PlanRows = []model.PlanNode{
		{NodeID: 0, Operator: "Sort", Rows: 12000, Estimated: 40000, Threads: 1},
		{NodeID: 1, Operator: "Clustered Index Scan", Object: "Orders", Rows: 40000, Estimated: 40000, Threads: 1},
		// An operator well past its estimate, which is the reading this
		// panel exists for and must not be capped at a hundred per cent.
		{NodeID: 2, Operator: "Nested Loops", Rows: 90000, Estimated: 30000, Threads: 4},
	}
	src.WaitRows = []model.SessionWait{
		{WaitType: "LCK_M_X", Waits: 12, WaitMs: 8000, MaxWaitMs: 3000, SignalMs: 4, SharePercent: 80},
		{WaitType: "PAGEIOLATCH_SH", Waits: 300, WaitMs: 2000, MaxWaitMs: 40, SignalMs: 90, SharePercent: 20},
	}
	src.LogRows = []model.LogSpaceSample{
		{Database: "alpha", RecoveryModel: "FULL", ReuseWait: "LOG_BACKUP", State: "ONLINE",
			SizeMB: 512, UsedMB: 480, UsedPercent: 93.75},
		{Database: "master", RecoveryModel: "SIMPLE", ReuseWait: "NOTHING", State: "ONLINE",
			SizeMB: 2, UsedMB: 0.5, UsedPercent: 25},
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
	// One tile switched off, one group folded, one column hidden and one
	// column moved to the front, so the browser can be asked whether the
	// configuration file actually reaches the screen.
	layout := config.DefaultLayout()
	for i := range layout.Dashboard {
		if layout.Dashboard[i].Group == "memory" {
			layout.Dashboard[i].Folded = true
			layout.Dashboard[i].Figures["plan_cache_mb"] = false
		}
	}
	off := false
	layout.Views["requests"] = config.ViewLayout{Columns: []config.ViewColumn{
		{Field: "cpu_ms"},
		{Field: "host", Show: &off},
	}}
	cfg := config.Default()
	cfg.Layouts = map[string]config.Layout{"default": layout}
	srv = srv.WithConfig(cfg)

	// The s command writes beside the executable, which during a test is
	// wherever go test put the binary. Point it at a temporary directory
	// instead, and hand the path back so the assertions can read the file.
	dir := t.TempDir()
	snaps := filepath.Join(dir, "snapshots")
	snapshotsInto(t, snaps)
	plans := filepath.Join(dir, "plans")
	oldPlanDir := planDir
	planDir = func() (string, error) { return plans, nil }
	t.Cleanup(func() { planDir = oldPlanDir })
	t.Cleanup(func() { checkPlanFile(t, plans) })

	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	go func() { _ = srv.Serve(ctx) }()
	return srv, snaps, func() {
		cancel()
		_ = srv.Close()
	}
}

// checkSnapshotFile reads what the s command wrote and asserts it is a
// standalone document holding every row the view had, not the handful the
// virtualised grid kept in the DOM.
func checkSnapshotFile(t *testing.T, dir string, wantRows int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the s command created no snapshots directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the snapshots directory holds %d files, want 1", len(entries))
	}
	if !snapshotName.MatchString(entries[0].Name()) {
		t.Errorf("the snapshot is named %q, not server-yyyy-mm-dd-hhmmss.html", entries[0].Name())
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.HasPrefix(body, "<!DOCTYPE html>") {
		t.Errorf("the snapshot does not start with a doctype: %.60q", body)
	}
	if !strings.Contains(body, "<style>") {
		t.Error("the snapshot carries no stylesheet, so it would open as an unstyled dump")
	}
	if strings.Contains(body, "<script") {
		t.Error("the snapshot carries a script; a saved state is a document, not a running application")
	}
	// One <tr> per row, plus the header row.
	if n := strings.Count(body, "<tr>"); n != wantRows+1 {
		t.Errorf("the snapshot holds %d rows and the view had %d; the virtualised grid keeps about forty in the DOM, which is what a document-level save would have caught", n-1, wantRows)
	}
}

// e2eTableGeom is what the driver measures about one table on screen.
type e2eTableGeom struct {
	Cols      int `json:"cols"`
	HeadLines int `json:"headLines"`
	RowLines  int `json:"rowLines"`
	RowHeight int `json:"rowHeight"`
	Widest    int `json:"widest"`
	Second    int `json:"second"`
	Narrowest int `json:"narrowest"`
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(s []string, want string) bool { return idx(s, want) >= 0 }

// anyContains is containsString for cells whose text is longer than the
// thing being looked for.
func anyContains(cells []string, want string) bool {
	for _, c := range cells {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

// checkPlanFile reads what the d command wrote. The extension is what makes
// a plan open as a plan rather than as text, which is the point of saving
// one, and the fake reports a live plan, so the name has to say so.
func checkPlanFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("the d command created no plans directory: %v", err)
		return
	}
	if len(entries) != 1 {
		t.Errorf("the plans directory holds %d files, want 1", len(entries))
		return
	}
	name := entries[0].Name()
	if !strings.HasSuffix(name, ".sqlplan") {
		t.Errorf("the plan is named %q; the extension is what opens it as a plan", name)
	}
	if !strings.Contains(name, "-live-") {
		t.Errorf("the plan is named %q; the fake reports a live plan and the name has to say which arrived", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ShowPlanXML") {
		t.Errorf("the saved plan is not showplan XML: %.60q", b)
	}
}

func idx(s []string, want string) int {
	for i, v := range s {
		if v == want {
			return i
		}
	}
	return -1
}

func panelState(panel []struct {
	F  string `json:"f"`
	On bool   `json:"on"`
}, field string) (on, listed bool) {
	for _, p := range panel {
		if p.F == field {
			return p.On, true
		}
	}
	return false, false
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
