# sqltop specification

A `top` for SQL servers. Real-time view of what an instance is doing right now,
with enough short history to review a query after it has finished.

Status: scope settled. Supersedes the initial French sketch (see git history). The
rendering strategy is already settled by measurement, recorded in section 10.1.

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

### 2.1 Implementation principles

KISS, idiomatic Go, and an explicit watch on technical debt. Stated as rules
that can actually be checked, because a principle nobody can fail is decoration.

Standard library first. A dependency needs a stated reason, in the commit that
introduces it. The whole budget for the MVP is the SQL Server driver and its
krb5 provider; `modernc.org/sqlite` joins it only when local persistence
arrives. Tabulator was measured and rejected rather than kept out of taste, and
Go dependencies face the same bar.

No abstraction before the second implementation. The `Source` interface earns
its place because Azure SQL Database is a real second implementation inside the
MVP, not because PostgreSQL might happen one day. Nothing else gets an interface
until there are two concrete users of it.

Boring concurrency. One goroutine per collection tier, channels to hand results
off, one mutex around the retention window. No worker pools, no custom
scheduler, no clever lock-free structure. The tool spends its time waiting on
the network, not on CPU.

Errors carry context and are never swallowed. A source that fails degrades what
the interface shows and says so in the status bar; it does not panic, and it
does not silently display stale numbers as if they were fresh.

Options must earn themselves. Every entry in the configuration file exists
because someone would realistically change it. A knob added "in case" is debt
with a nice name.

Debt is written down, not absorbed. A shortcut is recorded where it lives, with
the reason and what it would take to undo. An undocumented shortcut is the only kind that is unacceptable.

Measure before optimising. There is precedent: the renderer decision cost two
days of benchmarking and overturned two of my own predictions. Guessing at
performance in this codebase has a poor track record.

`gofmt` clean and `go vet` clean before any commit.

Test the pure parts and do not mock a database. What deserves tests here is
arithmetic and shaping: counter delta and ratio computation, blocking-chain
flattening, retention-window eviction, capability gating, configuration
resolution order. Testing that a SQL string equals a SQL string proves nothing.

The specific debt risk of this project, worth naming because it is not obvious:
the hand-rolled renderer growing into a half-finished grid library. It was
chosen for being ten times cheaper than Tabulator on the refresh loop, not for
being a framework. Sorting, column resizing and filters are wanted and are
specified. Virtual columns, plugin systems, a theming engine and a generic
formatter API are not; each would trade away the reason the thing was chosen.
If it ever needs those, the honest move is to revisit the measurement, not to
grow a library by accident. Operative rule: any grid feature beyond sorting,
filtering and column resizing must be justified by a measurement showing it
keeps the refresh budget of section 10.

## 3. Target and requirements

| Item | MVP | Later |
|---|---|---|
| Engine | SQL Server 2019 and later, on-premises; Azure SQL Database; Azure SQL Managed Instance | PostgreSQL, MySQL |
| Degraded | SQL Server 2016 SP1 to 2017: everything works except live plan progress, which needs trace flag 7412 the tool will not set | |
| Floor | SQL Server 2012 to 2016 RTM: connects, grid and dashboard work, no plan progress at all. Below 2012, the tool refuses to connect and says why rather than failing query by query | |
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

These are minimums, not exact matches. `VIEW SERVER STATE` includes
`VIEW SERVER PERFORMANCE STATE`, so a login that has the former still works on
2022 and later. The preflight checks what the login can actually read and
reports the gap; it does not infer rights from the version alone.

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
(section 10.1).

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

Granularity is per figure, not per group. Azure SQL Database will support some
dashboard tiles and not others within the same family, so one unavailable tile
must be able to disappear without taking its neighbours with it.

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

Frozen windows are evicted, or memory grows linearly with how often someone
switches. At most three are kept, least recently used first out, and any frozen
window older than its own retention period is dropped since every sample in it
would have expired anyway.

### 4.3 The local HTTP server

It binds `127.0.0.1` only. Never `0.0.0.0`, and there is no flag to make it do
so. The reason is not tidiness: this interface can kill sessions on a production
server, and a bind on all interfaces would hand that to anyone on the network.

The port defaults to 8420 and is configurable. On startup the tool prints, and
opens, a URL carrying a token generated for that run; requests without it are
refused. That keeps other local users of a shared machine out. No CORS headers
are emitted, since nothing legitimate is cross-origin here.

### 4.4 Losing the connection

A dropped connection is normal, not exceptional: failovers, restarts, laptops
that sleep. The collector retries with exponential backoff, from one second to a
thirty second ceiling, indefinitely, and the status bar shows the state and the
next attempt. The retention window is kept and marked stale, exactly as on an
instance switch, so the last minutes before the drop remain readable. On
reconnection the preflight runs again, because the server may have come back as
a different version or with different rights after a failover.

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
| tabs  requests | blocking | waits | repetitive | throughput | programs |
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
| SQL Server CPU %, system CPU % | `sys.dm_os_ring_buffers`, `RING_BUFFER_SCHEDULER_MONITOR` | One sample per minute, 256 kept. Both figures are what the engine holds, not settings. Not available on Azure SQL DB |
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

Not every row `sys.dm_exec_requests` can produce belongs in it. Measured
under load, the large majority of what that view returns at any instant is
the engine's own scheduler bookkeeping: worker threads named TASK MANAGER,
LOG WRITER, RESOURCE MONITOR, BRKR TASK and the like, present continuously
and never the reason a DBA opened this tool. Finding out what each row is
doing in tempdb, one of the columns below, costs more server CPU than the
rest of the query combined, because it means a separate lookup against
`sys.dm_db_task_space_usage` for every row fetched; paying that cost for a
row nobody reads is exactly the kind of self-inflicted load section 2
rules out. The grid therefore keeps a row only when `sys.dm_exec_sessions`
marks it as an actual login rather than one of the engine's own worker
threads, or when it is blocking another session or is itself blocked, or
when it is running a parallel plan. The first of those covers essentially
all real work whatever its status or wait, because it asks the server
directly what kind of session this is rather than reconstructing the
answer from status text; the other two exist so that a worker thread doing
something worth seeing, chiefly one on either side of a block, is never
dropped for want of being a login. There is no setting to see the excluded
rows: the filter exists to stop the tool paying for what nobody reads, not
to be turned off.

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

Sorting and filtering happen in the browser, on data already in the retention
window. Section 10.1 records the measurement behind that: nine modes, every
client-side candidate against a server-side twin, and the pairs do not
separate. Doing the work in Go would also cost the three properties that
matter here, namely a filter per viewer rather than per server, a filter that
applies to the history rather than deciding what was ever collected, and rows
that leave the grid because they ended rather than because they stopped
matching, which the wire protocol cannot otherwise tell apart.

#### Operators

Five, and no more until something concrete needs a sixth.

| Operator | Applies to | Note |
|---|---|---|
| `contains` | text | The default for a typed filter |
| `=` | text and numbers | |
| `>` , `<` | numbers | What makes "everything above a second of CPU" expressible |
| `in` | text | A list, for picking several databases or commands at once |

Text comparison is case-insensitive. A DBA hunting a runaway query is not
also spelling a database name in the right case.

Filters on different columns combine with AND. Several values on one column
through `in` combine with OR, which is what `in` means. There is no interface
for expressing anything else, deliberately: a filter language is a project of
its own and this is a grid.

#### Sorting and filtering against the blocking tree

The blocking view of section 7 orders a blocker immediately above those it
blocks, which is an ordering a column sort would destroy and a filter would
tear holes in. Both are defined rather than forbidden.

Sorting reorders the chain heads and keeps each chain welded underneath its
own. The worst chain comes to the top and the structure survives, which is
the only version of sorting that is worth anything in this view.

Filtering pulls a matching row's blockers in with it, shown greyed and marked
as context even though they do not match. A blocking chain means nothing
without its head: the blocker is what the operator is looking for, and it is
routinely in a database they did not filter for.

#### The scroll position when a filter shrinks the list

Changing a filter re-anchors the view on the selected row, if it survives the
filter, and goes to the top otherwise. Keeping the selection in view is the
central gesture of the tool.

This is a real problem and not a hypothetical one. Filtering 800 rows down to
110 while scrolled toward the bottom cost five scroll positions out of 122
ticks on the bench, against none in the eight other modes, because the
content became shorter than the offset and the browser clamped it, again on
every tick as the row count moved.

### 8.2 Column selection and saved layouts

Columns can be shown, hidden, reordered and resized. That state is a named
layout and it persists.

A layout holds the column set, order and widths per view, the sort, the saved
filters, the dashboard's tile selection, and which of its groups are folded.
Layouts are named and switchable. One layout is the default.

The dashboard is configurable the same way the grid is: a layout may name the
groups it wants and, inside a group, the figures it wants, and a group or
figure the file does not mention keeps the built-in default. Hiding a tile is
a display decision and never a collection decision. The collector goes on
sampling every figure, for three reasons: a figure that stopped being
collected has no history, so re-enabling its tile would show an empty
sparkline rather than the slope that made you look; two browsers on one
collector would need either a union or a query each; and "you hid it" would
need a third visual state next to "the server cannot answer it", when the
whole point of the Available flag is that there are two.

Suppressing collection is a separate setting with a separate name. See the
collection scope in section 8.3, which turns a whole tier off for everybody
and is worth something precisely because it removes a query rather than a
tile.

Persistence is the configuration file, not browser local storage. Reasons: it
survives a change of browser, it can be copied between machines, it can be
committed to a team repository, and a DBA who has built a good layout can hand
it to a colleague. The server owns the file and the UI reads and writes it
through an endpoint.

### 8.3 The configuration file

One file, `sqltop.yaml`, holds everything the user can tune: instance list,
refresh tiers, retention window, collection scope, layouts.

YAML rather than JSON, which is a dependency in a project whose rule is
standard library first, so the reason is written down here as well as in the
commit that introduces it. This file is meant to be opened and edited by hand
and handed between colleagues, and JSON is a poor format for that: quoted
keys, no trailing commas, and a syntax error message that points at a
character rather than at a field. The parser accepts JSON as well, since JSON
is a subset of YAML, so an existing `sqltop.json` works unchanged once
renamed.

The tool rewrites this file when the interface saves a layout. Comments do
not survive that rewrite, so the file does not carry any and nothing in it
depends on carrying any.

Both a portable install and a per-user install must work, so the file is looked
up in this order and the first hit wins:

1. The path given by `--config`, if present. An explicit path that does not
   exist is an error, not a silent fallback.
2. `sqltop.yaml` beside the binary. Portable mode: a binary and its config on a
   USB stick or a jump box. It wins over the user directory, because someone who
   put a file next to the binary meant it.
3. `sqltop.yaml` in the user configuration directory, from `os.UserConfigDir`.
   `~/.config/sqltop/` on Linux, `%AppData%` on Windows, `Library/Application
   Support` on macOS.
4. No file at all: built-in defaults, nothing written until the user saves.

Writes go back to the file the configuration was loaded from. When there was
none, saving creates one beside the binary if that directory is writable, and in
the user directory otherwise. The status bar names the file in use, so there is
never a doubt about which one is being edited.

Shape:

```yaml
instances:
  - name: PROD-SQL01
    dsn: sqlserver://prod-sql01?authenticator=krb5
  - name: Azure sales
    dsn: sqlserver://x.database.windows.net?database=sales

tiers:
  requests: 1s
  counters: 1s
  space: 5s
  cpuHistory: 60s
  livePlan: 2s

retention: 15m
server:
  port: 8420
budget:
  serverCpuMsPerSecond: 50

collect:
  skipTiers: []

layouts:
  default:
    dashboard:
      groups:
        - id: cpu
          folded: false
          figures: [sql_cpu_percent, runnable_tasks, scheduler_load_factor]
        - id: memory
          folded: true
    views:
      requests:
        columns:
          - field: spid
            width: 60
          - field: database
            width: 100
          - field: command
            width: 100
          - field: cpu_ms
            width: 90
        sort:
          - field: cpu_ms
            dir: desc
        filters:
          - field: database
            op: in
            value: [CRM]
      blocking:
        columns:
          - field: spid
            width: 60
          - field: blocking_depth
            width: 50
          - field: sql_text
            width: 520
```

`collect.skipTiers` is the collection scope of section 8.2. A tier named there
is never sampled, for every viewer, which removes its query rather than its
tiles. Absent or empty means collect everything.

A layout is exactly that shape: per view, an ordered column list with widths,
a sort, and a filter list, plus the dashboard's groups and figures. Column
order is the list order; a column absent from the list is hidden. Anything the
file does not mention falls back to the built-in default, so a hand-written
partial layout is valid, and the `blocking` view above inherits its sort and
filters while naming only three columns.

Every view of section 7 is configured independently under `views`, because a
blocking chain and a repetitive-query aggregation do not want the same columns
in the same order.

Every refresh tier of section 10 is configurable here, including the live plan
refresh period, and the collection budget past which the tool throttles itself. Connection secrets are not stored
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
tool polls it for the selected session only, on the `livePlan` period of the
configuration file, two seconds by default, and re-renders. Actual against estimated rows per operator gives a genuine sense of
where the query is and whether the estimates were wrong.

Three limits to display honestly rather than hide:

- The function returns nothing for a plan reaching 128 levels of nested XML.
- It returns nothing once the request has finished. Falling back to
  `sys.dm_exec_query_plan` gives the plan without runtime figures.
- On SQL Server 2016 SP1 to 2017 the profiling infrastructure is not on by
  default. The tool reports that rather than enabling anything server-wide.

Plan rendering uses `html-query-plan` (Justin Pealing, MIT), vendored into the
embedded assets like Tabulator was for the bench. It is a declared dependency
under the rule of section 2.1, and the justification is the same one that
decided the renderer: rendering showplan XML as an SSMS-like graph is the single
largest piece of work in this tool, it is solved, and writing it again would buy
nothing. It is the only front-end dependency; the grid stays hand-rolled.

The three limits above are shown inside the plan panel itself, as a plain line
of text where the plan would be, naming which limit was hit. Not an icon, not a
banner elsewhere: the explanation belongs where the missing thing was expected.

### 9.1 Killing a session

This is the only destructive action in the tool, it is irreversible, and it can
roll back hours of work on a production server. It is therefore gated three
ways.

The `KillSession` capability must be present, and the login must actually hold
the right; the preflight settles that once rather than discovering it on the
click.

Confirmation restates what is about to die: session id, login, host, program,
database, elapsed time, and the first line of the statement. A confirmation that
says only "are you sure?" is decoration, because the risk is killing the wrong
row after the grid refreshed under the cursor.

Every kill, attempted or successful, is appended to a local log file with the
timestamp, the instance, and that same identity block. A DBA who kills the wrong
session needs to be able to say exactly what they did.

The panel never runs in the polling loop. Plan retrieval happens only for a
selected session, and stops when the selection is cleared.

## 10. Performance requirements

The user requirement was "it has to be very fast". Made concrete:

Rendering budget. Under 16 ms per refresh at 800 rows, so a tick fits inside one
frame. Measured at 4.8 ms in the bench, so there is headroom.

Collection budget. Under 50 ms of server CPU time per second, all tiers
combined.

Measured on the server, not by stopwatch. An earlier draft of this spec said
"measured client-side as the round-trip of the collection queries", which mixed
two different quantities: a round trip includes network latency, so monitoring a
distant server across a WAN would throttle the tool while the server was
perfectly fine, and a saturated local server would slip past unnoticed.

The tool reads its own cost instead. `sys.dm_exec_sessions` carries `cpu_time`
and `logical_reads` for every session, including the tool's own, found through
`@@SPID`. Differentiating those between two ticks gives exactly what the tool
has cost the instance, in server CPU milliseconds, with no network in the
figure. That number is displayed: an instrument that claims to bound its own
cost should show it.

Throttling is ordered, not proportional. When the budget is exceeded over a
sliding ten second window, tiers degrade from the least valuable upward: first
tier C doubles its period, then tier B, and tier A last, since the request grid
is the tool. On-demand work is never throttled, because it only happens when a
human asked for it. Periods recover one step at a time once consumption has been
under budget for thirty seconds. Every change is announced in the status bar,
naming which tier slowed and why. The tool does not silently keep hammering, and
it does not silently go quiet either.

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

### 10.1 The measurements behind these numbers

The rendering budget and the wire protocol are not estimates. Four strategies
were built against a synthetic load and measured before any of this was
specified. The harness that produced them is a local one, deliberately not
tracked in this repository, so its results live here.

Chrome 151, Linux x86_64, 800 rows, 1 Hz, 5 % churn:

| Renderer | Ticks | apply p50 | apply p95 | frame p95 | Time frozen | Scroll lost | Selection lost |
|---|---|---|---|---|---|---|---|
| Hand-rolled, virtualised | 120 | 4.8 ms | 5.8 ms | 15.5 ms | 0 s (0 %) | 0 | 0 |
| Tabulator `setData` | 120 | 46.8 ms | 51.3 ms | 54.8 ms | 6.0 s of 120 s (5 %) | 120 of 120 | 0 |

Firefox 153, Linux x86_64, 1 Hz, 5 % churn:

| Renderer | Rows | Ticks | apply p50 | apply p95 | frame p95 | Time frozen | Selection lost |
|---|---|---|---|---|---|---|---|
| Hand-rolled, virtualised | 760 | 124 | 12 ms | 17 ms | 36 ms | 5.0 s of 124 s (4 %) | 0 |
| Tabulator `replaceData` | 760 | 122 | 163 ms | 178 ms | 182 ms | 20.3 s of 122 s (17 %) | 4 |
| Tabulator `replaceData` | 880 | 121 | 163 ms | 181 ms | 186 ms | 20.4 s of 121 s (17 %) | 8 |
| Tabulator `setData` | 300 | 11 | 78 ms | 115 ms | 116 ms | 0.9 s of 11 s (8 %) | not tested |
| Tabulator `updateData` over a delta feed | 300 | - | interface became unresponsive | | | | |

Chrome is two to three times faster than Firefox on both renderers, but the
ratio between them does not move: a factor of ten.

Three results decided the design.

Scroll position lost on 120 ticks out of 120 is the sharpest of them. It is the
documented behaviour of `setData`, which returns the list to the top on every
call: once a second, the rows jump back to the beginning. That eliminates the
mode whatever its speed, which is why section 10 states a budget in frozen time
and lost state rather than in milliseconds alone.

Time frozen, 17 % against nothing, is why the hand-rolled renderer won. A grid
that blocks a sixth of the wall clock is not a real-time monitor.

The delta feed became unresponsive and is why the protocol sends snapshots. It
also saves nothing on the wire: measured at 300 rows, a snapshot weighed 167 kB
and a delta 168 kB, because on active requests every counter moves every second
and so every row ends up in the delta anyway.

The reference table of section 4 comes from the same measurement. Out of 565
bytes per row:

| Field | Share | Changes between ticks |
|---|---|---|
| SQL text | 24 % | Never, for a given request |
| CPU history | 16 % | One point out of twenty-four |
| Program name | 7 % | Never |

That is 47 % of the payload repeated every second for nothing, which is what
the per-session reference table removes.

The second half of the payload work came later, once the grid was running
against a real server rather than a generator. With the reference table in
place, a snapshot at 800 rows still weighed 239 kB and left every second, and
a row cost 298 bytes. Ninety-eight of those bytes were the eighteen JSON key
names, their quotes and their colons: a third of everything sent, spent
restating the same eighteen words eight hundred times a second.

| Row encoding | Bytes per row | Snapshot at 800 rows | Per second |
|---|---|---|---|
| Object, one key per column | 298 | 239 kB | 239 kB |
| Positional array | 191 | 153 kB | 153 kB |

A positional format is a bad idea when nobody sends the header: a column
added on one side shifts every column after it on the other, nothing fails,
and reads appear under writes. So the column order travels once per
connection, on the same terms as the reference table, and the client indexes
by name through it. The order is checked against the row struct's own field
tags by reflection, so the two cannot drift.

For comparison, and because it was asked: minifying the page itself, which
carries the whole interface inline, takes it from 32.0 kB to 18.8 kB. Over
loopback that is 0.9 ms of transfer and 0.3 ms of JavaScript compilation
becoming roughly half of each, once, at page load. The stream spends the
saving in a sixteenth of one tick. The page is left readable.

### Where sorting and filtering belong

The caveat that used to stand here said the 16 ms budget had been measured on
a grid that only displayed, and had to be re-measured before sorting and
filtering were built rather than assumed to hold. It has been.

The worry was specific, and it was not about whether JavaScript can sort 800
rows. The recycled row pool is keyed on session and request id. A sort on a
column that moves every tick gives almost every pooled row a new identity
every tick, which throws away the "rewrite only the cells that changed"
optimisation the 4.8 ms figure depends on. If that were where the cost sat,
moving the sort into the server would change nothing, because the browser
would still have to repaint reordered rows.

So every client-side candidate was measured against a server-side twin
delivering the identical row order, already sorted and filtered in Go, with
the page doing nothing. The gap inside a pair is what the JavaScript costs;
what both members share is the cost of the reordering itself. Two sort keys:
session id, which barely reorders between ticks, and CPU, which moves by a
different amount on every row every tick and genuinely scrambles the order.

Chrome 151, Linux x86_64, 800 rows, 1 Hz, 5 % churn, 1600 by 1000 viewport,
122 ticks per mode, against the shipped renderer and, since sorting and
filtering now exist, against the shipped sort and filter rather than a
simulation of them. `apply` therefore covers filtering and sorting as well as
layout.

| Mode | apply p50 | apply p95 | frame p95 | Time frozen | Scroll lost | Selection lost |
|---|---|---|---|---|---|---|
| No sort, no filter | 3.6 ms | 4.8 ms | 16.7 ms | 0 % | 0 | 0 |
| Client sort, stable key | 3.2 ms | 6.0 ms | 16.7 ms | 0 % | 0 | 0 |
| Server sort, stable key | 2.9 ms | 5.4 ms | 16.7 ms | 0 % | 0 | 0 |
| Client sort, volatile key | 3.0 ms | 5.0 ms | 16.7 ms | 0 % | 0 | 0 |
| Server sort, volatile key | 3.2 ms | 5.7 ms | 16.8 ms | 0 % | 0 | 0 |
| Client filter | 2.6 ms | 5.0 ms | 16.7 ms | 0 % | 3 | 0 |
| Server filter | 3.2 ms | 5.1 ms | 16.7 ms | 0 % | 1 | 0 |
| Client filter and volatile sort | 2.7 ms | 5.5 ms | 16.7 ms | 0 % | 1 | 0 |
| Server filter and volatile sort | 2.8 ms | 4.8 ms | 16.7 ms | 0 % | 0 | 0 |

Three things settle it.

The pairs do not separate. Every mode sits between 2.6 and 3.6 ms against a
16 ms budget, and the spread inside a client/server pair is smaller than the
spread between repeats of the same mode. The direction is the argument, not
the size: if doing the work in Go helped, the server twin would be
consistently faster, and it is faster on one of the four pairs and slower on
the other three, which is what noise looks like. Every client mode is at or
below the no-sort-no-filter baseline, which is the clearest statement of the
result available: the work costs less than the measurement can see.

The frame time does not move at all. 16.7 ms on every mode is one frame at
60 Hz: the page is hitting the display's own cadence and dropping nothing,
with no frozen time and no lost selection anywhere.

The feared cost did not materialise, and the reason is worth writing down so
nobody re-derives the fear. The renderer only paints the visible window,
about 33 rows, and under a volatile sort the changed-cells optimisation was
already barely helping: CPU, reads and elapsed move on every row every tick
regardless. Reordering raises the changed cells per painted row from roughly
eight to eighteen, which is a few hundred writes a second, not a few
thousand.

So sorting and filtering are client-side work on data already in the browser.
That is also the answer that keeps the properties worth keeping: a filter per
viewer rather than per server, a filter that applies to the retention window
rather than deciding what was ever collected, and rows that leave the grid
because they ended rather than because they stopped matching, which the
protocol cannot otherwise tell apart.

The scroll losses are down but not gone, and the residue says what it is.
Before the re-anchoring rule above existed, filtering cost five lost
positions out of 122 ticks and every other mode lost none. With it, the
filtering modes lose one to three, and the server filtering mode, where the
page does no filtering at all, loses one as well. That residue is therefore
not the filter: it is a list of 110 rows getting shorter and longer with
ordinary session churn while the viewport sits near its end, which is what
any shrinking list does in a browser. The re-anchoring rule covers the case
it was written for, a filter changing, and this one is left alone
deliberately rather than fixed by pinning a scroll position that the user
did not ask to have pinned.

Three sets of figures now exist for this table and the conclusion has not
moved through any of them. The first was taken before the number formatter
was fixed and ran 5.2 to 6.4 ms. The second, after that fix, ran 2.7 to 4.3.
This one measures the shipped sort and filter rather than a simulation and
runs 2.6 to 3.6. Each time the whole table moved together, which is what a
change in something every mode uses should do, and each time the client and
server twins stayed inside each other's noise.

## 11. Versioning

The tool reports its own version, and starts at 0.1.

Scheme. Zero-major while the shape can still change: 0.1 is the collector and a
working request grid, 0.2 adds the dashboard, the views and the plan panel, and
1.0 is the first version usable by someone who did not write it. Inside that,
the middle number moves when a milestone lands and the last one when a fix
ships. Nothing here promises API stability, because there is no API.

Where the number lives. A single constant in `internal/buildinfo`, no build
flags and no code generation. The commit and the dirty flag come from
`runtime/debug.ReadBuildInfo`, which the toolchain fills in on its own from Go
1.18 onward, so a plain `go build` produces a binary that can say exactly which
tree it came from. Nothing has to be passed at build time for that to work,
which is the point: a version that depends on the build command is a version
that is wrong the first time someone builds it differently.

Where the number shows. `--version` prints it and exits. The tool logs it as
its first line at startup, before anything can fail, because the first question
about any report is which build produced it and a run that dies on a bad
configuration file is exactly the report that arrives without one. The
interface header carries the same string, so a screenshot and a log agree.

Changing it. `scripts/bump-version.sh <version>` rewrites the constant and
nothing else. It refuses anything that is not major.minor.patch, and it neither
commits nor tags: whether a milestone actually works is a judgement, not a
script's to make.

Where it shows. `sqltop --version` prints it and exits. The status endpoint
carries it, and the interface header shows it beside the instance name, because
the first question about a bug report is which build produced it.

Releases are tagged `v0.1.0` and so on, matching the constant. The tag is cut
when the milestone works, not when the constant changes.

## 12. Explicitly not in scope

No headless or unattended capture mode, ever. Servers already have Extended
Events and tracing for recording without a human present, and they do it better.
This tool exists to be watched while it runs. The short retention window is
there so that a query which finished thirty seconds ago can still be inspected,
without the tool ever becoming a recorder.

No alerting, no thresholds, no notifications.

No configuration of the monitored server. The tool reads.

## 13. Later versions

Local persistence. SQLite in pure Go (`modernc.org/sqlite`) for replaying a
capture after the fact, with the samples, the session and request tables, a
deduplicated SQL text table keyed by hash, and compressed plans stored once per
plan handle. Not needed for the MVP, where the in-memory window is enough.

Extended Events capture, opt-in, for the short history of completed queries that
the DMVs cannot give.

PostgreSQL and MySQL sources.

## 14. Settled

The questions this draft opened have been answered and folded in above: target
2019 and later with graceful degradation below, Azure SQL Database in the MVP,
Windows authentication from Linux, one instance displayed at a time behind a
switcher, configurable refresh tiers, a JSON configuration file resolved from
either the binary directory or the user directory, and no headless mode.

What remains open is empirical rather than a matter of choice: exactly which
dashboard figures survive on Azure SQL Database has to be confirmed against a
live instance. The capability mechanism of section 4.1 is what absorbs the
answer without disturbing the rest.

### 14.1 How the empirical questions get answered

Local SQL Server instances run in Podman, so most of it needs no cloud account
and no shared server. Development images are already present on the workstation
for 2022 and 2025; 2019 is the minimum target and should be pulled, and 2016 or
2017 to exercise the degraded path.

Covered locally: every dashboard source and counter type, the permission
preflight against a deliberately under-privileged login, live plan progress and
its 128-level limit, blocking chains, and the observation budget measured
through the tool's own `sys.dm_exec_sessions` figures.

Not covered locally, and needing a real instance: Azure SQL Database, which
cannot be containerised, and Kerberos authentication against a real domain.
Those two stay open until someone points the tool at the real thing.

One measurement gap is already known. The rendering bench measured a passive
grid. The 16 ms budget has not been verified with sorting and filtering active,
which change the per-refresh work. The bench exists and should be re-run once
the grid has those functions rather than assuming the margin holds.
