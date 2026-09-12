# Screenshots

## Server metrics

The server dashboard and live requests in one view, covering CPU, memory,
throughput, transactions and tempdb.

![SQLTop server metrics](screenshots/screenshot01-metrics.png)

## Active requests

The compact request grid shows running and suspended work, current waits and
the SQL text, with a filter for every column.

![SQLTop active requests](screenshots/screenshot02-requests.png)

## Sessions

The sessions view includes connection age, idle time, open transactions and
resource use.

![SQLTop sessions](screenshots/screenshot03-sessions.png)

## Session waits

Selecting a request and opening its waits shows the distribution accumulated
by that session since the last reset.

![SQLTop session waits](screenshots/screenshot04-session-waits.png)

## Live execution plan

The plan view follows an executing statement and compares actual rows with the
optimizer's estimates.

![SQLTop live execution plan](screenshots/screenshot05-live-execution-plan.png)

## Session history

The rolling history keeps recently completed statements on the selected session, 
including their peak duration, CPU and wait type.

![SQLTop session history](screenshots/screenshot05-session-history.png)
