package mssql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

// open connects to the container, or skips. Integration tests must never fail
// on a machine without Podman; `go test ./...` stays green there.
func open(t *testing.T) *Source {
	t.Helper()
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
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

func TestSatisfiesSource(t *testing.T) {
	var _ source.Source = New()
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
		t.Fatalf("major version = %d, want at least 12; below that the tool refuses to connect", info.MajorVersion)
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
