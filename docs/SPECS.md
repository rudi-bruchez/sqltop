# sqltop specification

A `top` for SQL servers. Real-time view of what an instance is doing right now,
with enough short history to review a query after it has finished.

Status: scope settled. Supersedes the initial French sketch (see git history). The
rendering strategy is already settled by measurement, see `bench/README.md`.

## 1. Purpose

The tool answers, in this order, the questions a DBA asks when a server is
misbehaving:

1. Is the server healthy right now? Header dashboard.
2. What is running? Request grid.
3. Who is stuck, and behind whom? Blocking view.
4. What is the server waiting on? Waits view.
5. What keeps coming back? Repetitive queries view.
6. Why is this one query slow, right now, while it runs? Live plan progress.

It is a diagnostic instrument, not a monitoring platform. It has no agent, no
alerting, no long-term store.

## 2. Design constraints

These are binding. Anything that violates one of them is out of scope.

Single static binary. Pure Go, no CGO, cross-compiled in one command for Linux,
Windows and macOS. The web assets are embedded; nothing is fetched at runtime.

Nothing installed on the monitored server. The MVP is read-only through the
DMVs. No stored procedure, no table, no job, no Extended Events session. The XE
capture discussed in the research notes is deferred to a later version and will
be opt-in behind an explicit flag, with a named session and automatic cleanup.

Bounded observation cost. The tool must never become the problem it is meant to
diagnose. See section 8.

Source abstraction from day one, see section 4.1. SQL Server is the MVP target;
PostgreSQL and MySQL follow. No SQL Server concept leaks into the core model.

English everywhere. Code, comments, UI, docs.

## 3. Target and requirements

| Item | MVP | Later |
|---|---|---|
| Engine | SQL Server 2019 and later, on-premises; Azure SQL Database; Azure SQL Managed Instance | PostgreSQL, MySQL |
| Degraded | SQL Server 2016 SP1 to 2017: everything works except live plan progress, which needs trace flag 7412 the tool will not set | |
| Auth | SQL authentication, Microsoft Entra, and Windows / Kerberos including from Linux | |
| Permission | See the table below | |

Targeting 2019 makes the flagship feature work with nothing to enable:
lightweight profiling v3 is on by default there and on Azure SQL Database. On
2016 SP1 to 2017 the tool reports that live plan progress is unavailable and why,
rather than turning anything on.

### 3.1 Permissions per target

| Target | Needed | Note |
|---|---|---|
| SQL Server 2019 and earlier | `VIEW SERVER STATE` | |
| SQL Server 2022 and later | `VIEW SERVER PERFORMANCE STATE` | |
| Azure SQL Managed Instance | `VIEW SERVER STATE` | Behaves like on-premises |
| Azure SQL Database | `VIEW DATABASE STATE` for database-scoped views; server-scoped views need membership of `##MS_ServerStateReader##`, granted from `master` | Without it, a session sees only itself |

### 3.2 Azure SQL Database is scoped to one database

This is the deepest difference and it shapes the UI, not just the queries. A
connection to Azure SQL Database is bound to a single database; there is no
`USE`, and no instance-wide view. Consequences:

- The grid shows the sessions of that one database, not of a server.
- The `database` column carries a single value and is hidden by default there.
- Instance-level dashboard figures are unavailable or come from different
  sources. CPU in particular comes from `sys.dm_db_resource_stats`, at fifteen
  second granularity, not from the scheduler ring buffer.

The tool states plainly, in the header, that it is looking at one database
rather than an instance. Exactly which dashboard figures survive on Azure SQL
Database is to be confirmed against a live instance during implementation; the
capability mechanism in section 4.1 exists so that this can be settled per
figure without touching the UI.

### 3.3 Windows authentication from Linux, verified

The `krb5` provider of `github.com/microsoft/go-mssqldb` is pure Go. Verified by
building: with `CGO_ENABLED=0`, a program importing both the driver and the
provider links 32 `gokrb5` packages and produces a statically linked binary,
and cross-compiles to `windows/amd64`, `darwin/arm64` and `linux/arm64` from a
Linux host. Windows authentication therefore does not cost the single-static-
binary constraint.

Credentials come from a keytab, a credential cache, or a username and password,
with a `krb5.conf`.

## 4. Architecture

Three layers, no UI concept below the top one.

Collector. One goroutine per source, polling on a tiered schedule (section 8).
Produces immutable snapshots.

State. A rolling in-memory window, indexed by `(session_id, request_id,
sample_time)`. One row per sample, never one row per query: a request active for
twelve minutes must leave a series that can be replayed. Default retention 15
minutes, configurable. This window is what the derived views aggregate over, and
what the rate and ratio counters differentiate against.

Presentation. A local HTTP server, embedded assets, SSE push to the browser. The
grid is a hand-rolled virtualised renderer; Tabulator was measured and rejected
(`bench/README.md`).

### 4.1 The source layer

Yes, there is a pluggable layer, and it is the part that decides whether this
tool ever reaches PostgreSQL. The design point is that being agnostic does not
mean pretending every engine is the same. It means the core model is neutral and
that each source declares what it can do, so the UI adapts instead of the model
lying.

```go
// Source is one connection to one instance. Everything above this line is
// engine-neutral: no DMV, no showplan, no T-SQL leaks upward.
type Source interface {
    Open(ctx context.Context, dsn string) error
    Close() error

    // Identify returns instance metadata and, crucially, what this source
    // can actually deliver on this server, at this version, with these rights.
    Identify(ctx context.Context) (ServerInfo, Capabilities, error)

    // SampleRequests is the hot path, called on tier A.
    SampleRequests(ctx context.Context) ([]RequestSample, error)

    // SampleServer feeds the dashboard, called on tiers B, C and D.
    SampleServer(ctx context.Context, tier Tier) (ServerSample, error)

    // QueryText and Plan are on demand only, never in the polling loop.
    QueryText(ctx context.Context, ref RequestRef) (string, error)
    Plan(ctx context.Context, ref RequestRef, live bool) (Plan, error)

    // Kill is optional, gated by Capabilities.
    Kill(ctx context.Context, sessionID int64) error
}
```

`Capabilities` is the load-bearing piece. It is a set of flags such as
`LivePlanProgress`, `InstanceWideView`, `TempdbPerTask`, `WaitStatsCumulative`,
`SchedulerLoad`, `KillSession`. The UI hides or greys what a source cannot
provide, and the dashboard renders "n/a" rather than a plausible zero. Azure SQL
Database and SQL Server 2017 are the first two consumers of this mechanism, well
before PostgreSQL is.

A source is a Go package implementing that interface. Adding MySQL means adding
a package and registering it; it means touching nothing in the core, the state
window, or the UI.

Two rules keep the abstraction honest, learnt from the research notes:

- The event-capture path (Extended Events and its equivalents) is a separate
  optional interface, not part of `Source`. PostgreSQL and MySQL have no
  equivalent of a ring buffer target, and an abstraction that assumed one would
  be wrong on two engines out of three.
- `Plan` returns an opaque document plus its format. Showplan XML, an EXPLAIN
  tree and a MySQL plan have nothing in common; the renderer dispatches on
  format rather than the model pretending they unify.

### 4.2 One instance displayed, several within reach

A named list of instances lives in the configuration file and is presented as a
switcher, so moving from one server to another is one click.

Only the selected instance is collected. Polling every configured instance would
multiply the observation cost against servers nobody is looking at, which
contradicts section 2. On switching away, the retention window of the previous
instance is kept in memory and marked stale, so coming back shows the history up
to the moment of the switch, clearly labelled as frozen rather than live.

The wire protocol carries a flat row list. Two payload rules follow from the
bench measurements:

- The blocking chain is flattened on the Go side into a depth column. It is not
  sent as a nested tree.
- Per-session invariants (SQL text, program name, login, host) are sent once in
  a reference table keyed by session, not on every tick. The CPU history is
  appended one point at a time, not resent as a series. Measured, this removes
  47 % of the payload.

## 5. Screen layout

```
+------------------------------------------------------------------+
| server dashboard          collapsible, one line when collapsed   |
+------------------------------------------------------------------+
| view tabs   requests | blocking | waits | repetitive | throughput |
+------------------------------------------------------------------+
|                                                                  |
|  grid                                                            |
|                                                                  |
+------------------------------------------------------------------+
| detail panel     appears on row selection, live plan progress    |
+------------------------------------------------------------------+
```

The dashboard collapses to a single line of key figures and expands to the full
panel. The collapsed or expanded state is part of the saved layout.

## 6. Server dashboard

Always visible, refreshed continuously. This is the pulse of the instance.

Three mechanical points govern the whole section.

Most of these figures come from `sys.dm_os_performance_counters`. That view
returns roughly 1500 rows; the tool queries only the counters it needs, by name,
in one round trip.

Counter semantics must be respected or the numbers are wrong. `cntr_type` 65792
is a raw current value, usable as is. Types 272696320 and 272696576 are
cumulative per-second counters: the rate is the delta between two samples. Type
537003264 is a ratio whose denominator is a separate base counter of type
1073939712, and it too must be differentiated between two samples.

Which means the dashboard needs the previous sample to display anything. The
first tick after connection shows placeholders, not zeros.

| Figure | Source | Handling |
|---|---|---|
| Instance, host, edition, version, uptime | `SERVERPROPERTY`, `sys.dm_os_sys_info`, `sys.dm_os_host_info` | Once at connection |
| SQL Server CPU %, system CPU % | `sys.dm_os_ring_buffers`, `RING_BUFFER_SCHEDULER_MONITOR` | One sample per minute, 256 kept. Not available on Azure SQL DB |
| Scheduler load | `sys.dm_os_schedulers` | Runnable tasks and load factor per scheduler. This replaces the per-CPU utilisation asked for in the original sketch, which SQL Server does not expose |
| Total and target server memory | `sys.dm_os_sys_info`, `committed_kb` / `committed_target_kb` | Raw values |
| Buffer pool, plan cache, query memory | `sys.dm_os_memory_clerks`, `sys.dm_os_memory_cache_counters` | Grouped by clerk type |
| Page life expectancy | `Buffer Manager\Page life expectancy` | Raw. Per NUMA node from `Buffer Node` when there is more than one |
| Buffer cache hit ratio | `Buffer Manager\Buffer cache hit ratio` and its base | Windowed, see below |
| Memory grants pending, outstanding | `sys.dm_exec_query_resource_semaphores` | Raw |
| tempdb: free, user objects, internal objects, version store | `sys.dm_db_file_space_usage` | Raw, 5 s tier |
| Version store size and growth rate | `sys.dm_tran_version_store_space_usage` | Documented as cheap; it does not walk individual version records |
| Open transactions | `Transactions\Transactions` | Raw, all active transactions |
| Longest running transaction | `Transactions\Longest Transaction Running Time` | Only populated under read committed snapshot isolation. Show "n/a" otherwise rather than a misleading zero |
| Batch requests/sec, compilations/sec, recompilations/sec | `SQL Statistics` | Rate, delta between samples |
| Full scans/sec | `Access Methods\Full Scans/sec` | Rate, delta. This is the "table scans" figure from the original sketch. It is instance-wide, not per table |
| Active request count | Our own samples | Counted from the grid data, free |

On the buffer cache hit ratio. It was asked for explicitly, so it is in. But
Microsoft's own documentation states that the ratio covers the last few thousand
page accesses and that "after a long period of time, the ratio moves very
little". Read raw, it sits at 99-point-something on every server and tells you
nothing. The tool therefore displays the windowed value, the delta of the
counter against the delta of its base between two samples, which is what
Microsoft describes as the way to get a reading for the last second. Page life
expectancy is displayed next to it and is the figure to trust.

Each numeric tile carries a sparkline over the retention window. A number alone
does not show a server going wrong; its slope does.

## 7. Views

Activation by clicking a tab and by single keypress, in the spirit of the
PowerShell prototype. Shortcuts are shown in the tab labels.

| View | Key | Shows | Source |
|---|---|---|---|
| Requests | `r` | Every active request, one row each. The default | `sys.dm_exec_sessions`, `sys.dm_exec_requests`, `sys.dm_exec_sql_text` |
| Blocking | `b` | Blocking chains only, flattened with a depth column, ordered so a blocker is immediately above those it blocks. Head blockers highlighted | Same, plus `sys.dm_os_waiting_tasks` |
| Waits | `w` | Two sub-modes toggled with `c`: current waits per request, and cumulative wait statistics differentiated over the window | `sys.dm_os_waiting_tasks`, `sys.dm_os_wait_stats` |
| Repetitive queries | `q` | Aggregation of the retention window by `query_hash`: executions seen, distinct sessions, total CPU, average and maximum elapsed, one sample text. This is what catches the query that is individually fast and collectively ruinous | Derived from stored samples |
| Throughput | `t` | Request counts and rates over the window: active requests, batch requests/sec, compilations, recompilations, by database and by command | Derived, plus `SQL Statistics` |
| Programs | `p` | Aggregation by program name and login | Derived |

Views are projections of one shared retention window. Switching views does not
re-query the server.

## 8. Request grid

### 8.1 Columns

Every column is sortable and filterable. Two of them were called out
specifically and are already prototyped in the bench.

| Column | Notes |
|---|---|
| `spid`, `status`, `blocked_by`, `blocking_depth` | |
| `database` | Filter and sort. Named explicitly as a requirement |
| `command` | Filter and sort. Named explicitly as a requirement. `SELECT`, `INSERT`, `BACKUP DATABASE`, `DBCC`, and so on |
| `login`, `host`, `program` | |
| `elapsed`, `cpu_ms`, `cpu_sparkline` | |
| `logical_reads`, `physical_reads`, `writes` | |
| `tempdb_mb` | From `sys.dm_db_task_space_usage`, allocation minus deallocation |
| `memory_grant_mb`, `dop` | |
| `wait_type`, `wait_ms`, `wait_resource` | Colour-coded by wait family |
| `open_tran`, `isolation_level` | |
| `percent_complete` | Populated only for the operations SQL Server reports it for, such as `BACKUP` and `DBCC`. Blank elsewhere; the live plan is the answer for ordinary queries |
| `query_hash` | Hidden by default, the join key for the repetitive queries view |
| `sql_text` | Current statement, extracted by offsets, not the whole batch |

Filtering is per column, and combinable. Filtering by database and by command
type is the pair that gets used most, hence their explicit mention.

### 8.2 Column selection and saved layouts

Columns can be shown, hidden, reordered and resized. That state is a named
layout and it persists.

A layout holds the column set, order and widths per view, the sort, the saved
filters, and the collapsed or expanded state of the dashboard. Layouts are named
and switchable. One layout is the default.

Persistence is a JSON file, not browser local storage. Reasons: it survives a
change of browser, it can be copied between machines, it can be committed to a
team repository, and a DBA who has built a good layout can hand it to a
colleague. The server owns the file and the UI reads and writes it through an
endpoint.

### 8.3 The configuration file

One file holds everything the user can tune: instance list, refresh tiers,
retention window, layouts.

Both a portable install and a per-user install must work, so the file is looked
up in this order and the first hit wins:

1. The path given by `--config`, if present. An explicit path that does not
   exist is an error, not a silent fallback.
2. `sqltop.json` beside the binary. Portable mode: a binary and its config on a
   USB stick or a jump box. It wins over the user directory, because someone who
   put a file next to the binary meant it.
3. `sqltop.json` in the user configuration directory, from `os.UserConfigDir`.
   `~/.config/sqltop/` on Linux, `%AppData%` on Windows, `Library/Application
   Support` on macOS.
4. No file at all: built-in defaults, nothing written until the user saves.

Writes go back to the file the configuration was loaded from. When there was
none, saving creates one beside the binary if that directory is writable, and in
the user directory otherwise. The status bar names the file in use, so there is
never a doubt about which one is being edited.

Shape:

```json
{
  "instances": [
    { "name": "PROD-SQL01", "dsn": "sqlserver://prod-sql01?authenticator=krb5" },
    { "name": "Azure sales", "dsn": "sqlserver://x.database.windows.net?database=sales" }
  ],
  "tiers": { "requests": "1s", "counters": "1s", "space": "5s", "cpuHistory": "60s" },
  "retention": "15m",
  "budget": { "collectionMs": 50 },
  "layouts": { "default": { } }
}
```

Every refresh tier of section 10 is configurable here, including the collection
budget past which the tool throttles itself. Connection secrets are not stored
in this file: a DSN may reference `${SQLTOP_CONN}` and the value comes from the
environment, loaded from `.env` at startup.

## 9. Query detail and live plan progress

Selecting a row opens the detail panel. Selection must survive the refresh; that
constraint drove the renderer decision and is measured in the bench.

The panel shows the full SQL text, the session context, the sample history for
that request, and the execution plan.

Live progress works like this. `sys.dm_exec_query_statistics_xml(session_id)`
returns the showplan of an in-flight request carrying the actual row counts
reached so far. Lightweight profiling v3 feeds it and is enabled by default from
SQL Server 2019 and on Azure SQL Database, so nothing has to be turned on. The
tool polls it for the selected session only, every two to three seconds, and
re-renders. Actual against estimated rows per operator gives a genuine sense of
where the query is and whether the estimates were wrong.

Three limits to display honestly rather than hide:

- The function returns nothing for a plan reaching 128 levels of nested XML.
- It returns nothing once the request has finished. Falling back to
  `sys.dm_exec_query_plan` gives the plan without runtime figures.
- On SQL Server 2016 SP1 to 2017 the profiling infrastructure is not on by
  default. The tool reports that rather than enabling anything server-wide.

Plan rendering uses an existing MIT-licensed showplan viewer rather than a
hand-written one.

The panel never runs in the polling loop. Plan retrieval happens only for a
selected session, and stops when the selection is cleared.

## 10. Performance requirements

The user requirement was "it has to be very fast". Made concrete:

Rendering budget. Under 16 ms per refresh at 800 rows, so a tick fits inside one
frame. Measured at 4.8 ms in the bench, so there is headroom.

Collection budget. Under 50 ms of server time per second, all tiers combined,
measured client-side as the round-trip of the collection queries. Past that, the
tool slows its own cadence and says so in the status bar. It never silently
keeps hammering.

Refresh tiers. Not everything deserves one hertz. Every period below is a
default and is configurable in the JSON file, section 8.3.

| Tier | Period | Contents |
|---|---|---|
| A | 1 s | Requests, sessions, waiting tasks, active request count |
| B | 1 s | The filtered performance counter query, `sys.dm_os_sys_info` |
| C | 5 s | tempdb file space, version store, memory clerks, scheduler detail |
| D | 1 min | Ring buffer CPU history, which the engine only produces once a minute |
| On demand | - | SQL text of a selected request, execution plan, live plan progress |

Never in the loop. `sys.dm_exec_query_plan`, `sys.dm_exec_text_query_plan` and
`sys.dm_exec_query_statistics_xml` are on-demand only.

Never enabled by the tool. Trace flags, database scoped configurations, and any
server-wide profiling setting. The tool reads; it does not reconfigure.

## 11. Explicitly not in scope

No headless or unattended capture mode, ever. Servers already have Extended
Events and tracing for recording without a human present, and they do it better.
This tool exists to be watched while it runs. The short retention window is
there so that a query which finished thirty seconds ago can still be inspected,
without the tool ever becoming a recorder.

No alerting, no thresholds, no notifications.

No configuration of the monitored server. The tool reads.

## 12. Later versions

Local persistence. SQLite in pure Go (`modernc.org/sqlite`) for replaying a
capture after the fact, with the samples, the session and request tables, a
deduplicated SQL text table keyed by hash, and compressed plans stored once per
plan handle. Not needed for the MVP, where the in-memory window is enough.

Extended Events capture, opt-in, for the short history of completed queries that
the DMVs cannot give.

PostgreSQL and MySQL sources.

## 13. Settled

The questions this draft opened have been answered and folded in above: target
2019 and later with graceful degradation below, Azure SQL Database in the MVP,
Windows authentication from Linux, one instance displayed at a time behind a
switcher, configurable refresh tiers, a JSON configuration file resolved from
either the binary directory or the user directory, and no headless mode.

What remains open is empirical rather than a matter of choice: exactly which
dashboard figures survive on Azure SQL Database has to be confirmed against a
live instance. The capability mechanism of section 4.1 is what absorbs the
answer without disturbing the rest.
