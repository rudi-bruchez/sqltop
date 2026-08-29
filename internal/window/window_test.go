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
