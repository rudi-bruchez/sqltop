package dotenv

import (
	"os"
	"path/filepath"
	"runtime"
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

// TestSetKeepsEveryOtherLineAndReplacesTheDefinition covers every shape of
// line Load reads as a definition, because a line Set misses stays first in
// the file and Load keeps reading it: the page would report the string saved
// and the next run would open the page again.
func TestSetKeepsEveryOtherLineAndReplacesTheDefinition(t *testing.T) {
	const v = "sqlserver://sa:x@db01"
	const def = "SQLTOP_CONN=" + v
	cases := []struct{ name, before, after string }{
		{"no key", "A=1\n# note\n", "A=1\n# note\n" + def + "\n"},
		{"no final newline", "A=1", "A=1\n" + def + "\n"},
		{"empty file", "", def + "\n"},
		{"empty definition", "A=1\nSQLTOP_CONN=\nB=2\n", "A=1\n" + def + "\nB=2\n"},
		{"export", "export SQLTOP_CONN=old\n", def + "\n"},
		{"spaces around the equals sign", "SQLTOP_CONN = old\n", def + "\n"},
		{"leading spaces", "  SQLTOP_CONN=old\n", def + "\n"},
		{"twice", "SQLTOP_CONN=a\nX=1\nSQLTOP_CONN=b\n", def + "\nX=1\n"},
		{"commented out", "# SQLTOP_CONN=old\n", "# SQLTOP_CONN=old\n" + def + "\n"},
		{"a longer name", "SQLTOP_CONNX=1\n", "SQLTOP_CONNX=1\n" + def + "\n"},
		{"crlf", "A=1\r\nSQLTOP_CONN=old\r\n", "A=1\r\n" + def + "\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(p, []byte(c.before), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Set(p, "SQLTOP_CONN", v); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.after {
				t.Errorf("file is %q, want %q", got, c.after)
			}
			t.Setenv("SQLTOP_CONN", "")
			os.Unsetenv("SQLTOP_CONN")
			if _, err := Load(p); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv("SQLTOP_CONN"); got != v {
				t.Errorf("Load reads %q back, want %q", got, v)
			}
		})
	}
}

func TestSetCreatesAMissingFileReadableByItsOwnerOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := Set(p, "SQLTOP_CONN", "v"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "SQLTOP_CONN=v\n" {
		t.Errorf("file is %q", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("a new .env has mode %v; it can hold a password, so 0600", fi.Mode().Perm())
	}
}

// TestSetKeepsTheModeOfAnExistingFile guards the one thing os.Rename does not
// do on its own: the renamed file has the temporary file's mode, not the
// original's.
func TestSetKeepsTheModeOfAnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod, because WriteFile's mode passes through the umask.
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Set(p, "SQLTOP_CONN", "v"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode is %v after Set, want the original 0640", fi.Mode().Perm())
	}
}

func TestSetWritesThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.env")
	link := filepath.Join(dir, ".env")
	if err := os.WriteFile(target, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Set(link, "SQLTOP_CONN", "v"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "A=1\nSQLTOP_CONN=v\n" {
		t.Errorf("the link's target holds %q", got)
	}
}

func TestSetRefusesALineBreakAndLeavesTheFileAlone(t *testing.T) {
	for _, v := range []string{"a\nb", "a\rb"} {
		dir := t.TempDir()
		p := filepath.Join(dir, ".env")
		if err := os.WriteFile(p, []byte("A=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Set(p, "SQLTOP_CONN", v); err == nil {
			t.Errorf("Set accepted %q, which would become two lines", v)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "A=1\n" {
			t.Errorf("a refused Set changed the file to %q", got)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("a refused Set left %d files in the directory, want 1", len(entries))
		}
	}
}

// TestSetRoundTripsAConnectionStringThroughLoad uses the shape BuildDSN
// produces for a hostile password: userinfo leaves & and = bare.
func TestSetRoundTripsAConnectionStringThroughLoad(t *testing.T) {
	const v = "sqlserver://CORP%5Cdba:p%40ss%3Aw%2Fo%23r%25d%20%3F&=%C3%A9@db01:14330/SALES?database=a+b&encrypt=true"
	p := filepath.Join(t.TempDir(), ".env")
	if err := Set(p, "SQLTOP_CONN", v); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQLTOP_CONN", "")
	os.Unsetenv("SQLTOP_CONN")
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("SQLTOP_CONN"); got != v {
		t.Errorf("Load reads %q back, want %q", got, v)
	}
}
