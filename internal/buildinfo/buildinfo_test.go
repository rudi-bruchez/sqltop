package buildinfo

import (
	"strings"
	"testing"
)

func TestVersionStartsAtZeroOne(t *testing.T) {
	if !strings.HasPrefix(Version, "0.1.") {
		t.Fatalf("Version = %q, want the 0.1 series", Version)
	}
}

func TestStringAlwaysCarriesTheVersion(t *testing.T) {
	got := String()
	if !strings.Contains(got, Version) {
		t.Fatalf("String() = %q, want it to contain %q", got, Version)
	}
}

func TestRevisionDoesNotPanicWithoutBuildInfo(t *testing.T) {
	// go test builds without VCS stamping in some configurations, so this
	// must degrade to an empty revision rather than failing.
	rev, _ := Revision()
	if strings.Contains(rev, " ") {
		t.Fatalf("revision = %q, want a bare hash or nothing", rev)
	}
}
