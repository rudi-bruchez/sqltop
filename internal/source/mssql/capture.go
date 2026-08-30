package mssql

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
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
const sweepCaptureQueryTemplate = `SELECT s.name
FROM sys.server_event_sessions AS s
LEFT JOIN sys.dm_xe_sessions AS x ON x.name = s.name
WHERE s.name LIKE '` + capturePrefix + `%'
  AND (x.name IS NULL
       OR x.create_time < DATEADD(minute, -%d, SYSDATETIME()))
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
