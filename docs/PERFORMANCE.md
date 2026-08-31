# Performance work, and what was measured

Everything here was measured before it was kept, and several things were
measured and then not kept. The rejections are the useful half of this
document: they are the optimisations that look obviously worthwhile and are
not, and without them written down someone re-derives them every six months.

The project rule this serves is in `CLAUDE.md`: measure before optimising,
guessing at performance here has a poor record.

## Where the time and the bytes actually go

Two budgets, and they are not the same budget.

The observation budget is server CPU spent on the monitored instance by the
tool's own session, measured from `sys.dm_exec_sessions` over a sliding ten
second window. Spec section 10 sets it at 50 ms per second by default. This
is the one that matters, because it is spent on somebody else's production
server.

The rendering budget is 16 ms per refresh in the browser, which is one frame
at 60 Hz. Spec section 10.1 holds the measurements.

The wire between them is a third thing, and until it was measured nobody knew
which of the two it belonged to. It turns out to belong to neither: at 800
rows over loopback it is 153 kB a second, which costs no perceptible time at
either end but is the largest number in the system and so attracts attention
it does not deserve.

## Server side, Go

### The query hint that was 87 % of the tool's cost

Every query this tool sends carried `OPTION (RECOMPILE, MAXDOP 1)`, and the
`RECOMPILE` half was there to keep the plan out of the monitored server's
cache. It was never measured. It is now, against the 2022 container under an
eight thread load, forty calls per figure after warming the plan, the cost
read from the tool's own session's `cpu_time`:

| Query | With `RECOMPILE` | Without | Cut |
|---|---|---|---|
| `requestsQuery`, the grid, once a second | 7.6 ms | 0.4 ms | 95 % |
| `countersQuery`, once a second | 2.5 ms | 1.3 ms | 48 % |
| `osViewsQuery`, once a second | 2.5 ms | 0.2 ms | 92 % |
| `sessionsQuery`, on demand | 3.2 ms | 0.1 ms | 97 % |
| `transactionsQuery`, on demand | 3.2 ms | 0.02 ms | 99 % |
| `logSpaceQuery`, on demand | 18.1 ms | 1.5 ms | 92 % |
| `locksQuery`, on demand | see below | see below | not comparable |

Steady state, the three tier queries together: 12.6 ms of server CPU per
second before, 1.8 ms after. The observation budget is 50 ms per second, so
the tool went from a quarter of its own allowance to a twenty-fifth of it.

All of that was compilation. `SELECT name, recovery_model_desc,
log_reuse_wait_desc, state_desc FROM sys.databases` costs 11.15 ms with the
hint and 0.07 ms without, on five databases; nothing about executing that
statement takes eleven milliseconds.

`locksQuery` is left out of the table on purpose. Its cost is the walk of
the lock manager, which no hint changes, and that walk depends on how many
locks exist at the instant of the call. Under the load these numbers were
taken under, that population comes and goes with the blocker transaction the
generator opens and rolls back, so two consecutive measurements of the same
statement came out at 31 ms and 16 ms and one ordering of the runs made the
`RECOMPILE` version look faster. The honest statement is that it costs tens
of milliseconds, that compilation is a small part of that, and that a
figure for it is only worth quoting alongside the lock count it was taken
at. Nothing else in the tool has that property.

What the hint bought was ten fewer cached plans on a server that holds
thousands. The argument that usually justifies `RECOMPILE` on a monitoring
query, that a plan compiled at one cardinality is wrong at another, does not
apply here: these statements take no parameters, and the dynamic management
views carry no statistics, so a fresh compile produces the same plan from
the same fixed guesses every time. Checked rather than assumed, by driving
the grid query on one cached plan under an eight thread and then a
forty-eight thread load: the CPU per call tracked the row count and nothing
else, 0.4 ms against 6.3 ms for roughly sixteen times the rows.

One thing fell out of it. `model.Cost.LogicalReads` is now reliably zero.
The reads it used to carry were the compiler reading catalog metadata; the
views themselves are memory resident and the queries read no pages at all.
An integration test asserting that twenty samples cost some logical reads
failed on the change, correctly, and now asserts on CPU.

`MAXDOP 1` stays. It is what keeps a monitoring query from taking parallel
workers on the server it is watching, it is per statement, and it costs
nothing. `TestEveryQueryCarriesTheHints` now requires it and forbids
`RECOMPILE`, so putting the hint back on one statement means changing that
test and redoing this measurement, which is the point.

### The database a transaction is in

`sys.dm_tran_database_transactions` has one row per database a transaction
has touched, and the first version of the transactions view took
`COUNT(*)` as the number of databases it spans and `MIN(database_id)` as the
one it is in. Both are wrong, and the container said so within one run of a
test written to check it: a single `INSERT` into a single table reported
three databases and named `master`.

The three rows are the database the work is in, `tempdb`, and the resource
database at id 32767. What separates them is written log: only the real one
has any. So the name is now the database with the most log written, and the
count is how many databases have written any log at all with `tempdb`
excluded, because a temporary table is not a second database anybody means
when they say a transaction spans two.

The first attempt at that fix ranked user databases first with `database_id
> 4`, which put the resource database at the top of every transaction and
made `DB_NAME` return NULL, so every row came back with no database at all.
The same test caught it. `TestTransactionNamesTheDatabaseTheWorkIsIn` is the
regression guard for both.

### The requests query

The tempdb per-task figure arrives through an `OUTER APPLY`. Filtering on its
output rather than inside a derived table made it run once per row: 752 ms
against 292 ms of server CPU over twenty runs. It now sits in an inner
derived table.

Engine-internal sessions are filtered in the `WHERE` clause using
`sys.dm_exec_sessions.is_user_process`. Before that, 94 % of the rows shipped
were engine internals nobody asked for, and the tool spent 41.7 ms/s of its
own 50 ms/s budget at 112 concurrent requests, on a server the spec sizes at
800. After: 23.0 ms/s.

The query is built once per server from its version and the login's
capabilities, not assembled per tick. `r.dop` does not exist before SQL
Server 2016 and the tempdb view needs a right a login may not hold, so the
alternative was either a per-tick branch or a query that fails forever.

### The counters

`sys.dm_os_performance_counters` returns roughly 1500 rows. The tool asks for
the sixteen it needs, by name, in one round trip.

The predicate compares the padded column directly rather than
`RTRIM(LTRIM(counter_name))`. The trimming looked like defensive coding
against a `CHAR`-padded column and was in fact a per-row cost over fifteen
hundred rows, evaluated before the `IN` list was considered, buying nothing:
SQL Server already ignores trailing spaces when comparing a character column
with `=` or `IN`.

| Predicate | Cost per call |
|---|---|
| `RTRIM(LTRIM(counter_name)) IN (...)` | 4.38 ms |
| `counter_name IN (...)` | 2.23 ms |
| bare `COUNT(*)`, no predicate at all | 1.00 ms |

Both forms were checked to select the identical seventeen rows before the
change was made, and a test now fails if the trimming returns to the `WHERE`
clause. It stays on the `SELECT` list, where it runs seventeen times rather
than fifteen hundred and where the Go side genuinely needs it.

This was found while answering a different question, which is worth
recording because the wrong answer was the plausible one. Asked whether a
configurable dashboard should trim the counter list to only the tiles on
screen, the measurement said trimming from eighteen counters to one saves
62 %. That looked like an argument for coupling the query to the UI. It was
an argument that the predicate was bad: fixing it saves 49 % for everybody,
with no configuration, no coupling, and all sixteen counters still
collected.

Scheduler load and the memory clerks travel together in a second query rather
than two, because both are `sys.dm_os_` views needing the same right and
absent on the same engines. Measured at 1.70 ms of server CPU per call
against 4.17 ms for the counters query beside it, which is 3.4 % of the
budget at a one second period. The memory clerk half is the one that grows
with the size of the instance, so that figure is worth taking again on a
large server.

An external reviewer measured the same tier under its own load and got
10.65 ms for the two queries together, which leaves roughly 6.5 ms for
osViewsQuery against the 1.70 ms recorded above. Both figures are honest and
they were taken under different loads on a machine that was not idle either
time. The disagreement is the useful part: it says the memory-clerk half of
that query grows with what the server is doing, not only with how big it
is, and that the recorded figure should have said what the machine was
doing when it was taken. Every measurement in this document was taken on
this workstation against a container, with the tool and a load generator
running and nothing else deliberately loading the machine.

tempdb's total is summed in Go from its three parts rather than asked of the
server as a fourth aggregate. This is not a speed optimisation: two
aggregates over a view that moves between them can disagree, and a dashboard
whose parts do not add up to its whole is one an operator stops trusting.

### The wire protocol

Two changes, both measured, and the second is the larger.

Per-session invariants travel once. SQL text was 24 % of a row and the
program name 7 %; with login and host, which are the same kind of value, the
reference table removed close to a third of every row. Section 10.1 has the
original breakdown.

Rows are positional arrays rather than objects. Even with the reference table
in place, ninety-eight bytes of every 298 byte row were the eighteen JSON key
names, their quotes and their colons: a third of the steady state spent
restating the same eighteen words eight hundred times a second.

| Row encoding | Bytes per row | Snapshot at 800 rows |
|---|---|---|
| Object | 298 | 239 kB |
| Positional array | 191 | 153 kB |

The column order travels once per connection rather than being hard-coded on
both sides, and is checked against the row struct's own field tags by
reflection. A positional format without a header is a format where a field
added on one side shifts every column after it on the other, nothing fails,
and reads appear under writes.

The string and number writers are hand-rolled, because they run five times a
row eight hundred times a second and `encoding/json` allocates a fresh slice
per value. They are allowed to exist only because two fuzzers hold them to
producing exactly what `encoding/json` produces, byte for byte. That was not
a formality: within two seconds the fuzzer found the replacement character
emitted as its escape rather than as itself, and the backspace and form feed
characters written as six-digit escapes where the standard library writes
their short forms. They then ran 2.8 million and 3.9 million executions
clean. The one deliberate divergence is that a NaN or an infinity becomes 0
rather than an error, because failing a whole snapshot over one
unrepresentable cell would blank the grid.

### The page

The interface is composed once and served inline: no stylesheet request, no
script request, and an inline data URI for the icon. That began as a
correctness fix rather than a speed one. A relative URL does not inherit the
query string that carries the per-run token, so the browser fetched
`/style.css` and `/app.js` without it and got 401; every check until then had
used curl with an explicit token. The favicon was the same bug with a
different cause: the browser asks for `/favicon.ico` unprompted, and every
route here requires a token by design.

## Browser side, JavaScript

### The renderer

A pool of recycled table rows covering the visible window, two spacers
holding the scroll height, and only changed cells rewritten. Chosen over
Tabulator by measurement, 4.8 ms against 46.8 at 800 rows, with the deciding
figures being frozen wall clock and lost scroll position rather than
milliseconds. Section 10.1 has the table.

The per-tick path never writes markup except on a single cell whose content
changed. `internal/web/app_assets_test.go` enforces that by scanning the
file, because the mistaken version still renders correctly and only the
timing regresses, which no review catches.

### Number formatting

The largest remaining win, and it was found by profiling rather than by
thinking. `Number.prototype.toLocaleString` builds a formatter on every call.
Over a 45 second profile at 800 rows it was the second most expensive named
function in the page, 115.5 ms of self time against 148.6 ms for the whole
of `layout`.

One reused `Intl.NumberFormat` instead:

| | before | after |
|---|---|---|
| `n0` self time over 45 s | 115.5 ms | 9.6 ms |
| apply p50 at 800 rows | 5.2 ms | 3.7 ms |
| apply p50, slowest of nine modes | 6.4 ms | 4.3 ms |

A 27 % cut in the cost of a refresh, from one line, with formatting verified
byte for byte against what it replaced.

### What the profile says is left

45 seconds at 800 rows and 1 Hz. The page uses 3 % of one core; 97 % of the
time the processor is idle.

| | self time over 45 s | per tick |
|---|---|---|
| `(program)`, V8 internals and `JSON.parse` | 1017 ms | 22 ms |
| `layout` | 143 ms | 3.2 ms |
| garbage collector | 54 ms | 1.2 ms |
| `sparkPoints` | 11 ms | 0.24 ms |
| `n0` | 9.6 ms | 0.21 ms |

`frame p95` is 16.7 ms on every mode measured, which is exactly one frame at
60 Hz. The page is already hitting the display's cadence and dropping
nothing. Optimising below that is not visible to anyone.

### Bounded memory

The client prunes its reference table on the same rule the server uses: a key
no row used this tick is dropped. Without it a tab left open grew without
bound, measured at roughly 1.3 MB an hour.

Sparkline history is capped at 120 points per tile. Spec section 6 asks for a
sparkline over the retention window, and this is deliberately not that: the
window is fifteen minutes, nine hundred points at one tick a second, and a
sparkline a hundred pixels wide draws that as a smear.

## Measured and rejected

### Preparing the statements

Once `RECOMPILE` came off, the obvious next question was whether the
statements should be prepared, so the text crosses the wire once and every
later call sends a handle through `sp_execute` instead of three kilobytes of
SQL.

Measured on the same connection the tool actually uses, so the cost lands in
the same session `Cost` differentiates, forty calls each:

| Query | Ad hoc | Prepared |
|---|---|---|
| `requestsQuery` | 0.10 and 0.42 ms | 0.70 and 0.15 ms |
| `countersQuery` | 1.35 and 1.30 ms | 1.50 and 1.25 ms |
| `osViewsQuery` | 0.30 and 0.17 ms | 0.15 and 0.12 ms |

Two runs each, and the two columns are inside each other's noise. There is
nothing to win: the plan is already in the cache under its statement text,
and finding it there costs a hash lookup. What preparing would buy is the
three kilobytes of query text per call, which on a loopback or a local
network is not a figure anybody can feel, against handles that live on the
connection and would have to be invalidated and rebuilt every time the
pinned connection is repaired. Rejected.

### Sorting and filtering in Go

The question was not whether JavaScript can sort 800 rows. The recycled row
pool is keyed on session and request id, so a sort on a column that moves
every tick gives almost every pooled row a new identity every tick, which
throws away the rewrite-only-what-changed optimisation. If the cost sat
there, moving the sort into the server would not help either, because the
browser still has to repaint reordered rows.

Nine modes, 122 ticks each, every client-side candidate against a server-side
twin delivering the identical row order already sorted and filtered in Go.
The pairs do not separate. Measured a third time once the feature actually
existed, against the shipped sort and filter rather than the bench's
simulation of one, every client mode landed at or below the
no-sort-no-filter baseline: the work costs less than the measurement can
see. The full table is in spec section 10.1.

Client-side also keeps the properties worth keeping: a filter per viewer
rather than per server, a filter over the retention window rather than one
that decides what was ever collected, and rows that leave the grid because
they ended rather than because they stopped matching, which the protocol
cannot otherwise tell apart.

### Minifying the page

Real, and far too small to matter.

| | before | after |
|---|---|---|
| composed page | 32.0 kB | 18.8 kB |
| transfer over loopback | 0.9 ms | about 0.5 ms |
| JavaScript compile | 0.3 ms | 0.2 ms |

That is roughly half a millisecond, once, at page load, against 153 kB a
second on the stream: the saving is spent in a sixteenth of one tick. It also
carries a correctness risk that is not hypothetical. Stripping JavaScript
comments correctly needs a tokenizer, because a regular expression cannot
tell `//` in code from `//` in a string or a regex literal, and `app.js`
contains regex literals. The `setup-region` markers the regression guard
reads are themselves comments.

### Compressing the response

`compress/gzip` would take the page from 32.0 kB to 11.8 kB, which beats
minification on every axis and carries no correctness risk. Not taken, for
the same reason minification was not: 0.9 ms of loopback transfer is not a
problem. Worth reconsidering only if the interface is ever served over
something that is not loopback, which the security model currently forbids.

### The sparklines

Suspected alongside the number formatter, profiled at 0.24 ms per tick.
Untouched.

### Pre-built DOM nodes instead of markup writes

Would remove most of the 1.2 ms per tick of garbage collection by replacing
per-cell markup with child nodes updated through `textContent`. That is an
invasive change to the render path for about a millisecond against a
sixteen millisecond budget, on a page already hitting frame rate. Not taken.

### Shorter comments as a performance measure

`app.js` went from 43 % comments to 27 %, and the file is 23 % smaller. The
JavaScript compile time moved from 0.3 ms to 0.2 ms, which is nothing. The
pass was worth doing for readability and the rule is recorded in `CLAUDE.md`;
it is listed here so nobody records it as a performance win.

## The tools

All of these live in `bench/`, which is deliberately untracked, so they exist
only on a machine that has them.

- `bench/sortfilter/` serves the real `internal/web/assets` files with two
  single-line hooks patched into `app.js`, feeds them real
  `model.RequestSample` values through the real encoder, and measures apply
  time, frame time, frozen wall clock, lost scroll and lost selection. Both
  patches fail loudly if their anchor moves. A bench that measures its own
  copy of a renderer measures its own copy.
- `bench/sortfilter/drive.js` runs every mode unattended over the DevTools
  protocol. `drive-firefox.js` does the same over WebDriver BiDi.
- `bench/sortfilter/profile.js` takes a real CPU profile of the live page and
  prints self time per function.
- `bench/probe.js` loads the real page in a real browser and reports console
  errors, failed requests and what every tile and cell actually shows. This
  is the tool that finds the class of bug curl cannot: it found both the
  subresource 401s and the favicon 401s.

## The machine is a variable, and it has caught this project three times

Every number in this document was taken on one workstation, and three
separate times the state of that workstation turned out to be the thing
that moved a measurement, not the code.

A morning of benchmark runs left eighty-one stale chromium processes alive,
because Linux truncates a process name to fifteen characters and
`pkill -x chromium-browser` therefore matched nothing. The first one kept
the debugging port, later launches quietly handed their tabs to it, and it
was carrying twenty dead tabs still retrying their connections. Everything
measured before that was noticed had been measured on a busy machine.

An external reviewer measured a refresh at 7.8 ms against the 3.7 recorded
here, on a machine that was simultaneously running a coding agent, a browser
and the tool. Both figures were honest and they described different
conditions. What was wrong was this document, for not saying which.

And the Firefox pass below was taken on a pristine headless profile, no
extensions, no other windows, while the original Firefox measurements in
spec section 10.1 were taken on a machine running the owner's ordinary
Firefox with many windows open. Those old numbers said Firefox was two to
three times slower than Chrome. The new ones say it is slightly faster.
Attributing that to the browser would be a mistake: the likelier variable is
the load, and the difference between the two conditions is plausibly larger
than any difference between the two browsers.

The rule that follows, and it is the only defensible one: a rendering figure
means nothing without the state of the machine that produced it. Every table
here now says what else was running. And the case nobody has measured is the
realistic one, a browser with forty tabs and a dozen extensions, which is
what the tool will actually run in.

## Suggested by an external reviewer, and what came of each

An external reviewer was given docs/QUERIES.md and the cost table above and
asked what else could be done. Six suggestions came back. They are recorded
here with their verdicts, because a rejected suggestion with no reason
written down gets raised again by the next reviewer and paid for twice.

**Group the lock aggregate on the entity id and resolve the object name once
per group.** The reasoning was that `OBJECT_NAME` is called once per lock and
takes a schema latch each time. Measured, twice, and it was slower: 39.4 ms
against 31.7 ms on the same lock population. The `CASE WHEN resource_type =
'OBJECT'` guard already stops the function being called for the key and page
locks that make up almost all of a large population, and grouping on the raw
entity id splits what is now one row per index back into one row per
partition. Rejected, with the caveat that the lock population moves under
the measurement and neither figure is worth much on its own.

**Restrict the lock view to sessions in a blocking chain.** The same reviewer
argued against its own suggestion, correctly: an idle transaction holding a
large lock count and blocking nobody yet is exactly what a DBA is looking
for. Not done.

**Take the SQL text out of the tick.** `sys.dm_exec_sql_text` is applied once
per row in the grid query, and on a server with eight hundred active requests
that is eight hundred calls into the plan cache for text the client already
holds and caches by fingerprint. This is the most promising of the six and
it is not done, because the container cannot produce eight hundred concurrent
requests and a change of this size on an unmeasured hypothesis is exactly
what section 2.1 forbids. Two shapes are worth measuring when a server that
big is available: deduplicating the handles inside the query, which needs no
client change and cannot lose much, and returning handles rather than text
with a second query for the ones the tool has not seen, which needs a cache
in the source and is the larger win if handle diversity is low.

**Pre-aggregate `sys.dm_db_task_space_usage` by session and LEFT JOIN it,
instead of applying it per row.** Plausible and not yet measured. Note that
this is not the shape already rejected above under "the requests query":
that one moved the filter, this one moves the aggregation.

**Merge the round trips.** `countersQuery`, `osViewsQuery` and `costQuery`
run on the same tier in the same tick and are three batches today. They could
be one, read back with `Rows.NextResultSet`. It saves TDS framing and two
round trips, which on a loopback connection is a fraction of a millisecond of
wall clock and no server CPU at all. It would matter on a server across a
WAN, which is in the list of things this project has never measured. Not done.

**A larger TDS packet size.** Measured, on an 800 row result of about 400 kB,
four runs: 1.50 to 1.63 ms of wall clock at the default 4096 bytes, and 1.16
to 1.31 ms at 16384 or 32767. A real 15 to 20 % of the transfer, and no
change in server CPU that the noise allows to be read. Not taken yet for the
same reason as the merge: the saving is wall clock on a link that has no
latency, and the case for it is a remote server nobody here has measured.

### And what it got wrong

The same reviewer said `FROM tempdb.sys.dm_db_file_space_usage` would fail on
Azure SQL Database because three-part names are not supported there. It is
wrong: Microsoft's own T-SQL differences page says "three part names
referencing the tempdb database and the current database are supported", and
the tempdb page gives `SELECT ... FROM tempdb.sys.database_files` as an
example. Checked against the documentation rather than argued about, because
this is one of the two things the project cannot test locally.

It also reported that the ring buffer CPU history silently returns nothing on
Azure SQL Database. That is half right and already handled: the capability
probe skips that check outright on Azure SQL Database, so the two CPU tiles
are greyed as unavailable rather than showing a plausible zero. What is true
underneath it is a real gap rather than a defect, and it is in the list
below: Azure SQL Database reports CPU through `sys.dm_db_resource_stats`, and
this tool does not read it.

It was right about one thing, and it was a defect in code written the same
day. See "the database a transaction is in" above.

## What has not been measured

- A browser under realistic load. Both passes ran on pristine headless
  profiles with no extensions and nothing else open, which makes them
  comparable to each other and to nothing a user will experience. See the
  section above: this is now the largest known gap in these figures.
- A remote server. Every observation-cost figure here comes from a container
  on the same machine. The budget is measured as server CPU rather than round
  trip time specifically so that distance does not throttle a healthy server,
  but that reasoning has not been checked against a real network.
- A large instance. The memory clerk query is the one whose cost grows with
  the size of the server, and it has only been measured against a container.
- Anything above 800 rows. That is the number section 10.1 chose and every
  measurement since has used it.

## What the budget cannot see

The observation budget measures the CPU of the tool's own session. Extended
Events dispatch does not run there: predicate evaluation and event
construction happen on the thread of the workload being watched. The
statement capture is the first feature in this tool whose cost its own
instrument cannot report.

The predicate is one integer comparison per candidate event, and the two
events fire once per batch rather than once per statement, so the expected
cost is small. Expected is not measured. Measure it against the containers
before relying on the number, and record it here beside the others.

One figure is measured and belongs here now. The ring buffer holds whichever
of a thousand events and 1024 KB comes first, so drained every two seconds
the capture keeps up with about five hundred statements a second on the
watched session. Past that it reports exactly what it missed.
