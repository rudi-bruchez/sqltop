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
