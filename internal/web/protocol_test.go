package web

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

func rows(n int) []model.RequestSample {
	long := strings.Repeat("SELECT c.customer_id, SUM(l.net_amount) FROM dbo.SalesOrder c ", 4)
	out := make([]model.RequestSample, n)
	for i := range out {
		out[i] = model.RequestSample{
			Ref:     model.RequestRef{SessionID: int64(51 + i)},
			SQLText: long,
			Program: ".Net SqlClient Data Provider",
			Login:   "app_web",
			Host:    "WEB01",
			CPUMs:   int64(i * 10),
		}
	}
	return out
}

func TestSnapshotSendsInvariantsOnceThenReferences(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	first := e.Snapshot(rows(50), nil, st)
	if len(first.Refs) != 50 {
		t.Fatalf("first snapshot carried %d refs, want 50", len(first.Refs))
	}

	second := e.Snapshot(rows(50), nil, st)
	if len(second.Refs) != 0 {
		t.Fatalf("second snapshot carried %d refs, want 0: a session's SQL text and program name never change, so they travel once", len(second.Refs))
	}
	if len(second.Rows) != 50 {
		t.Fatal("rows must still all be present")
	}
	for _, r := range second.Rows {
		if r.RefKey == "" {
			t.Fatal("every row must point at its reference entry")
		}
	}
}

func TestReferenceTableCutsThePayloadSubstantially(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	first, _ := json.Marshal(e.Snapshot(rows(300), nil, st))
	second, _ := json.Marshal(e.Snapshot(rows(300), nil, st))

	saved := 1 - float64(len(second))/float64(len(first))
	if saved < 0.40 {
		t.Fatalf("steady-state payload is only %.0f%% smaller than the first; the bench measured 47%% of the payload as redundant, so something is still being resent", saved*100)
	}
	t.Logf("first %d bytes, steady state %d bytes, %.0f%% smaller", len(first), len(second), saved*100)
}

func TestReferencesAreDroppedWhenTheSessionGoes(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	e.Snapshot(rows(10), nil, st)
	e.Snapshot(rows(2), nil, st)
	// The eight departed sessions must not be remembered forever, or the
	// encoder leaks on a server that churns connections.
	if n := e.known(); n != 2 {
		t.Fatalf("encoder remembers %d sessions, want 2", n)
	}
}

func TestReturningSessionGetsItsReferenceAgain(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	e.Snapshot(rows(3), nil, st)
	e.Snapshot(nil, nil, st)
	back := e.Snapshot(rows(3), nil, st)

	if len(back.Refs) != 3 {
		t.Fatalf("got %d refs, want 3: a session that left and came back must be described again, or the grid shows blank text", len(back.Refs))
	}
}

// TestUnavailableFigureIsNotEncodedAsZero proves the honesty distinction
// from an earlier task survives this encoding: a figure the server cannot
// answer must arrive as Available: false, never silently as a numeric zero
// that a dashboard would render as a real measurement.
func TestUnavailableFigureIsNotEncodedAsZero(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	figures := map[string]model.Figure{
		"batch_requests_sec": {Available: false},
		"cpu_percent":        {Value: 42, Unit: "%", Available: true},
	}

	payload := e.Snapshot(nil, figures, st)
	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Figures map[string]model.Figure `json:"figures"`
	}
	if err := json.Unmarshal(buf, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got, ok := decoded.Figures["batch_requests_sec"]
	if !ok {
		t.Fatal("unavailable figure must still be present in the payload, not dropped")
	}
	if got.Available {
		t.Fatal("unavailable figure decoded as available")
	}
	if got.Value != 0 {
		t.Fatalf("unavailable figure carried a non-zero value %v, which would be even more misleading than a plain zero", got.Value)
	}

	// The literal bytes matter here, not just the round trip: a bug that
	// mapped Available to omitempty on the wrong field, or that dropped the
	// field entirely, could still decode back to false by zero-value luck.
	// The wire form itself must say "available":false for the bad figure.
	if !strings.Contains(string(buf), `"batch_requests_sec":{"value":0,"unit":"","available":false}`) {
		t.Fatalf("unavailable figure not encoded as an explicit false, wire form: %s", buf)
	}

	avail, ok := decoded.Figures["cpu_percent"]
	if !ok || !avail.Available || avail.Value != 42 {
		t.Fatalf("available figure was not encoded faithfully: %+v", avail)
	}
}

// TestStatusCarriesWhateverTheCollectorReports proves the encoder passes
// collector.Status through rather than reinventing or flattening it: a
// disconnected status with a message describing which tier is failing must
// reach the client verbatim.
func TestStatusCarriesWhateverTheCollectorReports(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{
		Connected:       false,
		Message:         "sampling requests: connection reset by peer",
		CostMsPerSecond: 12.5,
	}

	payload := e.Snapshot(nil, nil, st)
	if payload.Status.Connected {
		t.Fatal("status must report disconnected")
	}
	if payload.Status.Message != st.Message {
		t.Fatalf("status message = %q, want %q", payload.Status.Message, st.Message)
	}
	// Spec section 10: an instrument that claims to bound its own cost
	// should show it, at all times rather than only while throttled, which
	// is what interpolating it solely into the throttle message used to
	// mean. This crosses the seam between Collector.Status, which already
	// had the figure, and the wire payload, which reached the browser
	// without it.
	if payload.Status.CostMsPerSecond != st.CostMsPerSecond {
		t.Fatalf("status cost = %v, want %v", payload.Status.CostMsPerSecond, st.CostMsPerSecond)
	}
}

// TestMovingToADifferentStatementGetsANewReference proves a session that
// keeps its ID but starts a different statement is treated as a new
// reference, not shown under its previous query's text.
func TestMovingToADifferentStatementGetsANewReference(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	first := []model.RequestSample{{
		Ref:     model.RequestRef{SessionID: 60},
		SQLText: "SELECT 1",
	}}
	e.Snapshot(first, nil, st)

	second := []model.RequestSample{{
		Ref:     model.RequestRef{SessionID: 60},
		SQLText: "SELECT 2",
	}}
	out := e.Snapshot(second, nil, st)

	if len(out.Refs) != 1 {
		t.Fatalf("session 60 changed statement, want a fresh reference, got %d", len(out.Refs))
	}
	if len(out.Rows) != 1 || out.Rows[0].RefKey == "" {
		t.Fatal("row must point at the new reference")
	}
	for _, ref := range out.Refs {
		if ref.SQL != "SELECT 2" {
			t.Fatalf("reference carries %q, want the new statement", ref.SQL)
		}
	}
	// The stale reference for the old statement must not linger either.
	if n := e.known(); n != 1 {
		t.Fatalf("encoder remembers %d references for one session, want 1", n)
	}
}

// TestSessionReuseWithSameStatementGetsANewReference is the fix-round-1
// critical bug: SQL Server reuses session IDs routinely, and a fingerprint
// built from the session ID and the statement text alone cannot tell alice
// on SSMS from PC1 apart from bob on sqlcmd from PC2 once the server has
// handed bob alice's old session ID and he happens to run the identical
// text (a health probe, or a shared stored procedure, make this ordinary).
// Without login, host and program in the key, no new reference goes out and
// the grid keeps showing bob's row labelled alice, in a tool that can kill
// sessions by that label.
func TestSessionReuseWithSameStatementGetsANewReference(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	alice := []model.RequestSample{{
		Ref:     model.RequestRef{SessionID: 51},
		SQLText: "SELECT 1",
		Login:   "alice",
		Host:    "PC1",
		Program: "SSMS",
	}}
	e.Snapshot(alice, nil, st)

	bob := []model.RequestSample{{
		Ref:     model.RequestRef{SessionID: 51},
		SQLText: "SELECT 1",
		Login:   "bob",
		Host:    "PC2",
		Program: "sqlcmd",
	}}
	out := e.Snapshot(bob, nil, st)

	if len(out.Refs) != 1 {
		t.Fatalf("session 51 was handed to a different login, want a fresh reference, got %d", len(out.Refs))
	}
	for _, ref := range out.Refs {
		if ref.Login != "bob" || ref.Host != "PC2" || ref.Program != "sqlcmd" {
			t.Fatalf("reference still describes the previous connection: %+v", ref)
		}
	}
}

// TestSameQueryHashDifferentLiteralGetsANewReference is the second half of
// the fix-round-1 critical bug. QueryHash is computed over a statement's
// parameterised shape, so it stays identical when only a literal changes;
// preferring it over the text meant a session moving from WHERE id = 1 to
// WHERE id = 999999 sent no new reference, and the grid kept the first
// literal forever, contradicting spec 8.1's definition of sql_text as the
// current statement.
func TestSameQueryHashDifferentLiteralGetsANewReference(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	first := []model.RequestSample{{
		Ref:       model.RequestRef{SessionID: 70},
		SQLText:   "SELECT * FROM Orders WHERE id = 1",
		QueryHash: "0xABCDEF",
	}}
	e.Snapshot(first, nil, st)

	second := []model.RequestSample{{
		Ref:       model.RequestRef{SessionID: 70},
		SQLText:   "SELECT * FROM Orders WHERE id = 999999",
		QueryHash: "0xABCDEF",
	}}
	out := e.Snapshot(second, nil, st)

	if len(out.Refs) != 1 {
		t.Fatalf("statement's literal changed under an unchanged query hash, want a fresh reference, got %d", len(out.Refs))
	}
	for _, ref := range out.Refs {
		if ref.SQL != "SELECT * FROM Orders WHERE id = 999999" {
			t.Fatalf("reference still carries the first literal: %q", ref.SQL)
		}
	}
}

// TestConcurrentRequestsOnOneSessionAreDistinctRows proves MARS is not
// silently collapsed: model.RequestRef carries a request ID alongside the
// session ID because the state window is indexed by both (spec section 4),
// and the virtualised renderer needs a stable per-row identity to keep
// selection and scroll position across ticks (section 10.1). SPID alone
// cannot tell two concurrent requests on one session apart.
func TestConcurrentRequestsOnOneSessionAreDistinctRows(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	mars := []model.RequestSample{
		{Ref: model.RequestRef{SessionID: 80, RequestID: 0}, SQLText: "SELECT 1"},
		{Ref: model.RequestRef{SessionID: 80, RequestID: 1}, SQLText: "SELECT 2"},
	}
	out := e.Snapshot(mars, nil, st)

	if len(out.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(out.Rows))
	}
	if out.Rows[0].RequestID == out.Rows[1].RequestID {
		t.Fatalf("two concurrent requests on one session carry the same request id: %d and %d", out.Rows[0].RequestID, out.Rows[1].RequestID)
	}
	seen := map[int32]bool{}
	for _, r := range out.Rows {
		if r.SPID != 80 {
			t.Fatalf("row SPID = %d, want 80", r.SPID)
		}
		seen[r.RequestID] = true
	}
	if !seen[0] || !seen[1] {
		t.Fatalf("both request ids must be present: %v", seen)
	}
}

// TestCapabilitiesAreEncodedAsNames proves collector.Status.Caps reaches the
// client, per spec 4.1: capabilities are the load-bearing piece the UI uses
// to grey what a source cannot provide, and StatusPayload is the only
// channel this protocol gives it. Names, not the raw bitset, so a
// JavaScript consumer never has to know Go's bit layout.
func TestCapabilitiesAreEncodedAsNames(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{
		Connected: true,
		Caps:      model.Caps(model.CapKillSession, model.CapLivePlanProgress),
	}

	out := e.Snapshot(nil, nil, st)
	want := map[string]bool{"killSession": true, "livePlanProgress": true}
	if len(out.Status.Caps) != len(want) {
		t.Fatalf("got caps %v, want exactly %v", out.Status.Caps, want)
	}
	for _, c := range out.Status.Caps {
		if !want[c] {
			t.Fatalf("unexpected capability name %q", c)
		}
	}

	buf, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(buf), `"killSession"`) {
		t.Fatalf("wire form did not carry a readable capability name: %s", buf)
	}
}

func TestNoCapabilitiesOmitsTheField(t *testing.T) {
	e := NewEncoder()
	out := e.Snapshot(nil, nil, collector.Status{Connected: true})
	buf, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(buf), `"caps"`) {
		t.Fatalf("empty capability set should be omitted, not sent as an empty list: %s", buf)
	}
}

// TestFiguresAreCopiedNotAliased proves Snapshot does not hand the caller's
// own map back out inside the payload: mutating figures after the call
// returns must not retroactively change what was already encoded.
func TestFiguresAreCopiedNotAliased(t *testing.T) {
	e := NewEncoder()
	figures := map[string]model.Figure{"cpu_percent": {Value: 10, Available: true}}

	out := e.Snapshot(nil, figures, collector.Status{Connected: true})
	figures["cpu_percent"] = model.Figure{Value: 99, Available: true}

	if out.Figures["cpu_percent"].Value != 10 {
		t.Fatalf("payload's figure changed after the caller mutated its own map: got %v, want the value at call time (10)", out.Figures["cpu_percent"].Value)
	}
}

// TestSnapshotIsSafeForConcurrentUse is the regression for the fix-round-1
// finding at protocol.go:128: e.sent took a concurrent write from two
// goroutines and crashed the process with a fatal error no recover() could
// catch. Four goroutines hammering one Encoder, run under -race, is what
// the reviewer used to reproduce it.
func TestSnapshotIsSafeForConcurrentUse(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				e.Snapshot(rows(20), nil, st)
			}
		}()
	}
	wg.Wait()
}

// TestSnapshotStampsATimestamp is the regression for the fix-round-1
// mutation that deleted the "TS:" assignment and still passed every test
// that existed at the time: it restores the brief's own bug, where every
// payload shipped "ts":0.
func TestSnapshotStampsATimestamp(t *testing.T) {
	e := NewEncoder()
	before := time.Now().UnixMilli()
	out := e.Snapshot(nil, nil, collector.Status{Connected: true})
	after := time.Now().UnixMilli()
	if out.TS < before || out.TS > after {
		t.Fatalf("TS = %d, want between %d and %d (the encode instant)", out.TS, before, after)
	}
}

// TestStatusMessageCarriesEveryTierFailureNotJustTheFirst is the regression
// for the fix-round-1 mutation that replaced Message: st.Message with only
// the first "; "-separated part and still passed: exactly the regression
// the per-tier failure-state work in the collector existed to prevent, one
// failing tier among several must not go invisible again on the way to the
// browser.
func TestStatusMessageCarriesEveryTierFailureNotJustTheFirst(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{
		Connected: false,
		Message:   "sampling requests: timeout; sampling counters: access denied; reading own cost: broken pipe",
	}
	out := e.Snapshot(nil, nil, st)
	if out.Status.Message != st.Message {
		t.Fatalf("status message = %q, want %q verbatim", out.Status.Message, st.Message)
	}
	if strings.Count(out.Status.Message, ";") != 2 {
		t.Fatalf("expected all three semicolon-separated tier failures to survive, got %q", out.Status.Message)
	}
}

func TestClipLeavesShortTextAlone(t *testing.T) {
	if got := clip("SELECT 1"); got != "SELECT 1" {
		t.Fatalf("clip modified a short string: %q", got)
	}
}

// TestClipBacksUpToARuneBoundary places a multi-byte rune (the euro sign,
// three bytes in UTF-8) straddling the maxRefSQL cut point. A naive
// s[:maxRefSQL] slice would split it and hand the browser invalid UTF-8,
// rendered as a replacement glyph on any non-ASCII batch.
func TestClipBacksUpToARuneBoundary(t *testing.T) {
	prefix := strings.Repeat("a", maxRefSQL-1)
	s := prefix + "€" + strings.Repeat("b", 100)

	got := clip(s)

	if !utf8.ValidString(got) {
		t.Fatalf("clip produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, "\n-- truncated by sqltop") {
		t.Fatalf("clip did not append the truncation marker")
	}
	if len(got) >= len(s) {
		t.Fatalf("clip did not actually shorten the string")
	}
}

// TestReferenceTableSavingsOnARealisticRow re-measures the reference
// table's payoff on a row shaped closer to section 10.1's bench than the
// longer fixture the earlier tests in this file use. That fixture's SQL
// text is disproportionately large, which is why it measured a 69%
// reduction; the reviewer's own harness (not tracked in this repository,
// see docs/SPECS.md section 10.1) measured 53% on a realistic 420-byte row
// carrying a 135-byte statement, and reported that the figure is sensitive:
// 35% at an 8-byte statement, 91% at 2000 bytes, and it falls with churn
// (57% at 0% churn, 28% at 50%, 0% at 100%, since a row that never repeats
// never gets to reuse a reference). 53% is the number to plan around, not
// 69%. This test does not reproduce that harness byte for byte; it checks
// the mechanism still saves a substantial share on a row built to roughly
// that shape, so 69% does not quietly become the assumed number elsewhere.
func TestReferenceTableSavingsOnARealisticRow(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}
	stmt := "SELECT o.order_id, o.total FROM dbo.Orders o WHERE o.customer_id = @p0 AND o.status = @p1"

	realistic := func(n int) []model.RequestSample {
		out := make([]model.RequestSample, n)
		for i := range out {
			out[i] = model.RequestSample{
				Ref:      model.RequestRef{SessionID: int64(100 + i)},
				SQLText:  stmt,
				Program:  ".Net SqlClient Data Provider",
				Login:    "app_web",
				Host:     "WEB01",
				Database: "Sales",
				Command:  "SELECT",
				Status:   "running",
				CPUMs:    int64(i * 5),
			}
		}
		return out
	}

	first, _ := json.Marshal(e.Snapshot(realistic(300), nil, st))
	second, _ := json.Marshal(e.Snapshot(realistic(300), nil, st))

	saved := 1 - float64(len(second))/float64(len(first))
	t.Logf("first %d bytes, steady state %d bytes, %.0f%% smaller (statement length %d bytes)",
		len(first), len(second), saved*100, len(stmt))
	if saved < 0.30 {
		t.Fatalf("steady-state payload is only %.0f%% smaller on a realistic row; the reference table's payoff is sensitive to statement length and should not collapse this far", saved*100)
	}
}

// TestBothEndpointsReportTheSameServerFacts closes a drift that had already
// happened once: /api/status and /api/stream each built their own
// StatusPayload, so when host, edition and the instance start time were
// added to the struct only the stream learned about them, and the same tool
// answered two different things about the same server depending on which of
// its own endpoints you asked. Comparing the two payloads field by field
// through the JSON they actually serialise means a field added to
// StatusPayload and wired into only one of them fails here, without anybody
// having to remember this file exists.
func TestBothEndpointsReportTheSameServerFacts(t *testing.T) {
	st := collector.Status{
		Connected: true,
		Message:   "a message",
		Info: model.ServerInfo{
			Instance:       "SQL01\\PROD",
			Host:           "host01",
			Edition:        "Developer Edition (64-bit)",
			ProductVersion: "16.0.4265.3",
			StartedAt:      time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		},
		Caps:            model.Caps(model.CapInstanceWideView),
		CostMsPerSecond: 12.5,
	}

	fromStream := NewEncoder().Snapshot(nil, nil, st).Status
	fromStatus := newStatusPayload(st)

	a, err := json.Marshal(fromStream)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(fromStatus)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("the two endpoints disagree about the same server:\n stream: %s\n status: %s", a, b)
	}

	// Every field the dashboard's first row needs has to survive the trip,
	// which a comparison of two equally empty payloads would not catch.
	var got map[string]any
	if err := json.Unmarshal(a, &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"instance":  "SQL01\\PROD",
		"host":      "host01",
		"edition":   "Developer Edition (64-bit)",
		"version":   "16.0.4265.3",
		"startedAt": float64(st.Info.StartedAt.UnixMilli()),
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
}

// TestUnknownStartTimeIsNotSentAsAnInstant pins the honesty rule on the one
// field where the zero value is a plausible-looking number rather than an
// obviously empty string: an unset time.Time marshalled through UnixMilli
// is a large negative integer, which the page would happily render as an
// uptime of roughly two thousand years.
func TestUnknownStartTimeIsNotSentAsAnInstant(t *testing.T) {
	p := newStatusPayload(collector.Status{Info: model.ServerInfo{Instance: "x"}})
	if p.StartedAt != 0 {
		t.Fatalf("StartedAt = %d for a source that could not read it, want 0", p.StartedAt)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "startedAt") {
		t.Errorf("payload = %s; an unknown start time must be omitted, not sent as a number the page could render", b)
	}
}

// TestRowFieldsMatchTheStruct is what makes the positional row format safe.
// The client is told the column order once and then indexes by position, so
// a field added to Row without a matching entry in rowFields would shift
// every column after it and put reads under writes with nothing failing.
// Reflection over the json tags, in declaration order, is the only check
// that cannot itself be forgotten.
func TestRowFieldsMatchTheStruct(t *testing.T) {
	rt := reflect.TypeOf(Row{})
	var tags []string
	for i := 0; i < rt.NumField(); i++ {
		tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			t.Fatalf("Row.%s has no json tag; every column has to be nameable on the wire", rt.Field(i).Name)
		}
		tags = append(tags, tag)
	}
	if !slices.Equal(tags, rowFields) {
		t.Errorf("rowFields does not match Row's json tags in order:\n struct: %v\n rowFields: %v", tags, rowFields)
	}
}

// TestRowRoundTripsThroughTheArrayForm checks the format end to end: every
// field arrives back in its own place. A transposition of two same-typed
// neighbours is the defect this format invites and the one a size check
// alone would miss, so every value here is distinct.
func TestRowRoundTripsThroughTheArrayForm(t *testing.T) {
	want := Row{
		SPID: 51, RequestID: 2, RefKey: "51:abc", Status: "suspended",
		Database: "OLTP_Main", Command: "SELECT", BlockedBy: 47, Depth: 3,
		ElapsedMs: 12345, CPUMs: 678, Reads: 90123, Writes: 456,
		TempdbMB: 7.25, GrantMB: 1024.5, DOP: 8, WaitType: "LCK_M_X",
		WaitMs: 999, Percent: 42.5,
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != '[' {
		t.Fatalf("row marshalled as %s; the wire format is a positional array, not an object", b)
	}
	var got Row
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v (from %s)", err, b)
	}
	if got != want {
		t.Errorf("round trip lost or moved a value:\n got %+v\nwant %+v", got, want)
	}
}

// TestFirstSnapshotCarriesTheColumnHeaderAndLaterOnesDoNot pins the "sent
// once" half of the format. Without the header the client cannot read a
// single row; with it on every tick it would cost the bytes the format was
// changed to save.
func TestFirstSnapshotCarriesTheColumnHeaderAndLaterOnesDoNot(t *testing.T) {
	e := NewEncoder()
	first := e.Snapshot(nil, nil, collector.Status{})
	if !slices.Equal(first.Cols, rowFields) {
		t.Fatalf("first snapshot Cols = %v, want %v", first.Cols, rowFields)
	}
	second := e.Snapshot(nil, nil, collector.Status{})
	if second.Cols != nil {
		t.Errorf("second snapshot repeats the column header: %v", second.Cols)
	}
	// A reconnecting client gets a new encoder and has to be told again,
	// or its grid would be unreadable for the life of the connection.
	if again := NewEncoder().Snapshot(nil, nil, collector.Status{}); !slices.Equal(again.Cols, rowFields) {
		t.Errorf("a fresh encoder did not send the header: %v", again.Cols)
	}
}

// FuzzAppendJSONString is why the hand-rolled string writer is allowed to
// exist. It runs five times per row, 800 rows a second, which is what
// justifies not calling encoding/json; this proves it produces exactly what
// encoding/json would, invalid UTF-8, control characters, HTML escaping and
// the JavaScript line terminators included.
func FuzzAppendJSONString(f *testing.F) {
	for _, seed := range []string{
		"", "plain", `quote " backslash \`, "<script>&amp;", "tab\tnewline\nreturn\r",
		"\x00\x01\x1f", "café", "日本語", "\xff\xfe invalid", "  ",
		"emoji 🐢 and a lone surrogate byte \xed\xa0\x80",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, err := json.Marshal(s)
		if err != nil {
			t.Skip("encoding/json refuses it, so there is nothing to match")
		}
		got := appendJSONString(nil, s)
		if string(got) != string(want) {
			t.Errorf("appendJSONString(%q)\n got %s\nwant %s", s, got, want)
		}
	})
}

// FuzzAppendJSONFloat holds the number writer to the same standard, with
// the one documented exception: encoding/json refuses NaN and infinity,
// and this writes 0 rather than failing a whole snapshot over one cell.
func FuzzAppendJSONFloat(f *testing.F) {
	for _, seed := range []float64{0, 1, -1, 0.5, 1e-7, 1e21, 1e-320, 123456.789, math.MaxFloat64} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v float64) {
		got := string(appendJSONFloat(nil, v))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if got != "0" {
				t.Errorf("appendJSONFloat(%v) = %s, want 0", v, got)
			}
			return
		}
		want, err := json.Marshal(v)
		if err != nil {
			t.Skip()
		}
		if got != string(want) {
			t.Errorf("appendJSONFloat(%v)\n got %s\nwant %s", v, got, want)
		}
	})
}

// TestMarshalledOrderMatchesTheAdvertisedColumns closes the hole an external
// review opened by driving it: transposing two same-typed fields in both
// MarshalJSON and UnmarshalJSON leaves every other test in this package
// green, because the round trip uses the same wrong order on both sides,
// while the wire now disagrees with the column header the client indexes
// by. The browser shows reads under writes and nothing anywhere fails.
//
// TestRowFieldsMatchTheStruct checks rowFields against the struct and
// TestRowRoundTripsThroughTheArrayForm checks Marshal against Unmarshal.
// Neither checks the one thing that matters: that the order MarshalJSON
// actually writes is the order rowFields advertises. This does, by filling
// every field with a value no other field has and reading the marshalled
// array back position by position.
func TestMarshalledOrderMatchesTheAdvertisedColumns(t *testing.T) {
	rt := reflect.TypeOf(Row{})
	rv := reflect.New(rt).Elem()
	for i := 0; i < rt.NumField(); i++ {
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Int, reflect.Int32, reflect.Int64:
			f.SetInt(int64(100 + i))
		case reflect.Float64:
			f.SetFloat(float64(100+i) + 0.5)
		case reflect.String:
			f.SetString(fmt.Sprintf("f%d", i))
		default:
			t.Fatalf("Row.%s is a %s, which this test cannot fill with a distinguishable value; teach it that kind before adding the field", rt.Field(i).Name, f.Kind())
		}
	}

	b, err := json.Marshal(rv.Interface().(Row))
	if err != nil {
		t.Fatal(err)
	}
	var got []json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v (from %s)", err, b)
	}
	if len(got) != len(rowFields) {
		t.Fatalf("MarshalJSON wrote %d columns, rowFields advertises %d", len(got), len(rowFields))
	}

	for i, name := range rowFields {
		field, ok := fieldByJSONTag(rt, rv, name)
		if !ok {
			t.Errorf("rowFields[%d] is %q, which is not a json tag on Row", i, name)
			continue
		}
		want, err := json.Marshal(field.Interface())
		if err != nil {
			t.Fatal(err)
		}
		if string(got[i]) != string(want) {
			t.Errorf("position %d is advertised as %q but MarshalJSON wrote %s there, and %q holds %s; the wire order and the column header disagree, so the browser will show one column's values under another's label",
				i, name, got[i], name, want)
		}
	}
}

func fieldByJSONTag(rt reflect.Type, rv reflect.Value, tag string) (reflect.Value, bool) {
	for i := 0; i < rt.NumField(); i++ {
		if name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); name == tag {
			return rv.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// TestDeploymentReachesTheWire keeps the label travelling. It is one string
// added to a struct that two endpoints build, which is exactly the shape of
// thing that used to reach only one of them.
func TestDeploymentReachesTheWire(t *testing.T) {
	for _, d := range []model.Deployment{
		model.DeploymentAzureSQLDB, model.DeploymentAzureMI, model.DeploymentAmazonRDS,
		model.DeploymentGoogleCloudSQL, model.DeploymentOnPremisesOrVM,
	} {
		st := collector.Status{Info: model.ServerInfo{Deployment: d}}
		if got := newStatusPayload(st).Deployment; got != string(d) {
			t.Errorf("deployment %q became %q on the wire", d, got)
		}
	}
	// Unknown is omitted rather than sent as an empty string the page would
	// have to special-case into a hidden row.
	b, err := json.Marshal(newStatusPayload(collector.Status{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "deployment") {
		t.Errorf("payload = %s; an unknown deployment is omitted", b)
	}
}
