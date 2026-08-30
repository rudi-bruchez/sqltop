# Changelog

Versioning follows `docs/SPECS.md` section 11: zero-major while the shape can
still change, and the tag is cut when the milestone works rather than when the
constant changes.

## Unreleased

Nothing yet. Still to come: the other views, the plan panel, the kill flow,
and the instance switcher.

## 0.2.1, 30 August 2026

Sorting and filtering, and the verification that was missing under the 0.2
interface.

- Column sorting and per-column filtering in the request grid, placed in the
  browser after measuring every client-side candidate against a server-side
  twin. Five operators, read from what is typed rather than picked from a
  dropdown. Changing a filter re-anchors the view on the selected row.
- The configuration file is now `sqltop.yaml`. JSON is a subset of YAML, so
  an existing file works once renamed, and the resolution looks for both.
- An end-to-end test that drives the real page in a real browser from
  `go test`, hermetic and skipped when chromium or deno is missing. It closes
  the gap that mattered: two of thirty-three JavaScript functions had been
  reachable from any test, under a release that was mostly interface.
- A Firefox pass on the rendering bench, and a standing caveat with it: every
  figure in this project depends on what else the machine was running, which
  caught us three times in one day.
- Each dashboard group folds on its own. The full product version and the
  deployment kind sit beside the edition, and a filter box carries a cross to
  clear it.
- The counters query stopped trimming the columns it filters on, which was a
  per-row cost over fifteen hundred rows buying nothing: 4.38 ms per call to
  2.23.
- `sqlstress` no longer borrows sqltop's configuration type, which had broken
  it silently, and it has tests now.

## 0.2.0, 30 August 2026

The server dashboard, and a body of performance work on both sides of the
wire. The other views, the plan panel and the kill flow are still to come.

- The dashboard of `docs/SPECS.md` section 6, above the grid: instance, host,
  edition, version and uptime read once at connection, then thirty figures in
  four groups, each with a sparkline. An unavailable figure reads n/a and
  never falls back to the last value it had.
- Three families of figures the spec named and nothing collected: scheduler
  load, the memory clerks behind the buffer pool, plan cache and query
  memory, and tempdb broken into user objects, internal objects and version
  store. Plus the instance start time, which had been declared on the model
  and never once assigned.
- `docs/QUERIES.md`, every query the tool sends, extracted from the code by a
  test rather than copied, so it cannot drift. The catalogue behind it also
  walks the call sites, checks that each query carries its hints, and checks
  that none of them writes to the monitored server.
- Grid rows travel as positional arrays with the column order sent once per
  connection, which took a snapshot at 800 rows from 239 kB to 153. One
  reused number formatter in the browser took a refresh from 5.2 ms to 3.7.
- Sorting and filtering were measured before being designed, against a
  server-side twin of every client-side candidate. They belong in the
  browser; `docs/SPECS.md` section 10.1 has the nine-mode table.
- `docs/PERFORMANCE.md`, which records what was optimised, what was measured,
  and what was measured and rejected.

Reviewed externally by two other coding agents. What they found and what was
done about it is in the commit log; the short version is one real defect in
the new wire format's own guarantee, two pieces of debt that had been written
down and left, and three papercuts.

## 0.1.0, 30 August 2026

The collector and a working request grid. See
`docs/plans/2026-08-29-collector.md` for what that covers and what it
deliberately leaves to 0.2.

Settled before any of it was written:

- The main-screen renderer, chosen by measurement over four candidates. The
  hand-rolled virtualised grid refreshes in 4.8 ms against Tabulator's 46.8 at
  800 rows, freezes for none of the wall clock against 5 to 17 %, and never
  loses the selected row. The measurements are in `docs/SPECS.md` section 10.1.
- The wire protocol, which sends per-session invariants once rather than every
  tick after the bench measured 47 % of the payload as redundant.
- The observation budget, measured as server CPU from the tool's own session
  rather than as a round trip, so a distant server does not throttle a healthy
  one.

Added while building it: a demonstration database restored into the podman
container by `scripts/restoredb.sh`, and `sqlstress/`, a load generator that
runs `.sql` files against it for a set duration on a set number of threads.
The container had held only the system databases, which is enough to ask the
DMVs about a sleeping session and not enough for anything else: a query that
reads nothing produces no logical reads, no tempdb, no memory grant, and two
sessions cannot block each other without a row to lock.
