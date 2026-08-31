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

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
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

func TestCaptureSeesABatchAndAnRPC(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()

	// A second, independent connection: the Source's pool is capped at one
	// and that one is pinned.
	db := captureDB(t)
	watched, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer watched.Close()
	var spid int64
	if err := watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	if _, err := watched.ExecContext(ctx, "SELECT 'sqltop_capture_probe_batch'"); err != nil {
		t.Fatal(err)
	}
	// A parameterised statement reaches the server as an RPC on sp_executesql.
	var n int
	if err := watched.QueryRowContext(ctx, "SELECT @p1", 42).Scan(&n); err != nil {
		t.Fatal(err)
	}

	// MAX_DISPATCH_LATENCY is two seconds, so poll until they arrive rather
	// than sleeping once and hoping.
	var got []model.CapturedStatement
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, _, err := s.PollCapture(ctx, h, 0)
		if err != nil {
			t.Fatal(err)
		}
		got = st
		if len(got) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	var batch, rpc *model.CapturedStatement
	for i := range got {
		if strings.Contains(got[i].Text, "sqltop_capture_probe_batch") {
			batch = &got[i]
		}
		if got[i].Kind == "rpc" {
			rpc = &got[i]
		}
	}
	if batch == nil {
		t.Fatalf("the batch never arrived; got %d statements", len(got))
	}
	if batch.DurationUs <= 0 {
		t.Errorf("duration is %d microseconds, which is not a duration", batch.DurationUs)
	}
	if batch.Database == "" {
		t.Error("the database_name action did not arrive")
	}
	if batch.Result != "OK" {
		t.Errorf("result is %q, want OK; the numeric code is not the result", batch.Result)
	}
	if rpc == nil {
		t.Fatal("the parameterised statement did not arrive as an rpc")
	}
}

func TestCaptureIgnoresOtherSessions(t *testing.T) {
	// The whole cost argument for this feature rests on the predicate being
	// scoped to one session, so it gets a negative test.
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	watched, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer watched.Close()
	var spid int64
	if err := watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	other, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ExecContext(ctx, "SELECT 'sqltop_capture_probe_other_session'"); err != nil {
		t.Fatal(err)
	}
	if _, err := watched.ExecContext(ctx, "SELECT 'sqltop_capture_probe_watched'"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)

	got, _, err := s.PollCapture(ctx, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenWatched := false
	for _, st := range got {
		if strings.Contains(st.Text, "probe_other_session") {
			t.Fatal("the predicate is not scoped to one session")
		}
		if strings.Contains(st.Text, "probe_watched") {
			seenWatched = true
		}
	}
	if !seenWatched {
		t.Fatal("the watched session's own statement never arrived, so the absence above proves nothing")
	}
}

func TestStopRemovesTheSession(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	h, err := s.StartCapture(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, h.Name)) })
	if !sessionExists(t, db, h.Name) {
		t.Fatal("StartCapture left no session on the server")
	}
	if err := s.StopCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, db, h.Name) {
		t.Error("the event session survived StopCapture; it would outlive the process")
	}
}

func TestStopRefusesANameThatIsNotOurs(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	err := s.StopCapture(context.Background(), source.CaptureHandle{Name: "system_health"})
	if err == nil {
		t.Fatal("StopCapture dropped a session outside the prefix; this login could drop system_health")
	}
}

func TestPollRefusesANameThatIsNotOurs(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	_, _, err := s.PollCapture(context.Background(), source.CaptureHandle{Name: "system_health"}, 0)
	if err == nil {
		t.Fatal("PollCapture read a session outside the prefix")
	}
}

func TestPollReportsMissedEventsUnderLoad(t *testing.T) {
	// The buffer holds a thousand. Driving more than that between two polls
	// must produce an exact count, not merely a noticed gap.
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	watched, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer watched.Close()
	var spid int64
	if err := watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	for i := 0; i < 2500; i++ {
		if _, err := watched.ExecContext(ctx, fmt.Sprintf("SELECT %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(4 * time.Second)

	_, prog, err := s.PollCapture(ctx, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Missed == 0 {
		t.Fatalf("2500 statements through a 1000 event buffer reported no loss; progress %+v", prog)
	}
	if prog.Total < 2500 {
		t.Errorf("Total is %d, want at least the 2500 driven", prog.Total)
	}
}

func TestWithoutTheFlagNothingIsEverCreated(t *testing.T) {
	// The read-only guarantee in one test: the count of event sessions on
	// the server must not move, in either direction, across everything the
	// feature can be asked to do.
	s := open(t)
	db := captureDB(t)
	_, caps, err := s.Identify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Has(model.CapCaptureSession) {
		t.Fatal("the capture capability is present without the flag")
	}
	ok, why, err := s.CanCapture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("CanCapture said yes without the flag")
	}
	if !strings.Contains(why, "-capture") {
		t.Errorf("the reason is %q; it has to name the flag, or the panel cannot tell the user what to do", why)
	}

	before := countSessions(t, db)
	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartCapture(context.Background(), 51); err == nil {
		t.Error("StartCapture succeeded without the flag")
	}
	if got := countSessions(t, db); got != before {
		t.Errorf("event session count moved from %d to %d without the flag", before, got)
	}
}

func TestTheCapabilityAppearsWithTheFlag(t *testing.T) {
	s := open(t)
	s.AllowCapture(true)
	_, caps, err := s.Identify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Has(model.CapCaptureSession) {
		t.Error("the flag is on and the login is sa, so the capability should be present")
	}
}
