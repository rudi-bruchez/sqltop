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

func TestDefaultConfigIsValid(t *testing.T) {
	if err := Default().validate(); err != nil {
		t.Fatalf("the built-in defaults must always validate: %v", err)
	}
}

// TestLoadRejectsZeroTierPeriod is the exact scenario the review measured: a
// "requests" period of "0s", typed for "1s" or because zero looked like "as
// fast as sensible", turned into 1.26 million DMV queries a second once it
// reached the collector. Every tier is checked, not only requests, because
// the collector reads all of them the same way.
func TestLoadRejectsZeroTierPeriod(t *testing.T) {
	fields := []string{"requests", "counters", "space", "cpuHistory", "livePlan"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "sqltop.json")
			body := `{"tiers":{"` + field + `":"0s"}}`
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("a %q tier of 0s must be rejected, not accepted and clamped later", field)
			}
		})
	}
}

func TestLoadRejectsNegativeTierPeriod(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.json")
	if err := os.WriteFile(p, []byte(`{"tiers":{"requests":"-1s"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("a negative tier period must be rejected")
	}
}

func TestLoadRejectsTierPeriodBelowTheFloor(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.json")
	if err := os.WriteFile(p, []byte(`{"tiers":{"requests":"50ms"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("a tier period below the 100ms floor must be rejected")
	}
}

func TestLoadAcceptsTierPeriodAtTheFloor(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.json")
	if err := os.WriteFile(p, []byte(`{"tiers":{"requests":"100ms"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("a tier period exactly at the floor must be accepted: %v", err)
	}
}

func TestLoadRejectsNonPositiveRetention(t *testing.T) {
	cases := []string{`"0s"`, `"-5m"`}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "sqltop.json")
			if err := os.WriteFile(p, []byte(`{"retention":`+v+`}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("a retention of %s must be rejected", v)
			}
		})
	}
}

// TestLoadRejectsNonPositiveMaxSamples is the second scenario the review
// measured: maxSamples of 0 evicts every sample the instant it arrives, so
// the grid is permanently empty with no error to explain why.
func TestLoadRejectsNonPositiveMaxSamples(t *testing.T) {
	cases := []string{"0", "-1"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "sqltop.json")
			if err := os.WriteFile(p, []byte(`{"budget":{"maxSamples":`+v+`}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("a maxSamples of %s must be rejected", v)
			}
		})
	}
}

// TestLoadRejectsNonPositiveBudget is the third scenario the review named: a
// negative CPU budget throttles the collector to the floor immediately,
// which looks like the tool being broken rather than a typo in its own file.
func TestLoadRejectsNonPositiveBudget(t *testing.T) {
	cases := []string{"0", "-50"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "sqltop.json")
			if err := os.WriteFile(p, []byte(`{"budget":{"serverCpuMsPerSecond":`+v+`}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("a serverCpuMsPerSecond of %s must be rejected", v)
			}
		})
	}
}

func TestLoadRejectsPortOutOfRange(t *testing.T) {
	cases := []string{"0", "-1", "65536", "100000"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "sqltop.json")
			if err := os.WriteFile(p, []byte(`{"server":{"port":`+v+`}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("a port of %s must be rejected", v)
			}
		})
	}
}

// TestLoadValidFileStillLoadsUnchanged guards against the validation itself
// becoming the bug: a fully-specified, legitimate file must load with every
// value intact, not just avoid an error.
func TestLoadValidFileStillLoadsUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.json")
	body := `{
		"tiers": {"requests": "2s", "counters": "3s", "space": "10s", "cpuHistory": "90s", "livePlan": "4s"},
		"retention": "30m",
		"server": {"port": 9000},
		"budget": {"serverCpuMsPerSecond": 75, "maxSamples": 250000}
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(p)
	if err != nil {
		t.Fatalf("a valid, fully-specified file must load without error: %v", err)
	}
	if time.Duration(got.Tiers.Requests) != 2*time.Second {
		t.Errorf("tiers.requests = %v, want 2s", got.Tiers.Requests)
	}
	if time.Duration(got.Tiers.Counters) != 3*time.Second {
		t.Errorf("tiers.counters = %v, want 3s", got.Tiers.Counters)
	}
	if time.Duration(got.Tiers.Space) != 10*time.Second {
		t.Errorf("tiers.space = %v, want 10s", got.Tiers.Space)
	}
	if time.Duration(got.Tiers.CPUHistory) != 90*time.Second {
		t.Errorf("tiers.cpuHistory = %v, want 90s", got.Tiers.CPUHistory)
	}
	if time.Duration(got.Tiers.LivePlan) != 4*time.Second {
		t.Errorf("tiers.livePlan = %v, want 4s", got.Tiers.LivePlan)
	}
	if time.Duration(got.Retention) != 30*time.Minute {
		t.Errorf("retention = %v, want 30m", got.Retention)
	}
	if got.Server.Port != 9000 {
		t.Errorf("server.port = %d, want 9000", got.Server.Port)
	}
	if got.Budget.ServerCPUMsPerSecond != 75 {
		t.Errorf("budget.serverCpuMsPerSecond = %d, want 75", got.Budget.ServerCPUMsPerSecond)
	}
	if got.Budget.MaxSamples != 250000 {
		t.Errorf("budget.maxSamples = %d, want 250000", got.Budget.MaxSamples)
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
