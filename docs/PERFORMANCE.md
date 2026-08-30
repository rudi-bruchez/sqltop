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
the fourteen it needs, by name, in one round trip.

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

### Sorting and filtering in Go

The question was not whether JavaScript can sort 800 rows. The recycled row
pool is keyed on session and request id, so a sort on a column that moves
every tick gives almost every pooled row a new identity every tick, which
throws away the rewrite-only-what-changed optimisation. If the cost sat
there, moving the sort into the server would not help either, because the
browser still has to repaint reordered rows.

Nine modes, 122 ticks each, every client-side candidate against a server-side
twin delivering the identical row order already sorted and filtered in Go.
The pairs do not separate, and twice the server twin is the slower of the
two. Sorting 800 rows in JavaScript costs 0.2 ms. The full table is in spec
section 10.1.

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

## What has not been measured

- Firefox. Section 10.1's original runs found it two to three times slower
  than Chrome on both renderers, with the ratio between renderers unchanged.
  The sort and filter table is Chrome only. `drive-firefox.js` exists and has
  not been run.
- A remote server. Every observation-cost figure here comes from a container
  on the same machine. The budget is measured as server CPU rather than round
  trip time specifically so that distance does not throttle a healthy server,
  but that reasoning has not been checked against a real network.
- A large instance. The memory clerk query is the one whose cost grows with
  the size of the server, and it has only been measured against a container.
- Anything above 800 rows. That is the number section 10.1 chose and every
  measurement since has used it.
