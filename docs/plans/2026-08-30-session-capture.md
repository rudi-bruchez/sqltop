# Per-session statement capture implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** With a row selected in the requests grid, one key captures every completed batch and RPC on that session through an Extended Events session created on demand, drains it to a JSON Lines file under `traces/`, shows it in a panel, and destroys the event session when the capture ends.

**Architecture:** Four layers. A `Capturer` optional interface beside `Source` carries engine-neutral types. `internal/source/mssql/capture.go` implements it, going through the same `s.mu` and connection-repair layer as every other query in that package. `internal/capture` owns the lifecycle: one goroutine per capture, the stop conditions, the JSON Lines file, and a mutex-guarded slice of recent statements. `internal/web` reaches it through the collector exactly as the other on-demand views do, and adds one detail-panel mode on the key `c`. Nothing anywhere runs unless `-capture` was passed.

**Tech Stack:** Go 1.27, standard library plus the existing `github.com/microsoft/go-mssqldb`. No new dependency. `encoding/xml` for the ring buffer, `encoding/json` for the file, `crypto/rand` for the session name suffix.

**Spec:** `docs/specs/2026-08-30-session-capture-design.md`. Read it before Task 1. `docs/SPECS.md` is the project authority it argues from.

**Provenance:** this is the second version. The first was reviewed by five independent readers who drove it against real 2019 and 2022 engines and found roughly twenty-five defects, several structural. Every measured fact below was measured, not recalled, and the ones that overturned a belief are marked where they appear. Their reports are the reason the task list changed shape rather than gaining patches.

## Global Constraints

- Pure Go, no CGO.
- The tool is read-only on the monitored server except through this feature, and this feature does nothing at all unless `-capture` was passed: no `CREATE`, no `ALTER`, no `DROP`, and no sweep, since the sweep is itself a `DROP`.
- Every event session is named `sqltop_capture_<spid>_<hex>`, and every `DROP` filters on that prefix. Nothing outside it is ever touched.
- `EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS`, never `NO_EVENT_LOSS`, which stalls the monitored workload.
- The ring buffer target sets `MAX_EVENTS_LIMIT = 1000` and `MAX_MEMORY = 1024` explicitly. Measured on 2019 and 2022: a target naming only `MAX_MEMORY` gets the default limit of 1000 events whatever memory it asks for, at 1024 KB and at 4096 KB alike.
- Every statement this feature sends goes through `s.query`, `s.queryRow` or `s.exec`, never through `s.db` directly. `s.db` is a pinned `*sql.Conn`, nil between a dead connection and its repair, and `database/sql` documents a `*sql.Conn` as unsafe for concurrent use. Bypassing the helpers loses the mutex, loses the repair, and escapes `TestEveryQuerySentComesFromTheCatalogue`.
- Ten minutes is the hard cap and is not configurable: the sweep uses it as evidence about other instances, so every instance must agree.
- Durations from Extended Events are microseconds and stay microseconds until display.
- English everywhere. `gofmt` clean, `go vet ./...` clean, `deno lint internal/web/assets/app.js` clean before every commit. Commits carry no attribution trailer; the message is prose explaining why.
- Comments say why, in as few words as it can be said. No archaeology, no task numbers.

## Names in the existing code, verified

Getting these wrong was the largest single category of defect in the first version. They were read out of the tree, not remembered.

| What you need | What it is actually called |
|---|---|
| mssql test source | `open(t *testing.T) *Source` in `mssql_test.go` |
| web test server | `newTestServer(t *testing.T) *Server` in `server_test.go` |
| the source's pinned connection | `s.db *sql.Conn`, with `s.pool *sql.DB` behind it |
| query helpers | `s.query`, `s.queryRow`; there is no `s.exec` yet, Task 2 adds it |
| the window on `Server` | `s.win`, and it holds only `RequestSample` |
| how a handler reaches the source | `s.col`, the collector, which delegates |
| web routes | `[]route{{path, handler}}`, no per-route token wrapper |
| Azure SQL Database constant | `model.DeploymentAzureSQLDB` |
| detail panel heading element | `#detailWho` |
| help dialog element | `#helpDialog` |
| a cell in a `CELL_` table | `{num: true, text: (r) => ...}`, not a bare function |
| the browser test | shells out to `testdata/e2e-driver.js` under deno, decodes `e2eResult`; there is no Go `page` object |

---

### Task 1: Extract the beside-the-binary output directory

`snapshots/` and `plans/` resolve a directory and write non-overwriting files inside `internal/web`. `traces/` is the third, and it needs something neither has: a file it can append to over minutes rather than one body written at once.

**Files:**
- Create: `internal/outdir/outdir.go`, `internal/outdir/outdir_test.go`
- Modify: `internal/web/commands.go`, `internal/web/commands_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `outdir.Beside(name string) (string, error)`, `outdir.Create(dir, base, ext string) (*os.File, string, error)`, `outdir.Write(dir, base, ext string, body []byte) (string, error)`.

- [ ] **Step 1: Write the failing test**

```go
package outdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	first, err := Write(dir, "server-2026-08-30-201455", ".html", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(dir, "server-2026-08-30-201455", ".html", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("the second write reused %s", first)
	}
	if got, _ := os.ReadFile(first); string(got) != "one" {
		t.Errorf("the first file now holds %q", got)
	}
	if !strings.HasSuffix(second, "-2.html") {
		t.Errorf("second file is %s, want a -2 suffix", filepath.Base(second))
	}
}

func TestCreateReturnsAnAppendableFile(t *testing.T) {
	dir := t.TempDir()
	f, path, err := Create(dir, "capture-51", ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("a\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("b\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\nb\n" {
		t.Errorf("file holds %q, want two appended lines", got)
	}
}

func TestCreateMakesTheDirectory(t *testing.T) {
	// traces/ does not exist until the first capture, and Create is what
	// must bring it into being.
	dir := filepath.Join(t.TempDir(), "traces")
	f, _, err := Create(dir, "capture-51", ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestBesideIsUnderTheExecutable(t *testing.T) {
	got, err := Beside("traces")
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if filepath.Dir(got) != filepath.Dir(exe) {
		t.Errorf("Beside returned %s, which is not beside %s", got, exe)
	}
	if filepath.Base(got) != "traces" {
		t.Errorf("Beside returned %s, want a directory named traces", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/outdir/`
Expected: FAIL, the package does not compile.

- [ ] **Step 3: Write the implementation**

```go
// Package outdir resolves the directories sqltop writes beside its own
// executable, and creates files in them without ever overwriting one.
//
// Beside the executable rather than in a home directory because spec section
// 7 puts them there: a portable install carries its own output, and a DBA who
// copied the binary to a jump box finds it next to the binary.
package outdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Beside names a directory next to the running executable. It does not
// create it; Create does, when there is something to put in it.
func Beside(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

// Create opens a new file and hands it back still open, for a caller that
// writes over time. The names have one second of resolution, so two within
// the same second would otherwise land on each other; the numeric suffix is
// not a feature, it is the alternative to losing a file somebody asked for.
func Create(dir, base, ext string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	for n := 1; n <= 9; n++ {
		name := base + ext
		if n > 1 {
			name = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return f, path, nil
	}
	return nil, "", fmt.Errorf("outdir: nine files already exist for %s", base)
}

// Write is Create for a caller that has the whole body already.
func Write(dir, base, ext string, body []byte) (string, error) {
	f, path, err := Create(dir, base, ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/outdir/`
Expected: PASS, four tests.

- [ ] **Step 5: Move the web package onto it, test file included**

In `internal/web/commands.go` delete `besideBinary` and `writeUnique`, add the import, and change the seams:

```go
var snapshotDir = func() (string, error) { return outdir.Beside("snapshots") }
var planDir = func() (string, error) { return outdir.Beside("plans") }
```

Replace each `writeUnique(dir, base, ext, body)` with `outdir.Write(dir, base, ext, body)`. Remove `path/filepath` from the imports if nothing else uses it.

`internal/web/commands_test.go` calls `writeUnique` directly at line 92 and will not compile otherwise. Change that call the same way. This file is easy to miss because `go test ./internal/web` is the only thing that reads it, and `go build ./...` stays green.

- [ ] **Step 6: Run the whole suite**

Run: `go vet ./... && go test ./... 2>&1 | tail -20`
Expected: PASS everywhere. `go vet` is what catches the test file, so run it, not just the build.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/outdir internal/web/commands.go internal/web/commands_test.go
git commit -F - <<'MSG'
Give the beside-the-binary output its own package

Snapshots and plans both resolve a directory next to the executable and both
write files that refuse to overwrite. The capture makes three, which is the
second real implementation the project's rule waits for, and it needs
something the pair could not do: a file it can append to over several minutes
rather than one body written at once. Create returns the open file and Write
is the old behaviour on top of it.
MSG
```

---

### Task 2: An exec helper on the source's connection layer

Every statement in `internal/source/mssql` goes through `s.query` or `s.queryRow`, which take `s.mu` and call `repairLocked` on failure. Neither can run a statement that returns nothing, and the capture needs three of those. Adding `s.exec` beside them is what keeps the capture inside the discipline the rest of the package already has; calling `s.db.ExecContext` directly would lose the mutex on a connection `database/sql` documents as unsafe to share, lose the repair, and escape the catalogue check.

**Files:**
- Modify: `internal/source/mssql/mssql.go`
- Modify: `internal/source/mssql/catalogue_test.go` (teach the catalogue check about `s.exec`)
- Modify: `internal/source/mssql/mssql_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `(*Source).exec(ctx context.Context, q string) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestExecTakesTheMutexAndRepairs(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	// A harmless statement that returns nothing. SET options are session
	// scoped and reset with the connection, so this changes nothing that
	// outlives the test and is not a write to the server.
	if err := s.exec(ctx, "SET LOCK_TIMEOUT 5000"); err != nil {
		t.Fatal(err)
	}

	// Concurrency: exec must serialise against query the way query does
	// against itself, since both run on one pinned connection.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.exec(ctx, "SET LOCK_TIMEOUT 5000"); err != nil {
				t.Error(err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Identify(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `eval "$(scripts/testdb.sh)" && go test -race ./internal/source/mssql/ -run TestExecTakes`
Expected: FAIL, `s.exec` is undefined.

- [ ] **Step 3: Write it, mirroring queryRow exactly**

Read `queryRow` at `internal/source/mssql/mssql.go:268` and follow it line for line: take `s.mu`, get the connection through `connLocked`, run, and hand a failure to `repairLocked` so a dead pinned connection is replaced rather than retried forever.

```go
// exec runs a statement that returns nothing, on the same terms as query and
// queryRow: one at a time on the pinned connection, with a dead connection
// handed to repairLocked rather than retried. Only the capture uses it, and
// only behind the flag; see capture.go.
func (s *Source) exec(ctx context.Context, q string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, err := s.connLocked(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, q); err != nil {
		s.repairLocked(conn, err)
		return err
	}
	return nil
}
```

- [ ] **Step 4: Extend the catalogue check to see it**

`TestEveryQuerySentComesFromTheCatalogue` walks the package for calls to `s.query` and `s.queryRow` and checks their argument is a catalogued identifier. A statement passed to `s.exec` would escape it entirely, which is precisely how a query gets sent that nobody documented. Add `"exec"` to the set of method names it inspects, beside `query` and `queryRow`.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/source/mssql/ -run 'Exec|Catalogue' -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 6: Prove the catalogue extension works**

Add a throwaway `s.exec(ctx, "SELECT 1")` somewhere in the package and run `TestEveryQuerySentComesFromTheCatalogue`. It must FAIL, naming the literal. Remove it.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/mssql.go internal/source/mssql/catalogue_test.go internal/source/mssql/mssql_test.go
git commit -F - <<'MSG'
Let this package run a statement that returns nothing, on its own terms

The capture needs three statements that return no rows, and the package had
no way to send one that goes through its own discipline. Calling ExecContext
on the pinned connection directly would have dropped three things at once: the
mutex that serialises callers on a connection database/sql documents as unsafe
to share, the repair path that replaces a connection killed by a failover, and
the catalogue check, which only ever looked at query and queryRow and would
have let an undocumented statement through unseen.
MSG
```

---

### Task 3: The model types and the capability

**Files:**
- Create: `internal/model/capture.go`, `internal/model/capture_test.go`
- Modify: `internal/model/model.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `model.CapturedStatement`, `model.CaptureProgress`, `model.CaptureNote`, `model.CaptureState`, `model.StopReason`, `model.CapCaptureSession`.

- [ ] **Step 1: Write the failing test**

```go
package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapCaptureSessionIsItsOwnBit(t *testing.T) {
	c := Caps(CapCaptureSession)
	if !c.Has(CapCaptureSession) {
		t.Error("CapCaptureSession should be present")
	}
	if c.Has(CapLivePlanProgress) || c.Has(CapSessionWaitStats) {
		t.Error("CapCaptureSession collides with an existing capability")
	}
}

func TestStopReasonsAreDistinctAndSpoken(t *testing.T) {
	all := []StopReason{
		StopByKey, StopByShutdown, StopByBrowserGone, StopBySessionGone,
		StopBySessionReused, StopByTimeCap, StopByServerLost,
	}
	seen := map[string]bool{}
	for _, r := range all {
		if r.String() == "" {
			t.Errorf("stop reason %d has no wording", int(r))
		}
		if seen[r.String()] {
			t.Errorf("two stop reasons both say %q", r.String())
		}
		seen[r.String()] = true
	}
	if StopNotStopped.String() != "" {
		t.Error("the zero value must be silent, since it is what a running capture holds")
	}
}

func TestTheStatementDoesNotOwnTheWordKind(t *testing.T) {
	// The trace file needs a discriminator per line, and the statement
	// already spends "kind" on batch versus rpc. Two JSON keys of the same
	// name in one object is not a parse error, it is worse: the decoder
	// keeps the last, so the record silently reads as a statement kind.
	b, err := json.Marshal(CapturedStatement{Kind: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"kind":"batch"`) {
		t.Fatalf("CapturedStatement no longer serialises kind: %s", b)
	}
	if strings.Contains(string(b), `"record"`) {
		t.Error("CapturedStatement claims the record discriminator; the trace writer needs that name free")
	}
}

func TestCaptureStateFieldNamesMatchWhatTheInterfaceReads(t *testing.T) {
	// app.js reads these names. A mismatch is silent in both directions:
	// the Go side compiles and the JavaScript reads undefined.
	b, _ := json.Marshal(CaptureState{})
	for _, name := range []string{
		`"available"`, `"active"`, `"session_id"`, `"started_at"`,
		`"stopped"`, `"statements"`, `"missed"`, `"dropped"`, `"unknown"`,
	} {
		if !strings.Contains(string(b), name) {
			t.Errorf("CaptureState does not serialise %s; app.js reads it", name)
		}
	}
	n, _ := json.Marshal(CaptureNote{})
	for _, name := range []string{`"session_id"`, `"since"`} {
		if !strings.Contains(string(n), name) {
			t.Errorf("CaptureNote does not serialise %s; app.js reads it", name)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/model/ -run 'Capture|StopReason|Kind'`
Expected: FAIL, undefined identifiers.

- [ ] **Step 3: Add the capability**

Append to the `Capability` const block in `internal/model/model.go`, after `CapSessionWaitStats`:

```go
	// CapCaptureSession reports whether this source can create a scoped
	// event capture on one session: the right to create an event session
	// and the right to read the DMVs that drain it, both, since neither
	// implies the other. The only capability describing something the tool
	// would write rather than read, and false unless the operator passed
	// the flag that permits writing at all.
	CapCaptureSession
```

- [ ] **Step 4: Write the types**

Note `omitempty` is absent from the fields the interface always reads. A zero that disappears is a field the JavaScript sees as undefined, and the panel's whole job is to state a number even when it is zero.

```go
package model

import "time"

// CapturedStatement is one completed batch or RPC on a watched session.
//
// Durations are microseconds because Extended Events reports microseconds,
// and a 400 microsecond batch rounded to zero milliseconds is a zero that
// lies about exactly the statement this feature exists to show.
type CapturedStatement struct {
	At            time.Time `json:"at"`
	Kind          string    `json:"kind"` // "batch" or "rpc"
	Object        string    `json:"object,omitempty"`
	Text          string    `json:"text"`
	DurationUs    int64     `json:"duration_us"`
	CPUUs         int64     `json:"cpu_us"`
	LogicalReads  int64     `json:"logical_reads"`
	PhysicalReads int64     `json:"physical_reads"`
	Writes        int64     `json:"writes"`
	RowCount      int64     `json:"row_count"`
	Result        string    `json:"result,omitempty"`
	Database      string    `json:"database,omitempty"`
	Application   string    `json:"application,omitempty"`
	User          string    `json:"user,omitempty"`
}

// CaptureProgress is what a drain reports alongside the statements. Missed
// and Dropped are different losses: Missed passed through the buffer between
// two reads, Dropped never reached the buffer. Reporting one as the other
// would hide which is happening, and they have different cures.
type CaptureProgress struct {
	Total     int64
	Missed    int64
	Dropped   int64
	Truncated bool // the read could not place events, so Missed is unknown
}

// CaptureNote is another capture running on the same instance, named by what
// it watches rather than by what it is called on the server. Event session
// names are a SQL Server concept and spec section 4.1 keeps those below the
// source seam.
type CaptureNote struct {
	SessionID int64     `json:"session_id"`
	Since     time.Time `json:"since"`
}

// StopReason is why a capture ended. Every one is shown to the user, so every
// one has wording, and the zero value is silent because it is what a running
// capture holds.
type StopReason int

const (
	StopNotStopped StopReason = iota
	StopByKey
	StopByShutdown
	StopByBrowserGone
	StopBySessionGone
	StopBySessionReused
	StopByTimeCap
	StopByServerLost
)

func (r StopReason) String() string {
	switch r {
	case StopByKey:
		return "stopped"
	case StopByShutdown:
		return "sqltop is shutting down"
	case StopByBrowserGone:
		return "the browser went away"
	case StopBySessionGone:
		return "the session ended"
	case StopBySessionReused:
		return "the connection pool handed the session to someone else"
	case StopByTimeCap:
		return "the ten minute cap"
	case StopByServerLost:
		return "the server could not be reached"
	}
	return ""
}

// CaptureState is the whole of what the panel needs, in one value.
type CaptureState struct {
	Available  bool          `json:"available"`
	Why        string        `json:"why,omitempty"`
	Active     bool          `json:"active"`
	SessionID  int64         `json:"session_id"`
	StartedAt  time.Time     `json:"started_at"`
	Stopped    string        `json:"stopped,omitempty"`
	Statements int           `json:"statements"`
	Missed     int64         `json:"missed"`
	Dropped    int64         `json:"dropped"`
	Unknown    bool          `json:"unknown"`
	File       string        `json:"file,omitempty"`
	Others     []CaptureNote `json:"others,omitempty"`
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/model/`
Expected: PASS, including the existing capability tests.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/model/capture.go internal/model/capture_test.go internal/model/model.go
git commit -F - <<'MSG'
Name what a capture produces, in engine-neutral terms

Durations stay in microseconds all the way to the renderer: Extended Events
reports microseconds, and the short statements this feature exists to show are
exactly the ones a millisecond would round to a lying zero. The two kinds of
loss are separate fields because they have different causes and different
cures, and one number covering both would hide which is happening.

Two tests exist because both mistakes are silent. The statement already spends
the word kind on batch versus rpc, so the trace file cannot also use it as its
record discriminator: two keys of one name in an object is not a parse error,
the decoder simply keeps the last. And the state's JSON names are asserted
against what the interface reads, since a mismatch compiles on one side and
reads undefined on the other.
MSG
```

---

### Task 4: The Capturer interface and the ring buffer parser

The interface, and the pure function that carries the feature: turning a ring buffer document into statements plus an exact count of what was missed.

**Files:**
- Modify: `internal/source/source.go`
- Create: `internal/source/mssql/ringbuffer.go`, `internal/source/mssql/ringbuffer_test.go`

**Interfaces:**
- Consumes: the model types from Task 3.
- Produces: `source.Capturer`, `source.CaptureHandle`, and `parseRingBuffer(doc string, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error)`.

- [ ] **Step 1: Add the interface**

In `internal/source/source.go`, below `Source`, adding `"time"` to the imports:

```go
// CaptureHandle identifies one running capture. Opaque above this package.
type CaptureHandle struct {
	Name      string
	SessionID int64
	Started   time.Time
}

// Capturer is optional and deliberately not part of Source. Spec section 4.1:
// PostgreSQL and MySQL have no equivalent of a ring buffer target, and an
// abstraction assuming one would be wrong on two engines out of three.
//
// The only interface in this tool that writes to the monitored server, and
// nothing calls it unless the operator passed the flag that permits that.
type Capturer interface {
	// CanCapture reports whether a capture is possible here, and says why
	// not when it is not. A greyed key with no explanation is the failure
	// this project has already fixed twice in the dashboard.
	CanCapture(ctx context.Context) (bool, string, error)

	// SweepCaptures drops the event sessions under this tool's prefix that
	// are dead by construction, and never one that might be alive and
	// belong to another instance of sqltop.
	SweepCaptures(ctx context.Context) (dropped int, err error)

	// RunningCaptures reports the other captures alive on this instance, so
	// a second watcher of one session knows it is doubling the cost.
	RunningCaptures(ctx context.Context) ([]model.CaptureNote, error)

	// WatchedSession answers the only question the capture manager cannot
	// answer for itself: is the session it started on still that session.
	// The login time moves when a pooled connection is reset, and ok is
	// false when the session is gone.
	WatchedSession(ctx context.Context, spid int64) (login time.Time, ok bool, err error)

	StartCapture(ctx context.Context, spid int64) (CaptureHandle, error)

	// PollCapture returns the statements past mark, and what was lost. The
	// returned Total replaces the caller's mark.
	PollCapture(ctx context.Context, h CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error)

	StopCapture(ctx context.Context, h CaptureHandle) error
}
```

`WatchedSession` is on this interface rather than answered from the retention window, and that is a correction. The window holds `RequestSample`, which carries no login time, and `Latest()` returns the tick's *running requests*, so an idle watched session is absent from it entirely: a manager asking the window would stop every capture within one tick of the session going quiet, which is the case the feature most needs to survive.

- [ ] **Step 2: Write the failing parser test**

The `result` fixture below is the real shape, taken from a 2022 engine on 30 August 2026: the type element, then the numeric value, then the text. Reading `<value>` for that field yields `"0"`, not `"OK"`, which is why the parser prefers `<text>` and why a test asserts it.

```go
package mssql

import "testing"

const ringOne = `<RingBufferTarget truncated="0" processingTime="0" totalEventsProcessed="3" eventCount="3" droppedCount="0" memoryUsed="775">
  <event name="sql_batch_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.238Z">
    <data name="duration"><type name="uint64" package="package0"></type><value>1234</value></data>
    <data name="cpu_time"><value>1000</value></data>
    <data name="logical_reads"><value>7</value></data>
    <data name="physical_reads"><value>0</value></data>
    <data name="writes"><value>0</value></data>
    <data name="row_count"><value>1</value></data>
    <data name="result"><type name="rpc_return_result" package="sqlserver"></type><value>0</value><text><![CDATA[OK]]></text></data>
    <data name="batch_text"><type name="unicode_string" package="package0"></type><value><![CDATA[SELECT 1]]></value></data>
    <action name="database_name" package="sqlserver"><value>tempdb</value></action>
    <action name="client_app_name" package="sqlserver"><value>sqlcmd</value></action>
    <action name="username" package="sqlserver"><value>sa</value></action>
  </event>
  <event name="rpc_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.239Z">
    <data name="duration"><value>50</value></data>
    <data name="object_name"><value>sp_executesql</value></data>
    <data name="statement"><value><![CDATA[SELECT @a]]></value></data>
  </event>
  <event name="sql_batch_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.240Z">
    <data name="duration"><value>9</value></data>
    <data name="batch_text"><value><![CDATA[SELECT 2]]></value></data>
  </event>
</RingBufferTarget>`

func TestParseRingBufferReadsFieldsAndActions(t *testing.T) {
	got, prog, err := parseRingBuffer(ringOne, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d statements, want 3", len(got))
	}
	if prog.Total != 3 || prog.Missed != 0 || prog.Truncated {
		t.Errorf("progress %+v, want Total 3 and nothing lost", prog)
	}
	b := got[0]
	if b.Kind != "batch" || b.Text != "SELECT 1" {
		t.Errorf("first statement is %s %q", b.Kind, b.Text)
	}
	if b.DurationUs != 1234 || b.CPUUs != 1000 || b.LogicalReads != 7 || b.RowCount != 1 {
		t.Errorf("numbers came out %+v", b)
	}
	// The field that made this test necessary: <value> is the numeric code.
	if b.Result != "OK" {
		t.Errorf("result is %q, want OK; the engine puts the code in <value> and the wording in <text>", b.Result)
	}
	if b.Database != "tempdb" || b.Application != "sqlcmd" || b.User != "sa" {
		t.Errorf("actions came out database=%q app=%q user=%q", b.Database, b.Application, b.User)
	}
	if b.At.IsZero() {
		t.Error("the timestamp did not parse")
	}
	r := got[1]
	if r.Kind != "rpc" || r.Object != "sp_executesql" || r.Text != "SELECT @a" {
		t.Errorf("rpc came out %s object=%q text=%q", r.Kind, r.Object, r.Text)
	}
}

func TestParseRingBufferEmitsOnlyWhatIsPastTheMark(t *testing.T) {
	// The buffer holds absolute indices 0, 1 and 2. A caller that has
	// consumed through index 1 must be given index 2 and nothing else.
	got, prog, err := parseRingBuffer(ringOne, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want only the one past the mark", len(got))
	}
	if got[0].Text != "SELECT 2" {
		t.Errorf("emitted %q, want the last event", got[0].Text)
	}
	if prog.Missed != 0 {
		t.Errorf("Missed is %d, want 0: nothing was lost here", prog.Missed)
	}
}

func TestParseRingBufferCountsWhatPassedThroughUnread(t *testing.T) {
	// 500 processed, 3 held, so indices 0 through 496 are gone. A caller
	// whose mark is 10 missed 487.
	x := `<RingBufferTarget truncated="0" totalEventsProcessed="500" eventCount="3">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>a</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.239Z"><data name="batch_text"><value>b</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.240Z"><data name="batch_text"><value>c</value></data></event>
	</RingBufferTarget>`
	got, prog, err := parseRingBuffer(x, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d statements, want all 3 the buffer still holds", len(got))
	}
	if prog.Missed != 487 {
		t.Errorf("Missed is %d, want 487 (oldest retained is index 497, the mark was 10)", prog.Missed)
	}
}

func TestATruncatedDocumentKeepsTheOldestAndIsStillPlaceable(t *testing.T) {
	// Measured against 2022: driving 4000 events through an unbounded ring
	// buffer gave totalEventsProcessed=4000, eventCount=4000, truncated=1,
	// and 2191 nodes in the document holding markers 0 through 2190. The
	// document keeps the OLDEST of the buffer and drops the newest, so the
	// first node sits at total-eventCount and NOT at total-len(nodes).
	//
	// Getting this backwards is not a near miss. It labels the oldest event
	// with a high index, concludes placement is impossible, emits the whole
	// document every poll, and ships duplicates while discarding what was
	// really missing.
	x := `<RingBufferTarget truncated="1" totalEventsProcessed="500" eventCount="400">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>oldest</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.239Z"><data name="batch_text"><value>next</value></data></event>
	</RingBufferTarget>`
	// The buffer holds indices 100..499. The document holds the first two of
	// those, 100 and 101. A caller at mark 101 has consumed index 100 and
	// must be handed index 101 alone.
	got, prog, err := parseRingBuffer(x, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "next" {
		t.Fatalf("got %d statements %v; placement must survive truncation", len(got), got)
	}
	if !prog.Truncated {
		t.Error("truncation must still be reported, since the tail of the buffer was not returned")
	}
	if prog.Missed != 0 {
		t.Errorf("Missed is %d; nothing was lost before the document here", prog.Missed)
	}
	if prog.Total != 500 {
		t.Errorf("Total is %d, want 500", prog.Total)
	}
}

func TestTruncationCountsTheTailItCouldNotReturn(t *testing.T) {
	// 400 held, 2 returned: 398 events are in the buffer and not in the
	// document. They are not lost forever, but this poll did not see them,
	// and the mark must not advance past them or they never will be.
	x := `<RingBufferTarget truncated="1" totalEventsProcessed="500" eventCount="400">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>a</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.239Z"><data name="batch_text"><value>b</value></data></event>
	</RingBufferTarget>`
	_, prog, err := parseRingBuffer(x, 100)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Seen != 102 {
		t.Errorf("Seen is %d, want 102: the mark may only advance to the end of what the document actually held", prog.Seen)
	}
}

func TestParseRingBufferOnAnEmptyTarget(t *testing.T) {
	got, prog, err := parseRingBuffer(`<RingBufferTarget truncated="0" totalEventsProcessed="0" eventCount="0"/>`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || prog.Total != 0 || prog.Missed != 0 {
		t.Errorf("an empty target gave %d statements and %+v", len(got), prog)
	}
}
```

- [ ] **Step 3: Run them and watch them fail**

Run: `go test ./internal/source/mssql/ -run RingBuffer -v 2>&1 | tail -20`
Expected: FAIL, `parseRingBuffer` undefined.

- [ ] **Step 4: Write the parser**

`CaptureProgress` gains a `Seen` field: with truncation the mark may only advance to the end of what the document actually contained, not to `Total`, or the tail the document could not carry would be skipped forever. Add `Seen int64` to the struct in `internal/model/capture.go` with that reason as its comment.

```go
package mssql

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// The ring buffer hands back what it holds on every read, so an event is seen
// many times and has to be placed rather than deduplicated by guesswork.
// totalEventsProcessed is cumulative for the life of the session and
// eventCount is what is held now, so the buffer holds absolute indices
// total-eventCount through total-1, in order, and the document holds a prefix
// of that range: measured on 2022, a truncated read keeps the oldest and
// drops the newest.
type ringTarget struct {
	Truncated  int       `xml:"truncated,attr"`
	Total      int64     `xml:"totalEventsProcessed,attr"`
	EventCount int64     `xml:"eventCount,attr"`
	Events     []ringEvt `xml:"event"`
}

type ringEvt struct {
	Name      string     `xml:"name,attr"`
	Timestamp string     `xml:"timestamp,attr"`
	Data      []ringData `xml:"data"`
	Actions   []ringData `xml:"action"`
}

// Text carries the readable form where the engine has one. For result the
// engine puts the numeric code in value and the wording in text, so a parser
// that reads only value reports every statement as "0".
type ringData struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value"`
	Text  string `xml:"text"`
}

func parseRingBuffer(doc string, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, model.CaptureProgress{Seen: mark}, nil
	}
	var t ringTarget
	if err := xml.Unmarshal([]byte(doc), &t); err != nil {
		return nil, model.CaptureProgress{Seen: mark}, err
	}

	prog := model.CaptureProgress{Total: t.Total}
	// Either signal means the document is not the whole buffer. The flag is
	// the server saying so; a node count below eventCount says the same and
	// is the one that shows up first.
	prog.Truncated = t.Truncated != 0 || int64(len(t.Events)) < t.EventCount

	// The first node of the document is the oldest event the buffer holds,
	// truncated or not.
	first := t.Total - t.EventCount
	if first < 0 {
		first = 0
	}
	if first > mark {
		prog.Missed = first - mark
	}
	// The mark may only advance to the end of what this document carried.
	prog.Seen = first + int64(len(t.Events))
	if prog.Seen < mark {
		prog.Seen = mark
	}

	out := make([]model.CapturedStatement, 0, len(t.Events))
	for i, e := range t.Events {
		if first+int64(i) < mark {
			continue
		}
		out = append(out, statementOf(e))
	}
	return out, prog, nil
}

func statementOf(e ringEvt) model.CapturedStatement {
	s := model.CapturedStatement{Kind: "batch"}
	if e.Name == "rpc_completed" {
		s.Kind = "rpc"
	}
	if at, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
		s.At = at
	}
	for _, d := range e.Data {
		switch d.Name {
		case "duration":
			s.DurationUs = atoi(d.Value)
		case "cpu_time":
			s.CPUUs = atoi(d.Value)
		case "logical_reads":
			s.LogicalReads = atoi(d.Value)
		case "physical_reads":
			s.PhysicalReads = atoi(d.Value)
		case "writes":
			s.Writes = atoi(d.Value)
		case "row_count":
			s.RowCount = atoi(d.Value)
		case "result":
			s.Result = pick(d.Text, d.Value)
		case "object_name":
			s.Object = pick(d.Text, d.Value)
		case "batch_text", "statement":
			s.Text = d.Value
		}
	}
	for _, a := range e.Actions {
		switch a.Name {
		case "database_name":
			s.Database = pick(a.Text, a.Value)
		case "client_app_name":
			s.Application = pick(a.Text, a.Value)
		case "username":
			s.User = pick(a.Text, a.Value)
		}
	}
	return s
}

func pick(text, value string) string {
	if text != "" {
		return text
	}
	return value
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
```

- [ ] **Step 5: Run the tests and watch them pass**

Run: `go test ./internal/source/mssql/ -run RingBuffer -v 2>&1 | tail -20`
Expected: PASS, six tests.

- [ ] **Step 6: Break each of the three claims and watch the right test fail**

Do all three; this is the project's rule from `CLAUDE.md` and it exists because assertions here have passed against deliberately broken code.

Change `first := t.Total - t.EventCount` to `t.Total - int64(len(t.Events))`: `TestATruncatedDocumentKeepsTheOldestAndIsStillPlaceable` must FAIL. Change `prog.Seen = first + int64(len(t.Events))` to `prog.Seen = t.Total`: `TestTruncationCountsTheTailItCouldNotReturn` must FAIL. Change `s.Result = pick(d.Text, d.Value)` to `d.Value`: `TestParseRingBufferReadsFieldsAndActions` must FAIL with `result is "0"`. Restore all three.

- [ ] **Step 7: Confirm the truncation claim against the engine yourself**

Do not take the fixture's word for it. Write a throwaway program under `/tmp`, create a session with `SET MAX_EVENTS_LIMIT = 0, MAX_MEMORY = 8192`, drive four thousand batches each padded to about 800 bytes and carrying a printable sequence number, read the target, and confirm the document holds the low numbers and stops short of the high ones while `eventCount` still reports the whole buffer. This is the claim the entire feature rests on and it was got backwards once already.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/source.go internal/source/mssql/ringbuffer.go internal/source/mssql/ringbuffer_test.go internal/model/capture.go
git commit -F - <<'MSG'
Place ring buffer events by index, and get the direction of truncation right

The target returns its whole contents on every read, so the same statement
arrives many times and something must decide which are new. Its own
totalEventsProcessed and eventCount attributes answer exactly: the buffer
holds a known range of absolute indices in order, so a high water mark
suffices and no timestamp heuristic is needed. The same arithmetic counts what
passed through between two reads, which is the difference between a list that
is incomplete and one that says how incomplete.

Truncation is where this was wrong, and wrong in the expensive direction.
Driving four thousand events through an unbounded buffer on 2022 shows the
document keeps the oldest and drops the newest while eventCount still reports
the whole buffer, so the first node sits at total minus eventCount and not at
total minus the node count. The earlier arithmetic labelled the oldest event
with a high index, concluded placement was impossible, and would have re-sent
the whole document on every poll: silent duplicates, with the genuinely
missing events discarded. Placement now survives truncation, and the mark
advances only to the end of what the document actually carried, so the tail it
could not fit is read next time rather than skipped.

The result field was reading as "0" everywhere. The engine puts the numeric
code in value and the wording in text, so the parser prefers text, and a test
asserts OK rather than counting rows and calling it covered.
MSG
```

---

### Task 5: The statements, and two narrow exceptions rather than one

The DDL and the queries, plus the exceptions they need in the package's own invariant tests. There are two, on different axes, and the first version of this plan only knew about one.

**Files:**
- Create: `internal/source/mssql/capture.go` (statements and builders only)
- Create: `internal/source/mssql/capture_test.go`
- Modify: `internal/source/mssql/catalogue_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `capturePrefix`, `captureCap`, `captureSessionName`, `createCaptureQueryTemplate`, `startCaptureQueryTemplate`, `stopCaptureQueryTemplate`, `sweepCaptureQuery`, `runningCapturesQuery`, `drainCaptureQuery`, `capturePermissionQuery`, `watchedSessionQuery`.

- [ ] **Step 1: Write the failing test**

```go
package mssql

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestCaptureDDLNeverStallsTheWorkload(t *testing.T) {
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if strings.Contains(strings.ToUpper(ddl), "NO_EVENT_LOSS") {
		t.Fatal("the capture DDL asks the engine to block the monitored workload when the buffer fills")
	}
	if !strings.Contains(ddl, "ALLOW_SINGLE_EVENT_LOSS") {
		t.Error("the retention mode is not stated, so it defaults rather than being chosen")
	}
}

func TestCaptureDDLStatesBothRingBufferCaps(t *testing.T) {
	// Measured on 2019 and 2022 at 1024 KB and again at 4096 KB: a target
	// naming only MAX_MEMORY holds exactly 1000 events, because the event
	// limit defaults and governs. The memory figure alone describes a
	// buffer the feature never receives.
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if !strings.Contains(ddl, "MAX_EVENTS_LIMIT = 1000") {
		t.Error("the event count cap is left implicit, so the default governs silently")
	}
	if !strings.Contains(ddl, "MAX_MEMORY = 1024") {
		t.Error("the ring buffer target memory cap is missing")
	}
	if !strings.Contains(ddl, "STARTUP_STATE = OFF") {
		t.Error("without STARTUP_STATE = OFF a leftover session returns after a server restart")
	}
}

func TestCaptureSessionNameIsPrefixedAndInert(t *testing.T) {
	ok := regexp.MustCompile(`^sqltop_capture_[0-9]+_[0-9a-f]{8}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name, err := captureSessionName(51)
		if err != nil {
			t.Fatal(err)
		}
		if !ok.MatchString(name) {
			t.Fatalf("name %q is not the prefix, an integer and hex", name)
		}
		seen[name] = true
	}
	if len(seen) < 90 {
		t.Errorf("only %d distinct names in 100; the suffix is not random enough", len(seen))
	}
}

func TestTheSweepComparesTimesOnTheSameClock(t *testing.T) {
	// The defect this test exists for shipped green on every UTC container
	// and would have destroyed a colleague's live capture on any server west
	// of Greenwich. sys.dm_xe_sessions.create_time is local server time;
	// comparing it to SYSUTCDATETIME() makes every session look hours old.
	if strings.Contains(strings.ToUpper(sweepCaptureQuery), "SYSUTCDATETIME") {
		t.Fatal("the sweep compares a local-time column to UTC; west of Greenwich it drops live captures, east of it leaves dead ones")
	}
	if !strings.Contains(strings.ToUpper(sweepCaptureQuery), "SYSDATETIME") {
		t.Error("the age comparison must be made on the server, against the same clock as create_time")
	}
	if !strings.Contains(sweepCaptureQuery, capturePrefix+"%") {
		t.Error("the sweep does not filter on the prefix, so it can see other people's event sessions")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/source/mssql/ -run Capture`
Expected: FAIL, undefined identifiers.

- [ ] **Step 3: Write the statements**

The four read-only ones carry `OPTION (MAXDOP 1)` like every other query in this package. The three DDL ones cannot: `CREATE EVENT SESSION ... OPTION (MAXDOP 1)` is `Msg 156, incorrect syntax near 'OPTION'`, verified on 2019 and 2022. That is the second exception, on a different axis from the write one, and Step 4 handles it.

```go
package mssql

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// capturePrefix names every event session this tool creates, and is what
// every DROP it issues filters on. ALTER ANY EVENT SESSION is a server-wide
// right that would also allow dropping system_health; the prefix is what
// guarantees we never do.
const capturePrefix = "sqltop_capture_"

// captureCap is how long a capture may run. Deliberately not configurable:
// the sweep uses it as evidence about captures belonging to other instances,
// and that reasoning holds only while every instance agrees. Encoding it in
// the session name is the way out if a knob is ever wanted.
const captureCap = 10 * time.Minute

// captureSessionName builds an identifier that can carry nothing but an
// integer and hex, which is what makes bracketing it safe.
func captureSessionName(spid int64) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d_%s", capturePrefix, spid, hex.EncodeToString(b[:])), nil
}

// The predicate is a literal because an event session predicate is compiled
// at creation and cannot be parameterised. The session id comes from a row of
// our own grid as an int64 and is rendered by %d.
const createCaptureQueryTemplate = `CREATE EVENT SESSION [%s] ON SERVER
ADD EVENT sqlserver.sql_batch_completed (
    ACTION (sqlserver.database_name, sqlserver.client_app_name, sqlserver.username)
    WHERE (sqlserver.session_id = %d)
),
ADD EVENT sqlserver.rpc_completed (
    ACTION (sqlserver.database_name, sqlserver.client_app_name, sqlserver.username)
    WHERE (sqlserver.session_id = %d)
)
ADD TARGET package0.ring_buffer (SET MAX_EVENTS_LIMIT = 1000, MAX_MEMORY = 1024)
WITH (
    MAX_MEMORY = 2 MB,
    EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS,
    MAX_DISPATCH_LATENCY = 2 SECONDS,
    TRACK_CAUSALITY = OFF,
    STARTUP_STATE = OFF
)`

const startCaptureQueryTemplate = `ALTER EVENT SESSION [%s] ON SERVER STATE = START`

const stopCaptureQueryTemplate = `DROP EVENT SESSION [%s] ON SERVER`

// sweepCaptureQuery names what is dead by construction and nothing else. A
// definition that is not started is a residue, because a live capture is
// always started and a stopped one has its definition dropped. A started
// session older than twice the cap belongs to nobody, because a live instance
// would have stopped it at the cap. Anything started and younger is left
// alone: it is probably another instance's, and destroying a colleague's
// capture is worse than leaving a stale one for another twenty minutes.
//
// SYSDATETIME and not SYSUTCDATETIME: create_time is local server time.
const sweepCaptureQuery = `SELECT s.name
FROM sys.server_event_sessions AS s
LEFT JOIN sys.dm_xe_sessions AS x ON x.name = s.name
WHERE s.name LIKE '` + capturePrefix + `%'
  AND (x.name IS NULL
       OR x.create_time < DATEADD(minute, -@cap, SYSDATETIME()))
OPTION (MAXDOP 1)`

const runningCapturesQuery = `SELECT x.name, x.create_time
FROM sys.dm_xe_sessions AS x
WHERE x.name LIKE '` + capturePrefix + `%'
OPTION (MAXDOP 1)`

const drainCaptureQuery = `SELECT CAST(t.target_data AS nvarchar(max)), s.dropped_event_count, s.dropped_buffer_count
FROM sys.dm_xe_sessions AS s
JOIN sys.dm_xe_session_targets AS t ON t.event_session_address = s.address
WHERE s.name = @name AND t.target_name = 'ring_buffer'
OPTION (MAXDOP 1)`

// capturePermissionQuery asks for both rights, because neither implies the
// other: a login able to create a session but not to read the DMVs would
// create a capture it could never drain.
const capturePermissionQuery = `SELECT
    HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION'),
    HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE')
OPTION (MAXDOP 1)`

// watchedSessionQuery answers whether the captured session is still the one
// the capture started on. login_time moves when a pooled connection is reset,
// which the project measured while building the sessions view; connect_time
// does not, which is why this reads login_time and not that.
const watchedSessionQuery = `SELECT s.login_time
FROM sys.dm_exec_sessions AS s
WHERE s.session_id = @spid
OPTION (MAXDOP 1)`
```

- [ ] **Step 4: Teach the three invariant tests about the two exceptions**

Three existing tests break otherwise, and only one of them was foreseen. Add `writes bool` to `catalogueEntry`, add all seven statements to `queryCatalogue()` with `writes: true` on the three DDL templates, then make each exception explicit and narrow.

`TestNoQueryWritesToTheMonitoredServer` needs two changes, not one. Skip `writes` entries, and strip single-quoted literals before splitting into words: `capturePermissionQuery` is a pure `SELECT` that trips the check on the word `ALTER` inside the string `'ALTER ANY EVENT SESSION'`. A permission name in a literal is not a statement, and stripping literals makes the check more accurate rather than weaker.

```go
	for _, e := range queryCatalogue() {
		if e.writes {
			// The capture of docs/specs/2026-08-30-session-capture-design.md
			// is the exception section 2 of the spec named and deferred.
			// TestTheWriteExceptionIsOnlyTheCapture keeps it narrow, and
			// that half is the one that matters.
			continue
		}
		// A quoted literal is data, not a statement. Without this,
		// capturePermissionQuery reports the tool altering the server
		// because it asks whether the login may.
		sql := stripLiterals(e.sql)
		words := strings.FieldsFunc(strings.ToUpper(sql), func(r rune) bool {
			return !(r >= 'A' && r <= 'Z') && r != '_'
		})
		...
	}
```

```go
// stripLiterals blanks single-quoted strings, keeping the quotes so nothing
// on either side is joined into one word. Doubled quotes inside a literal are
// an escaped quote and stay inside it.
func stripLiterals(sql string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			in = !in
			b.WriteByte(c)
			continue
		}
		if in {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
```

`TestEveryQueryCarriesTheHints` requires exactly one `OPTION (MAXDOP 1)` in every entry. The three DDL templates cannot carry one: `Msg 156, incorrect syntax near 'OPTION'`. Skip `writes` entries there too, with the reason stated, and add a test that the four read-only capture statements do carry it, so the exception cannot quietly swallow them:

```go
func TestEveryQueryCarriesTheHints(t *testing.T) {
	for _, e := range queryCatalogue() {
		if e.writes {
			// DDL takes no query hint. CREATE EVENT SESSION ... OPTION
			// (MAXDOP 1) is a syntax error on 2019 and 2022 alike, so this
			// is a property of T-SQL rather than an exemption granted.
			continue
		}
		...
	}
}
```

Then the companion that keeps both exceptions from widening:

```go
// TestTheWriteExceptionIsOnlyTheCapture stops the exceptions above from
// spreading. A statement may claim them only if it is one of these three by
// name, and only if its text is the DDL that names a bracketed identifier
// this package builds.
func TestTheWriteExceptionIsOnlyTheCapture(t *testing.T) {
	allowed := map[string]bool{
		"createCaptureQueryTemplate": true,
		"startCaptureQueryTemplate":  true,
		"stopCaptureQueryTemplate":   true,
	}
	claimed := map[string]bool{}
	for _, e := range queryCatalogue() {
		if !e.writes {
			continue
		}
		if !allowed[e.name] {
			t.Errorf("%s claims the write exception; only the capture DDL may", e.name)
			continue
		}
		if claimed[e.name] {
			t.Errorf("%s appears twice in the catalogue; a second entry under an allowed name would inherit the exception", e.name)
		}
		claimed[e.name] = true
		// Every allowed statement is an EVENT SESSION statement naming a
		// bracketed identifier and nothing else. This is deliberately not a
		// substring test for "[%s]": that would let any statement carrying
		// that fragment through, DROP DATABASE included.
		if !eventSessionDDL(e.sql) {
			t.Errorf("%s is not an EVENT SESSION statement over a bracketed name: %q", e.name, e.sql)
		}
	}
	for name := range allowed {
		if !claimed[name] {
			t.Errorf("%s is allowed to write but is not in the catalogue", name)
		}
	}
}

// eventSessionDDL matches the three shapes and nothing else.
func eventSessionDDL(sql string) bool {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?s)^CREATE EVENT SESSION \[%s\] ON SERVER\b`),
		regexp.MustCompile(`^ALTER EVENT SESSION \[%s\] ON SERVER STATE = START$`),
		regexp.MustCompile(`^DROP EVENT SESSION \[%s\] ON SERVER$`),
	} {
		if re.MatchString(sql) {
			return true
		}
	}
	return false
}

func TestTheReadOnlyCaptureQueriesStillCarryTheHint(t *testing.T) {
	want := map[string]bool{
		"sweepCaptureQuery": true, "runningCapturesQuery": true,
		"drainCaptureQuery": true, "capturePermissionQuery": true,
		"watchedSessionQuery": true,
	}
	for _, e := range queryCatalogue() {
		if want[e.name] && !strings.Contains(e.sql, "OPTION (MAXDOP 1)") {
			t.Errorf("%s is a read-only capture query and must carry the hint like every other", e.name)
		}
	}
}
```

Write the seven catalogue entries with real `when` and `why` text; they are what `docs/QUERIES.md` is generated from.

- [ ] **Step 5: Run the whole package**

Run: `go test ./internal/source/mssql/ 2>&1 | tail -20`
Expected: PASS. Run the whole package, not a filtered subset: the three tests this step repairs are exactly the ones a `-run Capture` filter hides.

- [ ] **Step 6: Prove both exceptions are narrow**

Set `writes: true` on the `spidQuery` entry: `TestTheWriteExceptionIsOnlyTheCapture` must FAIL. Add a second entry named `createCaptureQueryTemplate` whose sql is `DROP DATABASE master -- [%s]`: it must FAIL on both the duplicate and the shape. Remove the hint from `drainCaptureQuery`: `TestTheReadOnlyCaptureQueriesStillCarryTheHint` must FAIL. Restore all three.

- [ ] **Step 7: Regenerate the queries document, and check its preamble**

Run: `go test ./internal/source/mssql -run TestQueriesDocIsCurrent -update && go test ./internal/source/mssql -run TestQueriesDocIsCurrent`

`docs/QUERIES.md` opens by stating that every statement below is read-only and carries `OPTION (MAXDOP 1)`. Both halves are now false for three of them. Amend `renderQueriesDoc` so the preamble names the exception and the generated entries mark those three, then regenerate again. A generated document that lies is worse than a hand-written one, because nobody re-reads it.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/capture.go internal/source/mssql/capture_test.go internal/source/mssql/catalogue_test.go docs/QUERIES.md
git commit -F - <<'MSG'
Write the capture statements, and cut two narrow holes rather than one wide one

Three invariant tests stand between this package and a statement nobody meant
to send, and the capture breaks all three. The write check forbids the DDL,
the hint check requires an OPTION clause that DDL cannot take, and the write
check also fires on a pure SELECT that merely asks whether the login holds
ALTER ANY EVENT SESSION, because the permission name is a word inside a
literal. The last of those is fixed rather than excepted: quoted text is data,
not a statement, and blanking literals before the word scan makes the check
more accurate instead of weaker.

The other two are exceptions, on different axes, and each is bounded by a test
that fails if it widens. Only three statements by name may write, none may
appear twice under an allowed name, and each must match the exact EVENT
SESSION shape rather than merely containing a fragment: an earlier draft
admitted anything carrying the substring [%s], which would have let DROP
DATABASE master past on a comment.

The sweep compares create_time to SYSDATETIME and a test forbids
SYSUTCDATETIME by name. That column is local server time, and the UTC
comparison the first draft carried passed every test on containers that run in
UTC while dropping live captures on any server west of Greenwich.
MSG
```

---

### Task 6: The read side: permission, sweep, running captures, watched session

**Files:**
- Modify: `internal/source/mssql/capture.go`, `internal/source/mssql/capture_test.go`, `internal/source/mssql/mssql.go`

**Interfaces:**
- Consumes: Task 5's statements, Task 2's `s.exec`.
- Produces: `(*Source).CanCapture`, `.SweepCaptures`, `.RunningCaptures`, `.WatchedSession`, and the `captureAllowed` field.

- [ ] **Step 1: Write the failing tests**

`open(t)` returns a `*Source` whose `db` is one pinned `*sql.Conn`, and `Open` sets `SetMaxOpenConns(1)` on the pool behind it. A test needing a second connection must open its own `sql.DB`; asking the source's pool for one blocks until the context expires.

```go
// captureDB opens a second, independent connection for tests that need to
// drive statements on a session the Source is watching. The Source's own
// pool is capped at one connection and that one is pinned, so a second
// checkout from it never arrives.
func captureDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		t.Skip("SQLTOP_TEST_DSN is unset")
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func sessionExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sys.server_event_sessions WHERE name = @p1", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestSweepRemovesAStoppedDefinition(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9999_deadbeef"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9999, 9999))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	// Created with STARTUP_STATE = OFF and never started: a residue.
	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, db, name) {
		t.Error("the stopped definition survived the sweep")
	}
}

func TestSweepLeavesAYoungRunningCaptureAlone(t *testing.T) {
	// The property protecting a colleague's capture, and the one most
	// likely to regress.
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9998_beefcafe"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9998, 9998))
	mustExec(t, db, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sessionExists(t, db, name) {
		t.Fatal("the sweep destroyed a running capture younger than the cap; that is somebody else's work")
	}
}

func TestSweepRemovesAnOldRunningCapture(t *testing.T) {
	// Waiting twenty minutes is not a test. The threshold is a parameter, so
	// a negative one makes every running session older than it.
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9997_f00df00d"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9997, 9997))
	mustExec(t, db, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	if _, err := s.sweepOlderThan(context.Background(), -1*time.Minute); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, db, name) {
		t.Error("a running capture past the threshold survived the sweep")
	}
}

func TestRunningCapturesReportsTheSessionIdNotTheName(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	db := captureDB(t)
	name := capturePrefix + "9996_0badc0de"
	mustExec(t, db, fmt.Sprintf(createCaptureQueryTemplate, name, 9996, 9996))
	mustExec(t, db, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	notes, err := s.RunningCaptures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range notes {
		if n.SessionID == 9996 {
			found = true
			if n.Since.IsZero() {
				t.Error("the note carries no start time")
			}
		}
	}
	if !found {
		t.Errorf("RunningCaptures returned %+v, want a note for session 9996", notes)
	}
}

func TestWatchedSessionSeesALoginAndAnAbsence(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var spid int64
	if err := conn.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}

	login, ok, err := s.WatchedSession(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || login.IsZero() {
		t.Fatalf("a live session reported ok=%v login=%v", ok, login)
	}

	conn.Close()
	db.SetMaxIdleConns(0) // make the close real rather than a return to the pool
	time.Sleep(time.Second)
	if _, ok, err := s.WatchedSession(ctx, spid); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a closed session still reports present; the capture would never stop")
	}
}

func TestCaptureIsUnavailableWithoutTheFlag(t *testing.T) {
	s := open(t)
	s.captureAllowed = false
	db := captureDB(t)
	before := countSessions(t, db)

	ok, why, err := s.CanCapture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("capture is available without the flag")
	}
	if why == "" {
		t.Error("unavailable with no reason given")
	}
	// The sweep is itself a DROP and must not run either.
	if n, err := s.SweepCaptures(context.Background()); err != nil || n != 0 {
		t.Errorf("the sweep ran without the flag: %d dropped, err %v", n, err)
	}
	if got := countSessions(t, db); got != before {
		t.Errorf("event session count moved from %d to %d without the flag", before, got)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run 'Sweep|RunningCaptures|WatchedSession|CaptureIsUnavailable'`
Expected: FAIL, undefined methods.

- [ ] **Step 3: Write the methods, all of them through the helpers**

Add `captureAllowed bool` to the `Source` struct with a comment saying it comes from `-capture` and that nothing here writes while it is false.

```go
// CanCapture reports whether a capture is possible, and says why not when it
// is not. Three gates, in the order a reader would ask them.
func (s *Source) CanCapture(ctx context.Context) (bool, string, error) {
	if !s.captureAllowed {
		return false, "capture is off; start sqltop with -capture to allow it", nil
	}
	if s.info.Deployment == model.DeploymentAzureSQLDB {
		return false, "Azure SQL Database has only database-scoped event sessions, which this is not written for", nil
	}
	var ddl, view bool
	if err := s.queryRow(ctx, capturePermissionQuery, &ddl, &view); err != nil {
		return false, "", err
	}
	switch {
	case !ddl && !view:
		return false, "this login has neither ALTER ANY EVENT SESSION nor VIEW SERVER STATE", nil
	case !ddl:
		return false, "this login lacks ALTER ANY EVENT SESSION", nil
	case !view:
		return false, "this login lacks VIEW SERVER STATE, so a capture could be created but never read", nil
	}
	return true, "", nil
}

func (s *Source) SweepCaptures(ctx context.Context) (int, error) {
	return s.sweepOlderThan(ctx, 2*captureCap)
}

// sweepOlderThan is SweepCaptures with the threshold exposed, so the age rule
// can be tested without waiting twenty minutes.
func (s *Source) sweepOlderThan(ctx context.Context, age time.Duration) (int, error) {
	if !s.captureAllowed {
		return 0, nil
	}
	var names []string
	err := s.query(ctx, sweepCaptureQuery, func(rows *sql.Rows) error {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
		return nil
	}, sql.Named("cap", int(age.Minutes())))
	if err != nil {
		return 0, err
	}

	var dropped int
	for _, n := range names {
		// Belt and braces. The query filters on the prefix and so does
		// this: the right this feature holds would also allow dropping
		// system_health, and one filter is one mistake away from that.
		if !strings.HasPrefix(n, capturePrefix) {
			continue
		}
		if err := s.exec(ctx, fmt.Sprintf(stopCaptureQueryTemplate, n)); err != nil {
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}
```

`s.query` and `s.queryRow` will need to accept trailing arguments if they do not already; read their signatures at `internal/source/mssql/mssql.go:268` and `:292` and extend them with `args ...any` passed through, rather than reaching around them.

Write `RunningCaptures` and `WatchedSession` the same way. `WatchedSession` returns `ok=false` on `sql.ErrNoRows`, which is the session having ended. `spidFromCaptureName` parses the integer back out of the name so the note carries what it watches rather than what it is called.

- [ ] **Step 4: Run against both engines**

```bash
eval "$(scripts/testdb.sh)"
go test -race ./internal/source/mssql/ -run 'Sweep|RunningCaptures|WatchedSession|CaptureIsUnavailable' -v 2>&1 | tail -30
podman start sqltop-test-2019
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11439?database=master&encrypt=disable" \
  go test ./internal/source/mssql/ -run 'Sweep|RunningCaptures|WatchedSession|CaptureIsUnavailable' 2>&1 | tail -10
```
Expected: PASS on 2019 and 2022.

- [ ] **Step 5: Run the sweep tests on a server that is not in UTC**

This is the only step that would have caught the clock defect, and it is the reason this step exists.

```bash
podman run -d --name sqltop-tz -e ACCEPT_EULA=Y -e TZ=America/Los_Angeles \
  -e MSSQL_SA_PASSWORD='Sqltop_dev_2026!' -p 11443:1433 mcr.microsoft.com/mssql/server:2022-latest
# wait for it to accept connections, then:
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11443?database=master&encrypt=disable" \
  go test ./internal/source/mssql/ -run Sweep -v 2>&1 | tail -20
podman rm -f sqltop-tz
```

Expected: PASS, `TestSweepLeavesAYoungRunningCaptureAlone` included. With `SYSUTCDATETIME` in the query it fails here and passes everywhere else, which is exactly how the defect reached a plan that looked finished. Record in `CLAUDE.md` that the sweep has a timezone-sensitive test and how to run it, or the next person will not know it exists.

- [ ] **Step 6: Leave the server clean**

Run the capture tests twice in a row; the second run only passes if the first left nothing. Then confirm by hand:

```bash
podman exec sqltop-test /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P 'Sqltop_dev_2026!' -C \
  -Q "SELECT name FROM sys.server_event_sessions WHERE name LIKE 'sqltop_%'"
```
Expected: no rows.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/capture.go internal/source/mssql/capture_test.go internal/source/mssql/mssql.go
git commit -F - <<'MSG'
Recover abandoned captures without destroying live ones

An event session outlives the process that made it, so a kill leaves one
running and something must remove it. The difficulty is that several people
can watch one server, and a name cannot prove ownership across machines. So
the sweep looks not for an owner but for the two states that are dead by
construction: a definition that is not started, which a live capture never is,
and a started session older than twice the cap, which a live instance would
have stopped. Anything younger is left alone.

The test that matters is the negative one: a running capture younger than the
threshold must survive a sweep, because that property stands between this
feature and destroying a colleague's work. It is run twice, once on a UTC
container and once on a container in Los Angeles, and only the second would
have caught the clock the first draft compared against.

Whether the watched session is still the same session is asked of the server
with one small query rather than of the retention window. The window holds
only running requests and no login time at all, so a manager consulting it
would have stopped every capture within a tick of the session going quiet,
which is the case this feature most needs to survive.
MSG
```

---

### Task 7: Start, poll and stop

**Files:**
- Modify: `internal/source/mssql/capture.go`, `internal/source/mssql/capture_test.go`

**Interfaces:**
- Consumes: `parseRingBuffer` from Task 4, the statements from Task 5, `s.exec` from Task 2.
- Produces: `(*Source).StartCapture`, `.PollCapture`, `.StopCapture`, satisfying `source.Capturer`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCaptureSeesABatchAndAnRPC(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()

	// A second, independent connection: the Source's pool is capped at one
	// and that one is pinned.
	db := captureDB(t)
	watched, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer watched.Close()
	var spid int64
	if err := watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	if _, err := watched.ExecContext(ctx, "SELECT 'sqltop_capture_probe_batch'"); err != nil {
		t.Fatal(err)
	}
	// A parameterised statement reaches the server as an RPC on sp_executesql.
	var n int
	if err := watched.QueryRowContext(ctx, "SELECT @p1", 42).Scan(&n); err != nil {
		t.Fatal(err)
	}

	// MAX_DISPATCH_LATENCY is two seconds, so poll until they arrive rather
	// than sleeping once and hoping.
	var got []model.CapturedStatement
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, _, err := s.PollCapture(ctx, h, 0)
		if err != nil {
			t.Fatal(err)
		}
		got = st
		if len(got) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	var batch, rpc *model.CapturedStatement
	for i := range got {
		if strings.Contains(got[i].Text, "sqltop_capture_probe_batch") {
			batch = &got[i]
		}
		if got[i].Kind == "rpc" {
			rpc = &got[i]
		}
	}
	if batch == nil {
		t.Fatalf("the batch never arrived; got %d statements", len(got))
	}
	if batch.DurationUs <= 0 {
		t.Errorf("duration is %d microseconds, which is not a duration", batch.DurationUs)
	}
	if batch.Database == "" {
		t.Error("the database_name action did not arrive")
	}
	if batch.Result != "OK" {
		t.Errorf("result is %q, want OK; the numeric code is not the result", batch.Result)
	}
	if rpc == nil {
		t.Fatal("the parameterised statement did not arrive as an rpc")
	}
}

func TestCaptureIgnoresOtherSessions(t *testing.T) {
	// The whole cost argument for this feature rests on the predicate being
	// scoped to one session, so it gets a negative test.
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	watched, _ := db.Conn(ctx)
	defer watched.Close()
	var spid int64
	watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid)

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	other, _ := db.Conn(ctx)
	defer other.Close()
	other.ExecContext(ctx, "SELECT 'sqltop_capture_probe_other_session'")
	watched.ExecContext(ctx, "SELECT 'sqltop_capture_probe_watched'")
	time.Sleep(4 * time.Second)

	got, _, err := s.PollCapture(ctx, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range got {
		if strings.Contains(st.Text, "probe_other_session") {
			t.Fatal("the predicate is not scoped to one session")
		}
	}
}

func TestStopRemovesTheSession(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	h, err := s.StartCapture(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionExists(t, db, h.Name) {
		t.Fatal("StartCapture left no session on the server")
	}
	if err := s.StopCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, db, h.Name) {
		t.Error("the event session survived StopCapture; it would outlive the process")
	}
}

func TestStopRefusesANameThatIsNotOurs(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	err := s.StopCapture(context.Background(), source.CaptureHandle{Name: "system_health"})
	if err == nil {
		t.Fatal("StopCapture dropped a session outside the prefix; this login could drop system_health")
	}
}

func TestPollReportsMissedEventsUnderLoad(t *testing.T) {
	// The buffer holds a thousand. Driving more than that between two polls
	// must produce an exact count, not merely a noticed gap.
	s := open(t)
	s.captureAllowed = true
	ctx := context.Background()
	db := captureDB(t)
	watched, _ := db.Conn(ctx)
	defer watched.Close()
	var spid int64
	watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid)

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	for i := 0; i < 2500; i++ {
		if _, err := watched.ExecContext(ctx, fmt.Sprintf("SELECT %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(4 * time.Second)

	_, prog, err := s.PollCapture(ctx, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Missed == 0 {
		t.Fatalf("2500 statements through a 1000 event buffer reported no loss; progress %+v", prog)
	}
	if prog.Total < 2500 {
		t.Errorf("Total is %d, want at least the 2500 driven", prog.Total)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run 'CaptureSees|CaptureIgnores|StopRemoves|StopRefuses|PollReports'`
Expected: FAIL, undefined methods.

- [ ] **Step 3: Write the methods**

```go
var _ source.Capturer = (*Source)(nil)

// StartCapture creates the session and starts it, sweeping first so a crashed
// predecessor is cleaned rather than accumulated beside.
func (s *Source) StartCapture(ctx context.Context, spid int64) (source.CaptureHandle, error) {
	if !s.captureAllowed {
		return source.CaptureHandle{}, errors.New("mssql: capture is off")
	}
	if _, err := s.SweepCaptures(ctx); err != nil {
		return source.CaptureHandle{}, err
	}
	name, err := captureSessionName(spid)
	if err != nil {
		return source.CaptureHandle{}, err
	}
	if err := s.exec(ctx, fmt.Sprintf(createCaptureQueryTemplate, name, spid, spid)); err != nil {
		return source.CaptureHandle{}, err
	}
	if err := s.exec(ctx, fmt.Sprintf(startCaptureQueryTemplate, name)); err != nil {
		// A session created but not started must not be left behind. It is
		// exactly the residue the sweep exists to clean, and relying on the
		// sweep for a failure we are standing in front of is how recovery
		// paths stop being tested.
		s.exec(ctx, fmt.Sprintf(stopCaptureQueryTemplate, name))
		return source.CaptureHandle{}, err
	}
	return source.CaptureHandle{Name: name, SessionID: spid, Started: time.Now()}, nil
}

func (s *Source) PollCapture(ctx context.Context, h source.CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	var doc sql.NullString
	var droppedEvents, droppedBuffers int64
	err := s.queryRow(ctx, drainCaptureQuery, []any{&doc, &droppedEvents, &droppedBuffers},
		sql.Named("name", h.Name))
	if errors.Is(err, sql.ErrNoRows) {
		// The session is gone from under us. The caller decides what that
		// means; here it is nothing to read.
		return nil, model.CaptureProgress{Seen: mark}, nil
	}
	if err != nil {
		return nil, model.CaptureProgress{Seen: mark}, err
	}
	out, prog, err := parseRingBuffer(doc.String, mark)
	if err != nil {
		return nil, model.CaptureProgress{Seen: mark}, err
	}
	prog.Dropped = droppedEvents
	return out, prog, nil
}

func (s *Source) StopCapture(ctx context.Context, h source.CaptureHandle) error {
	if !strings.HasPrefix(h.Name, capturePrefix) {
		return fmt.Errorf("mssql: refusing to drop %q, which is not one of ours", h.Name)
	}
	return s.exec(ctx, fmt.Sprintf(stopCaptureQueryTemplate, h.Name))
}
```

Match `s.queryRow`'s real signature; the shape above is illustrative of the arguments, not of its calling convention. Read it before writing this.

- [ ] **Step 4: Run on both engines**

```bash
eval "$(scripts/testdb.sh)"
go test -race ./internal/source/mssql/ -run 'CaptureSees|CaptureIgnores|StopRemoves|StopRefuses|PollReports' -v 2>&1 | tail -30
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11439?database=master&encrypt=disable" \
  go test ./internal/source/mssql/ -run 'CaptureSees|CaptureIgnores|StopRemoves|StopRefuses|PollReports' 2>&1 | tail -10
```
Expected: PASS on both.

- [ ] **Step 5: Break the predicate and watch the negative test fail**

Change `sqlserver.session_id = %d` to `sqlserver.session_id >= 0`. `TestCaptureIgnoresOtherSessions` must FAIL. Restore it.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/capture.go internal/source/mssql/capture_test.go
git commit -F - <<'MSG'
Create, drain and destroy a capture against a real engine

Start sweeps before it creates, and a session that is created but will not
start is dropped at once rather than left for the sweep: relying on the
recovery path for a failure we are standing in front of is how recovery paths
stop being tested. Stop refuses any name outside this tool's prefix, which is
not defensive decoration: the right this feature holds would let it drop
system_health.

The tests drive a real 2019 and a real 2022 on their own connection rather
than the source's, because that one is pinned and its pool is capped at one, so
asking it for a second connection waits for a context that never comes.
MSG
```

---

### Task 8: The capture manager

The lifecycle, the drain goroutine, the file, and the mutex the panel needs. Tested with a fake `Capturer`, so none of it needs a server.

**Files:**
- Create: `internal/capture/capture.go`, `internal/capture/capture_test.go`

**Interfaces:**
- Consumes: `source.Capturer`, `source.CaptureHandle`, the model types, `outdir`.
- Produces: `capture.New(c source.Capturer) *Manager`, `(*Manager).Toggle`, `.Stop`, `.State`, `.Recent`.

The manager asks the source for the watched session's login time. It does not consult the retention window, which holds only running requests and no login time at all.

- [ ] **Step 1: Write the failing test**

The race test is the one to write carefully. The first version of this plan had one that asserted nothing twice over: its name did not match the `-run` pattern the step used, so it never ran, and even when run it could not race, because the drain ticked every ten milliseconds while the reader finished in microseconds. Overlap has to be forced.

```go
package capture

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

// fakeCapturer is the whole server in memory, so the manager's timing is
// driven rather than waited on.
type fakeCapturer struct {
	mu       sync.Mutex
	started  []string
	stopped  []string
	queue    []model.CapturedStatement
	prog     model.CaptureProgress
	login    time.Time
	present  bool
	always   bool // hand out a statement on every poll, for the race test
}

func newFake() *fakeCapturer {
	return &fakeCapturer{login: time.Now(), present: true}
}

func (f *fakeCapturer) CanCapture(context.Context) (bool, string, error) { return true, "", nil }
func (f *fakeCapturer) SweepCaptures(context.Context) (int, error)       { return 0, nil }
func (f *fakeCapturer) RunningCaptures(context.Context) ([]model.CaptureNote, error) {
	return nil, nil
}
func (f *fakeCapturer) WatchedSession(_ context.Context, _ int64) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.login, f.present, nil
}
func (f *fakeCapturer) StartCapture(_ context.Context, spid int64) (source.CaptureHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := "sqltop_capture_fake"
	f.started = append(f.started, name)
	return source.CaptureHandle{Name: name, SessionID: spid, Started: time.Now()}, nil
}
func (f *fakeCapturer) PollCapture(_ context.Context, _ source.CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.always {
		return []model.CapturedStatement{{Kind: "batch", Text: "x"}}, model.CaptureProgress{Seen: mark + 1, Total: mark + 1}, nil
	}
	out := f.queue
	f.queue = nil
	p := f.prog
	p.Seen = mark + int64(len(out))
	return out, p, nil
}
func (f *fakeCapturer) StopCapture(_ context.Context, h source.CaptureHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, h.Name)
	return nil
}
func (f *fakeCapturer) offer(s ...model.CapturedStatement) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, s...)
}

func testManager(t *testing.T) (*Manager, *fakeCapturer, string) {
	t.Helper()
	dir := t.TempDir()
	f := newFake()
	m := New(f)
	m.dir = func() (string, error) { return dir, nil }
	m.interval = 5 * time.Millisecond
	t.Cleanup(func() { m.Stop(context.Background(), model.StopByShutdown) })
	return m, f, dir
}

func TestToggleStartsAndTheSecondToggleStops(t *testing.T) {
	m, f, _ := testManager(t)
	ctx := context.Background()
	if err := m.Toggle(ctx, 51); err != nil {
		t.Fatal(err)
	}
	if st := m.State(ctx); !st.Active || st.SessionID != 51 {
		t.Fatalf("state after start is %+v", st)
	}
	if err := m.Toggle(ctx, 51); err != nil {
		t.Fatal(err)
	}
	st := m.State(ctx)
	if st.Active {
		t.Error("the second toggle did not stop the capture")
	}
	if st.Stopped == "" {
		t.Error("a stopped capture must say why")
	}
	if len(f.stopped) != 1 {
		t.Errorf("the event session was dropped %d times, want once", len(f.stopped))
	}
}

func TestTogglingAnotherSessionStopsTheFirst(t *testing.T) {
	m, f, _ := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	m.Toggle(ctx, 63)
	if st := m.State(ctx); st.SessionID != 63 || !st.Active {
		t.Fatalf("state is %+v, want an active capture on 63", st)
	}
	if len(f.stopped) != 1 || len(f.started) != 2 {
		t.Errorf("%d starts and %d stops, want two and one", len(f.started), len(f.stopped))
	}
}

func TestStatementsReachTheFileAsTheyArrive(t *testing.T) {
	m, f, dir := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	f.offer(model.CapturedStatement{Kind: "batch", Text: "SELECT 1", DurationUs: 900})
	waitFor(t, func() bool { return len(m.Recent()) == 1 })

	// Readable while the capture still runs: a process killed mid-capture
	// leaves a valid partial trace.
	path := m.State(ctx).File
	if path == "" || !strings.HasPrefix(path, dir) {
		t.Fatalf("state names file %q, want one under %s", path, dir)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) < 2 {
		t.Fatalf("file holds %d lines, want a header and an event", len(lines))
	}
	var head, ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatal(err)
	}
	if head["record"] != "header" || head["session_id"] == nil || head["version"] == nil || head["instance"] == nil {
		t.Errorf("header is %v; spec section 8 wants the tool version, the instance and the session", head)
	}
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
		t.Fatal(err)
	}
	// record, not kind: the statement already spends kind on batch versus
	// rpc, and two keys of one name leave the decoder keeping the last.
	if ev["record"] != "event" {
		t.Errorf("second line is %v, want a record of kind event", ev)
	}
	if ev["kind"] != "batch" || ev["text"] != "SELECT 1" {
		t.Errorf("the statement did not survive the record wrapper: %v", ev)
	}
}

func TestALossIsWrittenAsAGapRecord(t *testing.T) {
	m, f, _ := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	f.mu.Lock()
	f.prog = model.CaptureProgress{Total: 500, Missed: 487}
	f.mu.Unlock()
	f.offer(model.CapturedStatement{Kind: "batch", Text: "SELECT 1"})
	waitFor(t, func() bool { return m.State(ctx).Missed == 487 })

	body, _ := os.ReadFile(m.State(ctx).File)
	if !strings.Contains(string(body), `"record":"gap"`) || !strings.Contains(string(body), `"lost":487`) {
		t.Error("487 events were lost and the file does not say so with the count")
	}
}

func TestTheEndRecordNamesTheReason(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	path := m.State(ctx).File
	m.Stop(ctx, model.StopByTimeCap)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"record":"end"`) || !strings.Contains(last, "ten minute cap") {
		t.Errorf("last record is %s, want an end record naming the cap", last)
	}
}

func TestTheTimeCapStopsTheCapture(t *testing.T) {
	m, _, _ := testManager(t)
	m.cap = 30 * time.Millisecond
	ctx := context.Background()
	m.Toggle(ctx, 51)
	waitFor(t, func() bool { return !m.State(ctx).Active })
	if got := m.State(ctx).Stopped; !strings.Contains(got, "cap") {
		t.Errorf("stopped because %q, want the cap", got)
	}
}

func TestASessionHandedToSomeoneElseStopsTheCapture(t *testing.T) {
	m, f, _ := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	waitFor(t, func() bool { return m.State(ctx).Active })
	f.mu.Lock()
	f.login = f.login.Add(time.Second) // the pool reset the connection
	f.mu.Unlock()
	waitFor(t, func() bool { return !m.State(ctx).Active })
	if got := m.State(ctx).Stopped; !strings.Contains(got, "pool") {
		t.Errorf("stopped because %q, want the pooled reuse", got)
	}
}

func TestASessionThatEndedStopsTheCapture(t *testing.T) {
	m, f, _ := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	waitFor(t, func() bool { return m.State(ctx).Active })
	f.mu.Lock()
	f.present = false
	f.mu.Unlock()
	waitFor(t, func() bool { return !m.State(ctx).Active })
}

func TestRecentAndStateAreSafeWhileTheDrainWrites(t *testing.T) {
	// Under -race. Overlap is forced rather than hoped for: the drain
	// interval is one microsecond and the fake hands out a statement on
	// every poll, so the appending goroutine is always inside the slice
	// while the readers are. With m.interval left at milliseconds this test
	// passes with the mutex removed, which is the trap the first version of
	// this plan fell into.
	dir := t.TempDir()
	f := newFake()
	f.always = true
	m := New(f)
	m.dir = func() (string, error) { return dir, nil }
	m.interval = time.Microsecond
	defer m.Stop(context.Background(), model.StopByShutdown)

	if err := m.Toggle(context.Background(), 51); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := time.Now().Add(300 * time.Millisecond)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				_ = m.Recent()
				_ = m.State(context.Background())
			}
		}()
	}
	wg.Wait()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/capture/`
Expected: FAIL, the package does not exist.

- [ ] **Step 3: Write the manager**

The header needs the tool version and the instance, which spec section 8 requires and the first version omitted, so `New` takes them: `New(c source.Capturer, version, instance string) *Manager`.

Key points to get right, each of which was a defect once:

The record discriminator is `record`, not `kind`. `CapturedStatement` already serialises `kind` as batch or rpc, and a flat object with two `kind` keys leaves a decoder holding the statement's value. Write an event line by marshalling a struct that embeds the statement:

```go
type eventRecord struct {
	Record string `json:"record"`
	model.CapturedStatement
}
```

An embedded struct flattens into the same object, so the line stays one self-describing record with no nesting and no collision.

`Stop` must be safe called twice and safe called from the drain goroutine. Take `m.mu`, take `m.run` and nil it, release, then cancel and wait on `r.done`. Waiting while holding `m.mu` would deadlock against a `State` call the drain is blocked behind. The drain calls `m.Stop` from a fresh goroutine and returns immediately, so it never waits on its own `done`.

`Seen`, not `Total`, advances the mark: `r.mark = prog.Seen`. Advancing to `Total` when a read was truncated skips the tail the document could not carry, permanently.

The drain runs whether or not the panel is open. A capture nobody drains fills its buffer and loses events in silence.

`State` answers when nothing is running, returning the last capture's outcome, because "ended because the session was reused" is what a reader needs and an empty panel is not.

- [ ] **Step 4: Run with the race detector**

Run: `go test -race ./internal/capture/ -v 2>&1 | tail -30`
Expected: PASS, ten tests, no race.

- [ ] **Step 5: Prove the race test can fail**

Remove the mutex from `Recent` and run `go test -race ./internal/capture/ -run TestRecentAndStateAreSafeWhileTheDrainWrites`. It must report a data race. If it does not, the overlap is still not being forced and the test is worthless: raise the goroutine count or lengthen the window until it does, then restore the mutex. Do not move on from a race test that cannot fail.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/capture
git commit -F - <<'MSG'
Own the life of a capture, including the ways it ends badly

The drain runs whether or not the panel is open: a capture nobody reads fills
its buffer and loses events in silence, so draining cannot depend on somebody
looking. It writes each statement as it arrives rather than at the end, so a
process killed mid-capture leaves a valid partial trace whose last record says
it was not a clean end.

Each line of that file carries a record field rather than a kind field. The
statement already spends kind on batch versus rpc, and a flat object with two
keys of one name is not a parse error but something worse: the decoder keeps
the last, so every record would have read as a statement kind.

The mark advances to what the last read actually contained and not to the
session's total, or a truncated read would skip the tail it could not carry
and never come back for it.

The statements the panel reads are copied under a mutex held only across the
copy. The test for that forces overlap rather than hoping for it, with the
drain ticking every microsecond and always producing: at millisecond intervals
it passed with the mutex removed, which is precisely the assertion this
repository has been caught writing before.
MSG
```

---

### Task 9: The collector delegation and the endpoint

Handlers reach the source through `s.col`, never directly. The collector is where the capture joins the tool.

**Files:**
- Modify: `internal/collector/collector.go`, `internal/web/server.go`, `internal/web/views.go`
- Create: `internal/web/capture_test.go`
- Modify: `internal/source/fake/fake.go`

**Interfaces:**
- Consumes: `capture.Manager` from Task 8.
- Produces: `(*Collector).Captures() *capture.Manager` (nil when the source cannot capture), `GET`/`POST /api/capture`.

- [ ] **Step 1: Write the failing test**

`newTestServer(t)` is the helper; routes carry no per-route token wrapper, so build requests the way the existing view tests do. Read one of them first.

```go
func TestCaptureEndpointTogglesAndReports(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, apiRequest(t, s, "POST", "/api/capture?spid=51"))
	if rw.Code != 200 {
		t.Fatalf("POST returned %d: %s", rw.Code, rw.Body)
	}

	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, apiRequest(t, s, "GET", "/api/capture"))
	var got struct {
		State model.CaptureState        `json:"state"`
		Rows  []model.CapturedStatement `json:"rows"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.State.Active || got.State.SessionID != 51 {
		t.Errorf("state is %+v, want an active capture on 51", got.State)
	}
}

func TestCaptureEndpointRefusesAMissingSpid(t *testing.T) {
	s := newTestServer(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, apiRequest(t, s, "POST", "/api/capture"))
	if rw.Code != 400 {
		t.Errorf("POST with no spid returned %d, want 400", rw.Code)
	}
}

func TestCaptureEndpointSaysWhyWhenUnavailable(t *testing.T) {
	// A fake source cannot capture. The panel must be able to say so, which
	// means a 200 with a reason and not a 500.
	s := newTestServerWithoutCapture(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, apiRequest(t, s, "GET", "/api/capture"))
	if rw.Code != 200 {
		t.Fatalf("GET returned %d", rw.Code)
	}
	var got struct {
		State model.CaptureState `json:"state"`
	}
	json.Unmarshal(rw.Body.Bytes(), &got)
	if got.State.Available {
		t.Fatal("a source with no Capturer reported capture available")
	}
	if got.State.Why == "" {
		t.Error("unavailable with no reason; a greyed key with no explanation is the failure this project has fixed twice")
	}
}

func TestOtherCapturesReachTheState(t *testing.T) {
	// Others is what warns a second watcher it is doubling the dispatch
	// cost on the monitored workload, and nothing else will tell them.
	s := newTestServer(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, apiRequest(t, s, "GET", "/api/capture"))
	var got struct {
		State model.CaptureState `json:"state"`
	}
	json.Unmarshal(rw.Body.Bytes(), &got)
	if got.State.Others == nil {
		t.Error("the handler never asks RunningCaptures, so the panel can never warn about a second watcher")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/web/ -run Capture`
Expected: FAIL, 404.

- [ ] **Step 3: Delegate through the collector**

`Collector` holds `src source.Source`. Add, beside `Sessions` and the others:

```go
// Captures is the capture manager, or nil when this source cannot capture at
// all. Built once in New rather than type-asserted per request, so the answer
// cannot change under a handler.
func (c *Collector) Captures() *capture.Manager { return c.captures }
```

Build it in the collector's constructor when the source implements `source.Capturer`, passing the version and the instance name the header record needs.

- [ ] **Step 4: Write the handler and register it**

In `internal/web/views.go`, following the shape of `sessions`:

```go
// capture is the c command: POST toggles a capture on one session, GET
// reports what it has seen. A source that cannot capture answers with a
// reason rather than an error, because the panel has to be able to say why
// the key did nothing.
func (s *Server) capture(rw http.ResponseWriter, req *http.Request) {
	m := s.col.Captures()
	if m == nil {
		writeJSON(rw, map[string]any{
			"state": model.CaptureState{Why: "this source cannot capture"},
			"rows":  []model.CapturedStatement{},
		})
		return
	}
	ctx := req.Context()
	if req.Method == http.MethodPost {
		spid, err := strconv.ParseInt(req.URL.Query().Get("spid"), 10, 64)
		if err != nil || spid <= 0 {
			http.Error(rw, "a session id is required", http.StatusBadRequest)
			return
		}
		if err := m.Toggle(ctx, spid); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
	}
	rows := m.Recent()
	if rows == nil {
		rows = []model.CapturedStatement{}
	}
	writeJSON(rw, map[string]any{"state": m.State(ctx), "rows": rows})
}
```

`m.State` is what populates `Others`, by asking `RunningCaptures`; without that call the field is always nil and the warning never appears.

Add the route to `routes()` in the existing positional form:

```go
		{"/api/capture", http.HandlerFunc(s.capture)},
```

- [ ] **Step 5: Run and commit**

```bash
go vet ./... && go test ./internal/web/ -run Capture -v 2>&1 | tail -20
gofmt -l . && git add internal/collector/collector.go internal/web/views.go internal/web/server.go internal/web/capture_test.go internal/source/fake/fake.go
git commit -F - <<'MSG'
Reach the capture the way every other view reaches the source

Through the collector, which is where the handlers already go and which
decides once whether this source can capture at all rather than type-asserting
per request. A source that cannot answers with a reason and an empty list, not
a 500: the panel's job is to say why the key did nothing, and turning a
missing permission into a bug report defeats it.

The state carries the other captures running on the instance, because nothing
else will tell a second watcher of one session that it is doubling the
dispatch cost on the workload being watched.
MSG
```

---

### Task 10: The capture view in the column catalogue, and its cell table

These two ship together. A catalogue view with no `CELL_` table fails `internal/web`'s own consistency test, so splitting them across tasks would commit a red suite.

**Files:**
- Modify: `internal/model/columns.go`, `internal/model/columns_test.go`
- Modify: `internal/web/assets/app.js`

**Interfaces:**
- Consumes: nothing.
- Produces: a `ViewDef` with `ID: "capture"` and no `Key`, and `CELL_CAPTURE` registered in `CELLS`.

- [ ] **Step 1: Write the failing test**

```go
func TestCaptureViewIsInTheCatalogue(t *testing.T) {
	v, ok := ViewByID("capture")
	if !ok {
		t.Fatal("the capture view is not in the catalogue, so its columns cannot be configured like every other view")
	}
	if v.Key != "" {
		t.Errorf("the capture view claims tab key %q; it is a detail panel and its key lives in the interface", v.Key)
	}
	want := []string{"at", "kind", "database", "duration_ms", "cpu_ms", "logical_reads", "writes", "rows", "result", "text"}
	got := map[string]bool{}
	for _, c := range v.Columns {
		got[c.Field] = true
		if c.Width <= 0 {
			t.Errorf("column %s has no width floor", c.Field)
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("the capture view has no %s column", w)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/model/ -run CaptureView`
Expected: FAIL.

- [ ] **Step 3: Add the view**

After the `sessionwaits` entry in `ViewCatalogue`:

```go
	{ID: "capture", Title: "captured statements", Columns: []Column{
		{Field: "at", Title: "time", Width: 92, Default: true},
		{Field: "kind", Title: "kind", Width: 52, Default: true},
		{Field: "database", Title: "database", Width: 110, Default: true},
		// Decimals, because a 400 microsecond batch is exactly what somebody
		// opens this panel to find and a whole millisecond would round it to
		// a zero that lies.
		{Field: "duration_ms", Title: "ms", Width: 74, Default: true},
		{Field: "cpu_ms", Title: "cpu ms", Width: 74, Default: true},
		{Field: "logical_reads", Title: "reads", Width: 80, Default: true},
		{Field: "writes", Title: "writes", Width: 70, Default: false},
		{Field: "rows", Title: "rows", Width: 70, Default: true},
		{Field: "result", Title: "result", Width: 70, Default: false},
		{Field: "object", Title: "object", Width: 130, Default: false},
		{Field: "application", Title: "program", Width: 130, Default: false},
		{Field: "user", Title: "login", Width: 110, Default: false},
		{Field: "text", Title: "statement", Width: 400, Default: true},
	}},
```

- [ ] **Step 4: Add the cell table, in the shape the renderer actually uses**

A cell is an object, `{num: true, text: (r) => ...}`, not a bare function. `columnsFor` merges it with `Object.assign`, which copies nothing from a function, and `listTable` then calls `c.text(r)` on undefined. A bare-function table leaves the catalogue test green and throws on first render. Copy the shape from `CELL_SESSIONWAITS` above it.

```js
const CELL_CAPTURE = {
  at: { text: (r) => fClockMs(r.at) },
  kind: { text: (r) => r.kind },
  database: { text: (r) => r.database || "" },
  // Microseconds in, milliseconds out, with decimals kept.
  duration_ms: { num: true, text: (r) => (r.duration_us / 1000).toFixed(2) },
  cpu_ms: { num: true, text: (r) => (r.cpu_us / 1000).toFixed(2) },
  logical_reads: { num: true, text: (r) => n0(r.logical_reads) },
  writes: { num: true, text: (r) => n0(r.writes) },
  rows: { num: true, text: (r) => n0(r.row_count) },
  result: { text: (r) => r.result || "" },
  object: { text: (r) => r.object || "" },
  application: { text: (r) => r.application || "" },
  user: { text: (r) => r.user || "" },
  text: { text: (r) => r.text || "" },
};
```

Register `capture: CELL_CAPTURE` in `CELLS`. Write `fClockMs` beside the other formatters if no equivalent exists; `fDur` takes seconds and is not it. Check the file for what is already there before adding a formatter.

- [ ] **Step 5: Run everything**

Run: `go test ./internal/model/ ./internal/web/ 2>&1 | tail -20 && deno lint internal/web/assets/app.js`
Expected: PASS and clean, with the catalogue-to-cell-table consistency test satisfied by both halves landing together.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/model/columns.go internal/model/columns_test.go internal/web/assets/app.js
git commit -F - <<'MSG'
Put the captured statements in the catalogue, with the table that draws them

Both halves in one commit because the interface has a test that every
catalogue view has a cell table, so landing them apart would commit a red
suite. The cells are objects rather than bare functions: columnsFor merges
them with Object.assign, which copies nothing from a function, and the row
renderer then calls text on undefined. A table of bare functions leaves every
Go test green and throws the moment the panel is opened.

The millisecond columns keep two decimals. A four hundred microsecond batch is
exactly the statement somebody opens this panel to find.
MSG
```

---

### Task 11: The panel and the key

**Files:**
- Modify: `internal/web/assets/app.js`
- Modify: `internal/web/testdata/e2e-driver.js` and `internal/web/e2e_test.go`

**Interfaces:**
- Consumes: `/api/capture` from Task 9, `CELL_CAPTURE` from Task 10.
- Produces: `DETAIL_SOURCE.capture`, `KEYS.c`, a `COMMANDS` row.

The browser test is not Go. `internal/web/e2e_test.go` shells out to `deno run testdata/e2e-driver.js`, which drives Chromium over the DevTools protocol and prints a JSON blob the Go side decodes into `e2eResult`. Adding an assertion means adding a measurement to the driver script and a field to that struct. Read both before writing anything, and read the `setup-region` markers note in `CLAUDE.md`: `app_assets_test.go` parses those markers verbatim.

- [ ] **Step 1: Add the detail source and the key**

In `DETAIL_SOURCE`, beside `history` and `sessionwaits`:

```js
  capture: {
    path: "/api/capture",
    view: "capture",
    needsRequest: true,
    heading: (spid, rows, j) => captureHead(j.state, rows.length),
  },
```

```js
// captureHead states the situation in one line and never leaves the reader
// guessing. An empty table with no explanation reads as a broken feature.
function captureHead(st, n) {
  if (!st) return "capture";
  if (!st.available) return "capture unavailable: " + (st.why || "not supported here");
  if (!st.active) {
    return st.stopped
      ? "capture ended, " + st.stopped + ", " + n + " statement" + (n === 1 ? "" : "s")
      : "press c on a row to capture that session's statements";
  }
  const secs = (Date.now() - Date.parse(st.started_at)) / 1000;
  let head = "capturing spid " + st.session_id + " for " + fDur(secs)
    + ", " + n + " statement" + (n === 1 ? "" : "s");
  if (n === 0) head += " so far; the session is idle";
  // Two different losses, never collapsed into one number.
  if (st.missed) head += ", " + st.missed + " missed between reads";
  if (st.unknown) head += ", and an uncounted gap";
  if (st.dropped) head += ", " + st.dropped + " dropped by the server";
  const others = (st.others || []).filter((o) => o.session_id !== st.session_id);
  if (others.length) {
    head += " (also captured here: " + others.map((o) => "spid " + o.session_id).join(", ") + ")";
  }
  return head;
}
```

The field names are `st.session_id`, `st.started_at`, `st.unknown`, `o.session_id`. They match the JSON tags asserted in Task 3, and that assertion exists because a mismatch here compiles on the Go side and reads undefined on this one.

The key, following `savePlan`'s shape for finding the selected row:

```js
// toggleCapture asks the server to start or stop and then opens the panel.
// The server owns the decision: c on another row replaces the capture rather
// than adding one.
function toggleCapture() {
  if (!isGrid(activeView)) {
    say("a capture belongs to a session; the requests and blocking views are where they are");
    return;
  }
  const r = selectedKey === null ? null : view.find((x) => rowKey(x) === selectedKey);
  if (!r) {
    say("select a row first: the session captured is the selected one");
    return;
  }
  post("/api/capture?spid=" + val(r, "spid"), "", "application/json")
    .then(() => {
      if (detailMode !== "capture") setDetail("capture");
      pollDetail();
    })
    .catch((e) => say("could not change the capture: " + e.message));
}
```

The `catch` is not decoration. Without it a refused start is a silent no-op, in the one feature built to always say why.

Add `c: toggleCapture` to `KEYS`, and to `COMMANDS`:

```js
  ["c", "capture every statement the selected session runs, into traces/", "capture"],
```

- [ ] **Step 2: Add the measurements to the driver**

In `testdata/e2e-driver.js`, following how an existing view assertion is taken, add a step that selects a row, sends `c`, waits for `#detail` to be visible, and reports: the text of `#detailWho`, the width of the first `#detailList` header cell, the height of `#detailList`, and whether pressing `h` shows the word `capture` inside `#helpDialog`. Add the matching fields to `e2eResult` in `e2e_test.go` and assert them:

```go
	if got.CaptureHead == "" {
		t.Error("the capture panel opened with no header; a panel that explains nothing is the failure this feature is built around")
	}
	if got.CaptureFirstColWidth < 40 {
		t.Errorf("the first capture column is %g px wide; the table did not lay out", got.CaptureFirstColWidth)
	}
	if got.CaptureListHeight < 20 {
		t.Errorf("the capture table is %g px tall", got.CaptureListHeight)
	}
	if !got.HelpMentionsCapture {
		t.Error("c is bound but absent from the help; a key nobody can discover is a key nobody uses")
	}
```

The fixture uses `fake.New`, which has no capture methods, so the panel will render the unavailable state. That is the right thing to assert: the header must be non-empty and the table must lay out even when nothing can be captured, which is the state most users will meet first.

- [ ] **Step 3: Run the browser test**

Run: `deno lint internal/web/assets/app.js && go test ./internal/web/ -run E2E -v 2>&1 | tail -20`
Expected: PASS, or SKIP if chromium or deno is missing. A skip proves nothing; run this where both are installed.

- [ ] **Step 4: Break each assertion and watch it fail**

Make `captureHead` return the empty string: the header assertion must FAIL. Remove the `COMMANDS` row: the help assertion must FAIL. Change `CELL_CAPTURE` to bare arrow functions: the width assertion must FAIL, which is the check that the cell-table shape is right. Restore all three. `CLAUDE.md` requires this and the reason is on the record: two of the first four assertions written for this test passed against a deliberately broken page.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./... && deno lint internal/web/assets/app.js
git add internal/web/assets/app.js internal/web/testdata/e2e-driver.js internal/web/e2e_test.go
git commit -F - <<'MSG'
Show the capture, including what it did not see

The header is most of this. It names the session and the elapsed time, says
the session is idle when the count is zero rather than showing an empty table
that reads as broken, and keeps the two kinds of loss apart wherever they
appear: what passed through the buffer between reads is a different problem
from what the server dropped before it, with a different cure.

The assertions go through the deno driver, which is how this project's browser
test actually works, and each was verified by breaking what it asserts. The
column width one earns its place twice over: it is also what catches a cell
table written in the wrong shape, which every Go test happily accepts and
which throws the first time somebody opens the panel.
MSG
```

---

### Task 12: The three document corrections

The design ships with an amendment to the project's own specification. Shipping the code without it leaves the specification saying one thing and the program doing another.

**Files:**
- Modify: `docs/SPECS.md`, `CLAUDE.md`, `docs/PERFORMANCE.md`

- [ ] **Step 1: Amend section 7's key table**

The `Waits` row reads "Two sub-modes toggled with `c`". Change `c` to `g`, and add under the table:

> The waits sub-mode toggle was `c` until the statement capture took that key.
> The capture ships and the waits view does not, and a mnemonic is worth more
> on a key somebody presses than on one nobody can yet.

- [ ] **Step 2: Qualify section 12**

Replace "No configuration of the monitored server. The tool reads." with:

> No persistent configuration of the monitored server: no setting, no trace
> flag, no `sp_configure` value, nothing that outlives the run. The scoped
> statement capture of section 2 is the one thing this tool creates, it is
> opt-in behind a flag, it exists only while somebody is watching it, and it
> is removed when they stop. Everything else reads.

- [ ] **Step 3: Correct `CLAUDE.md`**

Replace the read-only hard constraint with:

```
- Read-only on the monitored server, with one stated exception. No object
  created, nothing configured, no trace flag set. The exception is the scoped
  statement capture of `docs/SPECS.md` section 2, which creates one named
  Extended Events session, only behind the `-capture` flag, only while
  somebody is watching, and removes it when they stop. Without the flag the
  tool creates and drops nothing at all, the recovery sweep included.
```

And add to the testing section, because a test nobody knows about is a test nobody runs:

```
The capture's sweep compares an event session's `create_time`, which is local
server time, against `SYSDATETIME()`. That is timezone-sensitive and every
container here runs in UTC, so the bug it guards against is invisible locally.
Run the sweep tests against a container started with `TZ=America/Los_Angeles`
after touching that query; the recipe is in the plan's Task 6.
```

- [ ] **Step 4: Record the measurement limitation**

Append to `docs/PERFORMANCE.md`:

> ## What the budget cannot see
>
> The observation budget measures the CPU of the tool's own session. Extended
> Events dispatch does not run there: predicate evaluation and event
> construction happen on the thread of the workload being watched. The
> statement capture is the first feature in this tool whose cost its own
> instrument cannot report.
>
> The predicate is one integer comparison per candidate event, and the two
> events fire once per batch rather than once per statement, so the expected
> cost is small. Expected is not measured. Measure it against the containers
> before relying on the number, and record it here beside the others.
>
> One figure is measured and belongs here now. The ring buffer holds whichever
> of a thousand events and 1024 KB comes first, so drained every two seconds
> the capture keeps up with about five hundred statements a second on the
> watched session. Past that it reports exactly what it missed.

- [ ] **Step 5: Remove the item from section 13**

Section 13 lists the Extended Events capture as a later version. Delete the line; it has arrived.

- [ ] **Step 6: Check the documents against each other**

Run: `grep -n "No object created\|No configuration of the monitored\|toggled with\|Extended Events capture, opt-in" docs/SPECS.md CLAUDE.md`
Expected: nothing that contradicts the feature as built.

- [ ] **Step 7: Commit**

```bash
git add docs/SPECS.md CLAUDE.md docs/PERFORMANCE.md
git commit -F - <<'MSG'
Say in the rules what the capture actually does

Section 2 authorised this feature and deferred it, but two other sentences
were written as though it never would arrive: CLAUDE.md's flat "no object
created" and section 12's "no configuration of the monitored server". Both now
say what is true, which is that one named session is created behind a flag
while somebody watches and removed when they stop, and that without the flag
nothing is created or dropped at all.

The key is the honest half. Section 7 reserved c for a sub-mode of the waits
view and the capture took it. That is a change of mind, not a discovery that
the key was free, and recording it as one is what leaves the waits view a
toggle waiting for it rather than a collision.

CLAUDE.md also gains the note that the sweep has a timezone-sensitive test,
because every container on this machine runs in UTC and the defect it guards
against is invisible without deliberately starting one that does not.
MSG
```

---

### Task 13: Wire the flag end to end

Nothing above runs until this task.

**Files:**
- Modify: `cmd/sqltop/main.go`, `internal/source/mssql/mssql.go`, `internal/collector/collector.go`, `internal/web/server.go`, `internal/web/stream.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `-capture` on the command line, `model.CapCaptureSession` in the capability set, a live manager.

`mssql.New()` takes no arguments and `NewServer` never sees a source, so the flag's route has to be designed rather than assumed. The shortest honest path: `mssql.New()` gains an option or an exported setter for `captureAllowed`, `main` sets it before `Open`, the collector builds the manager when the source implements `source.Capturer` and `CanCapture` says yes, and the server reaches it through `s.col.Captures()` as Task 9 established. Read `cmd/sqltop/main.go` and follow how the existing configuration reaches the source rather than inventing a second channel.

- [ ] **Step 1: Write the failing test**

```go
func TestWithoutTheFlagNothingIsEverCreated(t *testing.T) {
	// The read-only guarantee in one test.
	s := open(t) // captureAllowed defaults false
	db := captureDB(t)
	_, caps, err := s.Identify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Has(model.CapCaptureSession) {
		t.Fatal("the capture capability is present without the flag")
	}
	before := countSessions(t, db)
	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartCapture(context.Background(), 51); err == nil {
		t.Error("StartCapture succeeded without the flag")
	}
	if got := countSessions(t, db); got != before {
		t.Errorf("event session count moved from %d to %d without the flag", before, got)
	}
}

func TestTheCapabilityAppearsWithTheFlag(t *testing.T) {
	s := open(t)
	s.captureAllowed = true
	_, caps, err := s.Identify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Has(model.CapCaptureSession) {
		t.Error("the flag is on and the login is sa, so the capability should be present")
	}
}

func TestShutdownStopsARunningCapture(t *testing.T) {
	// A capture surviving the process is exactly the residue this design is
	// arranged around not producing.
	m, f, _ := testManager(t)
	m.Toggle(context.Background(), 51)
	m.Stop(context.Background(), model.StopByShutdown)
	if len(f.stopped) != 1 {
		t.Error("shutting down left the event session running on the server")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ ./internal/capture/ -run 'Flag|Capability|Shutdown'`
Expected: FAIL.

- [ ] **Step 3: Add the flag**

In `cmd/sqltop/main.go`, beside the others:

```go
	capture := flag.Bool("capture", false, "allow the c command to create a scoped Extended Events session on the monitored server; without this the tool creates and drops nothing")
```

- [ ] **Step 4: Probe the capability and sweep once**

In `Identify`, after the existing probes:

```go
	if s.captureAllowed {
		if ok, _, err := s.CanCapture(ctx); err == nil && ok {
			caps |= model.Capabilities(model.CapCaptureSession)
		}
	}
```

Then run the recovery sweep once after `Identify`, only with the flag, and log what it dropped. A tool that silently deletes objects on somebody's server is worse than one that does not.

- [ ] **Step 5: Stop on shutdown and on the browser leaving**

In the server's shutdown path:

```go
	if m := s.col.Captures(); m != nil {
		m.Stop(context.Background(), model.StopByShutdown)
	}
```

For the browser, `internal/web/stream.go` already knows when a client connects and disconnects. Keep a count; when it reaches zero start a thirty second timer, and if no client has arrived when it fires, stop the capture with `model.StopByBrowserGone`. Thirty seconds so a page reload does not kill a capture. Concretely: a `time.AfterFunc` stored on the `Server` under `s.mu`, cancelled by the next connection.

```go
// lastClientLeft starts the grace period after which an unwatched capture
// stops. Thirty seconds because a page reload is a disconnection, and killing
// a capture on every refresh would make the feature unusable.
func (s *Server) lastClientLeft() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captureIdle != nil {
		s.captureIdle.Stop()
	}
	s.captureIdle = time.AfterFunc(30*time.Second, func() {
		if m := s.col.Captures(); m != nil {
			m.Stop(context.Background(), model.StopByBrowserGone)
		}
	})
}

// clientArrived cancels it.
func (s *Server) clientArrived() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captureIdle != nil {
		s.captureIdle.Stop()
		s.captureIdle = nil
	}
}
```

- [ ] **Step 6: Run everything**

```bash
eval "$(scripts/testdb.sh)"
go vet ./... && gofmt -l . && deno lint internal/web/assets/app.js
go test -race ./... 2>&1 | tail -20
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11439?database=master&encrypt=disable" go test ./... 2>&1 | tail -10
```
Expected: PASS everywhere on both engines, no race, lint clean.

- [ ] **Step 7: Drive it by hand, which is the only check that covers the whole path**

```bash
go build -o /tmp/sqltop ./cmd/sqltop
eval "$(scripts/testdb.sh)"
SQLTOP_CONN="$SQLTOP_TEST_DSN" /tmp/sqltop -capture
```

With a workload running, check every one of these. The panel opens on `c` and names the session. Statements appear within a few seconds. `traces/` beside `/tmp/sqltop` holds a `.jsonl` whose first line is a header carrying the version and the instance. Pressing `c` again stops it, the header says so, and the file's last line is an end record. Closing the browser tab stops the capture after thirty seconds, while reloading the page does not. Then `kill -9` mid-capture, confirm with sqlcmd that the event session is still on the server, restart with `-capture`, and confirm the sweep removed it. Finally run once without `-capture` and confirm `c` says the flag is needed and that no event session is created.

- [ ] **Step 8: Commit**

```bash
git add cmd/sqltop/main.go internal/source/mssql/mssql.go internal/collector/collector.go internal/web/server.go internal/web/stream.go internal/source/mssql/capture_test.go
git commit -F - <<'MSG'
Put the whole capture behind one flag, and prove it stays there

Without -capture the capability is absent, the key says why it did nothing,
StartCapture refuses, and the recovery sweep does not run either, since the
sweep is itself a DROP and has no business on a server whose operator did not
ask. The test counts the event sessions on the server before and after and
fails if the number moves.

Shutdown stops any running capture, and so does the browser going away, after
thirty seconds rather than at once: a page reload is a disconnection, and a
capture that died on every refresh would be unusable. A session outliving the
process is precisely the residue this design is arranged around not producing,
and leaving the one case we can see coming to the sweep would mean the sweep
is never exercised.
MSG
```

---

## Self-review

**Spec coverage.** Design section 3 maps to Tasks 3 and 4, section 4 to Task 8, section 5 to Task 6, section 6 to Tasks 5 and 7, section 7 to Tasks 4 and 8, section 8 to Tasks 1 and 8, section 9 to Tasks 10 and 11, section 10 to Task 12, section 11 across every task, section 12's exclusions by not building them. The document corrections are Task 12.

**What the design asks for and this plan does not build.** `model.StopByFailover` was dropped rather than left unset: detecting a failover reliably needs more than this feature should carry, and a stop reason nothing can produce is worse than an absent one. The design's section 5 note about failover leaving a session on the old primary stands as a stated limitation with no code behind it, which is what it always was.

**Type consistency, checked rather than asserted.** `parseRingBuffer(doc string, mark int64)` is defined in Task 4 and called that way in Task 7. `PollCapture` takes `mark` in the interface (Task 4), the implementation (Task 7) and the manager (Task 8). `CaptureProgress.Seen` is introduced in Task 4 and is what Task 8 advances the mark to. `CaptureNote` serialises `session_id` and `since`, asserted in Task 3 and read as `o.session_id` in Task 11. `CaptureState.Unknown` serialises as `unknown` and is read as `st.unknown`. `New(c source.Capturer, version, instance string)` in Task 8 is called with three arguments by the collector in Task 9.

**Names taken from the tree, not from memory.** The table near the top of this plan lists every one, and the first version of this plan got eight of them wrong. Check it rather than trusting a recollection of the codebase, including this sentence's.

**Where a reviewer should look hardest.** Task 4's truncation arithmetic, because it was wrong in the first version in the direction that ships silent duplicates. Task 5's two exceptions, because an exception that widens is worth more than the check it lives in. Task 6's clock, because the wrong version of it passed every test on every container here. And Task 8's race test, because the previous one could not fail.
