package mssql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// The three on-demand views of spec section 7, against a real engine. They
// are integration tests and nothing else, on purpose: each one is a single
// query whose whole risk is that SQL Server rejects it or hands back a
// column shape the scan does not expect, and neither of those is something
// a fake can tell you.

func TestSessionsListsThisConnection(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identify(t, s, ctx)

	rows, err := s.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no sessions at all, and this test is holding one open")
	}

	var mine bool
	for _, r := range rows {
		if r.SessionID == s.spid {
			mine = true
			if r.ConnectedSec < 0 {
				t.Errorf("this session reports having been connected for %d seconds", r.ConnectedSec)
			}
			if r.Login == "" {
				t.Error("this session reports no login name")
			}
			if r.MemoryMB <= 0 {
				t.Errorf("this session reports %.2f MB of memory; every session holds some", r.MemoryMB)
			}
		}
		if r.OpenTran < 0 || r.TranSec < 0 {
			t.Errorf("session %d reports open_tran=%d tran_age=%ds", r.SessionID, r.OpenTran, r.TranSec)
		}
	}
	if !mine {
		t.Errorf("the tool's own session %d is not in a list of %d user sessions", s.spid, len(rows))
	}
}

// TestTransactionsSeesAnOpenTransactionAndWhatItLocked is the one that
// matters: an open write transaction on a second connection has to show up
// with an age, a state, some log, and the object it took a lock on.
func TestTransactionsSeesAnOpenTransactionAndWhatItLocked(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	identify(t, s, ctx)
	db := adminConn(t)

	if _, err := db.ExecContext(ctx, `IF OBJECT_ID('dbo.sqltop_tran_probe') IS NULL CREATE TABLE dbo.sqltop_tran_probe (id int NOT NULL)`); err != nil {
		t.Fatalf("could not create the probe table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS dbo.sqltop_tran_probe`)
	})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO dbo.sqltop_tran_probe (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	var spid int64
	if err := tx.QueryRowContext(ctx, `SELECT @@SPID`).Scan(&spid); err != nil {
		t.Fatal(err)
	}

	trans, locks, err := s.Transactions(ctx)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}

	var found bool
	for _, r := range trans {
		if r.SessionID != spid {
			continue
		}
		found = true
		if r.State != "active" {
			t.Errorf("the open transaction reports state %q, want active", r.State)
		}
		if r.Type != "read/write" {
			t.Errorf("a transaction that has inserted a row reports type %q", r.Type)
		}
		if r.Database == "" {
			t.Error("the open transaction names no database")
		}
		if r.LogRecords <= 0 {
			t.Errorf("an insert inside a transaction reports %d log records", r.LogRecords)
		}
		if r.ElapsedSec < 0 {
			t.Errorf("the open transaction reports an age of %d seconds", r.ElapsedSec)
		}
	}
	if !found {
		t.Fatalf("session %d holds an open write transaction and Transactions returned %d rows without it", spid, len(trans))
	}

	var named bool
	for _, l := range locks {
		if l.SessionID == spid && l.Object == "sqltop_tran_probe" {
			named = true
			if l.Count <= 0 {
				t.Errorf("a lock group with count %d", l.Count)
			}
			if l.Mode == "" || l.Status == "" {
				t.Errorf("lock group %+v has no mode or status", l)
			}
		}
	}
	if !named {
		t.Errorf("no OBJECT lock on sqltop_tran_probe among %d lock groups; the object name is the whole point of this view", len(locks))
	}
}

// TestIdleIsBlankWhileASessionIsBusy is a regression test for two versions
// of the same lie, both caught against the container rather than reasoned
// about. sys.dm_exec_sessions carries last_request_end_time while a request
// is running, holding the end of the previous request, so a session busy at
// that instant reported three seconds of idle. Comparing that end against
// last_request_start_time did not fix it either: the previous statement had
// started and finished inside the same millisecond and the two came back
// equal, so the comparison is wrong on exactly the sessions this view is
// about. The session's own status is what answers it.
func TestIdleIsBlankWhileASessionIsBusy(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	identify(t, s, ctx)

	// One connection that runs something and then genuinely sits, and one
	// that is still running when the sample is taken. Both pinned: a pool
	// would put the two statements on different sessions and this would be
	// watching the wrong ones.
	_, quietSPID := pinnedSession(t, ctx)
	busy, busySPID := pinnedSession(t, ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = busy.ExecContext(ctx, `WAITFOR DELAY '00:00:06'`)
	}()
	defer func() { <-done }()
	time.Sleep(3 * time.Second)

	rows, err := s.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	seen := 0
	for _, r := range rows {
		switch r.SessionID {
		case quietSPID:
			seen++
			if r.IdleSec < 2 {
				t.Errorf("a session idle for about three seconds reports %ds", r.IdleSec)
			}
		case busySPID:
			seen++
			if r.Status != "running" {
				t.Errorf("the busy session reports status %q", r.Status)
			}
			if r.IdleSec != 0 {
				t.Errorf("a session running a statement right now reports %ds of idle; the column would read as a measurement", r.IdleSec)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("found %d of the two sessions this test opened", seen)
	}
}

// pinnedSession opens one connection, keeps it, and returns its session id.
// Everything run through it lands on that session.
func pinnedSession(t *testing.T, ctx context.Context) (*sql.Conn, int64) {
	t.Helper()
	db := adminConn(t)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var spid int64
	if err := conn.QueryRowContext(ctx, `SELECT @@SPID`).Scan(&spid); err != nil {
		t.Fatal(err)
	}
	return conn, spid
}

// TestTransactionNamesTheDatabaseTheWorkIsIn is a regression test for a
// defect an external reviewer predicted and the container confirmed in one
// run. sys.dm_tran_database_transactions has a row per database a
// transaction has touched, and nearly every transaction touches more than
// the one its work is in: a single INSERT into a single table produced
// three rows, one of them the resource database, and the first version of
// this reported "3 databases" and named master by taking MIN(database_id).
//
// Both halves of that are the plausible-looking wrong answer this project's
// rules exist to prevent, so both are asserted here rather than left to a
// comment.
func TestTransactionNamesTheDatabaseTheWorkIsIn(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	identify(t, s, ctx)
	db := adminConn(t)

	if _, err := db.ExecContext(ctx, `IF OBJECT_ID('dbo.sqltop_db_probe') IS NULL CREATE TABLE dbo.sqltop_db_probe (id int, pad char(200))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS dbo.sqltop_db_probe`)
	})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO dbo.sqltop_db_probe (id, pad) SELECT TOP (500) object_id, 'x' FROM sys.all_columns`); err != nil {
		t.Fatal(err)
	}
	// No tempdb work is arranged on purpose: the engine adds the tempdb and
	// resource rows to this transaction by itself, which is exactly what
	// made the first version of the query report three databases for one
	// insert.

	var spid int64
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT @@SPID, DB_NAME()`).Scan(&spid, &current); err != nil {
		t.Fatal(err)
	}

	trans, _, err := s.Transactions(ctx)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	for _, r := range trans {
		if r.SessionID != spid {
			continue
		}
		if r.Database != current {
			t.Errorf("the transaction is named %q and its work is in %q", r.Database, current)
		}
		if r.Databases != 1 {
			t.Errorf("a transaction writing in one database reports spanning %d; tempdb and the resource database are not databases it spans", r.Databases)
		}
		if r.LogBytes <= 0 {
			t.Errorf("an insert of five hundred rows reports %d bytes of log", r.LogBytes)
		}
		return
	}
	t.Fatalf("session %d holds an open transaction and it is not in the list of %d", spid, len(trans))
}

func TestLogSpaceCoversEveryDatabase(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identify(t, s, ctx)

	rows, err := s.LogSpace(ctx)
	if err != nil {
		t.Fatalf("LogSpace: %v", err)
	}
	if len(rows) < 4 {
		t.Fatalf("got %d databases; every instance has at least master, model, msdb and tempdb", len(rows))
	}

	byName := map[string]bool{}
	for _, r := range rows {
		byName[r.Database] = true
		if r.RecoveryModel == "" {
			t.Errorf("%s reports no recovery model", r.Database)
		}
		// An online database always has a log, and the counters always
		// carry it. A figure of zero here would mean the join to the
		// performance counters silently matched nothing, which is exactly
		// the mistake a padded nchar column invites.
		if r.State == "ONLINE" && r.SizeMB <= 0 {
			t.Errorf("%s is online and reports a log of %.2f MB; the join to the performance counters found nothing", r.Database, r.SizeMB)
		}
		if r.UsedMB > r.SizeMB+1 {
			t.Errorf("%s reports %.2f MB active in a %.2f MB log", r.Database, r.UsedMB, r.SizeMB)
		}
		if r.UsedPercent < 0 || r.UsedPercent > 100 {
			t.Errorf("%s reports %.1f %% of its log used", r.Database, r.UsedPercent)
		}
	}
	for _, want := range []string{"master", "tempdb"} {
		if !byName[want] {
			t.Errorf("%s is missing from the log list", want)
		}
	}
}

// identify runs the capability probe the three views gate on. Without it
// they refuse, correctly: a login that cannot see past its own session must
// not be handed a list of one and told it is the instance.
func identify(t *testing.T, s *Source, ctx context.Context) {
	t.Helper()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatalf("Identify: %v", err)
	}
}

// slowStatement starts a statement that takes several seconds on a pinned
// connection and returns its ref, so the plan tests have something the
// engine is actually running. The sort is deliberate: it gives a plan with
// a dozen operators rather than one, so a per-node reading has something to
// say.
func slowStatement(t *testing.T, ctx context.Context) (model.RequestRef, chan struct{}) {
	t.Helper()
	conn, spid := pinnedSession(t, ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = conn.ExecContext(ctx, `
			SELECT TOP (400000) a.name, b.name AS b
			FROM sys.all_columns AS a CROSS JOIN sys.all_objects AS b
			ORDER BY a.name, b.name OPTION (MAXDOP 1)`)
	}()
	// Long enough for the request to exist and short enough not to slow the
	// suite: the statement above runs for several seconds.
	time.Sleep(1500 * time.Millisecond)
	t.Cleanup(func() { <-done })
	return model.RequestRef{SessionID: spid, RequestID: 0}, done
}

// TestPlanProgressFollowsARunningStatement is the point of the feature: the
// row counts move while the statement runs, and they are read against the
// optimiser's estimates.
func TestPlanProgressFollowsARunningStatement(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	identify(t, s, ctx)

	ref, _ := slowStatement(t, ctx)
	nodes, err := s.PlanProgress(ctx, ref)
	if err != nil {
		t.Fatalf("PlanProgress: %v", err)
	}
	if len(nodes) < 3 {
		t.Fatalf("a sort over a cross join reported %d operators", len(nodes))
	}

	var withRows, withEstimate int
	for _, n := range nodes {
		if n.Operator == "" {
			t.Errorf("node %d has no operator name", n.NodeID)
		}
		if n.Threads < 1 {
			t.Errorf("node %d reports %d threads", n.NodeID, n.Threads)
		}
		if n.Rows > 0 {
			withRows++
		}
		if n.Estimated > 0 {
			withEstimate++
		}
	}
	if withRows == 0 {
		t.Error("no operator has produced a row; a statement that has been running for a second and a half has")
	}
	if withEstimate == 0 {
		t.Error("no operator carries an estimate, and the whole reading is rows against estimate")
	}
}

// TestPlanIsShowplanXMLBothWays. The live plan is the one worth saving, and
// a server that cannot produce one still has to answer with the plan as
// compiled rather than with nothing.
func TestPlanIsShowplanXMLBothWays(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	identify(t, s, ctx)

	ref, _ := slowStatement(t, ctx)
	for _, live := range []bool{true, false} {
		plan, err := s.Plan(ctx, ref, live)
		if err != nil {
			t.Fatalf("Plan(live=%v): %v", live, err)
		}
		if plan.Format != "showplan-xml" {
			t.Errorf("live=%v: format %q", live, plan.Format)
		}
		if plan.Live != live {
			t.Errorf("live=%v: the plan reports Live=%v", live, plan.Live)
		}
		body := string(plan.Payload)
		if !strings.Contains(body, "ShowPlanXML") {
			t.Fatalf("live=%v: what came back is not showplan XML: %.120q", live, body)
		}
		// The one thing that separates the two: a live plan carries what
		// each operator has actually produced.
		if got := strings.Contains(body, "RunTimeCountersPerThread"); got != live {
			t.Errorf("live=%v: runtime counters present=%v", live, got)
		}
	}
}

// TestPlanOfAFinishedRequestSaysSo rather than returning an empty file or a
// bare error: a plan asked for a moment too late is the ordinary case.
func TestPlanOfAFinishedRequestSaysSo(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identify(t, s, ctx)

	_, err := s.Plan(ctx, model.RequestRef{SessionID: 32000, RequestID: 0}, false)
	if err == nil {
		t.Fatal("a session that is not running anything returned a plan")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("the error reads %q; it should say the request is gone", err)
	}
}
