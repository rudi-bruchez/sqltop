package window

import (
	"sort"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

func row(spid, blockedBy int64) model.RequestSample {
	return model.RequestSample{Ref: model.RequestRef{SessionID: spid}, BlockedBy: blockedBy}
}

func order(rows []model.RequestSample) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Ref.SessionID
	}
	return out
}

func depths(rows []model.RequestSample) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Depth
	}
	return out
}

func TestFlattenSimpleChain(t *testing.T) {
	// 51 blocks 52, which blocks 53.
	got := Flatten([]model.RequestSample{row(53, 52), row(51, 0), row(52, 51)})

	if want := []int64{51, 52, 53}; !equal64(order(got), want) {
		t.Fatalf("order = %v, want %v: a blocker must sit above what it blocks", order(got), want)
	}
	if want := []int{0, 1, 2}; !equalInt(depths(got), want) {
		t.Fatalf("depths = %v, want %v", depths(got), want)
	}
}

func TestFlattenTree(t *testing.T) {
	// 51 blocks both 52 and 53; 53 blocks 54.
	got := Flatten([]model.RequestSample{row(51, 0), row(52, 51), row(53, 51), row(54, 53)})

	if want := []int64{51, 52, 53, 54}; !equal64(order(got), want) {
		t.Fatalf("order = %v, want %v", order(got), want)
	}
	if want := []int{0, 1, 1, 2}; !equalInt(depths(got), want) {
		t.Fatalf("depths = %v, want %v", depths(got), want)
	}
}

func TestFlattenSurvivesACycle(t *testing.T) {
	// 51 blocked by 52, 52 blocked by 51. SQL Server can briefly report this.
	got := Flatten([]model.RequestSample{row(51, 52), row(52, 51)})

	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: a cycle must not drop or duplicate rows", len(got))
	}
	if want := []int64{51, 52}; !equal64(sortedIDs(got), want) {
		t.Fatalf("session ids = %v, want %v: every row exactly once", sortedIDs(got), want)
	}
}

func TestFlattenSelfBlock(t *testing.T) {
	got := Flatten([]model.RequestSample{row(51, 51)})

	if len(got) != 1 || got[0].Depth != 0 {
		t.Fatalf("got %+v, want one row at depth 0: a session cannot be its own child", got)
	}
}

func TestFlattenOrphanBlocker(t *testing.T) {
	// 52 says it is blocked by 51, but 51 is not in the sample: its request
	// finished between the two reads. 52 must still appear, at the root.
	got := Flatten([]model.RequestSample{row(52, 51)})

	if len(got) != 1 || got[0].Depth != 0 {
		t.Fatalf("got %+v, want the orphan at depth 0 rather than dropped", got)
	}
}

func TestFlattenKeepsUnblockedRows(t *testing.T) {
	got := Flatten([]model.RequestSample{row(51, 0), row(52, 0), row(53, 0)})
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if want := []int64{51, 52, 53}; !equal64(sortedIDs(got), want) {
		t.Fatalf("session ids = %v, want %v: every row exactly once", sortedIDs(got), want)
	}
}

func equal64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInt(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFlattenKeepsBothRequestsOfOneSession(t *testing.T) {
	// MARS: one session, two concurrent requests. Keying identity by session
	// id alone would silently drop one of them.
	rows := []model.RequestSample{
		{Ref: model.RequestRef{SessionID: 51, RequestID: 0}},
		{Ref: model.RequestRef{SessionID: 51, RequestID: 1}, BlockedBy: 52},
		{Ref: model.RequestRef{SessionID: 52, RequestID: 0}},
	}
	got := Flatten(rows)

	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: a request must never vanish from the grid", len(got))
	}
	var reqs []int32
	for _, r := range got {
		if r.Ref.SessionID == 51 {
			reqs = append(reqs, r.Ref.RequestID)
		}
	}
	if len(reqs) != 2 {
		t.Fatalf("session 51 appears %d times, want 2", len(reqs))
	}
}

func TestFlattenThreeNodeCycle(t *testing.T) {
	got := Flatten([]model.RequestSample{row(51, 53), row(52, 51), row(53, 52)})
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if want := []int64{51, 52, 53}; !equal64(sortedIDs(got), want) {
		t.Fatalf("session ids = %v, want %v: every row exactly once", sortedIDs(got), want)
	}
}

func TestFlattenCycleWithATail(t *testing.T) {
	// 51 and 52 block each other; 53 is blocked by 52 and hangs off the cycle.
	got := Flatten([]model.RequestSample{row(51, 52), row(52, 51), row(53, 52)})
	if want := []int64{51, 52, 53}; !equal64(sortedIDs(got), want) {
		t.Fatalf("session ids = %v, want %v", sortedIDs(got), want)
	}
}

func TestFlattenTwoIndependentCycles(t *testing.T) {
	got := Flatten([]model.RequestSample{
		row(51, 52), row(52, 51),
		row(61, 62), row(62, 61),
	})
	if want := []int64{51, 52, 61, 62}; !equal64(sortedIDs(got), want) {
		t.Fatalf("session ids = %v, want %v", sortedIDs(got), want)
	}
}

// sortedIDs returns the session ids of rows, sorted, so a test can assert the
// exact set that survived without depending on emission order.
func sortedIDs(rows []model.RequestSample) []int64 {
	out := order(rows)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
