# Per-session statement capture implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** With a row selected in the requests grid, one key captures every completed batch and RPC on that session through an Extended Events session created on demand, drains it to a JSON Lines file under `traces/`, shows it in a panel, and destroys the event session when the capture ends.

**Architecture:** Four layers, none of which knows the one above it. A `Capturer` optional interface beside `Source` carries engine-neutral capture types. `internal/source/mssql/capture.go` implements it with `CREATE EVENT SESSION`, a ring buffer target and an XML drain. `internal/capture` owns the lifecycle: one goroutine per capture, the stop conditions, the JSON Lines file and a mutex-guarded slice of recent statements. `internal/web` exposes one endpoint and one detail-panel mode on the key `c`. Nothing in the whole path runs unless `-capture` was passed.

**Tech Stack:** Go 1.27, standard library plus the existing `github.com/microsoft/go-mssqldb`. No new dependency. `encoding/xml` for the ring buffer, `encoding/json` for the file, `crypto/rand` for the session name suffix.

**Spec:** `docs/specs/2026-08-30-session-capture-design.md`, which was reviewed by two external agents against real 2019 and 2022 engines before this plan was written. Read it before Task 1. `docs/SPECS.md` is the project authority it argues from.

## Global Constraints

- Pure Go, no CGO. Nothing in this feature may change that.
- The tool is read-only on the monitored server except through this feature, and this feature does nothing at all unless the `-capture` flag was passed. No `CREATE`, no `ALTER`, no `DROP`, not even the sweep.
- Every event session this feature creates is named with the prefix `sqltop_capture_`, and every `DROP` it issues is filtered on that prefix. Nothing outside the prefix is ever touched.
- `EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS`, never `NO_EVENT_LOSS`, which would stall the monitored workload.
- The ring buffer target sets `MAX_EVENTS_LIMIT = 1000` and `MAX_MEMORY = 1024` explicitly. Measured: a target that names only `MAX_MEMORY` gets the default limit of 1000 events whatever memory it asks for.
- The hard cap on a capture is ten minutes and is not configurable. The sweep in Task 5 uses it as evidence about other instances, so every instance must agree on it.
- Durations from Extended Events are microseconds and stay microseconds until display.
- Version floor SQL Server 2019 for tests; the DDL was verified to run on 2019 and 2022.
- English everywhere: code, comments, UI, commits, documentation.
- Commits carry no attribution trailer of any kind. The message is prose explaining why, no bullet list of what changed, no bold, no em-dash.
- `gofmt` clean, `go vet ./...` clean, `deno lint internal/web/assets/app.js` clean before every commit.
- Comments say why, in as few words as it can be said. No archaeology, no task numbers, no account of what the code used to do.

---

### Task 1: Extract the beside-the-binary output directory

`snapshots/` and `plans/` resolve their directory and write non-overwriting files inside `internal/web`. `traces/` is the third consumer, which is the second real implementation the project's rule waits for, so the two functions move to their own package. The capture also needs to append as it goes, which the current all-at-once `writeUnique` cannot do, so the extraction adds an open-file form and keeps the existing one on top of it.

**Files:**
- Create: `internal/outdir/outdir.go`
- Create: `internal/outdir/outdir_test.go`
- Modify: `internal/web/commands.go` (remove `besideBinary` and `writeUnique`, call the package)

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
Expected: FAIL, the package does not compile because `Write`, `Create` and `Beside` are undefined.

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
// create it; the callers do, when they have something to put in it.
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
Expected: PASS, three tests.

- [ ] **Step 5: Move the web package onto it**

In `internal/web/commands.go`, delete `besideBinary` and `writeUnique` entirely and change the three call sites. The seam variables keep their shape, because the tests replace them:

```go
var snapshotDir = func() (string, error) { return outdir.Beside("snapshots") }
var planDir = func() (string, error) { return outdir.Beside("plans") }
```

Every remaining `writeUnique(dir, base, ext, body)` becomes `outdir.Write(dir, base, ext, body)`. Add `"github.com/rudi-bruchez/sqltop/internal/outdir"` to the imports and remove `path/filepath` if nothing else in the file uses it.

- [ ] **Step 6: Run the whole suite**

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS everywhere. The existing snapshot and plan tests exercise the moved code through their seams, so a mistake here fails them.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/outdir internal/web/commands.go
git commit -F - <<'MSG'
Give the beside-the-binary output its own package

Snapshots and plans both resolve a directory next to the executable and both
write files that refuse to overwrite. The capture makes three, which is the
second real implementation the project's rule waits for before extracting
anything, and it also needs something the pair could not do: a file it can
append to over several minutes rather than one body written at once. Create
returns the open file and Write is the old behaviour built on top of it.
MSG
```

---

### Task 2: The model types and the capability

Engine-neutral types for what a capture produces, and the capability flag that lets the interface grey the key rather than fail on use.

**Files:**
- Create: `internal/model/capture.go`
- Create: `internal/model/capture_test.go`
- Modify: `internal/model/model.go` (one new capability constant)

**Interfaces:**
- Consumes: nothing.
- Produces: `model.CapturedStatement`, `model.CaptureProgress`, `model.CaptureNote`, `model.CaptureState`, `model.StopReason` with its constants, and `model.CapCaptureSession`.

- [ ] **Step 1: Write the failing test**

```go
package model

import "testing"

func TestCapCaptureSessionIsItsOwnBit(t *testing.T) {
	c := Caps(CapCaptureSession)
	if !c.Has(CapCaptureSession) {
		t.Error("CapCaptureSession should be present")
	}
	if c.Has(CapLivePlanProgress) {
		t.Error("CapCaptureSession collides with CapLivePlanProgress")
	}
	if c.Has(CapSessionWaitStats) {
		t.Error("CapCaptureSession collides with CapSessionWaitStats")
	}
}

func TestStopReasonsAreDistinctAndSpoken(t *testing.T) {
	all := []StopReason{
		StopByKey, StopByShutdown, StopByBrowserGone, StopBySessionGone,
		StopBySessionReused, StopByTimeCap, StopByServerLost, StopByFailover,
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
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/model/ -run 'Capture|StopReason'`
Expected: FAIL, undefined identifiers.

- [ ] **Step 3: Add the capability**

In `internal/model/model.go`, append to the `Capability` const block, after `CapSessionWaitStats`:

```go
	// CapCaptureSession reports whether this source can create a scoped
	// event capture on one session: the right to create an event session
	// and the right to read the DMVs that drain it, both of them, since
	// neither implies the other. It is the only capability that reports
	// something the tool would write rather than read, and it is false
	// unless the operator passed the flag that permits that at all.
	CapCaptureSession
```

- [ ] **Step 4: Write the types**

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

// CaptureProgress is what a drain reports alongside the statements, and it
// is what keeps the panel honest. Missed and Dropped are different losses:
// Missed passed through the buffer between two reads, Dropped never reached
// the buffer at all. Reporting one as the other would hide a real cause.
type CaptureProgress struct {
	Total     int64 // events the server has processed for this capture
	Missed    int64 // events that passed through the buffer unread
	Dropped   int64 // events the server dropped before the target
	Truncated bool  // the read could not place events, so Missed is unknown
}

// CaptureNote is another capture running on the same instance, named by what
// it watches rather than by what it is called on the server. The name is a
// SQL Server concept and spec section 4.1 keeps those below this line.
type CaptureNote struct {
	SessionID int64
	Since     time.Time
}

// StopReason is why a capture ended. Every one of them is shown to the user,
// so every one of them has wording.
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
	StopByFailover
)

func (r StopReason) String() string {
	switch r {
	case StopNotStopped:
		return ""
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
	case StopByFailover:
		return "the instance failed over; an event session may remain on the old primary"
	}
	return ""
}

// CaptureState is the whole of what the panel needs in one value.
type CaptureState struct {
	Available bool          `json:"available"`
	Why       string        `json:"why,omitempty"` // why not, when Available is false
	Active    bool          `json:"active"`
	SessionID int64         `json:"session_id,omitempty"`
	StartedAt time.Time     `json:"started_at,omitempty"`
	Stopped   string        `json:"stopped,omitempty"`
	Statements int          `json:"statements"`
	Missed    int64         `json:"missed"`
	Dropped   int64         `json:"dropped"`
	Unknown   bool          `json:"unknown_loss"`
	File      string        `json:"file,omitempty"`
	Others    []CaptureNote `json:"others,omitempty"`
}
```

- [ ] **Step 5: Run the tests and watch them pass**

Run: `go test ./internal/model/`
Expected: PASS. `TestCapInstanceWideViewIsAbsent` and the other existing capability tests must still pass; if a bit collides the suite says so.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/model/capture.go internal/model/capture_test.go internal/model/model.go
git commit -F - <<'MSG'
Name what a capture produces, in engine-neutral terms

Spec section 4.1 keeps SQL Server concepts below the source seam, so the model
learns about captured statements and stop reasons and never about event
sessions. Two details are decisions rather than transcription. Durations stay
in microseconds all the way to the renderer, because Extended Events reports
microseconds and the short statements this feature exists to show are exactly
the ones a millisecond would round to a lying zero. And the two kinds of loss
are separate fields: one is what passed through the buffer between two reads,
the other is what never reached the buffer, and collapsing them would hide
which of the two is happening.
MSG
```

---

### Task 3: The Capturer interface and the ring buffer parser

The interface, and the one piece of logic that carries the whole feature: turning a ring buffer XML document into statements and an exact count of what was missed. The parser is a pure function over a string, so it is tested without a server, and the tests are the specification of the arithmetic.

**Files:**
- Modify: `internal/source/source.go` (add `CaptureHandle` and `Capturer`)
- Create: `internal/source/mssql/ringbuffer.go`
- Create: `internal/source/mssql/ringbuffer_test.go`

**Interfaces:**
- Consumes: `model.CapturedStatement`, `model.CaptureProgress` from Task 2.
- Produces: `source.Capturer`, `source.CaptureHandle`, and inside the mssql package `parseRingBuffer(xml string, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error)`.

- [ ] **Step 1: Add the interface**

In `internal/source/source.go`, below `Source`:

```go
// CaptureHandle identifies one running capture. It is opaque above this
// package: what it holds is the source's business.
type CaptureHandle struct {
	Name      string
	SessionID int64
	Started   time.Time
}

// Capturer is optional and deliberately not part of Source. Spec section 4.1:
// PostgreSQL and MySQL have no equivalent of a ring buffer target, and an
// abstraction that assumed one would be wrong on two engines out of three.
//
// It is the only interface in this tool that writes to the monitored server,
// and nothing calls it unless the operator passed the flag that permits that.
type Capturer interface {
	// CanCapture reports whether a capture is possible here, and says why
	// not when it is not. A greyed key with no explanation is the failure
	// this project has already fixed twice in the dashboard.
	CanCapture(ctx context.Context) (bool, string, error)

	// SweepCaptures drops the event sessions under this tool's prefix that
	// are dead by construction. It never touches one that might be alive
	// and belong to another instance of sqltop.
	SweepCaptures(ctx context.Context) (dropped int, err error)

	// RunningCaptures reports the other captures alive on this instance, so
	// a second watcher of the same session knows it is doubling the cost.
	RunningCaptures(ctx context.Context) ([]model.CaptureNote, error)

	StartCapture(ctx context.Context, spid int64) (CaptureHandle, error)

	// PollCapture returns the statements not yet seen, and what was lost.
	// mark is the caller's high water mark; the returned Total replaces it.
	PollCapture(ctx context.Context, h CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error)

	StopCapture(ctx context.Context, h CaptureHandle) error
}
```

Add `"time"` to the imports.

- [ ] **Step 2: Write the failing parser test**

The fixtures are the real shape, taken from what a 2022 engine returns.

```go
package mssql

import "testing"

const ringOne = `<RingBufferTarget truncated="0" processingTime="0" totalEventsProcessed="3" eventCount="3" droppedCount="0" memoryUsed="775">
  <event name="sql_batch_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.238Z">
    <data name="duration"><type name="uint64" package="package0"/><value>1234</value></data>
    <data name="cpu_time"><value>1000</value></data>
    <data name="logical_reads"><value>7</value></data>
    <data name="physical_reads"><value>0</value></data>
    <data name="writes"><value>0</value></data>
    <data name="row_count"><value>1</value></data>
    <data name="result"><text>OK</text><value>0</value></data>
    <data name="batch_text"><value><![CDATA[SELECT 1]]></value></data>
    <action name="database_name" package="sqlserver"><value>tempdb</value></action>
    <action name="client_app_name" package="sqlserver"><value>sqlcmd</value></action>
    <action name="username" package="sqlserver"><value>sa</value></action>
  </event>
  <event name="rpc_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.239Z">
    <data name="duration"><value>50</value></data>
    <data name="cpu_time"><value>50</value></data>
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
	// already seen through index 1 must be given index 2 and nothing else.
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
	// The buffer has processed 500 events and holds the last 3, so events 0
	// through 496 are gone. A caller whose mark is 10 missed 487 of them.
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
		t.Errorf("Missed is %d, want 487 (the oldest retained is index 497, the mark was 10)", prog.Missed)
	}
	if prog.Total != 500 {
		t.Errorf("Total is %d, want 500", prog.Total)
	}
}

func TestParseRingBufferTreatsAShortDocumentAsTruncation(t *testing.T) {
	// Measured against a real engine: when the XML exceeds 4 MB the
	// eventCount attribute keeps reporting the buffer, not the document. A
	// parser that trusts the attribute places every event wrongly, so a
	// node count below it is the same signal as the flag.
	x := `<RingBufferTarget truncated="0" totalEventsProcessed="500" eventCount="300">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>a</value></data></event>
	</RingBufferTarget>`
	got, prog, err := parseRingBuffer(x, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !prog.Truncated {
		t.Error("a document with fewer nodes than eventCount must report Truncated")
	}
	if prog.Missed != 0 {
		t.Errorf("Missed is %d; when the placement is untrustworthy the count is unknown, not invented", prog.Missed)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want the one node that was returned", len(got))
	}
}

func TestParseRingBufferHonoursTheTruncatedFlag(t *testing.T) {
	x := `<RingBufferTarget truncated="1" totalEventsProcessed="500" eventCount="1">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>a</value></data></event>
	</RingBufferTarget>`
	_, prog, err := parseRingBuffer(x, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !prog.Truncated || prog.Missed != 0 {
		t.Errorf("progress %+v, want Truncated with no invented count", prog)
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

Run: `go test ./internal/source/mssql/ -run RingBuffer`
Expected: FAIL, `parseRingBuffer` is undefined.

- [ ] **Step 4: Write the parser**

```go
package mssql

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// The ring buffer hands back everything it still holds on every read, so an
// event is seen many times and has to be placed rather than deduplicated by
// guesswork. totalEventsProcessed is cumulative for the life of the session
// and eventCount is what is held now, so the buffer holds absolute indices
// totalEventsProcessed-eventCount through totalEventsProcessed-1, in order.
// That is the whole scheme, and it is also how the number of events that
// passed through unread is counted exactly rather than estimated.
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

type ringData struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value"`
}

// parseRingBuffer turns one target_data document into the statements the
// caller has not seen and an honest account of what was lost. mark is the
// caller's high water mark: the absolute index it has consumed through.
func parseRingBuffer(doc string, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	var t ringTarget
	if strings.TrimSpace(doc) == "" {
		return nil, model.CaptureProgress{}, nil
	}
	if err := xml.Unmarshal([]byte(doc), &t); err != nil {
		return nil, model.CaptureProgress{}, err
	}

	prog := model.CaptureProgress{Total: t.Total}
	// Either signal means the document is not the buffer. The flag says the
	// server truncated; a node count below eventCount says the same thing
	// and is the one that shows up first.
	prog.Truncated = t.Truncated != 0 || int64(len(t.Events)) < t.EventCount

	first := t.Total - int64(len(t.Events)) // absolute index of the oldest node
	if !prog.Truncated && first > mark {
		prog.Missed = first - mark
	}

	out := make([]model.CapturedStatement, 0, len(t.Events))
	for i, e := range t.Events {
		if !prog.Truncated && first+int64(i) < mark {
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
			s.Result = d.Value
		case "object_name":
			s.Object = d.Value
		case "batch_text", "statement":
			s.Text = d.Value
		}
	}
	for _, a := range e.Actions {
		switch a.Name {
		case "database_name":
			s.Database = a.Value
		case "client_app_name":
			s.Application = a.Value
		case "username":
			s.User = a.Value
		}
	}
	return s
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
```

- [ ] **Step 5: Run the tests and watch them pass**

Run: `go test ./internal/source/mssql/ -run RingBuffer -v 2>&1 | tail -20`
Expected: PASS, six tests.

- [ ] **Step 6: Prove the arithmetic test is not passing by accident**

Change `first := t.Total - int64(len(t.Events))` to `first := t.Total - t.EventCount` and run the tests again. `TestParseRingBufferTreatsAShortDocumentAsTruncation` must still pass, but confirm by also changing the truncation detection to `t.Truncated != 0` alone: `TestParseRingBufferTreatsAShortDocumentAsTruncation` must then FAIL. Restore both lines. This is the project's rule from `CLAUDE.md`: break the thing an assertion asserts and watch it fail, because two of the first four assertions written for the browser test passed against a deliberately broken page.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/source.go internal/source/mssql/ringbuffer.go internal/source/mssql/ringbuffer_test.go
git commit -F - <<'MSG'
Place ring buffer events by index rather than deduplicate them by guess

The target returns its whole contents on every read, so the same statement
arrives many times and something has to decide which of them is new. Its own
totalEventsProcessed and eventCount attributes answer it exactly: the buffer
holds a known range of absolute indices in order, so a high water mark is
enough and no timestamp heuristic is needed. The same arithmetic counts what
passed through between two reads, which is the difference between a list that
is incomplete and a list that says how incomplete it is.

Truncation is the case that breaks it, and it is detected on two signals
rather than one. Past four megabytes of XML the eventCount attribute keeps
describing the buffer while the document holds fewer nodes, so a short
document is treated exactly like the flag: placement is abandoned, the loss
is declared unknown rather than invented, and the mark re-anchors.
MSG
```

---

### Task 4: The statements the tool sends, and the exception to the read-only check

The DDL builders, the sweep and drain queries, and the narrow exception that lets them exist alongside `TestNoQueryWritesToTheMonitoredServer`. No server is needed for this task: the tests read the generated text.

**Files:**
- Create: `internal/source/mssql/capture.go` (statements and builders only; the methods come in Tasks 5 and 6)
- Create: `internal/source/mssql/capture_test.go`
- Modify: `internal/source/mssql/catalogue_test.go` (the `writes` field and the two checks)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `capturePrefix`, `captureCap`, `captureSessionName(spid int64) (string, error)`, `createCaptureQueryTemplate`, `startCaptureQueryTemplate`, `stopCaptureQueryTemplate`, `sweepCaptureQuery`, `runningCapturesQuery`, `drainCaptureQuery`, `capturePermissionQuery`.

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
	// Measured against 2019 and 2022: a target that names only MAX_MEMORY
	// gets MAX_EVENTS_LIMIT = 1000 whatever memory it asks for, so the
	// memory figure alone describes a buffer the feature never receives.
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if !strings.Contains(ddl, "MAX_EVENTS_LIMIT") {
		t.Error("the event count cap is left implicit, so the default of 1000 governs silently")
	}
	if !strings.Contains(ddl, "MAX_MEMORY = 1024") {
		t.Error("the ring buffer target memory cap is missing")
	}
	if !strings.Contains(ddl, "STARTUP_STATE = OFF") {
		t.Error("without STARTUP_STATE = OFF a leftover session comes back after a server restart")
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
		t.Errorf("only %d distinct names out of 100; the suffix is not random enough to avoid collisions", len(seen))
	}
}

func TestEveryCaptureStatementNamesOnlyOurOwnPrefix(t *testing.T) {
	// The exception to the read-only rule is narrow or it is not an
	// exception. Every statement that writes must name an identifier under
	// the prefix, so none of them can reach system_health or anything else
	// a login with ALTER ANY EVENT SESSION could otherwise destroy.
	for _, s := range captureWritingStatements() {
		if !strings.Contains(s.sql, capturePrefix) {
			t.Errorf("%s writes but never names the %s prefix", s.name, capturePrefix)
		}
	}
}

func TestSweepNeverTouchesAnythingOutsideThePrefix(t *testing.T) {
	if !strings.Contains(sweepCaptureQuery, capturePrefix+"%") {
		t.Error("the sweep does not filter on the prefix, so it can see other people's event sessions")
	}
	if !strings.Contains(strings.ToUpper(sweepCaptureQuery), "SYSUTCDATETIME") {
		t.Error("the sweep's age comparison must be made on the server; clock skew has no business deciding whether to destroy a colleague's capture")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/source/mssql/ -run Capture`
Expected: FAIL, undefined identifiers.

- [ ] **Step 3: Write the statements**

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
// the sweep uses it as evidence about captures belonging to other instances
// of this tool, and that reasoning only holds while every instance agrees on
// the value. Encoding it in the session name is the way out if a knob is ever
// wanted.
const captureCap = 10 * time.Minute

// captureSessionName builds an identifier that cannot carry anything but an
// integer and hex, which is what makes bracketing it safe.
func captureSessionName(spid int64) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d_%s", capturePrefix, spid, hex.EncodeToString(b[:])), nil
}

// The predicate is a literal because an event session predicate is compiled
// when the session is created and cannot be parameterised. The session id
// comes from a row of our own grid as an int64 and is rendered by %d.
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

// sweepCaptureQuery names what is dead by construction and nothing else.
// A definition that is not started is a residue, because a live capture is
// always started and a stopped one has its definition dropped. A started
// session older than twice the cap belongs to nobody, because a live instance
// would have stopped it at the cap. Anything started and younger is left
// alone: it is probably another instance's, and destroying a colleague's
// capture is worse than leaving a stale one for another twenty minutes.
const sweepCaptureQuery = `SELECT s.name
FROM sys.server_event_sessions AS s
LEFT JOIN sys.dm_xe_sessions AS x ON x.name = s.name
WHERE s.name LIKE '` + capturePrefix + `%'
  AND (x.name IS NULL
       OR x.create_time < DATEADD(minute, -@cap, SYSUTCDATETIME()))`

const runningCapturesQuery = `SELECT x.name, x.create_time
FROM sys.dm_xe_sessions AS x
WHERE x.name LIKE '` + capturePrefix + `%'`

const drainCaptureQuery = `SELECT CAST(t.target_data AS nvarchar(max)), s.dropped_event_count, s.dropped_buffer_count
FROM sys.dm_xe_sessions AS s
JOIN sys.dm_xe_session_targets AS t ON t.event_session_address = s.address
WHERE s.name = @name AND t.target_name = 'ring_buffer'`

// capturePermissionQuery asks for both rights, because neither implies the
// other: a login able to create the session but not to read the DMVs would
// create a capture it could never drain.
const capturePermissionQuery = `SELECT
    HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION'),
    HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE')`
```

- [ ] **Step 4: Add the exception to the read-only check**

`queryCatalogue` is discovered by name suffix, so these statements must appear in it or `TestQueryCatalogueCoversEveryQueryInThePackage` fails. Add a `writes bool` to `catalogueEntry`, set it on the four writing entries, and skip those in the read-only check. In `internal/source/mssql/catalogue_test.go`:

```go
type catalogueEntry struct {
	name  string
	when  string
	why   string
	sql   string
	writes bool // the capture exception; see TestNoQueryWritesToTheMonitoredServer
}
```

Append these entries to the slice returned by `queryCatalogue()`:

```go
		{
			name:   "createCaptureQueryTemplate",
			when:   "on the c command, only when -capture was passed",
			why:    "Creates the scoped event capture of docs/specs/2026-08-30-session-capture-design.md. Both ring buffer caps are stated because a target naming only MAX_MEMORY silently gets a thousand-event limit.",
			sql:    createCaptureQueryTemplate,
			writes: true,
		},
		{
			name:   "startCaptureQueryTemplate",
			when:   "immediately after createCaptureQueryTemplate",
			why:    "Starts the session, which STARTUP_STATE = OFF deliberately does not do.",
			sql:    startCaptureQueryTemplate,
			writes: true,
		},
		{
			name:   "stopCaptureQueryTemplate",
			when:   "when a capture ends, for any of the reasons in model.StopReason",
			why:    "Removes the session. An event session outlives the process that made it, so this is not optional tidiness.",
			sql:    stopCaptureQueryTemplate,
			writes: true,
		},
		{
			name:   "sweepCaptureQuery",
			when:   "at connection and before each new capture, only when -capture was passed",
			why:    "Names the sessions under this tool's prefix that are dead by construction, so a crash does not leave one running forever. Reads only; the DROP it feeds is stopCaptureQueryTemplate.",
			sql:    sweepCaptureQuery,
		},
		{
			name: "runningCapturesQuery",
			when: "while the capture panel is open",
			why:  "Reports the other captures alive on this instance, so a second watcher of the same session knows it is doubling the dispatch cost on the monitored workload.",
			sql:  runningCapturesQuery,
		},
		{
			name: "drainCaptureQuery",
			when: "every two seconds while a capture runs",
			why:  "Reads the ring buffer target and the session's own dropped counters. The two losses it reports are different: one passed through the buffer between reads, the other never reached it.",
			sql:  drainCaptureQuery,
		},
		{
			name: "capturePermissionQuery",
			when: "at connection, inside Identify, only when -capture was passed",
			why:  "Asks for ALTER ANY EVENT SESSION and VIEW SERVER STATE together, because neither implies the other and a login with only the first would create a capture it could never read.",
			sql:  capturePermissionQuery,
		},
```

Then change the read-only check to honour the field, and add the companion that keeps it narrow:

```go
	for _, e := range queryCatalogue() {
		if e.writes {
			// The capture of docs/specs/2026-08-30-session-capture-design.md
			// is the exception section 2 of the spec named and deferred. It
			// is kept narrow by TestTheWriteExceptionIsOnlyTheCapture below,
			// which is the half of this that matters: turning the check off
			// would give up the property it exists to hold.
			continue
		}
		...
	}
```

```go
// TestTheWriteExceptionIsOnlyTheCapture is what keeps the exception above
// from widening. A statement may write only if it is one of these four by
// name and only if it names this tool's own prefix.
func TestTheWriteExceptionIsOnlyTheCapture(t *testing.T) {
	allowed := map[string]bool{
		"createCaptureQueryTemplate": true,
		"startCaptureQueryTemplate":  true,
		"stopCaptureQueryTemplate":   true,
	}
	for _, e := range queryCatalogue() {
		if !e.writes {
			continue
		}
		if !allowed[e.name] {
			t.Errorf("%s claims the write exception; only the capture statements may", e.name)
		}
		if !strings.Contains(e.sql, capturePrefix) && !strings.Contains(e.sql, "[%s]") {
			t.Errorf("%s writes without naming an identifier under %s", e.name, capturePrefix)
		}
	}
	for name := range allowed {
		found := false
		for _, e := range queryCatalogue() {
			if e.name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is allowed to write but is not in the catalogue at all", name)
		}
	}
}

// captureWritingStatements is the same set, for the tests in capture_test.go.
func captureWritingStatements() []catalogueEntry {
	var out []catalogueEntry
	for _, e := range queryCatalogue() {
		if e.writes {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 5: Run the package tests**

Run: `go test ./internal/source/mssql/ -run 'Capture|Catalogue|NoQueryWrites' -v 2>&1 | tail -30`
Expected: PASS. `TestNoQueryWritesToTheMonitoredServer` passes because the four writing entries are skipped, and `TestTheWriteExceptionIsOnlyTheCapture` passes because they are exactly the allowed ones.

- [ ] **Step 6: Prove the exception cannot widen**

Temporarily set `writes: true` on the `spidQuery` entry and run `go test ./internal/source/mssql/ -run TheWriteException`. It must FAIL with "spidQuery claims the write exception". Remove the change.

- [ ] **Step 7: Regenerate the queries document**

Run: `go test ./internal/source/mssql -run TestQueriesDocIsCurrent -update`
Then: `go test ./internal/source/mssql -run TestQueriesDocIsCurrent`
Expected: PASS, and `docs/QUERIES.md` now documents the seven capture statements.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/capture.go internal/source/mssql/capture_test.go internal/source/mssql/catalogue_test.go docs/QUERIES.md
git commit -F - <<'MSG'
Write the capture statements, and narrow the hole they need

The read-only check forbids CREATE, ALTER and DROP across every statement in
the package, and this feature needs all three. Turning the check off for them
would give up the property it exists to hold, so the exception is a field on
the catalogue entry, four named statements are allowed to claim it, and a
second test asserts that only those four do and that each one names this
tool's own prefix. A statement that writes and reaches outside the prefix now
fails the suite, which is the thing actually worth protecting: the right this
feature asks for would also allow dropping system_health.

Both ring buffer caps are written out because a target that names only
MAX_MEMORY gets an implicit thousand-event limit, measured on 2019 and 2022,
and a test now fails if the explicit one is ever removed. The sweep compares
ages with SYSUTCDATETIME rather than the process clock: skew between two
machines has no business deciding whether to destroy a colleague's capture.
MSG
```

---

### Task 5: CanCapture, the sweep, and running captures

The three read-side methods, tested against real containers.

**Files:**
- Modify: `internal/source/mssql/capture.go` (add the methods)
- Modify: `internal/source/mssql/capture_test.go` (add integration tests)

**Interfaces:**
- Consumes: the statements from Task 4, `source.Capturer` from Task 3.
- Produces: `(*Source).CanCapture`, `(*Source).SweepCaptures`, `(*Source).RunningCaptures`. Reads `s.captureAllowed bool`, a field set from the flag in Task 12; until then it is false and every method reports unavailable.

- [ ] **Step 1: Write the failing integration tests**

Follow the existing integration test conventions in this package: they read `SQLTOP_TEST_DSN` and skip when it is unset. Use the same helper the other tests use to open a source.

```go
func TestSweepRemovesAStoppedDefinition(t *testing.T) {
	s := testSource(t) // the package's existing helper; skips without a DSN
	s.captureAllowed = true
	name := capturePrefix + "9999_deadbeef"
	mustExec(t, s, fmt.Sprintf(createCaptureQueryTemplate, name, 9999, 9999))
	t.Cleanup(func() { s.db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	// Created with STARTUP_STATE = OFF and never started: a residue.
	n, err := s.SweepCaptures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("the sweep dropped %d sessions, want at least the stopped one", n)
	}
	if sessionExists(t, s, name) {
		t.Error("the stopped definition survived the sweep")
	}
}

func TestSweepLeavesAYoungRunningCaptureAlone(t *testing.T) {
	// This is the property that protects a colleague's capture, and it is
	// the one most likely to regress, so it gets its own test.
	s := testSource(t)
	s.captureAllowed = true
	name := capturePrefix + "9998_beefcafe"
	mustExec(t, s, fmt.Sprintf(createCaptureQueryTemplate, name, 9998, 9998))
	mustExec(t, s, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { s.db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sessionExists(t, s, name) {
		t.Fatal("the sweep destroyed a running capture younger than the cap; that is somebody else's work")
	}
}

func TestSweepRemovesAnOldRunningCapture(t *testing.T) {
	// Waiting twenty minutes is not a test. The age threshold is passed to
	// the query as a parameter, so the test asks for a negative cap, which
	// makes every running session older than the threshold.
	s := testSource(t)
	s.captureAllowed = true
	name := capturePrefix + "9997_f00df00d"
	mustExec(t, s, fmt.Sprintf(createCaptureQueryTemplate, name, 9997, 9997))
	mustExec(t, s, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { s.db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	if _, err := s.sweepOlderThan(context.Background(), -1*time.Minute); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, s, name) {
		t.Error("a running capture past the threshold survived the sweep")
	}
}

func TestRunningCapturesReportsTheSessionIdNotTheName(t *testing.T) {
	s := testSource(t)
	s.captureAllowed = true
	name := capturePrefix + "9996_0badc0de"
	mustExec(t, s, fmt.Sprintf(createCaptureQueryTemplate, name, 9996, 9996))
	mustExec(t, s, fmt.Sprintf(startCaptureQueryTemplate, name))
	t.Cleanup(func() { s.db.Exec(fmt.Sprintf(stopCaptureQueryTemplate, name)) })

	notes, err := s.RunningCaptures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
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

func TestCaptureIsUnavailableWithoutTheFlag(t *testing.T) {
	s := testSource(t)
	s.captureAllowed = false
	ok, why, err := s.CanCapture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("capture is available without the flag; nothing may be created on a server whose operator did not ask")
	}
	if why == "" {
		t.Error("unavailable with no reason given")
	}
	// And the sweep must not run either: it is itself a DROP.
	if n, err := s.SweepCaptures(context.Background()); err != nil || n != 0 {
		t.Errorf("the sweep ran without the flag: %d dropped, err %v", n, err)
	}
}
```

Add the two small helpers beside them:

```go
func mustExec(t *testing.T, s *Source, sql string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), sql); err != nil {
		t.Fatalf("%v\nwhile running: %s", err, sql)
	}
}

func sessionExists(t *testing.T, s *Source, name string) bool {
	t.Helper()
	var n int
	err := s.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sys.server_event_sessions WHERE name = @p1", name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n > 0
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run 'Sweep|RunningCaptures|CaptureIsUnavailable'`
Expected: FAIL, the methods are undefined.

- [ ] **Step 3: Write the methods**

```go
// CanCapture reports whether a capture is possible, and says why not when it
// is not. Three gates, in the order that a reader would ask them.
func (s *Source) CanCapture(ctx context.Context) (bool, string, error) {
	if !s.captureAllowed {
		return false, "capture is off; start sqltop with -capture to allow it", nil
	}
	if s.info.Deployment == model.DeploymentAzureSQLDatabase {
		return false, "Azure SQL Database has only database-scoped event sessions, which this is not written for", nil
	}
	var ddl, view bool
	if err := s.db.QueryRowContext(ctx, capturePermissionQuery).Scan(&ddl, &view); err != nil {
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

// SweepCaptures removes what is dead by construction. It never runs without
// the flag, because it is itself a DROP and has no business on a server whose
// operator did not ask for this feature.
func (s *Source) SweepCaptures(ctx context.Context) (int, error) {
	return s.sweepOlderThan(ctx, 2*captureCap)
}

// sweepOlderThan is SweepCaptures with the threshold exposed, so the age rule
// can be tested without waiting twenty minutes for it.
func (s *Source) sweepOlderThan(ctx context.Context, age time.Duration) (int, error) {
	if !s.captureAllowed {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, sweepCaptureQuery, sql.Named("cap", int(age.Minutes())))
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var dropped int
	for _, n := range names {
		// Belt and braces: the query filters on the prefix and so does
		// this. The right this feature holds would also allow dropping
		// system_health, and one filter is one mistake away from that.
		if !strings.HasPrefix(n, capturePrefix) {
			continue
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(stopCaptureQueryTemplate, n)); err != nil {
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}

// RunningCaptures names the captures alive on this instance by what they
// watch. The event session names stay below this line: spec section 4.1.
func (s *Source) RunningCaptures(ctx context.Context) ([]model.CaptureNote, error) {
	if !s.captureAllowed {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, runningCapturesQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CaptureNote
	for rows.Next() {
		var name string
		var since time.Time
		if err := rows.Scan(&name, &since); err != nil {
			return nil, err
		}
		out = append(out, model.CaptureNote{SessionID: spidFromCaptureName(name), Since: since})
	}
	return out, rows.Err()
}

// spidFromCaptureName reads the session id back out of the name. Zero when
// the name does not carry one, which cannot happen for a name this tool made
// and is not worth an error for one it did not.
func spidFromCaptureName(name string) int64 {
	rest, ok := strings.CutPrefix(name, capturePrefix)
	if !ok {
		return 0
	}
	digits, _, _ := strings.Cut(rest, "_")
	return atoi(digits)
}
```

Add `captureAllowed bool` to the `Source` struct in `internal/source/mssql/mssql.go`, with a comment saying it is set from the `-capture` flag and that nothing in this file writes to the server while it is false.

- [ ] **Step 4: Run the tests against both engines**

```bash
eval "$(scripts/testdb.sh)"
go test ./internal/source/mssql/ -run 'Sweep|RunningCaptures|CaptureIsUnavailable' -v 2>&1 | tail -30
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11439?database=master&encrypt=disable" \
  go test ./internal/source/mssql/ -run 'Sweep|RunningCaptures|CaptureIsUnavailable' 2>&1 | tail -10
```
Expected: PASS on 2022 and on 2019. Start `sqltop-test-2019` first with `podman start sqltop-test-2019` if it is not running.

- [ ] **Step 5: Leave the server clean**

Run: `go test ./internal/source/mssql/ -run Capture` twice in a row. The second run must pass, which it only does if the first left no session behind. Then check by hand that nothing under the prefix survives:

```bash
podman exec sqltop-test /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P 'Sqltop_dev_2026!' -C \
  -Q "SELECT name FROM sys.server_event_sessions WHERE name LIKE 'sqltop_capture_%'"
```
Expected: no rows.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/capture.go internal/source/mssql/capture_test.go internal/source/mssql/mssql.go
git commit -F - <<'MSG'
Recover abandoned captures without destroying live ones

An event session outlives the process that made it, so a kill leaves one
running and something has to remove it. The difficulty is that several people
can watch the same server at once, and a name cannot prove ownership across
machines. So the sweep does not look for an owner. It looks for the two states
that are dead by construction: a definition that is not started, which a live
capture never is, and a started session older than twice the cap, which a live
instance would have stopped. Anything younger is left alone.

The test for that last rule is the one that matters and it exists: a running
capture younger than the threshold must survive a sweep, because that is the
property standing between this feature and destroying a colleague's work. The
age threshold is a parameter so the old-session rule can be tested by asking
for a negative one rather than waiting twenty minutes.

Without the flag none of this runs at all, the sweep included, since the sweep
is itself a DROP and has no business on a server nobody asked.
MSG
```

---

### Task 6: Start, poll and stop

**Files:**
- Modify: `internal/source/mssql/capture.go`
- Modify: `internal/source/mssql/capture_test.go`

**Interfaces:**
- Consumes: `parseRingBuffer` from Task 3, the statements from Task 4.
- Produces: `(*Source).StartCapture`, `(*Source).PollCapture`, `(*Source).StopCapture`, satisfying `source.Capturer`.

- [ ] **Step 1: Write the failing test**

```go
// TestCaptureSeesABatchAndAnRPC is the whole feature end to end against a
// real engine: create, drive known work on a watched connection, drain, and
// find both kinds of statement with plausible numbers.
func TestCaptureSeesABatchAndAnRPC(t *testing.T) {
	s := testSource(t)
	s.captureAllowed = true
	ctx := context.Background()

	watched, err := s.db.Conn(ctx)
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
	// A parameterised statement goes to the server as an RPC on sp_executesql.
	var n int
	if err := watched.QueryRowContext(ctx, "SELECT @p1", 42).Scan(&n); err != nil {
		t.Fatal(err)
	}

	// MAX_DISPATCH_LATENCY is two seconds, so the events are not in the
	// target the instant they complete. Poll until they are rather than
	// sleeping once and hoping.
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
	if batch.Kind != "batch" {
		t.Errorf("the batch came back as kind %q", batch.Kind)
	}
	if batch.DurationUs <= 0 {
		t.Errorf("duration is %d microseconds, which is not a duration", batch.DurationUs)
	}
	if batch.Database == "" {
		t.Error("the database_name action did not arrive")
	}
	if rpc == nil {
		t.Fatal("the parameterised statement did not arrive as an rpc")
	}
}

func TestCaptureIgnoresOtherSessions(t *testing.T) {
	s := testSource(t)
	s.captureAllowed = true
	ctx := context.Background()
	watched, _ := s.db.Conn(ctx)
	defer watched.Close()
	var spid int64
	watched.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid)

	h, err := s.StartCapture(ctx, spid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCapture(ctx, h)

	other, _ := s.db.Conn(ctx)
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
			t.Fatal("the predicate is not scoped to one session; another session's statement was captured")
		}
	}
}

func TestStopRemovesTheSession(t *testing.T) {
	s := testSource(t)
	s.captureAllowed = true
	ctx := context.Background()
	h, err := s.StartCapture(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionExists(t, s, h.Name) {
		t.Fatal("StartCapture did not leave a session on the server")
	}
	if err := s.StopCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	if sessionExists(t, s, h.Name) {
		t.Error("the event session survived StopCapture; it would outlive the process")
	}
}

func TestPollReportsMissedEventsUnderLoad(t *testing.T) {
	// The buffer holds a thousand events. Driving more than that between
	// two polls must produce an exact count, not merely a noticed gap.
	s := testSource(t)
	s.captureAllowed = true
	ctx := context.Background()
	watched, _ := s.db.Conn(ctx)
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
	if prog.Truncated {
		t.Skip("the read was truncated, so the count is unknown by design; not a failure")
	}
	if prog.Missed == 0 {
		t.Fatalf("2500 statements through a 1000 event buffer reported no loss; progress %+v", prog)
	}
	if prog.Total < 2500 {
		t.Errorf("Total is %d, want at least the 2500 statements driven", prog.Total)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run 'CaptureSees|CaptureIgnores|StopRemoves|PollReports'`
Expected: FAIL, the methods are undefined.

- [ ] **Step 3: Write the methods**

```go
// StartCapture creates the session and starts it. It sweeps first, so a
// crashed predecessor is cleaned before a new one is added beside it.
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
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(createCaptureQueryTemplate, name, spid, spid)); err != nil {
		return source.CaptureHandle{}, err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(startCaptureQueryTemplate, name)); err != nil {
		// A session that was created but would not start must not be left
		// behind: it is exactly the residue the sweep exists to clean, and
		// leaving it here would mean relying on the sweep for a case we can
		// see happening.
		s.db.ExecContext(ctx, fmt.Sprintf(stopCaptureQueryTemplate, name))
		return source.CaptureHandle{}, err
	}
	return source.CaptureHandle{Name: name, SessionID: spid, Started: time.Now()}, nil
}

// PollCapture reads the target once and reports what is new and what was
// lost. The two losses stay separate: Missed passed through the buffer
// between reads, Dropped never reached the buffer.
func (s *Source) PollCapture(ctx context.Context, h source.CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	var doc sql.NullString
	var droppedEvents, droppedBuffers int64
	err := s.db.QueryRowContext(ctx, drainCaptureQuery, sql.Named("name", h.Name)).
		Scan(&doc, &droppedEvents, &droppedBuffers)
	if errors.Is(err, sql.ErrNoRows) {
		// The session is gone from under us. The caller decides what that
		// means; here it is simply nothing to read.
		return nil, model.CaptureProgress{}, nil
	}
	if err != nil {
		return nil, model.CaptureProgress{}, err
	}
	out, prog, err := parseRingBuffer(doc.String, mark)
	if err != nil {
		return nil, model.CaptureProgress{}, err
	}
	prog.Dropped = droppedEvents
	return out, prog, nil
}

func (s *Source) StopCapture(ctx context.Context, h source.CaptureHandle) error {
	if !strings.HasPrefix(h.Name, capturePrefix) {
		return fmt.Errorf("mssql: refusing to drop %q, which is not one of ours", h.Name)
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(stopCaptureQueryTemplate, h.Name))
	return err
}
```

Add the compile-time assertion beside them, so a signature drift is a build error rather than a runtime surprise:

```go
var _ source.Capturer = (*Source)(nil)
```

- [ ] **Step 4: Run the tests on both engines**

```bash
eval "$(scripts/testdb.sh)"
go test ./internal/source/mssql/ -run 'CaptureSees|CaptureIgnores|StopRemoves|PollReports' -v 2>&1 | tail -30
podman start sqltop-test-2019
SQLTOP_TEST_DSN="sqlserver://sa:Sqltop_dev_2026%21@127.0.0.1:11439?database=master&encrypt=disable" \
  go test ./internal/source/mssql/ -run 'CaptureSees|CaptureIgnores|StopRemoves|PollReports' 2>&1 | tail -10
```
Expected: PASS on both.

- [ ] **Step 5: Prove the session predicate test would catch a real mistake**

Change the predicate in `createCaptureQueryTemplate` from `sqlserver.session_id = %d` to `sqlserver.session_id >= 0` and run `TestCaptureIgnoresOtherSessions`. It must FAIL. Restore the predicate.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/source/mssql/capture.go internal/source/mssql/capture_test.go
git commit -F - <<'MSG'
Create, drain and destroy the capture against a real engine

Start sweeps before it creates, so a crashed predecessor is cleaned rather
than accumulated beside. A session that is created but will not start is
dropped immediately instead of being left for the sweep: relying on the
recovery path for a failure we are standing in front of is how recovery paths
stop being tested.

The tests drive a real 2019 and a real 2022, which is the only way any of this
can be checked. The one that matters most is the negative: a statement run on
another connection must not appear, since the whole cost argument for this
feature rests on the predicate being scoped to one session. Breaking the
predicate makes it fail, which is the only evidence that it asserts anything.
MSG
```

---

### Task 7: The capture manager

The lifecycle, the drain goroutine, the file, and the mutex that keeps the panel from racing the drain. Tested with a fake `Capturer`, so none of this needs a server.

**Files:**
- Create: `internal/capture/capture.go`
- Create: `internal/capture/capture_test.go`

**Interfaces:**
- Consumes: `source.Capturer`, `source.CaptureHandle`, the model types, `outdir`.
- Produces: `capture.Manager` with `New(c source.Capturer) *Manager`, `(*Manager).Toggle(ctx, spid int64) error`, `(*Manager).Stop(ctx, r model.StopReason)`, `(*Manager).State(ctx) model.CaptureState`, `(*Manager).Recent() []model.CapturedStatement`, `(*Manager).Watch(alive func(spid int64) (loginTime time.Time, ok bool))`.

- [ ] **Step 1: Write the failing test**

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

// fakeCapturer is the whole server, in memory. It hands out statements on
// demand so the manager's timing can be driven rather than waited on.
type fakeCapturer struct {
	mu      sync.Mutex
	started []string
	stopped []string
	queue   []model.CapturedStatement
	prog    model.CaptureProgress
	failStart error
}

func (f *fakeCapturer) CanCapture(context.Context) (bool, string, error) { return true, "", nil }
func (f *fakeCapturer) SweepCaptures(context.Context) (int, error)       { return 0, nil }
func (f *fakeCapturer) RunningCaptures(context.Context) ([]model.CaptureNote, error) {
	return nil, nil
}
func (f *fakeCapturer) StartCapture(_ context.Context, spid int64) (source.CaptureHandle, error) {
	if f.failStart != nil {
		return source.CaptureHandle{}, f.failStart
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	name := "sqltop_capture_fake"
	f.started = append(f.started, name)
	return source.CaptureHandle{Name: name, SessionID: spid, Started: time.Now()}, nil
}
func (f *fakeCapturer) PollCapture(_ context.Context, _ source.CaptureHandle, _ int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.queue
	f.queue = nil
	return out, f.prog, nil
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
	f := &fakeCapturer{}
	m := New(f)
	m.dir = func() (string, error) { return dir, nil }
	m.interval = 10 * time.Millisecond
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
	if len(f.stopped) != 1 {
		t.Errorf("the first capture was not stopped; %d stops recorded", len(f.stopped))
	}
	if len(f.started) != 2 {
		t.Errorf("%d captures started, want two", len(f.started))
	}
}

func TestStatementsReachTheFileAsTheyArrive(t *testing.T) {
	m, f, dir := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	f.offer(model.CapturedStatement{Kind: "batch", Text: "SELECT 1", DurationUs: 900})

	waitFor(t, func() bool { return len(m.Recent()) == 1 })

	// The file must be readable while the capture is still running: a
	// process killed mid-capture leaves a valid partial trace.
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
	var head map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatal(err)
	}
	if head["kind"] != "header" || head["session_id"] == nil {
		t.Errorf("first record is %v, want a header naming the session", head)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["kind"] != "event" || ev["text"] != "SELECT 1" {
		t.Errorf("second record is %v, want the captured statement", ev)
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
	if !strings.Contains(string(body), `"kind":"gap"`) {
		t.Error("487 events were lost and no gap record was written; a list that omits what it missed is the usual way this lies")
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
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		last = l
	}
	if !strings.Contains(last, `"kind":"end"`) || !strings.Contains(last, "ten minute cap") {
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

func TestASessionThatWasHandedToSomeoneElseStopsTheCapture(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	login := time.Now()
	var mu sync.Mutex
	m.Watch(func(spid int64) (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		return login, true
	})
	m.Toggle(ctx, 51)
	waitFor(t, func() bool { return m.State(ctx).Active })

	mu.Lock()
	login = login.Add(time.Second) // the pool reset the connection
	mu.Unlock()

	waitFor(t, func() bool { return !m.State(ctx).Active })
	if got := m.State(ctx).Stopped; !strings.Contains(got, "pool") {
		t.Errorf("stopped because %q, want the pooled reuse", got)
	}
}

func TestASessionThatEndedStopsTheCapture(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	alive := true
	var mu sync.Mutex
	m.Watch(func(spid int64) (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		return time.Time{}, alive
	})
	m.Toggle(ctx, 51)
	waitFor(t, func() bool { return m.State(ctx).Active })
	mu.Lock()
	alive = false
	mu.Unlock()
	waitFor(t, func() bool { return !m.State(ctx).Active })
}

func TestRecentIsSafeToReadWhileTheDrainWrites(t *testing.T) {
	// Run under -race. The drain goroutine appends while a handler reads,
	// which is the shape of the fatal map race an external reviewer found
	// in the layout endpoint on this project.
	m, f, _ := testManager(t)
	ctx := context.Background()
	m.Toggle(ctx, 51)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			f.offer(model.CapturedStatement{Kind: "batch", Text: "x"})
		}
	}()
	for i := 0; i < 500; i++ {
		_ = m.Recent()
		_ = m.State(ctx)
	}
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/capture/`
Expected: FAIL, the package does not exist.

- [ ] **Step 3: Write the manager**

```go
// Package capture owns the life of one statement capture: when it starts,
// the six things that end it, the goroutine that drains it, and the file it
// leaves behind.
//
// One capture at a time. Two would double the dispatch cost on the monitored
// workload and complicate the panel for no diagnostic gain.
package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/outdir"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

const recentKept = 1000

type Manager struct {
	src source.Capturer

	// Seams. Package fields rather than options: the tests need them and
	// nothing else does. Spec section 2.1 on knobs that have not earned
	// themselves.
	dir      func() (string, error)
	interval time.Duration
	cap      time.Duration

	mu      sync.Mutex
	run     *running
	last    model.CaptureState
	watched func(spid int64) (time.Time, bool)
}

type running struct {
	handle  source.CaptureHandle
	file    *os.File
	path    string
	cancel  context.CancelFunc
	done    chan struct{}
	login   time.Time
	started time.Time

	mu      sync.Mutex // guards everything below; held only across the copy
	recent  []model.CapturedStatement
	mark    int64
	missed  int64
	dropped int64
	unknown bool
}

func New(c source.Capturer) *Manager {
	return &Manager{
		src:      c,
		dir:      func() (string, error) { return outdir.Beside("traces") },
		interval: 2 * time.Second,
		cap:      10 * time.Minute,
	}
}

// Watch supplies the question the manager cannot answer itself: is this
// session still the one we started on. It returns the session's login time
// and whether it exists at all.
func (m *Manager) Watch(f func(spid int64) (time.Time, bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watched = f
}

// Toggle starts a capture on spid, or stops the running one if it is already
// watching that session. A capture on another session replaces it.
func (m *Manager) Toggle(ctx context.Context, spid int64) error {
	m.mu.Lock()
	current := m.run
	m.mu.Unlock()

	if current != nil && current.handle.SessionID == spid {
		m.Stop(ctx, model.StopByKey)
		return nil
	}
	if current != nil {
		m.Stop(ctx, model.StopByKey)
	}
	return m.start(ctx, spid)
}

func (m *Manager) start(ctx context.Context, spid int64) error {
	dir, err := m.dir()
	if err != nil {
		return err
	}
	h, err := m.src.StartCapture(ctx, spid)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("capture-%d-%s", spid, time.Now().Format("2006-01-02-150405"))
	f, path, err := outdir.Create(dir, base, ".jsonl")
	if err != nil {
		m.src.StopCapture(ctx, h)
		return err
	}

	m.mu.Lock()
	watched := m.watched
	m.mu.Unlock()

	var login time.Time
	if watched != nil {
		login, _ = watched(spid)
	}

	r := &running{
		handle: h, file: f, path: path,
		login: login, started: time.Now(),
		done: make(chan struct{}),
	}
	writeRecord(f, map[string]any{
		"kind": "header", "session_id": spid, "started_at": r.started,
		"login_time": login, "event_session": h.Name,
	})

	dctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	m.mu.Lock()
	m.run = r
	m.last = model.CaptureState{}
	m.mu.Unlock()

	go m.drain(dctx, r, watched)
	return nil
}

// drain is the only writer of r's fields. It runs whether or not the panel
// is open: a capture nobody drains fills its buffer and loses events
// silently, so draining is not conditional on anybody looking.
func (m *Manager) drain(ctx context.Context, r *running, watched func(int64) (time.Time, bool)) {
	defer close(r.done)
	t := time.NewTicker(m.interval)
	defer t.Stop()
	deadline := time.After(m.cap)

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			go m.Stop(context.Background(), model.StopByTimeCap)
			return
		case <-t.C:
		}

		if watched != nil {
			login, ok := watched(r.handle.SessionID)
			if !ok {
				go m.Stop(context.Background(), model.StopBySessionGone)
				return
			}
			if !r.login.IsZero() && !login.IsZero() && !login.Equal(r.login) {
				go m.Stop(context.Background(), model.StopBySessionReused)
				return
			}
		}

		r.mu.Lock()
		mark := r.mark
		r.mu.Unlock()

		got, prog, err := m.src.PollCapture(ctx, r.handle, mark)
		if err != nil {
			// A lost connection is repaired elsewhere; the next tick tries
			// again. The cap is what ends a capture whose server never
			// comes back.
			continue
		}

		r.mu.Lock()
		if prog.Missed > 0 {
			r.missed += prog.Missed
			writeRecord(r.file, map[string]any{"kind": "gap", "lost": prog.Missed})
		}
		if prog.Truncated {
			r.unknown = true
			writeRecord(r.file, map[string]any{"kind": "gap", "lost": nil,
				"reason": "the read exceeded the four megabyte limit, so what was lost cannot be counted"})
		}
		r.dropped = prog.Dropped
		r.mark = prog.Total
		for _, s := range got {
			writeEvent(r.file, s)
			r.recent = append(r.recent, s)
		}
		if n := len(r.recent) - recentKept; n > 0 {
			r.recent = append(r.recent[:0], r.recent[n:]...)
		}
		r.mu.Unlock()
	}
}

// Stop ends the running capture. Safe to call when none is running, and safe
// to call twice: the second is a no-op.
func (m *Manager) Stop(ctx context.Context, reason model.StopReason) {
	m.mu.Lock()
	r := m.run
	m.run = nil
	if r == nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	r.cancel()
	<-r.done

	r.mu.Lock()
	state := model.CaptureState{
		SessionID: r.handle.SessionID, StartedAt: r.started,
		Stopped: reason.String(), Statements: len(r.recent),
		Missed: r.missed, Dropped: r.dropped, Unknown: r.unknown, File: r.path,
	}
	writeRecord(r.file, map[string]any{"kind": "end", "reason": reason.String()})
	r.file.Close()
	r.mu.Unlock()

	if err := m.src.StopCapture(ctx, r.handle); err != nil {
		state.Why = "the event session could not be removed: " + err.Error()
	}

	m.mu.Lock()
	m.last = state
	m.mu.Unlock()
}

// State is what the panel renders. It answers even when nothing is running,
// because "the last capture ended because the session was reused" is exactly
// what a reader needs and an empty panel is not.
func (m *Manager) State(ctx context.Context) model.CaptureState {
	m.mu.Lock()
	r, last := m.run, m.last
	m.mu.Unlock()

	if r == nil {
		last.Available = true
		return last
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return model.CaptureState{
		Available: true, Active: true,
		SessionID: r.handle.SessionID, StartedAt: r.started,
		Statements: len(r.recent), Missed: r.missed,
		Dropped: r.dropped, Unknown: r.unknown, File: r.path,
	}
}

// Recent copies the retained statements. The copy is the point: the caller
// renders at its own pace while the drain keeps appending.
func (m *Manager) Recent() []model.CapturedStatement {
	m.mu.Lock()
	r := m.run
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.CapturedStatement, len(r.recent))
	copy(out, r.recent)
	return out
}

func writeRecord(f *os.File, rec map[string]any) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
}

// writeEvent flattens the statement beside its kind, so every line of the
// file is one self-describing record and a reader needs no schema.
func writeEvent(f *os.File, s model.CapturedStatement) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	f.Write(append(append([]byte(`{"kind":"event",`), b[1:len(b)-1]...), "}\n"...))
}
```

- [ ] **Step 4: Run the tests, with the race detector**

Run: `go test -race ./internal/capture/ -v 2>&1 | tail -30`
Expected: PASS, ten tests, no race report. The race detector is not optional here: `TestRecentIsSafeToReadWhileTheDrainWrites` is meaningless without it.

- [ ] **Step 5: Prove the race test would catch a real mistake**

Remove `r.mu.Lock()` and `r.mu.Unlock()` from `Recent` and run `go test -race ./internal/capture/ -run Race`. It must report a data race. Restore the lock.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/capture
git commit -F - <<'MSG'
Own the life of a capture, including the five ways it ends badly

The drain runs whether or not anybody has the panel open, which is the point:
a capture nobody reads fills its buffer and loses events in silence, so
draining cannot be conditional on somebody looking. It writes each statement
to the file as it arrives rather than at the end, so a process killed
mid-capture still leaves a valid partial trace whose last record says it was
not a clean end.

The statements the panel reads are copied under a mutex held only across the
copy. This is the same shape as the fatal map race an external reviewer found
in the layout endpoint here, and the test that would catch it runs under the
race detector or it asserts nothing.

The session being handed to somebody else by the connection pool is the stop
condition worth naming. Nothing about the session id changes when that
happens, so the capture would go on recording a stranger's work under the
previous owner's name. The login time is what moves, and the manager watches
it every tick.
MSG
```

---

### Task 8: The endpoint

**Files:**
- Modify: `internal/web/server.go` (the `Server` gains a `*capture.Manager`, one route)
- Modify: `internal/web/views.go` (the handler)
- Modify: `internal/web/protocol.go` (the payload)
- Create: `internal/web/capture_test.go`

**Interfaces:**
- Consumes: `capture.Manager` from Task 7, `model.CaptureState`.
- Produces: `GET /api/capture` returning `{state, rows}`, and `POST /api/capture?spid=N` toggling.

- [ ] **Step 1: Write the failing test**

Follow the conventions of the existing handler tests in this package, which build a `Server` over the fake source and call `Handler()`.

```go
func TestCaptureEndpointTogglesAndReports(t *testing.T) {
	s := testServer(t) // the package's existing helper
	h := s.Handler()

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, authed(t, "POST", "/api/capture?spid=51", nil))
	if rw.Code != 200 {
		t.Fatalf("POST returned %d: %s", rw.Code, rw.Body)
	}

	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, authed(t, "GET", "/api/capture", nil))
	if rw.Code != 200 {
		t.Fatalf("GET returned %d: %s", rw.Code, rw.Body)
	}
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
	s := testServer(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, authed(t, "POST", "/api/capture", nil))
	if rw.Code != 400 {
		t.Errorf("POST with no spid returned %d, want 400", rw.Code)
	}
}

func TestCaptureEndpointSaysWhyWhenUnavailable(t *testing.T) {
	// A source that cannot capture must produce a reason the panel can
	// show, not a 500 and not an empty grid.
	s := testServerNoCapture(t)
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, authed(t, "GET", "/api/capture", nil))
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
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/web/ -run Capture`
Expected: FAIL, 404 on the route.

- [ ] **Step 3: Write the handler**

In `internal/web/views.go`:

```go
// capture is the c command: POST toggles a capture on one session, GET
// reports what it has seen. Spec section 9 of the capture design.
//
// A source that cannot capture answers with a reason rather than an error,
// because the panel has to be able to say why the key did nothing.
func (s *Server) capture(rw http.ResponseWriter, req *http.Request) {
	if s.captures == nil {
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
		if err := s.captures.Toggle(ctx, spid); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
	}

	rows := s.captures.Recent()
	if rows == nil {
		rows = []model.CapturedStatement{}
	}
	writeJSON(rw, map[string]any{"state": s.captures.State(ctx), "rows": rows})
}
```

In `internal/web/server.go`, add `captures *capture.Manager` to `Server`, a setter or constructor argument matching the package's existing style, and the route beside the others:

```go
		{path: "/api/capture", handler: s.token(http.HandlerFunc(s.capture))},
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/web/ -run Capture -v 2>&1 | tail -20`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./... && go test ./... 2>&1 | tail -10
git add internal/web/views.go internal/web/server.go internal/web/protocol.go internal/web/capture_test.go
git commit -F - <<'MSG'
Expose the capture on one endpoint that always answers

A source that cannot capture returns a reason and an empty list rather than an
error, because the panel's job is to say why the key did nothing. Answering a
request for an unavailable feature with a 500 turns a missing permission into
a bug report.
MSG
```

---

### Task 9: The capture view in the column catalogue

**Files:**
- Modify: `internal/model/columns.go`
- Modify: `internal/model/columns_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: a `ViewDef` with `ID: "capture"` and no `Key`, following `history` and `sessionwaits`, whose keys live in the JavaScript rather than the catalogue.

- [ ] **Step 1: Write the failing test**

```go
func TestCaptureViewIsInTheCatalogue(t *testing.T) {
	v, ok := ViewByID("capture")
	if !ok {
		t.Fatal("the capture view is not in the catalogue, so its columns cannot be configured like every other view")
	}
	if v.Key != "" {
		t.Errorf("the capture view claims tab key %q; it is a detail panel, and its key lives in the interface", v.Key)
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
Expected: FAIL, `ViewByID("capture")` returns false.

- [ ] **Step 3: Add the view**

In the `ViewCatalogue` slice in `internal/model/columns.go`, after the `sessionwaits` entry:

```go
	{ID: "capture", Title: "captured statements", Columns: []Column{
		{Field: "at", Title: "time", Width: 92, Default: true},
		{Field: "kind", Title: "kind", Width: 52, Default: true},
		{Field: "database", Title: "database", Width: 110, Default: true},
		// Sub-millisecond statements are the ones this feature exists to
		// show, so the millisecond columns carry decimals rather than
		// rounding a real duration to a zero.
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

- [ ] **Step 4: Run the model tests**

Run: `go test ./internal/model/`
Expected: PASS. The catalogue's own invariant tests (unique ids, unique keys, at least one default column) cover the new entry automatically.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/model/columns.go internal/model/columns_test.go
git commit -F - <<'MSG'
Put the captured statements in the column catalogue

It is a grid like every other one, so its columns reorder and hide through the
same mechanism and persist in the same file. The millisecond columns keep
decimals: a four hundred microsecond batch is exactly the statement somebody
opens this panel to find, and rounding it to zero would hide it.
MSG
```

---

### Task 10: The panel and the key

**Files:**
- Modify: `internal/web/assets/app.js`
- Modify: `internal/web/assets/style.css` if the header needs a rule
- Modify: `internal/web/e2e_test.go`

**Interfaces:**
- Consumes: `/api/capture` from Task 8, the `capture` view from Task 9.
- Produces: `CELL_CAPTURE`, an entry in `CELLS`, an entry in `DETAIL_SOURCE`, `KEYS.c`, and a `COMMANDS` row.

- [ ] **Step 1: Write the failing browser test**

Follow the conventions of the existing geometric tests in `e2e_test.go`, and read `CLAUDE.md` on this file first: break the thing an assertion asserts and watch it fail, because two of the first four assertions written here passed against a deliberately broken page.

```go
func TestCapturePanelOpensAndShowsItsState(t *testing.T) {
	// ... the package's existing browser harness setup ...
	// Press c with a row selected.
	page.Keyboard("c")
	page.WaitFor("#detail:not([hidden])")

	head := page.Text("#detailHead")
	if head == "" {
		t.Fatal("the capture panel opened with no header; a running capture with no explanation reads as broken")
	}
	if !strings.Contains(head, "capture") {
		t.Errorf("header says %q, want it to name the capture", head)
	}

	// The panel is a grid like the others, so it must be laid out, not
	// collapsed. The bug class this catches has shipped here twice.
	w := page.Number("document.querySelector('#detailList thead th').getBoundingClientRect().width")
	if w < 40 {
		t.Errorf("the first capture column is %g px wide; the table did not lay out", w)
	}
	rowsHeight := page.Number("document.querySelector('#detailList').getBoundingClientRect().height")
	if rowsHeight < 20 {
		t.Errorf("the capture table is %g px tall", rowsHeight)
	}
}

func TestCaptureKeyIsInTheHelp(t *testing.T) {
	// ... harness ...
	page.Keyboard("h")
	page.WaitFor("#help:not([hidden])")
	if !strings.Contains(page.Text("#helpList"), "capture") {
		t.Error("c is bound but absent from the help; a key nobody can discover is a key nobody uses")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/web/ -run Capture -v 2>&1 | tail -20`
Expected: FAIL, or SKIP if chromium and deno are absent. If it skips, install them or run the task on a machine that has them; a skipped browser test proves nothing.

- [ ] **Step 3: Add the cell table**

Beside the other `CELL_` tables in `app.js`:

```js
const CELL_CAPTURE = {
  at: (r) => fClock(r.at),
  kind: (r) => r.kind,
  database: (r) => r.database || "",
  // Microseconds to milliseconds with two decimals: a 400 us batch is the
  // one this panel exists to show, and 0 would hide it.
  duration_ms: (r) => (r.duration_us / 1000).toFixed(2),
  cpu_ms: (r) => (r.cpu_us / 1000).toFixed(2),
  logical_reads: (r) => r.logical_reads,
  writes: (r) => r.writes,
  rows: (r) => r.row_count,
  result: (r) => r.result || "",
  object: (r) => r.object || "",
  application: (r) => r.application || "",
  user: (r) => r.user || "",
  text: (r) => r.text || "",
};
```

Add `capture: CELL_CAPTURE` to `CELLS`.

- [ ] **Step 4: Add the detail source**

In `DETAIL_SOURCE`:

```js
  capture: {
    path: "/api/capture",
    view: "capture",
    needsRequest: true,
    heading: (spid, rows, j) => captureHead(j.state, rows.length),
  },
```

And the header function, which is where the honesty lives:

```js
// captureHead says the state in one line and never leaves the reader
// guessing. An empty table with no explanation reads as a broken feature.
function captureHead(st, n) {
  if (!st) return "capture";
  if (!st.available) return "capture unavailable: " + (st.why || "not supported here");
  if (!st.active) {
    return st.stopped
      ? "capture ended, " + st.stopped + ", " + n + " statement" + (n === 1 ? "" : "s")
      : "press c on a row to capture that session's statements";
  }
  let head = "capturing spid " + st.session_id + " for " + fDur((Date.now() - Date.parse(st.started_at)) / 1000)
    + ", " + n + " statement" + (n === 1 ? "" : "s");
  if (n === 0) head += " so far; the session is idle";
  // Two different losses, never collapsed into one number.
  if (st.missed) head += ", " + st.missed + " missed between reads";
  if (st.unknown) head += ", and an uncounted gap";
  if (st.dropped) head += ", " + st.dropped + " dropped by the server";
  if (st.others && st.others.length) {
    head += " (also captured here: " + st.others.map((o) => "spid " + o.SessionID).join(", ") + ")";
  }
  return head;
}
```

- [ ] **Step 5: Bind the key**

In `KEYS`, add `c: toggleCapture,` and write it beside the other command functions:

```js
// toggleCapture asks the server to start or stop, then opens the panel. The
// server owns the decision: pressing c on another row replaces the capture
// rather than adding one.
async function toggleCapture() {
  const spid = selectedSpid();
  if (spid === null) return;
  await post("/api/capture?spid=" + encodeURIComponent(spid));
  if (detailMode !== "capture") setDetail("capture");
  pollDetail();
}
```

In `COMMANDS`, add the row so the help lists it:

```js
  ["c", "capture every statement the selected session runs, into traces/", "capture"],
```

- [ ] **Step 6: Run the browser tests and the linter**

```bash
deno lint internal/web/assets/app.js
go test ./internal/web/ -run Capture -v 2>&1 | tail -20
```
Expected: lint clean, tests PASS.

- [ ] **Step 7: Prove each assertion fails when its subject is broken**

For `TestCapturePanelOpensAndShowsItsState`, temporarily make `captureHead` return the empty string and confirm the test FAILS. For `TestCaptureKeyIsInTheHelp`, temporarily remove the `COMMANDS` row and confirm it FAILS. Restore both. Do not skip this step: it is the rule in `CLAUDE.md` and it exists because assertions here have passed against a deliberately broken page.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go vet ./... && deno lint internal/web/assets/app.js
git add internal/web/assets/app.js internal/web/assets/style.css internal/web/e2e_test.go
git commit -F - <<'MSG'
Show the capture, including what it did not see

The header is most of this. It says which session is being captured and for
how long, and when the count is zero it says the session is idle rather than
showing an empty table that reads as a broken feature. The two kinds of loss
stay separate wherever they appear: what passed through the buffer between two
reads is a different problem from what the server dropped before the buffer,
and one number covering both would hide which is happening.

Durations are shown to two decimal places of a millisecond because the source
is microseconds and the short statements are the ones somebody opens this
panel to find.
MSG
```

---

### Task 11: The three document corrections

The design ships with an amendment to the project's own specification, and shipping the code without it would leave the specification saying one thing and the program doing another.

**Files:**
- Modify: `docs/SPECS.md` (section 2, section 7's table, section 12, section 13)
- Modify: `CLAUDE.md` (the hard constraints)
- Modify: `docs/PERFORMANCE.md` (the limitation)

**Interfaces:** none.

- [ ] **Step 1: Amend section 7's key table**

The `Waits` row currently reads "Two sub-modes toggled with `c`". Change it to name a free key, and add a note under the table:

```
| Waits | `w` | Two sub-modes toggled with `g`: current waits per request, and cumulative wait statistics differentiated over the window | `sys.dm_os_waiting_tasks`, `sys.dm_os_wait_stats` |
```

Under the table, in prose:

> The waits sub-mode toggle was `c` until the statement capture took that key.
> The capture ships and the waits view does not, and a mnemonic that works is
> worth more on a key somebody presses than on one nobody can yet.

- [ ] **Step 2: Qualify section 12**

Change "No configuration of the monitored server. The tool reads." to:

> No persistent configuration of the monitored server: no setting, no trace
> flag, no `sp_configure` value, nothing that outlives the run. The scoped
> statement capture of section 2 is the one thing this tool creates, it is
> opt-in behind a flag, it exists only while somebody is watching it, and it
> is removed when they stop. Everything else reads.

- [ ] **Step 3: Correct `CLAUDE.md`**

Change the hard constraint from "Read-only on the monitored server. No object created, nothing configured, no trace flag set." to:

```
- Read-only on the monitored server, with one stated exception. No object
  created, nothing configured, no trace flag set. The exception is the scoped
  statement capture of `docs/SPECS.md` section 2, which creates one named
  Extended Events session, only behind the `-capture` flag, only while
  somebody is watching, and removes it when they stop. Without the flag the
  tool creates and drops nothing at all, the recovery sweep included.
```

- [ ] **Step 4: Record the measurement limitation**

Append to `docs/PERFORMANCE.md`:

> ## What the budget cannot see
>
> The observation budget measures the CPU of the tool's own session. Extended
> Events dispatch does not run there: predicate evaluation and event
> construction happen on the thread of the workload being watched. The
> statement capture is therefore the first feature in this tool whose cost its
> own instrument cannot report.
>
> The predicate is one integer comparison per candidate event, and the two
> events chosen fire once per batch rather than once per statement, so the
> expected cost is small. Expected is not measured. Measure it against the
> containers before relying on the number, and record it here beside the
> others.

- [ ] **Step 5: Move the item in section 13**

Section 13 lists "Extended Events capture, opt-in, for the short history of completed queries that the DMVs cannot give" as a later version. Remove that line: it has arrived.

- [ ] **Step 6: Check the documents against each other**

Run: `grep -n "No object created\|No configuration of the monitored\|toggled with" docs/SPECS.md CLAUDE.md`
Expected: no occurrence contradicts the feature as built.

- [ ] **Step 7: Commit**

```bash
git add docs/SPECS.md CLAUDE.md docs/PERFORMANCE.md
git commit -F - <<'MSG'
Say in the rules what the capture actually does

Section 2 authorised this feature and deferred it, but two other sentences
were written as though it never would arrive: CLAUDE.md's flat "no object
created", and section 12's "no configuration of the monitored server". Both
now say what is true, which is that one named session is created behind a flag
while somebody watches it and removed when they stop, and that without the
flag nothing is created or dropped at all.

The key is the honest half. Section 7 reserved c for a sub-mode of the waits
view, and the capture took it. That is a change of mind, not a discovery that
the key was free, and it is recorded as one so the waits view finds its
toggle waiting for it rather than a collision.

PERFORMANCE.md gains the limitation that matters more than any number in it:
event dispatch runs on the monitored workload's threads, where this tool's own
budget cannot see it.
MSG
```

---

### Task 12: Wire the flag end to end

Nothing above runs until this task. The flag reaches the source, the manager reaches the server, and the manager learns how to ask whether the watched session is still the one it started on.

**Files:**
- Modify: `cmd/sqltop/main.go`
- Modify: `internal/source/mssql/mssql.go` (accept the flag, probe the capability in `Identify`)
- Modify: `internal/web/server.go` (build the manager, wire `Watch`, stop on shutdown and on the browser leaving)
- Modify: `internal/web/capture_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `-capture` on the command line, `model.CapCaptureSession` in the capability set, a `*capture.Manager` on the server.

- [ ] **Step 1: Write the failing test**

```go
func TestWithoutTheFlagNothingIsEverCreated(t *testing.T) {
	// The whole read-only guarantee in one test: with the flag off, the
	// capability is absent, the key does nothing, and the sweep does not run.
	s := testSourceNoFlag(t)
	_, caps, err := s.Identify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Has(model.CapCaptureSession) {
		t.Fatal("the capture capability is present without the flag")
	}
	before := countEventSessions(t, s)
	if _, err := s.SweepCaptures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countEventSessions(t, s); got != before {
		t.Errorf("event session count moved from %d to %d without the flag", before, got)
	}
}

func TestTheCapabilityAppearsWithTheFlag(t *testing.T) {
	s := testSource(t)
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
	// A capture that survives the process is exactly the residue this whole
	// design is arranged around not producing.
	m, f, _ := testManagerFromServer(t)
	m.Toggle(context.Background(), 51)
	closeTheServer(t)
	if len(f.stopped) != 1 {
		t.Error("shutting down left the event session running on the server")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ ./internal/web/ -run 'Flag|Capability|Shutdown'`
Expected: FAIL.

- [ ] **Step 3: Add the flag**

In `cmd/sqltop/main.go`, beside the others:

```go
	capture := flag.Bool("capture", false, "allow the c command to create a scoped Extended Events session on the monitored server; without this the tool creates and drops nothing")
```

Pass it into the source when it is constructed, following how the existing options reach it.

- [ ] **Step 4: Probe the capability**

In `Identify`, after the existing probes, and only when `s.captureAllowed`:

```go
	if s.captureAllowed {
		if ok, _, err := s.CanCapture(ctx); err == nil && ok {
			caps |= model.Capabilities(model.CapCaptureSession)
		}
	}
```

Then, still in `Open` or immediately after `Identify`, run the recovery sweep once, and only with the flag. Log what it dropped rather than swallowing it: a tool that silently deletes objects on a server is worse than one that does not.

- [ ] **Step 5: Wire the manager**

In `internal/web/server.go`, when the source implements `source.Capturer` and the flag is on, build the manager and give it the question it cannot answer itself:

```go
	if c, ok := src.(source.Capturer); ok {
		s.captures = capture.New(c)
		// The manager needs to know whether the watched session is still
		// the one it started on. The window already holds every session
		// sample, so this is a lookup rather than a query.
		s.captures.Watch(func(spid int64) (time.Time, bool) {
			return s.window.SessionLogin(spid)
		})
	}
```

Add `Window.SessionLogin(spid int64) (time.Time, bool)` in `internal/window/window.go` if it does not exist, reading the latest session sample under the window's existing mutex.

Stop the capture on shutdown, in whatever the server's existing `Close` or shutdown path is:

```go
	if s.captures != nil {
		s.captures.Stop(context.Background(), model.StopByShutdown)
	}
```

And on the browser leaving. The stream already knows when a client disconnects; when the count of live streams reaches zero, start a thirty second timer, and stop the capture if no client has returned when it fires. Thirty seconds so a page reload does not kill a capture.

- [ ] **Step 6: Run everything**

```bash
eval "$(scripts/testdb.sh)"
go test -race ./... 2>&1 | tail -20
deno lint internal/web/assets/app.js
gofmt -l . && go vet ./...
```
Expected: PASS everywhere, no race, lint clean.

- [ ] **Step 7: Drive it by hand, which is the only test that covers the whole path**

```bash
go build -o /tmp/sqltop ./cmd/sqltop
eval "$(scripts/testdb.sh)"
SQLTOP_CONN="$SQLTOP_TEST_DSN" /tmp/sqltop -capture
```

With a workload running, select a row, press `c`, and check all of this: the panel opens and names the session; statements appear within a few seconds; `traces/` beside `/tmp/sqltop` holds a `.jsonl` whose first line is a header; pressing `c` again stops it and the header says so; the file's last line is an end record. Then kill the process with `kill -9` mid-capture, confirm with sqlcmd that the event session is still on the server, restart with `-capture`, and confirm the sweep removed it. Finally run once without `-capture` and confirm `c` reports that the flag is needed and that no event session is created.

- [ ] **Step 8: Commit**

```bash
git add cmd/sqltop/main.go internal/source/mssql/mssql.go internal/web/server.go internal/web/capture_test.go internal/window/window.go
git commit -F - <<'MSG'
Put the whole capture behind one flag, and prove it stays there

Without -capture the capability is absent, the key says why it did nothing,
and the recovery sweep does not run either, since the sweep is itself a DROP
and has no business on a server whose operator did not ask for any of this.
The test for that counts the event sessions on the server before and after and
fails if the number moves.

The manager cannot tell on its own whether the session it is watching is still
the same one, so the server hands it that question and answers it from the
retention window, which already holds every session sample. Shutdown stops any
running capture: a session that outlives the process is precisely the residue
this design is arranged around not producing, and leaving it to the sweep for
the one case we can see coming would mean never testing the sweep.
MSG
```

---

## Self-review

**Spec coverage.** Every section of the design maps to a task: section 3 to Tasks 2 and 3, section 4 to Task 7, section 5 to Task 5, section 6 to Tasks 4 and 6, section 7 to Tasks 3 and 7, section 8 to Tasks 1 and 7, section 9 to Tasks 9 and 10, section 10 to Task 11, section 11 across every task's tests, section 12's exclusions by not building them. Section 2's document corrections are Task 11.

**Two things the design names that this plan does not build, deliberately.** Failover detection has no task: `model.StopByFailover` exists and nothing sets it, because detecting a failover reliably needs more than this feature should carry, and the stop reason is there for whoever adds it. Say so in the Task 11 commit rather than leaving a reason nothing produces. The `Others` field of `CaptureState` is populated by `RunningCaptures`, which Task 5 builds and Task 8's handler must call; if it does not, the panel's "also captured here" clause never fires. Task 8 should call it.

**Type consistency.** `parseRingBuffer` takes `(doc string, mark int64)` in Task 3 and is called that way in Task 6. `PollCapture` takes `mark` in the interface in Task 3, the implementation in Task 6, and the manager in Task 7. `CaptureNote` has exported fields `SessionID` and `Since`, used by the JavaScript in Task 10 as `o.SessionID`, so it must be serialised with those names or given JSON tags; give it `json:"session_id"` and `json:"since"` in Task 2 and use `o.session_id` in Task 10.
