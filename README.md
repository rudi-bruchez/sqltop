# sqltop

A `top` for SQL servers: real-time monitoring of active requests, with a short
rolling history so a query can be reviewed after it has finished.

Status: 0.1 released, 0.2 in progress. The collector works, the request grid is
live and the server dashboard is in. The other views, the plan panel and the
kill flow are still to come, as are column sorting and filtering. The
rendering strategy for the main screen was settled by measurement against four
candidates, using a local harness that is not tracked here.

## Running it

The connection string comes from the environment, never from the configuration
file: it carries a password.

```
echo 'SQLTOP_CONN=sqlserver://user:password@server:1433?database=master' > .env
go run ./cmd/sqltop
```

It prints its version, then a line like `sqltop on http://127.0.0.1:8421/?t=...`.
Open that link as printed. The token is new on every run, so a bookmarked URL
will not work.

Options: `--config <path>` for an explicit configuration file, `--env <path>`
for a `.env` elsewhere, `--show-config` to print the resolved configuration and
exit, `--version` to print the build and exit. Without `--config` it looks
beside the binary, then in the user configuration directory, then falls back to
the defaults.

To build first, which is what a static binary is for:

```
CGO_ENABLED=0 go build -o sqltop ./cmd/sqltop
SQLTOP_CONN='...' ./sqltop
```

The grid shows user work and leaves out the engine's own background tasks, so
an idle instance shows an empty grid. That is deliberate: on a real server those
rows are most of what `sys.dm_exec_requests` returns, and they cost more to
collect than they are worth. Sessions holding an open transaction, blocking or
blocked sessions, and anything using tempdb stay visible whatever their state.

### Against a local SQL Server in Podman

```
eval "$(scripts/testdb.sh)"                  # starts the container, exports SQLTOP_TEST_DSN
SQLTOP_CONN="$SQLTOP_TEST_DSN" go run ./cmd/sqltop
```

`scripts/restoredb.sh` restores a demonstration database into that container,
and `sqlstress/` puts load on it so there is something to watch:

```
scripts/restoredb.sh
cd sqlstress && go run . -duration 2m
```

## Versions

The version is a constant in `internal/buildinfo`; the commit and the dirty
flag come from the Go toolchain, so a plain `go build` produces a binary that
can say exactly which tree it came from. It is printed at startup, by
`--version`, and in the interface header. `scripts/bump-version.sh <version>`
moves it, and deliberately does not commit or tag.

## Shape of the project

A single static Go binary, no CGO, cross-compiled in one command. It serves its
own web interface from an embedded filesystem and opens it in the local browser;
nothing is installed on the monitored server and nothing is downloaded at
runtime.

The first target is SQL Server, read-only through the DMVs. PostgreSQL and MySQL
come later, behind a source abstraction designed in from the start.

## Language

Everything in this repository is written in English: code, comments, specs,
documentation and user interface. The only exception is `docs/`, which holds
captured research conversations in their original French; they are source
material, not project documentation.

## Layout

| Path | Contents |
|---|---|
| `cmd/sqltop/` | The binary |
| `internal/` | The collector, the source layer, the wire protocol and the web server |
| `sqlstress/` | A load generator for the demonstration database, for tests and demos |
| `scripts/` | Test container, demonstration database, version bump |
| `docs/SPECS.md` | The specification, which is the authority |
| `docs/QUERIES.md` | Every query the tool sends, generated from the code by a test |
| `docs/plans/` | Implementation plans and the decisions taken while executing them |
| `docs/` | Research notes, in their original French |
