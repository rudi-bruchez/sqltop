package outdir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	first, err := Write(dir, "server-2026-08-30-201455", ".html", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(dir, "server-2026-08-30-201455", ".html", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("the second write reused %s", first)
	}
	if got, _ := os.ReadFile(first); string(got) != "one" {
		t.Errorf("the first file now holds %q", got)
	}
	if !strings.HasSuffix(second, "-2.html") {
		t.Errorf("second file is %s, want a -2 suffix", filepath.Base(second))
	}
}

func TestCreateReturnsAnAppendableFile(t *testing.T) {
	dir := t.TempDir()
	f, path, err := Create(dir, "capture-51", ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("a\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("b\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\nb\n" {
		t.Errorf("file holds %q, want two appended lines", got)
	}
}

func TestCreateMakesTheDirectory(t *testing.T) {
	// traces/ does not exist until the first capture, and Create is what
	// must bring it into being.
	dir := filepath.Join(t.TempDir(), "traces")
	f, _, err := Create(dir, "capture-51", ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestCreateWritesForTheOwnerAlone guards what these files contain rather than
// how they are named: production SQL text, literals included. A default mode
// would publish it to every account on the host.
func TestCreateWritesForTheOwnerAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "snapshots")
	path, err := Write(dir, "server-2026-09-13-101500", ".html", []byte("<p>SELECT * FROM Payroll WHERE ssn = '123-45-6789'</p>"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s has mode %v, want 0600: it carries production SQL", path, fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("%s has mode %v, want 0700", dir, di.Mode().Perm())
	}
}

func TestBesideIsUnderTheExecutable(t *testing.T) {
	got, err := Beside("traces")
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if filepath.Dir(got) != filepath.Dir(exe) {
		t.Errorf("Beside returned %s, which is not beside %s", got, exe)
	}
	if filepath.Base(got) != "traces" {
		t.Errorf("Beside returned %s, want a directory named traces", got)
	}
}
