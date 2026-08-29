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

Two things cannot be tested locally and stay open until pointed at the real
thing: Azure SQL Database, which cannot be containerised, and Kerberos against a
real domain.

## Commits

Commits are authored by the repository owner alone. Do not add
`Co-Authored-By` or `Claude-Session` trailers, or any other attribution
footer, to commit messages. This applies to every agent working in this
repository, not only the main session.

Messages are prose explaining why, not a bullet list of what changed. No bold,
no em-dashes.

## Before committing

`gofmt` clean and `go vet ./...` clean.

`bench/` is a local rendering harness and is deliberately not tracked in git.
It still has to build when the tree does, but only on a machine that has it.
