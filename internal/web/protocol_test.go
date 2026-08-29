package web

import (
	"encoding/json"
	"strings"
	"testing"

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
	if !strings.Contains(string(buf), `"batch_requests_sec":{"Value":0,"Unit":"","Available":false}`) {
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
