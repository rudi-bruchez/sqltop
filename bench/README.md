# Rendering bench

Purpose: decide how the sqltop main screen renders, before writing the
collector. The bench talks to no DBMS. It simulates a population of active
requests evolving the way it would on a busy server.

## Running it

```sh
go run ./bench
```

Then open http://127.0.0.1:8420

Flags: `-addr`, `-rows`, `-hz`, `-churn`. The last three can also be changed
live from the page.

## What is compared

Four strategies, same columns, same instrumentation.

| Mode | Feed | Question it answers |
|---|---|---|
| Tabulator `setData` | snapshot | The worst case, used as an upper reference |
| Tabulator `replaceData` | snapshot | The reasonable default |
| Tabulator `updateData` + delta | delta | Is the delta worth its complexity |
| Hand-rolled HTML table | snapshot | Is one less dependency worth it |

The hand-rolled table is virtualised, like Tabulator: a pool of recycled `<tr>`
covering only the visible window, two spacers to hold the scroll height, and a
rewrite of only those cells whose rendering changed. Without that virtualisation
the comparison is rigged, since Tabulator only ever paints one screenful.

## What to look at

The timings matter less than the two amber counters.

- `apply p50` / `p95` : synchronous apply time. Under 16 ms, the refresh fits in
  a single frame.
- `frame p95` : time to the painted frame. This is what the user feels.
- `freezes >50 ms` : intervals between painted frames longer than 50 ms, that is
  the moments the interface stalled. Measured by observing frames directly, so
  it works in Firefox as well as Chrome. PerformanceObserver's `longtask` entry
  type does not exist in Firefox and would always read zero there.
- `scroll lost` : ticks where the scroll position jumped back to the top. A
  single occurrence per minute makes the tool unpleasant.
- `selection lost` : ticks where the selected row lost its selection while still
  present in the data. This is the knockout criterion: if you cannot keep one
  query in view while it runs, the tool is pointless.

Suggested protocol: hit "realistic load", select a row, tick "continuous
scrolling", let it run for two minutes per mode, then "copy report". Two minutes
is about 120 ticks; below that the p95 is roughly the maximum and means nothing.
In Firefox, `performance.now()` is rounded to the millisecond, so readings carry
a plus or minus 1 ms.

## Measurements (Chrome 151, Linux x86_64, 800 rows, 1 Hz, 5 % churn)

| Mode | Layout | Ticks | apply p50 | apply p95 | frame p95 | Time frozen | Scroll lost | Selection lost |
|---|---|---|---|---|---|---|---|---|
| plain | - | 120 | 4.8 ms | 5.8 ms | 15.5 ms | 0 s (0 %) | 0 | 0 |
| setData | fitDataFill | 120 | 46.8 ms | 51.3 ms | 54.8 ms | 6.0 s / 120 s (5 %) | 120 of 120 | 0 |

Chrome is two to three times faster than Firefox on both renderers, but the
ratio between them does not move: a factor of ten.

`scroll lost` at 120 of 120 is the sharpest result of the whole bench. It is the
documented signature of `setData`, which resets the scroll position to the top on
every call. Once a second, the list jumps back to the beginning. That alone
eliminates the mode, whatever its speed. The selection does survive, Tabulator
finding it again through its `spid` index.

Method note: earlier Firefox runs reported `scroll lost = 0` for `setData`. That
was a flaw in the counter, which compared the position before and after without
neutralising the automatic scrolling. Once fixed, it detects the failure on 120
ticks out of 120.

## Measurements (Firefox 153, Linux x86_64, 1 Hz, 5 % churn)

| Mode | Layout | Rows | Ticks | apply p50 | apply p95 | frame p95 | Time frozen | Selection lost |
|---|---|---|---|---|---|---|---|---|
| plain | - | 760 | 124 | 12 ms | 17 ms | 36 ms | 5.0 s / 124 s (4 %) | 0 |
| replaceData | fitDataFill | 760 | 122 | 163 ms | 178 ms | 182 ms | 20.3 s / 122 s (17 %) | 4 |
| replaceData | fitDataFill | 880 | 121 | 163 ms | 181 ms | 186 ms | 20.4 s / 121 s (17 %) | 8 |
| replaceData | fitColumns | 300 | 12 | 133 ms | 140 ms | 143 ms | 1.5 s / 12 s (13 %) | not tested |
| setData | fitColumns | 300 | 11 | 78 ms | 115 ms | 116 ms | 0.9 s / 11 s (8 %) | not tested |
| setData | fitDataFill | 300 | 6 | 93 ms | 101 ms | 103 ms | not tested | not tested |
| updateData + delta | - | 300 | - | interface became unresponsive | | | | |

Runs below twenty ticks only serve the layout diagnosis. Only the runs above 120
ticks carry usable percentiles.

## Decision: hand-rolled virtualised renderer

Three gaps, all pointing the same way, on two-minute runs.

Cost per refresh: 12 ms against 163 ms, a factor of thirteen.

Time with the interface frozen: 4 % against 17 % of wall-clock. A grid that
blocks one sixth of the time is not a real-time monitor, it is a slideshow.

Selection: never lost over 124 ticks, against 4 to 8 losses on the Tabulator
side. Selection is the central gesture of the tool, keeping one query in view
while it runs.

The layout was not to blame. Moving `replaceData` from `fitDataFill` to
`fitColumns` changes nothing (133 against 134 ms); `setData` gains modestly (78
against 93 ms). The cost is intrinsic to a full data replacement in Tabulator,
and it is nearly independent of volume: 93 ms at 300 rows against 100 ms at
3000. It is a fixed cost per tick, not a volume cost, so trimming the displayed
load will not make it go away.

### What the hand-rolled renderer still has to grow

The bench only measures the refresh loop. Tabulator also brought header sorting,
column resizing and reordering, filters, layout persistence, exports and
keyboard navigation. None of that comes with the 90 lines of the hand-rolled
renderer. Budget a few hundred more lines, to write and to maintain. That is the
real price of the decision, taken knowingly rather than ignored.

### Open items

The 42 freezes seen in the hand-rolled mode under Firefox drop to zero under
Chrome, so it is Firefox-specific behaviour rather than a design flaw. Batching
`layoutPlain` into a `requestAnimationFrame` is still worth doing; it costs ten
lines and can only help.

### Budget this frees up

At 4.8 ms per tick over 800 rows, rendering burns about half a percent of one
core. The whole observation budget therefore stays available for the collector,
and that is where the real risk of the product sits: the DMV polling loop, not
the display.

## Findings settled while writing the bench

The blocking tree is incompatible with delta mode. When a row's parent changes,
Tabulator moves it back to the root, so the whole tree would have to be rebuilt
on every tick, which cancels the benefit of the delta. The checkbox is therefore
disabled in that mode. Consequence for the real tool: either the tree lives in
snapshot mode, or the blocking chain is flattened on the Go side into an
indentation column, the way the PowerShell prototype already does.

The delta saves nothing on the wire. Measured at 300 rows, a snapshot weighs
167 kB and a delta 168 kB: on active requests every counter moves every second,
so every row ends up in the delta. The only possible gain of a delta is DOM
work, and the bench measured that it loses there too.

There is real fat on the wire, though. Out of 565 bytes per row:

| Field | Weight | Changes between ticks |
|---|---|---|
| `sql` | 24 % | Never, for a given spid |
| `cpu_hist` | 16 % | One point out of twenty-four |
| `program` | 7 % | Never |

That is 47 % of the payload redundant from one tick to the next. The real
protocol should send the SQL text and the program name once per session, in a
reference table, and append a single point to the CPU history instead of
resending the series. Not urgent at 300 rows over loopback; it becomes so from a
remote workstation.
