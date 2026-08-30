# Changelog

Versioning follows `docs/SPECS.md` section 11: zero-major while the shape can
still change, and the tag is cut when the milestone works rather than when the
constant changes.

## Unreleased

0.2 in progress. The server dashboard is in; the other views, the plan panel
and the kill flow are not.

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
  checks that each query carries its hints and that none of them writes to
  the monitored server.

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
