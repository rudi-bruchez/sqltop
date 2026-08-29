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

## Before committing

`gofmt` clean, `go vet ./...` clean, and the bench still builds.
