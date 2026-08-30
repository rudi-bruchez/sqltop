# Per-session statement capture

Design for capturing every completed batch and RPC on one chosen session,
using an Extended Events session created on demand and destroyed when the
capture ends.

This is the feature `docs/SPECS.md` section 2 named and deferred: "The XE
capture discussed in the research notes is deferred to a later version and
will be opt-in behind an explicit flag, with a named session and automatic
cleanup." It is also idea 1 of `docs/IDEES.md` seen from the other side: the
retention window answers what a request looked like while it was running, and
this answers what a session actually ran.

Everything below is scoped to SQL Server. PostgreSQL and MySQL have no
equivalent of a ring buffer target, which is why section 4.1 already puts
event capture behind a separate optional interface rather than inside
`Source`.

## 1. What it does

With a row selected in the requests grid, one key starts a capture on that
session id. Completed batches and RPCs on that session are drained from the
server every two seconds, written to a file under `traces/` as they arrive,
and shown in a panel below the grid. The same key stops it. So do five other
things, listed in section 4.

The capture is not a profiler and is not a trace of the instance. It watches
one session id, which is what makes it cheap enough to be defensible and
narrow enough to be readable.

## 2. Why this is not a breach of the read-only rule

The tool's first constraint is that it creates nothing on the monitored
server. This feature creates an object, so it needs the rule's own stated
exception rather than an argument around it, and it has one: section 2
authorises exactly this, opt-in, named, cleaned up.

Three things make the exception narrow enough to be worth taking.

Nothing happens without the flag. Without `-capture` on the command line the
tool never issues `CREATE`, `ALTER` or `DROP EVENT SESSION`, and never runs
the sweep of section 5, which is itself a `DROP`. A server whose operator did
not ask for this sees the same read-only tool it saw before.

Nothing is written to the server's disk. The target is the ring buffer, in
memory, capped at 1024 KB. There is no `.xel` file, no path to configure, no
permission to grant on a filesystem, and nothing left behind if the process
dies. The file that survives is on the machine running sqltop.

Nothing outside our own prefix is touched. Every session this feature creates
is named `sqltop_capture_<spid>_<random>`, and every `DROP` it issues is
filtered on that prefix. `ALTER ANY EVENT SESSION` is a server-wide right that
would also allow dropping `system_health`; the prefix filter is what
guarantees we never do.

Two documents have to be corrected before this ships, and both corrections
are consequences of section 2 rather than new licence. `CLAUDE.md` states the
constraint as "No object created" without section 2's MVP qualifier. Section
12 of the specification says "No configuration of the monitored server. The
tool reads.", which section 2 already contradicts and which should be read,
and reworded, as persistent configuration: a setting, a trace flag, an
`sp_configure` value, anything that outlives the run. A named session created
and destroyed inside one run of the tool is not that, and section 12's real
subject, stated in its own first paragraph, is unattended recording, which
section 4 of this document forbids just as firmly.

## 3. The interface between the core and the source

Section 4.1 requires that no SQL Server concept leak upward. The core knows
about captures; it does not know about event sessions.

```go
// package model

// CapturedStatement is one completed batch or RPC. Durations are
// microseconds, because Extended Events reports microseconds and a 400 us
// batch rounded to zero milliseconds is a zero that lies, on exactly the
// kind of statement this feature exists to show.
type CapturedStatement struct {
    At            time.Time
    Kind          string // "batch" or "rpc"
    Object        string // RPC only
    Text          string
    DurationUs    int64
    CPUUs         int64
    LogicalReads  int64
    PhysicalReads int64
    Writes        int64
    RowCount      int64
    Result        string
    Database      string
    Application   string
    User          string
}

// CaptureProgress is what the source reports alongside a batch of
// statements, and it is what makes the panel honest about loss.
type CaptureProgress struct {
    Total     int64 // events the server has processed for this capture
    Missed    int64 // events that passed through the buffer unread
    Dropped   int64 // events the server dropped before the target
    Truncated bool  // the ring buffer XML exceeded 4 MB
}
```

```go
// package source

// Capturer is optional. A source that cannot capture does not implement it,
// and the interface never appears in Source.
type Capturer interface {
    CanCapture(ctx context.Context) (bool, string, error)
    SweepCaptures(ctx context.Context) (dropped int, err error)
    RunningCaptures(ctx context.Context) ([]string, error)
    StartCapture(ctx context.Context, spid int64) (CaptureHandle, error)
    PollCapture(ctx context.Context, h CaptureHandle) ([]model.CapturedStatement, model.CaptureProgress, error)
    StopCapture(ctx context.Context, h CaptureHandle) error
}
```

`CanCapture` returns a reason when it returns false, because a greyed key
with no explanation is the failure mode this project has already fixed twice
in the dashboard. It probes
`HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION')` rather than
guessing from the login's role membership, and it refuses Azure SQL Database
outright.

A new capability, `CapCaptureSession`, joins the existing set so the UI can
grey the key rather than fail on use.

## 4. Lifecycle

One capture at a time per running instance of sqltop. Two would double the
server cost and complicate the panel for no diagnostic gain. Pressing the key
on another row stops the first capture and says so.

The capture stops on any of six conditions, and the panel header always says
which one ended it.

The key is pressed again. The process shuts down, including on `SIGINT`. The
last browser connection has been gone for thirty seconds, which tolerates a
page reload without leaking a capture behind a closed tab. The watched
session disappears from `sys.dm_exec_sessions`. Its `login_time` changes,
which means the connection pool has handed the session to somebody else and
what we would capture from here is a stranger's work. Ten minutes elapse.

Ten minutes is not configurable, and that is a decision rather than an
omission. Section 5 sweeps other instances' abandoned captures using the cap
as its evidence, and that reasoning only holds if every instance agrees on the
value. If a configurable cap is ever wanted, the cap has to be encoded in the
session name so a sweeper applies the owner's contract rather than its own;
that is the way out, and it is not taken today. SQL Server 2025 and Azure have
a server-side time-bound session option, but the version floor is 2019, so the
cap is enforced by the client on every version for consistency.

When the cap fires the capture stops, the header says so, and the key starts a
fresh one. There is no automatic renewal: an unattended capture that renews
itself is the unattended recording section 12 rules out.

## 5. Recovering what a crash left behind

An event session outlives the process that created it. A `kill -9` leaves one
running on the server, and something has to remove it.

The difficulty is that the same tool can be run several times against the same
server, by different people on different machines, so an instance cannot claim
ownership of a session it finds. A name cannot carry proof of ownership: a pid
is meaningless across machines. The sweep therefore does not look for an
owner. It looks for what is dead by construction, and there are exactly two
such states.

A session under our prefix that exists in `sys.server_event_sessions` but is
absent from `sys.dm_xe_sessions` is not started. A live capture is always
started, and a stopped capture has its definition dropped, so a stopped
definition is a residue with no ambiguity and no clock involved. This is the
case after a server restart, where `STARTUP_STATE = OFF` does precisely its
job.

A session under our prefix that is started and whose `sys.dm_xe_sessions.create_time`
is older than twice the cap belongs to nobody: a live instance would have
stopped it at the cap. Twice the cap is a margin nothing legitimate reaches.

Anything started and younger than that is left alone. It is probably somebody
else's, and destroying a colleague's capture is a worse failure than leaving a
stale one for another twenty minutes.

The sweep runs at connection and again before each new capture, and only when
the flag is set. A failed `DROP`, typically a permission the login does not
have, is reported in the panel header and not retried in a loop.

## 6. The event session

```sql
CREATE EVENT SESSION [sqltop_capture_51_a3f2c9] ON SERVER
ADD EVENT sqlserver.sql_batch_completed (
    ACTION (sqlserver.database_name, sqlserver.client_app_name, sqlserver.username)
    WHERE (sqlserver.session_id = 51)
),
ADD EVENT sqlserver.rpc_completed (
    ACTION (sqlserver.database_name, sqlserver.client_app_name, sqlserver.username)
    WHERE (sqlserver.session_id = 51)
)
ADD TARGET package0.ring_buffer (SET MAX_MEMORY = 1024)
WITH (
    MAX_MEMORY = 2 MB,
    EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS,
    MAX_DISPATCH_LATENCY = 2 SECONDS,
    TRACK_CAUSALITY = OFF,
    STARTUP_STATE = OFF
);
```

Every clause is load-bearing.

`EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS` is the one that matters most.
`NO_EVENT_LOSS` makes the monitored workload wait for the buffer, which turns
a diagnostic tool into the outage. It must never be used here, and a test
should assert the generated DDL does not contain it.

The ring buffer target is capped at 1024 KB on Microsoft's own advice. The
documented consumption limit is a 4 MB XML document, and a larger buffer
simply produces truncated reads. 1024 KB leaves room for the XML markup and
Unicode expansion that pushes the document past the buffer's own size.

`MAX_DISPATCH_LATENCY = 2 SECONDS` matches the drain interval, so events reach
the target at roughly the rate we read them.

`TRACK_CAUSALITY = OFF` because the activity id it would add is work done on
the monitored thread, and section 7 explains why we minimise that.

The session id is a literal in the predicate, not a parameter: an event
session predicate is compiled when the session is created. The value comes
from a row of our own grid and is an `int64`, and it is rendered through an
integer formatter, never through string concatenation of user input. The
session name's random suffix comes from `crypto/rand` and is hex, so the
bracketed identifier can contain nothing that needs escaping. A test should
assert both.

## 7. Draining, and knowing what was lost

A goroutine per capture polls every two seconds. It does not wait for the
panel to be open: a capture that nobody drains fills its buffer and loses
events silently, so draining is independent of whether anybody is looking.

Each poll reads the target once:

```sql
SELECT CAST(t.target_data AS xml), s.dropped_event_count, s.dropped_buffer_count
FROM sys.dm_xe_sessions AS s
JOIN sys.dm_xe_session_targets AS t ON t.event_session_address = s.address
WHERE s.name = @name AND t.target_name = 'ring_buffer';
```

The XML is parsed with `encoding/xml`. The ring buffer returns everything it
still holds on every read, so the same event is seen many times and has to be
placed rather than deduplicated by guesswork.

The `RingBufferTarget` element carries `totalEventsProcessed`, cumulative for
the life of the session, and `eventCount`, the number currently held. The
buffer therefore holds events numbered `totalEventsProcessed - eventCount`
through `totalEventsProcessed - 1`, in order, which gives every event an exact
absolute index. The drain keeps a high water mark, emits everything at or
above it, and sets the mark to `totalEventsProcessed`.

That arithmetic is also the loss report. If `totalEventsProcessed - eventCount`
is greater than the mark, exactly that many events passed through the buffer
between two polls and were never read. The count is exact, it is written to
the file as a gap record, and it is shown in the panel. This is a different
loss from `dropped_event_count`, which counts what the server discarded before
it ever reached the target; both are reported, separately, because they mean
different things.

`truncated="1"` on the element means the XML omitted part of the buffer, at
which point positional arithmetic cannot be trusted. The 1024 KB cap is there
to make this not happen; if it happens anyway the drain records a gap of
unknown size, re-anchors the mark to `totalEventsProcessed`, and says so. An
unknown gap that announces itself is acceptable. A silent one is not.

The last thousand statements are kept in memory for the panel, so opening it
does not re-read the file.

If the connection to the server is lost, the existing repair path reconnects
and the drain resumes; the event session is server-scoped and unaffected. If
the server cannot be reached for longer than the cap, the capture is
considered ended, and the `DROP` is attempted on the next successful
connection, with the sweep as the backstop.

## 8. The file

`traces/capture-<spid>-<yyyy-mm-dd-hhmmss>.jsonl`, beside the executable,
created through the same `writeUnique` seam that `snapshots/` and `plans/`
already use.

JSON Lines rather than `.xel`: the `.xel` format is undocumented and cannot be
written from Go without reimplementing it, and it would in any case be the
wrong artefact for something that never existed as a file on the server. JSON
Lines appends, survives a process that dies mid-capture, and is readable by
anything.

Three record kinds share the stream, distinguished by `kind`. A header,
written first, naming the tool version, the instance, the session id, its
`login_time`, the event session name and the start time. An `event` record per
statement, carrying the fields of `CapturedStatement`. A `gap` record with a
count, or a null count where the count is unknown, and the reason. A final
`end` record with the stop reason from section 4.

Records are written as they arrive, not at the end. A file that stops
mid-stream because the process was killed is still a valid partial capture,
and its last record tells the reader it was not a clean end.

The trace directory is resolved beside the binary exactly as `snapshots/` and
`plans/` are. That resolution and `writeUnique` currently live in
`internal/web`; this is their third consumer, which is the second real
implementation the project's own rule waits for, so they move to a small
shared package rather than being copied.

## 9. The panel

A sixth mode of the detail panel, on `c`, alongside `t`, `e`, `y` and `n`.
`c` was reserved in section 7 of the specification for a sub-mode of the waits
view; that view does not exist yet, and a good mnemonic for a shipped feature
outranks a reserved one for an unwritten one. The waits sub-mode will need
another key or a click when it lands.

The header states the situation in one line and never leaves the reader
guessing: capture unavailable and why, capture running on session 51 since
2 minutes with 1 480 statements, capture ended because the session id was
reused, or capture ended at the ten minute cap. Loss is shown beside the
count when it is not zero, with the two kinds distinguished.

The header also lists any other `sqltop_capture_` session running on the
instance. Two people capturing the same session id is legitimate and the
random suffix makes it possible, but it doubles the dispatch cost on the
monitored workload, and section 10 explains why nothing else in this tool will
tell them.

The table is a view of the catalogue like every other, with a `capture` entry
whose columns are configurable and reorderable through the existing mechanism.
Its columns are time, kind, database, duration, CPU, logical reads, writes,
rows, result and text. Durations are shown in milliseconds with enough decimal
places that a sub-millisecond statement does not read as zero.

Arrow key navigation and the statement panel behave as they do in the other
views.

## 10. What this costs, and what we cannot see

The tool's observation budget measures the CPU of its own session. Extended
Events dispatch does not run there: predicate evaluation and event
construction happen on the thread of the workload being watched. This is the
first feature in sqltop whose cost the tool's own instrument cannot see, and
that has to be stated in `docs/PERFORMANCE.md` as a limitation rather than
discovered later.

The predicate is a single integer comparison per candidate event, which is the
cheapest predicate Extended Events offers, and the two events chosen fire once
per batch rather than once per statement. The expected cost is small. Expected
is not measured, and the honest position is that it will be measured against
the containers before this ships, with the number recorded next to the others.

The drain query itself runs on our connection and is visible to the budget: it
casts a bounded amount of XML on the server every two seconds and belongs in
the same table as the other on-demand queries.

## 11. Testing

The integration suite runs against real containers, which is the only way this
feature can be tested at all. Against 2019, 2022 and 2025: create a capture on
a session driven by the test itself, run a known batch and a known RPC on it,
and assert both appear with plausible durations. Assert the event session no
longer exists after the stop. Assert a killed drain leaves a session that the
sweep then removes, which is the crash path made explicit rather than assumed.

The sweep's two rules each need a test that builds the state directly: a
stopped session under the prefix, and a started session whose `create_time` is
old, faked by creating it and shifting the comparison rather than waiting
twenty minutes. A third test asserts that a started session younger than the
threshold survives a sweep, because that is the property protecting a
colleague's capture and it is the one most likely to regress.

`TestNoQueryWritesToTheMonitoredServer` forbids `CREATE`, `ALTER`, `DROP` and
`EXEC` across the query catalogue and must gain an exception. The exception is
a named set of capture statements excluded by name, plus a companion test
asserting that every statement in that set targets an identifier beginning
with `sqltop_capture_`. An exception that merely turns the check off would
give up the property the check exists to hold.

Two properties get their own tests because getting them wrong is expensive:
the generated DDL never contains `NO_EVENT_LOSS`, and the generated session
name and predicate contain nothing but the integer session id and hex.

The panel gets geometric assertions like the other views, and each one is
verified by breaking what it asserts and watching it fail, as the interface
tests already require.

## 12. What is deliberately not here

No `event_file` target, therefore no `.xel`, on any deployment. The moment the
tool writes to the server's disk it needs a path, a permission, a retention
policy and a cleanup story on a filesystem it cannot see.

No Azure SQL Database. Its event sessions are database-scoped, use different
catalogue views and a different DMV set, and cannot be tested here, since
Azure SQL Database cannot be containerised. Guessing at an untestable code
path is how the wrong thing ships quietly. It is refused with a message that
says why. Managed Instance is server-scoped and is expected to work; like
Managed Instance edition detection, that is asserted from documentation and
not from a test, and is marked as such.

No capture of anything but one session id. Capturing a database, a login or an
application name is a profiler, and a profiler needs a file target, a
retention policy and a cost model this design does not have.

No filter on statement text, no aggregation, no grouping in the first version.
The file holds everything captured, and grouping over it is a client-side
question that can be answered later without changing anything on the server.

No renewal of an expired capture, and no capture that starts at launch. Both
turn a tool somebody runs when they are worried into a recorder that runs when
nobody is watching, which section 12 of the specification rules out.
