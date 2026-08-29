package mssql

import (
	"context"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// A Source with no capabilities must not touch the connection at all. This is
// the denied side of the gate, which the integration tests cannot reach: the
// container's sa login holds every right, so every run there proves only that
// the granted path works. A zero-value Source has a nil pinned connection, so
// a gate that leaked would fail loudly here rather than send a query that the
// server would refuse once a second forever.
func TestGatedReadsDoNotTouchAServerTheyMayNotRead(t *testing.T) {
	s := &Source{}
	ctx := context.Background()

	raw, err := s.readCounters(ctx)
	if err != nil {
		t.Errorf("readCounters returned %v; an absent capability is not an error", err)
	}
	if raw != nil {
		t.Errorf("readCounters returned %v; it must not read anything without the capability", raw)
	}

	figures := map[string]model.Figure{}
	if err := s.readSpace(ctx, figures); err != nil {
		t.Errorf("readSpace returned %v; an absent capability is not an error", err)
	}
	for name, f := range figures {
		if f.Available {
			t.Errorf("%s came back available with no capability to read it; an unreadable figure has to say so rather than show a zero", name)
		}
	}
	for _, name := range []string{"tempdb_used_mb", "tempdb_free_mb"} {
		if _, ok := figures[name]; !ok {
			t.Errorf("%s is missing; the dashboard needs the tile marked unavailable, not absent", name)
		}
	}

	if err := s.readCPUHistory(ctx, figures); err != nil {
		t.Errorf("readCPUHistory returned %v; an absent capability is not an error", err)
	}
}
