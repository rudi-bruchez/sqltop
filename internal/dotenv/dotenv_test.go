package dotenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileIsSilent(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("a missing .env must be a no-op, got %v", err)
	}
}

func TestLoadParsesAndRealEnvironmentWins(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	body := "# a comment\n" +
		"export SQLTOP_A=one\n" +
		"SQLTOP_B=\"two words\"\n" +
		"SQLTOP_C='three'\n" +
		"SQLTOP_TAKEN=from_file\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQLTOP_TAKEN", "from_environment")

	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ key, want string }{
		{"SQLTOP_A", "one"},
		{"SQLTOP_B", "two words"},
		{"SQLTOP_C", "three"},
		{"SQLTOP_TAKEN", "from_environment"},
	} {
		if got := os.Getenv(c.key); got != c.want {
			t.Errorf("%s = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestMalformedLinesAreReportedNotSwallowed is the diagnosis this parser
// used to make impossible. A colon typed instead of an equals sign, or a
// name with no value at all, was skipped in silence, and the only symptom
// was the tool failing much later with "no instance to connect to" and
// nothing linking the two.
func TestMalformedLinesAreReportedNotSwallowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	body := "# a comment\n\nGOOD_KEY=good\nSQLTOP_CONN\nOTHER: value\n=novalue\nexport EXPORTED=yes\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOD_KEY", "")
	os.Unsetenv("GOOD_KEY")
	os.Unsetenv("EXPORTED")

	warnings, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 3 {
		t.Fatalf("got %d warnings %q, want one each for the missing =, the colon, and the empty name", len(warnings), warnings)
	}
	for _, want := range []string{"line 4", "line 5", "line 6"} {
		found := false
		for _, w := range warnings {
			if strings.Contains(w, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no warning names %s; a warning that does not say which line is barely better than silence", want)
		}
	}
	// The good lines still load: a malformed neighbour must not cost them.
	if got := os.Getenv("GOOD_KEY"); got != "good" {
		t.Errorf("GOOD_KEY = %q, want good", got)
	}
	if got := os.Getenv("EXPORTED"); got != "yes" {
		t.Errorf("EXPORTED = %q, want yes", got)
	}
}
