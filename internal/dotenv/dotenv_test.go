package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsSilent(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "absent")); err != nil {
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

	if err := Load(p); err != nil {
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
