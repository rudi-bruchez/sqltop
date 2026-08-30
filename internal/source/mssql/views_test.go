package mssql

import (
	"context"
	"testing"
	"time"
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
