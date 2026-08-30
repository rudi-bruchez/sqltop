package mssql

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// The measurement harness behind the tables in docs/PERFORMANCE.md. None of
// it is a gate: it prints numbers and asserts nothing, and it is skipped
// unless SQLTOP_MEASURE is set, because a stopwatch does not belong in a
// suite that has to stay green on a loaded machine.
//
// It lives in the repository rather than in somebody's scratch directory so
// that the day a number in the documentation is doubted, redoing the
// measurement is one command rather than an afternoon. Run it against a
// container under a real load: sqlstress in this repository is what those
// numbers were taken under.
//
//	eval "$(scripts/testdb.sh)"
//	cd sqlstress && SQLSTRESS_DSN="$SQLTOP_TEST_DSN" go run . -duration 5m -env /dev/null &
//	SQLTOP_MEASURE=1 go test ./internal/source/mssql -run TestMeasure -v

func measuring(t *testing.T) (*Source, context.Context) {
	t.Helper()
	if os.Getenv("SQLTOP_MEASURE") == "" {
		t.Skip("set SQLTOP_MEASURE to run the measurement harness")
	}
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	identify(t, s, ctx)
	return s, ctx
}

// TestMeasureQueryCost is the table in docs/PERFORMANCE.md: what every
// statement costs the monitored server, and what OPTION (RECOMPILE) was
// costing before it came off. The cost is the delta of the tool's own
// session's cpu_time, which is what the observation budget throttles on, so
// this measures the same quantity the tool reports about itself.
func TestMeasureQueryCost(t *testing.T) {
	s, ctx := measuring(t)

	withRecompile := func(q string) string {
		return strings.ReplaceAll(q, "OPTION (MAXDOP 1)", "OPTION (RECOMPILE, MAXDOP 1)")
	}
	run := func(q string) func() error {
		return func() error { return s.query(ctx, q, func(*sql.Rows) error { return nil }) }
	}
	info, caps := s.snapshot()
	grid := buildRequestsQuery(info, caps)

	cases := []struct {
		name string
		call func() error
	}{
		{"requestsQuery", run(grid)},
		{"requestsQuery, with RECOMPILE", run(withRecompile(grid))},
		{"countersQuery", run(countersQuery)},
		{"countersQuery, with RECOMPILE", run(withRecompile(countersQuery))},
		{"osViewsQuery", run(osViewsQuery)},
		{"osViewsQuery, with RECOMPILE", run(withRecompile(osViewsQuery))},
		{"sessionsQuery", run(sessionsQuery)},
		{"sessionsQuery, with RECOMPILE", run(withRecompile(sessionsQuery))},
		{"transactionsQuery", run(transactionsQuery)},
		{"transactionsQuery, with RECOMPILE", run(withRecompile(transactionsQuery))},
		{"locksQuery", run(locksQuery)},
		{"locksQuery, with RECOMPILE", run(withRecompile(locksQuery))},
		{"logSpaceQuery", run(logSpaceQuery)},
		{"logSpaceQuery, with RECOMPILE", run(withRecompile(logSpaceQuery))},
	}
	report(t, s, ctx, cases)
}

// TestMeasurePreparedStatements is the "measured and rejected" entry: the
// same statements through sp_prepexec, so the text crosses the wire once
// and every later call sends a handle. It runs on the Source's own pinned
// connection, so the cost lands in the session Cost differentiates.
func TestMeasurePreparedStatements(t *testing.T) {
	s, ctx := measuring(t)

	run := func(q string) func() error {
		return func() error { return s.query(ctx, q, func(*sql.Rows) error { return nil }) }
	}
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

	report(t, s, ctx, []struct {
		name string
		call func() error
	}{
		{"requestsQuery, ad hoc", run(grid)},
		{"requestsQuery, prepared", prepared(grid)},
		{"countersQuery, ad hoc", run(countersQuery)},
		{"countersQuery, prepared", prepared(countersQuery)},
		{"osViewsQuery, ad hoc", run(osViewsQuery)},
		{"osViewsQuery, prepared", prepared(osViewsQuery)},
	})
}

// TestMeasurePacketSize isolates the transport from the query: one statement
// returning a fixed 800 rows of about 500 bytes, whatever the server is
// doing, over connections that differ only in TDS packet size. It uses its
// own handles rather than the Source, because the packet size is negotiated
// when the connection is made.
func TestMeasurePacketSize(t *testing.T) {
	if os.Getenv("SQLTOP_MEASURE") == "" {
		t.Skip("set SQLTOP_MEASURE to run the measurement harness")
	}
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		t.Skip("SQLTOP_TEST_DSN is unset")
	}
	ctx := context.Background()

	// The shape of a full grid tick carrying SQL text.
	const q = `SELECT TOP (800) CAST(object_id AS bigint), REPLICATE(N'x', 250)
	           FROM sys.all_columns OPTION (MAXDOP 1)`

	for _, size := range []string{"", "8192", "16384", "32767"} {
		d, label := dsn, "default (4096)"
		if size != "" {
			sep := "?"
			if strings.Contains(d, "?") {
				sep = "&"
			}
			d += sep + "packet+size=" + size
			label = size
		}
		db, err := sql.Open("sqlserver", d)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		drain := func() error {
			rows, err := db.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			for rows.Next() {
				var a int64
				var b string
				if err := rows.Scan(&a, &b); err != nil {
					return err
				}
			}
			return rows.Close()
		}
		if err := drain(); err != nil {
			t.Fatal(err)
		}
		const runs = 30
		start := time.Now()
		for i := 0; i < runs; i++ {
			if err := drain(); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("packet size %-14s %6.2f ms wall per call", label, time.Since(start).Seconds()*1000/runs)
		db.Close()
	}
}

// report runs each case forty times after warming its plan and prints the
// server CPU and the wall clock per call. Warming matters: the first call
// of a statement that is not in the cache pays a compilation nobody else
// will, and averaging it in would measure the wrong thing.
func report(t *testing.T, s *Source, ctx context.Context, cases []struct {
	name string
	call func() error
}) {
	t.Helper()
	const runs = 40
	for _, c := range cases {
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
