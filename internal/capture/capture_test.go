package capture

import (
	"context"
	"encoding/json"
	"errors"
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
	mu      sync.Mutex
	started []string
	stopped []string
	queue   []model.CapturedStatement
	prog    model.CaptureProgress
	login   time.Time
	present bool
	always  bool // hand out a statement on every poll, for the race test
	fails   bool // every poll errors, for the lost server test
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
	if f.fails {
		return nil, model.CaptureProgress{Seen: mark}, errors.New("the connection is gone")
	}
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
	m := New(f, "0.5.0-test", "testhost\\TESTINSTANCE")
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
	// The fake reports the same loss on every poll and the manager adds them
	// up, so the total only equals 487 for one interval. Waiting for it to
	// reach 487 is what makes this deterministic; the file below is where
	// the exact count is asserted.
	waitFor(t, func() bool { return m.State(ctx).Missed >= 487 })

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

func TestPollsThatKeepFailingEndTheCapture(t *testing.T) {
	// One failed poll is not an ending: the source replaces a dead
	// connection by itself and the next tick succeeds.
	m, f, _ := testManager(t)
	ctx := context.Background()
	f.mu.Lock()
	f.fails = true
	f.mu.Unlock()
	m.Toggle(ctx, 51)
	waitFor(t, func() bool { return !m.State(ctx).Active })
	if got := m.State(ctx).Stopped; !strings.Contains(got, "could not be reached") {
		t.Errorf("stopped because %q, want the lost server", got)
	}
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
	m := New(f, "0.5.0-test", "testhost\\TESTINSTANCE")
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
