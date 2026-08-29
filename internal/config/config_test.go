package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seams points the resolution at temporary directories for one test.
func seams(t *testing.T, binary, user string) {
	t.Helper()
	oldBinary, oldUser := binaryDir, userConfigDir
	binaryDir = func() string { return binary }
	userConfigDir = func() string { return user }
	t.Cleanup(func() { binaryDir, userConfigDir = oldBinary, oldUser })
}

func TestResolveExplicitMissingIsError(t *testing.T) {
	_, err := Resolve(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("an explicit --config that does not exist must be an error, not a silent fallback")
	}
}

func TestResolvePrefersBesideBinary(t *testing.T) {
	dir := t.TempDir()
	beside := filepath.Join(dir, "sqltop.json")
	if err := os.WriteFile(beside, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seams(t, dir, t.TempDir())

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != beside {
		t.Fatalf("got %q, want the file beside the binary %q", got, beside)
	}
}

func TestResolveFallsBackToUserDir(t *testing.T) {
	userDir := t.TempDir()
	want := filepath.Join(userDir, "sqltop.json")
	if err := os.WriteFile(want, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seams(t, t.TempDir(), userDir)

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveNoFileIsNotAnError(t *testing.T) {
	seams(t, t.TempDir(), t.TempDir())

	got, err := Resolve("")
	if err != nil {
		t.Fatalf("no config file must mean built-in defaults, not an error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want the empty path that means defaults", got)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.json")
	if err := os.WriteFile(p, []byte(`{"retention":"5m"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(got.Retention).Minutes() != 5 {
		t.Fatalf("retention = %v, want 5m", got.Retention)
	}
	if got.Server.Port != Default().Server.Port {
		t.Fatalf("port = %d, want the default %d for a key the file omits", got.Server.Port, Default().Server.Port)
	}
	if got.Tiers.Requests != Default().Tiers.Requests {
		t.Fatalf("requests tier = %v, want the default %v", got.Tiers.Requests, Default().Tiers.Requests)
	}
}

func TestSaveRoundTripsThroughTheUserDirectory(t *testing.T) {
	// Save is used by the UI plan when a layout changes, but it is written
	// and tested here so it does not ship as untested code nobody exercises.
	user := t.TempDir()
	seams(t, filepath.Join(t.TempDir(), "not-writable-because-absent"), user)

	cfg := Default()
	cfg.Retention = Duration(7 * time.Minute)

	path, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != user {
		t.Fatalf("saved to %q, want the user directory %q when the binary directory is not writable", path, user)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(back.Retention) != 7*time.Minute {
		t.Fatalf("round trip lost the value: %v", back.Retention)
	}
}
