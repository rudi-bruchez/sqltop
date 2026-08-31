package outdir

import (
	"os"
	"path/filepath"
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
