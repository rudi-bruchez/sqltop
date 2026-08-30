# sqltop

A `top` for SQL servers: real-time monitoring of active requests, with a short
rolling history so a query can be reviewed after it has finished.

Status: 0.4 released. The collector works; the request grid is live with
sorting, per-column filtering and columns you can hide and reorder; the server
dashboard is in; and there are views for blocking chains, open sessions, open
transactions with the objects they have locked, and every database's
transaction log; and selecting a row shows its statement or follows its plan
as it runs, and writes that plan to a file. The waits, repetitive-query,
throughput and programs views and the kill flow are still to come.

The rendering strategy for the main screen was settled by measurement against
four candidates, using a local harness that is not tracked here. So was the
decision to sort and filter in the browser rather than in the query, and the
removal of a query hint that turned out to be most of what this tool cost the
server it watches. `docs/PERFORMANCE.md` has the numbers.

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

## Configuration

`sqltop --write-config` writes a complete `sqltop.yaml` beside the binary,
with every dashboard tile and every grid column of every view listed and
switched on, so a tile or a column can be turned off without knowing its name.
`sqltop --show-config` prints what the tool actually resolved and from which
file.

The interface opens in the default browser at startup. `--no-browser` stops
that, which is what you want over SSH.

## Keys

| Key | Does |
|---|---|
| `r` `b` `u` `x` `l` | Requests, blocking, sessions, transactions, transaction logs |
| `↑` `↓` | Move the selection through the grid |
| `t` | Show the selected row's statement under the grid |
| `s` | Save the visible state to `snapshots/` beside the binary |
| `p` | Pause and resume the display |
| `f` | Step the sampling period through 1, 2, 5, 10 and 30 seconds |
| `h` | The same list, on screen |

## Versions

The version is a constant in `internal/buildinfo`; the commit and the dirty
flag come from the Go toolchain, so a plain `go build` produces a binary that
can say exactly which tree it came from. It is printed at startup, by
`--version`, and in the interface header. `scripts/bump-version.sh <version>`
moves it, and deliberately does not commit or tag.

## Licence

MIT. See `LICENSE`.

## Shape of the project

A single static Go binary, no CGO, cross-compiled in one command. It serves its
own web interface from an embedded filesystem and opens it in the local browser;
nothing is installed on the monitored server and nothing is downloaded at
runtime.

The first target is SQL Server, read-only through the DMVs. PostgreSQL and MySQL
come later, behind a source abstraction designed in from the start.

## Language

Everything in this repository is written in English: code, comments, specs,
documentation and user interface.

## Layout

| Path | Contents |
|---|---|
| `cmd/sqltop/` | The binary |
| `internal/` | The collector, the source layer, the wire protocol and the web server |
| `sqlstress/` | A load generator for the demonstration database, for tests and demos |
| `scripts/` | Test container, demonstration database, version bump |
| `docs/SPECS.md` | The specification, which is the authority |
| `docs/QUERIES.md` | Every query the tool sends, generated from the code by a test |
| `docs/PERFORMANCE.md` | What was optimised, what was measured, and what was measured and rejected |
| `docs/plans/` | Implementation plans and the decisions taken while executing them |
| `docs/IDEES.md` | Candidate features, with the reasons for and against each |
| `LICENSE` | MIT |

