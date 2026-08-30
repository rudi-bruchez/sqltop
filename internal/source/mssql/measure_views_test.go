package mssql

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMeasureViewCost is not a gate. It prints what each on-demand view
// costs the monitored server, so the numbers in docs/PERFORMANCE.md come
// from a measurement rather than from a guess. Run it against a loaded
// container with -v; it is skipped unless SQLTOP_MEASURE is set, because it
// is a stopwatch and stopwatches do not belong in a test suite that has to
// stay green.
func TestMeasureViewCost(t *testing.T) {
	if os.Getenv("SQLTOP_MEASURE") == "" {
		t.Skip("set SQLTOP_MEASURE to measure the on-demand views")
	}
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	identify(t, s, ctx)

	const runs = 40
	withRecompile := func(q string) string {
		return strings.ReplaceAll(q, "OPTION (MAXDOP 1)", "OPTION (RECOMPILE, MAXDOP 1)")
	}
	run := func(q string) func() error {
		return func() error { return s.query(ctx, q, func(*sql.Rows) error { return nil }) }
	}
	// prepared runs the same statement through sp_prepexec, so the text
	// crosses the wire once and every later call sends a handle. It uses
	// the Source's own pinned connection, so the cost lands in the same
	// session Cost differentiates.
	prepared := func(q string) func() error {
		s.mu.Lock()
		conn, err := s.connLocked(ctx)
		if err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
		stmt, err := conn.PrepareContext(ctx, q)
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stmt.Close() })
		return func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			rows, err := stmt.QueryContext(ctx)
			if err != nil {
				return err
			}
			for rows.Next() {
			}
			return rows.Close()
		}
	}
	info, caps := s.snapshot()
	grid := buildRequestsQuery(info, caps)

	cases := []struct {
		name string
		call func() error
	}{
		{"requestsQuery", run(grid)},
		{"requestsQuery, with RECOMPILE", run(withRecompile(grid))},
		{"requestsQuery, prepared", prepared(grid)},
		{"countersQuery", run(countersQuery)},
		{"countersQuery, prepared", prepared(countersQuery)},
		{"osViewsQuery", run(osViewsQuery)},
		{"osViewsQuery, prepared", prepared(osViewsQuery)},
		{"sessionsQuery", run(sessionsQuery)},
		{"sessionsQuery, with RECOMPILE", run(withRecompile(sessionsQuery))},
		{"transactionsQuery", run(transactionsQuery)},
		{"transactionsQuery, with RECOMPILE", run(withRecompile(transactionsQuery))},
		{"locksQuery", run(locksQuery)},
		{"locksQuery, with RECOMPILE", run(withRecompile(locksQuery))},
		{"logSpaceQuery", run(logSpaceQuery)},
		{"logSpaceQuery, with RECOMPILE", run(withRecompile(logSpaceQuery))},
	}
	for _, c := range cases {
		// Warm the plan first, so the first compile is not averaged in.
		if err := c.call(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		before, err := s.Cost(ctx)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for i := 0; i < runs; i++ {
			if err := c.call(); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		wall := time.Since(start)
		after, err := s.Cost(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%-38s %6.2f ms of server CPU per call, %6.2f ms wall per call",
			c.name, float64(after.CPUMs-before.CPUMs)/runs, wall.Seconds()*1000/runs)
	}
}
