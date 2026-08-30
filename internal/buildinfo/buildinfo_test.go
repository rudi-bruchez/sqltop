package buildinfo

import (
	"strconv"
	"strings"
	"testing"
)

// TestVersionIsZeroMajorAndWellFormed guards what spec section 11 actually
// says: zero-major while the shape can still change. It used to pin the 0.1
// series specifically, which made cutting 0.2 fail a test that was never
// about the minor number, and which would have failed again at every
// release after that.
func TestVersionIsZeroMajorAndWellFormed(t *testing.T) {
	parts := strings.Split(Version, ".")
	if len(parts) != 3 {
		t.Fatalf("Version = %q, want three dot-separated parts", Version)
	}
	for i, p := range parts {
		if p == "" {
			t.Fatalf("Version = %q, part %d is empty", Version, i)
		}
		if _, err := strconv.Atoi(p); err != nil {
			t.Fatalf("Version = %q, part %d is not a number: %v", Version, i, err)
		}
	}
	if parts[0] != "0" {
		t.Errorf("Version = %q; spec section 11 keeps the major at zero while the shape can still change, so a one here is a decision somebody has to make on purpose", Version)
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
