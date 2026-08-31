# sqltop

A `top` for SQL servers. See `docs/SPECS.md` for the full specification; it is
the authority, this file only carries what must be true from the first line of
every session.

## Language

Everything is written in English: code, comments, commits, UI, documentation.
There is no exception. The captured research conversations that used to sit
in `docs/` in French were removed before the project was made public.

## Implementation rules

KISS and idiomatic Go. `docs/SPECS.md` section 2.1 states these as checkable
rules; read it before writing code. In short:

- Standard library first. A new dependency needs a reason stated in the commit
  that introduces it. There is one: `go.yaml.in/yaml/v3`, for the
  configuration file, because that file is edited by hand and handed between
  colleagues and JSON is a poor format for that.
- No abstraction before there is a second real implementation of it.
- Boring concurrency: one goroutine per collection tier, channels, one mutex on
  the retention window.
- Options must earn themselves; a knob added "in case" is debt.
- Debt is written down where it lives, with the reason and the way out.
- Measure before optimising. Guessing at performance here has a poor record.

## Comments

Say why, in as few words as it can be said. A comment earns its place by
explaining something the code cannot; anything else is a second copy of the
code that will fall out of date.

- No archaeology. No "fix round 2", no task numbers, no account of what the
  code used to do or which review found what. Git has all of that, keeps it
  accurate, and does not make the next reader scroll past it.
- Do not restate a measurement that lives in `docs/SPECS.md`. Name the
  section and give the one number that matters.
- JavaScript especially. `internal/web/assets/app.js` is served inline in
  every page, so its comments are read far more often than they are useful.
  They were 43 % of the file once. Keep them near a quarter.
- The exception, and it is the only one: the `setup-region: begin` and
  `setup-region: end` markers in `app.js` are read verbatim by
  `app_assets_test.go` to tell setup work from the render path. They are
  code, not commentary. Read that test before touching them.

The pass that produced this rule was applied to `app.js`. The Go files have
not had it, and several still carry essays and review history. Trimming them
is worth doing and has not been done.

## Hard constraints

- Pure Go, no CGO. The binary is static and cross-compiles in one command.
  Verified to hold even with Kerberos authentication.
- Read-only on the monitored server, with one stated exception. No object
  created, nothing configured, no trace flag set. The exception is the scoped
  statement capture of `docs/SPECS.md` section 2, which creates one named
  Extended Events session, only behind the `-capture` flag, only while
  somebody is watching, and removes it when they stop. Without the flag the
  tool creates and drops nothing at all, the recovery sweep included.
- Plan retrieval is on demand only and never enters the polling loop.
- Secrets come from the environment via `.env`, never from the config file and
  never in code.

## Testing against a real engine

SQL Server runs locally in Podman, so tests hit a real engine rather than a
mock. This machine already has `mcr.microsoft.com/mssql/server` images for
2022-latest and 2025-latest, and stopped containers named `sql2022` and
`sql2025`. Pull 2019 (the minimum target) and 2016 or 2017 (the degraded path)
when those need exercising.

The 2019 image is now pulled and `sqltop-test-2019` runs on port 11439. The
whole integration suite passes against it, which is what the version floor in
the spec is worth checking against. Point the tests at it by exporting
`SQLTOP_TEST_DSN` for that port. 2016 or 2017, the degraded path, is still
unpulled and untested.

Two things cannot be tested locally and stay open until pointed at the real
thing: Azure SQL Database, which cannot be containerised, and Kerberos against a
real domain. Managed instance edition detection is asserted from documentation
for the same reason.

One test is timezone-sensitive and every container here runs in UTC, so it
proves nothing where it usually runs. The capture sweep compares
`sys.dm_xe_sessions.create_time`, which is local server time, against the
server clock; against `SYSUTCDATETIME()` instead, every session looks hours
old and the sweep drops a colleague's live capture on any server west of
Greenwich, while passing on every container on this machine. After touching
`sweepCaptureQueryTemplate` or `sweepOlderThan`, run the sweep tests on a
server that is not in UTC:

```
podman run -d --name sqltop-tz -e ACCEPT_EULA=Y -e TZ=America/Los_Angeles \
  -e MSSQL_SA_PASSWORD='Sqltop_dev_2026!' -p 11443:1433 \
  mcr.microsoft.com/mssql/server:2022-latest
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11443?database=master&encrypt=disable" \
  go test ./internal/source/mssql/ -run Sweep -v
podman rm -f sqltop-tz
```

`TestSweepLeavesAYoungRunningCaptureAlone` is the one that matters there.

## Commits

The rule lives in the machine-wide `~/.claude/CLAUDE.md`: commits carry no
attribution trailer, and the message is prose explaining why. Repeated here
only so an agent reading this file alone does not have to guess.

## Testing the interface

`go test ./internal/web` drives the real page in a real browser when
chromium and deno are both present, and skips when either is missing. That
test exists because of an asymmetry worth naming: the Go side has close to
two hundred tests and the JavaScript side had two functions reachable from
any of them, while 0.2 was mostly a release of interface. It is also the only
kind of check that finds the class of bug that has actually shipped here, a
page whose stylesheet is refused because a relative URL does not carry the
token; curl cannot find that.

It is hermetic: a fake source, no container, no network. When adding an
assertion to it, break the thing it asserts and watch it fail first. Two of
the first four assertions written passed against a deliberately broken page,
because the fixture made them true by accident.

## Before committing

`gofmt` clean and `go vet ./...` clean.

`deno lint internal/web/assets/app.js` clean. Deno rather than eslint: one
static binary, no `package.json`, no `node_modules` and no configuration
file, in a repository that otherwise has no JavaScript toolchain. The gate
also runs from `go test ./internal/web`, which finds the binary on the PATH
or in deno's default install directory and skips when it finds neither, so
a machine without it still builds and tests.

`bench/` is a local rendering harness and is deliberately not tracked in git.
It still has to build when the tree does, but only on a machine that has it.
