# sqltop specification

A `top` for SQL servers. Real-time view of what an instance is doing right now,
with enough short history to review a query after it has finished.

Status: draft. Supersedes the initial French sketch (see git history). The
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

Source abstraction from day one. SQL Server is the MVP target; PostgreSQL and
MySQL follow. No SQL Server concept leaks into the core model.

English everywhere. Code, comments, UI, docs.

## 3. Target and requirements

| Item | MVP | Later |
|---|---|---|
| Engine | SQL Server 2016 SP1 and later, on-premises | Azure SQL DB, Managed Instance, PostgreSQL, MySQL |
| Live plan progress | SQL Server 2019 and later, where lightweight profiling v3 is on by default | 2016 SP1 to 2017, where it needs trace flag 7412 |
| Permission | `VIEW SERVER STATE` (`VIEW SERVER PERFORMANCE STATE` from 2022) | |
| Auth | SQL authentication, connection string from `SQLTOP_CONN` via `.env` | Windows / Kerberos |

On connection the tool runs a preflight check and reports, in plain language,
which features the current login cannot use, rather than failing per query.

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

Persistence is a JSON file in the user configuration directory, not browser
local storage. Reasons: it survives a change of browser, it can be copied
between machines, it can be committed to a team repository, and a DBA who has
built a good layout can hand it to a colleague. The server owns the file and the
UI reads and writes it through an endpoint.

A layout holds the column set, order and widths per view, the sort, the saved
filters, and the collapsed or expanded state of the dashboard. Layouts are named
and switchable. One layout is the default. Per-server overrides are allowed but
not required.

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

Refresh tiers. Not everything deserves one hertz.

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

## 11. Later versions

Local persistence. SQLite in pure Go (`modernc.org/sqlite`) for replaying a
capture after the fact, with the samples, the session and request tables, a
deduplicated SQL text table keyed by hash, and compressed plans stored once per
plan handle. Not needed for the MVP, where the in-memory window is enough.

Extended Events capture, opt-in, for the short history of completed queries that
the DMVs cannot give.

PostgreSQL and MySQL sources.

## 12. Open questions

These need a decision before the collector is written.

1. Minimum SQL Server version. 2016 SP1 widens the audience but makes live plan
   progress conditional. 2019 makes the flagship feature work everywhere with no
   caveat. Recommendation: target 2019 and later, degrade gracefully on 2016 to
   2017.
2. Is Azure SQL Database in the MVP? Several of the dashboard sources do not
   exist there. Recommendation: no, but keep the abstraction honest.
3. Windows authentication from Linux, in the MVP or later?
4. One instance at a time, or several side by side? The PowerShell prototype
   switches server at runtime; several at once is a different UI.
5. Is a headless mode needed, collector without UI, for unattended capture?
