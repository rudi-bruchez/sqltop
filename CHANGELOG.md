# Changelog

Versioning follows `docs/SPECS.md` section 11: zero-major while the shape can
still change, and the tag is cut when the milestone works rather than when the
constant changes.

## Unreleased, towards 0.1.0

The collector and a working request grid. See
`docs/plans/2026-08-29-collector.md` for what that covers and what it
deliberately leaves to 0.2.

Settled before any of it was written:

- The main-screen renderer, chosen by measurement over four candidates. The
  hand-rolled virtualised grid refreshes in 4.8 ms against Tabulator's 46.8 at
  800 rows, freezes for none of the wall clock against 5 to 17 %, and never
  loses the selected row. `bench/` keeps the harness that proved it.
- The wire protocol, which sends per-session invariants once rather than every
  tick after the bench measured 47 % of the payload as redundant.
- The observation budget, measured as server CPU from the tool's own session
  rather than as a round trip, so a distant server does not throttle a healthy
  one.
