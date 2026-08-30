package model

import "testing"

func TestCapabilitiesSet(t *testing.T) {
	c := Caps(CapLivePlanProgress, CapKillSession)

	if !c.Has(CapLivePlanProgress) {
		t.Error("CapLivePlanProgress should be present")
	}
	if c.Has(CapInstanceWideView) {
		t.Error("CapInstanceWideView was never added and must be absent")
	}
	if Caps().Has(CapKillSession) {
		t.Error("the empty set has nothing")
	}
}

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
