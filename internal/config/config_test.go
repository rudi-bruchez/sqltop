package config

import (
	"github.com/rudi-bruchez/sqltop/internal/model"
	"os"
	"path/filepath"
	"strings"
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
	if err := Default().Validate(); err != nil {
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

// TestValidateRejectsCeilings is the other half of the bounds check. An
// external review found that validate had floors and no ceilings, which
// leaves the same shape of hole the missing floors left: a value nobody
// types on purpose, accepted, after which the program behaves strangely for
// a reason nothing explains. A budget of a million milliseconds a second is
// the zero-period bug from the other end, since a throttle that can never
// be exceeded never intervenes.
func TestValidateRejectsCeilings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"tier period", func(c *Config) { c.Tiers.Requests = Duration(2 * time.Hour) }, "tiers.requests"},
		{"cpu history period", func(c *Config) { c.Tiers.CPUHistory = Duration(48 * time.Hour) }, "tiers.cpuHistory"},
		{"retention", func(c *Config) { c.Retention = Duration(365 * 24 * time.Hour) }, "retention"},
		{"max samples", func(c *Config) { c.Budget.MaxSamples = 1 << 40 }, "budget.maxSamples"},
		{"budget", func(c *Config) { c.Budget.ServerCPUMsPerSecond = 1_000_000 }, "budget.serverCpuMsPerSecond"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("validate accepted it; a value this far outside any real configuration is a typo, and accepting it is how the program ends up behaving oddly with nothing to explain why")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the field %q that caused it", err, tc.want)
			}
		})
	}
}

// TestDefaultsSitInsideTheirOwnBounds is the check that keeps the ceilings
// honest: a bound tight enough to reject the shipped defaults would be a
// bound that breaks the tool out of the box.
func TestDefaultsSitInsideTheirOwnBounds(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("the built-in defaults do not pass validation: %v", err)
	}
}

// TestLoadsYAMLNatively is the test the existing ones do not amount to.
// They all write JSON, and JSON is a subset of YAML, so every one of them
// would pass against a parser that had never seen a YAML document. This
// writes YAML syntax: unquoted keys, unquoted scalars, block sequences,
// nested maps by indentation.
func TestLoadsYAMLNatively(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.yaml")
	body := `
instances:
  - name: PROD-SQL01
    dsn: sqlserver://prod-sql01?authenticator=krb5
  - name: Azure sales
    dsn: sqlserver://x.database.windows.net?database=sales
tiers:
  requests: 2s
  cpuHistory: 30s
retention: 5m
server:
  port: 9000
budget:
  serverCpuMsPerSecond: 25
  maxSamples: 1000
layouts:
  default:
    views:
      requests:
        columns:
          - field: spid
            width: 60
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances) != 2 || cfg.Instances[1].Name != "Azure sales" {
		t.Errorf("instances = %+v, want the two from the file in order", cfg.Instances)
	}
	if cfg.Tiers.Requests.Std() != 2*time.Second {
		t.Errorf("tiers.requests = %s, want 2s", cfg.Tiers.Requests)
	}
	// The camelCase key has to survive: yaml's default is to lowercase a
	// field name, which would silently ignore cpuHistory and leave the
	// default in place.
	if cfg.Tiers.CPUHistory.Std() != 30*time.Second {
		t.Errorf("tiers.cpuHistory = %s, want 30s; the key is camelCase and needs an explicit tag", cfg.Tiers.CPUHistory)
	}
	if cfg.Budget.ServerCPUMsPerSecond != 25 || cfg.Budget.MaxSamples != 1000 {
		t.Errorf("budget = %+v, want 25 and 1000", cfg.Budget)
	}
	// A field the file leaves out keeps its default, which is what makes a
	// partial hand-written file valid.
	if cfg.Tiers.Space.Std() != Default().Tiers.Space.Std() {
		t.Errorf("tiers.space = %s, want the default %s", cfg.Tiers.Space, Default().Tiers.Space)
	}
	if cfg.Layouts == nil {
		t.Error("layouts came back nil; the opaque node must survive loading or a saved layout is lost on the next write")
	}
}

// TestJSONStillLoads holds the compatibility claim in the spec: JSON is a
// subset of YAML, so an install predating the format change needs a rename
// and nothing else.
func TestJSONStillLoads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.json")
	body := `{"retention":"7m","tiers":{"cpuHistory":"90s"},"server":{"port":8500}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.Std() != 7*time.Minute || cfg.Tiers.CPUHistory.Std() != 90*time.Second || cfg.Server.Port != 8500 {
		t.Errorf("got %s / %s / %d, want 7m / 90s / 8500", cfg.Retention, cfg.Tiers.CPUHistory, cfg.Server.Port)
	}
}

// TestResolvePrefersYAMLOverJSONInTheSameDirectory pins the order within a
// location. A directory holding both must be answered by the current
// format, not by whichever name the loop happens to try first.
func TestResolvePrefersYAMLOverJSONInTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	seams(t, dir, t.TempDir())
	for _, name := range []string{"sqltop.json", "sqltop.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "sqltop.yaml"); got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

// TestSaveWritesYAMLThatLoadsBack is the round trip. A Save that produced
// something Load could not read would be discovered by a user, once, after
// they had built a layout worth keeping.
func TestSaveWritesYAMLThatLoadsBack(t *testing.T) {
	dir := t.TempDir()
	seams(t, dir, t.TempDir())

	cfg := Default()
	cfg.Retention = Duration(11 * time.Minute)
	cfg.Tiers.CPUHistory = Duration(45 * time.Second)
	cfg.Instances = []Instance{{Name: "one", DSN: "sqlserver://host"}}

	path, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "sqltop.yaml" {
		t.Errorf("saved to %q, want sqltop.yaml", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Durations must come back as "45s", not as a count of nanoseconds:
	// the file is meant to be read by a person.
	if !strings.Contains(string(b), "45s") || !strings.Contains(string(b), "11m") {
		t.Errorf("saved file does not carry readable durations:\n%s", b)
	}
	if strings.Contains(string(b), "{") {
		t.Errorf("saved file looks like JSON rather than YAML:\n%s", b)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("%v; what Save writes, Load has to read:\n%s", err, b)
	}
	if back.Retention != cfg.Retention || back.Tiers.CPUHistory != cfg.Tiers.CPUHistory {
		t.Errorf("round trip lost a value: got %s / %s, want %s / %s", back.Retention, back.Tiers.CPUHistory, cfg.Retention, cfg.Tiers.CPUHistory)
	}
	if len(back.Instances) != 1 || back.Instances[0].Name != "one" {
		t.Errorf("round trip lost the instance list: %+v", back.Instances)
	}
}

// TestMalformedYAMLNamesTheLine is why the format was changed at all. A
// syntax error in a file a person edits has to say where.
func TestMalformedYAMLNamesTheLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sqltop.yaml")
	if err := os.WriteFile(p, []byte("retention: 5m\ntiers:\n  requests: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("a malformed file loaded without complaint")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("error %q does not say which line; that is the whole reason this file is not JSON", err)
	}
}

// TestDefaultLayoutListsEveryTile is the point of the whole exercise: the
// file has to show what exists, so somebody can switch a tile off without
// knowing its name in advance.
func TestDefaultLayoutListsEveryTile(t *testing.T) {
	l := DefaultLayout()
	if len(l.Dashboard) != len(model.DashboardCatalogue) {
		t.Fatalf("wrote %d groups, the catalogue has %d", len(l.Dashboard), len(model.DashboardCatalogue))
	}
	for i, g := range model.DashboardCatalogue {
		got := l.Dashboard[i]
		if got.Group != g.ID {
			t.Errorf("group %d is %q, want %q; the file follows the catalogue's order", i, got.Group, g.ID)
		}
		for _, f := range g.Figures {
			on, listed := got.Figures[f.Key]
			if !listed {
				t.Errorf("%s: %q is missing, so it can only be switched off by somebody who already knows the name", g.ID, f.Key)
			}
			if !on {
				t.Errorf("%s: %q defaults to off", g.ID, f.Key)
			}
		}
		if len(got.Figures) != len(g.Figures) {
			t.Errorf("%s lists %d figures, the catalogue has %d", g.ID, len(got.Figures), len(g.Figures))
		}
	}
}

// TestDashboardResolvesAPartialFile holds the rule that makes a hand-edited
// file safe: what the file does not mention keeps its default, which is on.
// Without it, a figure added by a later version stays invisible to everyone
// who ever saved a configuration.
func TestDashboardResolvesAPartialFile(t *testing.T) {
	cfg := Default()
	cfg.Layouts = map[string]Layout{"default": {Dashboard: []DashboardGroup{
		{Group: "memory", Folded: true, Figures: map[string]bool{"plan_cache_mb": false}},
	}}}

	got := cfg.Dashboard()
	if len(got) != len(model.DashboardCatalogue) {
		t.Fatalf("resolved %d groups from a file naming one, want all %d", len(got), len(model.DashboardCatalogue))
	}
	// The one the file names comes first, because the file's order wins.
	if got[0].Group != "memory" || !got[0].Folded {
		t.Errorf("first group is %+v; a group the file names keeps its place and its folded state", got[0])
	}
	if got[0].Figures["plan_cache_mb"] {
		t.Error("plan_cache_mb is on despite being switched off in the file")
	}
	if !got[0].Figures["buffer_pool_mb"] {
		t.Error("buffer_pool_mb is off although the file never mentions it; unmentioned means default, and the default is on")
	}
	for _, g := range got[1:] {
		for k, on := range g.Figures {
			if !on {
				t.Errorf("%s/%s is off although the file never mentions its group", g.Group, k)
			}
		}
	}
}

// TestDashboardIgnoresAnUnknownGroup keeps a typo or a leftover from an
// older version from rendering an empty heading.
func TestDashboardIgnoresAnUnknownGroup(t *testing.T) {
	cfg := Default()
	cfg.Layouts = map[string]Layout{"default": {Dashboard: []DashboardGroup{{Group: "no-such-group"}}}}
	for _, g := range cfg.Dashboard() {
		if g.Group == "no-such-group" {
			t.Error("an unknown group survived resolution")
		}
	}
}

// ptr is a local helper: ViewColumn.Show is a pointer because "not
// mentioned" and "mentioned as false" have to be different things.
func ptr(b bool) *bool { return &b }

func columnFields(cols []ViewColumn) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Field
	}
	return out
}

func columnByField(cols []ViewColumn, field string) (ViewColumn, bool) {
	for _, c := range cols {
		if c.Field == field {
			return c, true
		}
	}
	return ViewColumn{}, false
}

// TestColumnsWithNoConfigurationAreTheCatalogue. A tool started with no file
// at all has to draw a sensible grid, and the catalogue is the only place
// that can say what that is.
func TestColumnsWithNoConfigurationAreTheCatalogue(t *testing.T) {
	got := Default().Columns("requests")
	def, ok := model.ViewByID("requests")
	if !ok {
		t.Fatal("the catalogue has no requests view")
	}
	if len(got) != len(def.Columns) {
		t.Fatalf("resolved %d columns, the catalogue has %d", len(got), len(def.Columns))
	}
	for i, c := range got {
		if c.Field != def.Columns[i].Field {
			t.Fatalf("resolved order %v, catalogue order %v", columnFields(got), fieldsOf(def))
		}
		if c.Show == nil || *c.Show != def.Columns[i].Default {
			t.Errorf("%s resolved to show=%v, the catalogue default is %v", c.Field, c.Show, def.Columns[i].Default)
		}
	}
}

func fieldsOf(v model.ViewDef) []string {
	out := make([]string, len(v.Columns))
	for i, c := range v.Columns {
		out[i] = c.Field
	}
	return out
}

// TestPartialColumnListKeepsEverythingItDoesNotMention is the rule that
// keeps a column added by a later version from being invisible to everybody
// who ever saved a layout. It is the same rule the dashboard follows, and
// the opposite of what an earlier draft of the spec said.
func TestPartialColumnListKeepsEverythingItDoesNotMention(t *testing.T) {
	cfg := Default()
	cfg.Layouts = map[string]Layout{"default": {Views: map[string]ViewLayout{
		"requests": {Columns: []ViewColumn{
			{Field: "sql_text"},
			{Field: "cpu_ms", Width: 140},
			{Field: "host", Show: ptr(false)},
		}},
	}}}
	got := cfg.Columns("requests")

	if got[0].Field != "sql_text" || got[1].Field != "cpu_ms" || got[2].Field != "host" {
		t.Fatalf("the file's order did not win: %v", columnFields(got))
	}
	if len(got) != len(model.ViewCatalogue[0].Columns) {
		t.Fatalf("resolved %d columns from a file naming 3; every catalogue column should still be there: %v", len(got), columnFields(got))
	}
	// Named only to be moved, with no switch of its own: it must stay on.
	if c, _ := columnByField(got, "sql_text"); c.Show == nil || !*c.Show {
		t.Error("sql_text was named only to move it and came back switched off")
	}
	if c, _ := columnByField(got, "host"); c.Show == nil || *c.Show {
		t.Error("host was switched off in the file and came back on")
	}
	if c, _ := columnByField(got, "cpu_ms"); c.Width != 140 {
		t.Errorf("cpu_ms width is %d, the file says 140", c.Width)
	}
	// Unmentioned, and off in the catalogue: the default is the catalogue's,
	// not a blanket on.
	if c, ok := columnByField(got, "percent_complete"); !ok || c.Show == nil || *c.Show {
		t.Error("percent_complete is off by default in the catalogue and resolved to on")
	}
	// Unmentioned, and on in the catalogue.
	if c, ok := columnByField(got, "database"); !ok || c.Show == nil || !*c.Show {
		t.Error("database was not mentioned and did not keep its default, which is on")
	}
}

// TestColumnsDropWhatTheCatalogueDoesNotKnow: a field left over from an
// older version, or a typo, must not become an empty column on screen.
func TestColumnsDropWhatTheCatalogueDoesNotKnow(t *testing.T) {
	cfg := Default()
	cfg.Layouts = map[string]Layout{"default": {Views: map[string]ViewLayout{
		"requests": {Columns: []ViewColumn{
			{Field: "cpu_ms"},
			{Field: "no_such_column", Show: ptr(true)},
			{Field: "cpu_ms", Show: ptr(false)},
		}},
	}}}
	got := cfg.Columns("requests")
	if _, ok := columnByField(got, "no_such_column"); ok {
		t.Error("a column the catalogue does not know survived into the resolved list")
	}
	if got[0].Field != "cpu_ms" {
		t.Errorf("resolved order %v, want cpu_ms first", columnFields(got))
	}
	// The second mention of cpu_ms is ignored, not merged: the first wins,
	// so a duplicate cannot silently switch off what the first line moved.
	if c, _ := columnByField(got, "cpu_ms"); c.Show == nil || !*c.Show {
		t.Error("a duplicate line later in the file overrode the first one")
	}
	if n := countField(got, "cpu_ms"); n != 1 {
		t.Errorf("cpu_ms appears %d times in the resolved list", n)
	}
}

func countField(cols []ViewColumn, field string) int {
	n := 0
	for _, c := range cols {
		if c.Field == field {
			n++
		}
	}
	return n
}

// TestColumnsOfAnUnknownViewAreEmpty rather than a panic or the requests
// columns: a view the catalogue does not have is one nothing can draw.
func TestColumnsOfAnUnknownViewAreEmpty(t *testing.T) {
	if got := Default().Columns("nowhere"); got != nil {
		t.Errorf("an unknown view resolved to %v", columnFields(got))
	}
}

// TestWriteConfigListsEveryColumn is the request this whole mechanism came
// from: the file has to show what exists so a column can be switched off
// without knowing its name in advance.
func TestWriteConfigListsEveryColumn(t *testing.T) {
	l := DefaultLayout()
	for _, v := range model.ViewCatalogue {
		got := l.Views[v.ID].Columns
		if len(got) != len(v.Columns) {
			t.Fatalf("view %s: the default layout lists %d columns, the catalogue has %d", v.ID, len(got), len(v.Columns))
		}
		for i, c := range got {
			if c.Field != v.Columns[i].Field {
				t.Fatalf("view %s: order %v, catalogue %v", v.ID, columnFields(got), fieldsOf(v))
			}
			if c.Show == nil {
				t.Errorf("view %s: %s is written without an explicit switch, which is the one thing this file is for", v.ID, c.Field)
			}
		}
	}
}

// The sampling cadence a fresh install runs at. Pinned by a test because it
// is the one default an operator feels immediately: it is what the tool costs
// the instance it watches, every second, before anybody touches a key.
//
// Requests and Counters move together on purpose. The f key and /api/period
// drive the request tier alone, so letting the counters run faster would put
// the dashboard five ticks ahead of the grid it sits above, for figures
// nobody reads that often.
func TestDefaultSamplingCadenceIsFiveSeconds(t *testing.T) {
	d := Default()
	for _, c := range []struct {
		field string
		got   Duration
		want  time.Duration
	}{
		{"tiers.requests", d.Tiers.Requests, 5 * time.Second},
		{"tiers.counters", d.Tiers.Counters, 5 * time.Second},
	} {
		if c.got.Std() != c.want {
			t.Errorf("%s = %s, want %s", c.field, c.got.Std(), c.want)
		}
	}
}

// The tiers deliberately left alone by that change. A live plan that only
// advanced every five seconds would stop showing a statement progressing,
// which is the whole reason the panel exists.
func TestDefaultLeavesThePlanAndHistoryTiersAlone(t *testing.T) {
	d := Default()
	if got := d.Tiers.LivePlan.Std(); got != 2*time.Second {
		t.Errorf("tiers.livePlan = %s, want 2s: the live plan must stay finer than the grid", got)
	}
	if got := d.Tiers.CPUHistory.Std(); got != time.Minute {
		t.Errorf("tiers.cpuHistory = %s, want 1m", got)
	}
}
