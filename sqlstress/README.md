# sqlstress

A load generator for the demonstration database, so that sqltop has something
to watch. It is a test aid, not part of the product, and it is the only thing
in this repository that writes to a server.

## Running it

```
eval "$(scripts/testdb.sh)"     # starts the container, exports SQLTOP_TEST_DSN
scripts/restoredb.sh            # restores PachadataFormation into it
cd sqlstress && go run . -duration 2m
```

The connection string comes from `SQLSTRESS_DSN`, falling back to
`SQLTOP_TEST_DSN` so that a machine already set up for the integration tests
needs nothing more. It is never read from the configuration file: it carries a
password, and the project's rule is that secrets live in the environment.

## Configuration

`sqlstress.json` holds what a given demonstration is, so it can be committed
and replayed. `-threads` and `-duration` override it for a one-off run.

| Key | Meaning |
|---|---|
| `threads` | Concurrent sessions, one pooled connection each |
| `duration` | How long to run |
| `pause` | Idle time between two calls on the same thread |
| `queries` | Directory of `.sql` files, relative to the configuration file |
| `database` | Appended to the DSN when it does not already name one |

## The workload

Every `.sql` file in `queries/` is one batch. Threads run them round robin,
each thread starting at a different point in the list. Adding a query to a
demonstration means dropping a file in the directory; nothing in the Go code
knows what the queries are.

A file whose name ends in `-solo` is run by thread zero alone. That exists for
the blocker: eight threads each holding exclusive locks for a few seconds queue
up behind one another, the run becomes one long blocking chain, and every other
query is starved. One blocker is enough to make the blocking view show
something.

The shipped set is chosen so that each column of the grid has a reason to be
non-zero somewhere: a seek that should never linger, a scan with heavy logical
reads, a sort deliberately starved of memory so it spills to tempdb, a
reporting join, a parallel hash join, a blocker and its victim, a temporary
table, and a non-sargable scan.

Nothing in it changes the database. The one statement that writes runs inside a
transaction that always rolls back.

PachadataFormation has read committed snapshot on, so readers take their row
versions from tempdb and never wait for a writer. The blocked reader asks for
the locking flavour of read committed with a hint on the statement rather than
turning the setting off, which would change a database that is not ours.

## The corrupt table

`Contact.ProspectUS_MAX` carries a corrupt page in the demonstration backup:
`DBCC CHECKDB` reports two consistency errors on page (1:14802) and any scan of
that table fails with error 824. It restores without complaint, so the failure
only appears when something reads it.

This looks deliberate, the sort of thing a training database carries so a class
has something to find, so the table is left alone rather than repaired. The
queries here use `Contact.ProspectUS_N` instead. It is worth knowing about
before a demonstration: pointing a query at that table produces an error rather
than load.
