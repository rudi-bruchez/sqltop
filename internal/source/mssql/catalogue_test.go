package mssql

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

var updateDoc = flag.Bool("update", false, "rewrite docs/QUERIES.md from the catalogue below")

// catalogueEntry is one statement this package can send to a monitored
// server, with enough context to judge it without reading the Go around it.
type catalogueEntry struct {
	name string
	when string // what makes it run
	why  string // what it is for, and anything a reader should know before changing it
	sql  string
}

// queryCatalogue is the single list of everything this package sends. It
// feeds three things: the hint check below, the completeness check that
// makes forgetting an entry impossible, and docs/QUERIES.md, which exists
// so the queries can be reviewed and improved by someone reading SQL rather
// than Go.
//
// The two dynamic queries appear as the shapes they are actually built
// into, not as their templates: a template with %s in it is not a statement
// anybody can paste into a session and run.
func queryCatalogue() []catalogueEntry {
	modern := model.Caps(model.CapRequestDOP, model.CapTempdbPerTask)
	return []catalogueEntry{
		{
			name: "spidQuery",
			when: "once, when the pinned connection is established",
			why:  "Learns the session id the tool is using, so its own rows can be told apart from everyone else's.",
			sql:  spidQuery,
		},
		{
			name: "identifyQuery",
			when: "at connection, and again after a reconnection",
			why:  "Instance name, host, edition, product version and engine edition. Most of the first row of the dashboard comes from here.",
			sql:  identifyQuery,
		},
		{
			name: "startTimeQuery",
			when: "at connection, inside Identify",
			why:  "The instance start time, which the dashboard counts up from as the uptime. A login that may not read this view keeps everything else and simply shows no uptime.",
			sql:  startTimeQuery,
		},
		{
			name: "readCommittedSnapshotQuery",
			when: "at connection, inside Identify",
			why:  "Gates the longest-running-transaction figure, which the engine only populates under read committed snapshot isolation. Asked once rather than per tick because it is a server fact, not a measurement.",
			sql:  readCommittedSnapshotQuery,
		},
		{
			name: "instanceWideViewGrantQuery",
			when: "at connection, inside the capability probe",
			why:  "Decides whether this login can see the whole instance or only itself.",
			sql:  instanceWideViewGrantQuery,
		},
		{
			name: "canQueryTemplate",
			when: "at connection, once per capability probed",
			why:  "Asks whether a given view can be read at all, by reading one row from it. %s is substituted with the view name from a fixed list in probe, never with anything a user supplies.",
			sql:  canQueryTemplate,
		},
		{
			name: "requestsQuery (full capabilities)",
			when: "every tick of the requests tier, one second by default",
			why:  "The grid. This is the hottest query in the tool by a wide margin and the first place to look at if the tool costs the monitored server too much. The tempdb OUTER APPLY sits inside a derived table on purpose: filtering on its output instead made it run once per row, measured at 752 ms against 292 ms of server CPU over twenty runs.",
			sql:  buildRequestsQuery(model.ServerInfo{MajorVersion: 15}, modern),
		},
		{
			name: "requestsQuery (no dop column, no tempdb right)",
			when: "the same tick, on a server below SQL Server 2016 or a login without the tempdb view",
			why:  "The degraded shape. Both figures become substituted literal zeros, which is why the capability travels to the browser: the grid greys those columns rather than showing the zero as a measurement.",
			sql:  buildRequestsQuery(model.ServerInfo{MajorVersion: 13}, 0),
		},
		{
			name: "countersQuery",
			when: "every tick of the counters tier, one second by default",
			why:  "The performance counters behind most of the dashboard. Built from the catalogue in counters.go, so the IN list below changes when that catalogue does. sys.dm_os_performance_counters returns roughly 1500 rows; this asks for the dozen it needs by name.",
			sql:  countersQuery,
		},
		{
			name: "osViewsQuery",
			when: "every tick of the counters tier, alongside countersQuery",
			why:  "Scheduler load and the memory clerks. Measured at 1.70 ms of server CPU per call against the test container, next to 4.17 ms for countersQuery. The memory-clerk half is the one that grows with the size of the instance.",
			sql:  osViewsQuery,
		},
		{
			name: "spaceQuery",
			when: "every tick of the space tier, five seconds by default",
			why:  "Tempdb, broken into user objects, internal objects, version store and free space. The total shown on the dashboard is the sum of the first three computed in Go, not a fifth aggregate asked of the server, so the breakdown always adds up to the total.",
			sql:  spaceQuery,
		},
		{
			name: "versionStoreQuery",
			when: "every tick of the space tier",
			why:  "The transaction version store, which is a different measurement from the version store space reserved inside tempdb above. Documented as cheap because it does not walk individual version records.",
			sql:  versionStoreQuery,
		},
		{
			name: "cpuHistoryQuery",
			when: "every tick of the CPU history tier, one minute by default",
			why:  "SQL Server CPU and system idle from the scheduler-monitor ring buffer. The engine writes one record a minute and keeps 256, so polling faster than a minute reads the same record again. SystemIdle is not populated on Linux, which is why the other-processes figure is withheld there rather than computed.",
			sql:  cpuHistoryQuery,
		},
		{
			name: "costQuery",
			when: "every tick, on whatever tier ran last",
			why:  "The tool's own server CPU and logical reads, read from its own session. This is what the observation budget throttles against, and it is why the connection is pinned: a pooled connection would be reset between checkouts and zero these counters.",
			sql:  costQuery,
		},
	}
}

// TestEveryQueryCarriesTheHints guards the three requirements that are easy
// to forget the day someone adds a query: read uncommitted comes from the
// session, but RECOMPILE keeps the plan out of the cache and MAXDOP 1 keeps
// a monitoring query from taking parallel workers on the server it is
// watching. Both are per statement, and SQL Server allows only one OPTION
// clause per query, so they have to travel together.
func TestEveryQueryCarriesTheHints(t *testing.T) {
	for _, e := range queryCatalogue() {
		if !strings.Contains(e.sql, "OPTION (RECOMPILE, MAXDOP 1)") {
			t.Errorf("%s is missing OPTION (RECOMPILE, MAXDOP 1)", e.name)
		}
		if n := strings.Count(strings.ToUpper(e.sql), "OPTION ("); n != 1 {
			t.Errorf("%s has %d OPTION clauses, SQL Server allows one", e.name, n)
		}
	}
}

// TestNoQueryWritesToTheMonitoredServer is the read-only constraint from
// CLAUDE.md, checked rather than asserted in prose. Word-boundary matching,
// not substring: "UPDATE" as a substring appears inside perfectly innocent
// identifiers, and a check that cries wolf gets deleted.
func TestNoQueryWritesToTheMonitoredServer(t *testing.T) {
	forbidden := []string{"INSERT", "UPDATE", "DELETE", "MERGE", "DROP", "CREATE", "ALTER", "TRUNCATE", "EXEC", "EXECUTE", "GRANT", "REVOKE", "DBCC", "KILL", "BACKUP", "RESTORE"}
	for _, e := range queryCatalogue() {
		// The underscore counts as a word character, or every reference to
		// sys.dm_exec_requests reports the tool executing something. That
		// false positive is not hypothetical: it fired on three of these
		// queries the first time this check ran, and a check that cries
		// wolf is a check somebody deletes.
		words := strings.FieldsFunc(strings.ToUpper(e.sql), func(r rune) bool {
			return !(r >= 'A' && r <= 'Z') && r != '_'
		})
		for _, w := range words {
			for _, bad := range forbidden {
				if w == bad {
					t.Errorf("%s contains the word %s; this tool is read-only on the monitored server (CLAUDE.md)", e.name, bad)
				}
			}
		}
	}
}

// TestQueryCatalogueCoversEveryQueryInThePackage makes forgetting an entry
// impossible, which a hand-kept list does not: the list this replaced had
// been missing readCommittedSnapshotQuery since that query was added, so
// neither the hint check nor anything else had ever looked at it.
//
// The rule it enforces is the package's own naming convention: a
// package-level identifier whose name ends in Query or QueryTemplate is a
// statement sent to a monitored server, and must appear in the catalogue.
// That convention is load-bearing from here on. A query bound to a name
// that breaks it escapes this check, which is the one hole left, and it is
// a hole someone has to dig on purpose.
func TestQueryCatalogueCoversEveryQueryInThePackage(t *testing.T) {
	inCatalogue := map[string]bool{}
	for _, e := range queryCatalogue() {
		// The dynamic entries are named after the shape they document,
		// not after the identifier, so match on the prefix.
		name, _, _ := strings.Cut(e.name, " ")
		inCatalogue[name] = true
	}
	// The two templates are documented through the shapes they build.
	inCatalogue["requestsQueryTemplate"] = true

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var missing []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, id := range vs.Names {
					n := id.Name
					if !strings.HasSuffix(n, "Query") && !strings.HasSuffix(n, "QueryTemplate") {
						continue
					}
					if !inCatalogue[n] {
						missing = append(missing, fmt.Sprintf("%s (%s)", n, f))
					}
				}
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these queries are not in queryCatalogue, so they are neither hint-checked nor documented in docs/QUERIES.md: %s", strings.Join(missing, ", "))
	}
}

// TestQueriesDocIsCurrent keeps docs/QUERIES.md generated rather than
// written, so it cannot drift from what the tool actually sends. Regenerate
// with:
//
//	go test ./internal/source/mssql -run TestQueriesDocIsCurrent -update
func TestQueriesDocIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "QUERIES.md")
	want := renderQueriesDoc()

	if *updateDoc {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; regenerate with: go test ./internal/source/mssql -run TestQueriesDocIsCurrent -update", err)
	}
	if string(got) != want {
		t.Errorf("docs/QUERIES.md no longer matches the queries in this package; regenerate with: go test ./internal/source/mssql -run TestQueriesDocIsCurrent -update")
	}
}

func renderQueriesDoc() string {
	var b strings.Builder
	b.WriteString(`# Every query sqltop sends

Generated from ` + "`internal/source/mssql`" + `. Do not edit this file: it is
rewritten from the code by

` + "```sh\ngo test ./internal/source/mssql -run TestQueriesDocIsCurrent -update\n```" + `

and a test fails when it falls out of date. To change a query, change it in
the Go source and regenerate.

Every statement here is read-only, carries ` + "`OPTION (RECOMPILE, MAXDOP 1)`" + `
and runs on a session set to read uncommitted. Those three properties are
checked by tests, not by convention: RECOMPILE keeps these plans out of the
monitored server's cache, MAXDOP 1 keeps a monitoring query from taking
parallel workers on the server it is watching, and read uncommitted keeps it
from blocking or being blocked.

Two of the queries are built rather than written, because they depend on the
server's version and on what the login may read. They appear below as the
shapes they are actually built into.

`)
	for _, e := range queryCatalogue() {
		fmt.Fprintf(&b, "## %s\n\nRuns %s.\n\n%s\n\n```sql\n%s\n```\n\n", e.name, e.when, e.why, strings.TrimSpace(e.sql))
	}
	return b.String()
}
