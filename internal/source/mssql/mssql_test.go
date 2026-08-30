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

// TestSampleRequestsFilterKeepsBothSidesOfABlock is the CRITICAL TWO
// blocking escape hatch, exercised for real. A blocker session holds a row
// lock inside an open transaction and then sits inside WAITFOR DELAY, which
// keeps it actively executing (status 'suspended', on the noise filter's
// drop list) rather than idle between batches; sys.dm_exec_requests carries
// no row at all for a session with no request in flight, blocker or not,
// which is a limitation of the DMV itself and predates this filter, so the
// scenario has to keep the blocker inside a request to be representative of
// what the query can ever show. A second session then blocks trying to
// touch the same row (status 'suspended' too). Both rows must survive,
// because spec section 8.1 lists blocked_by and blocking_depth as columns
// of this exact grid, and a filter that hid either side of a block would be
// worse than the noise it was built to remove.
//
// Everything the blocker does past the CREATE TABLE happens in one
// ExecContext call. Splitting it across several round trips on a pooled
// *sql.DB was tried first and does not work: go-mssqldb resets the session
// between checkouts even at MaxOpenConns(1), which rolls back the open
// transaction and drops the global temp table between calls, the same
// reset TestIsolationSurvivesASessionReset exists to catch on the tool's
// own pinned connection.
func TestSampleRequestsFilterKeepsBothSidesOfABlock(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	dsn := os.Getenv("SQLTOP_TEST_DSN")

	blocker, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	blockCtx, cancelBlock := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelBlock()
	blockDone := make(chan error, 1)
	go func() {
		_, err := blocker.ExecContext(blockCtx, `
			CREATE TABLE ##sqltop_block_test (id INT PRIMARY KEY, val INT);
			INSERT INTO ##sqltop_block_test VALUES (1, 1);
			BEGIN TRAN;
			UPDATE ##sqltop_block_test SET val = val WHERE id = 1;
			WAITFOR DELAY '00:00:06';
			ROLLBACK TRAN;`)
		blockDone <- err
	}()
	defer func() { cancelBlock(); <-blockDone }()

	// Give the setup statements time to run and the WAITFOR to start before
	// a second connection tries to touch the same row.
	time.Sleep(1500 * time.Millisecond)

	victim, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	victimCtx, cancelVictim := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelVictim()
	victimDone := make(chan error, 1)
	go func() {
		_, err := victim.ExecContext(victimCtx, `UPDATE ##sqltop_block_test SET val = val WHERE id = 1`)
		victimDone <- err
	}()
	defer func() { cancelVictim(); <-victimDone }()

	// Give the lock wait time to register before sampling.
	time.Sleep(1500 * time.Millisecond)

	rows, err := s.SampleRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var victimRow *model.RequestSample
	for i := range rows {
		if rows[i].BlockedBy != 0 {
			victimRow = &rows[i]
			break
		}
	}
	if victimRow == nil {
		t.Fatalf("no blocked row found among %d sampled rows: the filter hid the victim", len(rows))
	}

	var blockerRow *model.RequestSample
	for i := range rows {
		if rows[i].Ref.SessionID == victimRow.BlockedBy {
			blockerRow = &rows[i]
			break
		}
	}
	if blockerRow == nil {
		t.Fatalf("blocking session (spid %d) is missing from %d sampled rows: the filter hid the blocker", victimRow.BlockedBy, len(rows))
	}
	t.Logf("blocker: spid=%d status=%q open_tran=%d; victim: spid=%d status=%q open_tran=%d blocked_by=%d",
		blockerRow.Ref.SessionID, blockerRow.Status, blockerRow.OpenTran,
		victimRow.Ref.SessionID, victimRow.Status, victimRow.OpenTran, victimRow.BlockedBy)
}

// TestSampleRequestsFilterInvariant checks both directions of the noise
// filter's real predicate (is_user_process = 1, or blocked, or a parallel
// plan; see the WHERE clause's own comment in requests.go for how it got
// there) against a raw, independent reading of the same instant: nothing
// SampleRequests returns is there without one of those reasons, and
// nothing that has one of those reasons is missing. The second half is the
// one that matters most - it is a direct check of "no real user work is
// silently hidden", read from sys.dm_exec_sessions.is_user_process itself
// rather than reconstructed from status text the way the ported prototype
// predicate tried to and failed (see TestSampleRequestsSeesALongQuery).
func TestSampleRequestsFilterInvariant(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	// A side connection reads the same instant's ground truth independently
	// of the query under test, so this does not just check the query
	// against its own idea of what is real.
	raw, err := sql.Open("sqlserver", os.Getenv("SQLTOP_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	rows, err := s.SampleRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	present := make(map[int64]bool, len(rows))
	for _, r := range rows {
		present[r.Ref.SessionID] = true
	}

	type ground struct {
		sessionID  int64
		isUserProc bool
		blockedBy  int64
		dop        int
	}
	var truth []ground
	grows, err := raw.QueryContext(ctx, `
		SELECT r.session_id, ISNULL(s.is_user_process, 1), ISNULL(r.blocking_session_id, 0), ISNULL(r.dop, 0)
		FROM sys.dm_exec_requests AS r LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = r.session_id
		WHERE r.session_id <> @@SPID
		OPTION (RECOMPILE, MAXDOP 1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer grows.Close()
	for grows.Next() {
		var g ground
		if err := grows.Scan(&g.sessionID, &g.isUserProc, &g.blockedBy, &g.dop); err != nil {
			t.Fatal(err)
		}
		truth = append(truth, g)
	}
	if err := grows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(truth) == 0 {
		t.Skip("nothing active this tick")
	}

	for _, g := range truth {
		shouldBeKept := g.isUserProc || g.blockedBy != 0 || g.dop > 1
		if shouldBeKept && !present[g.sessionID] {
			t.Errorf("session %d (is_user_process=%v, blocked_by=%d, dop=%d) should have been kept but is missing from %d sampled rows",
				g.sessionID, g.isUserProc, g.blockedBy, g.dop, len(rows))
		}
	}

	truthByID := make(map[int64]ground, len(truth))
	for _, g := range truth {
		truthByID[g.sessionID] = g
	}
	for _, r := range rows {
		g, found := truthByID[r.Ref.SessionID]
		if !found {
			// The ground-truth snapshot and SampleRequests ran a moment
			// apart; a request that finished in between is not a filter
			// bug.
			continue
		}
		reason := g.isUserProc || g.blockedBy != 0 || g.dop > 1
		if !reason {
			t.Errorf("session %d: kept without a reason the filter documents (is_user_process=%v, blocked_by=%d, dop=%d)",
				r.Ref.SessionID, g.isUserProc, g.blockedBy, g.dop)
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

// TestLongestTransactionGatedByReadCommittedSnapshot crosses the seam this
// fix closes: Identify discovers whether any database on the instance has
// read committed snapshot isolation on, and SampleServer(TierCounters) is
// supposed to act on that fact for longest_transaction_s, rather than the
// two simply agreeing by construction the way a test of counterState.apply
// alone, or of Identify alone, would let them. Whichever branch this run
// lands in is asserted, not skipped, so the test is meaningful whether or
// not scripts/restoredb.sh has put the read-committed-snapshot demonstration
// database on the container: a fresh container exercises the "no database
// has it on" branch, restoredb.sh's PachadataFormation exercises the other.
//
// Value itself is not asserted to be nonzero in the positive branch. This
// counter was found, against the demonstration database's own long-running
// RCSI transaction, to read zero regardless of how long that transaction
// ran - the same kind of platform gap TestOtherCPUPercentUnavailableWhenIdleIsZero
// already documents for SystemIdle. What this fix owns is Available, not
// what the engine itself chooses to put in cntr_value.
func TestLongestTransactionGatedByReadCommittedSnapshot(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	info, _, err := s.Identify(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.SampleServer(ctx, model.TierCounters)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := got.Figures["longest_transaction_s"]
	if !ok {
		t.Fatal("longest_transaction_s is missing; the dashboard needs the tile marked unavailable, not absent")
	}

	if info.HasReadCommittedSnapshot {
		if !f.Available {
			t.Fatalf("info.HasReadCommittedSnapshot = true (a database on this instance has RCSI on, run scripts/restoredb.sh for the demonstration database) but longest_transaction_s = %+v", f)
		}
	} else {
		if f.Available {
			t.Fatalf("info.HasReadCommittedSnapshot = false but longest_transaction_s = %+v, want Available false rather than the counter's literal zero", f)
		}
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
		"spid":         spidQuery,
		// instanceWideViewGrantQuery and canQueryTemplate are probe's own
		// queries (mssql.go): easy to forget here because probe runs once,
		// inside Identify, rather than on a collection tier, but they carry
		// the same RECOMPILE/MAXDOP 1 obligation as every other statement
		// this source sends.
		"instanceWideViewGrant": instanceWideViewGrantQuery,
		"can":                   canQueryTemplate,
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

// TestOtherCPUPercentUnavailableWhenIdleIsZero guards a fabrication that
// reached the container running these tests: SQL Server on Linux never
// populates SystemIdle in the scheduler-monitor ring buffer record, so a
// straight 100-idle-sqlCPU would report other processes pegged at 100% of
// the CPU on a box that is otherwise idle, marked Available. Skips rather
// than asserts the opposite on any engine where SystemIdle turns out to be
// populated, since the figure is legitimately available there.
func TestOtherCPUPercentUnavailableWhenIdleIsZero(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	var sqlCPU, idle int
	if err := s.queryRow(ctx, cpuHistoryQuery, &sqlCPU, &idle); err != nil {
		t.Skip("no scheduler-monitor record available yet on this engine")
	}
	if idle != 0 {
		t.Skip("this engine populates SystemIdle; the Linux gap this test guards does not apply here")
	}

	got, err := s.SampleServer(ctx, model.TierCPUHistory)
	if err != nil {
		t.Fatal(err)
	}
	if f := got.Figures["other_cpu_percent"]; f.Available {
		t.Fatalf("other_cpu_percent = %+v with idle == 0, want unavailable rather than a fabricated 100%%", f)
	}
}
