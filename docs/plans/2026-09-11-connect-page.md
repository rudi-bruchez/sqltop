# Connect page implementation plan

> For agentic workers: REQUIRED SUB-SKILL: use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

Goal: when sqltop starts with no connection string, serve a form on the usual address that builds one, connect with it, hand the same socket and token over to the monitor, and optionally save the string to `.env`.

Architecture: `internal/dotenv` gains `Set`, sharing its line parser with `Load`. `internal/source/mssql` gains `connstr.go` (the form's fields, `BuildDSN`, the methods this platform offers, redaction) and `hint.go` (one line of advice per known driver failure). `internal/web` splits the listening socket and token out of `NewServer` into a `Listener`, and gains `connect.go`: a second, small HTTP server that serves the connect page on that listener until one attempt succeeds, then stops through the existing `gracefulShutdown` without closing the socket. `cmd/sqltop` supplies the attempt function and continues with the source it opened.

Tech stack: Go 1.27, standard library plus the existing `github.com/microsoft/go-mssqldb` v1.11.0 (its `msdsn` and `integratedauth` packages in tests only). No new dependency. The page is plain HTML and JavaScript composed inline like the monitor page.

Spec: `docs/specs/2026-09-11-connect-page-design.md`. Read it before Task 1. `docs/SPECS.md` is the authority it argues from. Section numbers below ("spec 3.2") refer to the design document.

## Global Constraints

- Pure Go, no CGO. No new module in `go.mod`.
- The server binds `127.0.0.1` only; `config.Server.Port` is validated to 1 to 65535.
- Nothing is sent to the monitored server before a connection attempt. A failed attempt leaves nothing behind.
- Secrets never reach `sqltop.yaml`, the log, or any response. The only file the connect page may write is `.env`, and only when its checkbox is ticked.
- Kerberos is not on the page (spec section 2). Methods are exactly `sql`, `windows` (Windows builds only) and `domain`.
- Every route of both phases goes through the token check, the `Host` check and the security headers.
- `gofmt -l .` prints nothing, `go vet ./...` is clean, and `deno lint internal/web/assets/app.js internal/web/assets/connect.js` is clean before every commit.
- Commits carry no attribution trailer of any kind. The message is prose explaining why, with no bold and no em dash.
- Comments say why, in as few words as it can be said. No archaeology, no task numbers, no review history.
- English everywhere: code, comments, UI text, documentation.

## How to run the tests in this plan

Shell state does not survive between tool calls, so every test run that needs the container sets it up in the same invocation:

```bash
eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql -run '<filter>' -count=1 -v
```

Each step gives the `-run` filter and the number of tests it must report. A smaller number means the filter is wrong, not that the code is right. A test that reports `SKIP` for want of `SQLTOP_TEST_DSN` has not run; with the container up it must say `PASS`.

Every task has a step that breaks the code on purpose and watches the test fail. Reporting that two of three breakages were caught is the success of that step, not a failure: say which one was not caught and why. If a measurement contradicts this plan, stop and say so; do not bend the code to match the plan.

## Names in the existing code, verified

Read out of the tree at commit `2fd364e`, not recalled.

| What you need | What it is actually called |
|---|---|
| web test server | `newTestServer(t *testing.T) *Server` in `internal/web/server_test.go` |
| a request from the page itself | `loopbackRequest(method, target string) *http.Request` in `server_test.go` |
| fast tiers for tests | `testTiers() config.Tiers` in `server_test.go` |
| fake source | `fake.New(rows []model.RequestSample) *fake.Source`; `fake.New(nil)` for none |
| window and collector in tests | `window.New(time.Minute, 1000)`, `w.Append(time.Now(), []model.RequestSample{...})`, `collector.New(src, w, collector.NewBudget(50, testTiers()))` |
| server fields | `s.token string`, `s.listener net.Listener` |
| shutdown | `gracefulShutdown(srv *http.Server)`, grace `var shutdownGrace = 2 * time.Second` (a test may shrink it) |
| page composition | `composePage() ([]byte, error)`, memoised as `var page = sync.OnceValues(composePage)` |
| JSON response | `writeJSON(rw http.ResponseWriter, v any)`, status 200 only; `viewError` writes 503 |
| routes | `type route struct { path string; handler http.Handler }`, `(s *Server) routes() ([]route, error)` |
| token check | method `(s *Server) authenticate(next http.Handler) http.Handler`, called once, in `Handler` |
| guard on the token check | `TestConstantTimeCompareIsUsedForTheToken` reads `server.go` for `"crypto/subtle"` and `subtle.ConstantTimeCompare(`; the compare must stay in `server.go` |
| other guards | `hostAllowed(host string) bool`, `securityHeaders(next http.Handler) http.Handler` |
| linter gate | `TestShippedJavaScriptPassesTheLinter` in `internal/web/app_assets_test.go`, runs `deno lint assets/app.js` |
| browser helpers | `lookChromium() string`, `findDeno() string`, `devToolsPort(profile string, wait time.Duration) (string, error)`, `lastJSONLine(s string) string` |
| browser driver pattern | `internal/web/testdata/e2e-driver.js`, run as `deno run --quiet --allow-net=127.0.0.1 <driver> <pageURL> <cdpPort>` |
| browser opening | `web.OpenBrowser(url string) (string, error)` |
| the source | `mssql.New() *Source`, `(s *Source) Open(ctx, dsn string) error`, `AllowCapture(ok bool)`, `Close() error` |
| app name on the DSN | `withAppName(dsn string) string` in `mssql.go`, applied by `Open` |
| container DSN in tests | `SQLTOP_TEST_DSN`; tests skip when unset and fail when `SQLTOP_REQUIRE_DB` is set too |
| dotenv | `dotenv.Load(path string) ([]string, error)` |
| driver DSN parser | `msdsn.Parse(dsn string) (msdsn.Config, error)`: `Host`, `Port uint64` (0 when absent), `Instance`, `User`, `Password`, `Database`, `Encryption` (`msdsn.EncryptionOff`, `EncryptionRequired`, `EncryptionStrict`), and trust is `TLSConfig.InsecureSkipVerify` (there is no trust field) |
| integrated auth selection | `integratedauth.GetIntegratedAuthenticator(cfg msdsn.Config)`: returns `nil, nil` for a user without a backslash outside Windows (falls back to SQL authentication), the `ntlm` authenticator for `DOMAIN\user` |

Measured facts this plan relies on, each run against the container or the driver at `2fd364e`:

- `msdsn.Parse` keeps an unbracketed IPv6 host without a port (`sqlserver://sa:x@::1` gives host `::1`); with a port, `net.JoinHostPort` brackets it and the parse strips the brackets. A bracketed host without a port would reach the dialer with its brackets and fail, so the brackets appear only with a port.
- With no `encrypt` parameter, `msdsn.Parse` gives `EncryptionOff` and `TLSConfig.InsecureSkipVerify == true`.
- `encrypt=true` without trust against the container fails with `mssql: connect: TLS Handshake failed: tls: failed to verify certificate: x509: cannot validate certificate for 127.0.0.1 because it doesn't contain any IP SANs`. That message contains both `TLS Handshake failed` and `x509:`, so the hint table is read in order and `x509:` comes first.
- `127.0.0.1\NOPE` without a port fails at once with `mssql: connect: no instance matching 'NOPE' returned from host '127.0.0.1'`.

---

### Task 1: `dotenv.Set`, sharing one line parser with `Load`

Spec section 7. The connect page reaches `.env` precisely when the file defines `SQLTOP_CONN` as empty (`.env.example` ships it that way), so `Set` must replace every line `Load` would read as that definition, spacing and `export` included.

Files:
- Modify: `internal/dotenv/dotenv.go`
- Modify: `internal/dotenv/dotenv_test.go`

Interfaces:
- Consumes: nothing.
- Produces: `dotenv.Set(path, key, value string) error`.

- [ ] Step 1: Write the failing tests

Append to `internal/dotenv/dotenv_test.go`, adding `"runtime"` to its imports:

```go
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
```

- [ ] Step 2: Run the tests to watch them fail

Run: `go test ./internal/dotenv -run 'TestSet' -count=1`
Expected: a compile failure, `undefined: Set`.

- [ ] Step 3: Implement

Five edits to `internal/dotenv/dotenv.go`.

First, in `Load`'s doc comment, the sentence that quotes a startup message Task 7 removes, and tells the history of a review, becomes a statement that stays true. Replace

```
// never stops the tool; but skipping it in silence, which is what this used
// to do, turns a colon typed instead of an equals sign into "no instance to
// connect to" at startup with nothing to connect the two. Reported after an
// external reviewer typed exactly that typo and watched the error say
// nothing useful.
```

with

```
// never stops the tool; but skipping it in silence turns a colon typed
// instead of an equals sign into a missing connection string, with nothing
// at startup to connect the two.
```

Second, the package comment's first line becomes `// Package dotenv reads KEY=VALUE pairs from a file into the environment,` followed by a new line `// and writes one back.`, and the imports become:

```go
import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)
```

Third, add `entry` and `read` above `Load`. Fourth, replace `Load`'s function body (not its comment) with the one below, which goes through `read` and otherwise behaves exactly as before. Fifth, add `Set` after `Load`.

```go
// entry is one line of the file as Load reads it.
type entry struct {
	line       string // trimmed and without its export prefix, for warnings
	key, value string
	ok         bool // false when the line has no "="
}

// read parses one raw line, or returns nil for a blank line or a comment.
// Load and Set both go through it, so the line Set replaces is exactly the
// line Load would have read as the definition.
func read(raw string) *entry {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, ok := strings.Cut(line, "=")
	return &entry{line: line, key: strings.TrimSpace(key), value: strings.TrimSpace(value), ok: ok}
}

func Load(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var warnings []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		e := read(sc.Text())
		if e == nil {
			continue
		}
		if !e.ok {
			warnings = append(warnings, fmt.Sprintf("%s line %d: no = in %q, ignored", path, n, e.line))
			continue
		}
		key, value := e.key, e.value
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			warnings = append(warnings, fmt.Sprintf("%s line %d: empty name before =, ignored", path, n))
			continue
		}
		if _, taken := os.LookupEnv(key); !taken {
			if err := os.Setenv(key, value); err != nil {
				return warnings, err
			}
		}
	}
	return warnings, sc.Err()
}

// Set makes key=value the definition of key in the file at path. The first
// line Load would read as that definition is replaced and any later one
// dropped; every other line is kept byte for byte. A missing file is created
// with mode 0600, an existing one keeps its mode.
func Set(path, key, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("dotenv: the value for %s contains a line break", key)
	}
	// Through a link, the file to rewrite is its target: renaming over the
	// link would replace it with a copy and leave the target stale.
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	mode := fs.FileMode(0o600)
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = fi.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	def := key + "=" + value
	var b strings.Builder
	done := false
	for _, raw := range strings.SplitAfter(string(old), "\n") {
		if raw == "" {
			continue // SplitAfter's empty last element after a final newline
		}
		body := strings.TrimRight(raw, "\r\n")
		if e := read(body); e != nil && e.ok && e.key == key {
			if !done {
				b.WriteString(def + raw[len(body):])
				done = true
			}
			continue
		}
		b.WriteString(raw)
	}
	if !done {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(def + "\n")
	}

	// Through a temporary file, so a crash never leaves half a .env. Rename
	// installs the temporary file's mode, hence the Chmod.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
```

The deferred `os.Remove` is a no-op once the rename has succeeded, since the temporary name no longer exists; it only cleans up after a failure.

- [ ] Step 4: Run the tests to watch them pass

Run: `go test ./internal/dotenv -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `ok`. `go test ./internal/dotenv -run 'TestSet' -count=1 -v | grep -c '^--- PASS: TestSet'` prints `6` on Linux; the existing `Load` tests pass unchanged.

- [ ] Step 5: Break it and watch the right tests fail

Each breakage below is written so that it still compiles: a compile error is not the test failing. Do them one at a time, run `go test ./internal/dotenv -count=1`, write down which test failed, then restore the file from a copy you made first (never `git checkout`, `git restore` or `git stash`):

1. In `Set`, replace `e != nil && e.ok && e.key == key` with `e != nil && strings.HasPrefix(body, key+"=")`. Expected: the `export`, `spaces around the equals sign` and `leading spaces` subtests fail.
2. Replace `tmp.Chmod(mode)` with `tmp.Chmod(mode &^ 0o077)`. Expected: `TestSetKeepsTheModeOfAnExistingFile` fails with mode 0600.
3. Delete the `strings.ContainsAny` check. Expected: `TestSetRefusesALineBreakAndLeavesTheFileAlone` fails.
4. Delete the `filepath.EvalSymlinks` block. Expected: `TestSetWritesThroughASymlink` fails on the replaced link.

- [ ] Step 6: Gates and commit

```bash
gofmt -l . ; go vet ./... && go test ./internal/dotenv -count=1
git add internal/dotenv/dotenv.go internal/dotenv/dotenv_test.go
git commit -F - <<'EOF'
Let dotenv write a definition back as Load would read it

The connect page saves the connection string it built, and it reaches that
point precisely when .env defines SQLTOP_CONN as empty, which is how
.env.example ships. A replace that matched only "KEY=" at the start of a
line would miss "SQLTOP_CONN = " or an export prefix, append after it, and
Load would go on reading the empty first definition forever. Set and Load
now share the one function that decides what a definition is.

The write goes through a temporary file and a rename so a crash cannot
leave half a file, and the original mode is copied over explicitly because
rename installs the temporary file's own.
EOF
```

---

### Task 2: The connection string from the form

Spec sections 4.1, 4.2, 4.3 and 5.

Files:
- Create: `internal/source/mssql/connstr.go`
- Create: `internal/source/mssql/connstr_test.go`

Interfaces:
- Consumes: nothing.
- Produces:
  - `type ConnParams struct { Server, Auth, Login, Password, Database, Encrypt string; Trust bool }` with JSON tags `server`, `auth`, `login`, `password`, `database`, `encrypt`, `trust`.
  - `func BuildDSN(p ConnParams) (string, error)`
  - `type AuthMethod struct { ID string; Label string; Fields []string; Note string }` with JSON tags `id`, `label`, `fields`, `note,omitempty`.
  - `func AuthMethods() []AuthMethod`
  - `func Redacted(dsn string) string`

- [ ] Step 1: Write the failing tests

Create `internal/source/mssql/connstr_test.go`:

```go
package mssql

import (
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/go-mssqldb/integratedauth"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// TestBuildDSNIsReadBackByTheDriver checks what the driver understands, not
// the string: each result goes through withAppName, as Open sends it, and
// then through go-mssqldb's own parser.
func TestBuildDSNIsReadBackByTheDriver(t *testing.T) {
	const hostile = "p@ss:w/o#r%d ?&=é"
	sql := func(server string) ConnParams {
		return ConnParams{Server: server, Auth: "sql", Login: "sa", Password: "x"}
	}
	type want struct {
		host, instance, user, password, database string
		port                                     uint64
		enc                                      msdsn.Encryption
		trust                                    bool
	}
	base := want{host: "db01", user: "sa", password: "x", enc: msdsn.EncryptionOff, trust: true}
	with := func(f func(*want)) want { w := base; f(&w); return w }
	cases := []struct {
		name string
		in   ConnParams
		want want
	}{
		{"host", sql("db01"), base},
		{"instance", sql(`db01\SALES`), with(func(w *want) { w.instance = "SALES" })},
		{"port", sql("db01,14330"), with(func(w *want) { w.port = 14330 })},
		{"instance and port", sql(`db01\SALES,14330`), with(func(w *want) { w.instance, w.port = "SALES", 14330 })},
		{"tcp prefix", sql("tcp:db01,14330"), with(func(w *want) { w.port = 14330 })},
		{"upper case tcp prefix", sql("TCP:db01,14330"), with(func(w *want) { w.port = 14330 })},
		{"dot", sql("."), with(func(w *want) { w.host = "localhost" })},
		{"local", sql("(local)"), with(func(w *want) { w.host = "localhost" })},
		{"spaces", sql(`  db01 \ SALES , 14330 `), with(func(w *want) { w.instance, w.port = "SALES", 14330 })},
		{"ipv6 with a port", sql("::1,14330"), with(func(w *want) { w.host, w.port = "::1", 14330 })},
		{"ipv6 without a port", sql("::1"), with(func(w *want) { w.host = "::1" })},
		{"hostile password", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: hostile}, with(func(w *want) { w.password = hostile })},
		{"empty sql password", ConnParams{Server: "db01", Auth: "sql", Login: "sa"}, with(func(w *want) { w.password = "" })},
		{"domain account", ConnParams{Server: "db01", Auth: "domain", Login: `CORP\dba`, Password: hostile}, with(func(w *want) { w.user, w.password = `CORP\dba`, hostile })},
		{"database", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: "x", Database: "Sales DB"}, with(func(w *want) { w.database = "Sales DB" })},
		{"required", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: "x", Encrypt: "true"}, with(func(w *want) { w.enc, w.trust = msdsn.EncryptionRequired, false })},
		{"required and trusted", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: "x", Encrypt: "true", Trust: true}, with(func(w *want) { w.enc = msdsn.EncryptionRequired })},
		{"strict", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: "x", Encrypt: "strict"}, with(func(w *want) { w.enc, w.trust = msdsn.EncryptionStrict, false })},
		{"trust is ignored under strict", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: "x", Encrypt: "strict", Trust: true}, with(func(w *want) { w.enc, w.trust = msdsn.EncryptionStrict, false })},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			name string
			in   ConnParams
			want want
		}{"current windows account", ConnParams{Server: "db01", Auth: "windows"}, with(func(w *want) { w.user, w.password = "", "" })})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dsn, err := BuildDSN(c.in)
			if err != nil {
				t.Fatalf("BuildDSN: %v", err)
			}
			cfg, err := msdsn.Parse(withAppName(dsn))
			if err != nil {
				t.Fatalf("the driver cannot parse %q: %v", dsn, err)
			}
			trust := cfg.TLSConfig != nil && cfg.TLSConfig.InsecureSkipVerify
			got := want{cfg.Host, cfg.Instance, cfg.User, cfg.Password, cfg.Database, cfg.Port, cfg.Encryption, trust}
			if got != c.want {
				t.Errorf("%q reads back as %+v, want %+v", dsn, got, c.want)
			}
		})
	}
}

func TestBuildDSNRefusesAnIncompleteForm(t *testing.T) {
	cases := []struct {
		name string
		in   ConnParams
		want string // a word the message must contain
	}{
		{"empty server", ConnParams{Server: "  ", Auth: "sql", Login: "sa"}, "server"},
		{"port not a number", ConnParams{Server: "db01,abc", Auth: "sql", Login: "sa"}, "port"},
		{"port zero", ConnParams{Server: "db01,0", Auth: "sql", Login: "sa"}, "port"},
		{"port too large", ConnParams{Server: "db01,65536", Auth: "sql", Login: "sa"}, "port"},
		{"empty instance", ConnParams{Server: `db01\`, Auth: "sql", Login: "sa"}, "instance"},
		{"space in the name", ConnParams{Server: "db 01", Auth: "sql", Login: "sa"}, "server name"},
		{"slash in the name", ConnParams{Server: "db01/x", Auth: "sql", Login: "sa"}, "server name"},
		{"question mark in the name", ConnParams{Server: "db01?x", Auth: "sql", Login: "sa"}, "server name"},
		{"hash in the name", ConnParams{Server: "db01#x", Auth: "sql", Login: "sa"}, "server name"},
		{"at sign in the name", ConnParams{Server: "sa@db01", Auth: "sql", Login: "sa"}, "server name"},
		{"space in the instance", ConnParams{Server: `db01\SA LES`, Auth: "sql", Login: "sa"}, "server name"},
		{"unknown method", ConnParams{Server: "db01", Auth: "kerberos"}, "authentication"},
		{"empty sql login", ConnParams{Server: "db01", Auth: "sql"}, "login"},
		{"empty domain login", ConnParams{Server: "db01", Auth: "domain", Password: "x"}, "login"},
		{"domain login without a backslash", ConnParams{Server: "db01", Auth: "domain", Login: "dba", Password: "x"}, `DOMAIN\user`},
		{"domain account without a password", ConnParams{Server: "db01", Auth: "domain", Login: `CORP\dba`}, "password"},
		{"unknown encryption", ConnParams{Server: "db01", Auth: "sql", Login: "sa", Encrypt: "yes"}, "encryption"},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, struct {
			name string
			in   ConnParams
			want string
		}{"windows account off windows", ConnParams{Server: "db01", Auth: "windows"}, "authentication"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dsn, err := BuildDSN(c.in)
			if err == nil {
				t.Fatalf("accepted, giving %q", dsn)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// TestDomainLoginSelectsNTLMOutsideWindows is the claim the domain method
// rests on: the backslash is what makes the driver use NTLM, and a plain
// login falls back to SQL authentication.
func TestDomainLoginSelectsNTLMOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("winsspi is the provider on Windows")
	}
	for _, c := range []struct {
		in         ConnParams
		integrated bool
	}{
		{ConnParams{Server: "db01", Auth: "domain", Login: `CORP\dba`, Password: "x"}, true},
		{ConnParams{Server: "db01", Auth: "sql", Login: "sa", Password: "x"}, false},
	} {
		dsn, err := BuildDSN(c.in)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := msdsn.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		a, err := integratedauth.GetIntegratedAuthenticator(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if (a != nil) != c.integrated {
			t.Errorf("%s: integrated authenticator %T, want integrated=%v", c.in.Login, a, c.integrated)
		}
	}
}

func TestAuthMethodsFollowThePlatform(t *testing.T) {
	for _, c := range []struct {
		goos string
		ids  string
		note bool // whether the domain method carries the NTLM note
	}{
		{"linux", "sql domain", true},
		{"darwin", "sql domain", true},
		{"windows", "sql windows domain", false},
	} {
		var ids []string
		var note string
		for _, m := range authMethods(c.goos) {
			ids = append(ids, m.ID)
			if m.ID == "domain" {
				note = m.Note
			}
		}
		if strings.Join(ids, " ") != c.ids {
			t.Errorf("%s offers %v, want %s", c.goos, ids, c.ids)
		}
		if (note != "") != c.note {
			t.Errorf("%s: domain note %q, want one=%v", c.goos, note, c.note)
		}
	}
	if len(AuthMethods()) != len(authMethods(runtime.GOOS)) {
		t.Error("AuthMethods does not follow runtime.GOOS")
	}
}

func TestRedactedHidesThePassword(t *testing.T) {
	const password = "p@ss:w/o#r%d"
	dsn, err := BuildDSN(ConnParams{Server: `db01\SALES`, Auth: "sql", Login: "sa", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	r := Redacted(dsn)
	if !strings.Contains(r, "xxxxx") || !strings.Contains(r, "db01") {
		t.Errorf("Redacted gives %q", r)
	}
	for _, leak := range []string{password, "p%40ss"} {
		if strings.Contains(r, leak) {
			t.Errorf("Redacted gives %q, which carries the password", r)
		}
	}
	if got := Redacted("::not a url"); got != "" {
		t.Errorf("an unparseable string redacts to %q, want empty", got)
	}
}
```

- [ ] Step 2: Run the tests to watch them fail

Run: `go test ./internal/source/mssql -run 'TestBuildDSN|TestDomainLogin|TestAuthMethods|TestRedacted' -count=1`
Expected: a compile failure, `undefined: ConnParams`.

- [ ] Step 3: Implement

Create `internal/source/mssql/connstr.go`:

```go
package mssql

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

// ConnParams is what the connect page posts. docs/specs/2026-09-11-connect-page-design.md
// section 5.
type ConnParams struct {
	Server   string `json:"server"`   // host, host\instance, host,port, as SSMS takes them
	Auth     string `json:"auth"`     // sql, windows or domain
	Login    string `json:"login"`    // sql: the login; domain: DOMAIN\user
	Password string `json:"password"` // sql and domain only
	Database string `json:"database"` // optional
	Encrypt  string `json:"encrypt"`  // "", "true" or "strict"
	Trust    bool   `json:"trust"`    // read only when Encrypt is "true"
}

// AuthMethod is one way of logging in that the connect page offers. Fields
// names which of login and password the form shows for it.
type AuthMethod struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Fields []string `json:"fields"`
	Note   string   `json:"note,omitempty"`
}

// AuthMethods lists the methods this binary can use, in the order the page
// shows them.
func AuthMethods() []AuthMethod { return authMethods(runtime.GOOS) }

// authMethods takes the platform as a parameter so every platform's list is
// testable anywhere. The current account needs winsspi, which go-mssqldb
// registers only in Windows builds.
func authMethods(goos string) []AuthMethod {
	ms := []AuthMethod{{ID: "sql", Label: "SQL Server login", Fields: []string{"login", "password"}}}
	if goos == "windows" {
		ms = append(ms, AuthMethod{ID: "windows", Label: "Windows, current account", Fields: []string{}})
	}
	domain := AuthMethod{ID: "domain", Label: "Windows, domain account", Fields: []string{"login", "password"}}
	if goos != "windows" {
		domain.Note = "Sent as NTLM. A domain policy can refuse NTLM; Kerberos then needs a connection string written by hand, see the README."
	}
	return append(ms, domain)
}

func offered(auth string) bool {
	for _, m := range AuthMethods() {
		if m.ID == auth {
			return true
		}
	}
	return false
}

// splitServer reads the server field the way SSMS does.
func splitServer(s string) (host, instance string, port int, err error) {
	s = strings.TrimSpace(s)
	if len(s) >= 4 && strings.EqualFold(s[:4], "tcp:") {
		s = strings.TrimSpace(s[4:])
	}
	if i := strings.LastIndex(s, ","); i >= 0 {
		n, err := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err != nil || n < 1 || n > 65535 {
			return "", "", 0, errors.New("the port after the comma must be a number from 1 to 65535")
		}
		port, s = n, strings.TrimSpace(s[:i])
	}
	host = s
	if h, inst, ok := strings.Cut(s, `\`); ok {
		host, instance = strings.TrimSpace(h), strings.TrimSpace(inst)
		if instance == "" {
			return "", "", 0, errors.New(`nothing follows the backslash: write host\instance, or leave the instance out`)
		}
	}
	if host == "." || strings.EqualFold(host, "(local)") {
		host = "localhost"
	}
	if host == "" {
		return "", "", 0, errors.New("the server is empty")
	}
	// These would end the host inside the URL, and the driver would then
	// fail with a bare "invalid URL format" instead of a word about the field.
	if strings.ContainsAny(host+instance, " \t/?#@") {
		return "", "", 0, errors.New("the server name cannot contain a space or any of / ? # @")
	}
	return host, instance, port, nil
}

// BuildDSN returns a sqlserver:// URL, or an error the page can show under
// the field at fault. Assembled with net/url, which is what escapes a
// password or a DOMAIN\user correctly.
func BuildDSN(p ConnParams) (string, error) {
	host, instance, port, err := splitServer(p.Server)
	if err != nil {
		return "", err
	}
	if !offered(p.Auth) {
		return "", fmt.Errorf("authentication %q is not offered by this build", p.Auth)
	}
	u := url.URL{Scheme: "sqlserver"}
	login := strings.TrimSpace(p.Login)
	switch p.Auth {
	case "sql":
		if login == "" {
			return "", errors.New("the login is empty")
		}
		u.User = url.UserPassword(login, p.Password)
	case "domain":
		if login == "" {
			return "", errors.New("the login is empty")
		}
		if !strings.Contains(login, `\`) {
			return "", errors.New(`a domain account is written DOMAIN\user`)
		}
		if p.Password == "" {
			return "", errors.New("a domain account needs its password")
		}
		u.User = url.UserPassword(login, p.Password)
	}
	// No brackets without a port: the driver keeps the host as given, and
	// would dial "[::1]" literally.
	u.Host = host
	if port > 0 {
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if instance != "" {
		u.Path = "/" + instance
	}
	q := url.Values{}
	if p.Database != "" {
		q.Set("database", p.Database)
	}
	switch p.Encrypt {
	case "":
	case "true":
		q.Set("encrypt", "true")
		if p.Trust {
			q.Set("trustservercertificate", "true")
		}
	case "strict":
		q.Set("encrypt", "strict")
	default:
		return "", fmt.Errorf("unknown encryption %q", p.Encrypt)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Redacted is dsn with its password replaced, for the page and the log. An
// unparseable string gives nothing rather than itself.
func Redacted(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return u.Redacted()
}
```

- [ ] Step 4: Run the tests to watch them pass

Run: `go test ./internal/source/mssql -run 'TestBuildDSN|TestDomainLogin|TestAuthMethods|TestRedacted' -count=1 -v 2>&1 | grep -c '^--- PASS: Test'`
Expected: `5` on Linux (the subtests are counted separately and do not start with `--- PASS: Test`). Then run the whole package without the container: `go test ./internal/source/mssql -count=1` must say `ok`.

- [ ] Step 5: Break it and watch the right tests fail

One at a time, restoring from a copy each time. Each still compiles:

1. Replace `u.Host = net.JoinHostPort(host, strconv.Itoa(port))` with the two lines `_ = net.JoinHostPort` and `u.Host = host + ":" + strconv.Itoa(port)`. Expected: `ipv6 with a port` fails.
2. Before `u.Host = host`, add `if strings.Contains(host, ":") { host = "[" + host + "]" }`. Expected: both IPv6 cases fail, the one with a port on doubled brackets.
3. Delete the `EqualFold` block that strips `tcp:`. Expected: both prefix cases fail.
4. Build the user as `url.User(login)` for `domain`. Expected: `domain account` fails on its password.
5. Delete the `strings.ContainsAny(host+instance, ...)` check. Expected: the six name cases of `TestBuildDSNRefusesAnIncompleteForm` fail.

- [ ] Step 6: Gates and commit

```bash
gofmt -l . ; go vet ./... && go test ./internal/source/mssql -count=1
git add internal/source/mssql/connstr.go internal/source/mssql/connstr_test.go
git commit -F - <<'EOF'
Build a connection string from what a DBA types into SSMS

The connect page takes a server the way SSMS does, host, host\instance or
host,port, and a login method, and this turns them into a sqlserver:// URL.
It lives here rather than in the page because net/url is what escapes a
password with an @ or a colon, and the backslash of a domain account,
correctly, and because instance names and Windows logins are SQL Server
notions the web package has no business knowing.

The tests read every result back through go-mssqldb's own parser, after
withAppName as Open sends it, so what they check is what the driver
understands rather than what the string looks like. That is how the IPv6
case was settled: brackets only with a port, because without one the driver
would dial the brackets.
EOF
```

---

### Task 3: One line of advice per known driver failure

Spec section 6, the hint table.

Files:
- Create: `internal/source/mssql/hint.go`
- Create: `internal/source/mssql/hint_test.go`

Interfaces:
- Consumes: `ConnParams`, `BuildDSN` from Task 2; `New`, `Open`, `Close` on `*Source`.
- Produces: `func Hint(err error) string`.

- [ ] Step 1: Write the failing tests

Create `internal/source/mssql/hint_test.go`:

```go
package mssql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-mssqldb/msdsn"
)

// hintFor is the advice of the row matching on in. The tests name rows by
// their key and by a word the advice must carry, never by position: an
// expectation read out of the table by index moves with the table, and a
// reordering would pass.
func hintFor(t *testing.T, in, word string) string {
	t.Helper()
	for _, h := range hints {
		if h.in == in {
			if !strings.Contains(h.hint, word) {
				t.Fatalf("the advice for %q no longer mentions %q: %q", in, word, h.hint)
			}
			return h.hint
		}
	}
	t.Fatalf("no hint row matches on %q", in)
	return ""
}

// TestHintPicksTheFirstMatchingRow uses the messages the driver actually
// returned against the container. The certificate one carries both
// "TLS Handshake failed" and "x509:", which is why the table is ordered.
func TestHintPicksTheFirstMatchingRow(t *testing.T) {
	for _, c := range []struct {
		err, in, word string
	}{
		{"mssql: connect: no instance matching 'NOPE' returned from host '127.0.0.1'", "no instance matching", "host,port"},
		{"mssql: connect: TLS Handshake failed: tls: failed to verify certificate: x509: cannot validate certificate for 127.0.0.1 because it doesn't contain any IP SANs", "x509:", "certificate"},
		{"mssql: connect: TLS Handshake failed: EOF", "TLS Handshake failed", "TDS 8"},
		{"mssql: connect: mssql: login error: Login failed for user 'sa'.", "Login failed for user", "SQL Server authentication"},
	} {
		if got, want := Hint(errors.New(c.err)), hintFor(t, c.in, c.word); got != want {
			t.Errorf("Hint(%q) = %q, want %q", c.err, got, want)
		}
	}
	if got := Hint(errors.New("mssql: connect: unable to open tcp connection with host '127.0.0.1:1': dial tcp 127.0.0.1:1: connect: connection refused")); got != "" {
		t.Errorf("a refused TCP connection got the hint %q", got)
	}
	if Hint(nil) != "" {
		t.Error("Hint(nil) is not empty")
	}
}

// containerParams is what the connect page would post for the container
// SQLTOP_TEST_DSN points at.
func containerParams(t *testing.T) ConnParams {
	t.Helper()
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		if os.Getenv("SQLTOP_REQUIRE_DB") != "" {
			t.Fatal("SQLTOP_TEST_DSN is unset and SQLTOP_REQUIRE_DB is set; run: eval \"$(scripts/testdb.sh)\"")
		}
		t.Skip("SQLTOP_TEST_DSN is unset; run: eval \"$(scripts/testdb.sh)\"")
	}
	cfg, err := msdsn.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	server := cfg.Host
	if cfg.Port != 0 { // 0 when the DSN names none, which BuildDSN would refuse as a port
		server = fmt.Sprintf("%s,%d", cfg.Host, cfg.Port)
	}
	return ConnParams{Server: server, Auth: "sql", Login: cfg.User, Password: cfg.Password}
}

func TestConnectPageParamsOpenTheContainer(t *testing.T) {
	dsn, err := BuildDSN(containerParams(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := New()
	if err := s.Open(ctx, dsn); err != nil {
		t.Fatalf("the string the page would build does not connect: %v", err)
	}
	s.Close()
}

// TestHintsMatchWhatTheDriverSays checks the table against the driver rather
// than against the document it came from.
func TestHintsMatchWhatTheDriverSays(t *testing.T) {
	good := containerParams(t)
	for _, c := range []struct {
		name string
		edit func(*ConnParams)
		in   string // what the error must contain, and the row it selects
		word string // what that row's advice must say
	}{
		{"named instance", func(p *ConnParams) { p.Server = `127.0.0.1\NOPE` }, "no instance matching", "host,port"},
		{"certificate", func(p *ConnParams) { p.Encrypt = "true" }, "x509:", "certificate"},
		{"strict", func(p *ConnParams) { p.Encrypt = "strict" }, "TLS Handshake failed", "TDS 8"},
		{"wrong password", func(p *ConnParams) { p.Password += "-wrong" }, "Login failed for user", "SQL Server authentication"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := good
			c.edit(&p)
			dsn, err := BuildDSN(p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s := New()
			err = s.Open(ctx, dsn)
			if err == nil {
				s.Close()
				t.Fatal("connected; this case needs a failure")
			}
			if !strings.Contains(err.Error(), c.in) {
				t.Fatalf("the driver said %q, which does not contain %q", err, c.in)
			}
			if got, want := Hint(err), hintFor(t, c.in, c.word); got != want {
				t.Errorf("hint %q, want %q", got, want)
			}
		})
	}
}
```

- [ ] Step 2: Run the tests to watch them fail

Run: `go test ./internal/source/mssql -run 'TestHint' -count=1`
Expected: a compile failure naming `hints` or `Hint` as undefined; which one the compiler reports first does not matter.

- [ ] Step 3: Implement

Create `internal/source/mssql/hint.go`:

```go
package mssql

import "strings"

// hints is read in order and the first match wins: a certificate failure
// reads "TLS Handshake failed: ... x509: ...", so x509 comes before the
// handshake row.
var hints = []struct{ in, hint string }{
	{"no instance matching", "SQL Server Browser did not return that instance. Type host,port instead; the port is in SQL Server Configuration Manager."},
	{"x509:", `The server certificate was refused. Leave encryption on the driver default, or choose "required" and tick "trust the server certificate".`},
	{"TLS Handshake failed", `The server did not complete the TLS handshake. With "strict", the server must support TDS 8 (SQL Server 2022 and later, with strict encryption configured); otherwise choose another encryption mode.`},
	{"Login failed for user", "The server refused the login. For a SQL Server login, the server must also allow SQL Server authentication."},
}

// Hint is the advice the connect page shows under a failed attempt, or
// nothing when the error is not one it knows.
func Hint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, h := range hints {
		if strings.Contains(msg, h.in) {
			return h.hint
		}
	}
	return ""
}
```

- [ ] Step 4: Run the tests to watch them pass, with the container, in one invocation

Run: `eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql -run 'TestHint|TestConnectPageParams' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: three top-level tests, `--- PASS: TestHintPicksTheFirstMatchingRow`, `--- PASS: TestConnectPageParamsOpenTheContainer`, `--- PASS: TestHintsMatchWhatTheDriverSays`, and four `PASS` subtests under the last. Any `SKIP` means the container was not reached: fix that before going on.

- [ ] Step 5: Break it and watch the right tests fail

1. Swap the `x509:` and `TLS Handshake failed` rows. Expected: the certificate case fails in both tests, getting the handshake advice; the strict case still passes.
2. In the table only, change `Login failed for user` to `Login failed for usr`. Expected: `hintFor` fails in both tests with "no hint row matches".
3. In the `x509:` row's advice, replace every `certificate` with `cert`. Expected: `hintFor` fails in both tests naming the missing word, which is what stops a reworded hint from passing unnoticed.

- [ ] Step 6: Gates and commit

```bash
gofmt -l . ; go vet ./... && eval "$(scripts/testdb.sh)" && go test ./internal/source/mssql -count=1
git add internal/source/mssql/hint.go internal/source/mssql/hint_test.go
git commit -F - <<'EOF'
Say one useful thing under the four failures the connect page will meet

A DBA who types a named instance behind a firewall, or ticks strict against
a server that does not speak TDS 8, gets a driver message that names the
mechanism and not the way out. Four fixed strings pick a line of advice.
They are checked against what the driver actually returns from the
container, not against the design document, and read in order because a
refused certificate says both "TLS Handshake failed" and "x509:".
EOF
```

---

### Task 4: The listening socket and token as a value of their own

Spec section 3.1, step 2. No change of behaviour: `NewServer` keeps its signature and its tests.

Files:
- Modify: `internal/web/server.go`
- Modify: `internal/web/server_test.go`

Interfaces:
- Consumes: nothing new.
- Produces:
  - `type Listener struct { ln *net.TCPListener; token string }`
  - `func Listen(cfg config.Server) (*Listener, error)`
  - `func (l *Listener) URL() string`, `func (l *Listener) Close() error`
  - `func NewServerOn(c *collector.Collector, w *window.Window, l *Listener) *Server`
  - `func requireToken(token string, next http.Handler) http.Handler`, in `server.go`

- [ ] Step 1: Write the failing test

Append to `internal/web/server_test.go`:

```go
// TestNewServerOnServesOnTheListenersAddressAndToken is what lets the connect
// page and the monitor take turns on one socket: the monitor must answer on
// the listener's address, to the listener's token, and to nothing else.
func TestNewServerOnServesOnTheListenersAddressAndToken(t *testing.T) {
	l, err := Listen(config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	w := window.New(time.Minute, 1000)
	c := collector.New(fake.New(nil), w, collector.NewBudget(50, testTiers()))
	s := NewServerOn(c, w, l)
	t.Cleanup(func() { s.Close() })

	if s.URL() != l.URL() {
		t.Errorf("server URL %q, listener URL %q", s.URL(), l.URL())
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t="+l.token))
	if rec.Code != http.StatusOK {
		t.Errorf("status with the listener's token = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status?t=other"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status with another token = %d, want 401", rec.Code)
	}
}
```

- [ ] Step 2: Run it to watch it fail

Run: `go test ./internal/web -run TestNewServerOn -count=1`
Expected: a compile failure naming `Listen` or `NewServerOn` as undefined.

- [ ] Step 3: Implement

In `internal/web/server.go`, replace `NewServer` and its doc comment with:

```go
// Listener is the bound loopback socket and the token that guards it. It
// is made first so that the connect page and the monitor can serve on one
// address, one after the other.
type Listener struct {
	ln    *net.TCPListener
	token string
}

// Listen binds 127.0.0.1 and nothing else. There is deliberately no option
// to widen it: this interface will eventually be able to kill sessions on a
// production server, and a bind on all interfaces would hand that to anyone
// on the network. cfg.Port is the only knob, and it still only ever selects
// a port on the loopback address, never the interface.
func Listen(cfg config.Server) (*Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("web: listen: %w", err)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		ln.Close()
		return nil, fmt.Errorf("web: token: %w", err)
	}
	return &Listener{ln: ln.(*net.TCPListener), token: hex.EncodeToString(raw[:])}, nil
}

// URL is the address with its token. Server.URL's comment says what putting
// the token there costs.
func (l *Listener) URL() string {
	return fmt.Sprintf("http://%s/?t=%s", l.ln.Addr().String(), l.token)
}

// Close releases the socket, for a caller that stops before any server has.
func (l *Listener) Close() error { return l.ln.Close() }

// NewServer binds its own listener. See Listen.
func NewServer(c *collector.Collector, w *window.Window, cfg config.Server) (*Server, error) {
	l, err := Listen(cfg)
	if err != nil {
		return nil, err
	}
	return NewServerOn(c, w, l), nil
}

// NewServerOn serves the monitor on an existing listener and its token.
func NewServerOn(c *collector.Collector, w *window.Window, l *Listener) *Server {
	// Built with the defaults rather than with a zero Config: every path
	// that reads the configuration (the dashboard, the grid columns, the
	// two endpoints that validate against it) then has one shape to handle
	// instead of two, and a test server behaves like a real one that
	// happened to find no file.
	srv := &Server{col: c, win: w, token: l.token, listener: l.ln}
	return srv.WithConfig(config.Default())
}
```

Replace the `authenticate` method with a function of the token. Its doc comment stays word for word, except that its first word, `authenticate`, becomes `requireToken`. It stays in this file, where `TestConstantTimeCompareIsUsedForTheToken` looks for it:

```go
func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if !hostAllowed(req.Host) {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := req.URL.Query().Get("t")
		if got == "" {
			got = req.Header.Get("X-Sqltop-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, req)
	})
}
```

In `Handler`, the last line becomes `return securityHeaders(requireToken(s.token, mux))`.

Then, in the comments of `server.go` and `server_test.go`, every occurrence of the whole word `authenticate` becomes `requireToken`, and the one quotation `securityHeaders(s.authenticate(...))` in `server_test.go` becomes `securityHeaders(requireToken(...))`. Words that merely contain it, `unauthenticated` and `authenticated`, stay. Besides the call, the doc comment and the declaration handled above, there are eleven such comment lines today: `server.go` lines 106, 152, 185 and 281, `server_test.go` lines 131, 241, 280, 462, 467, 507 and 509 (line 509 is the quotation). Afterwards `grep -nw authenticate internal/web/*.go` prints nothing.

Finally, the comment on `routes()` begins "routes is the single place a path is registered." Make that sentence "routes is the single place a path of the monitor is registered; the connect page's two are in AskConnection, behind the same requireToken." and leave the rest of that comment as it is.

- [ ] Step 4: Run the whole package

Run: `go test ./internal/web -count=1 2>&1 | tail -3`
Expected: `ok`. Then `go test ./internal/web -run TestNewServerOn -count=1 -v | grep -c '^--- PASS'` prints `1`.

- [ ] Step 5: Break it and watch the right tests fail

1. In `NewServerOn`, give the server a fresh token from `rand.Read` instead of `l.token`. Expected: `TestNewServerOnServesOnTheListenersAddressAndToken` fails on both the URL and the 200.
2. Replace `subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1` with `subtle.ConstantTimeCompare([]byte(got), []byte(got)) != 1`, which compiles and accepts any token. Expected: `TestTokenIsRequired`, `TestWrongTokenIsRejected` and the 401 half of `TestNewServerOnServesOnTheListenersAddressAndToken` fail.

- [ ] Step 6: Gates and commit

```bash
gofmt -l . ; go vet ./... && go test ./internal/web -count=1
git add internal/web/server.go internal/web/server_test.go
git commit -F - <<'EOF'
Make the socket and its token a value the server is given

Starting without a connection string will serve a connect page first and
the monitor afterwards, and the tab the browser already opened has to keep
working across the change: same address, same token. So binding and
drawing the token move out of NewServer into Listen, and NewServerOn builds
a monitor on an existing listener. NewServer keeps its signature and calls
both, so nothing that uses it changes.

The token check becomes a function of the token for the same reason, so
both phases go through the one constant-time compare.
EOF
```

---

### Task 5: The connect phase and its page

Spec sections 3.2, 3.3, 4 and 6.

Files:
- Create: `internal/web/connect.go`
- Create: `internal/web/connect_test.go`
- Create: `internal/web/assets/connect.html`
- Create: `internal/web/assets/connect.js`
- Modify: `internal/web/server.go` (compose one page from any HTML and script)
- Modify: `internal/web/assets/style.css` (the connect page's rules)

Interfaces:
- Consumes: `Listener`, `requireToken`, `securityHeaders`, `gracefulShutdown`, `shutdownGrace`, `writeJSON`, `route` from Task 4 and the existing file.
- Produces:
  - `type ConnectFunc func(ctx context.Context, body []byte) (ConnectResult, error)`
  - `type ConnectResult struct { DSN, EnvWritten, EnvError string }` with JSON tags `dsn`, `env_written`, `env_error`
  - `type ConnectError struct { Status int; Message, Hint string }`, implementing `error`
  - `func (l *Listener) AskConnection(ctx context.Context, options any, connect ConnectFunc) error`
  - `var connectTimeout = 30 * time.Second`
  - Page element ids the browser test uses: `connectForm`, `server`, `methods` (radios `name="auth"`), `loginRow`, `login`, `passwordRow`, `password`, `methodNote`, `database`, `encrypt`, `trustRow`, `trust`, `saveRow`, `saveEnv`, `envPath`, `saveNote`, `saveOff`, `go`, `status`, `error`, `hint`.
  - The options object the page reads: `methods` (a list of `{id, label, fields, note}`), `env_path` (string), and `save` (boolean; `false` hides the save box and shows `saveOff`; absent counts as `true`).

- [ ] Step 1: Write the failing tests

Create `internal/web/connect_test.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// fresh sends every request on a new connection, so no test is answered on a
// kept-alive socket by whichever server happened to accept it first.
var fresh = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}

var testOptions = map[string]any{
	"methods":  []map[string]any{{"id": "sql", "label": "SQL Server login", "fields": []string{"login", "password"}}},
	"env_path": "/somewhere/.env",
}

// askConnection starts a connect phase on a fresh listener. The channel
// receives what AskConnection returns.
func askConnection(t *testing.T, connect ConnectFunc) (*Listener, string, <-chan error, context.CancelFunc) {
	t.Helper()
	l, err := Listen(config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		errc <- l.AskConnection(ctx, testOptions, connect)
		close(returned)
	}()
	// A phase still running when its test ends shuts down afterwards, and
	// its shutdown reads shutdownGrace, which a later test writes. Wait for it.
	t.Cleanup(func() {
		cancel()
		<-returned
	})
	return l, "http://" + l.ln.Addr().String(), errc, cancel
}

func postJSON(t *testing.T, url, body string) (int, map[string]string) {
	t.Helper()
	res, err := fresh.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func succeed(context.Context, []byte) (ConnectResult, error) {
	return ConnectResult{DSN: "sqlserver://sa:xxxxx@db01"}, nil
}

func TestConnectPhaseNeedsTheToken(t *testing.T) {
	called := 0
	l, base, _, _ := askConnection(t, func(context.Context, []byte) (ConnectResult, error) {
		called++
		return ConnectResult{}, nil
	})
	for _, path := range []string{"/", "/api/connect"} {
		res, err := fresh.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, res.StatusCode)
		}
		req, _ := http.NewRequest(http.MethodGet, base+path+"?t="+l.token, nil)
		req.Host = "evil.attacker.example"
		res, err = fresh.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with a foreign Host = %d, want 401", path, res.StatusCode)
		}
	}
	if code, _ := postJSON(t, base+"/api/connect", `{}`); code != http.StatusUnauthorized {
		t.Errorf("POST without a token = %d, want 401", code)
	}
	if called != 0 {
		t.Errorf("the attempt ran %d times for requests without a token", called)
	}
}

func TestConnectPhaseServesThePageAndTheOptions(t *testing.T) {
	l, base, _, _ := askConnection(t, succeed)
	q := "?t=" + l.token

	res, err := fresh.Get(base + "/" + q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="connectForm"`) {
		t.Fatalf("GET / = %d without the form", res.StatusCode)
	}
	// The one page that takes a password carries the monitor's headers.
	if res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the connect page is served with Referrer-Policy %q and Cache-Control %q",
			res.Header.Get("Referrer-Policy"), res.Header.Get("Cache-Control"))
	}
	for _, sub := range []string{`href="style.css"`, `src="connect.js"`} {
		if strings.Contains(string(body), sub) {
			t.Errorf("the page still asks the browser for %s, which a relative URL fetches without the token", sub)
		}
	}

	res, err = fresh.Get(base + "/api/connect" + q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"env_path":"/somewhere/.env"`) {
		t.Errorf("the options came back as %s", body)
	}

	// The page polls this route to learn that the monitor has taken over.
	res, err = fresh.Get(base + "/api/status" + q)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/status during the connect phase = %d, want 404", res.StatusCode)
	}
}

func TestConnectRefusesOtherMethods(t *testing.T) {
	l, base, _, _ := askConnection(t, succeed)
	req, _ := http.NewRequest(http.MethodPut, base+"/api/connect?t="+l.token, strings.NewReader("{}"))
	res, err := fresh.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want 405", res.StatusCode)
	}
}

func TestSecondAttemptWhileOneRunsIsRefusedAtOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l, base, done, _ := askConnection(t, func(context.Context, []byte) (ConnectResult, error) {
		close(entered)
		<-release
		return ConnectResult{DSN: "x"}, nil
	})
	url := base + "/api/connect?t=" + l.token
	first := make(chan int, 1)
	go func() {
		// Not postJSON: t.Fatal must not be called from this goroutine.
		res, err := fresh.Post(url, "application/json", strings.NewReader(`{}`))
		if err != nil {
			first <- -1
			return
		}
		res.Body.Close()
		first <- res.StatusCode
	}()
	<-entered
	start := time.Now()
	code, body := postJSON(t, url, `{}`)
	if code != http.StatusConflict {
		t.Errorf("second POST = %d, want 409", code)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("the second POST waited %v for the first; it must be refused at once", el)
	}
	if body["error"] == "" {
		t.Error("the 409 carries no message")
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("first POST = %d, want 200", code)
	}
	if err := <-done; err != nil {
		t.Errorf("AskConnection returned %v after a success", err)
	}
}

func TestConnectErrorReachesThePage(t *testing.T) {
	calls := 0
	l, base, _, _ := askConnection(t, func(context.Context, []byte) (ConnectResult, error) {
		calls++
		if calls == 1 {
			return ConnectResult{}, &ConnectError{Status: http.StatusBadGateway, Message: "boom", Hint: "try x"}
		}
		return ConnectResult{}, errors.New("plain")
	})
	url := base + "/api/connect?t=" + l.token
	code, body := postJSON(t, url, `{}`)
	if code != http.StatusBadGateway || body["error"] != "boom" || body["hint"] != "try x" {
		t.Errorf("a ConnectError came out as %d %v", code, body)
	}
	code, body = postJSON(t, url, `{}`)
	if code != http.StatusBadGateway || body["error"] != "plain" {
		t.Errorf("a plain error came out as %d %v", code, body)
	}
}

func TestAttemptHasADeadline(t *testing.T) {
	old := connectTimeout
	connectTimeout = 100 * time.Millisecond
	t.Cleanup(func() { connectTimeout = old })
	l, base, _, _ := askConnection(t, func(ctx context.Context, _ []byte) (ConnectResult, error) {
		<-ctx.Done()
		return ConnectResult{}, ctx.Err()
	})
	start := time.Now()
	code, _ := postJSON(t, base+"/api/connect?t="+l.token, `{}`)
	if code != http.StatusBadGateway {
		t.Errorf("an attempt past its deadline = %d, want 502", code)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("the attempt ran %v; its context should have ended it at 100ms", el)
	}
}

// TestHandoverAnswersARequestSentBetweenTheTwoServers is the claim the
// design rests on: while nobody accepts, a request waits in the kernel's
// queue on the socket that never closed, and the monitor answers it.
func TestHandoverAnswersARequestSentBetweenTheTwoServers(t *testing.T) {
	l, base, done, _ := askConnection(t, succeed)
	if code, body := postJSON(t, base+"/api/connect?t="+l.token, `{}`); code != http.StatusOK || body["dsn"] == "" {
		t.Fatalf("POST = %d %v", code, body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AskConnection did not return after a successful attempt")
	}

	got := make(chan int, 1)
	go func() {
		res, err := fresh.Get(base + "/api/status?t=" + l.token)
		if err != nil {
			got <- -1
			return
		}
		res.Body.Close()
		got <- res.StatusCode
	}()
	time.Sleep(200 * time.Millisecond) // the request is queued; nobody accepts yet

	w := window.New(time.Minute, 1000)
	c := collector.New(fake.New(nil), w, collector.NewBudget(50, testTiers()))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = NewServerOn(c, w, l).Serve(ctx)
	}()
	// Serve's shutdown reads shutdownGrace, which the next test writes: it
	// must be over before this test is.
	defer func() {
		cancel()
		<-served
	}()

	select {
	case code := <-got:
		if code != http.StatusOK {
			t.Errorf("the request sent during the handover got %d, want 200 from the monitor", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request sent during the handover was never answered")
	}
}

// TestHandoverIsNotHeldByAnIdleConnection: net/http holds Shutdown five
// seconds for a connection that has sent nothing, which is what a browser's
// speculative connection is. The grace cuts it short.
func TestHandoverIsNotHeldByAnIdleConnection(t *testing.T) {
	old := shutdownGrace
	shutdownGrace = 200 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })

	l, base, done, _ := askConnection(t, succeed)
	idle, err := net.Dial("tcp", l.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	time.Sleep(100 * time.Millisecond) // accepted, and silent

	if code, _ := postJSON(t, base+"/api/connect?t="+l.token, `{}`); code != http.StatusOK {
		t.Fatalf("POST = %d", code)
	}
	start := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if el := time.Since(start); el > 1500*time.Millisecond {
			t.Errorf("the handover took %v with an idle connection open; the grace is %v", el, shutdownGrace)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("AskConnection did not return")
	}
}

func TestAskConnectionReturnsWhenCancelled(t *testing.T) {
	_, _, done, cancel := askConnection(t, succeed)
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("AskConnection returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AskConnection did not return after its context was cancelled")
	}
}
```

- [ ] Step 2: Run the tests to watch them fail

Run: `go test ./internal/web -run 'TestConnectPhase|TestConnectRefuses|TestSecondAttempt|TestConnectError|TestAttemptHasADeadline|TestHandover|TestAskConnection' -count=1`
Expected: a compile failure, `undefined: ConnectFunc` or `l.AskConnection undefined`.

- [ ] Step 3: Generalise the page composition

In `internal/web/server.go`, add `"path"` to the imports and split `composePage` in two. Its long comment stays on `composePage`; `compose` carries only what differs:

```go
func composePage() ([]byte, error) { return compose("assets/index.html", "assets/app.js") }

// compose builds one self-contained page from an HTML file, style.css and a
// script, for the reason given on composePage.
func compose(htmlPath, jsPath string) ([]byte, error) {
	html, err := assetsFS.ReadFile(htmlPath)
	if err != nil {
		return nil, err
	}
	css, err := assetsFS.ReadFile("assets/style.css")
	if err != nil {
		return nil, err
	}
	js, err := assetsFS.ReadFile(jsPath)
	if err != nil {
		return nil, err
	}

	link := []byte(`<link rel="stylesheet" href="style.css">`)
	if !bytes.Contains(html, link) {
		return nil, fmt.Errorf("web: %s does not carry the expected stylesheet link", htmlPath)
	}
	styled := append([]byte("<style>\n"), css...)
	styled = append(styled, []byte("\n</style>")...)
	html = bytes.Replace(html, link, styled, 1)

	script := []byte(`<script src="` + path.Base(jsPath) + `"></script>`)
	if !bytes.Contains(html, script) {
		return nil, fmt.Errorf("web: %s does not carry the expected script tag", htmlPath)
	}
	inlined := append([]byte("<script>\n"), js...)
	inlined = append(inlined, []byte("\n</script>")...)
	html = bytes.Replace(html, script, inlined, 1)

	return html, nil
}
```

- [ ] Step 4: Write the connect phase

Create `internal/web/connect.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// ConnectFunc tries one connection from the body the connect page posted.
// On failure it returns a *ConnectError carrying the status and the words
// to show; any other error is shown as a failed connection.
type ConnectFunc func(ctx context.Context, body []byte) (ConnectResult, error)

// ConnectResult is what the page shows after a success. DSN is redacted.
type ConnectResult struct {
	DSN        string `json:"dsn"`
	EnvWritten string `json:"env_written"`
	EnvError   string `json:"env_error"`
}

// ConnectError is a failed attempt as the page shows it.
type ConnectError struct {
	Status        int
	Message, Hint string
}

func (e *ConnectError) Error() string { return e.Message }

// connectTimeout bounds one attempt. A var only so a test can shrink it.
var connectTimeout = 30 * time.Second

var connectPage = sync.OnceValues(func() ([]byte, error) {
	return compose("assets/connect.html", "assets/connect.js")
})

// handoff lets http.Server.Shutdown stop accepting without closing the
// socket: the monitor accepts on it next, and a connection arriving between
// the two waits in the kernel's queue instead of being refused.
type handoff struct{ *net.TCPListener }

func (h handoff) Close() error { return h.SetDeadline(time.Now()) }

// connectPhase answers the connect page until one attempt succeeds.
type connectPhase struct {
	options   any
	connect   ConnectFunc
	busy      sync.Mutex // held for one attempt; TryLock makes a second a 409
	connected bool       // under busy
	done      chan struct{}
}

// AskConnection serves the connect page on l until connect succeeds once,
// then stops without closing the socket, so the monitor can serve on it
// next with the same token. It returns ctx.Err() when ctx ends first.
// docs/specs/2026-09-11-connect-page-design.md section 3.
func (l *Listener) AskConnection(ctx context.Context, options any, connect ConnectFunc) error {
	if _, err := connectPage(); err != nil {
		return err
	}
	p := &connectPhase{options: options, connect: connect, done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(p.index))
	mux.Handle("/api/connect", http.HandlerFunc(p.attempt))
	srv := &http.Server{
		Handler:           securityHeaders(requireToken(l.token, mux)),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(handoff{l.ln}) }()

	var err error
	select {
	case <-p.done:
	case <-ctx.Done():
		err = ctx.Err()
	case serr := <-served:
		return fmt.Errorf("web: connect page: %w", serr)
	}
	// Bounded: a browser leaves speculative connections idle, and net/http
	// would otherwise wait five seconds on each before letting go.
	gracefulShutdown(srv)
	<-served
	if derr := l.ln.SetDeadline(time.Time{}); derr != nil && err == nil {
		err = derr
	}
	return err
}

func (p *connectPhase) index(rw http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(rw, req)
		return
	}
	body, err := connectPage()
	if err != nil {
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Write(body)
}

func (p *connectPhase) attempt(rw http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		writeJSON(rw, p.options)
		return
	case http.MethodPost:
	default:
		rw.Header().Set("Allow", "GET, POST")
		http.Error(rw, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	if !p.busy.TryLock() {
		writeJSONStatus(rw, http.StatusConflict, map[string]string{"error": "a connection attempt is already running"})
		return
	}
	defer p.busy.Unlock()
	if p.connected {
		writeJSONStatus(rw, http.StatusConflict, map[string]string{"error": "already connected"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, req.Body, 64<<10))
	if err != nil {
		writeJSONStatus(rw, http.StatusBadRequest, map[string]string{"error": "unreadable request: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), connectTimeout)
	defer cancel()
	res, err := p.connect(ctx, body)
	if err != nil {
		var ce *ConnectError
		if !errors.As(err, &ce) {
			ce = &ConnectError{Status: http.StatusBadGateway, Message: err.Error()}
		}
		writeJSONStatus(rw, ce.Status, map[string]string{"error": ce.Message, "hint": ce.Hint})
		return
	}
	p.connected = true
	writeJSON(rw, res)
	close(p.done)
}

func writeJSONStatus(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(v)
}
```

`close(p.done)` runs inside the handler, after the response is written: `gracefulShutdown` then waits for that handler to return before the server stops.

- [ ] Step 5: Write the page

Create `internal/web/assets/connect.html`. The icon is the monitor page's data URI, the same bytes as `index.html` line 14, because a page without one makes the browser ask for `/favicon.ico` with no token:

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>sqltop: connect</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Crect width='16' height='16' rx='3' fill='%2314161a'/%3E%3Crect x='3' y='9' width='2' height='4' fill='%236fb3d2'/%3E%3Crect x='7' y='5' width='2' height='8' fill='%238fbf7f'/%3E%3Crect x='11' y='7' width='2' height='6' fill='%23d08770'/%3E%3C/svg%3E">
<link rel="stylesheet" href="style.css">
</head>
<body>

<header>
  <h1>sqltop</h1>
  <div class="conn"><span>no instance configured</span></div>
</header>

<main class="connect">
<form id="connectForm" autocomplete="off">
  <label>server
    <input type="text" id="server" required autofocus placeholder="db01, db01\SALES or db01,14330">
  </label>
  <fieldset id="methods"><legend>authentication</legend></fieldset>
  <p id="methodNote" class="note" hidden></p>
  <label id="loginRow">login <input type="text" id="login" autocomplete="username"></label>
  <label id="passwordRow">password <input type="password" id="password" autocomplete="current-password"></label>
  <details>
    <summary>more</summary>
    <label>database <input type="text" id="database" placeholder="the login's default"></label>
    <p class="note">Azure SQL Database needs one.</p>
    <label>encryption
      <select id="encrypt">
        <option value="">driver default</option>
        <option value="true">required</option>
        <option value="strict">strict (TDS 8)</option>
      </select>
    </label>
    <label id="trustRow" hidden><input type="checkbox" id="trust"> trust the server certificate</label>
  </details>
  <label id="saveRow"><input type="checkbox" id="saveEnv"> save the connection string to <code id="envPath"></code></label>
  <p id="saveNote" class="note" hidden>The password will be stored in clear text in that file.</p>
  <p id="saveOff" class="note" hidden>sqltop.yaml names an instance whose connection string does not read SQLTOP_CONN, so saving to .env would change nothing.</p>
  <button type="submit" id="go">connect</button>
</form>
<p id="status" role="status" aria-live="polite"></p>
<p id="error" class="listError" hidden></p>
<p id="hint" class="note" hidden></p>
</main>

<script src="connect.js"></script>
</body>
</html>
```

After writing it, check the icon is byte for byte the monitor's: `diff <(sed -n 14p internal/web/assets/index.html) <(grep 'rel="icon"' internal/web/assets/connect.html)` prints nothing.

Create `internal/web/assets/connect.js`:

```js
// The connect page: builds the form from what the server says this binary
// can do, posts it, and hands over to the monitor on the same address.
"use strict";

const $ = (id) => document.getElementById(id);
const q = "?t=" + encodeURIComponent(new URLSearchParams(location.search).get("t") || "");
let methods = [];

function method() {
  const r = document.querySelector('input[name="auth"]:checked');
  return methods.find((m) => m.id === (r && r.value)) || methods[0] || { id: "", fields: [] };
}

function say(id, text) {
  $(id).textContent = text;
  $(id).hidden = !text;
}

// Only the chosen method's fields are on screen, and only they are sent.
function showFields() {
  const m = method();
  const f = new Set(m.fields || []);
  $("loginRow").hidden = !f.has("login");
  $("passwordRow").hidden = !f.has("password");
  $("login").placeholder = m.id === "domain" ? "DOMAIN\\user" : "";
  say("methodNote", m.note || "");
  $("saveNote").hidden = !f.has("password") || $("saveRow").hidden;
  $("trustRow").hidden = $("encrypt").value !== "true";
}

function setup(opts) {
  methods = opts.methods || [];
  for (const [i, m] of methods.entries()) {
    const input = document.createElement("input");
    input.type = "radio";
    input.name = "auth";
    input.value = m.id;
    input.checked = i === 0;
    input.addEventListener("change", showFields);
    const label = document.createElement("label");
    label.append(input, " " + m.label);
    $("methods").append(label);
  }
  $("envPath").textContent = opts.env_path || ".env";
  // A saved string nobody reads would be reported as saved and change nothing.
  $("saveRow").hidden = opts.save === false;
  $("saveOff").hidden = opts.save !== false;
  showFields();
}

// Only the monitor answers /api/status, so its first 200 means the handover
// is done; a network error is the handover still in progress.
function waitForMonitor() {
  fetch("/api/status" + q)
    .then((r) => (r.ok ? location.assign("/" + q) : setTimeout(waitForMonitor, 250)))
    .catch(() => setTimeout(waitForMonitor, 250));
}

function submit(e) {
  e.preventDefault();
  const m = method();
  const f = new Set(m.fields || []);
  const server = $("server").value;
  const body = {
    server: server,
    auth: m.id,
    login: f.has("login") ? $("login").value : "",
    password: f.has("password") ? $("password").value : "",
    database: $("database").value,
    encrypt: $("encrypt").value,
    trust: $("encrypt").value === "true" && $("trust").checked,
    save_env: !$("saveRow").hidden && $("saveEnv").checked,
  };
  say("error", "");
  say("hint", "");
  // Without a port, a named instance goes through SQL Server Browser, which
  // takes the attempt's whole deadline to fail when a firewall drops it.
  say("status", server.includes("\\") && !server.includes(",")
    ? "resolving the instance through SQL Server Browser; if it does not answer, this fails after 30 seconds"
    : "connecting");
  $("go").disabled = true;
  fetch("/api/connect" + q, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
    .then((r) => r.json().then((j) => (r.ok ? j : Promise.reject(j))))
    .then((j) => {
      say("status", "connected to " + j.dsn + (j.env_written ? ", saved to " + j.env_written : ""));
      if (j.env_error) say("error", "could not write the .env file: " + j.env_error);
      waitForMonitor();
    })
    .catch((j) => {
      $("go").disabled = false;
      say("status", "");
      say("error", (j && (j.error || j.message)) || String(j));
      say("hint", (j && j.hint) || "");
    });
}

fetch("/api/connect" + q)
  .then((r) => r.json())
  .then(setup)
  .catch((e) => say("error", "could not load the form: " + e.message));
$("connectForm").addEventListener("submit", submit);
$("encrypt").addEventListener("change", showFields);
```

Append to `internal/web/assets/style.css`:

```css
/* The connect page: one narrow column of labelled fields. */
.connect { flex: 1 1 auto; overflow: auto; padding: 14px; max-width: 36rem; }
.connect label { display: block; margin: 10px 0; }
.connect fieldset { border: 1px solid var(--line); border-radius: 3px; margin: 10px 0; }
.connect fieldset label { display: inline-block; margin: 4px 14px 4px 0; }
.connect input[type="text"], .connect input[type="password"], .connect select {
  display: block;
  width: 100%;
  margin-top: 3px;
  background: #1b1f26;
  border: 1px solid var(--line);
  border-radius: 2px;
  color: var(--fg);
  font: inherit;
  padding: 3px 6px;
}
.connect .note { color: var(--dim); font-size: 12px; margin: 4px 0; }
.connect button {
  background: #1c1f26;
  color: inherit;
  border: 1px solid #2a2e37;
  border-radius: 3px;
  padding: 3px 12px;
  font: inherit;
  cursor: pointer;
}
```

- [ ] Step 6: Run the tests to watch them pass

Run: `go test ./internal/web -run 'TestConnectPhase|TestConnectRefuses|TestSecondAttempt|TestConnectError|TestAttemptHasADeadline|TestHandover|TestAskConnection' -count=1 -v 2>&1 | grep -c '^--- PASS'`
Expected: `9`. Then the same filter under the race detector, several times, since the tests share `shutdownGrace` and a phase or a monitor outliving its test is exactly what it catches: `go test -race ./internal/web -run '<the same filter>' -count=5` says `ok`. Then the whole package: `go test ./internal/web -count=1` says `ok`, and `deno lint internal/web/assets/connect.js` is clean.

- [ ] Step 7: Break it and watch the right tests fail

1. Replace `handoff{l.ln}` with `l.ln`. Expected: `TestHandoverAnswersARequestSentBetweenTheTwoServers` fails at once, because `Shutdown` really closes the socket and `AskConnection` returns the error of `SetDeadline` on it (`use of closed network connection`), before any request is queued.
2. Replace `gracefulShutdown(srv)` with `srv.Shutdown(context.Background())`. Expected: `TestHandoverIsNotHeldByAnIdleConnection` fails at about five seconds.
3. Replace `if !p.busy.TryLock() {` with `p.busy.Lock(); if false {`. Expected: `TestSecondAttemptWhileOneRunsIsRefusedAtOnce` fails after five seconds, the second POST blocked behind the first until the client times out; it never reaches the elapsed-time check.
4. Replace `ctx, cancel := context.WithTimeout(req.Context(), connectTimeout)` with `ctx, cancel := context.WithCancel(req.Context())`. Expected: `TestAttemptHasADeadline` fails when the client gives up at five seconds.
5. Replace `securityHeaders(requireToken(l.token, mux))` with `requireToken(l.token, mux)`. Expected: `TestConnectPhaseServesThePageAndTheOptions` fails on the headers.

- [ ] Step 8: Gates and commit

```bash
gofmt -l . ; go vet ./... && go test ./internal/web -count=1 && deno lint internal/web/assets/app.js internal/web/assets/connect.js
git add internal/web/connect.go internal/web/connect_test.go internal/web/server.go internal/web/assets/connect.html internal/web/assets/connect.js internal/web/assets/style.css
git commit -F - <<'EOF'
Serve a connect page until one attempt succeeds, then hand the socket over

Without a connection string the tool used to exit. It now serves a form on
the address it prints, with the same token check, Host check and headers as
the monitor, and stops as soon as one attempt connects. It stops through
gracefulShutdown on a listener whose Close only sets a deadline, so the
socket never closes: a request the browser sends during the handover waits
in the kernel's queue and the monitor answers it.

The grace matters more than it looks. A browser leaves speculative
connections idle, and net/http waits five seconds on each before a plain
Shutdown returns. One attempt runs at a time; a second is told so at once
rather than queued behind a thirty-second login.
EOF
```

---

### Task 6: The connect page in a real browser

Spec section 9, the browser test. The Go side of Task 5 cannot see whether the page shows the right fields or lands on the monitor.

Files:
- Create: `internal/web/connect_e2e_test.go`
- Create: `internal/web/testdata/connect-driver.js`
- Modify: `internal/web/e2e_test.go` (move the chromium launch into `launchChromium`)
- Modify: `internal/web/app_assets_test.go` (lint `connect.js` too)

Interfaces:
- Consumes: `Listen`, `AskConnection`, `NewServerOn`, `ConnectResult`, `ConnectError` and the page ids listed in Task 5.
- Produces: `func launchChromium(t *testing.T) (deno, port string)`.

- [ ] Step 1: Move the chromium launch into a helper

In `internal/web/e2e_test.go`, the first forty lines of `TestEndToEndInABrowser` (from `chrome := lookChromium()` through the `devToolsPort` call and its error check) move into this function, verbatim apart from returning instead of declaring. `TestEndToEndInABrowser` afterwards uses only `deno` and `port` from that block, which was checked.

```go
// launchChromium starts a headless chromium for one test and returns deno's
// path and chromium's DevTools port, or skips when either is missing.
func launchChromium(t *testing.T) (deno, port string) {
	t.Helper()
	chrome := lookChromium()
	if chrome == "" {
		t.Skip("no chromium-browser or chromium on PATH; this test drives the real page in a real browser")
	}
	deno = findDeno()
	if deno == "" {
		t.Skip("deno not installed; the DevTools protocol needs a WebSocket and Go's standard library has none")
	}
	// Not t.TempDir: chromium goes on writing into its profile while it
	// shuts down, and the framework's cleanup runs into a directory that
	// is not empty yet and fails the test over nothing. This one is
	// removed after the process is confirmed gone, and a leftover
	// directory in the system temp is not worth failing a test for.
	profile, err := os.MkdirTemp("", "sqltop-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome,
		"--headless", "--disable-gpu", "--no-sandbox",
		"--window-size=1600,1000",
		"--remote-debugging-port=0",
		"--user-data-dir="+profile,
		"about:blank")
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start %s: %v", chrome, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.RemoveAll(profile)
	})
	port, err = devToolsPort(profile, 30*time.Second)
	if err != nil {
		t.Fatalf("%v; chromium did not report a debugging port", err)
	}
	return deno, port
}
```

`TestEndToEndInABrowser` then begins:

```go
func TestEndToEndInABrowser(t *testing.T) {
	deno, port := launchChromium(t)

	srv, snapDir, stop := browserTestServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
```

and continues exactly as before from `defer cancel()`. Run `go test ./internal/web -run TestEndToEndInABrowser -count=1` now and see `ok` before writing anything new: this step must change nothing.

- [ ] Step 2: Write the driver

Create `internal/web/testdata/connect-driver.js`:

```js
// Drives the connect page over the Chrome DevTools Protocol and prints one
// JSON object of observations. connect_e2e_test.go does the asserting.
//
//   deno run --allow-net connect-driver.js <pageURL> <cdpPort>
"use strict";

const pageURL = Deno.args[0];
const cdpPort = Deno.args[1];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const target = await (await fetch(
  `http://127.0.0.1:${cdpPort}/json/new?${encodeURIComponent(pageURL)}`,
  { method: "PUT" },
)).json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.addEventListener("open", resolve);
  ws.addEventListener("error", reject);
});

let id = 0;
const pending = new Map();
const problems = [];
ws.addEventListener("message", (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) {
    pending.get(m.id)(m);
    pending.delete(m.id);
  }
  if (m.method === "Runtime.exceptionThrown") {
    problems.push("exception: " + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
  }
  // The 502 of the refused attempt and the 404s of the handover poll are
  // expected, and the browser logs both as errors.
  if (m.method === "Log.entryAdded" && m.params.entry.level === "error" && !(m.params.entry.url || "").includes("/api/")) {
    problems.push("log: " + m.params.entry.text + " " + (m.params.entry.url || ""));
  }
});
const send = (method, params) => new Promise((resolve) => {
  const n = ++id;
  pending.set(n, resolve);
  ws.send(JSON.stringify({ id: n, method, params: params || {} }));
});

await send("Runtime.enable");
await send("Log.enable");
await send("Page.enable");
await send("Page.navigate", { url: pageURL });

const ev = async (expr) => {
  const r = await send("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.result.exceptionDetails) throw new Error("evaluate failed: " + JSON.stringify(r.result.exceptionDetails));
  return r.result.result.value;
};
const json = async (expr) => JSON.parse(await ev("JSON.stringify(" + expr + ")"));
const waitFor = async (expr, ms) => {
  for (let i = 0; i < ms / 100; i++) {
    try {
      if (await ev(expr)) return true;
    } catch { /* between two documents */ }
    await sleep(100);
  }
  return false;
};

const out = { problems };
await waitFor(`document.querySelectorAll('input[name="auth"]').length > 0`, 10000);
out.methods = await json(`[...document.querySelectorAll('input[name="auth"]')].map((r) => r.value)`);

out.fields = {};
for (const m of out.methods) {
  await ev(`(() => { const r = document.querySelector('input[name="auth"][value="${m}"]'); r.checked = true; r.dispatchEvent(new Event("change")); })()`);
  out.fields[m] = await json(`({
    login: !document.getElementById("loginRow").hidden,
    password: !document.getElementById("passwordRow").hidden,
    note: document.getElementById("methodNote").hidden ? "" : document.getElementById("methodNote").textContent,
  })`);
}

out.trust = {};
for (const v of ["", "true", "strict"]) {
  await ev(`(() => { const s = document.getElementById("encrypt"); s.value = ${JSON.stringify(v)}; s.dispatchEvent(new Event("change")); })()`);
  out.trust[v || "default"] = await ev(`!document.getElementById("trustRow").hidden`);
}
await ev(`(() => { const s = document.getElementById("encrypt"); s.value = ""; s.dispatchEvent(new Event("change")); })()`);
await ev(`(() => { const r = document.querySelector('input[name="auth"][value="sql"]'); r.checked = true; r.dispatchEvent(new Event("change")); })()`);

const submit = (server) => ev(`(() => {
  document.getElementById("server").value = ${JSON.stringify(server)};
  document.getElementById("login").value = "dba";
  document.getElementById("password").value = "p@ss";
  document.getElementById("connectForm").requestSubmit();
})()`);

await submit("nope");
await waitFor(`!document.getElementById("error").hidden`, 5000);
out.failure = await json(`({
  error: document.getElementById("error").textContent,
  hint: document.getElementById("hint").textContent,
  enabled: !document.getElementById("go").disabled,
})`);
out.env = await json(`({
  path: document.getElementById("envPath").textContent,
  note: !document.getElementById("saveNote").hidden,
})`);

// The successful attempt also carries the two boxes a DBA ticks, so the
// test sees them reach the server rather than only appear on screen.
await ev(`(() => {
  const s = document.getElementById("encrypt");
  s.value = "true";
  s.dispatchEvent(new Event("change"));
  document.getElementById("trust").checked = true;
  document.getElementById("saveEnv").checked = true;
})()`);
await submit("db01");
out.landed = await waitFor(
  `document.getElementById("gridBody") !== null && [...document.querySelectorAll("#gridBody tr")].some((r) => r.children.length > 1 && !r.hidden)`,
  15000,
);

console.log(JSON.stringify(out));
ws.close();
Deno.exit(0);
```

- [ ] Step 3: Write the test

Create `internal/web/connect_e2e_test.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/source/fake"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

// TestConnectPageInABrowser drives the connect page the way a DBA would:
// each method shows its own fields, a refused attempt says why, and a
// successful one lands on the monitor in the same tab.
func TestConnectPageInABrowser(t *testing.T) {
	deno, port := launchChromium(t)

	l, err := Listen(config.Server{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	var mu sync.Mutex
	var last []byte
	connect := func(_ context.Context, body []byte) (ConnectResult, error) {
		mu.Lock()
		last = body
		mu.Unlock()
		var p struct {
			Server string `json:"server"`
		}
		_ = json.Unmarshal(body, &p)
		if p.Server != "db01" {
			return ConnectResult{}, &ConnectError{Status: http.StatusBadGateway, Message: "no such server", Hint: "check the name"}
		}
		return ConnectResult{DSN: "sqlserver://dba:xxxxx@db01"}, nil
	}
	options := map[string]any{
		"methods": []map[string]any{
			{"id": "sql", "label": "SQL Server login", "fields": []string{"login", "password"}},
			{"id": "windows", "label": "Windows, current account", "fields": []string{}},
			{"id": "domain", "label": "Windows, domain account", "fields": []string{"login", "password"}, "note": "Sent as NTLM."},
		},
		"env_path": "/somewhere/.env",
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	// Serve's shutdown reads shutdownGrace, which other tests write: it must
	// be over before this test is.
	defer func() {
		cancel()
		<-served
	}()
	go func() {
		defer close(served)
		if err := l.AskConnection(ctx, options, connect); err != nil {
			return // the landed assertion below says what went wrong
		}
		rows := []model.RequestSample{{
			At: time.Now(), Ref: model.RequestRef{SessionID: 51}, Status: "running",
			Database: "alpha", Login: "svc", Program: "sqltop e2e", Command: "SELECT", SQLText: "SELECT 1",
		}}
		w := window.New(time.Minute, 1000)
		c := collector.New(fake.New(rows), w, collector.NewBudget(50, testTiers()))
		go c.Run(ctx)
		_ = NewServerOn(c, w, l).Serve(ctx)
	}()

	runCtx, runCancel := context.WithTimeout(ctx, 90*time.Second)
	defer runCancel()
	driver := filepath.Join("testdata", "connect-driver.js")
	out, err := exec.CommandContext(runCtx, deno, "run", "--quiet", "--allow-net=127.0.0.1", driver, l.URL(), port).CombinedOutput()
	if err != nil {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}

	var got struct {
		Problems []string `json:"problems"`
		Methods  []string `json:"methods"`
		Fields   map[string]struct {
			Login, Password bool
			Note            string
		} `json:"fields"`
		Trust   map[string]bool `json:"trust"`
		Failure struct {
			Error, Hint string
			Enabled     bool
		} `json:"failure"`
		Env struct {
			Path string
			Note bool
		} `json:"env"`
		Landed bool `json:"landed"`
	}
	if err := json.Unmarshal([]byte(lastJSONLine(string(out))), &got); err != nil {
		t.Fatalf("could not read the driver's report: %v\n%s", err, out)
	}

	if len(got.Problems) > 0 {
		t.Errorf("the browser reported: %s", strings.Join(got.Problems, "; "))
	}
	if strings.Join(got.Methods, " ") != "sql windows domain" {
		t.Errorf("the page offers %v, want the three methods the server sent, in order", got.Methods)
	}
	for m, want := range map[string]struct {
		login, password bool
		note            string
	}{
		"sql":     {true, true, ""},
		"windows": {false, false, ""},
		"domain":  {true, true, "Sent as NTLM."},
	} {
		f := got.Fields[m]
		if f.Login != want.login || f.Password != want.password || f.Note != want.note {
			t.Errorf("%s shows login=%v password=%v note=%q, want %v %v %q", m, f.Login, f.Password, f.Note, want.login, want.password, want.note)
		}
	}
	if got.Trust["default"] || !got.Trust["true"] || got.Trust["strict"] {
		t.Errorf("the trust box shows as %v; only under required", got.Trust)
	}
	if got.Failure.Error != "no such server" || got.Failure.Hint != "check the name" || !got.Failure.Enabled {
		t.Errorf("a refused attempt shows %+v", got.Failure)
	}
	if got.Env.Path != "/somewhere/.env" || !got.Env.Note {
		t.Errorf("the save box names %q with the clear-text note shown=%v; it must name the server's path and warn for a method with a password", got.Env.Path, got.Env.Note)
	}
	if !got.Landed {
		t.Error("a successful attempt did not land on the monitor with rows")
	}

	mu.Lock()
	defer mu.Unlock()
	var posted map[string]any
	if err := json.Unmarshal(last, &posted); err != nil {
		t.Fatalf("the last POST was not JSON: %s", last)
	}
	for k, v := range map[string]any{"server": "db01", "auth": "sql", "login": "dba", "password": "p@ss", "encrypt": "true", "trust": true, "save_env": true} {
		if posted[k] != v {
			t.Errorf("the page posted %s=%v, want %v; the whole body is %s", k, posted[k], v, last)
		}
	}
}
```

- [ ] Step 4: Lint the new script in the gate

In `internal/web/app_assets_test.go`, `TestShippedJavaScriptPassesTheLinter` runs `exec.Command(deno, "lint", "assets/app.js", "assets/connect.js")`, its failure message names both files, and its skip message says `deno lint internal/web/assets/app.js internal/web/assets/connect.js`.

- [ ] Step 5: Run the tests

Run: `go test ./internal/web -run 'TestConnectPageInABrowser|TestEndToEndInABrowser|TestShippedJavaScript' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: three `--- PASS`. A `--- SKIP` means chromium or deno is missing and nothing was tested; say so rather than report success.

- [ ] Step 6: Break it and watch the right assertions fail

One at a time, each in `connect.js`, restoring from a copy:

1. In `showFields`, set `$("passwordRow").hidden = false`. Expected: the `windows` fields assertion fails.
2. In `waitForMonitor`, replace `location.assign("/" + q)` with `void 0`, which keeps the script valid. Expected: `landed` fails.
3. In the `.catch` of `submit`, drop the `say("hint", ...)` line. Expected: the failure assertion fails on the hint.
4. In `submit`, send `password` whatever the method. This one is not caught when the last POST is a `sql` attempt; write that down as the breakage the test does not see, which is the expected answer.
5. In `submit`, send `trust: false` always. Expected: the posted-body assertion fails on `trust`.
6. In `submit`, send `save_env: false` always. Expected: the posted-body assertion fails on `save_env`.

- [ ] Step 7: Gates and commit

```bash
gofmt -l . ; go vet ./... && go test ./internal/web -count=1 && deno lint internal/web/assets/app.js internal/web/assets/connect.js
git add internal/web/connect_e2e_test.go internal/web/testdata/connect-driver.js internal/web/e2e_test.go internal/web/app_assets_test.go
git commit -F - <<'EOF'
Drive the connect page in a real browser

The Go tests of the connect phase see the routes and the socket, not the
page: whether each method shows its own fields, whether a refused attempt
says why, whether a success lands on the monitor in the same tab. Those are
the things a DBA meets first, and only a browser answers them. The chromium
launch moves into a helper now that two tests need it, and the linter gate
covers the new script.
EOF
```

---

### Task 7: Wire it into `main`

Spec section 3.1.

Files:
- Create: `cmd/sqltop/connect.go`
- Create: `cmd/sqltop/connect_test.go`
- Modify: `cmd/sqltop/main.go`

Interfaces:
- Consumes: `mssql.ConnParams`, `mssql.BuildDSN`, `mssql.AuthMethods`, `mssql.Redacted`, `mssql.Hint` (Tasks 2 and 3); `dotenv.Set` (Task 1); `web.Listen`, `web.NewServerOn`, `(*web.Listener).AskConnection`, `web.ConnectFunc`, `web.ConnectResult`, `web.ConnectError` (Tasks 4 and 5).
- Produces: `func attempt(envPath string, save, capture bool, opened **mssql.Source) web.ConnectFunc`, `func connectOptions(envPath string, save bool) map[string]any`, `func saveReachesTheConnection(instances []config.Instance) bool`, `func openBrowser(url string, disabled bool)`.

- [ ] Step 1: Write the failing tests

Create `cmd/sqltop/connect_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
	"github.com/rudi-bruchez/sqltop/internal/source/mssql"
	"github.com/rudi-bruchez/sqltop/internal/web"
)

// TestSaveReachesTheConnection: a string saved to .env is read on the next
// run only when no configured instance supplies its own.
func TestSaveReachesTheConnection(t *testing.T) {
	for _, c := range []struct {
		instances []config.Instance
		want      bool
	}{
		{nil, true},
		{[]config.Instance{{Name: "a", DSN: ""}}, true},
		{[]config.Instance{{Name: "a", DSN: "${SQLTOP_CONN}"}}, true},
		{[]config.Instance{{Name: "a", DSN: "${OTHER_CONN}"}}, false},
	} {
		if got := saveReachesTheConnection(c.instances); got != c.want {
			t.Errorf("%+v: %v, want %v", c.instances, got, c.want)
		}
	}
}

func TestAttemptRefusesWhatCannotBeAConnectionString(t *testing.T) {
	var opened *mssql.Source
	f := attempt(filepath.Join(t.TempDir(), ".env"), true, false, &opened)
	for _, body := range []string{`{`, `{"server":"","auth":"sql","login":"sa"}`} {
		_, err := f(context.Background(), []byte(body))
		var ce *web.ConnectError
		if !errors.As(err, &ce) || ce.Status != http.StatusBadRequest {
			t.Errorf("%s gave %v, want a 400", body, err)
		}
	}
	if opened != nil {
		t.Error("a refused form left a source behind")
	}
}

// TestAttemptReportsAFailedConnectionWithItsHint needs no server: nothing
// answers SQL Server Browser on the loopback address, and the driver says so
// at once.
func TestAttemptReportsAFailedConnectionWithItsHint(t *testing.T) {
	var opened *mssql.Source
	f := attempt(filepath.Join(t.TempDir(), ".env"), true, false, &opened)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := f(ctx, []byte(`{"server":"127.0.0.1\\NOPE","auth":"sql","login":"sa","password":"x"}`))
	var ce *web.ConnectError
	if !errors.As(err, &ce) || ce.Status != http.StatusBadGateway {
		t.Fatalf("got %v, want a 502", err)
	}
	if !strings.Contains(ce.Message, "no instance matching") || ce.Hint == "" {
		t.Errorf("message %q hint %q", ce.Message, ce.Hint)
	}
	if opened != nil {
		t.Error("a failed attempt left a source behind")
	}
}

func TestAttemptOpensTheContainerAndSavesTheString(t *testing.T) {
	dsn := os.Getenv("SQLTOP_TEST_DSN")
	if dsn == "" {
		if os.Getenv("SQLTOP_REQUIRE_DB") != "" {
			t.Fatal("SQLTOP_TEST_DSN is unset and SQLTOP_REQUIRE_DB is set; run: eval \"$(scripts/testdb.sh)\"")
		}
		t.Skip("SQLTOP_TEST_DSN is unset; run: eval \"$(scripts/testdb.sh)\"")
	}
	cfg, err := msdsn.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	server := cfg.Host
	if cfg.Port != 0 {
		server = fmt.Sprintf("%s,%d", cfg.Host, cfg.Port)
	}
	body, _ := json.Marshal(map[string]any{
		"server": server, "auth": "sql",
		"login": cfg.User, "password": cfg.Password, "save_env": true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// With saving pointless, a ticked box writes nothing.
	off := filepath.Join(t.TempDir(), ".env")
	var discarded *mssql.Source
	res, err := attempt(off, false, false, &discarded)(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	discarded.Close()
	if res.EnvWritten != "" {
		t.Errorf("saving was off and the attempt reports writing %q", res.EnvWritten)
	}
	if _, err := os.Stat(off); !os.IsNotExist(err) {
		t.Errorf("saving was off and %s exists (%v)", off, err)
	}

	env := filepath.Join(t.TempDir(), ".env")
	var opened *mssql.Source
	res, err = attempt(env, true, false, &opened)(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	if opened == nil {
		t.Fatal("a successful attempt did not hand its source over")
	}
	defer opened.Close()
	if strings.Contains(res.DSN, cfg.Password) {
		t.Errorf("the page is shown %q, which carries the password", res.DSN)
	}
	if res.EnvWritten != env || res.EnvError != "" {
		t.Errorf("written=%q error=%q", res.EnvWritten, res.EnvError)
	}
	t.Setenv("SQLTOP_CONN", "")
	os.Unsetenv("SQLTOP_CONN")
	if _, err := dotenv.Load(env); err != nil {
		t.Fatal(err)
	}
	saved, err := msdsn.Parse(os.Getenv("SQLTOP_CONN"))
	if err != nil {
		t.Fatalf("the saved string does not parse: %v", err)
	}
	if saved.User != cfg.User || saved.Password != cfg.Password || saved.Port != cfg.Port {
		t.Errorf("the saved string reads back as %s@%s:%d", saved.User, saved.Host, saved.Port)
	}
}
```

- [ ] Step 2: Run them to watch them fail

Run: `go test ./cmd/sqltop -count=1`
Expected: a compile failure, `undefined: attempt`.

- [ ] Step 3: Implement the attempt

Create `cmd/sqltop/connect.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
	"github.com/rudi-bruchez/sqltop/internal/source/mssql"
	"github.com/rudi-bruchez/sqltop/internal/web"
)

// connectRequest is what the connect page posts.
type connectRequest struct {
	mssql.ConnParams
	SaveEnv bool `json:"save_env"`
}

// connectOptions is what the page may offer on this platform.
func connectOptions(envPath string, save bool) map[string]any {
	return map[string]any{"methods": mssql.AuthMethods(), "env_path": envPath, "save": save}
}

// saveReachesTheConnection reports whether a SQLTOP_CONN saved to .env is
// read on the next run: not when sqltop.yaml names an instance whose
// connection string does not mention it, which would make the page report a
// save that changes nothing.
func saveReachesTheConnection(instances []config.Instance) bool {
	return len(instances) == 0 || instances[0].DSN == "" || strings.Contains(instances[0].DSN, "SQLTOP_CONN")
}

// attempt is what the connect page calls for each try. The source of the
// try that succeeds is left in *opened for main to go on with, so the
// server sees one login rather than two.
func attempt(envPath string, save, capture bool, opened **mssql.Source) web.ConnectFunc {
	return func(ctx context.Context, body []byte) (web.ConnectResult, error) {
		var req connectRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return web.ConnectResult{}, &web.ConnectError{Status: http.StatusBadRequest, Message: "unreadable form: " + err.Error()}
		}
		dsn, err := mssql.BuildDSN(req.ConnParams)
		if err != nil {
			return web.ConnectResult{}, &web.ConnectError{Status: http.StatusBadRequest, Message: err.Error()}
		}
		src := mssql.New()
		src.AllowCapture(capture)
		if err := src.Open(ctx, dsn); err != nil {
			return web.ConnectResult{}, &web.ConnectError{Status: http.StatusBadGateway, Message: err.Error(), Hint: mssql.Hint(err)}
		}
		*opened = src
		res := web.ConnectResult{DSN: mssql.Redacted(dsn)}
		log.Printf("connected to %s", res.DSN)
		if req.SaveEnv && save {
			if err := dotenv.Set(envPath, "SQLTOP_CONN", dsn); err != nil {
				res.EnvError = err.Error()
			} else {
				res.EnvWritten = envPath
				log.Printf("wrote SQLTOP_CONN to %s", envPath)
			}
		}
		return res, nil
	}
}

// openBrowser opens url unless -no-browser was given. The interface is the
// tool and a URL with a token in it is not something anybody enjoys
// retyping. A failure is a line in the log and nothing more: most machines
// a DBA logs into have no desktop at all, and refusing to run there would
// be worse than printing the address and letting them paste it.
func openBrowser(url string, disabled bool) {
	if disabled {
		return
	}
	if name, err := web.OpenBrowser(url); err != nil {
		log.Printf("could not open a browser with %s (%v); the address above is what to paste, or pass -no-browser to stop trying", name, err)
	}
}
```

- [ ] Step 4: Rewire `main`

In `cmd/sqltop/main.go`, add `"path/filepath"` to the imports. Replace everything from `dsn := os.Getenv("SQLTOP_CONN")` to the end of `main` with the following. The block between `defer src.Close()` and the collector, the sweep, is unchanged and is shown whole so nothing is lost; the only other lines that move are the browser opening, now `openBrowser`, and the server construction, now on the listener.

```go
	// The page offers to write here, so it names the file it will write.
	envFile, err := filepath.Abs(*envPath)
	if err != nil {
		envFile = *envPath
	}

	dsn := os.Getenv("SQLTOP_CONN")
	if len(cfg.Instances) > 0 && cfg.Instances[0].DSN != "" {
		dsn = os.ExpandEnv(cfg.Instances[0].DSN)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Bound first, so that the connect page and the monitor serve one after
	// the other on one address and one token.
	l, err := web.Listen(cfg.Server)
	if err != nil {
		log.Fatal(err)
	}

	var src *mssql.Source
	if dsn != "" {
		src = mssql.New()
		src.AllowCapture(*capture)
		if err := src.Open(ctx, dsn); err != nil {
			l.Close()
			if ctx.Err() != nil {
				return // Ctrl-C while connecting is a request to stop, not a failure
			}
			log.Fatal(err)
		}
	} else {
		log.Printf("no instance configured; connect from %s", l.URL())
		openBrowser(l.URL(), *noBrowser)
		save := saveReachesTheConnection(cfg.Instances)
		if err := l.AskConnection(ctx, connectOptions(envFile, save), attempt(envFile, save, *capture, &src)); err != nil {
			if src != nil {
				src.Close()
			}
			l.Close()
			if ctx.Err() != nil {
				return
			}
			log.Fatal(err)
		}
	}
	defer src.Close()

	// The recovery sweep is behind the flag like everything else, because it
	// is itself a DROP and a server whose operator never asked for captures
	// must see nothing created and nothing removed. It runs here, before the
	// collector takes the connection, so it is over before anything else
	// competes for it, and it says what it dropped: a tool that quietly
	// deletes objects on somebody's server is worse than one that does not.
	if *capture {
		switch n, err := src.SweepCaptures(ctx); {
		case err != nil:
			log.Printf("warning: could not look for abandoned capture sessions: %v", err)
		case n > 0:
			log.Printf("dropped %d abandoned capture session(s) left behind by an earlier run", n)
		}
	}

	win := window.New(cfg.Retention.Std(), cfg.Budget.MaxSamples)
	col := collector.New(src, win, collector.NewBudget(cfg.Budget.ServerCPUMsPerSecond, cfg.Tiers))
	// colDone closes once col.Run actually returns, not merely once ctx is
	// cancelled: the wait below on it is what keeps the deferred src.Close
	// above from firing while a tier goroutine is still mid-query against
	// that same connection. col.Run's own error is only logged, not fatal:
	// by the time it returns, ctx is already cancelled and shutdown is
	// already under way, so there is nothing left to abort.
	colDone := make(chan struct{})
	go func() {
		defer close(colDone)
		if err := col.Run(ctx); err != nil {
			log.Printf("collector stopped: %v", err)
		}
	}()

	srv := web.NewServerOn(col, win, l).WithConfig(cfg)
	// The token in this URL is exactly what URL's own doc comment names as
	// its cost: printing it here sends it to stderr, and from there to
	// whatever captures this process's output, journald, a CI log, a
	// terminal scrollback, with whatever permissions that carries. Accepted
	// for the same reason URL accepts putting it in the address at all;
	// see that comment for the full reasoning rather than repeating it here.
	log.Printf("sqltop on %s", srv.URL())
	// The connect page already opened the browser, and its tab becomes the
	// monitor by itself.
	if dsn != "" {
		openBrowser(srv.URL(), *noBrowser)
	}
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
	<-colDone
}
```

The one comment rewritten on the way is `colDone`'s, which carried a task reference ("fix round 1, task 14") the project's comment rules forbid.

- [ ] Step 5: Run the tests, with the container, in one invocation

Run: `eval "$(scripts/testdb.sh)" && go test ./cmd/sqltop -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: four `--- PASS`, none skipped.

- [ ] Step 6: Run the binary end to end against the container

The container is the one `scripts/testdb.sh` starts: `127.0.0.1`, port `11433`, login `sa`, password `Sqltop_dev_2026!` (the `%21` in `SQLTOP_TEST_DSN` is the `!`). Everything below runs in one Bash invocation, so the state survives. The explicit `--config` keeps a `sqltop.yaml` elsewhere on the machine from supplying an instance, and port 18420 keeps clear of a sqltop already running on 8420.

```bash
eval "$(scripts/testdb.sh)"
T=$(mktemp -d)
CGO_ENABLED=0 go build -o "$T/sqltop" ./cmd/sqltop
printf 'server:\n  port: 18420\n' > "$T/sqltop.yaml"
env -u SQLTOP_CONN "$T/sqltop" --no-browser --config "$T/sqltop.yaml" --env "$T/.env" > "$T/log" 2>&1 &
PID=$!
for i in $(seq 1 40); do grep -q 'connect from' "$T/log" && break; sleep 0.25; done
URL=$(grep -o 'http://[^ ]*' "$T/log" | head -1); BASE=${URL%%/?t=*}; TOK=${URL##*t=}
echo "== options"; curl -s "$BASE/api/connect?t=$TOK"; echo
echo "== attempt"
curl -s -X POST "$BASE/api/connect?t=$TOK" -H 'Content-Type: application/json' \
  -d '{"server":"127.0.0.1,11433","auth":"sql","login":"sa","password":"Sqltop_dev_2026!","save_env":true}'; echo
for i in $(seq 1 40); do curl -sf "$BASE/api/status?t=$TOK" > "$T/status.json" && break; sleep 0.25; done
echo "== status"; grep -o '"connected":[a-z]*' "$T/status.json"
echo "== .env"; cat "$T/.env"; stat -c '%a' "$T/.env"
echo "== log"; grep -E 'no instance configured|connected to|wrote SQLTOP_CONN|sqltop on' "$T/log"
echo "== password in the log: $(grep -c 'Sqltop_dev_2026' "$T/log")"
kill "$PID"; wait "$PID" 2>/dev/null
env -u SQLTOP_CONN "$T/sqltop" --no-browser --config "$T/sqltop.yaml" --env "$T/.env" > "$T/log2" 2>&1 &
PID=$!
for i in $(seq 1 40); do grep -q 'sqltop on' "$T/log2" && break; sleep 0.25; done
echo "== second run asks for a connection: $(grep -c 'no instance configured' "$T/log2")"
kill "$PID"; wait "$PID" 2>/dev/null
rm -rf "$T"
```

Expected, in order: the options JSON listing `sql` and `domain` and the absolute `env_path`; a body whose `dsn` shows `xxxxx` and no password; `"connected":true`; a `.env` holding one `SQLTOP_CONN=sqlserver://...` line; mode `600`; the four log lines; `password in the log: 0`; `second run asks for a connection: 0`. Stop the process by the PID the shell recorded, never with `pkill`: `pkill -x sqltop` would stop any other sqltop on the machine, and `pkill -f` matches the shell running it.

- [ ] Step 7: Break it and watch the right tests fail

1. In `attempt`, set `*opened = src` before `Open` instead of after. Expected: `TestAttemptReportsAFailedConnectionWithItsHint` fails on the source left behind.
2. In `attempt`, save `res.DSN` to `.env` instead of `dsn`. Expected: `TestAttemptOpensTheContainerAndSavesTheString` fails, the saved password reading `xxxxx`.
3. In `attempt`, replace `req.SaveEnv && save` with `req.SaveEnv`. Expected: `TestAttemptOpensTheContainerAndSavesTheString` fails on "saving was off".
4. In `saveReachesTheConnection`, drop the `strings.Contains` clause. Expected: `TestSaveReachesTheConnection` fails on `${SQLTOP_CONN}`.

- [ ] Step 8: Gates and commit

```bash
gofmt -l . ; go vet ./... && eval "$(scripts/testdb.sh)" && go test ./... -count=1
git add cmd/sqltop/connect.go cmd/sqltop/connect_test.go cmd/sqltop/main.go
git commit -F - <<'EOF'
Open the connect page when there is no connection string

Starting without SQLTOP_CONN, or with an instance whose DSN expands to
nothing, used to end in one log line. It now binds, prints and opens the
usual address, serves the connect page there, and continues with the source
the successful attempt opened, so the server sees one login. A configured
string takes the old path, except that the socket is bound before the
server is contacted, so a busy port now fails first, with nothing sent.

The attempt writes the full string to .env when asked, and shows and logs
only the redacted form.
EOF
```

---

### Task 8: The documents

Spec section 11.

Files:
- Modify: `docs/SPECS.md` (sections 3, 3.3, 4.3, 13)
- Modify: `README.md`
- Modify: `.env.example`
- Modify: `CLAUDE.md` (the lint command)

Interfaces: none.

- [ ] Step 1: `docs/SPECS.md`

Section 3, the Auth row of the table, becomes:

```
| Auth | SQL authentication; Windows through the current account on Windows and through a domain account everywhere (NTLM outside Windows); Kerberos from Linux by a hand-written connection string only, see 3.3 | Microsoft Entra |
```

Section 3.3 is replaced whole by:

```markdown
### 3.3 Windows authentication from Linux

Two ways, with different reach.

A domain account and its password, sent as NTLM. go-mssqldb registers its
`ntlm` provider on every platform and uses it outside Windows whenever the
user name contains a backslash, `DOMAIN\user`. Pure Go, nothing to
configure, and refused wherever a domain policy restricts NTLM.

Kerberos, through the driver's `krb5` provider, which is pure Go: with
`CGO_ENABLED=0`, a program importing both the driver and the provider links
32 `gokrb5` packages, produces a statically linked binary, and cross-compiles
to `windows/amd64`, `darwin/arm64` and `linux/arm64` from a Linux host.
Building is all that was verified that way. Running it on Fedora 44 showed
three limits of the gokrb5 library underneath (v8.4.4, its latest release):

- the stock Fedora and RHEL `/etc/krb5.conf` carries
  `dns_canonicalize_hostname = fallback`, which gokrb5 rejects as an invalid
  boolean, failing the whole file before any ticket is read;
- it ignores `includedir`, so realms declared under `/etc/krb5.conf.d/` are
  invisible to it;
- it reads a credential cache only from a file, where those distributions
  default to `KEYRING:` or `KCM:`.

A hand-written connection string therefore works only with a krb5.conf
gokrb5 can parse, named with `krb5-configfile`, and a file cache named with
`krb5-credcachefile`, and with the server's fully qualified name:

    kinit -c FILE:/tmp/krb5cc_$(id -u) dba@CORP.EXAMPLE
    sqlserver://db01.corp.example:1433?authenticator=krb5&krb5-configfile=/home/dba/krb5-sqltop.conf&krb5-credcachefile=/tmp/krb5cc_1000

The connect page does not offer Kerberos, for these reasons
(`docs/specs/2026-09-11-connect-page-design.md` section 2). Against a real
domain it remains untested, see section 14.
```

Section 4.3 gains a last paragraph:

```markdown
Started without a connection string, neither `SQLTOP_CONN` nor an instance
in the configuration file, the tool does not exit: it serves a connect page
on the same address and token, and once a connection succeeds the monitor
takes the same socket over. `docs/specs/2026-09-11-connect-page-design.md`.
```

Section 13 gains a paragraph:

```markdown
Microsoft Entra authentication. It needs `github.com/microsoft/go-mssqldb/azuread`,
which pulls the Azure SDK (`azidentity` and its dependencies), the largest
dependency this project would take, and section 2.1 asks for a stated reason
before one comes in.
```

- [ ] Step 2: `README.md`

In "Running it", after the paragraph that ends "so a bookmarked URL will not work.", fix `8421` to `8420` in the sample line above it, and add:

```markdown
Started without a connection string, sqltop serves a connect page at that
address instead: a server field that takes what SSMS takes (`db01`,
`db01\SALES`, `db01,14330`), a login method, and a box to save the result to
`.env`. By hand, the string looks like this:

| Case | Connection string |
|---|---|
| default instance | `sqlserver://user:password@db01?database=master` |
| named instance, through SQL Server Browser | `sqlserver://user:password@db01/SALES` |
| explicit port | `sqlserver://user:password@db01:14330` |
| Windows domain account (NTLM outside Windows) | `sqlserver://CORP%5Cdba:password@db01` |
| current Windows account, on Windows | `sqlserver://db01` |
| Kerberos ticket | see `docs/SPECS.md` section 3.3 |

A password containing `@`, `:`, `/`, `#` or `%` must be percent-encoded; the
connect page does it for you. To reuse the string in `sqltop.yaml`, keep it
in `.env` and write `dsn: ${SQLTOP_CONN}` there, whole: a variable standing
for the password alone is inserted unescaped.
```

- [ ] Step 3: `.env.example` and `CLAUDE.md`

`.env.example` becomes exactly this, its whole content:

```
# Connection string for the instance sqltop opens by default. Left empty,
# sqltop serves a connect page that builds one and can save it here.
# Referenced from sqltop.yaml as ${SQLTOP_CONN}.
SQLTOP_CONN=

# DSN used by the integration tests. Set it with: eval "$(scripts/testdb.sh)"
SQLTOP_TEST_DSN=
```

`CLAUDE.md`, in "Before committing", `deno lint internal/web/assets/app.js` becomes `deno lint internal/web/assets/app.js internal/web/assets/connect.js`.

- [ ] Step 4: Check

Run: `grep -n 'Microsoft Entra' docs/SPECS.md` shows the Auth row and section 13 only; `grep -n '8421' README.md` shows nothing; `grep -c '—' docs/SPECS.md README.md .env.example` shows the same counts as before the edits (the new text adds none).

- [ ] Step 5: Commit

```bash
git add docs/SPECS.md README.md .env.example CLAUDE.md
git commit -F - <<'EOF'
Say what connecting actually supports, and how to do it by hand

The specification listed Microsoft Entra as supported and Kerberos from
Linux as verified. Entra was never linked, and the verification was a
build: run on Fedora, the Kerberos library rejects the stock krb5.conf,
ignores its includedir and cannot read the default ticket cache. The
specification now says what works, what a hand-written Kerberos string
needs, and that the new connect page leaves Kerberos out for those reasons.

The README gains the table of connection strings a DBA would otherwise have
to piece together from the driver's documentation, and the port it quotes
is the real one.
EOF
```

---

## Self-review

Spec coverage, section by section:

- 1 and 2 (what it does and does not do): Tasks 5 and 7; Kerberos and Entra absent by construction, documented in Task 8.
- 3.1 (flow, the two behaviour changes): Task 7, verified end to end in its Step 6.
- 3.2 (handover, `handoff`, `gracefulShutdown`, deadline cleared): Task 5, `TestHandoverAnswersARequestSentBetweenTheTwoServers` and `TestHandoverIsNotHeldByAnIdleConnection`.
- 3.3 (the page polls `/api/status`, retries network errors): Task 5 `connect.js` `waitForMonitor`; Task 6 `landed`; the 404 in `TestConnectPhaseServesThePageAndTheOptions`.
- 4.1 (server field grammar, the Browser wait message): Task 2 `splitServer` and its table; Task 5 `submit`'s status line.
- 4.2 (methods per platform, NTLM note, winsspi): Task 2 `authMethods`, `TestAuthMethodsFollowThePlatform`, `TestDomainLoginSelectsNTLMOutsideWindows`.
- 4.3 (folded fields, trust only under required): Task 2 encryption cases; Task 5 page; Task 6 `trust`.
- 5 (`BuildDSN`, form errors, empty SQL password, redaction, the `${SQLTOP_CONN}` advice): Task 2; the advice in Task 8.
- 6 (routes, statuses, `TryLock`, 30 s, the four hints, `internal/web` generic): Tasks 3 and 5.
- 7 (`Set`): Task 1.
- 8 (logging): Task 7 `attempt`, checked in Step 6's log grep.
- 9 (tests): distributed as listed there.
- 10 (unverifiable here): nothing to build; the named-instance success path, NTLM and winsspi against a domain stay open.
- 11 (documents): Task 8.

Placeholders: none. The icon's data URI is written out, with a `diff` to prove it matches the monitor's.

Security headers on the connect phase: asserted in `TestConnectPhaseServesThePageAndTheOptions`, and Task 5 breakage 5 removes them to see it fail.

Breakages: every "break it" step is written to compile, since a compile error is not a test failing, and each names the test and the reason that actually fires, as measured by the five readers who executed this plan.

Type consistency: `ConnParams`, `BuildDSN`, `AuthMethod`, `AuthMethods`, `Redacted`, `Hint`, `hintFor`, `Listener`, `Listen`, `NewServerOn`, `requireToken`, `ConnectFunc`, `ConnectResult`, `ConnectError`, `AskConnection`, `connectTimeout`, `attempt`, `connectOptions`, `saveReachesTheConnection`, `openBrowser` are each defined once and used with the same signature everywhere after.
