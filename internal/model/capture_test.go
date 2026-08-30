package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapCaptureSessionIsItsOwnBit(t *testing.T) {
	c := Caps(CapCaptureSession)
	if !c.Has(CapCaptureSession) {
		t.Error("CapCaptureSession should be present")
	}
	if c.Has(CapLivePlanProgress) || c.Has(CapSessionWaitStats) {
		t.Error("CapCaptureSession collides with an existing capability")
	}
}

func TestStopReasonsAreDistinctAndSpoken(t *testing.T) {
	all := []StopReason{
		StopByKey, StopByShutdown, StopByBrowserGone, StopBySessionGone,
		StopBySessionReused, StopByTimeCap, StopByServerLost,
	}
	seen := map[string]bool{}
	for _, r := range all {
		if r.String() == "" {
			t.Errorf("stop reason %d has no wording", int(r))
		}
		if seen[r.String()] {
			t.Errorf("two stop reasons both say %q", r.String())
		}
		seen[r.String()] = true
	}
	if StopNotStopped.String() != "" {
		t.Error("the zero value must be silent, since it is what a running capture holds")
	}
}

func TestTheStatementDoesNotOwnTheWordKind(t *testing.T) {
	// The trace file needs a discriminator per line, and the statement
	// already spends "kind" on batch versus rpc. Two JSON keys of the same
	// name in one object is not a parse error, it is worse: the decoder
	// keeps the last, so the record silently reads as a statement kind.
	b, err := json.Marshal(CapturedStatement{Kind: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"kind":"batch"`) {
		t.Fatalf("CapturedStatement no longer serialises kind: %s", b)
	}
	if strings.Contains(string(b), `"record"`) {
		t.Error("CapturedStatement claims the record discriminator; the trace writer needs that name free")
	}
}

func TestCaptureStateFieldNamesMatchWhatTheInterfaceReads(t *testing.T) {
	// app.js reads these names. A mismatch is silent in both directions:
	// the Go side compiles and the JavaScript reads undefined.
	b, _ := json.Marshal(CaptureState{})
	for _, name := range []string{
		`"available"`, `"active"`, `"session_id"`, `"started_at"`,
		`"stopped"`, `"statements"`, `"missed"`, `"dropped"`, `"unknown"`,
	} {
		if !strings.Contains(string(b), name) {
			t.Errorf("CaptureState does not serialise %s; app.js reads it", name)
		}
	}
	n, _ := json.Marshal(CaptureNote{})
	for _, name := range []string{`"session_id"`, `"since"`} {
		if !strings.Contains(string(n), name) {
			t.Errorf("CaptureNote does not serialise %s; app.js reads it", name)
		}
	}
}
