package web

import (
	"encoding/json"
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
		Connected: false,
		Message:   "sampling requests: connection reset by peer",
	}

	payload := e.Snapshot(nil, nil, st)
	if payload.Status.Connected {
		t.Fatal("status must report disconnected")
	}
	if payload.Status.Message != st.Message {
		t.Fatalf("status message = %q, want %q", payload.Status.Message, st.Message)
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
