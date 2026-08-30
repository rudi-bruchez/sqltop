# Decisions taken while building the collector

This records what was decided during the autonomous implementation of
`docs/plans/2026-08-29-collector.md`, why, and what each decision costs if it
turns out to be wrong. It exists because the work ran unattended: the owner of
this repository was not asked, so every judgement call has to be visible and
reversible rather than buried in a commit.

The full working record, including the per-task review findings and the fix
rounds, is in the run ledger at `.superpowers/sdd/2026-08-29-collector/progress.md`.
That directory is not tracked in git, so this file is the durable half.

## How decisions were made

Three rules settled almost everything.

`docs/SPECS.md` is the authority. `CLAUDE.md` says so, and where the plan
disagreed with the specification, the specification won. That happened often
enough to be worth stating plainly: the plan was written before any code
existed, and eight of the decisions below correct it.

Verify rather than argue. Where a question could be answered by pointing the
tool at a real SQL Server, it was, and the answer went into the decision rather
than a plausible-sounding argument. Several findings that read as serious
dissolved under a measurement, and several that read as pedantic turned out to
be reproducible failures.

A test that encodes the old design is not evidence. This came up three times,
and each time the temptation was to keep the assertion and bend the code. The
rule adopted is the reverse: when an assertion and the specification disagree,
the assertion changes and the change is written down.

## Decisions that changed the plan

### The retention window owns what it is given, and copies what it returns

The plan's window stored the caller's slice without copying and handed back the
live backing array of the newest tick. Both were inert at the time, since the
only writer passed a fresh slice and the only reader did not mutate. Fixed
anyway: this is a concurrent structure written by a collector and read from an
HTTP handler, and handing out an unprotected reference to its internals
undermines the mutex that makes it safe. A getter returns a copy and a setter
that takes ownership says so. Costs one slice allocation per tick per client,
a few microseconds against 800 rows.

### Blocking chains are keyed by request, not by session

The plan keyed row identity by session id alone, so two rows sharing a session
lost one silently. MARS gives one session several concurrent requests, which is
why the model carries a request id beside the session id in the first place. A
vanished request, in a grid whose entire job is to show what is running, is the
worst failure this tool can have. Identity is now the full reference; parenthood
stays keyed by session, because `blocking_session_id` names a session and not a
request. If this is wrong, the grid mis-parents a blocked request under a
sibling of the same session, which is visible and wrong rather than invisible
and wrong.

### A counter that vanishes and returns is a first sample, not a rate

The counter arithmetic merged each reading into the previous state instead of
replacing it, so a counter that dropped out of one reading and came back was
differentiated against a stale value using only the latest tick's elapsed time.
The result is an inflated rate that still looks plausible, which is the precise
failure that file exists to prevent. The state is now replaced wholesale. Costs
one tick of "unavailable" after a counter reappears, instead of a number.

### The pinned connection, and everything it dragged in

The plan queried through a pooled connection limited to one. That is wrong for
a reason the plan itself half-knew: `database/sql` resets a connection on every
checkout, the driver turns that into the TDS reset flag, and SQL Server treats
it as `sp_reset_connection`, which zeroes the session's cumulative CPU. The
observation budget, which is the whole of specification section 10, would have
measured a permanently zeroed session.

Pinning one connection fixes that and removes two things the pool was giving
for free. Serialisation, because a shared connection is not safe for concurrent
use and two tiers sample from separate goroutines, so the source took a mutex
held across the whole query-and-scan cycle. And recovery, because a pinned
connection that dies stays dead, so the source re-pins. A later round added
early detection of a connection that dies mid-result-set, which the driver
reports as a raw read error rather than a sentinel.

### tempdb is allocation minus deallocation

Specification section 8.1 defines the column that way and the plan's SQL summed
allocation only. Alloc-only means a request that spilled four gigabytes and
freed them keeps reporting four gigabytes until it ends, so sorting by that
column ranks a request holding nothing above one holding gigabytes. That is the
exact question the column exists to answer.

### The requests query is built from the version and the capabilities

The plan used one constant assuming the best case. Two consequences: `r.dop`
does not exist before SQL Server 2016, so the whole query fails on 2014 with an
invalid column name and the grid stays permanently empty on a version the
specification promises the grid works on; and the query read a DMV whose
capability the source already probes, so a login without that right got a clean
preflight and then an empty grid. The query is now built once, after the
preflight, with the column count fixed at 25 on every branch so the scan never
varies. A configuration knob for this was rejected.

### A managed instance is not SQL Server 2014

Both Azure engines report product version 12.0.x while sitting at or above the
newest boxed release. Azure SQL Database was already excused from the version
gates by name; a managed instance was not, so it took the pre-2016 branch and
lost a column it has, and was refused the live plan probe although the feature
has been on there all along. Silent losses on a supported target, which is the
worst shape for this kind of bug: nothing fails, a column is just empty. The two
editions stay distinct in the model rather than collapsing into one flag,
because they genuinely differ in what they expose. Note that edition 8 is
asserted from documentation, not from a managed instance anyone here can reach.

### The observation budget keeps its sliding window

This is the decision that most nearly went wrong. The plan's suggested code
escalated on every observation once its window was full, running from no
throttling to full throttling in three ticks, before the first change could
possibly show up in the measurement. That is a real control-loop error and the
implementer was right to reject it. But the tick counts in the plan's own test
file encoded the same window-less design, so treating the tests as ground truth
led to deleting the sliding ten second window the specification mandates.

The consequence was demonstrated rather than argued. Driven with a realistic
minute of samples averaging 29 ms/s against a 50 ms/s budget, a server that is
completely fine, the window-less version throttled at the first five-second tier
tick, reached the floor in 34 seconds, and had not recovered after ten minutes,
because that tier's burst recurs faster than the quiet period it needs. The
window is precisely what stops the tool reacting to its own sampling rhythm. It
came back, the escalation cooldown stayed, and the test's tick count changed.

### Tier D is not throttled

The specification's degradation order is C, then B, then A. The CPU history tier
is not in it, and the engine only produces that data once a minute, so doubling
its period drops half the history while saving nothing against a per-second
budget. The plan threw it in anyway.

### The reference key covers everything the reference carries

The wire protocol sent a session's invariants once and referred to them
afterwards, keyed by session and statement, while the reference itself carries
the login, the host and the program as well. SQL Server reuses session ids
routinely, so a session handed to a different login running the same statement
text sent no new reference and the grid kept the previous owner's identity. In
a tool that can kill sessions, showing the wrong login beside a session id is
worse than showing nothing.

### The page has no subresources

The grid could not load in a browser at all. The page is served at a URL
carrying the token in its query string, and a relative reference does not
inherit the base URL's query, so the browser asked for the stylesheet and the
script without a token and got two refusals. Everything had been verified with
`curl` and an explicit token, which is exactly the method that cannot see this.

Both assets are now composed into the document at serve time, with the files
kept separate on disk. The page has no subresource at all, which removes the
token problem, the referrer question and a round trip together. A cookie is the
obvious fix and the wrong one: cookies are not scoped by port, so any other
local page on the loopback address would have the browser attach it.

### The grid reads the capabilities it is sent

The wire has carried the source's capabilities since the protocol was written,
and the page ignored them. Where a capability is missing the query substitutes a
literal zero, so on a 2014 instance the grid stated that every request uses
0.00 MB of tempdb and runs at parallelism 0, with the same authority as a
measurement, in front of someone diagnosing an incident. That is the plausible
zero the specification forbids.

### One condition decides the parallelism column

The fix above initially routed the browser's half through a capability and left
the query's half reading the version directly, with a comment asking the next
person to keep two copies in sync by hand. The stated reason was that a test
pinned one behaviour, which is a test encoding the old design. The condition now
lives in one function, the preflight sets the capability from it, and the query
reads the capability.

## Decisions about the server and its safety

The HTTP listener binds the loopback address as a literal, and the only
configuration field that reaches it is a port number. A review tried to reach it
on this machine's real network address and on the IPv6 loopback and was refused
both times.

The token is 128 bits from the cryptographic generator, compared in constant
time, and the refusal is byte-identical whether the token is missing, wrong, or
wrong at the same length.

A check on the `Host` header was added although the specification does not ask
for it. Without it a loopback server answers a request whose host is an
attacker's rebound domain. The attacker would already need a domain, a rebinding
resolver, an open tab, the port and the token, so the token is doing the real
work and this is defence in depth. It was added because the token is the thing
that leaks: it is in the URL, so it is in browser history, and the tool prints
it to standard error.

Reconnection with backoff was implemented although the plan lists it as out of
scope. Without it, a tier whose server is unreachable retries on its
one-second tick indefinitely, which is the busy loop specification section 4.4
exists to prevent. The plan's note reads as scoping what the interface does not
render, not as licensing a spin. The rest of section 4.4 is still owed: the next
attempt countdown, marking the retention window stale on disconnection, and
re-running the preflight on a genuine reconnection.

## Decisions about scope

Five things in the plan were confirmed as deliberately absent rather than
missing, after checking the plan's own coverage note: the instance switcher,
the views beyond the request grid, the full column set and its filters, saved
layouts, the plan panel, and the kill flow.

Two consequences were written down rather than silently accepted. The wire
protocol omits five columns that exist in the model; the shape that shipped
makes them cheap to add, provided two of them go into the per-session reference
rather than into the per-tick row, because they are invariants and putting them
in the row would re-inflate the payload the protocol exists to shrink. And the
kill capability is declared in the model and named on the wire but never set by
the preflight, which is correct while the kill flow is out of scope but reads
as a bug to anyone who finds it, so it is now commented as reserved.

## Decisions about tooling, which were not in the plan at all

The test container held only the system databases. That is enough to ask the
DMVs about a sleeping session and not enough for anything else: a query that
reads nothing produces no logical reads, no tempdb, no memory grant, and two
sessions cannot block each other without a row to lock. Half the columns this
tool exists to display had nothing to display.

So `scripts/restoredb.sh` restores the usual demonstration backup into the
container, and `sqlstress/` puts load on it from `.sql` files, for a set
duration on a set number of threads. The workload lives in files rather than in
Go, so adding a query to a demonstration means dropping a file in a directory.

Two facts that database taught us, both worth knowing before a demonstration.
It has read committed snapshot on, so a reader never blocks and the blocking
demonstration shows nothing unless the reader asks for the locking flavour with
a hint on the statement, which is preferable to changing a setting on a database
that is not ours. And one table carries a corrupt page: it restores quietly and
then fails any scan with error 824. That looks like a deliberate teaching
artefact, so it was left alone and the queries avoid it.

A question was asked about running an HTML linter or minifier to gain
performance. The answer was no, and the reasoning is worth keeping: the measured
cost is per-tick DOM work, 4.8 ms against 46.8, while the document is parsed
once at load, and the whole page is a handful of kilobytes served over the
loopback address. There is also no generated HTML to clean, because the chosen
renderer recycles a row pool and rewrites changed cells, which is precisely why
it won. The useful half of the idea was taken instead: a test that fails if the
update path ever writes `innerHTML`, which is the mistake that would bring the
46 ms back and is easy to make while adding a column.

## Findings that were rejected, with reasons

Not every review finding was accepted. These were argued down.

The escalation message hardcodes "over the last 10s" while the averaged span
can be shorter early on or longer under slow sampling. Cosmetic, and the
alternative reads worse.

An interval that straddles the edge of the budget window is counted whole rather
than prorated. Prorating would assume cost is spread uniformly inside an
interval, which is the assumption the window exists to stop making.

The wire encoder builds composite keys and splits them apart again. It is the
plan's shape and it runs fifteen times by seventeen. Churn, not a defect.

A defensive clamp in the backoff is behaviourally inert at the default periods.
It is reachable for a tier configured below thirty seconds whose throttled
period exceeds it, so it stays.

The status bar shows a failing tier's error ahead of the throttle text. An
operator wants the failure before the degradation it caused.

One line that guarantees a clean shutdown is defended by no test, because
deleting it fails nothing and a test sensitive enough to catch it would depend
on scheduling. The comment was corrected to claim only what holds. A flaky test
guarding a correct line is worth less than an honest comment beside it.

## What could not be verified

Azure SQL Database cannot be containerised and Kerberos against a real domain
needs a domain, so both remain unverified against the real thing. Managed
instance edition detection is asserted from documentation for the same reason.

There is no browser in the environment this was built in. The page's bytes were
validated and the stream was driven end to end against a real engine under
load, but nothing was rendered and nothing was clicked. The 4.8 ms figure is
carried over from the bench on the strength of the mechanism being identical,
not re-measured, and the specification already records that the budget was
measured on a grid that only displayed, so it has to be measured again when
sorting and filtering arrive.

## What the final review found, and what three reviewers disagreed about

After the fourteen tasks were done, the branch went through a whole-branch
review and two external ones, run unattended by `agy` and by `opencode`. The
three disagreed on the verdict, which is itself the useful part: the two
external reviewers said there were no serious defects, and both were wrong, but
each found something true that the other two missed.

### Two defects that no per-task review could have found

Nothing validated the configuration file. A user who wrote a zero tier period
in `sqltop.json`, as a typo or because zero looks like "as fast as sensible",
got a tight loop with no delay and no complaint: measured at about 1.26 million
DMV queries a second against whatever server the tool was pointed at. The
observation budget cannot intervene, because throttling doubles the period and
twice zero is zero. Specification section 2's one hard promise, that the tool
must never become the problem it is meant to diagnose, was defeated by one
character. `config.Load` now rejects a file that would do this, naming the field
and the value, which is how section 8.3 already treats a missing configuration
path.

The tool exceeded its own observation budget in ordinary use. Measured by
reading its own session out of `sys.dm_exec_sessions`: 41.7 ms/s against a
50 ms/s budget at 112 active requests, where the specification sizes the
renderer at 800 rows. Two causes, one fix. The tempdb lookup cost two to five
times the CPU of the rest of the hot query, and 94 per cent of the rows it paid
for were engine internals. The PowerShell prototype this project descends from
filtered exactly those, and the port dropped the predicate silently, which was
possible because the specification described the grid's columns and never said
which requests appear in it. The grid now filters, the specification says so,
and the measurement after the change is 23.0 ms/s, 46 per cent of the budget,
with no throttling.

The port was not literal. Two departures were forced by measurement rather than
taste: the prototype's status-and-open-transaction heuristic hid genuine
long-running work, because a bare `WAITFOR` has no implicit transaction, and it
was replaced by asking whether the session is a user process; and filtering on
the tempdb lookup's own output made that lookup run for every row anyway, 752
milliseconds against 292 over twenty runs, so the filter moved into an inner
derived table.

### Two seams where every layer was individually right

The specification names one figure that must show "n/a" rather than a zero, the
longest running transaction, which is only populated under read committed
snapshot isolation. It was the one figure shipping a zero, marked available,
because the counter layer is correct in isolation and the requirement lives in a
table two sections away. It is now gated on whether any database on the instance
has snapshot isolation on, discovered once with the other server facts.

The status bar could say the tool had throttled itself and could never say it
had recovered. The budget writes the recovery message at the same moment it
returns to level zero, and the collector only forwarded the message when the
level was above zero, so that one string was unreachable by construction. Every
intermediate step was announced and the one saying the tool is healthy again
never was. Neither package was wrong alone, and no test crossed the seam.

The observation cost was also collected and never displayed, although the
specification says an instrument that claims to bound its own cost should show
it. It reached exactly one consumer, a throttle message that only renders once
the cost is already too high. It is now in the status payload and on the page.

### What the external reviewers contributed

`agy` and the final review independently found the same thing: the code paths
that classify a database error as an absent capability rather than a dead
connection had no test coverage at all, because every integration test runs as
`sa`, which holds every right, so no test ever saw a permission denial. That
classifier already had a bug once, and its fix was defended by a comment rather
than a test. It is now exercised against a deliberately under-privileged login
created and dropped by the test, and against a real killed session.

`opencode` found the one thing nobody else did: the integration tests only ever
ran against SQL Server 2022, while the specification targets 2019 as the floor
and promises a clean degradation below it. The version-gated logic was tested
against fabricated version structures and never against an older engine. A 2019
container now exists and the whole integration suite passes against it, which is
the first time this code has met an engine other than 2022. The 2016 and 2017
degraded path is still untested.

Both external reviews were run in a dedicated git worktree so that an unattended
tool with loosened permissions could not touch the repository. One of them
resolved its project to the main checkout anyway and wrote its report there.
Nothing tracked was modified, but the lesson is worth keeping: a worktree is not
a sandbox, and the check afterwards belongs in the real repository, not only in
the copy.
