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

// TestStatementsCountsRunsNotSamples is the whole difficulty of the queries
// view. The window samples, so the number of ticks a shape appeared in says
// nothing about how many times it ran, and the request id cannot stand in for
// an execution either: it is 0 on every session that is not using MARS. What
// can be counted is streaks of consecutive ticks, one per run.
func TestStatementsCountsRunsNotSamples(t *testing.T) {
	w := New(time.Hour, 10000)
	base := time.Now().Add(-time.Minute)

	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Second) }
	// elapsed is given per sample rather than derived from the CPU, because a
	// run that spans ticks has to carry an elapsed that grew with them: a
	// request seen at three one-second ticks has been running for seconds.
	row := func(i int, spid int64, hash, text string, cpu, elapsed int64) model.RequestSample {
		return model.RequestSample{
			At: at(i), Ref: model.RequestRef{SessionID: spid},
			Database: "CRM", Command: "SELECT", QueryHash: hash, SQLText: text,
			CPUMs: cpu, ElapsedMs: elapsed,
		}
	}

	// Session 51 runs the shape for three ticks, stops for one, and runs it
	// again: two runs, four samples. Session 52 runs it once in the first
	// tick. The literals differ and the hash does not, which is what folds
	// them into one row.
	w.Append(at(0), []model.RequestSample{row(0, 51, "0xAA", "SELECT a WHERE id = 1", 10, 200), row(0, 52, "0xAA", "SELECT a WHERE id = 7", 5, 100)})
	w.Append(at(1), []model.RequestSample{row(1, 51, "0xAA", "SELECT a WHERE id = 1", 40, 1200)})
	w.Append(at(2), []model.RequestSample{row(2, 51, "0xAA", "SELECT a WHERE id = 1", 90, 2200)})
	w.Append(at(3), nil)
	w.Append(at(4), []model.RequestSample{row(4, 51, "0xAA", "SELECT a WHERE id = 2", 20, 300)})

	got, total := w.Statements(0)
	if len(got) != 1 || total != 1 {
		t.Fatalf("one shape under three literals came back as %d rows of %d; the query hash is what folds them", len(got), total)
	}
	q := got[0]
	if q.Runs != 3 {
		t.Errorf("Runs = %d, want 3: two streaks on session 51 and one on session 52", q.Runs)
	}
	if q.Samples != 5 {
		t.Errorf("Samples = %d, want 5", q.Samples)
	}
	if q.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2", q.Sessions)
	}
	// The engine's counters are cumulative per request, so each run gives its
	// own maximum: 90 + 20 + 5. Summing every sample would report 165.
	if q.TotalCPUMs != 115 {
		t.Errorf("TotalCPUMs = %d, want 115: a run contributes its maximum, not the sum of its samples", q.TotalCPUMs)
	}
	if q.MaxElapsedMs != 2200 {
		t.Errorf("MaxElapsedMs = %d, want 2200", q.MaxElapsedMs)
	}
	// (2200 + 300 + 100) / 3 runs.
	if q.AvgElapsedMs != 866 {
		t.Errorf("AvgElapsedMs = %d, want 866", q.AvgElapsedMs)
	}
	if q.SQLText != "SELECT a WHERE id = 2" {
		t.Errorf("SQLText = %q, want the most recently seen literal", q.SQLText)
	}
}

// TestStatementsSplitsTwoExecutionsInAdjacentTicks is the defect a real server
// showed and the fixture above cannot: consecutive ticks are not enough to
// call two sightings one run. Under the budget governor the period stretches
// to five seconds, so a query that runs for under a second is caught once per
// execution, and two executions on one session in two neighbouring ticks were
// being folded into a single run. A request already alive at the previous tick
// must have been running at least as long as the gap between them.
func TestStatementsSplitsTwoExecutionsInAdjacentTicks(t *testing.T) {
	w := New(time.Hour, 10000)
	base := time.Now().Add(-time.Minute)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * 5 * time.Second) }

	// Two ticks five seconds apart. The same session, the same shape, and an
	// elapsed of 700 ms both times: it cannot be one execution spanning both.
	w.Append(at(0), []model.RequestSample{{
		At: at(0), Ref: model.RequestRef{SessionID: 51}, Database: "CRM",
		QueryHash: "0xAA", SQLText: "SELECT a", ElapsedMs: 700, CPUMs: 600,
	}})
	w.Append(at(1), []model.RequestSample{{
		At: at(1), Ref: model.RequestRef{SessionID: 51}, Database: "CRM",
		QueryHash: "0xAA", SQLText: "SELECT a", ElapsedMs: 700, CPUMs: 600,
	}})

	got, _ := w.Statements(0)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Runs != 2 {
		t.Errorf("Runs = %d, want 2: a 700 ms request cannot span two ticks five seconds apart", got[0].Runs)
	}
	// Two runs of 600 ms, not one: the cost follows the run count.
	if got[0].TotalCPUMs != 1200 {
		t.Errorf("TotalCPUMs = %d, want 1200", got[0].TotalCPUMs)
	}
}

// TestStatementsKeepsOneLongRunTogether is the other half of the same rule. A
// request that really is still running across ticks has an elapsed that has
// grown by at least the gap, and must stay one run however many ticks it
// spans.
func TestStatementsKeepsOneLongRunTogether(t *testing.T) {
	w := New(time.Hour, 10000)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * 5 * time.Second)
		w.Append(at, []model.RequestSample{{
			At: at, Ref: model.RequestRef{SessionID: 51}, Database: "CRM",
			QueryHash: "0xAA", SQLText: "SELECT slow",
			ElapsedMs: int64(3000 + i*5000), CPUMs: int64(100 * (i + 1)),
		}})
	}

	got, _ := w.Statements(0)
	if len(got) != 1 || got[0].Runs != 1 {
		t.Fatalf("a request running across four ticks came back as %d rows and %d runs, want 1 and 1", len(got), got[0].Runs)
	}
	if got[0].TotalCPUMs != 400 {
		t.Errorf("TotalCPUMs = %d, want 400: one run contributes its maximum", got[0].TotalCPUMs)
	}
}

// TestStatementsSeparatesDatabasesAndOrdersByCost. One shape run against two
// databases is two answers, and the row carries a database name that would
// otherwise be true of only half of what it counted.
func TestStatementsSeparatesDatabasesAndOrdersByCost(t *testing.T) {
	w := New(time.Hour, 10000)
	at := time.Now()
	w.Append(at, []model.RequestSample{
		{At: at, Ref: model.RequestRef{SessionID: 51}, Database: "CRM", QueryHash: "0xAA", SQLText: "SELECT 1", CPUMs: 10},
		{At: at, Ref: model.RequestRef{SessionID: 52}, Database: "ERP", QueryHash: "0xAA", SQLText: "SELECT 1", CPUMs: 900},
		// No hash: the engine leaves it empty for some statements, and the
		// text has to stand in or every one of them folds into a single row.
		{At: at, Ref: model.RequestRef{SessionID: 53}, Database: "CRM", SQLText: "DBCC CHECKDB", CPUMs: 500},
		{At: at, Ref: model.RequestRef{SessionID: 54}, Database: "CRM", SQLText: "BACKUP DATABASE", CPUMs: 1},
	})

	got, total := w.Statements(0)
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4: two databases for one hash, and two unhashed statements kept apart by their text", len(got))
	}
	if got[0].Database != "ERP" || got[0].TotalCPUMs != 900 {
		t.Errorf("the first row is %s at %d ms; the most expensive comes first", got[0].Database, got[0].TotalCPUMs)
	}
	if got[len(got)-1].TotalCPUMs != 1 {
		t.Errorf("the last row costs %d ms, want the cheapest at 1", got[len(got)-1].TotalCPUMs)
	}

	// The cap keeps the expensive end and still reports what it left out, so
	// the interface can say the list is a top rather than the whole window.
	capped, total := w.Statements(2)
	if len(capped) != 2 || total != 4 {
		t.Fatalf("Statements(2) returned %d rows of %d, want 2 of 4", len(capped), total)
	}
	if capped[0].TotalCPUMs != 900 {
		t.Errorf("the cap kept the row at %d ms first; it must cut the cheap end", capped[0].TotalCPUMs)
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
