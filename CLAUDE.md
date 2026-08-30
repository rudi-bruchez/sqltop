# sqltop

A `top` for SQL servers. See `docs/SPECS.md` for the full specification; it is
the authority, this file only carries what must be true from the first line of
every session.

## Language

Everything is written in English: code, comments, commits, UI, documentation.
The sole exception is `docs/`, which holds captured research conversations in
their original French. They are source material and are not to be translated.

## Implementation rules

KISS and idiomatic Go. `docs/SPECS.md` section 2.1 states these as checkable
rules; read it before writing code. In short:

- Standard library first. A new dependency needs a reason stated in the commit
  that introduces it.
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
- Read-only on the monitored server. No object created, nothing configured, no
  trace flag set.
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

## Commits

The rule lives in the machine-wide `~/.claude/CLAUDE.md`: commits carry no
attribution trailer, and the message is prose explaining why. Repeated here
only so an agent reading this file alone does not have to guess.

## Before committing

`gofmt` clean and `go vet ./...` clean.

`bench/` is a local rendering harness and is deliberately not tracked in git.
It still has to build when the tree does, but only on a machine that has it.
