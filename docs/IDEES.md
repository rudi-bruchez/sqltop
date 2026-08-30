# Ideas

Candidate features, argued rather than listed. Nothing here is committed to;
the point of writing them down is that the reasons survive, so a rejected
idea does not come back and get paid for twice.

Each one carries what it would cost the monitored server, because that is
this project's currency. The numbers come from `docs/PERFORMANCE.md`: the
tool sits at roughly 3 ms of server CPU per second against a budget of 50,
so there is room, and the question for every idea below is whether it earns
its share of it.

Ordered by what they are worth, not by what they cost.

## 1. The retention window is collected and never shown

This is the largest gap in the tool and it needs no new query at all.

Spec section 12 justifies the whole design around one sentence: the short
retention window exists "so that a query which finished thirty seconds ago
can still be inspected". The collector fills that window, fifteen minutes of
it by default, bounded by age and by sample count, under a mutex, with tests.
`Window.History(ref)` returns one request's whole series.

Nothing calls it. The stream sends `Latest()` and only `Latest()`. The
interface has no way to look at any instant but this one. `p` freezes the
display, which is not the same thing: it stops the future, it does not open
the past.

What that would look like, in rising order of work:

A scrubber over the window. A slider or two keys stepping one tick back and
forward, redrawing the grid from a retained tick rather than the newest one,
with the status bar saying how far back you are. The renderer already draws
whatever array it is handed; the window already holds them. The work is a
protocol change (the client would need the ticks, not just the newest) and a
decision about how many to send.

A per-request history panel. With a row selected, what that request looked
like over the window: its CPU climbing, its wait type changing, the moment it
became blocked. `History(ref)` returns exactly this today. It is the answer
to "why is this slow" that a single instant cannot give, and it costs one
function call and a panel.

A wait breakdown for the selected request, from the samples already retained:
this statement spent eighty per cent of the last minute on `LCK_M_X`. That is
a real diagnosis, computed from data already in memory, with no query and no
new DMV.

The last of those three is the cheapest and the most useful, and it is where
I would start.

## 2. Storage latency

The dashboard has no IO latency figure, and that is a hole. `PAGEIOLATCH_SH`
climbing is the commonest wait on a struggling server and it means nothing on
its own: it is a symptom of either slow storage or a bad plan reading too
much, and the two are distinguished by one number the tool does not read.

`sys.dm_io_virtual_file_stats(NULL, NULL)` gives read and write latency per
database file, as cumulative totals that have to be differentiated between
two samples exactly like the performance counters already are. One row per
file, so tens of rows on an ordinary instance. It belongs on the five second
space tier and would cost about what `logSpaceQuery` costs, one to two
milliseconds.

What to show: read and write latency in milliseconds per file, worst first,
and the total. Not per operation counts; the latency is the number that
decides anything.

This is the single cheapest addition with a real diagnostic payoff.

## 3. Deadlocks, from a session the server is already running

A deadlock is the one failure a live monitor structurally cannot see: it is
over before anybody looks. sqltop will never see one.

The server has already recorded it. `system_health` is an Extended Events
session that runs by default on every SQL Server since 2008 and captures
every deadlock graph, and it can be read with
`sys.fn_xe_file_target_read_file` or from its ring buffer target. Reading it
creates nothing, configures nothing and starts nothing.

Spec section 12 says no unattended capture, and this does not breach it: the
tool would not be recording, it would be reading what the server recorded
whether sqltop was there or not. The same argument covers Query Store below.
The line the spec draws is that sqltop must not become a recorder, not that
it must ignore recordings.

The cost is real and needs measuring before committing: reading the file
target parses XML on the server, and the session's ring buffer holds a few
megabytes. On demand only, on a tab, never on a tier. Worth a measurement
before a decision.

What it buys: the last deadlock graphs with their victims, their statements
and their resources, on a tool that installs nothing. That is a thing people
pay for.

## 4. Query Store, for "what changed"

Every question in the grid is about now. The question a DBA actually arrives
with is "it was fine yesterday". Nothing in this tool answers it, and one
read-only source can: `sys.query_store_runtime_stats` and its neighbours,
where Query Store is on, which is the default from SQL Server 2022 and on
Azure.

What it would show: the queries whose duration or CPU has regressed against
their own recent history, and the plan change that goes with it. That is the
highest-value question in SQL Server performance work, and the server has
already done the recording.

Two real costs. It is per database, so it means a query per database with
Query Store on, which is the context-switch problem the log view already
avoided once. And it is a different kind of screen from everything else here:
a leaderboard over hours, not a live grid. It may want to be its own view
with its own refresh, measured in minutes rather than seconds.

I would put this after 1 and 2 and probably after 3, not because it is worth
less but because it is the largest piece of work in this document and the
easiest to get wrong.

## 5. A server facts panel

The header says instance, host, edition, version, deployment and uptime. What
an expert actually asks in the first minute is a slightly different list, and
all of it is one query at connection: MAXDOP, cost threshold for parallelism,
max server memory against the machine's, the compatibility level and
cardinality estimator version of the databases in play, whether any database
has auto-close or auto-shrink on, whether instant file initialisation is
granted, and the log reuse state the log view already reads.

`sys.configurations`, `sys.databases` and `sys.dm_server_services` cover it.
Once at connection, so the cost is a fraction of a millisecond amortised over
a session.

There is a tension worth naming. Section 12 rules out alerting and
thresholds, and a panel that says "auto-shrink is on, and that is bad" is a
threshold wearing a different hat. The way through is to report and not to
judge: show `auto_shrink: ON` next to the database name and let the reader
know what that means. The tool states facts; it does not grade servers.

## 6. Searching the retained SQL text

The filters are per column and per view. A different question comes up
constantly: who is touching this table? Right now that means typing a table
name into the SQL text filter of the requests grid, which searches the
current tick only.

The retained window holds every statement seen in the last fifteen minutes,
in the browser, in the reference table. Searching all of it, and reporting
which sessions ran something matching and when, is a client-side loop over
data already present. No query, no protocol change, one more panel.

## 7. Copying a statement

A DBA reads a statement in sqltop and then wants it in a query window. Today
that means selecting text out of a `<pre>`, which works and is worse than a
button. The statement panel should offer one, and so should the plan panel
for the plan's file path.

Trivial, and used forty times a day.

## 8. Availability group health

On a server with availability groups, redo queue size, log send queue and
synchronisation state are first-class concerns, and `sys.dm_hadr_database_replica_states`
answers all three cheaply. Gated on whether the instance has any replicas,
which is one cheap check at connection, so it costs nothing on the servers
that have none.

Narrow audience, high value inside it, small work. A good candidate precisely
because it is cheap to skip when irrelevant.

## 9. Filter presets

Two or three named filters reachable by a key: only blocked, only over a
second of CPU, only this database. They are expressible in the existing
filter language, so this is a shortcut rather than a feature, and shortcuts
have to earn their key. Worth doing only once the key space is settled.

## What I would not add, and why

A plan tree renderer. Drawing showplan XML as an operator tree is weeks of
work, and SQL Server Management Studio and Plan Explorer both do it well and
are both already installed on the machine of anybody who would want it. `d`
writes a `.sqlplan` that opens in either of them, which is the right amount
of this problem for this tool to solve.

Index tuning advice. The missing index DMVs are famously misleading, index
usage statistics need weeks of uptime to mean anything, and a tool that shows
what is happening now is not the right place to recommend a schema change.

Alerting, thresholds, notifications. Section 12, and it is right: the moment
a monitoring tool can wake somebody up it needs a service, a state store, a
delivery mechanism and an on-call story, and it stops being a program you run
when you are worried.

Baselines over days or weeks. That needs the persistence of section 13 and,
more than that, it needs the tool to run when nobody is watching, which
section 12 rules out. Query Store is the answer to that question and the
server already keeps it.

A tree view of blocking chains. The flattened list with a depth column is
what the spec chose, and the reason holds: a tree that reorders under a sort
is worse than a list that does not.

Anything requiring an agent, a repository database, or a change on the
monitored server.

## Two smaller things noticed while writing this

The requests grid fetches `sys.dm_exec_sql_text` once per row. On a server
with eight hundred active requests that is eight hundred lookups into the
plan cache for text the client already holds and caches by fingerprint.
`docs/PERFORMANCE.md` records this as the most promising unmeasured
optimisation left; it needs a server big enough to measure on.

Azure SQL Database reports CPU through `sys.dm_db_resource_stats` and this
tool does not read it, so the two CPU tiles are honestly greyed there rather
than filled. That is correct behaviour and still a gap.
