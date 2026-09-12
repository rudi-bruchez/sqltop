# sqltop

A `top` for SQL servers: real-time monitoring of active requests, with a short
rolling history so a query can be reviewed after it has finished.

Status: 0.6 released. The collector works; the request grid is live with
sorting, per-column filtering and columns you can hide and reorder; the server
dashboard is in; and there are views for blocking chains, open sessions, open
transactions with the objects they have locked, and every database's
transaction log. Selecting a row shows its statement, follows its plan as it
runs, writes that plan to a file, lists what the session has been seen running
and what it has waited on, and, behind a flag, captures every statement it
sends. Started without a connection string, the tool asks for one in the
browser instead of exiting. The repetitive-query, throughput and programs
views and the kill flow are still to come.

The rendering strategy for the main screen was settled by measurement against
four candidates, using a local harness that is not tracked here. So was the
decision to sort and filter in the browser rather than in the query, and the
removal of a query hint that turned out to be most of what this tool cost the
server it watches. `docs/PERFORMANCE.md` has the numbers.

## Installing

One static binary, nothing to install on the monitored server, nothing
downloaded at runtime. Take the archive for your platform from the
[releases](https://github.com/rudi-bruchez/sqltop/releases), check it against
the published checksums, and run it:

```
sha256sum -c --ignore-missing sqltop_0.6.0_checksums.txt
tar xzf sqltop_0.6.0_linux_x64.tar.gz
./sqltop_0.6.0_linux_x64/sqltop --version
```

The archives are `linux_x64`, `linux_arm64`, `osx_x64`, `osx_arm64` and
`win_x64`, each with the binary, this README, the changelog and the licence.
Or build it yourself, which needs Go 1.27 and no C toolchain:

```
CGO_ENABLED=0 go build -o sqltop ./cmd/sqltop
```

What it can watch:

| Engine | What works |
|---|---|
| SQL Server 2019 and later, Azure SQL Managed Instance | Everything |
| Azure SQL Database | Scoped to one database, see `docs/SPECS.md` section 3.2 |
| SQL Server 2016 SP1 to 2017 | Everything except live plan progress, which needs a trace flag this tool will not set |
| SQL Server 2012 to 2016 RTM | Connects, grid and dashboard; no plan progress at all |
| Below SQL Server 2012 | Refuses to connect, and says why |

## Permissions

The login needs one server-level right, and nothing else:

| Target | Right |
|---|---|
| SQL Server 2019 and earlier | `VIEW SERVER STATE` |
| SQL Server 2022 and later | `VIEW SERVER PERFORMANCE STATE` |
| Azure SQL Managed Instance | `VIEW SERVER STATE` |
| Azure SQL Database | `VIEW DATABASE STATE`, plus membership of `##MS_ServerStateReader##` granted from `master` for the server-wide views; without it a session sees only itself |

```sql
CREATE LOGIN sqltop WITH PASSWORD = '...';
GRANT VIEW SERVER STATE TO sqltop;   -- VIEW SERVER PERFORMANCE STATE on 2022 and later
```

These are minimums rather than exact matches: `VIEW SERVER STATE` includes
`VIEW SERVER PERFORMANCE STATE`, so a login holding the former still works on
2022 and later. A missing right degrades one reading rather than stopping the
tool: the preflight asks the server what this login can actually read, the
interface greys what it cannot, and the status bar says so. The tempdb column
is the usual example, since `sys.dm_db_task_space_usage` needs a right of its
own.

The `c` key, which captures one session's statements, is the exception: it
needs `ALTER ANY EVENT SESSION` as well, and it only exists when the tool was
started with `-capture`. Neither right implies the other, so the tool checks
for both before offering the key, rather than creating a capture it could
never read.

## Running it

The connection string comes from the environment, never from the configuration
file: it carries a password.

```
echo 'SQLTOP_CONN=sqlserver://user:password@server:1433?database=master' > .env
go run ./cmd/sqltop
```

It prints its version, then a line like `sqltop on http://127.0.0.1:8420/?t=...`.
Open that link as printed. The token is new on every run, so a bookmarked URL
will not work.

Started without a connection string, sqltop serves a connect page at that
address instead: a server field that takes what SSMS takes (`db01`,
`db01\SALES`, `db01,14330`), a login method, and a box to save the result to
`.env`. By hand, the string looks like this:

| Case | Connection string |
|---|---|
| default instance | `sqlserver://user:password@db01?database=master` |
| named instance, through SQL Server Browser | `sqlserver://user:password@db01/SALES` |
| explicit port | `sqlserver://user:password@db01:14330` |
| Windows domain account (NTLM outside Windows) | `sqlserver://CORP%5Cdba:password@db01` |
| current Windows account, on Windows | `sqlserver://db01` |
| Kerberos ticket | see `docs/SPECS.md` section 3.3 |

A password containing `@`, `:`, `/`, `#` or `%` must be percent-encoded; the
connect page does it for you. To reuse the string in `sqltop.yaml`, keep it
in `.env` and write `dsn: ${SQLTOP_CONN}` there, whole: a variable standing
for the password alone is inserted unescaped.

Options: `--config <path>` for an explicit configuration file, `--env <path>`
for a `.env` elsewhere, `--show-config` to print the resolved configuration and
exit, `--version` to print the build and exit. Without `--config` it looks
beside the binary, then in the user configuration directory, then falls back to
the defaults.

From a built binary rather than the source tree, the same thing reads
`SQLTOP_CONN='...' ./sqltop`.

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
| `e` | Follow the selected request through its plan as it runs |
| `d` | Write the selected request's plan to `plans/` beside the binary |
| `y` | List what the selected session has been seen running, holding the display while it is open |
| `n` | Show what the selected session has waited on |
| `c` | Capture every statement the selected session runs, into `traces/`; only with `-capture` |
| `s` | Save the visible state to `snapshots/` beside the binary |
| `p` | Pause and resume the display, holding every panel as it stands |
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

The first target is SQL Server, read-only through the DMVs, with one exception
the operator has to ask for. Started with `-capture`, the `c` key creates a
named Extended Events session scoped to one session id, keeps it only while
somebody is watching, and removes it when they stop. Without that flag the tool
creates and drops nothing at all. PostgreSQL and MySQL come later, behind a
source abstraction designed in from the start.

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

