# sqltop Collector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the collector that samples a live SQL Server instance and pushes it to the browser, ending with real queries rendered in a real grid at `http://127.0.0.1:8420`.

**Architecture:** Three layers with no upward leakage. A `Source` interface hides every engine detail behind engine-neutral types and a `Capabilities` set; a tiered collector polls it on independent schedules and feeds a capped retention window; a loopback HTTP server pushes snapshots over SSE to a hand-rolled virtualised grid. The renderer strategy is already settled by measurement (`bench/README.md`), so this plan reuses that verdict rather than re-litigating it.

**Tech Stack:** Go 1.27, standard library, `github.com/microsoft/go-mssqldb` plus its `integratedauth/krb5` provider. No other Go dependency. No front-end framework, no build step.

**Version:** this plan delivers 0.1.0. Tag `v0.1.0` when task 14 works, not before.

**Spec:** `docs/SPECS.md` (scope settled). Read it alongside this plan; every task argues from a numbered section of it.

## Global Constraints

Copied verbatim from the spec. Every task's requirements implicitly include this section.

- Pure Go, no CGO. `CGO_ENABLED=0 go build ./...` must succeed, and the result must cross-compile to `windows/amd64`, `darwin/arm64` and `linux/arm64`. (Spec 2)
- Read-only on the monitored server. No object created, nothing configured, no trace flag set. (Spec 2)
- Every collection query runs under `READ UNCOMMITTED` and ends with `OPTION (RECOMPILE, MAXDOP 1)`. The isolation level is set through the connector's `SessionInitSQL` so it survives a session reset; the hint is per statement because there is only one `OPTION` clause allowed per query.
- Plan retrieval is on demand only and never enters the polling loop. (Spec 2, 10)
- Secrets come from the environment via `.env`, never from the config file and never in code. (Spec 8.3, CLAUDE.md)
- Target is SQL Server 2019 and later. 2016 SP1 to 2017 degrade (no live plan progress). 2012 to 2016 RTM are the floor (no plan progress at all). Below 2012 the tool refuses to connect and says why. (Spec 3)
- Minimum permissions: `VIEW SERVER STATE` on 2019 and earlier, `VIEW SERVER PERFORMANCE STATE` on 2022 and later, `VIEW DATABASE STATE` plus `##MS_ServerStateReader##` on Azure SQL Database. These are minimums; the preflight checks what the login can read rather than inferring from the version. (Spec 3.1)
- Collection budget: under 50 ms of server CPU time per second, all tiers combined, measured by differentiating the tool's own `cpu_time` in `sys.dm_exec_sessions` for `@@SPID`. Never by round-trip. (Spec 10)
- Rendering budget: under 16 ms per refresh at 800 rows. (Spec 10)
- The HTTP server binds `127.0.0.1` only, with no flag to widen it. Default port 8420. Every request carries a per-run token. (Spec 4.3)
- Only dependency added in this plan: `github.com/microsoft/go-mssqldb` and `github.com/microsoft/go-mssqldb/integratedauth/krb5`. Any other needs a reason in the commit that introduces it. (Spec 2.1)
- English everywhere: code, comments, commits, UI. (CLAUDE.md)
- The version is a constant in `internal/buildinfo`, starting at 0.1.0, with the commit taken from `runtime/debug.ReadBuildInfo` and never from build flags. (Spec 11)
- `gofmt` clean and `go vet ./...` clean before every commit. (Spec 2.1)

## Scope

In scope: configuration, the neutral model, the source layer and its SQL Server implementation, preflight and capabilities, the tiered collector, the capped retention window, the self-measured budget and its throttle, the wire protocol, the loopback HTTP server, the SSE stream, and a working grid fed by real data.

Deferred to the UI plan, deliberately: the dashboard tiles, the view tabs beyond the request grid, saved layouts, the plan viewer panel, and `Kill`. `Kill` is deferred on purpose rather than by omission: it is the only destructive action in the tool, its safety lives in a confirmation flow and an audit log (spec 9.1), and shipping the capability before its guardrails would be the wrong order.

`bench/` is untouched and keeps its synthetic generator. It is the non-regression harness for the renderer and must keep building. The real interface is a separate copy under `internal/web/assets/`; the duplication is intentional, since the bench carries mode switches and instrumentation the product does not.

## File Structure

| Path | Responsibility |
|---|---|
| `cmd/sqltop/main.go` | Flags, `.env` load, wiring, start |
| `internal/dotenv/dotenv.go` | `KEY=VALUE` parser, real environment wins |
| `internal/config/config.go` | Types, defaults, resolution order, load and save |
| `internal/model/model.go` | Engine-neutral types and `Capabilities` |
| `internal/window/window.go` | Retention window, eviction by age and by count |
| `internal/window/blocking.go` | Blocking chain flattening with depth |
| `internal/source/source.go` | The `Source` interface and its registry |
| `internal/source/fake/fake.go` | In-memory source for tests of upper layers |
| `internal/source/mssql/mssql.go` | Connection, preflight, capabilities |
| `internal/source/mssql/requests.go` | `SampleRequests` query and scan |
| `internal/source/mssql/server.go` | `SampleServer` per tier |
| `internal/source/mssql/counters.go` | Counter definitions and `cntr_type` arithmetic |
| `internal/source/mssql/plan.go` | `QueryText`, `Plan`, live plan |
| `internal/source/mssql/cost.go` | Own-session cost reading |
| `internal/collector/collector.go` | Tiered scheduler |
| `internal/collector/budget.go` | Budget accounting and ordered throttle |
| `internal/web/server.go` | Loopback HTTP, token, routes |
| `internal/web/stream.go` | SSE, snapshot and delta payload building |
| `internal/web/assets/` | The real interface: `index.html`, `app.js`, `style.css` |
| `scripts/testdb.sh` | Start or wake the Podman container, print the DSN |

## Testing Strategy

Unit tests cover the parts where a wrong answer would be silent and plausible: counter delta and ratio arithmetic, blocking-chain flattening, window eviction, config resolution, throttle state, payload composition. These need no database and must always run.

Integration tests run against a real SQL Server in Podman. They read `SQLTOP_TEST_DSN` and call `t.Skip` when it is unset, so `go test ./...` stays green on a machine without Podman. `scripts/testdb.sh` starts or wakes the container and prints the DSN to export.

Never mock a database to assert that a SQL string equals a SQL string. (Spec 2.1)

---

### Task 1: Module skeleton, dotenv and configuration

**Files:**
- Create: `internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`
- Create: `internal/dotenv/dotenv.go`, `internal/dotenv/dotenv_test.go`
- Create: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `cmd/sqltop/main.go`
- Create: `.env.example`
- Modify: `.gitignore` (confirm `.env` is present)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `buildinfo.Version` constant, `buildinfo.String() string`, `buildinfo.Revision() (rev string, dirty bool)`
  - `dotenv.Load(path string) error`
  - `config.Config` struct with fields `Instances []config.Instance`, `Tiers config.Tiers`, `Retention time.Duration`, `Server config.Server`, `Budget config.Budget`
  - `config.Instance{Name, DSN string}`
  - `config.Tiers{Requests, Counters, Space, CPUHistory, LivePlan time.Duration}`
  - `config.Server{Port int}`
  - `config.Budget{ServerCPUMsPerSecond int, MaxSamples int}`
  - `config.Resolve(explicit string) (path string, err error)`
  - `config.Load(path string) (Config, error)` where an empty path yields defaults
  - `config.Default() Config`

- [ ] **Step 1: Write the failing config resolution test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
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
```

The test file needs `"time"` in its imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestResolve -v`
Expected: FAIL to build, `undefined: Resolve`.

- [ ] **Step 3: Implement the configuration package**

Create `internal/config/config.go`. Durations are JSON strings like `"1s"`, so they need their own type. `Load` starts from `Default()` and unmarshals over it, which is what makes a partial file valid.

```go
// Package config loads sqltop's settings and decides which file they came from.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Duration is a time.Duration that marshals as "1s", "15m".
type Duration time.Duration

func (d Duration) String() string                { return time.Duration(d).String() }
func (d Duration) Std() time.Duration            { return time.Duration(d) }
func (d Duration) MarshalJSON() ([]byte, error)  { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: %q is not a duration: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

type Instance struct {
	Name string `json:"name"`
	DSN  string `json:"dsn"`
}

type Tiers struct {
	Requests   Duration `json:"requests"`
	Counters   Duration `json:"counters"`
	Space      Duration `json:"space"`
	CPUHistory Duration `json:"cpuHistory"`
	LivePlan   Duration `json:"livePlan"`
}

type Server struct {
	Port int `json:"port"`
}

type Budget struct {
	// ServerCPUMsPerSecond is the ceiling from spec section 10.
	ServerCPUMsPerSecond int `json:"serverCpuMsPerSecond"`
	// MaxSamples caps the retention window so memory stays bounded on a
	// busy server, where 15 minutes of history would otherwise grow without
	// limit. See spec section 4.2 and task 3.
	MaxSamples int `json:"maxSamples"`
}

type Config struct {
	Instances []Instance      `json:"instances"`
	Tiers     Tiers           `json:"tiers"`
	Retention Duration        `json:"retention"`
	Server    Server          `json:"server"`
	Budget    Budget          `json:"budget"`
	Layouts   json.RawMessage `json:"layouts,omitempty"`

	// Path is the file this came from, empty when built-in defaults were
	// used. The status bar names it, so it must survive loading.
	Path string `json:"-"`
}

func Default() Config {
	return Config{
		Tiers: Tiers{
			Requests:   Duration(time.Second),
			Counters:   Duration(time.Second),
			Space:      Duration(5 * time.Second),
			CPUHistory: Duration(time.Minute),
			LivePlan:   Duration(2 * time.Second),
		},
		Retention: Duration(15 * time.Minute),
		Server:    Server{Port: 8420},
		Budget:    Budget{ServerCPUMsPerSecond: 50, MaxSamples: 500_000},
	}
}

// Package-level seams the tests override with temporary directories. They are
// variables rather than environment lookups on purpose: an environment
// variable would also let a user silently redirect where their configuration
// is read from, which is a surprise nobody asked for.
var (
	binaryDir = func() string {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		return filepath.Dir(exe)
	}

	userConfigDir = func() string {
		d, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		return filepath.Join(d, "sqltop")
	}
)

// Resolve returns the configuration file to use, or "" when there is none and
// built-in defaults apply. Order: explicit, beside the binary, user directory.
// An explicit path that does not exist is an error rather than a silent
// fallback, because a typo must not look like a working default.
func Resolve(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config: %s: %w", explicit, err)
		}
		return explicit, nil
	}
	for _, dir := range []string{binaryDir(), userConfigDir()} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "sqltop.json")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", nil
}

// Load reads path over the built-in defaults, so a partial file is valid.
// An empty path yields the defaults untouched.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

// Save writes cfg back to the file it came from. When it came from defaults,
// it goes beside the binary if that directory is writable, and in the user
// configuration directory otherwise.
func Save(cfg Config) (string, error) {
	path := cfg.Path
	if path == "" {
		path = filepath.Join(binaryDir(), "sqltop.json")
		if err := writable(binaryDir()); err != nil {
			dir := userConfigDir()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("config: %w", err)
			}
			path = filepath.Join(dir, "sqltop.json")
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return path, nil
}

func writable(dir string) error {
	if dir == "" {
		return errors.New("no directory")
	}
	f, err := os.CreateTemp(dir, ".sqltop-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
```

- [ ] **Step 4: Run the config tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Write the failing dotenv test**

Create `internal/dotenv/dotenv_test.go`:

```go
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
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/dotenv/ -v`
Expected: FAIL to build, `undefined: Load`.

- [ ] **Step 7: Implement dotenv**

Create `internal/dotenv/dotenv.go`:

```go
// Package dotenv reads KEY=VALUE pairs from a file into the environment.
//
// Copied rather than depended on, per the project's standard-library-first
// rule. An absent file is a no-op: secrets may legitimately come from a real
// export instead, and an explicit export always wins over the file.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, taken := os.LookupEnv(key); !taken {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
```

- [ ] **Step 8: Run the dotenv tests to verify they pass**

Run: `go test ./internal/dotenv/ -v`
Expected: PASS, two tests.

- [ ] **Step 9: Write the failing buildinfo test**

Create `internal/buildinfo/buildinfo_test.go`:

```go
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
```

- [ ] **Step 10: Run it to verify it fails**

Run: `go test ./internal/buildinfo/ -v`
Expected: FAIL to build, `undefined: Version`.

- [ ] **Step 11: Implement buildinfo**

Create `internal/buildinfo/buildinfo.go`:

```go
// Package buildinfo reports which build of sqltop is running.
//
// The version is a constant, and the commit comes from the toolchain rather
// than from build flags: runtime/debug.ReadBuildInfo carries the VCS revision
// and dirty state that `go build` records on its own. A version that depends
// on the build command is a version that is wrong the first time someone
// builds it differently.
package buildinfo

import "runtime/debug"

// Version follows spec section 11: zero-major while the shape can change.
// 0.1 is the collector and a working request grid.
const Version = "0.1.0"

// Revision returns the commit this binary was built from, and whether the
// tree was dirty. Both are empty and false when the build carried no VCS
// information, which happens for `go run` and in some test configurations.
func Revision() (rev string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty
}

// String is what --version prints and what the interface header shows.
func String() string {
	out := "sqltop " + Version
	rev, dirty := Revision()
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		out += " (" + rev
		if dirty {
			out += ", dirty"
		}
		out += ")"
	}
	return out
}
```

- [ ] **Step 12: Run the buildinfo tests to verify they pass**

Run: `go test ./internal/buildinfo/ -v`
Expected: PASS, three tests.

- [ ] **Step 13: Wire a minimal main that proves resolution works**

Create `cmd/sqltop/main.go`:

```go
// Command sqltop is a top for SQL servers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
)

func main() {
	configPath := flag.String("config", "", "path to sqltop.json (default: beside the binary, then the user config directory)")
	envPath := flag.String("env", ".env", "path to the .env file holding secrets")
	showConfig := flag.Bool("show-config", false, "print the resolved configuration and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}

	if err := dotenv.Load(*envPath); err != nil {
		log.Printf("warning: %s: %v", *envPath, err)
	}

	path, err := config.Resolve(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}

	if *showConfig {
		where := cfg.Path
		if where == "" {
			where = "(built-in defaults, no file found)"
		}
		fmt.Fprintln(os.Stderr, "configuration from:", where)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	log.Fatal("not implemented yet: run with -show-config")
}
```

Create `.env.example`:

```sh
# Connection string for the instance sqltop opens by default.
# Referenced from sqltop.json as ${SQLTOP_CONN}.
SQLTOP_CONN=

# DSN used by the integration tests. Set it with: eval "$(scripts/testdb.sh)"
SQLTOP_TEST_DSN=
```

- [ ] **Step 14: Verify the version and the resolved defaults**

Run:
```bash
go run ./cmd/sqltop -version
go run ./cmd/sqltop -show-config
```
The first prints `sqltop 0.1.0`, with a commit in parentheses when built rather
than run. Then:
```bash
go run ./cmd/sqltop -show-config
```
Expected: `configuration from: (built-in defaults, no file found)` on stderr, and a JSON document on stdout with `"port": 8420`, `"retention": "15m0s"`, `"requests": "1s"`, `"livePlan": "2s"`, `"maxSamples": 500000`.

- [ ] **Step 15: Verify the no-CGO constraint still holds**

Run:
```bash
CGO_ENABLED=0 go build ./... && gofmt -l . && go vet ./...
```
Expected: no output from any of the three.

- [ ] **Step 16: Commit**

```bash
git add internal/buildinfo internal/dotenv internal/config cmd/sqltop .env.example
git commit -m "Add configuration loading, .env support and the version

The version is a constant starting at 0.1.0, and the commit comes from
runtime/debug.ReadBuildInfo rather than from build flags: a plain go
build already records the VCS revision and dirty state, so nothing has
to be passed at build time. A version that depends on the build command
is wrong the first time someone builds it differently.

Resolution order is explicit --config, then beside the binary, then the
user configuration directory, first hit wins, so portable and per-user
installs both work. An explicit path that does not exist is an error
rather than a silent fallback, because a typo must not look like a
working default.

Load starts from the built-in defaults and unmarshals over them, which
is what makes a hand-written partial file valid.

dotenv is copied rather than depended on, per the standard-library-first
rule. A missing file is a no-op and a real export always wins."
```

---

### Task 2: The engine-neutral model

**Files:**
- Create: `internal/model/model.go`, `internal/model/model_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: the vocabulary every later task uses.
  - `model.Tier` with constants `TierRequests`, `TierCounters`, `TierSpace`, `TierCPUHistory`
  - `model.RequestSample` (fields below, all exported)
  - `model.ServerSample{At time.Time, Figures map[string]model.Figure}`
  - `model.Figure{Value float64, Unit string, Available bool}`
  - `model.ServerInfo{Instance, Host, Edition, ProductVersion string, MajorVersion int, IsAzureSQLDB bool, StartedAt time.Time}`
  - `model.Capability` with constants and `model.Capabilities` as a set
  - `model.Caps(...Capability) Capabilities`, `(Capabilities).Has(Capability) bool`
  - `model.RequestRef{SessionID int64, RequestID int32}`
  - `model.Plan{Format string, Payload []byte, Live bool}`

Spec 4.1 requires that an unavailable figure disappear individually rather than take its family with it, which is why `Figure` carries `Available` instead of the map simply omitting keys: the UI needs to know the difference between "not supported here" and "not sampled yet".

- [ ] **Step 1: Write the failing capabilities test**

Create `internal/model/model_test.go`:

```go
package model

import "testing"

func TestCapabilitiesSet(t *testing.T) {
	c := Caps(CapLivePlanProgress, CapKillSession)

	if !c.Has(CapLivePlanProgress) {
		t.Error("CapLivePlanProgress should be present")
	}
	if c.Has(CapInstanceWideView) {
		t.Error("CapInstanceWideView was never added and must be absent")
	}
	if Caps().Has(CapKillSession) {
		t.Error("the empty set has nothing")
	}
}

func TestFigureDistinguishesUnsupportedFromUnsampled(t *testing.T) {
	unsupported := Figure{Available: false}
	unsampled := Figure{Available: true, Value: 0}

	if unsupported.Available == unsampled.Available {
		t.Fatal("a figure the source cannot provide must be distinguishable from one that is genuinely zero")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/model/ -v`
Expected: FAIL to build, `undefined: Caps`.

- [ ] **Step 3: Implement the model**

Create `internal/model/model.go`:

```go
// Package model holds the engine-neutral types. Nothing here may name a DMV,
// a showplan, or any other SQL Server concept: that is the whole point of the
// source abstraction in spec section 4.1.
package model

import "time"

// Tier names a collection schedule. Spec section 10.
type Tier int

const (
	TierRequests Tier = iota
	TierCounters
	TierSpace
	TierCPUHistory
)

func (t Tier) String() string {
	switch t {
	case TierRequests:
		return "requests"
	case TierCounters:
		return "counters"
	case TierSpace:
		return "space"
	case TierCPUHistory:
		return "cpuHistory"
	}
	return "unknown"
}

// Capability is something a source may or may not be able to do on this
// server, at this version, with these rights.
type Capability uint32

const (
	CapLivePlanProgress Capability = 1 << iota
	CapInstanceWideView
	CapTempdbPerTask
	CapWaitStatsCumulative
	CapSchedulerLoad
	CapKillSession
	CapVersionStoreUsage
	CapRingBufferCPU
)

type Capabilities uint32

func Caps(list ...Capability) Capabilities {
	var c Capabilities
	for _, x := range list {
		c |= Capabilities(x)
	}
	return c
}

func (c Capabilities) Has(x Capability) bool { return c&Capabilities(x) != 0 }
func (c Capabilities) With(x Capability) Capabilities { return c | Capabilities(x) }

// RequestRef identifies one running request across ticks.
type RequestRef struct {
	SessionID int64
	RequestID int32
}

// RequestSample is one observation of one active request at one instant.
// One row per sample, never one row per query: a request active for twelve
// minutes must leave a series that can be replayed. Spec section 4.
type RequestSample struct {
	At  time.Time
	Ref RequestRef

	Status    string
	Database  string
	Login     string
	Host      string
	Program   string
	Command   string
	BlockedBy int64
	// Depth is filled by the window, not the source: flattening the blocking
	// chain is engine-neutral work. Spec section 4.
	Depth int

	ElapsedMs      int64
	CPUMs          int64
	LogicalReads   int64
	PhysicalReads  int64
	Writes         int64
	TempdbMB       float64
	MemoryGrantMB  float64
	DOP            int
	OpenTran       int
	PercentComplete float64

	WaitType     string
	WaitMs       int64
	WaitResource string

	IsolationLevel string
	QueryHash      string
	// SQLText is sent once per session in the reference table, not on every
	// tick. Spec section 4.
	SQLText string
}

// Figure is one dashboard number. Available reports whether this source can
// produce it at all, which is different from it being zero.
type Figure struct {
	Value     float64
	Unit      string
	Available bool
}

// ServerSample is one observation of the instance as a whole.
type ServerSample struct {
	At      time.Time
	Figures map[string]Figure
}

type ServerInfo struct {
	Instance       string
	Host           string
	Edition        string
	ProductVersion string
	MajorVersion   int
	IsAzureSQLDB   bool
	StartedAt      time.Time
}

// Plan is deliberately opaque. Showplan XML, an EXPLAIN tree and a MySQL plan
// have nothing in common, so the renderer dispatches on Format rather than the
// model pretending they unify. Spec section 4.1.
type Plan struct {
	Format  string // "showplan-xml" for SQL Server
	Payload []byte
	Live    bool // carries in-flight row counts
}

// Cost is what the tool has spent on the server, read from its own session.
// Spec section 10.
type Cost struct {
	At           time.Time
	CPUMs        int64
	LogicalReads int64
}
```

- [ ] **Step 4: Run the model tests to verify they pass**

Run: `go test ./internal/model/ -v`
Expected: PASS, two tests.

- [ ] **Step 5: Commit**

```bash
git add internal/model
git commit -m "Add the engine-neutral model

Nothing here names a DMV or a showplan: that is what makes the source
abstraction worth having.

Two shapes deserve their reasons. RequestSample is one row per sample
rather than one row per query, so a request active for twelve minutes
leaves a series that can be replayed. Figure carries Available rather
than the map omitting keys, because the interface has to tell a figure
this source cannot produce from one that is genuinely zero, and spec 4.1
requires that granularity per figure rather than per family.

Plan stays opaque, a format plus a payload, since showplan XML and an
EXPLAIN tree do not unify."
```

---

### Task 3: The retention window

**Files:**
- Create: `internal/window/window.go`, `internal/window/window_test.go`

**Interfaces:**
- Consumes: `model.RequestSample`, `model.RequestRef`.
- Produces:
  - `window.New(retention time.Duration, maxSamples int) *window.Window`
  - `(*Window).Append(at time.Time, rows []model.RequestSample)`
  - `(*Window).Latest() []model.RequestSample`
  - `(*Window).History(ref model.RequestRef) []model.RequestSample`
  - `(*Window).Depth() (oldest time.Time, samples int, capped bool)`

The cap exists because 15 minutes at one hertz on a server with 800 active requests is roughly 720,000 samples, which grows without bound as the server gets busier. A diagnostic tool that swells in memory exactly when the server is in trouble is a bad surprise at the worst moment. Bounded memory, and the real depth reported so the shortening is visible rather than silent.

- [ ] **Step 1: Write the failing window tests**

Create `internal/window/window_test.go`:

```go
package window

import (
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

func sample(spid int64, cpu int64) model.RequestSample {
	return model.RequestSample{Ref: model.RequestRef{SessionID: spid}, CPUMs: cpu}
}

func TestLatestReturnsTheMostRecentTick(t *testing.T) {
	w := New(time.Minute, 1000)
	t0 := time.Now()

	w.Append(t0, []model.RequestSample{sample(51, 10), sample(52, 20)})
	w.Append(t0.Add(time.Second), []model.RequestSample{sample(51, 30)})

	got := w.Latest()
	if len(got) != 1 || got[0].CPUMs != 30 {
		t.Fatalf("Latest() = %+v, want the single row of the second tick", got)
	}
}

func TestHistoryReplaysOneRequest(t *testing.T) {
	w := New(time.Minute, 1000)
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		w.Append(t0.Add(time.Duration(i)*time.Second), []model.RequestSample{sample(51, int64(i * 100))})
	}

	got := w.History(model.RequestRef{SessionID: 51})
	if len(got) != 5 {
		t.Fatalf("History() returned %d samples, want 5", len(got))
	}
	for i, s := range got {
		if s.CPUMs != int64(i*100) {
			t.Fatalf("sample %d has CPUMs %d, want %d: history must stay in order", i, s.CPUMs, i*100)
		}
	}
}

func TestEvictsByAge(t *testing.T) {
	w := New(10*time.Second, 1000)
	t0 := time.Now()

	w.Append(t0, []model.RequestSample{sample(51, 1)})
	w.Append(t0.Add(30*time.Second), []model.RequestSample{sample(51, 2)})

	if got := w.History(model.RequestRef{SessionID: 51}); len(got) != 1 || got[0].CPUMs != 2 {
		t.Fatalf("History() = %+v, want only the sample inside the retention period", got)
	}
}

func TestEvictsByCountAndReportsCapped(t *testing.T) {
	w := New(time.Hour, 10)
	t0 := time.Now()
	for i := 0; i < 25; i++ {
		w.Append(t0.Add(time.Duration(i)*time.Second), []model.RequestSample{sample(51, int64(i))})
	}

	_, samples, capped := w.Depth()
	if samples != 10 {
		t.Fatalf("window holds %d samples, want exactly the cap of 10: one sample per tick means eviction lands on the boundary", samples)
	}
	if !capped {
		t.Fatal("Depth() must report capped=true once the count limit has bitten, so the UI can say the window is shorter than asked")
	}

	got := w.History(model.RequestRef{SessionID: 51})
	if len(got) == 0 || got[len(got)-1].CPUMs != 24 {
		t.Fatal("eviction must drop the oldest samples, never the newest")
	}
}

func TestDepthOnEmptyWindow(t *testing.T) {
	_, samples, capped := New(time.Minute, 100).Depth()
	if samples != 0 || capped {
		t.Fatalf("empty window reported %d samples capped=%v", samples, capped)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/window/ -v`
Expected: FAIL to build, `undefined: New`.

- [ ] **Step 3: Implement the window**

Create `internal/window/window.go`:

```go
// Package window keeps the rolling history the whole interface reads from.
package window

import (
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

type tick struct {
	at   time.Time
	rows []model.RequestSample
}

// Window holds recent ticks, bounded both by age and by total sample count.
// One mutex, no cleverness: the tool waits on the network, not on this.
type Window struct {
	mu        sync.RWMutex
	ticks     []tick
	samples   int
	retention time.Duration
	maxSample int
	capped    bool
}

func New(retention time.Duration, maxSamples int) *Window {
	return &Window{retention: retention, maxSample: maxSamples}
}

func (w *Window) Append(at time.Time, rows []model.RequestSample) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ticks = append(w.ticks, tick{at: at, rows: rows})
	w.samples += len(rows)
	w.evictLocked(at)
}

// evictLocked drops the oldest ticks until both bounds are satisfied. Age
// first, because that is the bound the user asked for; the count cap is the
// safety net that keeps memory bounded on a busy server.
func (w *Window) evictLocked(now time.Time) {
	cutoff := now.Add(-w.retention)
	drop := 0
	for drop < len(w.ticks) && w.ticks[drop].at.Before(cutoff) {
		w.samples -= len(w.ticks[drop].rows)
		drop++
	}

	w.capped = false
	for drop < len(w.ticks) && w.samples > w.maxSample {
		w.samples -= len(w.ticks[drop].rows)
		drop++
		w.capped = true
	}

	if drop > 0 {
		w.ticks = append([]tick(nil), w.ticks[drop:]...)
	}
}

func (w *Window) Latest() []model.RequestSample {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.ticks) == 0 {
		return nil
	}
	return w.ticks[len(w.ticks)-1].rows
}

func (w *Window) History(ref model.RequestRef) []model.RequestSample {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var out []model.RequestSample
	for _, t := range w.ticks {
		for _, r := range t.rows {
			if r.Ref == ref {
				out = append(out, r)
			}
		}
	}
	return out
}

// Depth reports what the window actually holds, which the status bar shows.
// capped is true when the sample cap, rather than the retention period, is
// what decided the oldest sample: the window is then shorter than asked and
// the user should be able to see that.
func (w *Window) Depth() (oldest time.Time, samples int, capped bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.ticks) == 0 {
		return time.Time{}, 0, false
	}
	return w.ticks[0].at, w.samples, w.capped
}
```

- [ ] **Step 4: Run the window tests to verify they pass**

Run: `go test ./internal/window/ -v -race`
Expected: PASS, five tests, no race warnings.

- [ ] **Step 5: Commit**

```bash
git add internal/window
git commit -m "Add the capped retention window

Bounded by age and by total sample count. The count cap is the reason
this is not just a time-based ring: 15 minutes at one hertz against 800
active requests is roughly 720,000 samples, and it grows as the server
gets busier. A diagnostic tool that swells in memory exactly when the
server is in trouble is a bad surprise at the worst moment.

Depth reports capped so the shortening is visible in the status bar
rather than silent."
```

---

### Task 4: Blocking chain flattening

**Files:**
- Create: `internal/window/blocking.go`, `internal/window/blocking_test.go`

**Interfaces:**
- Consumes: `model.RequestSample`.
- Produces: `window.Flatten(rows []model.RequestSample) []model.RequestSample`, returning rows reordered so every blocker sits immediately above what it blocks, with `Depth` filled.

The spec sends a flat list with a depth column rather than a nested tree, because the delta feed cannot carry a tree (spec 4, measured in `bench/README.md`). Getting this wrong produces a plausible but wrong picture of who is stuck behind whom, which is exactly the silent failure this project tests against.

- [ ] **Step 1: Write the failing tests**

Create `internal/window/blocking_test.go`:

```go
package window

import (
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

func row(spid, blockedBy int64) model.RequestSample {
	return model.RequestSample{Ref: model.RequestRef{SessionID: spid}, BlockedBy: blockedBy}
}

func order(rows []model.RequestSample) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Ref.SessionID
	}
	return out
}

func depths(rows []model.RequestSample) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Depth
	}
	return out
}

func TestFlattenSimpleChain(t *testing.T) {
	// 51 blocks 52, which blocks 53.
	got := Flatten([]model.RequestSample{row(53, 52), row(51, 0), row(52, 51)})

	if want := []int64{51, 52, 53}; !equal64(order(got), want) {
		t.Fatalf("order = %v, want %v: a blocker must sit above what it blocks", order(got), want)
	}
	if want := []int{0, 1, 2}; !equalInt(depths(got), want) {
		t.Fatalf("depths = %v, want %v", depths(got), want)
	}
}

func TestFlattenTree(t *testing.T) {
	// 51 blocks both 52 and 53; 53 blocks 54.
	got := Flatten([]model.RequestSample{row(51, 0), row(52, 51), row(53, 51), row(54, 53)})

	if want := []int64{51, 52, 53, 54}; !equal64(order(got), want) {
		t.Fatalf("order = %v, want %v", order(got), want)
	}
	if want := []int{0, 1, 1, 2}; !equalInt(depths(got), want) {
		t.Fatalf("depths = %v, want %v", depths(got), want)
	}
}

func TestFlattenSurvivesACycle(t *testing.T) {
	// 51 blocked by 52, 52 blocked by 51. SQL Server can briefly report this.
	got := Flatten([]model.RequestSample{row(51, 52), row(52, 51)})

	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: a cycle must not drop or duplicate rows", len(got))
	}
}

func TestFlattenSelfBlock(t *testing.T) {
	got := Flatten([]model.RequestSample{row(51, 51)})

	if len(got) != 1 || got[0].Depth != 0 {
		t.Fatalf("got %+v, want one row at depth 0: a session cannot be its own child", got)
	}
}

func TestFlattenOrphanBlocker(t *testing.T) {
	// 52 says it is blocked by 51, but 51 is not in the sample: its request
	// finished between the two reads. 52 must still appear, at the root.
	got := Flatten([]model.RequestSample{row(52, 51)})

	if len(got) != 1 || got[0].Depth != 0 {
		t.Fatalf("got %+v, want the orphan at depth 0 rather than dropped", got)
	}
}

func TestFlattenKeepsUnblockedRows(t *testing.T) {
	got := Flatten([]model.RequestSample{row(51, 0), row(52, 0), row(53, 0)})
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
}

func equal64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInt(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/window/ -run TestFlatten -v`
Expected: FAIL to build, `undefined: Flatten`.

- [ ] **Step 3: Implement Flatten**

Create `internal/window/blocking.go`:

```go
package window

import (
	"sort"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// Flatten reorders rows so that every blocker sits immediately above what it
// blocks, and fills Depth. The result is a flat list with an indentation
// level, not a tree: the wire protocol carries no nesting (spec section 4).
//
// Three cases the engine really produces have to survive: a session reporting
// itself as its own blocker, a cycle between two sessions, and a blocker whose
// own request finished between the read of one row and the next. None may drop
// a row, because a request missing from the grid is worse than one shown at
// the wrong indentation.
func Flatten(rows []model.RequestSample) []model.RequestSample {
	children := make(map[int64][]model.RequestSample, len(rows))
	present := make(map[int64]bool, len(rows))
	for _, r := range rows {
		present[r.Ref.SessionID] = true
	}

	var roots []model.RequestSample
	for _, r := range rows {
		parent := r.BlockedBy
		if parent == 0 || parent == r.Ref.SessionID || !present[parent] {
			roots = append(roots, r)
			continue
		}
		children[parent] = append(children[parent], r)
	}

	bySPID := func(s []model.RequestSample) {
		sort.Slice(s, func(i, j int) bool { return s[i].Ref.SessionID < s[j].Ref.SessionID })
	}
	bySPID(roots)
	for k := range children {
		bySPID(children[k])
	}

	out := make([]model.RequestSample, 0, len(rows))
	seen := make(map[int64]bool, len(rows))

	var walk func(r model.RequestSample, depth int)
	walk = func(r model.RequestSample, depth int) {
		if seen[r.Ref.SessionID] {
			return // cycle, or a row reachable twice: emit it once
		}
		seen[r.Ref.SessionID] = true
		r.Depth = depth
		out = append(out, r)
		for _, c := range children[r.Ref.SessionID] {
			walk(c, depth+1)
		}
	}

	for _, r := range roots {
		walk(r, 0)
	}

	// A pure cycle has no root, so nothing above reached it. Emit whatever
	// is left at depth zero rather than losing it.
	if len(out) < len(rows) {
		leftovers := make([]model.RequestSample, 0, len(rows)-len(out))
		for _, r := range rows {
			if !seen[r.Ref.SessionID] {
				leftovers = append(leftovers, r)
			}
		}
		bySPID(leftovers)
		for _, r := range leftovers {
			walk(r, 0)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/window/ -v`
Expected: PASS, eleven tests across the package.

- [ ] **Step 5: Commit**

```bash
git add internal/window/blocking.go internal/window/blocking_test.go
git commit -m "Flatten blocking chains into an indentation depth

The wire protocol carries a flat list, not a tree: the bench measured
that a tree cannot survive delta updates and that the delta saves nothing
anyway.

Three cases the engine really produces are covered, and none may drop a
row, because a request missing from the grid is worse than one shown at
the wrong indentation: a session reporting itself as its own blocker, a
cycle between two sessions, and a blocker whose request finished between
two reads."
```

---

### Task 5: Performance counter arithmetic

**Files:**
- Create: `internal/source/mssql/counters.go`, `internal/source/mssql/counters_test.go`

**Interfaces:**
- Consumes: `model.Figure`.
- Produces:
  - `mssql.counterKind` with `kindRaw`, `kindPerSecond`, `kindRatio`
  - `mssql.counterDef{key, object, name, kind, baseName, unit string}`
  - `mssql.counterDefs` slice, the catalogue the query filters on
  - `mssql.counterState` with `func (s *counterState) apply(at time.Time, raw map[string]int64) map[string]model.Figure`

This is the single highest-value test in the plan. Spec section 6 records the semantics: `cntr_type` 65792 is a raw value; 272696320 and 272696576 are cumulative per-second counters whose rate is the delta between two samples; 537003264 is a ratio whose denominator is a separate base counter of type 1073939712, and it too must be differentiated. Read raw instead, the buffer cache hit ratio sits at 99-point-something on every server in the world and tells you nothing. Getting this wrong produces numbers that look right.

- [ ] **Step 1: Write the failing arithmetic tests**

Create `internal/source/mssql/counters_test.go`:

```go
package mssql

import (
	"math"
	"testing"
	"time"
)

func TestFirstSampleIsUnavailableNotZero(t *testing.T) {
	s := newCounterState()
	got := s.apply(time.Now(), map[string]int64{"batch_requests_sec": 1000})

	if f := got["batch_requests_sec"]; f.Available {
		t.Fatal("a rate needs two samples; the first tick must report unavailable rather than a zero that reads as an idle server")
	}
}

func TestPerSecondIsADelta(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"batch_requests_sec": 1250})

	f := got["batch_requests_sec"]
	if !f.Available {
		t.Fatal("the second sample must produce a value")
	}
	if math.Abs(f.Value-250) > 0.01 {
		t.Fatalf("rate = %v, want 250 per second", f.Value)
	}
}

func TestPerSecondScalesByElapsedTime(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1000})
	got := s.apply(t0.Add(5*time.Second), map[string]int64{"batch_requests_sec": 2000})

	if f := got["batch_requests_sec"]; math.Abs(f.Value-200) > 0.01 {
		t.Fatalf("rate = %v, want 200 per second over five seconds, not the raw 1000 delta", f.Value)
	}
}

func TestRatioUsesItsBase(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"buffer_cache_hit_ratio": 900, "buffer_cache_hit_ratio__base": 1000})
	// Ninety hits out of a hundred lookups in the last interval.
	got := s.apply(t0.Add(time.Second), map[string]int64{"buffer_cache_hit_ratio": 990, "buffer_cache_hit_ratio__base": 1100})

	f := got["buffer_cache_hit_ratio"]
	if !f.Available {
		t.Fatal("the ratio must be available on the second sample")
	}
	if math.Abs(f.Value-90) > 0.01 {
		t.Fatalf("ratio = %v, want 90 percent for the interval, not the lifetime figure", f.Value)
	}
}

func TestRatioWithNoActivityIsUnavailable(t *testing.T) {
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"buffer_cache_hit_ratio": 900, "buffer_cache_hit_ratio__base": 1000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"buffer_cache_hit_ratio": 900, "buffer_cache_hit_ratio__base": 1000})

	if f := got["buffer_cache_hit_ratio"]; f.Available {
		t.Fatal("no lookups in the interval means no ratio; reporting zero percent would claim every read missed the cache")
	}
}

func TestRawPassesThrough(t *testing.T) {
	s := newCounterState()
	got := s.apply(time.Now(), map[string]int64{"page_life_expectancy": 4200})

	f := got["page_life_expectancy"]
	if !f.Available || math.Abs(f.Value-4200) > 0.01 {
		t.Fatalf("raw counter = %+v, want 4200 available on the first sample", f)
	}
}

func TestCounterResetIsNotGarbage(t *testing.T) {
	// The instance restarted between two ticks, so the cumulative counter
	// went backwards. A negative rate is nonsense and must not be shown.
	s := newCounterState()
	t0 := time.Now()
	s.apply(t0, map[string]int64{"batch_requests_sec": 1_000_000})
	got := s.apply(t0.Add(time.Second), map[string]int64{"batch_requests_sec": 12})

	if f := got["batch_requests_sec"]; f.Available {
		t.Fatal("a counter that went backwards means a restart; the tick must be skipped, not reported as a negative rate")
	}
}

func TestEveryDefinitionHasAKindAndAUnit(t *testing.T) {
	for _, d := range counterDefs {
		if d.key == "" || d.object == "" || d.name == "" {
			t.Errorf("incomplete definition: %+v", d)
		}
		if d.kind == kindRatio && d.baseName == "" {
			t.Errorf("%s is a ratio and must name its base counter", d.key)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/source/mssql/ -v`
Expected: FAIL to build, `undefined: newCounterState`.

- [ ] **Step 3: Implement the counter catalogue and arithmetic**

Create `internal/source/mssql/counters.go`:

```go
package mssql

import (
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// counterKind mirrors the cntr_type semantics documented for
// sys.dm_os_performance_counters. Spec section 6.
//
//	65792                  raw current value
//	272696320, 272696576   cumulative per second, the rate is a delta
//	537003264              ratio over a base counter of type 1073939712
type counterKind int

const (
	kindRaw counterKind = iota
	kindPerSecond
	kindRatio
)

type counterDef struct {
	key      string // our stable name, used by the UI
	object   string // object_name, matched with a trailing-space-tolerant LIKE
	name     string // counter_name
	kind     counterKind
	baseName string // counter_name of the base, ratios only
	unit     string
}

// counterDefs is the catalogue the collector queries. Only these rows are
// fetched: sys.dm_os_performance_counters returns roughly 1500, and pulling
// them all every second would spend the observation budget on nothing.
var counterDefs = []counterDef{
	{key: "page_life_expectancy", object: "Buffer Manager", name: "Page life expectancy", kind: kindRaw, unit: "s"},
	{key: "buffer_cache_hit_ratio", object: "Buffer Manager", name: "Buffer cache hit ratio", kind: kindRatio, baseName: "Buffer cache hit ratio base", unit: "%"},
	{key: "page_reads_sec", object: "Buffer Manager", name: "Page reads/sec", kind: kindPerSecond, unit: "/s"},
	{key: "page_writes_sec", object: "Buffer Manager", name: "Page writes/sec", kind: kindPerSecond, unit: "/s"},
	{key: "lazy_writes_sec", object: "Buffer Manager", name: "Lazy writes/sec", kind: kindPerSecond, unit: "/s"},
	{key: "batch_requests_sec", object: "SQL Statistics", name: "Batch Requests/sec", kind: kindPerSecond, unit: "/s"},
	{key: "compilations_sec", object: "SQL Statistics", name: "SQL Compilations/sec", kind: kindPerSecond, unit: "/s"},
	{key: "recompilations_sec", object: "SQL Statistics", name: "SQL Re-Compilations/sec", kind: kindPerSecond, unit: "/s"},
	{key: "full_scans_sec", object: "Access Methods", name: "Full Scans/sec", kind: kindPerSecond, unit: "/s"},
	{key: "open_transactions", object: "Transactions", name: "Transactions", kind: kindRaw, unit: ""},
	{key: "longest_transaction_s", object: "Transactions", name: "Longest Transaction Running Time", kind: kindRaw, unit: "s"},
	// Version store size and tempdb free space are deliberately absent here.
	// Both also exist as counters, but the space tier reads them from
	// sys.dm_db_file_space_usage and sys.dm_tran_version_store_space_usage,
	// which spec section 6 names and which are per-database rather than
	// instance-wide. Two sources for one number is how dashboards start
	// disagreeing with themselves.
	{key: "target_server_memory_kb", object: "Memory Manager", name: "Target Server Memory (KB)", kind: kindRaw, unit: "KB"},
	{key: "total_server_memory_kb", object: "Memory Manager", name: "Total Server Memory (KB)", kind: kindRaw, unit: "KB"},
	{key: "memory_grants_pending", object: "Memory Manager", name: "Memory Grants Pending", kind: kindRaw, unit: ""},
}

// baseKey is the map key under which a ratio's denominator is delivered.
func baseKey(key string) string { return key + "__base" }

type counterState struct {
	prev   map[string]int64
	prevAt time.Time
	seeded bool
}

func newCounterState() *counterState {
	return &counterState{prev: map[string]int64{}}
}

// apply turns one raw reading into displayable figures, differentiating what
// has to be differentiated. Figures that cannot be computed yet come back
// with Available false rather than zero: the difference between "not known"
// and "genuinely nothing" is the difference between a useful dashboard and a
// misleading one.
func (s *counterState) apply(at time.Time, raw map[string]int64) map[string]model.Figure {
	out := make(map[string]model.Figure, len(counterDefs))
	elapsed := at.Sub(s.prevAt).Seconds()

	for _, d := range counterDefs {
		cur, ok := raw[d.key]
		if !ok {
			out[d.key] = model.Figure{Unit: d.unit, Available: false}
			continue
		}

		switch d.kind {
		case kindRaw:
			out[d.key] = model.Figure{Value: float64(cur), Unit: d.unit, Available: true}

		case kindPerSecond:
			prev, had := s.prev[d.key]
			switch {
			case !s.seeded || !had || elapsed <= 0:
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			case cur < prev:
				// Went backwards: the instance restarted. Skip this tick.
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			default:
				out[d.key] = model.Figure{Value: float64(cur-prev) / elapsed, Unit: d.unit, Available: true}
			}

		case kindRatio:
			bk := baseKey(d.key)
			curBase, hasBase := raw[bk]
			prev, had := s.prev[d.key]
			prevBase, hadBase := s.prev[bk]
			dn := curBase - prevBase
			switch {
			case !s.seeded || !had || !hadBase || !hasBase:
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			case cur < prev || curBase < prevBase:
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			case dn <= 0:
				// No lookups in the interval. Reporting zero percent would
				// claim every read missed the cache.
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			default:
				out[d.key] = model.Figure{Value: float64(cur-prev) / float64(dn) * 100, Unit: d.unit, Available: true}
			}
		}
	}

	for k, v := range raw {
		s.prev[k] = v
	}
	s.prevAt = at
	s.seeded = true
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/source/mssql/ -v`
Expected: PASS, eight tests.

- [ ] **Step 5: Commit**

```bash
git add internal/source/mssql/counters.go internal/source/mssql/counters_test.go
git commit -m "Add performance counter arithmetic and the counter catalogue

The cntr_type semantics are the place this tool would produce numbers
that look right and are wrong. 65792 is a raw value, 272696320 and
272696576 are cumulative per second and need a delta, and 537003264 is a
ratio whose denominator is a separate base counter of type 1073939712
that must be differentiated too. Read raw, the buffer cache hit ratio
sits at 99-point-something on every server alive and says nothing.

Three cases return unavailable rather than a plausible zero: the first
tick, when no rate can exist yet; a counter that went backwards, which
means the instance restarted; and a ratio whose denominator did not move,
where zero percent would claim every read missed the cache.

The catalogue is filtered deliberately. The view returns about 1500 rows
and pulling all of them every second would spend the observation budget
on nothing."
```

---

### Task 6: The Source interface and a fake

**Files:**
- Create: `internal/source/source.go`
- Create: `internal/source/fake/fake.go`, `internal/source/fake/fake_test.go`

**Interfaces:**
- Consumes: everything from `internal/model`.
- Produces:
  - `source.Source` interface, exactly the signatures below
  - `fake.New(rows []model.RequestSample) *fake.Source` with settable `Caps`, `Info`, `CostPerCall`, and an `Err` field to force failures

`Kill` is absent from the interface in this plan. It is the only destructive action in the tool and its safety lives in a confirmation flow and an audit log (spec 9.1); adding the method before those exist would leave a loaded capability with no guard. It arrives with the UI plan.

- [ ] **Step 1: Write the interface**

Create `internal/source/source.go`:

```go
// Package source is the seam between sqltop and any database engine.
//
// Being agnostic does not mean pretending every engine is the same. It means
// the model is neutral and every source declares what it can actually do, so
// the interface adapts instead of the model lying. Spec section 4.1.
package source

import (
	"context"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

type Source interface {
	// Open connects. It must not create, alter or configure anything on the
	// server: sqltop is read-only.
	Open(ctx context.Context, dsn string) error
	Close() error

	// Identify returns instance metadata and what this source can deliver on
	// this server, at this version, with these rights. It probes rather than
	// inferring from the version alone.
	Identify(ctx context.Context) (model.ServerInfo, model.Capabilities, error)

	// SampleRequests is the hot path, called on the requests tier.
	SampleRequests(ctx context.Context) ([]model.RequestSample, error)

	// SampleServer feeds the dashboard on the slower tiers.
	SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error)

	// Cost reports what this connection has spent on the server so far,
	// cumulative. The collector differentiates it. Spec section 10.
	Cost(ctx context.Context) (model.Cost, error)

	// QueryText and Plan are on demand only and must never be called from a
	// polling loop.
	QueryText(ctx context.Context, ref model.RequestRef) (string, error)
	Plan(ctx context.Context, ref model.RequestRef, live bool) (model.Plan, error)
}
```

- [ ] **Step 2: Write the failing fake test**

Create `internal/source/fake/fake_test.go`:

```go
package fake

import (
	"context"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

func TestFakeSatisfiesSource(t *testing.T) {
	var _ source.Source = New(nil)
}

func TestFakeAccumulatesCost(t *testing.T) {
	ctx := context.Background()
	f := New([]model.RequestSample{{Ref: model.RequestRef{SessionID: 51}}})
	f.CostPerCall = 3

	if _, err := f.SampleRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SampleRequests(ctx); err != nil {
		t.Fatal(err)
	}

	c, err := f.Cost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.CPUMs != 6 {
		t.Fatalf("cost = %d ms, want 6: Cost is cumulative, the collector differentiates it", c.CPUMs)
	}
}

func TestFakeCanFail(t *testing.T) {
	f := New(nil)
	f.Err = context.DeadlineExceeded

	if _, err := f.SampleRequests(context.Background()); err == nil {
		t.Fatal("the fake must be able to fail, so the collector's degradation path is testable")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/source/fake/ -v`
Expected: FAIL to build, `undefined: New`.

- [ ] **Step 4: Implement the fake**

Create `internal/source/fake/fake.go`:

```go
// Package fake is an in-memory Source, so the collector, window and web
// layers can be tested without a database.
package fake

import (
	"context"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

type Source struct {
	mu   sync.Mutex
	rows []model.RequestSample

	Caps        model.Capabilities
	Info        model.ServerInfo
	CostPerCall int64
	Err         error

	cost model.Cost
}

func New(rows []model.RequestSample) *Source {
	return &Source{
		rows: rows,
		Caps: model.Caps(model.CapInstanceWideView, model.CapLivePlanProgress),
		Info: model.ServerInfo{Instance: "fake", MajorVersion: 15},
	}
}

// SetRows replaces what the next sample returns, so a test can make the
// population change between ticks.
func (s *Source) SetRows(rows []model.RequestSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = rows
}

func (s *Source) Open(context.Context, string) error { return s.Err }
func (s *Source) Close() error                       { return nil }

func (s *Source) Identify(context.Context) (model.ServerInfo, model.Capabilities, error) {
	return s.Info, s.Caps, s.Err
}

func (s *Source) SampleRequests(context.Context) ([]model.RequestSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	s.cost.CPUMs += s.CostPerCall
	s.cost.At = time.Now()
	out := make([]model.RequestSample, len(s.rows))
	copy(out, s.rows)
	return out, nil
}

func (s *Source) SampleServer(context.Context, model.Tier) (model.ServerSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return model.ServerSample{}, s.Err
	}
	s.cost.CPUMs += s.CostPerCall
	return model.ServerSample{At: time.Now(), Figures: map[string]model.Figure{}}, nil
}

func (s *Source) Cost(context.Context) (model.Cost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cost
	c.At = time.Now()
	return c, nil
}

func (s *Source) QueryText(context.Context, model.RequestRef) (string, error) {
	return "SELECT 1", s.Err
}

func (s *Source) Plan(_ context.Context, _ model.RequestRef, live bool) (model.Plan, error) {
	return model.Plan{Format: "showplan-xml", Payload: []byte("<ShowPlanXML/>"), Live: live}, s.Err
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/source/... -v`
Expected: PASS, three tests in `fake` plus the counter tests in `mssql`.

- [ ] **Step 6: Commit**

```bash
git add internal/source
git commit -m "Add the Source interface and an in-memory fake

Being agnostic is not pretending every engine is alike: the model stays
neutral and each source declares what it can actually do, so the
interface adapts rather than the model lying.

Kill is deliberately absent. It is the only destructive action in the
tool and its safety lives in a confirmation flow and an audit log; adding
the method before those exist would leave a loaded capability with no
guard. It arrives with the UI plan.

Cost is cumulative and the collector differentiates it, which is how the
observation budget gets measured in server CPU rather than round-trip."
```

---

### Task 7: SQL Server connection, preflight and capabilities

**Files:**
- Create: `internal/source/mssql/mssql.go`, `internal/source/mssql/mssql_test.go`
- Create: `scripts/testdb.sh`
- Modify: `go.mod` (adds `github.com/microsoft/go-mssqldb`)

**Interfaces:**
- Consumes: `source.Source`, `model.*`.
- Produces: `mssql.New() *mssql.Source` satisfying `source.Source`, and `mssql.ErrVersionTooOld`.

Three requirements apply to every query this package sends. `TestEveryQueryCarriesTheHints` enforces two of them mechanically, and lives in task 9 because it sweeps every query constant in the package and the last of them is not written until then. The session runs `READ UNCOMMITTED`, set through the connector's `SessionInitSQL` so it survives a reset rather than being a one-off after the first connect. Every statement ends with `OPTION (RECOMPILE, MAXDOP 1)`: `RECOMPILE` so the collector's own queries do not accumulate in the plan cache of the server it is watching, and `MAXDOP 1` so a monitoring query never takes parallel workers. They share one clause because SQL Server allows only one per statement.

The preflight probes rather than infers. A login may hold `VIEW SERVER STATE` on paper and be denied a specific view, and Azure SQL Database returns only the current session when the right is missing rather than failing. Spec 3.1 and 3.2.

- [ ] **Step 1: Write the container script**

Create `scripts/testdb.sh` and `chmod +x` it:

```sh
#!/usr/bin/env bash
# Start or wake the SQL Server container used by the integration tests, then
# print the export line. Usage: eval "$(scripts/testdb.sh)"
#
# The container is the tool's own, named sqltop-test, created on first use with
# the password below. It deliberately does not reuse whatever SQL Server
# containers happen to exist on the machine: the tests must provision what they
# need rather than depend on someone's local state.
set -euo pipefail

NAME="${SQLTOP_TEST_CONTAINER:-sqltop-test}"
IMAGE="${SQLTOP_TEST_IMAGE:-mcr.microsoft.com/mssql/server:2022-latest}"
PORT="${SQLTOP_TEST_PORT:-11433}"
PASSWORD="${SQLTOP_TEST_PASSWORD:-Sqltop_dev_2026!}"

if ! podman container exists "$NAME"; then
  podman run -d --name "$NAME" \
    -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD="$PASSWORD" \
    -p "$PORT":1433 "$IMAGE" >/dev/null
elif [ "$(podman inspect -f '{{.State.Running}}' "$NAME")" != "true" ]; then
  podman start "$NAME" >/dev/null
fi

# Wait for the engine to accept connections rather than guessing at a sleep.
for _ in $(seq 1 60); do
  if podman exec "$NAME" /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa \
      -P "$PASSWORD" -C -Q "SELECT 1" >/dev/null 2>&1; then
    echo "export SQLTOP_TEST_DSN='sqlserver://sa:${PASSWORD}@127.0.0.1:${PORT}?encrypt=disable'"
    exit 0
  fi
  sleep 2
done

echo "the container did not become ready in two minutes" >&2
exit 1
```

- [ ] **Step 2: Write the failing integration test**

Create `internal/source/mssql/mssql_test.go`:

```go
package mssql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
)

// open connects to the container, or skips. Integration tests must never fail
// on a machine without Podman; `go test ./...` stays green there.
func open(t *testing.T) *Source {
	t.Helper()
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		t.Skip("SQLTOP_TEST_DSN is unset; run: eval \"$(scripts/testdb.sh)\"")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := New()
	if err := s.Open(ctx, dsn); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSatisfiesSource(t *testing.T) {
	var _ source.Source = New()
}

func TestSessionIsReadUncommitted(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	var level string
	err := s.db.QueryRowContext(ctx, `
		SELECT CASE transaction_isolation_level
		    WHEN 1 THEN 'read uncommitted' ELSE 'other' END
		FROM sys.dm_exec_sessions WHERE session_id = @@SPID
		OPTION (RECOMPILE, MAXDOP 1)`).Scan(&level)
	if err != nil {
		t.Fatal(err)
	}
	if level != "read uncommitted" {
		t.Fatalf("isolation level = %q, want read uncommitted: a monitoring tool must not take shared locks on the server it is watching", level)
	}
}

func TestIdentifyReportsVersionAndCapabilities(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	info, caps, err := s.Identify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.MajorVersion < 12 {
		t.Fatalf("major version = %d, want at least 12; below that the tool refuses to connect", info.MajorVersion)
	}
	if info.ProductVersion == "" || info.Edition == "" {
		t.Fatalf("info = %+v, want the product version and edition filled", info)
	}
	if info.MajorVersion >= 15 && !caps.Has(model.CapLivePlanProgress) {
		t.Error("lightweight profiling v3 is on by default from 2019, so live plan progress must be advertised")
	}
	if !caps.Has(model.CapInstanceWideView) {
		t.Error("a container instance is not Azure SQL Database, so the instance-wide view must be advertised")
	}
}

func TestCostIsCumulativeAndNonZero(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	first, err := s.Cost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s.SampleRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	second, err := s.Cost(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if second.CPUMs < first.CPUMs {
		t.Fatalf("cost went backwards, %d then %d: it must be cumulative for the collector to differentiate it", first.CPUMs, second.CPUMs)
	}
	if second.LogicalReads <= first.LogicalReads {
		t.Error("twenty samples should have cost some logical reads; a flat zero means we are reading the wrong session")
	}
}
```

- [ ] **Step 3: Run it and watch it skip, then fail**

Run:
```bash
go test ./internal/source/mssql/ -run TestIdentify -v
eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run TestIdentify -v
```
Expected: first run SKIP; after the container is up, FAIL to build with `undefined: New`.

- [ ] **Step 4: Add the driver dependency**

Run:
```bash
go get github.com/microsoft/go-mssqldb@latest
go get github.com/microsoft/go-mssqldb/integratedauth/krb5@latest
go mod tidy
```

- [ ] **Step 5: Implement connection, preflight and capabilities**

Create `internal/source/mssql/mssql.go`:

```go
// Package mssql is the SQL Server implementation of source.Source.
//
// Everything here is read-only. No object is created, nothing is configured,
// no trace flag is set: spec section 2.
package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	_ "github.com/microsoft/go-mssqldb/integratedauth/krb5"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// ErrVersionTooOld is returned for anything below SQL Server 2012, where the
// tool refuses to connect rather than failing query by query. Spec section 3.
var ErrVersionTooOld = errors.New("sqltop: SQL Server 2012 or later is required")

type Source struct {
	db      *sql.DB
	spid    int64
	info    model.ServerInfo
	caps    model.Capabilities
	counter *counterState
}

func New() *Source { return &Source{counter: newCounterState()} }

// sessionInit runs on every new or reset session, so the settings survive a
// reconnection rather than being a one-off after the first connect.
//
// READ UNCOMMITTED because a monitoring tool must not take shared locks on the
// server it is watching. NOCOUNT because the row counts are noise on the wire.
const sessionInit = `SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED; SET NOCOUNT ON;`

func (s *Source) Open(ctx context.Context, dsn string) error {
	connector, err := mssql.NewConnector(dsn)
	if err != nil {
		return fmt.Errorf("mssql: open: %w", err)
	}
	connector.SessionInitSQL = sessionInit
	db := sql.OpenDB(connector)

	// One connection, always the same one, because Cost reads @@SPID and a
	// pool would hand us a different session on every call.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("mssql: connect: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT @@SPID").Scan(&s.spid); err != nil {
		db.Close()
		return fmt.Errorf("mssql: reading own spid: %w", err)
	}
	s.db = db
	return nil
}

func (s *Source) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

const identifyQuery = `
SELECT
    CAST(SERVERPROPERTY('ServerName')      AS nvarchar(256)),
    CAST(SERVERPROPERTY('MachineName')     AS nvarchar(256)),
    CAST(SERVERPROPERTY('Edition')         AS nvarchar(256)),
    CAST(SERVERPROPERTY('ProductVersion')  AS nvarchar(64)),
    CAST(SERVERPROPERTY('EngineEdition')   AS int)
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) Identify(ctx context.Context) (model.ServerInfo, model.Capabilities, error) {
	var info model.ServerInfo
	var machine, version sql.NullString
	var name sql.NullString
	var engineEdition int

	err := s.db.QueryRowContext(ctx, identifyQuery).
		Scan(&name, &machine, &info.Edition, &version, &engineEdition)
	if err != nil {
		return info, 0, fmt.Errorf("mssql: identify: %w", err)
	}
	info.Instance = name.String
	info.Host = machine.String
	info.ProductVersion = version.String
	info.MajorVersion = majorVersion(version.String)
	// EngineEdition 5 is Azure SQL Database, 8 is Managed Instance.
	info.IsAzureSQLDB = engineEdition == 5

	if !info.IsAzureSQLDB && info.MajorVersion > 0 && info.MajorVersion < 11 {
		return info, 0, fmt.Errorf("%w (found %s)", ErrVersionTooOld, info.ProductVersion)
	}

	caps := s.probe(ctx, info)
	s.info, s.caps = info, caps
	return info, caps, nil
}

// majorVersion turns "15.0.4335.1" into 15. Zero when it cannot tell, which
// makes the version-gated capabilities fall back to probing.
func majorVersion(product string) int {
	head, _, _ := strings.Cut(product, ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0
	}
	return n
}

// probe asks the server what actually works rather than inferring rights from
// the version. A login can hold VIEW SERVER STATE on paper and be denied one
// view, and Azure SQL Database quietly returns only the current session when
// the right is missing instead of raising an error. Spec section 3.1.
func (s *Source) probe(ctx context.Context, info model.ServerInfo) model.Capabilities {
	var caps model.Capabilities

	// Every probe carries the hint too, so nothing this package sends can
	// take a parallel worker or leave a plan behind.
	can := func(query string) bool {
		var one int
		err := s.db.QueryRowContext(ctx, query+" OPTION (RECOMPILE, MAXDOP 1)").Scan(&one)
		return err == nil
	}

	if !info.IsAzureSQLDB {
		// Ask the server what the login holds rather than counting visible
		// sessions. Counting works in practice, since an instance always has
		// dozens of system sessions, but it answers a different question and
		// would be wrong the day it is asked on something quieter.
		var granted int
		if err := s.db.QueryRowContext(ctx,
			`SELECT CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
			              OR HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE') = 1
			         THEN 1 ELSE 0 END
			 OPTION (RECOMPILE, MAXDOP 1)`).
			Scan(&granted); err == nil && granted == 1 {
			caps = caps.With(model.CapInstanceWideView)
		}
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_os_schedulers`) {
		caps = caps.With(model.CapSchedulerLoad)
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_os_wait_stats`) {
		caps = caps.With(model.CapWaitStatsCumulative)
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_db_task_space_usage`) {
		caps = caps.With(model.CapTempdbPerTask)
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_tran_version_store_space_usage`) {
		caps = caps.With(model.CapVersionStoreUsage)
	}
	if !info.IsAzureSQLDB && can(
		`SELECT TOP (1) 1 FROM sys.dm_os_ring_buffers WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'`) {
		caps = caps.With(model.CapRingBufferCPU)
	}
	// Lightweight profiling v3 is on by default from 2019 and on Azure SQL
	// Database. Below that it needs trace flag 7412, which the tool will not
	// set, so the feature is simply absent. Spec section 3.
	if info.IsAzureSQLDB || info.MajorVersion >= 15 {
		if can(`SELECT TOP (1) 1 FROM sys.dm_exec_query_statistics_xml(@@SPID)`) {
			caps = caps.With(model.CapLivePlanProgress)
		}
	}
	return caps
}

const costQuery = `
SELECT cpu_time, logical_reads
FROM sys.dm_exec_sessions
WHERE session_id = @@SPID
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) Cost(ctx context.Context) (model.Cost, error) {
	var c model.Cost
	if err := s.db.QueryRowContext(ctx, costQuery).Scan(&c.CPUMs, &c.LogicalReads); err != nil {
		return c, fmt.Errorf("mssql: own cost: %w", err)
	}
	c.At = time.Now()
	return c, nil
}

// errNotInThisPlan keeps mssql.Source satisfying source.Source from this task
// onward. The on-demand pair ships with the UI plan, where the plan panel that
// consumes them lives; a real implementation with no caller would be code
// nothing exercises.
var errNotInThisPlan = errors.New("mssql: query text and plan retrieval arrive with the UI plan")

func (s *Source) QueryText(context.Context, model.RequestRef) (string, error) {
	return "", errNotInThisPlan
}

func (s *Source) Plan(context.Context, model.RequestRef, bool) (model.Plan, error) {
	return model.Plan{}, errNotInThisPlan
}
```

`SampleRequests` and `SampleServer` arrive in tasks 8 and 9. Until then, add
these two stubs at the end of the same file so the package compiles and
`TestSatisfiesSource` passes:

```go
func (s *Source) SampleRequests(context.Context) ([]model.RequestSample, error) {
	return nil, nil // task 8
}

func (s *Source) SampleServer(context.Context, model.Tier) (model.ServerSample, error) {
	return model.ServerSample{Figures: map[string]model.Figure{}}, nil // task 9
}
```

Delete each stub in the task that replaces it.

- [ ] **Step 6: Run the integration tests to verify they pass**

Run:
```bash
eval "$(scripts/testdb.sh)"
go test ./internal/source/mssql/ -v
```
Expected: PASS. `TestIdentifyReportsVersionAndCapabilities` reports major version 16 on the 2022 image, with `CapLivePlanProgress` and `CapInstanceWideView` present.

- [ ] **Step 7: Verify the tests still skip cleanly without the container**

Run: `env -u SQLTOP_TEST_DSN go test ./... `
Expected: PASS everywhere, with the mssql integration tests reported as skipped.

- [ ] **Step 8: Verify no CGO regression from the new dependency**

Run: `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...`
Expected: both succeed.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal/source/mssql scripts/testdb.sh
git commit -m "Add the SQL Server source: connect, preflight, capabilities

The preflight probes rather than infers. A login can hold VIEW SERVER
STATE on paper and be denied one view, and Azure SQL Database quietly
returns only the current session when the right is missing instead of
raising an error, so asking the version what the login can do would be
wrong on both counts.

The pool is pinned to a single connection on purpose: Cost reads
@@SPID, and a pool would hand a different session to every call, making
the observation budget measure someone else.

Anything below SQL Server 2012 is refused at connection with an
explanation, rather than failing query by query later.

Integration tests read SQLTOP_TEST_DSN and skip when it is unset, so
go test ./... stays green on a machine without Podman. scripts/testdb.sh
starts or wakes the container and waits for the engine to answer instead
of guessing at a sleep."
```

---

### Task 8: Sampling active requests

**Files:**
- Create: `internal/source/mssql/requests.go`
- Modify: `internal/source/mssql/mssql_test.go` (append the tests below)

**Interfaces:**
- Consumes: `Source` from task 7.
- Produces: `(*Source).SampleRequests(ctx) ([]model.RequestSample, error)`.

The statement text uses the offsets, so the grid shows the statement currently running rather than the whole batch. `Depth` is left at zero here: flattening is engine-neutral and belongs to the window (task 4).

- [ ] **Step 1: Write the failing test**

Append to `internal/source/mssql/mssql_test.go`:

```go
func TestSampleRequestsSeesALongQuery(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	// A second connection runs something slow enough to be caught.
	victim, err := sql.Open("sqlserver", os.Getenv("SQLTOP_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		victim.ExecContext(ctx, `WAITFOR DELAY '00:00:06'`)
	}()
	defer <-done

	time.Sleep(1500 * time.Millisecond)

	rows, err := s.SampleRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var found *model.RequestSample
	for i := range rows {
		if strings.Contains(rows[i].SQLText, "WAITFOR DELAY") {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the running WAITFOR was not in the %d sampled rows", len(rows))
	}
	if found.Ref.SessionID == 0 {
		t.Error("session id must be filled")
	}
	if found.Database == "" {
		t.Error("database must be filled: it is a filter and sort column")
	}
	if found.Command == "" {
		t.Error("command must be filled: it is a filter and sort column")
	}
	if found.ElapsedMs <= 0 {
		t.Errorf("elapsed = %d ms, want a positive value after 1.5 seconds", found.ElapsedMs)
	}
	if found.Depth != 0 {
		t.Error("Depth is the window's job, not the source's")
	}
}

func TestIsolationSurvivesASessionReset(t *testing.T) {
	// SessionInitSQL is what makes this true. A one-off SET after connecting
	// would be lost the moment database/sql resets or re-establishes the
	// connection, and the tool would start locking without anyone noticing.
	s := open(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := s.SampleRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var level int
	if err := s.db.QueryRowContext(ctx,
		`SELECT transaction_isolation_level FROM sys.dm_exec_sessions
		 WHERE session_id = @@SPID OPTION (RECOMPILE, MAXDOP 1)`).Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level != 1 {
		t.Fatalf("isolation level = %d after several queries, want 1", level)
	}
}

func TestSampleRequestsExcludesItself(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	rows, err := s.SampleRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Ref.SessionID == s.spid {
			t.Fatal("the tool must not report its own collection query as activity")
		}
	}
}
```

Add `"database/sql"` and `"strings"` to that file's imports if they are not already there.

- [ ] **Step 2: Run it to verify it fails**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run TestSampleRequests -v`
Expected: FAIL, `SampleRequests` returns nothing yet.

- [ ] **Step 3: Implement the query**

Create `internal/source/mssql/requests.go`:

```go
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// requestsQuery is the hot path, run once per requests tier.
//
// Two deliberate choices. The statement text comes from the offsets rather
// than the whole batch, so the grid shows what is running now. And the tool
// filters out its own spid: reporting its own collection query as server
// activity would be both noise and a small lie.
const requestsQuery = `
SELECT
    r.session_id,
    r.request_id,
    RTRIM(r.status),
    ISNULL(DB_NAME(r.database_id), ''),
    ISNULL(s.login_name, ''),
    ISNULL(s.host_name, ''),
    ISNULL(s.program_name, ''),
    RTRIM(ISNULL(r.command, '')),
    ISNULL(r.blocking_session_id, 0),
    r.total_elapsed_time,
    r.cpu_time,
    r.logical_reads,
    r.reads,
    r.writes,
    ISNULL(tsu.tempdb_pages, 0) / 128.0,
    ISNULL(r.granted_query_memory, 0) * 8.0 / 1024.0,
    ISNULL(r.dop, 0),
    ISNULL(s.open_transaction_count, 0),
    ISNULL(r.percent_complete, 0),
    CASE ISNULL(s.transaction_isolation_level, 0)
        WHEN 0 THEN '' WHEN 1 THEN 'read uncommitted' WHEN 2 THEN 'read committed'
        WHEN 3 THEN 'repeatable read' WHEN 4 THEN 'serializable' WHEN 5 THEN 'snapshot'
        ELSE '' END,
    RTRIM(ISNULL(r.wait_type, '')),
    ISNULL(r.wait_time, 0),
    RTRIM(ISNULL(r.wait_resource, '')),
    ISNULL(CONVERT(varchar(34), r.query_hash, 1), ''),
    ISNULL(SUBSTRING(t.text,
        (r.statement_start_offset / 2) + 1,
        ((CASE r.statement_end_offset
            WHEN -1 THEN DATALENGTH(t.text)
            ELSE r.statement_end_offset
          END - r.statement_start_offset) / 2) + 1), '')
FROM sys.dm_exec_requests AS r
    LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = r.session_id
    OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) AS t
    OUTER APPLY (
        SELECT SUM(u.user_objects_alloc_page_count
                 + u.internal_objects_alloc_page_count) AS tempdb_pages
        FROM sys.dm_db_task_space_usage AS u
        WHERE u.session_id = r.session_id
    ) AS tsu
WHERE r.session_id <> @@SPID
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) SampleRequests(ctx context.Context) ([]model.RequestSample, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx, requestsQuery)
	if err != nil {
		return nil, fmt.Errorf("mssql: sample requests: %w", err)
	}
	defer rows.Close()

	var out []model.RequestSample
	for rows.Next() {
		var r model.RequestSample
		var requestID sql.NullInt32
		err := rows.Scan(
			&r.Ref.SessionID, &requestID, &r.Status, &r.Database,
			&r.Login, &r.Host, &r.Program, &r.Command, &r.BlockedBy,
			&r.ElapsedMs, &r.CPUMs, &r.LogicalReads, &r.PhysicalReads, &r.Writes,
			&r.TempdbMB, &r.MemoryGrantMB, &r.DOP, &r.OpenTran, &r.PercentComplete,
			&r.IsolationLevel,
			&r.WaitType, &r.WaitMs, &r.WaitResource, &r.QueryHash, &r.SQLText,
		)
		if err != nil {
			return nil, fmt.Errorf("mssql: scan request: %w", err)
		}
		r.At = now
		r.Ref.RequestID = requestID.Int32
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -v`
Expected: PASS, including both new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/source/mssql/requests.go internal/source/mssql/mssql_test.go
git commit -m "Sample active requests

The statement text comes from the offsets rather than the whole batch,
so the grid shows what is running now instead of the enclosing
procedure.

The tool filters out its own spid: reporting its own collection query as
server activity would be noise and a small lie.

Depth stays zero here. Flattening the blocking chain is engine-neutral
work and belongs to the window, not to a source."
```

---

### Task 9: Sampling the server, per tier

**Files:**
- Create: `internal/source/mssql/server.go`
- Modify: `internal/source/mssql/mssql_test.go` (append)

**Interfaces:**
- Consumes: `counterDefs`, `counterState` from task 5.
- Produces: `(*Source).SampleServer(ctx, tier) (model.ServerSample, error)`.

Each tier answers a different question at a different price, which is the whole point of spec section 10: the counters tier is one filtered round trip per second, the space tier is heavier and runs every five, and the CPU history tier runs once a minute because the engine only produces one sample a minute anyway.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/mssql/mssql_test.go`. `TestEveryQueryCarriesTheHints` sweeps every query constant in the package, so it lands here, with the last of them:

```go
func TestSampleServerCountersNeedTwoTicks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	first, err := s.SampleServer(ctx, model.TierCounters)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := first.Figures["batch_requests_sec"]; !ok {
		t.Fatal("the key must be present even before it has a value")
	} else if f.Available {
		t.Fatal("a rate cannot exist on the first tick")
	}
	if f := first.Figures["page_life_expectancy"]; !f.Available || f.Value <= 0 {
		t.Fatalf("PLE = %+v, want a raw value available immediately", f)
	}
	if f := first.Figures["total_server_memory_kb"]; !f.Available || f.Value <= 0 {
		t.Fatalf("committed memory = %+v, want a positive raw value from the counters", f)
	}

	time.Sleep(1200 * time.Millisecond)

	second, err := s.SampleServer(ctx, model.TierCounters)
	if err != nil {
		t.Fatal(err)
	}
	if f := second.Figures["batch_requests_sec"]; !f.Available {
		t.Fatal("the second tick must produce a rate")
	}
}

func TestSampleServerSpaceTier(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, _, err := s.Identify(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := s.SampleServer(ctx, model.TierSpace)
	if err != nil {
		t.Fatal(err)
	}
	if f := got.Figures["tempdb_used_mb"]; !f.Available {
		t.Fatal("tempdb usage must be available on a container instance")
	}
	if _, present := got.Figures["total_server_memory_mb"]; present {
		t.Fatal("server memory belongs to the counter catalogue, not the space tier: one figure, one source")
	}
}

// TestEveryQueryCarriesTheHints guards the three requirements that are easy to
// forget the day someone adds a query: read uncommitted comes from the session,
// but RECOMPILE keeps the plan out of the cache and MAXDOP 1 keeps a monitoring
// query from taking parallel workers on the server it is watching. Both are per
// statement, and SQL Server allows only one OPTION clause per query, so they
// have to travel together.
func TestEveryQueryCarriesTheHints(t *testing.T) {
	queries := map[string]string{
		"identify":   identifyQuery,
		"cost":       costQuery,
		"requests":   requestsQuery,
		"counters":   countersQuery,
		"space":      spaceQuery,
		"cpuHistory": cpuHistoryQuery,
	}
	for name, q := range queries {
		if !strings.Contains(q, "OPTION (RECOMPILE, MAXDOP 1)") {
			t.Errorf("%s query is missing OPTION (RECOMPILE, MAXDOP 1)", name)
		}
		if strings.Count(strings.ToUpper(q), "OPTION (") != 1 {
			t.Errorf("%s query has %d OPTION clauses, SQL Server allows one", name, strings.Count(strings.ToUpper(q), "OPTION ("))
		}
	}
}

func TestUnavailableFigureIsMarkedNotOmitted(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	info, _, err := s.Identify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsAzureSQLDB {
		t.Skip("this asserts the on-premises path")
	}
	got, err := s.SampleServer(ctx, model.TierCPUHistory)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Figures["sql_cpu_percent"]; !ok {
		t.Fatal("a figure this source cannot produce must still appear with Available false, so one tile can vanish without its neighbours")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -run TestSampleServer -v`
Expected: FAIL, `SampleServer` returns an empty sample.

- [ ] **Step 3: Implement the tiers**

Create `internal/source/mssql/server.go`:

```go
package mssql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// countersQuery pulls only the catalogue's rows. object_name is CHAR-padded,
// hence the LTRIM/RTRIM comparison rather than equality.
var countersQuery = buildCountersQuery()

func buildCountersQuery() string {
	var names []string
	seen := map[string]bool{}
	for _, d := range counterDefs {
		for _, n := range []string{d.name, d.baseName} {
			if n != "" && !seen[n] {
				seen[n] = true
				names = append(names, "N'"+strings.ReplaceAll(n, "'", "''")+"'")
			}
		}
	}
	return `
SELECT RTRIM(LTRIM(object_name)), RTRIM(LTRIM(counter_name)), cntr_value
FROM sys.dm_os_performance_counters
WHERE RTRIM(LTRIM(counter_name)) IN (` + strings.Join(names, ",") + `)
  AND (instance_name IS NULL OR RTRIM(LTRIM(instance_name)) IN (N'', N'_Total'))
OPTION (RECOMPILE, MAXDOP 1)`
}

func (s *Source) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	out := model.ServerSample{At: time.Now(), Figures: map[string]model.Figure{}}

	switch tier {
	case model.TierCounters:
		raw, err := s.readCounters(ctx)
		if err != nil {
			return out, err
		}
		out.Figures = s.counter.apply(out.At, raw)

	case model.TierSpace:
		if err := s.readSpace(ctx, out.Figures); err != nil {
			return out, err
		}

	case model.TierCPUHistory:
		if err := s.readCPUHistory(ctx, out.Figures); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Source) readCounters(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, countersQuery)
	if err != nil {
		return nil, fmt.Errorf("mssql: counters: %w", err)
	}
	defer rows.Close()

	byName := map[string]int64{}
	for rows.Next() {
		var object, name string
		var value int64
		if err := rows.Scan(&object, &name, &value); err != nil {
			return nil, err
		}
		byName[object+"|"+name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	raw := make(map[string]int64, len(counterDefs))
	for _, d := range counterDefs {
		for k, v := range byName {
			obj, n, _ := strings.Cut(k, "|")
			if !strings.HasSuffix(obj, d.object) {
				continue
			}
			if n == d.name {
				raw[d.key] = v
			}
			if d.baseName != "" && n == d.baseName {
				raw[baseKey(d.key)] = v
			}
		}
	}
	return raw, nil
}

// spaceQuery covers only what the performance counters do not. Server memory
// lives in the counter catalogue instead, both because it belongs in the same
// single round trip as the rest and because memory pressure moves faster than
// the five second space tier would show it.
const spaceQuery = `
SELECT
    (SELECT SUM(user_object_reserved_page_count + internal_object_reserved_page_count
              + version_store_reserved_page_count) * 8.0 / 1024.0
       FROM tempdb.sys.dm_db_file_space_usage),
    (SELECT SUM(unallocated_extent_page_count) * 8.0 / 1024.0
       FROM tempdb.sys.dm_db_file_space_usage)
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) readSpace(ctx context.Context, into map[string]model.Figure) error {
	var usedMB, freeMB float64
	if err := s.db.QueryRowContext(ctx, spaceQuery).Scan(&usedMB, &freeMB); err != nil {
		return fmt.Errorf("mssql: space: %w", err)
	}
	into["tempdb_used_mb"] = model.Figure{Value: usedMB, Unit: "MB", Available: true}
	into["tempdb_free_mb"] = model.Figure{Value: freeMB, Unit: "MB", Available: true}

	// Version store is its own view and its own capability: cheap by
	// documentation, since it does not walk individual version records.
	into["version_store_mb"] = model.Figure{Unit: "MB", Available: false}
	if s.caps.Has(model.CapVersionStoreUsage) {
		var kb float64
		if err := s.db.QueryRowContext(ctx,
			`SELECT ISNULL(SUM(reserved_space_kb), 0) FROM sys.dm_tran_version_store_space_usage
			 OPTION (RECOMPILE, MAXDOP 1)`).
			Scan(&kb); err == nil {
			into["version_store_mb"] = model.Figure{Value: kb / 1024.0, Unit: "MB", Available: true}
		}
	}
	return nil
}

// cpuHistoryQuery reads the most recent scheduler-monitor record. The engine
// writes one a minute and keeps 256; both figures are its own, not settings.
const cpuHistoryQuery = `
SELECT TOP (1)
    record.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int'),
    record.value('(./Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int')
FROM (
    SELECT CONVERT(xml, record) AS record, timestamp
    FROM sys.dm_os_ring_buffers
    WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
      AND record LIKE '%<SystemHealth>%'
) AS x
ORDER BY timestamp DESC
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) readCPUHistory(ctx context.Context, into map[string]model.Figure) error {
	// The keys always exist so one tile can be unavailable without its
	// neighbours disappearing. Spec section 4.1.
	into["sql_cpu_percent"] = model.Figure{Unit: "%", Available: false}
	into["other_cpu_percent"] = model.Figure{Unit: "%", Available: false}

	if !s.caps.Has(model.CapRingBufferCPU) {
		return nil
	}
	var sqlCPU, idle int
	if err := s.db.QueryRowContext(ctx, cpuHistoryQuery).Scan(&sqlCPU, &idle); err != nil {
		return nil // absent history is not an error, it is an unavailable figure
	}
	into["sql_cpu_percent"] = model.Figure{Value: float64(sqlCPU), Unit: "%", Available: true}
	into["other_cpu_percent"] = model.Figure{Value: float64(100 - idle - sqlCPU), Unit: "%", Available: true}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql/ -v`
Expected: PASS. `TestUnavailableFigureIsMarkedNotOmitted` passes whether or not the container has ring-buffer history yet, which is the point.

- [ ] **Step 5: Commit**

```bash
git add internal/source/mssql/server.go internal/source/mssql/mssql_test.go
git commit -m "Sample the server, one query shape per tier

Each tier answers a different question at a different price. The counters
tier is one filtered round trip: the view returns about 1500 rows and the
catalogue asks for sixteen. The space tier is heavier and runs every five
seconds. The CPU history tier runs once a minute because the engine only
writes one scheduler-monitor record a minute and keeps 256, both of which
are its numbers rather than settings of ours.

Keys are always present, with Available false when this server cannot
produce the figure, so one dashboard tile can vanish without taking its
neighbours with it."
```

---

### Task 10: The observation budget and its throttle

**Files:**
- Create: `internal/collector/budget.go`, `internal/collector/budget_test.go`

**Interfaces:**
- Consumes: `model.Cost`, `model.Tier`, `config.Tiers`.
- Produces:
  - `collector.NewBudget(limitMsPerSecond int, base config.Tiers) *collector.Budget`
  - `(*Budget).Observe(c model.Cost)` fed the cumulative cost each second
  - `(*Budget).Period(tier model.Tier) time.Duration`
  - `(*Budget).State() (usedMsPerSecond float64, level int, message string)`

Spec section 10, corrected: the budget is server CPU milliseconds per second, obtained by differentiating the tool's own `cpu_time`. Never round-trip, which would carry network latency and throttle a healthy but distant server. Throttling is ordered, not proportional: tier C first, then B, and A last because the request grid is the tool. On-demand work is never throttled, since it only happens when a human asked.

- [ ] **Step 1: Write the failing tests**

Create `internal/collector/budget_test.go`:

```go
package collector

import (
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

func baseTiers() config.Tiers {
	return config.Tiers{
		Requests:   config.Duration(time.Second),
		Counters:   config.Duration(time.Second),
		Space:      config.Duration(5 * time.Second),
		CPUHistory: config.Duration(time.Minute),
		LivePlan:   config.Duration(2 * time.Second),
	}
}

func feed(b *Budget, start time.Time, seconds int, msPerSecond int64) {
	var total int64
	for i := 1; i <= seconds; i++ {
		total += msPerSecond
		b.Observe(model.Cost{At: start.Add(time.Duration(i) * time.Second), CPUMs: total})
	}
}

func TestUnderBudgetKeepsBasePeriods(t *testing.T) {
	b := NewBudget(50, baseTiers())
	feed(b, time.Now(), 15, 6)

	if got := b.Period(model.TierSpace); got != 5*time.Second {
		t.Fatalf("space period = %v, want the base 5s while under budget", got)
	}
	used, level, _ := b.State()
	if level != 0 {
		t.Fatalf("level = %d, want 0", level)
	}
	if used < 5 || used > 7 {
		t.Fatalf("used = %v ms/s, want about 6", used)
	}
}

func TestFirstObservationCannotThrottle(t *testing.T) {
	b := NewBudget(50, baseTiers())
	// A single cumulative reading carries no rate; a huge one must not be
	// mistaken for a huge rate.
	b.Observe(model.Cost{At: time.Now(), CPUMs: 900_000})

	if _, level, _ := b.State(); level != 0 {
		t.Fatalf("level = %d, want 0: one cumulative sample is not a rate", level)
	}
}

func TestOverBudgetDegradesSpaceFirst(t *testing.T) {
	b := NewBudget(50, baseTiers())
	feed(b, time.Now(), 15, 80)

	if got := b.Period(model.TierSpace); got != 10*time.Second {
		t.Fatalf("space period = %v, want 10s: the least valuable tier slows first", got)
	}
	if got := b.Period(model.TierRequests); got != time.Second {
		t.Fatalf("requests period = %v, want 1s untouched at level 1: the grid is the tool", got)
	}
	if _, _, msg := b.State(); msg == "" {
		t.Fatal("every change must be announced in the status bar")
	}
}

func TestStillOverBudgetDegradesCountersThenRequests(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	feed(b, now, 15, 80)
	feed(b, now.Add(15*time.Second), 15, 80)
	if got := b.Period(model.TierCounters); got != 2*time.Second {
		t.Fatalf("counters period = %v, want 2s at level 2", got)
	}
	feed(b, now.Add(30*time.Second), 15, 80)
	if got := b.Period(model.TierRequests); got != 2*time.Second {
		t.Fatalf("requests period = %v, want 2s at level 3, the last thing to give", got)
	}
}

func TestRecoversOneStepAtATime(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	feed(b, now, 15, 80)
	feed(b, now.Add(15*time.Second), 15, 80)
	if _, level, _ := b.State(); level != 2 {
		t.Fatalf("level = %d, want 2 before recovery", level)
	}

	feed(b, now.Add(30*time.Second), 31, 5)
	if _, level, _ := b.State(); level != 1 {
		t.Fatalf("level = %d, want 1: recovery is one step per thirty quiet seconds, not a jump back", level)
	}
}

func TestCounterResetDoesNotThrottle(t *testing.T) {
	b := NewBudget(50, baseTiers())
	now := time.Now()
	b.Observe(model.Cost{At: now, CPUMs: 1_000_000})
	b.Observe(model.Cost{At: now.Add(time.Second), CPUMs: 5})

	if _, level, _ := b.State(); level != 0 {
		t.Fatal("a reconnection resets cpu_time; a negative delta must be ignored, not read as free capacity or as a spike")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/collector/ -v`
Expected: FAIL to build, `undefined: NewBudget`.

- [ ] **Step 3: Implement the budget**

Create `internal/collector/budget.go`:

```go
// Package collector schedules sampling and keeps the tool inside its
// observation budget.
package collector

import (
	"fmt"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

const (
	budgetWindow   = 10 * time.Second // sliding window the average is taken over
	recoveryPeriod = 30 * time.Second // quiet time required per step back
	maxLevel       = 3
)

type usage struct {
	at time.Time
	ms float64
}

// Budget turns the tool's own cost into a throttle level.
//
// The cost is server CPU, differentiated from cpu_time of the tool's own
// session. It is never a round trip: that would carry network latency, so a
// healthy server on the other side of a WAN would throttle the tool while a
// saturated local server slipped past.
type Budget struct {
	mu sync.Mutex

	limit  float64
	base   config.Tiers
	level  int
	msg    string
	recent []usage

	prev      model.Cost
	seeded    bool
	quietFrom time.Time
}

func NewBudget(limitMsPerSecond int, base config.Tiers) *Budget {
	return &Budget{limit: float64(limitMsPerSecond), base: base}
}

// Observe takes the cumulative cost and differentiates it.
func (b *Budget) Observe(c model.Cost) {
	b.mu.Lock()
	defer b.mu.Unlock()

	prev, seeded := b.prev, b.seeded
	b.prev, b.seeded = c, true
	if !seeded {
		return // one cumulative sample is not a rate
	}

	elapsed := c.At.Sub(prev.At).Seconds()
	if elapsed <= 0 || c.CPUMs < prev.CPUMs {
		// Reconnected, so cpu_time restarted. Skip rather than invent.
		return
	}

	b.recent = append(b.recent, usage{at: c.At, ms: float64(c.CPUMs-prev.CPUMs) / elapsed})
	cutoff := c.At.Add(-budgetWindow)
	keep := b.recent[:0]
	for _, u := range b.recent {
		if !u.at.Before(cutoff) {
			keep = append(keep, u)
		}
	}
	b.recent = keep

	b.reviseLocked(c.At)
}

func (b *Budget) averageLocked() float64 {
	if len(b.recent) == 0 {
		return 0
	}
	var sum float64
	for _, u := range b.recent {
		sum += u.ms
	}
	return sum / float64(len(b.recent))
}

// reviseLocked moves one step at a time in either direction. Degradation
// order is deliberate: the space tier is the least valuable, the request grid
// is the tool itself and gives last.
func (b *Budget) reviseLocked(now time.Time) {
	// Wait for a full window before acting, so a burst does not throttle.
	if len(b.recent) < int(budgetWindow/time.Second) {
		return
	}
	avg := b.averageLocked()

	switch {
	case avg > b.limit && b.level < maxLevel:
		b.level++
		b.quietFrom = time.Time{}
		b.msg = fmt.Sprintf("observation budget exceeded (%.0f ms/s of server CPU, limit %.0f): %s",
			avg, b.limit, degradedAt(b.level))
	case avg <= b.limit && b.level > 0:
		if b.quietFrom.IsZero() {
			b.quietFrom = now
			return
		}
		if now.Sub(b.quietFrom) >= recoveryPeriod {
			b.level--
			b.quietFrom = now
			if b.level == 0 {
				b.msg = "back inside the observation budget, full refresh rate restored"
			} else {
				b.msg = fmt.Sprintf("recovering: %s", degradedAt(b.level))
			}
		}
	default:
		b.quietFrom = time.Time{}
	}
}

func degradedAt(level int) string {
	switch level {
	case 1:
		return "space tier slowed to half rate"
	case 2:
		return "space and counter tiers slowed to half rate"
	default:
		return "all tiers slowed to half rate, including the request grid"
	}
}

// Period returns the interval a tier should currently use. On-demand work
// never asks, because it only happens when a human requested it.
func (b *Budget) Period(tier model.Tier) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	base := map[model.Tier]time.Duration{
		model.TierRequests:   b.base.Requests.Std(),
		model.TierCounters:   b.base.Counters.Std(),
		model.TierSpace:      b.base.Space.Std(),
		model.TierCPUHistory: b.base.CPUHistory.Std(),
	}[tier]

	degradedFrom := map[model.Tier]int{
		model.TierSpace:      1,
		model.TierCounters:   2,
		model.TierRequests:   3,
		model.TierCPUHistory: 1,
	}[tier]

	if b.level >= degradedFrom {
		return base * 2
	}
	return base
}

func (b *Budget) State() (usedMsPerSecond float64, level int, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.averageLocked(), b.level, b.msg
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/collector/ -v -race`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/collector/budget.go internal/collector/budget_test.go
git commit -m "Measure the observation budget in server CPU, and throttle in order

An earlier draft of the spec measured the budget as the round trip of
the collection queries, which mixed two quantities: a round trip carries
network latency, so a healthy server across a WAN would have throttled
the tool while a saturated local server slipped past. The budget is now
the tool's own cpu_time from its own session, differentiated, which is
server CPU with no network in it.

Throttling is ordered rather than proportional. The space tier gives
first, then the counters, and the request grid last because it is the
tool. Recovery is one step per thirty quiet seconds, so the rate does not
oscillate. A full ten second window is required before acting, so a burst
does not throttle anything.

Two readings are ignored rather than believed: the first, since one
cumulative sample is not a rate, and any that went backwards, which means
the connection was re-established and cpu_time restarted."
```

---

### Task 11: The tiered collector

**Files:**
- Create: `internal/collector/collector.go`, `internal/collector/collector_test.go`

**Interfaces:**
- Consumes: `source.Source`, `*window.Window`, `*Budget`, `window.Flatten`.
- Produces:
  - `collector.New(src source.Source, w *window.Window, b *Budget) *collector.Collector`
  - `(*Collector).Run(ctx context.Context) error`
  - `(*Collector).Server() model.ServerSample` returning the merged latest figures
  - `(*Collector).Status() collector.Status` with `Connected bool`, `Message string`, `Info model.ServerInfo`, `Caps model.Capabilities`

Boring concurrency, as spec 2.1 requires: one goroutine per tier, a channel to hand results back, one mutex on the merged figures. No worker pool, no scheduler of our own.

- [ ] **Step 1: Write the failing tests**

Create `internal/collector/collector_test.go`:

```go
package collector

import (
	"context"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

func TestCollectorFillsTheWindowAndFlattens(t *testing.T) {
	src := fake.New([]model.RequestSample{
		{Ref: model.RequestRef{SessionID: 52}, BlockedBy: 51},
		{Ref: model.RequestRef{SessionID: 51}},
	})
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	rows := w.Latest()
	if len(rows) != 2 {
		t.Fatalf("window holds %d rows, want 2", len(rows))
	}
	if rows[0].Ref.SessionID != 51 || rows[1].Depth != 1 {
		t.Fatalf("rows = %+v, want the blocker first and the blocked at depth 1: the collector must flatten before storing", rows)
	}
}

func TestCollectorSurvivesASourceFailure(t *testing.T) {
	src := fake.New(nil)
	src.Err = context.DeadlineExceeded
	w := window.New(time.Minute, 1000)
	tiers := baseTiers()
	tiers.Requests = config.Duration(20 * time.Millisecond)
	c := New(src, w, NewBudget(50, tiers))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Run(ctx)

	st := c.Status()
	if st.Connected {
		t.Fatal("a failing source must show as disconnected rather than presenting stale numbers as fresh")
	}
	if st.Message == "" {
		t.Fatal("the status bar must say what went wrong")
	}
}

func TestCollectorStopsOnContextCancel(t *testing.T) {
	c := New(fake.New(nil), window.New(time.Minute, 100), NewBudget(50, baseTiers()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return promptly when its context is cancelled")
	}
}
```

Add `"github.com/rudi-bruchez/sqltop/internal/config"` to that file's imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/collector/ -run TestCollector -v`
Expected: FAIL to build, `undefined: New`.

- [ ] **Step 3: Implement the collector**

Create `internal/collector/collector.go`:

```go
package collector

import (
	"context"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

type Status struct {
	Connected bool
	Message   string
	Info      model.ServerInfo
	Caps      model.Capabilities
}

type Collector struct {
	src source.Source
	win *window.Window
	bud *Budget

	mu      sync.RWMutex
	figures map[string]model.Figure
	status  Status
}

func New(src source.Source, w *window.Window, b *Budget) *Collector {
	return &Collector{src: src, win: w, bud: b, figures: map[string]model.Figure{}}
}

// Run blocks until ctx is done. One goroutine per tier, each on its own
// period, plus one that reports what the tool has cost the server.
func (c *Collector) Run(ctx context.Context) error {
	info, caps, err := c.src.Identify(ctx)
	c.mu.Lock()
	c.status = Status{Connected: err == nil, Info: info, Caps: caps}
	if err != nil {
		c.status.Message = "identify: " + err.Error()
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	tiers := []model.Tier{model.TierRequests, model.TierCounters, model.TierSpace, model.TierCPUHistory}
	for _, tier := range tiers {
		wg.Add(1)
		go func(tier model.Tier) {
			defer wg.Done()
			c.loop(ctx, tier)
		}(tier)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.costLoop(ctx)
	}()

	wg.Wait()
	return ctx.Err()
}

// loop re-reads its period every iteration, which is how a throttle decision
// reaches a tier already running.
func (c *Collector) loop(ctx context.Context, tier model.Tier) {
	for {
		start := time.Now()
		c.sample(ctx, tier)

		wait := c.bud.Period(tier) - time.Since(start)
		if wait < 0 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *Collector) sample(ctx context.Context, tier model.Tier) {
	if tier == model.TierRequests {
		rows, err := c.src.SampleRequests(ctx)
		if err != nil {
			c.fail("sampling requests: " + err.Error())
			return
		}
		// The tick is stamped with the sample's own time, not the
		// collector's. Two clocks would drift apart under load, and the one
		// that matters for age eviction is the one the rows carry.
		at := time.Now()
		if len(rows) > 0 && !rows[0].At.IsZero() {
			at = rows[0].At
		}
		// Flattening is engine-neutral, so it happens here rather than in
		// any source. Spec section 4.
		c.win.Append(at, window.Flatten(rows))
		c.ok()
		return
	}

	sample, err := c.src.SampleServer(ctx, tier)
	if err != nil {
		c.fail("sampling " + tier.String() + ": " + err.Error())
		return
	}
	c.mu.Lock()
	for k, v := range sample.Figures {
		c.figures[k] = v
	}
	c.mu.Unlock()
	c.ok()
}

func (c *Collector) costLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cost, err := c.src.Cost(ctx)
			if err != nil {
				// Never swallowed: without this reading the budget stops
				// updating and the throttle stops reacting, which must be
				// visible rather than silent.
				c.fail("reading own cost: " + err.Error())
				continue
			}
			c.bud.Observe(cost)
		}
	}
}

func (c *Collector) ok() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = true
	if used, level, msg := c.bud.State(); level > 0 {
		c.status.Message = msg
		_ = used
	} else {
		c.status.Message = ""
	}
}

func (c *Collector) fail(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = false
	c.status.Message = msg
}

func (c *Collector) Server() model.ServerSample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := model.ServerSample{At: time.Now(), Figures: make(map[string]model.Figure, len(c.figures))}
	for k, v := range c.figures {
		out.Figures[k] = v
	}
	return out
}

func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/collector/ -v -race`
Expected: PASS, nine tests, no race warnings.

- [ ] **Step 5: Commit**

```bash
git add internal/collector/collector.go internal/collector/collector_test.go
git commit -m "Add the tiered collector

Boring concurrency, as the implementation principles require: one
goroutine per tier, each re-reading its period every iteration so a
throttle decision reaches a tier already running, plus one goroutine that
reports what the tool has cost the server. One mutex on the merged
figures. No worker pool and no scheduler of our own; the tool waits on
the network, not on CPU.

Blocking chains are flattened here rather than in any source, because
that work is engine-neutral.

A failing source shows as disconnected with the reason in the status
bar. It never keeps presenting stale numbers as if they were fresh."
```

---

### Task 12: The wire protocol

**Files:**
- Create: `internal/web/protocol.go`, `internal/web/protocol_test.go`

**Interfaces:**
- Consumes: `model.RequestSample`, `collector.Status`.
- Produces:
  - `web.NewEncoder() *web.Encoder`
  - `(*Encoder).Snapshot(rows []model.RequestSample, figures map[string]model.Figure, st collector.Status) web.SnapshotPayload`
  - `web.SnapshotPayload{Seq uint64, TS int64, Rows []web.Row, Refs map[string]web.Ref, Figures map[string]model.Figure, Status web.StatusPayload}`
  - `web.Row` with short JSON tags and no `sql`/`program`/`login`/`host` fields
  - `web.Ref{SQL, Program, Login, Host string}`

The bench measured 47 % of the payload as redundant from one tick to the next: the SQL text never changes for a session, nor does the program name, and the CPU history gains one point out of twenty-four. Per-session invariants therefore go into a reference table sent once, and rows carry a reference key. Spec section 4.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/protocol_test.go`:

```go
package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

func rows(n int) []model.RequestSample {
	long := strings.Repeat("SELECT c.customer_id, SUM(l.net_amount) FROM dbo.SalesOrder c ", 4)
	out := make([]model.RequestSample, n)
	for i := range out {
		out[i] = model.RequestSample{
			Ref:     model.RequestRef{SessionID: int64(51 + i)},
			SQLText: long,
			Program: ".Net SqlClient Data Provider",
			Login:   "app_web",
			Host:    "WEB01",
			CPUMs:   int64(i * 10),
		}
	}
	return out
}

func TestSnapshotSendsInvariantsOnceThenReferences(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	first := e.Snapshot(rows(50), nil, st)
	if len(first.Refs) != 50 {
		t.Fatalf("first snapshot carried %d refs, want 50", len(first.Refs))
	}

	second := e.Snapshot(rows(50), nil, st)
	if len(second.Refs) != 0 {
		t.Fatalf("second snapshot carried %d refs, want 0: a session's SQL text and program name never change, so they travel once", len(second.Refs))
	}
	if len(second.Rows) != 50 {
		t.Fatal("rows must still all be present")
	}
	for _, r := range second.Rows {
		if r.RefKey == "" {
			t.Fatal("every row must point at its reference entry")
		}
	}
}

func TestReferenceTableCutsThePayloadSubstantially(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	first, _ := json.Marshal(e.Snapshot(rows(300), nil, st))
	second, _ := json.Marshal(e.Snapshot(rows(300), nil, st))

	saved := 1 - float64(len(second))/float64(len(first))
	if saved < 0.40 {
		t.Fatalf("steady-state payload is only %.0f%% smaller than the first; the bench measured 47%% of the payload as redundant, so something is still being resent", saved*100)
	}
	t.Logf("first %d bytes, steady state %d bytes, %.0f%% smaller", len(first), len(second), saved*100)
}

func TestReferencesAreDroppedWhenTheSessionGoes(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	e.Snapshot(rows(10), nil, st)
	e.Snapshot(rows(2), nil, st)
	// The eight departed sessions must not be remembered forever, or the
	// encoder leaks on a server that churns connections.
	if n := e.known(); n != 2 {
		t.Fatalf("encoder remembers %d sessions, want 2", n)
	}
}

func TestReturningSessionGetsItsReferenceAgain(t *testing.T) {
	e := NewEncoder()
	st := collector.Status{Connected: true}

	e.Snapshot(rows(3), nil, st)
	e.Snapshot(nil, nil, st)
	back := e.Snapshot(rows(3), nil, st)

	if len(back.Refs) != 3 {
		t.Fatalf("got %d refs, want 3: a session that left and came back must be described again, or the grid shows blank text", len(back.Refs))
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -v`
Expected: FAIL to build, `undefined: NewEncoder`.

- [ ] **Step 3: Implement the encoder**

Create `internal/web/protocol.go`:

```go
// Package web serves the interface and pushes samples to it.
package web

import (
	"hash/fnv"
	"strconv"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

// Row is one grid line on the wire. It deliberately lacks the SQL text, the
// program name, the login and the host: those never change for a session, so
// they travel once in Refs. The bench measured them as 47 % of the payload,
// resent every second for nothing.
type Row struct {
	SPID      int64   `json:"spid"`
	RefKey    string  `json:"ref"`
	Status    string  `json:"st"`
	Database  string  `json:"db"`
	Command   string  `json:"cmd"`
	BlockedBy int64   `json:"by"`
	Depth     int     `json:"d"`
	ElapsedMs int64   `json:"el"`
	CPUMs     int64   `json:"cpu"`
	Reads     int64   `json:"rd"`
	Writes    int64   `json:"wr"`
	TempdbMB  float64 `json:"tdb"`
	GrantMB   float64 `json:"gr"`
	DOP       int     `json:"dop"`
	WaitType  string  `json:"w"`
	WaitMs    int64   `json:"wms"`
	Percent   float64 `json:"pct"`
}

// Ref holds what stays constant for one session.
type Ref struct {
	SQL     string `json:"sql"`
	Program string `json:"prg"`
	Login   string `json:"login"`
	Host    string `json:"host"`
}

type StatusPayload struct {
	Sqltop    string `json:"sqltop"`
	Connected bool   `json:"connected"`
	Message   string `json:"message,omitempty"`
	Instance  string `json:"instance"`
	Version   string `json:"version"`
}

type SnapshotPayload struct {
	Seq     uint64                  `json:"seq"`
	TS      int64                   `json:"ts"`
	Rows    []Row                   `json:"rows"`
	Refs    map[string]Ref          `json:"refs,omitempty"`
	Figures map[string]model.Figure `json:"figures,omitempty"`
	Status  StatusPayload           `json:"status"`
}

// Encoder remembers which references a client already holds. One encoder per
// connected client, since two clients may have joined at different times.
type Encoder struct {
	seq  uint64
	sent map[string]string // ref key -> a fingerprint of what was sent
}

func NewEncoder() *Encoder { return &Encoder{sent: map[string]string{}} }

func (e *Encoder) known() int { return len(e.sent) }

// refKey identifies a session's invariants. It includes the SQL text, so a
// session that starts a different statement gets a new reference rather than
// the grid showing the previous query under a new one.
func refKey(r model.RequestSample) string {
	return strconv.FormatInt(r.Ref.SessionID, 10) + ":" + fingerprint(r)
}

func fingerprint(r model.RequestSample) string {
	if r.QueryHash != "" {
		return r.QueryHash
	}
	// No query hash, for instance a request with no cached plan. Hash the
	// text. An earlier draft used its length, which collides: two sessions
	// running different statements of the same length would have shared a
	// reference key, and the second would have shown the first one's SQL.
	// FNV rather than a cryptographic hash because nothing here is a secret.
	h := fnv.New64a()
	h.Write([]byte(r.SQLText))
	return strconv.FormatUint(h.Sum64(), 16)
}

// maxRefSQL caps what a reference carries. A megabyte batch would otherwise
// sit in the reference table and cross the wire once at full size.
const maxRefSQL = 64 * 1024

func clip(s string) string {
	if len(s) <= maxRefSQL {
		return s
	}
	return s[:maxRefSQL] + "\n-- truncated by sqltop"
}

func (e *Encoder) Snapshot(rows []model.RequestSample, figures map[string]model.Figure, st collector.Status) SnapshotPayload {
	e.seq++
	out := SnapshotPayload{
		Seq:     e.seq,
		Rows:    make([]Row, 0, len(rows)),
		Figures: figures,
		Status: StatusPayload{
			Sqltop:    buildinfo.String(),
			Connected: st.Connected,
			Message:   st.Message,
			Instance:  st.Info.Instance,
			Version:   st.Info.ProductVersion,
		},
	}

	alive := make(map[string]string, len(rows))
	for _, r := range rows {
		key := refKey(r)
		alive[key] = key

		if _, had := e.sent[key]; !had {
			if out.Refs == nil {
				out.Refs = map[string]Ref{}
			}
			out.Refs[key] = Ref{SQL: clip(r.SQLText), Program: r.Program, Login: r.Login, Host: r.Host}
			e.sent[key] = key
		}

		out.Rows = append(out.Rows, Row{
			SPID: r.Ref.SessionID, RefKey: key, Status: r.Status, Database: r.Database,
			Command: r.Command, BlockedBy: r.BlockedBy, Depth: r.Depth,
			ElapsedMs: r.ElapsedMs, CPUMs: r.CPUMs, Reads: r.LogicalReads, Writes: r.Writes,
			TempdbMB: r.TempdbMB, GrantMB: r.MemoryGrantMB, DOP: r.DOP,
			WaitType: r.WaitType, WaitMs: r.WaitMs, Percent: r.PercentComplete,
		})
	}

	// Forget references nobody is using, or a server that churns connections
	// makes this map grow forever.
	for k := range e.sent {
		if _, still := alive[k]; !still {
			delete(e.sent, k)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS, four tests. `TestReferenceTableCutsThePayloadSubstantially` logs the two sizes; record the percentage in the commit message.

- [ ] **Step 5: Commit**

```bash
git add internal/web/protocol.go internal/web/protocol_test.go
git commit -m "Add the wire protocol with a per-session reference table

The bench measured 47 % of the payload as redundant from one tick to the
next: for a given session the SQL text never changes, nor does the
program name, the login or the host. They now travel once in a reference
table and rows carry a key.

Two details that would bite otherwise. The reference key includes a
fingerprint of the statement, so a session that moves on to a different
query gets a new reference rather than the grid showing the previous
query under a new row. And references for sessions that have gone are
dropped each tick, or the encoder would grow forever against a server
that churns connections."
```

---

### Task 13: The loopback HTTP server

**Files:**
- Create: `internal/web/server.go`, `internal/web/server_test.go`

**Interfaces:**
- Consumes: `*collector.Collector`, `*window.Window`, `config.Server`.
- Produces:
  - `web.NewServer(c *collector.Collector, w *window.Window, cfg config.Server) (*web.Server, error)`
  - `(*Server).URL() string` including the token
  - `(*Server).Serve(ctx context.Context) error`
  - `(*Server).Handler() http.Handler` for tests

Spec 4.3: binds `127.0.0.1` only, with no flag to widen it, because this interface will eventually be able to kill sessions on a production server. A per-run token keeps other local users of a shared machine out.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/server_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	w := window.New(time.Minute, 1000)
	w.Append(time.Now(), []model.RequestSample{{Ref: model.RequestRef{SessionID: 51}, SQLText: "SELECT 1"}})
	c := collector.New(fake.New(nil), w, collector.NewBudget(50, config.Default().Tiers))

	s, err := NewServer(c, w, config.Server{Port: 0}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenIsRequired(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a token", rec.Code)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?t=wrong", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a wrong token", rec.Code)
	}
}

func TestCorrectTokenIsAccepted(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?t="+s.token, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the run's token", rec.Code)
	}
}

func TestURLIsLoopbackAndCarriesTheToken(t *testing.T) {
	s := newTestServer(t)
	u := s.URL()

	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatalf("URL = %q, want a loopback address: this interface will be able to kill sessions", u)
	}
	if !strings.Contains(u, s.token) {
		t.Fatalf("URL = %q, want the run's token in it", u)
	}
}

func TestListenerIsBoundToLoopback(t *testing.T) {
	s := newTestServer(t)
	if addr := s.listener.Addr().String(); !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("bound to %q, want 127.0.0.1 only", addr)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run "Token|URL|Listener" -v`
Expected: FAIL to build, `undefined: NewServer`.

- [ ] **Step 3: Implement the server**

Create `internal/web/server.go`:

```go
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

//go:embed assets
var assetsFS embed.FS

type Server struct {
	col      *collector.Collector
	win      *window.Window
	token    string
	listener net.Listener
	// push is the stream period. It follows the requests tier, so changing
	// that in the configuration file changes what the browser receives too.
	push time.Duration
}

// NewServer binds 127.0.0.1 and nothing else. There is deliberately no option
// to widen it: this interface can eventually kill sessions on a production
// server, and a bind on all interfaces would hand that to the network.
func NewServer(c *collector.Collector, w *window.Window, cfg config.Server, push time.Duration) (*Server, error) {
	if push <= 0 {
		push = time.Second
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("web: listen: %w", err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		ln.Close()
		return nil, fmt.Errorf("web: token: %w", err)
	}
	return &Server{col: c, win: w, token: hex.EncodeToString(raw[:]), listener: ln, push: push}, nil
}

func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?t=%s", s.listener.Addr().String(), s.token)
}

func (s *Server) Handler() http.Handler {
	content, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err) // an embed that does not resolve is a build-time mistake
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(content)))
	mux.HandleFunc("/api/status", func(rw http.ResponseWriter, req *http.Request) {
		st := s.col.Status()
		oldest, samples, capped := s.win.Depth()
		writeJSON(rw, map[string]any{
			"sqltop":    buildinfo.String(),
			"connected": st.Connected,
			"message":   st.Message,
			"instance":  st.Info.Instance,
			"version":   st.Info.ProductVersion,
			"window": map[string]any{
				"oldest": oldest, "samples": samples, "capped": capped,
			},
		})
	})
	mux.HandleFunc("/api/stream", s.stream)

	return s.authenticate(mux)
}

// authenticate accepts the token from the query string, where the opened URL
// puts it, or from a header, which the page uses afterwards. Compared in
// constant time out of habit rather than necessity.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		got := req.URL.Query().Get("t")
		if got == "" {
			got = req.Header.Get("X-Sqltop-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, req)
	})
}

func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	if err := srv.Serve(s.listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(v)
}
```

Create a placeholder `internal/web/assets/index.html` so the embed resolves; task 14 replaces it:

```html
<!doctype html>
<title>sqltop</title>
<p>placeholder, replaced in task 14</p>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS, nine tests. `/api/stream` is registered but empty until task 14.

- [ ] **Step 5: Commit**

```bash
git add internal/web/server.go internal/web/server_test.go internal/web/assets
git commit -m "Add the loopback HTTP server with a per-run token

It binds 127.0.0.1 and nothing else, and there is deliberately no option
to widen it. The reason is not tidiness: this interface will be able to
kill sessions on a production server, and a bind on all interfaces would
hand that to anyone on the network.

The token keeps other local users of a shared machine out. It is
generated per run, carried in the URL the tool opens, and accepted from a
header afterwards."
```

---

### Task 14: The SSE stream and the real grid

**Files:**
- Create: `internal/web/stream.go`, `internal/web/stream_test.go`
- Create: `internal/web/assets/index.html` (replaces the placeholder), `internal/web/assets/app.js`, `internal/web/assets/style.css`
- Modify: `cmd/sqltop/main.go`

**Interfaces:**
- Consumes: `*Encoder`, `*collector.Collector`, `*window.Window`.
- Produces: `GET /api/stream` emitting `event: snapshot` at the requests period, and a working grid.

The renderer is the one the bench settled on: a pool of recycled `<tr>` covering the visible window, spacers holding the scroll height, and only changed cells rewritten. Copy the mechanism from `bench/web/app.js`, drop the four-mode switcher and the instrumentation, and add the reference-table lookup. `bench/` stays untouched: it is the non-regression harness and must keep building.

- [ ] **Step 1: Write the failing stream test**

Create `internal/web/stream_test.go`:

```go
package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamPushesSnapshots(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream?t="+s.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", ct)
	}

	var sawEvent, sawData bool
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && !(sawEvent && sawData) {
		line := sc.Text()
		if line == "event: snapshot" {
			sawEvent = true
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"rows"`) {
			sawData = true
		}
	}
	if !sawEvent || !sawData {
		t.Fatal("the stream must emit a snapshot event carrying rows within a few seconds")
	}
}

func TestStreamRequiresTheToken(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run TestStream -v`
Expected: FAIL, the handler returns nothing and the scanner reaches the end.

- [ ] **Step 3: Implement the stream**

Create `internal/web/stream.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"time"
)

// stream pushes a snapshot per tick. One Encoder per client, since two clients
// may have connected at different moments and hold different references.
func (s *Server) stream(rw http.ResponseWriter, req *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("X-Accel-Buffering", "no")

	enc := NewEncoder()
	send := func() bool {
		payload := enc.Snapshot(s.win.Latest(), s.col.Server().Figures, s.col.Status())
		payload.TS = time.Now().UnixMilli()
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := rw.Write([]byte("event: snapshot\ndata: " + string(b) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	t := time.NewTicker(s.push)
	defer t.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case <-t.C:
			if !send() {
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run the stream tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS, eleven tests.

- [ ] **Step 5: Build the real grid**

Replace `internal/web/assets/index.html`, and create `app.js` and `style.css`.

Copy `bench/web/style.css` verbatim except for the Tabulator rule at the end, which has no reason to exist here. Copy the structural half of `bench/web/index.html`: the header, the `plainScroll` container, its table, and the status bar. Drop the four mode radios, the load sliders, the stress controls and the stats tiles; those belong to the bench. The header needs five ids the script writes into: `build`, `dot`, `instance`, `version`, `message`, plus `rowCount` and `seq` in the status bar. `build` carries `buildinfo.String()`, because the first question about a bug report is which build produced it.

`app.js` keeps exactly the renderer the bench validated, and adds the reference lookup:

```js
// The renderer the bench settled on: a pool of recycled <tr> covering the
// visible window, two spacers holding the scroll height, and only changed
// cells rewritten. Measured at 4.8 ms per refresh over 800 rows against 46.8
// for Tabulator, with no freeze and no lost selection. See bench/README.md.
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const n0 = (v) => Math.round(v).toLocaleString("en-US");
const n2 = (v) => Number(v).toFixed(2);

const token = new URLSearchParams(location.search).get("t") || "";

// refs accumulates the per-session invariants the server sends once.
const refs = new Map();
function ref(key) {
  return refs.get(key) || { sql: "", prg: "", login: "", host: "" };
}

const COLUMNS = [
  { field: "spid", title: "spid", width: 60, html: (r) => `<span class="num">${r.spid}</span>` },
  { field: "st", title: "status", width: 90 },
  { field: "db", title: "database", width: 110 },
  { field: "login", title: "login", width: 100, html: (r) => esc(ref(r.ref).login) },
  { field: "host", title: "host", width: 95, html: (r) => esc(ref(r.ref).host) },
  { field: "prg", title: "program", width: 200, html: (r) => esc(ref(r.ref).prg) },
  { field: "cmd", title: "command", width: 110 },
  { field: "w", title: "wait type", width: 150, html: (r) => waitBadge(r.w) },
  { field: "wms", title: "wait ms", width: 85, html: (r) => `<span class="num">${n0(r.wms)}</span>` },
  { field: "el", title: "elapsed ms", width: 100, html: (r) => `<span class="num">${n0(r.el)}</span>` },
  { field: "cpu", title: "cpu ms", width: 90, html: (r) => `<span class="num">${n0(r.cpu)}</span>` },
  { field: "rd", title: "reads", width: 95, html: (r) => `<span class="num">${n0(r.rd)}</span>` },
  { field: "wr", title: "writes", width: 90, html: (r) => `<span class="num">${n0(r.wr)}</span>` },
  { field: "tdb", title: "tempdb MB", width: 100, html: (r) => `<span class="num">${n2(r.tdb)}</span>` },
  { field: "gr", title: "grant MB", width: 95, html: (r) => `<span class="num">${n2(r.gr)}</span>` },
  { field: "dop", title: "dop", width: 55, html: (r) => `<span class="num">${r.dop}</span>` },
  { field: "by", title: "blocked by", width: 95, html: (r) => (r.by ? `<span class="blocked">${r.by}</span>` : "") },
  { field: "sql", title: "SQL text", width: 520, html: (r) => `<span class="sqlcell" style="padding-left:${r.d * 14}px">${esc(ref(r.ref).sql)}</span>` },
];

function waitBadge(w) {
  if (!w) return "";
  let cls = "";
  if (w.startsWith("LCK_")) cls = " lck";
  else if (w.startsWith("PAGEIOLATCH") || w === "WRITELOG" || w === "ASYNC_NETWORK_IO") cls = " io";
  else if (w === "CXPACKET") cls = " cx";
  return `<span class="badge${cls}">${esc(w)}</span>`;
}

function cellHTML(col, row) {
  return col.html ? col.html(row) : esc(row[col.field] ?? "");
}

const ROW_H = 22;
const OVERSCAN = 8;
let data = [];
let selectedSpid = null;
const pool = [];
let spacerTop = null, spacerBottom = null;

function head() {
  $("gridHead").innerHTML = COLUMNS.map((c) => `<th style="min-width:${c.width}px">${esc(c.title)}</th>`).join("");
}

function ensureSpacers() {
  const body = $("gridBody");
  if (!spacerTop) {
    spacerTop = document.createElement("tr");
    spacerTop.appendChild(document.createElement("td")).colSpan = COLUMNS.length;
    body.appendChild(spacerTop);
  }
  if (!spacerBottom) {
    spacerBottom = document.createElement("tr");
    spacerBottom.appendChild(document.createElement("td")).colSpan = COLUMNS.length;
    body.appendChild(spacerBottom);
  }
}

function acquireRow() {
  const tr = document.createElement("tr");
  const tds = COLUMNS.map(() => tr.appendChild(document.createElement("td")));
  tr.addEventListener("click", () => {
    selectedSpid = tr._spid;
    for (const e of pool) e.tr.classList.toggle("sel", e.tr._spid === selectedSpid);
  });
  const entry = { tr, tds, spid: null, prev: {} };
  pool.push(entry);
  $("gridBody").insertBefore(tr, spacerBottom);
  return entry;
}

function layout() {
  const sc = document.querySelector(".gridScroll");
  if (!sc) return;
  ensureSpacers();

  const total = data.length;
  const first = Math.max(0, Math.floor(sc.scrollTop / ROW_H) - OVERSCAN);
  const visible = Math.ceil(sc.clientHeight / ROW_H) + OVERSCAN * 2;
  const count = Math.max(0, Math.min(total - first, visible));

  spacerTop.style.height = first * ROW_H + "px";
  spacerBottom.style.height = Math.max(0, (total - first - count) * ROW_H) + "px";

  while (pool.length < count) acquireRow();
  for (let i = count; i < pool.length; i++) pool[i].tr.hidden = true;

  for (let i = 0; i < count; i++) {
    const entry = pool[i];
    const r = data[first + i];
    entry.tr.hidden = false;
    if (entry.spid !== r.spid) { entry.spid = r.spid; entry.prev = {}; entry.tr._spid = r.spid; }
    for (let c = 0; c < COLUMNS.length; c++) {
      const col = COLUMNS[c];
      const html = cellHTML(col, r);
      if (entry.prev[col.field] !== html) {
        entry.tds[c].innerHTML = html;
        entry.prev[col.field] = html;
      }
    }
    entry.tr.classList.toggle("sel", r.spid === selectedSpid);
  }
}

function applyStatus(st, seq) {
  $("dot").classList.toggle("live", !!st.connected);
  if (st.sqltop) $("build").textContent = st.sqltop;
  $("instance").textContent = st.instance || "connecting...";
  $("version").textContent = st.version || "";
  $("message").textContent = st.message || "";
  $("rowCount").textContent = data.length + " requests";
  $("seq").textContent = "tick " + seq;
}

const es = new EventSource("/api/stream?t=" + encodeURIComponent(token));
es.addEventListener("snapshot", (e) => {
  const p = JSON.parse(e.data);
  if (p.refs) for (const [k, v] of Object.entries(p.refs)) refs.set(k, v);
  data = p.rows || [];
  layout();
  applyStatus(p.status || {}, p.seq);
});
es.addEventListener("error", () => {
  $("dot").classList.remove("live");
  $("message").textContent = "lost the connection to sqltop, retrying";
});

document.querySelector(".gridScroll").addEventListener("scroll", layout, { passive: true });
head();
```

- [ ] **Step 6: Wire main to run the whole thing**

Replace the `log.Fatal("not implemented yet")` line in `cmd/sqltop/main.go` with:

```go
	dsn := os.Getenv("SQLTOP_CONN")
	if len(cfg.Instances) > 0 && cfg.Instances[0].DSN != "" {
		dsn = os.ExpandEnv(cfg.Instances[0].DSN)
	}
	if dsn == "" {
		log.Fatal("no instance to connect to: set SQLTOP_CONN in .env, or add one to sqltop.json")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	src := mssql.New()
	if err := src.Open(ctx, dsn); err != nil {
		log.Fatal(err)
	}
	defer src.Close()

	win := window.New(cfg.Retention.Std(), cfg.Budget.MaxSamples)
	col := collector.New(src, win, collector.NewBudget(cfg.Budget.ServerCPUMsPerSecond, cfg.Tiers))
	go col.Run(ctx)

	srv, err := web.NewServer(col, win, cfg.Server, cfg.Tiers.Requests.Std())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sqltop on %s", srv.URL())
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
```

Add the imports it needs: `context`, `os/signal`, and the four internal packages.

- [ ] **Step 7: Run it against the container and see real queries**

Run:
```bash
eval "$(scripts/testdb.sh)"
SQLTOP_CONN="$SQLTOP_TEST_DSN" go run ./cmd/sqltop
```
Open the printed URL. In another terminal, generate activity:
```bash
podman exec sqltop-test /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa \
  -P "${SQLTOP_TEST_PASSWORD:-Sqltop_dev_2026!}" -C \
  -Q "WAITFOR DELAY '00:00:20'"
```
Expected: the `WAITFOR` appears within a second, its elapsed time climbs, the header shows the instance name and version, and clicking the row keeps it selected across refreshes.

- [ ] **Step 8: Confirm the bench still builds**

Run: `go build ./... && go vet ./... && gofmt -l .`
Expected: no output. `bench/` is untouched and still compiles, which is the CLAUDE.md gate.

- [ ] **Step 9: Verify the whole suite and the no-CGO constraint**

Run:
```bash
go test ./... -race
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/sqltop
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/sqltop
```
Expected: all pass.

- [ ] **Step 10: Commit**

```bash
git add internal/web cmd/sqltop
git commit -m "Stream real samples to a real grid

The renderer is the one the bench settled on, carried over unchanged: a
pool of recycled rows over the visible window, spacers holding the scroll
height, and only changed cells rewritten. It measured 4.8 ms per refresh
over 800 rows against 46.8 for Tabulator, with no freeze and no lost
selection.

The page resolves per-session invariants through the reference table
rather than receiving them every tick, and indents the SQL text by the
blocking depth the window computed.

bench/ is untouched. It keeps its synthetic generator and its four-way
mode switch, because it is the non-regression harness for exactly this
renderer and has to stay able to prove the choice again."
```

---

## Verified against a live engine before execution

An external review raised four things that could only be settled by asking a
real server. SQL Server 2022 (16.0.4265.3) in Podman, queried through
`github.com/microsoft/go-mssqldb`, which is what will actually run them.

`QUOTED_IDENTIFIER` is `ON` on a fresh driver connection, so the XML methods in
`cpuHistoryQuery` work. The same query pasted into `sqlcmd -Q` fails with error
1934, which is a `sqlcmd` default and not a property of the query; testing
through the wrong client would have sent this implementation chasing a
namespace problem it does not have.

`HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE')` returns 1 and is the check
task 7 now uses. Counting sessions, which the first draft did, returns 82 on an
idle container because an instance always carries dozens of system sessions, so
it would have worked; it simply answers a different question.

`RING_BUFFER_SCHEDULER_MONITOR` exists as a type but held zero records at one
minute of uptime, because the engine writes one a minute. The CPU history is
therefore legitimately empty on a freshly started server, which is why
`readCPUHistory` treats an absent row as an unavailable figure rather than an
error, and why the integration test asserts the key is present rather than that
it carries a value.

The buffer cache hit ratio counter and its base both read 76 on that instance,
which is the lifetime ratio of 100 % that makes the raw counter useless and the
windowed delta of task 5 necessary.

## Coverage gaps, listed rather than discovered later

These are spec requirements this plan does not implement. They are not
oversights; they are the UI plan's, and naming them here is what stops an
executor from either inventing them or assuming they exist.

Scheduler load (spec 6). `CapSchedulerLoad` is probed in task 7 but no figure
is produced. The capability is deliberately ahead of its consumer: the probe is
one line and knowing the answer at connection time is worth more than the
symmetry.

Memory clerks and plan cache breakdown (spec 6). Task 9 produces committed and
target server memory only. The clerk-level split is dashboard work.

Isolation level is now collected by task 8 but nothing displays it, since the
grid here carries a reduced column set.

Everything else absent is listed in the Self-Review below.

## Self-Review

**Spec coverage.** Section 2 constraints are enforced by the build and vet steps in tasks 1, 7 and 14. Section 2.1 shapes the dependency budget (task 7 is the only `go get`) and the test strategy throughout. Section 3 lands in task 7's preflight, including the version floor and the probe-rather-than-infer rule. Section 3.3 is verified by the cross-compilation step in task 7. Section 4 is tasks 3, 4, 6, 11 and 12. Section 4.1 is tasks 2 and 6. Section 4.3 is task 13. Section 6 is tasks 5 and 9. Section 8.3 is task 1. Section 10 is tasks 10 and 11.

Deliberately absent, and listed here so the gaps are visible rather than forgotten: section 4.2's instance switcher and frozen windows, section 4.4's reconnection with backoff, section 7's views beyond the request grid, section 8.1's full column set and its filters, section 8.2's saved layouts, section 9's plan panel and live progress rendering, and section 9.1's kill flow. `Plan` and `QueryText` exist on the interface from task 6 and the fake implements them, but no SQL Server implementation and no UI ships here. These are the UI plan.

**Types.** `model.RequestSample` is written by task 8, given `Depth` by task 4, stored by task 3, and read by task 12. `model.Cost` is produced by task 7 and consumed by task 10. `model.Figure` is produced by tasks 5 and 9 and consumed by tasks 11 and 12. `config.Tiers` is produced by task 1 and consumed by tasks 10 and 11. `collector.Status` is produced by task 11 and consumed by tasks 12 and 13. The `Source` interface in task 6 is implemented by task 7 for `Open`, `Close`, `Identify` and `Cost`, task 8 for `SampleRequests`, and task 9 for `SampleServer`; `QueryText` and `Plan` are stubbed in task 7 returning `errNotInThisPlan`, so `mssql.Source` satisfies the interface from that task onward and `TestSatisfiesSource` compiles; the stubs for `SampleRequests` and `SampleServer` are deleted by tasks 8 and 9 respectively.

**Review findings not applied, with reasons.** Two of the external review's points do not hold. The XML namespace concern in `cpuHistoryQuery` is unfounded through the driver, as the verification section records. And the claim that the spec has no section 9.1 is wrong: `docs/SPECS.md` line 510 carries it, so the reference to the kill flow stands.

**Known risk carried forward.** The 16 ms rendering budget was verified on a passive grid, and the reference-table lookup added in task 14 puts one extra map read per cell in the path. Sorting and filtering are not in this plan, so the number still holds for what ships here, but it must be re-measured with the bench when they arrive.

## Execution Handoff

Plan complete and saved to `docs/plans/2026-08-29-collector.md`. Two execution options:

**1. Subagent-Driven (recommended)** - a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** - execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
