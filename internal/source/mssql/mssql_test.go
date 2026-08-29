package mssql

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

var _ source.Source = (*Source)(nil)

// open connects to the container, or skips. Integration tests must never fail
// on a machine without Podman; `go test ./...` stays green there. Setting
// SQLTOP_REQUIRE_DB turns that skip into a failure, for a run that must not
// silently report green with zero database coverage.
func open(t *testing.T) *Source {
	t.Helper()
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		if os.Getenv("SQLTOP_REQUIRE_DB") != "" {
			t.Fatal("SQLTOP_TEST_DSN is unset and SQLTOP_REQUIRE_DB is set; run: eval \"$(scripts/testdb.sh)\"")
		}
		t.Skip("SQLTOP_TEST_DSN is unset; run: eval \"$(scripts/testdb.sh)\"")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := New()
	if err := s.Open(ctx, dsn); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionIsReadUncommitted(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	var level string
	err := s.db.QueryRowContext(ctx, `
		SELECT CASE transaction_isolation_level
		    WHEN 1 THEN 'read uncommitted' ELSE 'other' END
		FROM sys.dm_exec_sessions WHERE session_id = @@SPID
		OPTION (RECOMPILE, MAXDOP 1)`).Scan(&level)
	if err != nil {
		t.Fatal(err)
	}
	if level != "read uncommitted" {
		t.Fatalf("isolation level = %q, want read uncommitted: a monitoring tool must not take shared locks on the server it is watching", level)
	}
}

func TestIdentifyReportsVersionAndCapabilities(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	info, caps, err := s.Identify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.MajorVersion < 12 {
		t.Fatalf("major version = %d, want at least 12; below 11 the tool refuses to connect", info.MajorVersion)
	}
	if info.ProductVersion == "" || info.Edition == "" {
		t.Fatalf("info = %+v, want the product version and edition filled", info)
	}
	if info.MajorVersion >= 15 && !caps.Has(model.CapLivePlanProgress) {
		t.Error("lightweight profiling v3 is on by default from 2019, so live plan progress must be advertised")
	}
	if !caps.Has(model.CapInstanceWideView) {
		t.Error("a container instance is not Azure SQL Database, so the instance-wide view must be advertised")
	}
}

func TestCostIsCumulativeAndNonZero(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	first, err := s.Cost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s.SampleRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	second, err := s.Cost(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if second.CPUMs < first.CPUMs {
		t.Fatalf("cost went backwards, %d then %d: it must be cumulative for the collector to differentiate it", first.CPUMs, second.CPUMs)
	}
	if second.LogicalReads <= first.LogicalReads {
		t.Error("twenty samples should have cost some logical reads; a flat zero means we are reading the wrong session")
	}
}

func TestSampleRequestsSeesALongQuery(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	// A second connection runs something slow enough to be caught.
	victim, err := sql.Open("sqlserver", os.Getenv("SQLTOP_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		victim.ExecContext(ctx, `WAITFOR DELAY '00:00:06'`)
	}()
	defer func() { <-done }()

	time.Sleep(1500 * time.Millisecond)

	rows, err := s.SampleRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var found *model.RequestSample
	for i := range rows {
		if strings.Contains(rows[i].SQLText, "WAITFOR DELAY") {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the running WAITFOR was not in the %d sampled rows", len(rows))
	}
	if found.Ref.SessionID == 0 {
		t.Error("session id must be filled")
	}
	if found.Database == "" {
		t.Error("database must be filled: it is a filter and sort column")
	}
	if found.Command == "" {
		t.Error("command must be filled: it is a filter and sort column")
	}
	if !strings.Contains(found.Program, "go-mssqldb") {
		t.Errorf("program = %q, want the driver's default app name: a transposition in the login_name/host_name/program_name block would still pass every other check here", found.Program)
	}
	if found.Host == "" {
		t.Error("host must be filled")
	}
	if found.Program == found.Host {
		t.Error("program and host are adjacent same-typed columns from the same table; equal values would hide a transposition between them")
	}
	if found.Login == "" {
		t.Error("login must be filled")
	}
	if found.Login == found.Host {
		t.Error("login and host swapped would leave both assertions above passing; they have to differ for the block to be pinned")
	}
	if found.ElapsedMs <= 0 {
		t.Errorf("elapsed = %d ms, want a positive value after 1.5 seconds", found.ElapsedMs)
	}
	if found.Depth != 0 {
		t.Error("Depth is the window's job, not the source's")
	}
}

func TestIsolationSurvivesASessionReset(t *testing.T) {
	// SessionInitSQL is what makes this true. A one-off SET after connecting
	// would be lost the moment database/sql resets or re-establishes the
	// connection, and the tool would start locking without anyone noticing.
	s := open(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := s.SampleRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var level int
	if err := s.db.QueryRowContext(ctx,
		`SELECT transaction_isolation_level FROM sys.dm_exec_sessions
		 WHERE session_id = @@SPID OPTION (RECOMPILE, MAXDOP 1)`).Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level != 1 {
		t.Fatalf("isolation level = %d after several queries, want 1", level)
	}
}

func TestSampleRequestsExcludesItself(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	rows, err := s.SampleRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Ref.SessionID == s.spid {
			t.Fatal("the tool must not report its own collection query as activity")
		}
	}
}

func TestSampleServerCountersNeedTwoTicks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	first, err := s.SampleServer(ctx, model.TierCounters)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := first.Figures["batch_requests_sec"]; !ok {
		t.Fatal("the key must be present even before it has a value")
	} else if f.Available {
		t.Fatal("a rate cannot exist on the first tick")
	}
	if f := first.Figures["page_life_expectancy"]; !f.Available || f.Value <= 0 {
		t.Fatalf("PLE = %+v, want a raw value available immediately", f)
	}
	if f := first.Figures["total_server_memory_kb"]; !f.Available || f.Value <= 0 {
		t.Fatalf("committed memory = %+v, want a positive raw value from the counters", f)
	}

	time.Sleep(1200 * time.Millisecond)

	second, err := s.SampleServer(ctx, model.TierCounters)
	if err != nil {
		t.Fatal(err)
	}
	if f := second.Figures["batch_requests_sec"]; !f.Available {
		t.Fatal("the second tick must produce a rate")
	}
}

func TestSampleServerSpaceTier(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := s.SampleServer(ctx, model.TierSpace)
	if err != nil {
		t.Fatal(err)
	}
	if f := got.Figures["tempdb_used_mb"]; !f.Available {
		t.Fatal("tempdb usage must be available on a container instance")
	}
	if _, present := got.Figures["total_server_memory_mb"]; present {
		t.Fatal("server memory belongs to the counter catalogue, not the space tier: one figure, one source")
	}
}

// TestEveryQueryCarriesTheHints guards the three requirements that are easy to
// forget the day someone adds a query: read uncommitted comes from the session,
// but RECOMPILE keeps the plan out of the cache and MAXDOP 1 keeps a monitoring
// query from taking parallel workers on the server it is watching. Both are per
// statement, and SQL Server allows only one OPTION clause per query, so they
// have to travel together.
func TestEveryQueryCarriesTheHints(t *testing.T) {
	queries := map[string]string{
		"identify": identifyQuery,
		"cost":     costQuery,
		// requestsQueryTemplate, not a built s.requestsQuery: task 8 made the
		// requests query version- and capability-dependent, built once per
		// server by buildRequestsQuery (see requests_test.go's
		// TestBuiltQueryGates for the per-shape check). The trailing OPTION
		// clause is fixed text in the template, outside every substitution,
		// so the template alone already proves every built shape carries it.
		"requests":     requestsQueryTemplate,
		"counters":     countersQuery,
		"space":        spaceQuery,
		"versionStore": versionStoreQuery,
		"cpuHistory":   cpuHistoryQuery,
	}
	for name, q := range queries {
		if !strings.Contains(q, "OPTION (RECOMPILE, MAXDOP 1)") {
			t.Errorf("%s query is missing OPTION (RECOMPILE, MAXDOP 1)", name)
		}
		if strings.Count(strings.ToUpper(q), "OPTION (") != 1 {
			t.Errorf("%s query has %d OPTION clauses, SQL Server allows one", name, strings.Count(strings.ToUpper(q), "OPTION ("))
		}
	}
}

func TestUnavailableFigureIsMarkedNotOmitted(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	info, _, err := s.Identify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsAzureSQLDB {
		t.Skip("this asserts the on-premises path")
	}
	got, err := s.SampleServer(ctx, model.TierCPUHistory)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Figures["sql_cpu_percent"]; !ok {
		t.Fatal("a figure this source cannot produce must still appear with Available false, so one tile can vanish without its neighbours")
	}
}
