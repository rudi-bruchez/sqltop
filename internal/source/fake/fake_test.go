package fake

import (
	"context"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

func TestFakeSatisfiesSource(t *testing.T) {
	var _ source.Source = New(nil)
}

func TestFakeAccumulatesCost(t *testing.T) {
	ctx := context.Background()
	f := New([]model.RequestSample{{Ref: model.RequestRef{SessionID: 51}}})
	f.CostPerCall = 3

	if _, err := f.SampleRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SampleRequests(ctx); err != nil {
		t.Fatal(err)
	}

	c, err := f.Cost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.CPUMs != 6 {
		t.Fatalf("cost = %d ms, want 6: Cost is cumulative, the collector differentiates it", c.CPUMs)
	}
}

func TestFakeCanFail(t *testing.T) {
	f := New(nil)
	f.Err = context.DeadlineExceeded

	if _, err := f.SampleRequests(context.Background()); err == nil {
		t.Fatal("the fake must be able to fail, so the collector's degradation path is testable")
	}
}
