package mssql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

// capturePrefix names every event session this tool creates, and is what
// every DROP it issues filters on. ALTER ANY EVENT SESSION is a server-wide
// right that would also allow dropping system_health; the prefix is what
// guarantees we never do.
const capturePrefix = "sqltop_capture_"

// captureCap is how long a capture may run. Deliberately not configurable:
// the sweep uses it as evidence about captures belonging to other instances,
// and that reasoning holds only while every instance agrees. Encoding it in
// the session name is the way out if a knob is ever wanted.
const captureCap = 10 * time.Minute

// captureSessionName builds an identifier that can carry nothing but an
// integer and hex, which is what makes bracketing it safe.
func captureSessionName(spid int64) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d_%s", capturePrefix, spid, hex.EncodeToString(b[:])), nil
}

// The predicate is a literal because an event session predicate is compiled
// at creation and cannot be parameterised. The session id comes from a row of
// our own grid as an int64 and is rendered by %d.
const createCaptureQueryTemplate = `CREATE EVENT SESSION [%s] ON SERVER
ADD EVENT sqlserver.sql_batch_completed (
    ACTION (sqlserver.database_name, sqlserver.client_app_name, sqlserver.username)
    WHERE (sqlserver.session_id = %d)
),
ADD EVENT sqlserver.rpc_completed (
    ACTION (sqlserver.database_name, sqlserver.client_app_name, sqlserver.username)
    WHERE (sqlserver.session_id = %d)
)
ADD TARGET package0.ring_buffer (SET MAX_EVENTS_LIMIT = 1000, MAX_MEMORY = 1024)
WITH (
    MAX_MEMORY = 2 MB,
    EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS,
    MAX_DISPATCH_LATENCY = 2 SECONDS,
    TRACK_CAUSALITY = OFF,
    STARTUP_STATE = OFF
)`

const startCaptureQueryTemplate = `ALTER EVENT SESSION [%s] ON SERVER STATE = START`

const stopCaptureQueryTemplate = `DROP EVENT SESSION [%s] ON SERVER`

// sweepCaptureQueryTemplate names what is dead by construction and nothing
// else. A definition that is not started is a residue, because a live capture
// is always started and a stopped one has its definition dropped. A started
// session older than twice the cap belongs to nobody, because a live instance
// would have stopped it at the cap. Anything started and younger is left
// alone: it is probably another instance's, and destroying a colleague's
// capture is worse than leaving a stale one for another twenty minutes.
//
// SYSDATETIME and not SYSUTCDATETIME: create_time is local server time.
//
// The LIKE wildcard is doubled because this whole string is a format string;
// go vet reads it as one and rejects a lone percent before the tests do.
//
// The offset carries its own sign rather than the template writing -%d: a
// negative threshold would render as -- and comment out the rest of the
// statement, which is how the test that shortens the age found it.
const sweepCaptureQueryTemplate = `SELECT s.name
FROM sys.server_event_sessions AS s
LEFT JOIN sys.dm_xe_sessions AS x ON x.name = s.name
WHERE s.name LIKE '` + capturePrefix + `%%'
  AND (x.name IS NULL
       OR x.create_time < DATEADD(minute, %d, SYSDATETIME()))
OPTION (MAXDOP 1)`

const runningCapturesQuery = `SELECT x.name, x.create_time
FROM sys.dm_xe_sessions AS x
WHERE x.name LIKE '` + capturePrefix + `%'
OPTION (MAXDOP 1)`

// The name is formatted in rather than bound, because this package binds
// nothing: every parameterised query here is a template. Safe for the same
// reason the identifier in the DDL is, and for one more: the name comes from
// captureSessionName, whose output a test constrains to the prefix, an
// integer and hex, and PollCapture refuses a handle that does not match.
//
// dropped_buffer_count is deliberately not selected. Nothing displays it, and
// a column scanned into a variable nobody reads is one the next person
// assumes means something.
const drainCaptureQueryTemplate = `SELECT CAST(t.target_data AS nvarchar(max)), s.dropped_event_count
FROM sys.dm_xe_sessions AS s
JOIN sys.dm_xe_session_targets AS t ON t.event_session_address = s.address
WHERE s.name = '%s' AND t.target_name = 'ring_buffer'
OPTION (MAXDOP 1)`

// capturePermissionQuery asks for both rights, because neither implies the
// other: a login able to create a session but not to read the DMVs would
// create a capture it could never drain.
const capturePermissionQuery = `SELECT
    HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION'),
    HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE')
OPTION (MAXDOP 1)`

// watchedSessionQueryTemplate answers whether the captured session is still
// the one the capture started on. login_time moves when a pooled connection is
// reset, which the project measured while building the sessions view;
// connect_time does not, which is why this reads login_time and not that.
const watchedSessionQueryTemplate = `SELECT s.login_time
FROM sys.dm_exec_sessions AS s
WHERE s.session_id = %d
OPTION (MAXDOP 1)`

// spidFromCaptureName recovers the session id a capture watches from the name
// it was given, so a note names what is being watched rather than an event
// session nobody outside this package should have to know about. Zero when
// the name is not one of ours.
func spidFromCaptureName(name string) int64 {
	rest, ok := strings.CutPrefix(name, capturePrefix)
	if !ok {
		return 0
	}
	digits, _, _ := strings.Cut(rest, "_")
	spid, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0
	}
	return spid
}

// CanCapture reports whether a capture is possible, and says why not when it
// is not. Three gates, in the order a reader would ask them.
func (s *Source) CanCapture(ctx context.Context) (bool, string, error) {
	if !s.captureAllowed {
		return false, "capture is off; start sqltop with -capture to allow it", nil
	}
	// knownDeployment rather than s.info directly: info is written under
	// s.mu at the end of Identify, and reading it unsynchronised from a
	// handler goroutine is a race whatever the value turns out to be. It is
	// also empty until Identify finishes, so the Azure refusal is simply not
	// yet knowable on the first call, and an unknown deployment is allowed
	// through rather than refused: the permission probe below is the real
	// gate, and Azure fails it anyway because sys.dm_xe_sessions is not the
	// view it has.
	if s.knownDeployment() == model.DeploymentAzureSQLDB {
		return false, "Azure SQL Database has only database-scoped event sessions, which this is not written for", nil
	}
	var ddl, view bool
	if err := s.queryRow(ctx, capturePermissionQuery, &ddl, &view); err != nil {
		return false, "", err
	}
	switch {
	case !ddl && !view:
		return false, "this login has neither ALTER ANY EVENT SESSION nor VIEW SERVER STATE", nil
	case !ddl:
		return false, "this login lacks ALTER ANY EVENT SESSION", nil
	case !view:
		return false, "this login lacks VIEW SERVER STATE, so a capture could be created but never read", nil
	}
	return true, "", nil
}

func (s *Source) SweepCaptures(ctx context.Context) (int, error) {
	return s.sweepOlderThan(ctx, 2*captureCap)
}

// sweepOlderThan is SweepCaptures with the threshold exposed, so the age rule
// can be tested without waiting twenty minutes.
func (s *Source) sweepOlderThan(ctx context.Context, age time.Duration) (int, error) {
	if !s.captureAllowed {
		return 0, nil
	}
	var names []string
	q := fmt.Sprintf(sweepCaptureQueryTemplate, -int(age.Minutes()))
	err := s.query(ctx, q, func(rows *sql.Rows) error {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
		return nil
	})
	if err != nil {
		return 0, err
	}

	var dropped int
	for _, n := range names {
		// Belt and braces. The query filters on the prefix and so does
		// this: the right this feature holds would also allow dropping
		// system_health, and one filter is one mistake away from that.
		if !strings.HasPrefix(n, capturePrefix) {
			continue
		}
		drop := fmt.Sprintf(stopCaptureQueryTemplate, n)
		if err := s.exec(ctx, drop); err != nil {
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}

// RunningCaptures lists the captures alive on this instance, this Source's
// own included: nothing on the server distinguishes them, and the caller
// knows which session it started on.
//
// Since is the server's local wall clock, taken as the driver hands it over
// and never adjusted, on the same terms as Source.startTime.
func (s *Source) RunningCaptures(ctx context.Context) ([]model.CaptureNote, error) {
	var notes []model.CaptureNote
	err := s.query(ctx, runningCapturesQuery, func(rows *sql.Rows) error {
		var name string
		var since time.Time
		if err := rows.Scan(&name, &since); err != nil {
			return err
		}
		notes = append(notes, model.CaptureNote{SessionID: spidFromCaptureName(name), Since: since})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return notes, nil
}

// WatchedSession returns the login time of a session, and whether it is still
// there at all. No row is the session having ended, which is an answer and
// not an error: it is the commonest reason a capture has to stop.
func (s *Source) WatchedSession(ctx context.Context, spid int64) (time.Time, bool, error) {
	var login time.Time
	q := fmt.Sprintf(watchedSessionQueryTemplate, spid)
	switch err := s.queryRow(ctx, q, &login); {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, err
	}
	return login, true, nil
}

var _ source.Capturer = (*Source)(nil)

// StartCapture creates the session and starts it. It sweeps first so a
// crashed predecessor is cleaned rather than accumulated beside.
func (s *Source) StartCapture(ctx context.Context, spid int64) (source.CaptureHandle, error) {
	if !s.captureAllowed {
		return source.CaptureHandle{}, errors.New("mssql: capture is off")
	}
	if _, err := s.SweepCaptures(ctx); err != nil {
		return source.CaptureHandle{}, err
	}
	name, err := captureSessionName(spid)
	if err != nil {
		return source.CaptureHandle{}, err
	}
	create := fmt.Sprintf(createCaptureQueryTemplate, name, spid, spid)
	if err := s.exec(ctx, create); err != nil {
		return source.CaptureHandle{}, err
	}
	start := fmt.Sprintf(startCaptureQueryTemplate, name)
	if err := s.exec(ctx, start); err != nil {
		// A session created but not started must not be left behind.
		// Relying on the sweep for a failure we are standing in front of is
		// how recovery paths stop being tested.
		drop := fmt.Sprintf(stopCaptureQueryTemplate, name)
		s.exec(ctx, drop)
		return source.CaptureHandle{}, err
	}
	return source.CaptureHandle{Name: name, SessionID: spid, Started: time.Now()}, nil
}

// PollCapture reads the ring buffer whole and lets parseRingBuffer place what
// it holds against mark. Progress carries the caller's mark unchanged on
// every path that returns nothing, so a failed read never advances it.
func (s *Source) PollCapture(ctx context.Context, h source.CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	if !strings.HasPrefix(h.Name, capturePrefix) {
		return nil, model.CaptureProgress{Seen: mark}, fmt.Errorf("mssql: refusing to read %q, which is not one of ours", h.Name)
	}
	var doc sql.NullString
	var dropped int64
	q := fmt.Sprintf(drainCaptureQueryTemplate, h.Name)
	switch err := s.queryRow(ctx, q, &doc, &dropped); {
	case errors.Is(err, sql.ErrNoRows):
		// The session is gone from under us. The caller decides what that
		// means; here it is nothing to read.
		return nil, model.CaptureProgress{Seen: mark}, nil
	case err != nil:
		return nil, model.CaptureProgress{Seen: mark}, err
	}
	out, prog, err := parseRingBuffer(doc.String, mark)
	if err != nil {
		return nil, model.CaptureProgress{Seen: mark}, err
	}
	prog.Dropped = dropped
	return out, prog, nil
}

// StopCapture drops the session. Dropping a stopped session is what removes
// the definition too, so there is nothing else to undo.
func (s *Source) StopCapture(ctx context.Context, h source.CaptureHandle) error {
	if !strings.HasPrefix(h.Name, capturePrefix) {
		return fmt.Errorf("mssql: refusing to drop %q, which is not one of ours", h.Name)
	}
	q := fmt.Sprintf(stopCaptureQueryTemplate, h.Name)
	return s.exec(ctx, q)
}
