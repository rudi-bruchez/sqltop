package window

import (
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

func sample(spid int64, cpu int64) model.RequestSample {
	return model.RequestSample{Ref: model.RequestRef{SessionID: spid}, CPUMs: cpu}
}

func TestLatestReturnsTheMostRecentTick(t *testing.T) {
	w := New(time.Minute, 1000)
	t0 := time.Now()

	w.Append(t0, []model.RequestSample{sample(51, 10), sample(52, 20)})
	w.Append(t0.Add(time.Second), []model.RequestSample{sample(51, 30)})

	got := w.Latest()
	if len(got) != 1 || got[0].CPUMs != 30 {
		t.Fatalf("Latest() = %+v, want the single row of the second tick", got)
	}
}

func TestHistoryReplaysOneRequest(t *testing.T) {
	w := New(time.Minute, 1000)
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		w.Append(t0.Add(time.Duration(i)*time.Second), []model.RequestSample{sample(51, int64(i*100))})
	}

	got := w.History(model.RequestRef{SessionID: 51})
	if len(got) != 5 {
		t.Fatalf("History() returned %d samples, want 5", len(got))
	}
	for i, s := range got {
		if s.CPUMs != int64(i*100) {
			t.Fatalf("sample %d has CPUMs %d, want %d: history must stay in order", i, s.CPUMs, i*100)
		}
	}
}

func TestEvictsByAge(t *testing.T) {
	w := New(10*time.Second, 1000)
	t0 := time.Now()

	w.Append(t0, []model.RequestSample{sample(51, 1)})
	w.Append(t0.Add(30*time.Second), []model.RequestSample{sample(51, 2)})

	if got := w.History(model.RequestRef{SessionID: 51}); len(got) != 1 || got[0].CPUMs != 2 {
		t.Fatalf("History() = %+v, want only the sample inside the retention period", got)
	}
}

func TestEvictsByCountAndReportsCapped(t *testing.T) {
	w := New(time.Hour, 10)
	t0 := time.Now()
	for i := 0; i < 25; i++ {
		w.Append(t0.Add(time.Duration(i)*time.Second), []model.RequestSample{sample(51, int64(i))})
	}

	_, samples, capped := w.Depth()
	if samples != 10 {
		t.Fatalf("window holds %d samples, want exactly the cap of 10: one sample per tick means eviction lands on the boundary", samples)
	}
	if !capped {
		t.Fatal("Depth() must report capped=true once the count limit has bitten, so the UI can say the window is shorter than asked")
	}

	got := w.History(model.RequestRef{SessionID: 51})
	if len(got) == 0 || got[len(got)-1].CPUMs != 24 {
		t.Fatal("eviction must drop the oldest samples, never the newest")
	}
}

func TestDepthOnEmptyWindow(t *testing.T) {
	_, samples, capped := New(time.Minute, 100).Depth()
	if samples != 0 || capped {
		t.Fatalf("empty window reported %d samples capped=%v", samples, capped)
	}
}

// TestSessionStatementsGroupsWhatASessionRan is the feature that finally
// reaches the retention window. Spec section 12 justifies the window by a
// query that finished thirty seconds ago still being inspectable, and until
// this nothing read it back.
func TestSessionStatementsGroupsWhatASessionRan(t *testing.T) {
	w := New(time.Hour, 10000)
	base := time.Now().Add(-time.Minute)

	sample := func(at time.Time, spid int64, text, wait string, cpu int64) model.RequestSample {
		return model.RequestSample{
			At: at, Ref: model.RequestRef{SessionID: spid},
			Login: "app", Host: "APP01", Program: "svc", Database: "CRM", Command: "SELECT",
			SQLText: text, WaitType: wait, CPUMs: cpu, ElapsedMs: cpu * 2, LogicalReads: cpu,
		}
	}

	// Session 51 runs one statement for three ticks, then another for one.
	// Session 52 runs its own, and must not be folded in.
	w.Append(base, []model.RequestSample{sample(base, 51, "SELECT a", "LCK_M_X", 10), sample(base, 52, "SELECT z", "", 1)})
	w.Append(base.Add(time.Second), []model.RequestSample{sample(base.Add(time.Second), 51, "SELECT a", "LCK_M_X", 40)})
	w.Append(base.Add(2*time.Second), []model.RequestSample{sample(base.Add(2*time.Second), 51, "SELECT a", "PAGEIOLATCH_SH", 90)})
	w.Append(base.Add(3*time.Second), []model.RequestSample{sample(base.Add(3*time.Second), 51, "SELECT b", "", 5)})

	got := w.SessionStatements(51)
	if len(got) != 2 {
		t.Fatalf("session 51 ran two distinct statements and %d came back", len(got))
	}
	// Most recently seen first.
	if got[0].SQLText != "SELECT b" {
		t.Errorf("the first row is %q; the most recently seen statement comes first", got[0].SQLText)
	}

	a := got[1]
	if a.Samples != 3 {
		t.Errorf("SELECT a was seen in three ticks and reports %d", a.Samples)
	}
	if a.MaxCPUMs != 90 {
		t.Errorf("SELECT a peaked at 90 ms of CPU and reports %d", a.MaxCPUMs)
	}
	if a.LastAt.Sub(a.FirstAt) != 2*time.Second {
		t.Errorf("SELECT a spans %v, want two seconds", a.LastAt.Sub(a.FirstAt))
	}
	// Two samples on LCK_M_X against one on PAGEIOLATCH_SH.
	if a.TopWait != "LCK_M_X" || a.TopWaitSamples != 2 {
		t.Errorf("SELECT a waited most on %q in %d samples, want LCK_M_X in 2", a.TopWait, a.TopWaitSamples)
	}
}

// TestSessionStatementsKeepsDifferentLoginsApart. SQL Server reuses session
// ids freely, so two unrelated logins can hold the same number inside one
// window. Folding them into one history would invent something that never
// happened.
func TestSessionStatementsKeepsDifferentLoginsApart(t *testing.T) {
	w := New(time.Hour, 10000)
	at := time.Now()
	w.Append(at, []model.RequestSample{
		{At: at, Ref: model.RequestRef{SessionID: 51}, Login: "alice", Host: "PC1", Program: "SSMS", SQLText: "SELECT 1"},
	})
	w.Append(at.Add(time.Second), []model.RequestSample{
		{At: at.Add(time.Second), Ref: model.RequestRef{SessionID: 51}, Login: "bob", Host: "PC2", Program: "sqlcmd", SQLText: "SELECT 1"},
	})

	got := w.SessionStatements(51)
	if len(got) != 2 {
		t.Fatalf("the same text under two logins folded into %d row(s); it is two", len(got))
	}
	logins := map[string]bool{got[0].Login: true, got[1].Login: true}
	if !logins["alice"] || !logins["bob"] {
		t.Errorf("the two rows report logins %q and %q", got[0].Login, got[1].Login)
	}
}
