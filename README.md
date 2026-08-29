# sqltop

A `top` for SQL servers: real-time monitoring of active requests, with a short
rolling history so a query can be reviewed after it has finished.

Status: early. The rendering strategy for the main screen was settled by
measurement against four candidates, using a local harness that is not tracked
here.

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
| `docs/plans/` | Implementation plans |
| `docs/` | Research notes and the initial specification draft |
