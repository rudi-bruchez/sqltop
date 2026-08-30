package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCaptureDDLNeverStallsTheWorkload(t *testing.T) {
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if strings.Contains(strings.ToUpper(ddl), "NO_EVENT_LOSS") {
		t.Fatal("the capture DDL asks the engine to block the monitored workload when the buffer fills")
	}
	if !strings.Contains(ddl, "ALLOW_SINGLE_EVENT_LOSS") {
		t.Error("the retention mode is not stated, so it defaults rather than being chosen")
	}
}

func TestCaptureDDLStatesBothRingBufferCaps(t *testing.T) {
	// Measured on 2019 and 2022 at 1024 KB and again at 4096 KB: a target
	// naming only MAX_MEMORY holds exactly 1000 events, because the event
	// limit defaults and governs. The memory figure alone describes a
	// buffer the feature never receives.
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if !strings.Contains(ddl, "MAX_EVENTS_LIMIT = 1000") {
		t.Error("the event count cap is left implicit, so the default governs silently")
	}
	if !strings.Contains(ddl, "MAX_MEMORY = 1024") {
		t.Error("the ring buffer target memory cap is missing")
	}
	if !strings.Contains(ddl, "STARTUP_STATE = OFF") {
		t.Error("without STARTUP_STATE = OFF a leftover session returns after a server restart")
	}
}

func TestCaptureSessionNameIsPrefixedAndInert(t *testing.T) {
	ok := regexp.MustCompile(`^sqltop_capture_[0-9]+_[0-9a-f]{8}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name, err := captureSessionName(51)
		if err != nil {
			t.Fatal(err)
		}
		if !ok.MatchString(name) {
			t.Fatalf("name %q is not the prefix, an integer and hex", name)
		}
		seen[name] = true
	}
	if len(seen) < 90 {
		t.Errorf("only %d distinct names in 100; the suffix is not random enough", len(seen))
	}
}

func TestTheSweepComparesTimesOnTheSameClock(t *testing.T) {
	// The defect this test exists for shipped green on every UTC container
	// and would have destroyed a colleague's live capture on any server west
	// of Greenwich. sys.dm_xe_sessions.create_time is local server time;
	// comparing it to SYSUTCDATETIME() makes every session look hours old.
	if strings.Contains(strings.ToUpper(sweepCaptureQueryTemplate), "SYSUTCDATETIME") {
		t.Fatal("the sweep compares a local-time column to UTC; west of Greenwich it drops live captures, east of it leaves dead ones")
	}
	if !strings.Contains(strings.ToUpper(sweepCaptureQueryTemplate), "SYSDATETIME") {
		t.Error("the age comparison must be made on the server, against the same clock as create_time")
	}
	if !strings.Contains(sweepCaptureQueryTemplate, capturePrefix+"%") {
		t.Error("the sweep does not filter on the prefix, so it can see other people's event sessions")
	}
}

// captureDB opens a second, independent connection for tests that need to
// drive statements on a session the Source is watching. The Source's own
// pool is capped at one connection and that one is pinned, so a second
// checkout from it never arrives.
func captureDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		t.Skip("SQLTOP_TEST_DSN is unset")
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, sql string) {
	t.Helper()
	if _, err := db.Exec(sql); err != nil {
		t.Fatalf("%v\nwhile running: %s", err, sql)
	}
}

func countSessions(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sys.server_event_sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sessionExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sys.server_event_sessions WHERE name = @p1", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestSweepRemovesAStoppedDefinition(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9999_deadbeef"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9999, 9999))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	// Created with STARTUP_STATE = OFF and never started: a residue.
	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, db, name) {
		t.Error("the stopped definition survived the sweep")
	}
}

func TestSweepLeavesAYoungRunningCaptureAlone(t *testing.T) {
	// The property protecting a colleague's capture, and the one most
	// likely to regress.
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9998_beefcafe"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9998, 9998))
	mustExec(t, db, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sessionExists(t, db, name) {
		t.Fatal("the sweep destroyed a running capture younger than the cap; that is somebody else's work")
	}
}

func TestSweepRemovesAnOldRunningCapture(t *testing.T) {
	// Waiting twenty minutes is not a test. The threshold is a parameter, so
	// a negative one makes every running session older than it.
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9997_f00df00d"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9997, 9997))
	mustExec(t, db, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	if _, err := s.sweepOlderThan(context.Background(), -1*time.Minute); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, db, name) {
		t.Error("a running capture past the threshold survived the sweep")
	}
}

func TestRunningCapturesReportsTheSessionIdNotTheName(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9996_0badc0de"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9996, 9996))
	mustExec(t, db, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	notes, err := s.RunningCaptures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range notes {
		if n.SessionID == 9996 {
			found = true
			if n.Since.IsZero() {
				t.Error("the note carries no start time")
			}
		}
	}
	if !found {
		t.Errorf("RunningCaptures returned %+v, want a note for session 9996", notes)
	}
}

func TestWatchedSessionSeesALoginAndAnAbsence(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var spid int64
	if err := conn.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}

	login, ok, err := s.WatchedSession(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || login.IsZero() {
		t.Fatalf("a live session reported ok=%v login=%v", ok, login)
	}

	conn.Close()
	db.SetMaxIdleConns(0) // make the close real rather than a return to the pool
	time.Sleep(time.Second)
	if _, ok, err := s.WatchedSession(ctx, spid); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a closed session still reports present; the capture would never stop")
	}
}

func TestCaptureIsUnavailableWithoutTheFlag(t *testing.T) {
	s := open(t)
	s.captureAllowed = false
	db := captureDB(t)
	before := countSessions(t, db)

	ok, why, err := s.CanCapture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("capture is available without the flag")
	}
	if why == "" {
		t.Error("unavailable with no reason given")
	}
	// The sweep is itself a DROP and must not run either.
	if n, err := s.SweepCaptures(context.Background()); err != nil || n != 0 {
		t.Errorf("the sweep ran without the flag: %d dropped, err %v", n, err)
	}
	if got := countSessions(t, db); got != before {
		t.Errorf("event session count moved from %d to %d without the flag", before, got)
	}
}
