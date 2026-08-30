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
			name: "managedMarkerQuery",
			when: "at connection, inside Identify, and only when EngineEdition did not already answer",
			why:  "Asks whether the marker databases Amazon RDS and Google Cloud SQL install are present. Neither service reports itself through EngineEdition, so this is the only signal there is; DB_ID answers NULL for a database that is absent and for one the login may not see, so it is a positive detection and never a denial.",
			sql:  managedMarkerQuery,
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
			name: "sessionsQuery",
			when: "on demand, while the sessions view is open",
			why:  "Every open user session: who is connected, for how long, how long since their last request ended, and the age of their oldest open transaction. Cheap: one row per connection, and the OUTER APPLY reads views that hold one row per open transaction rather than one per lock. The durations are computed on the server's own clock, because a tool on another machine with a clock minutes out would otherwise report a transaction running for negative four minutes.",
			sql:  sessionsQuery,
		},
		{
			name: "transactionsQuery",
			when: "on demand, while the transactions view is open",
			why:  "Every open user transaction, with its age, its state and how much log it has written. A transaction spanning several databases is one row, with a count, rather than one row per database pretending to be several transactions.",
			sql:  transactionsQuery,
		},
		{
			name: "locksQuery",
			when: "on demand, while the transactions view is open, alongside transactionsQuery",
			why:  "What each session holding a transaction has locked, aggregated by database, resource type, object, mode and status. Never one row per lock: a single statement can hold millions and the question is which object, not which row of which page. This is the most expensive query in the tool after the grid, because it walks the lock manager, which is why it is on demand and never on a tier. Only OBJECT locks are named; OBJECT_NAME takes a database id so it resolves across databases without a context switch, while a page or key lock names a partition and turning that into a name means a query inside each database.",
			sql:  locksQuery,
		},
		{
			name: "logSpaceQuery",
			when: "on demand, while the transaction log view is open",
			why:  "Every database's log: size, active portion, percent used, recovery model, and what is stopping the log being reused. Read from the performance counters rather than from sys.dm_db_log_space_usage, which returns one row for the current database only and would mean a context switch per database.",
			sql:  logSpaceQuery,
		},
		{
			name: "planProgressQueryTemplate",
			when: "on demand, once a second while somebody is watching one request's plan",
			why:  "How far a running statement has got through its plan, one row per operator. Grouped by node because a parallel plan reports each node once per worker: an operator seen eight times is one operator, not eight. Needs the lightweight profiling that is on by default from SQL Server 2019 and on both Azure engines; below that it needs a trace flag this tool will not set, so the feature is absent rather than switched on behind the operator's back. The two substitutions are a session id and a request id, integers by type before they reach the query.",
			sql:  fmt.Sprintf(planProgressQueryTemplate, 51, 0),
		},
		{
			name: "livePlanQueryTemplate",
			when: "on demand, when somebody saves a plan",
			why:  "The plan of a running statement with the row counts it has produced so far, as showplan XML. Same gate as planProgressQueryTemplate. This is the artefact worth saving: an estimate that turned out wrong is only visible beside what actually happened.",
			sql:  fmt.Sprintf(livePlanQueryTemplate, 51, 0),
		},
		{
			name: "estimatedPlanQueryTemplate",
			when: "on demand, when somebody saves a plan and the server cannot produce a live one",
			why:  "The plan as the optimiser compiled it. sys.dm_exec_text_query_plan rather than sys.dm_exec_query_plan, and with the statement offsets: the offsets give the statement the request is on rather than the whole batch, and the text form returns nvarchar rather than xml, which is what stops a plan more than a hundred and twenty-eight levels deep failing outright. Those are exactly the plans somebody wants to look at.",
			sql:  fmt.Sprintf(estimatedPlanQueryTemplate, 51, 0),
		},
		{
			name: "costQuery",
			when: "every tick, on whatever tier ran last",
			why:  "The tool's own server CPU and logical reads, read from its own session. This is what the observation budget throttles against, and it is why the connection is pinned: a pooled connection would be reset between checkouts and zero these counters.",
			sql:  costQuery,
		},
	}
}

// TestEveryQueryCarriesTheHints guards two requirements that are easy to
// forget the day someone adds a query. Read uncommitted comes from the
// session, but MAXDOP 1 is per statement, and it keeps a monitoring query
// from taking parallel workers on the server it is watching.
//
// RECOMPILE used to be the other half of this rule and is now forbidden
// rather than required. It was there to keep the plan out of the cache, and
// it was measured: docs/PERFORMANCE.md carries the table, but the short
// version is 7.6 ms of server CPU per call on the grid against 0.4 ms
// without it, 18 ms against 1.5 ms on the log query, and 3 ms against
// nothing at all on the two transaction queries. Every one of those
// milliseconds was compilation, and together they were 87 % of what this
// tool cost the server it was watching.
//
// What it bought was ten fewer cached plans on a server that holds
// thousands. The cardinality argument that usually justifies it does not
// apply: these statements take no parameters, and the dynamic management
// views carry no statistics, so a fresh compile produces the same plan from
// the same fixed guesses every time. Verified by driving the grid query
// under an eight thread and then a forty-eight thread load on one cached
// plan; the CPU per call tracked the row count and nothing else.
//
// So a query carrying it now fails. The day somebody has a reason to put it
// back on one statement, this is the test to change and that is the
// measurement to redo.
func TestEveryQueryCarriesTheHints(t *testing.T) {
	for _, e := range queryCatalogue() {
		if !strings.Contains(e.sql, "OPTION (MAXDOP 1)") {
			t.Errorf("%s is missing OPTION (MAXDOP 1)", e.name)
		}
		if strings.Contains(strings.ToUpper(e.sql), "RECOMPILE") {
			t.Errorf("%s carries RECOMPILE, which was measured at 87 %% of this tool's cost on the monitored server and buys nothing these parameterless statements need; see docs/PERFORMANCE.md", e.name)
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
	// The templates are documented through the shapes they build.
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

Every statement here is read-only, carries ` + "`OPTION (MAXDOP 1)`" + `
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

// queryCallers are the methods that actually send SQL to the monitored
// server. Anything added beside them has to be added here too, which is the
// same naming-convention bargain the catalogue makes, one level down.
var queryCallers = map[string]bool{"query": true, "queryRow": true}

// TestEveryQuerySentComesFromTheCatalogue closes the hole an external review
// found in the check above: that one enforces a naming convention on
// package-level declarations, so a statement written as a literal at the
// call site, or built into a local variable from something that is not a
// catalogued query, never reaches it. The read-only rule and the query
// hints would then be guaranteed for the queries somebody remembered to
// name, which is not a guarantee.
//
// This walks the call sites instead. The second argument of every s.query
// and s.queryRow must be an identifier, never a literal, and must resolve
// to a catalogued query: either directly, or through an assignment in the
// same function whose right-hand side mentions one. That is one level of
// data flow rather than a real analysis, which is enough for this package
// and fails loudly rather than silently if the code ever gets cleverer than
// that.
func TestEveryQuerySentComesFromTheCatalogue(t *testing.T) {
	known := map[string]bool{
		// requestsQuery is a field on Source, built by buildRequestsQuery
		// from the template; the catalogue documents both of its shapes.
		"requestsQuery": true,
	}
	for _, e := range queryCatalogue() {
		name, _, _ := strings.Cut(e.name, " ")
		known[name] = true
	}
	known["requestsQueryTemplate"] = true

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !queryCallers[sel.Sel.Name] {
					return true
				}
				checked++
				where := fset.Position(call.Pos())
				switch arg := call.Args[1].(type) {
				case *ast.Ident:
					if known[arg.Name] {
						return true
					}
					if resolvesToKnown(fn.Body, arg.Name, known) {
						return true
					}
					t.Errorf("%s: %s is sent a query built from %s, which is not in queryCatalogue; it is therefore checked for neither the read-only rule nor the query hints", where, sel.Sel.Name, arg.Name)
				case *ast.SelectorExpr:
					if known[arg.Sel.Name] {
						return true
					}
					t.Errorf("%s: %s is sent %s, which is not in queryCatalogue", where, sel.Sel.Name, arg.Sel.Name)
				default:
					t.Errorf("%s: %s is sent a query that is not a named value (%T). A statement written at the call site is invisible to every check in this file", where, sel.Sel.Name, call.Args[1])
				}
				return true
			})
			return true
		})
	}
	if checked == 0 {
		t.Fatal("found no s.query or s.queryRow call sites at all; this test has stopped looking at what it claims to look at")
	}
}

// resolvesToKnown reports whether every assignment to name inside body
// mentions a catalogued query. One level of data flow: enough to accept
// `q := s.requestsQuery` and `q := fmt.Sprintf(canQueryTemplate, from)`,
// and to reject a local built from anything else.
func resolvesToKnown(body *ast.BlockStmt, name string, known map[string]bool) bool {
	assignments := 0
	ok := true
	ast.Inspect(body, func(n ast.Node) bool {
		as, is := n.(*ast.AssignStmt)
		if !is {
			return true
		}
		for i, lhs := range as.Lhs {
			id, is := lhs.(*ast.Ident)
			if !is || id.Name != name || i >= len(as.Rhs) {
				continue
			}
			assignments++
			if !mentionsKnown(as.Rhs[i], known) {
				ok = false
			}
		}
		return true
	})
	return assignments > 0 && ok
}

func mentionsKnown(e ast.Expr, known map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			if known[v.Name] {
				found = true
			}
		case *ast.SelectorExpr:
			if known[v.Sel.Name] {
				found = true
			}
		}
		return true
	})
	return found
}
