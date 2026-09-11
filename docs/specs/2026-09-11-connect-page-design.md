# Connecting without a connection string

Design for a page that builds the connection string when sqltop starts with
none, connects, and optionally writes the result to `.env` for next time.

The request behind it: a DBA should not have to know how a named instance or
Windows authentication is spelled in a `sqlserver://` URL to point this tool
at a server. Today the tool refuses to start without `SQLTOP_CONN` and says
so in one log line, which is correct and unhelpful.

## 1. What it does

When sqltop starts and no connection string resolves (neither `SQLTOP_CONN`
nor the first instance of `sqltop.yaml`), it does not exit. It opens the usual
URL, and that URL shows a form instead of the monitor. The form takes a
server, an authentication method and its fields, tests the connection by
opening it, and on success the same browser tab becomes the monitor, on the
same address, with the same token.

One checkbox, unchecked by default, writes the resulting `SQLTOP_CONN` line
to the `.env` file the tool read at startup, so the next run connects
directly.

## 2. What it does not do

- It is reachable only while the tool has no connection. Once connected,
  changing server means restarting. Switching instances from a running tool
  is `docs/SPECS.md` section 4.2, a separate piece of work with its own
  questions (the frozen windows, a capture in progress).
- No Microsoft Entra. The binary does not link it: that needs
  `github.com/microsoft/go-mssqldb/azuread`, which pulls the Azure SDK
  (`azidentity` and its dependencies), the largest dependency this project
  would ever take. Section 11 corrects `docs/SPECS.md`, which lists Entra as
  supported.
- No keytab. A keytab is how a service authenticates unattended, and this
  tool exists to be watched (`docs/SPECS.md` section 12). A hand-written
  connection string can still carry `krb5-keytabfile`.
- Nothing is written to `sqltop.yaml`. Secrets never go there
  (`docs/SPECS.md` section 8.3); the only file this page may write is `.env`,
  and only when asked.

## 3. Startup

### 3.1 The flow

1. `main` resolves the connection string exactly as today: `SQLTOP_CONN`,
   overridden by `cfg.Instances[0].DSN` after `os.ExpandEnv`.
2. The listening socket and the token are created first, by a new
   `web.Listen(cfg.Server)`. Today `web.NewServer` does both inside itself;
   they move out so that two servers can share them in turn.
3. With a connection string, nothing else changes: a source is opened, the
   capture sweep runs behind `-capture`, the collector starts, and a
   `web.Server` is built on the listener from step 2.
4. Without one, `main` logs `no instance configured; connect from <URL>`,
   opens the browser unless `-no-browser` is given, and calls
   `Listener.AskConnection`. That serves the connect page until one
   connection attempt succeeds, then returns. `main` then continues with step
   3 using the source the successful attempt opened, so the server sees one
   login, not two. The browser is not opened a second time.
5. Ctrl-C during the connect phase cancels the context; `AskConnection`
   returns `ctx.Err()` and `main` exits without opening anything.

Nothing is sent to the monitored server before an attempt, and a failed
attempt closes the source it created. The read-only rule of `docs/SPECS.md`
section 2 is unaffected: this phase runs no query of its own beyond the login
and the session initialisation `mssql.Source.Open` already performs.

### 3.2 Handing the socket over

The two phases serve on one `*net.TCPListener` so that the address, the port
and the token in the already-open tab stay valid, and so that no request is
ever refused between them: a connection arriving during the handover waits in
the kernel's accept queue and is accepted by whichever server calls `Accept`
next.

The obstacle is `http.Server.Shutdown`, which closes every listener it
served on. The connect phase therefore serves on a wrapper whose `Close` does
not close the socket but sets its accept deadline to now:

```go
type handoff struct{ *net.TCPListener }

func (h handoff) Close() error { return h.SetDeadline(time.Now()) }
```

`Shutdown` marks the server as shutting down, then calls `Close`; the blocked
`Accept` returns a timeout error; `http.Server.Serve` sees that it is shutting
down and returns `http.ErrServerClosed` (net/http `server.go`, the first test
after `l.Accept()`). `AskConnection` then clears the deadline with
`SetDeadline(time.Time{})` before returning, so the next server's `Accept`
blocks normally.

### 3.3 The browser side of the handover

After a successful attempt the page does not reload at once. The connect
phase may still be running when the reload arrives, possibly on the same
keep-alive connection, and it would answer with the connect page again. The
page instead polls `GET /api/status?t=<token>` every 250 ms. The connect
phase does not serve that route (404); the monitor does (200). On the first
200 the page loads `/?t=<token>`.

## 4. The page

### 4.1 Server field

One text field that accepts what a DBA already types into SSMS:

| Typed | Host | Instance | Port |
|---|---|---|---|
| `db01` | db01 | | |
| `db01\SALES` | db01 | SALES | |
| `db01,14330` | db01 | | 14330 |
| `db01\SALES,14330` | db01 | SALES | 14330 |
| `tcp:db01,14330` | db01 | | 14330 |
| `.` or `(local)` | localhost | | |

Whitespace around each part is trimmed. A port that is not an integer from 1
to 65535 is a form error. When both an instance and a port are given, both go
into the connection string and the driver uses the port without asking SQL
Server Browser (`tcpDialer.CallBrowser` in go-mssqldb `protocol.go`: the
Browser is asked only when an instance is set and the port is zero), which is
what SSMS does too.

### 4.2 Authentication methods

The list depends on the operating system the binary runs on
(`runtime.GOOS`), and the page receives it from the server rather than
deciding it itself.

| Id | Label | Fields | Offered on |
|---|---|---|---|
| `sql` | SQL Server login | login, password | all |
| `windows` | Windows, current account | none | windows only |
| `domain` | Windows, domain account | `DOMAIN\user`, password | all |
| `kerberos` | Kerberos ticket (kinit) | credential cache, krb5.conf | all but windows |

`windows` works because go-mssqldb registers `winsspi` as the default
integrated provider in Windows builds (`auth_windows.go`), used whenever the
connection string carries no user.

`domain` is NTLM. go-mssqldb registers its `ntlm` provider on every platform
and selects it when the user name contains a backslash
(`integratedauth/ntlm/ntlm.go`, `getAuth`). The page states under the
fields that a domain policy can refuse NTLM, in which case Kerberos is the
way.

`kerberos` uses the `krb5` provider this binary already links
(`internal/source/mssql/mssql.go` imports it). Only the credential cache form
is offered: a ticket obtained with `kinit` beforehand, no user, no password.

### 4.3 Kerberos defaults, and the trap they avoid

Two fields are prefilled from what the process can see:

- credential cache: from `KRB5CCNAME` when it names a file, with a `FILE:`
  prefix removed; otherwise `/tmp/krb5cc_<uid>` when that file exists;
  otherwise empty.
- krb5.conf: `KRB5_CONFIG` when set, otherwise `/etc/krb5.conf`.

The trap: the pure Go Kerberos library reads a credential cache only from a
file (`credentials.LoadCCache` in gokrb5, called from go-mssqldb
`integratedauth/krb5/krb5.go`). Fedora and RHEL default to the KCM cache
(`/etc/krb5.conf.d/kcm_default_ccache` sets `default_ccache_name = KCM:`,
checked on the machine this was written on), so a user who ran a plain
`kinit` has a ticket this tool cannot read. And when the connection string
names no cache, the driver reads `KRB5CCNAME` verbatim, prefix included, and
fails with `krb5-credcachefile does not exist`, which says nothing about why.

So the cache path always goes into the connection string explicitly, prefix
removed, and when `KRB5CCNAME` has a type other than `FILE:` (`KCM:`,
`KEYRING:`, `DIR:`, `API:`, `MEMORY:`), the field is left empty and the page
says, in one line, that this cache type cannot be read and gives the command
that makes one that can:

```
kinit -c FILE:/tmp/krb5cc_<uid> user@REALM
```

with the actual uid substituted.

### 4.4 The folded fields

Under one `<details>` element:

- database: empty means the login's default database. The page notes that
  Azure SQL Database requires one.
- encryption: three values.
  - driver default (the parameter is omitted): the driver encrypts the login
    and trusts the server certificate, and full encryption happens if the
    server demands it (go-mssqldb `msdsn/conn_str.go`, `parseTLS`: no
    `encrypt` parameter means `EncryptionOff` with `trustServerCert = true`).
  - required (`encrypt=true`): a checkbox "trust the server certificate"
    appears, unchecked, which adds `trustservercertificate=true`.
  - strict (`encrypt=strict`, TDS 8, SQL Server 2022 and later).

## 5. Building the connection string

In Go, in `internal/source/mssql/connstr.go`, never in the page's
JavaScript: `net/url` is what escapes a password containing `@`, `:`, `/`,
`#`, `%` or a space, and the backslash of a domain account, correctly.

```go
// ConnParams is what the connect page posts.
type ConnParams struct {
	Server     string `json:"server"`      // any form of section 4.1
	Auth       string `json:"auth"`        // sql, windows, domain, kerberos
	Login      string `json:"login"`       // sql: login; domain: DOMAIN\user
	Password   string `json:"password"`    // sql and domain only
	Database   string `json:"database"`    // optional
	Encrypt    string `json:"encrypt"`     // "", "true" or "strict"
	Trust      bool   `json:"trust"`       // only read when Encrypt is "true"
	Krb5Cache  string `json:"krb5_cache"`  // kerberos only
	Krb5Config string `json:"krb5_config"` // kerberos only
}

// BuildDSN returns a sqlserver:// URL, or an error naming the field at fault.
func BuildDSN(p ConnParams) (string, error)
```

The URL is assembled as a `url.URL` value: `Scheme: "sqlserver"`,
`User: url.UserPassword(login, password)` for `sql` and `domain` and nil
otherwise, `Host` as the host joined with the port when there is one
(`net.JoinHostPort`, which also brackets an IPv6 address), `Path` as `/`
plus the instance when there is one, and `RawQuery` from a `url.Values` with
`database`, `encrypt`, `trustservercertificate`, and for `kerberos`
`authenticator=krb5`, `krb5-credcachefile` and `krb5-configfile`.

Rejected as form errors, before any connection is attempted: an empty
server, an unknown auth id or one not offered on this platform, an empty
login for `sql` and `domain`, a `domain` login without a backslash, an empty
password for `sql` and `domain`, an empty credential cache or krb5.conf path
for `kerberos`, an encrypt value outside the three.

What the page shows after success, and what the log prints, is
`url.URL.Redacted()` of that URL, which replaces the password with `xxxxx`.
It teaches the format, and it can be copied into `sqltop.yaml` with the
password replaced by `${SQLTOP_CONN}` or kept in `.env`.

Three more functions in the same file give the page what it offers:

```go
// AuthMethods lists the methods of section 4.2 for this platform, in order.
func AuthMethods() []AuthMethod // {ID, Label string}

// KerberosDefaults computes section 4.3. Pure: the environment, the uid and
// the file test are passed in, so the cases are testable anywhere.
func KerberosDefaults(getenv func(string) string, uid int, exists func(string) bool) Krb5Defaults

type Krb5Defaults struct {
	Cache   string `json:"cache"`
	Config  string `json:"config"`
	Problem string `json:"problem"` // the one-line explanation, or ""
}
```

## 6. The two routes of the connect phase

Both go through the same token check, `Host` check and security headers as
every route of the monitor. Those are methods on `web.Server` today
(`authenticate`, `hostAllowed`, `securityHeaders`); `authenticate` becomes a
function of the token so that both phases call the same code.

`GET /` serves the connect page, composed inline like the monitor page
(`composePage` in `internal/web/server.go`), because a relative URL does not
carry the token.

`GET /api/connect` answers the options, which `main` provides:

```json
{"methods": [{"id": "sql", "label": "SQL Server login"}],
 "kerberos": {"cache": "/tmp/krb5cc_1000", "config": "/etc/krb5.conf", "problem": ""},
 "env_path": "/home/dba/sqltop/.env"}
```

`POST /api/connect` takes the fields of `ConnParams` plus `"save_env": true`
or `false`, and answers:

| Case | Status | Body |
|---|---|---|
| connected | 200 | `{"dsn": "<redacted>", "env_written": "<path or empty>", "env_error": "<message or empty>"}` |
| form rejected by `BuildDSN` | 400 | `{"error": "..."}` |
| an attempt already running, or already connected | 409 | `{"error": "..."}` |
| the connection failed | 502 | `{"error": "<driver message>", "hint": "<text or empty>"}` |

Each attempt creates a new `mssql.Source`, applies `AllowCapture(*capture)`,
and calls `Open` under a context of 30 seconds derived from the request's. A
failed `Open` already closes what it created (`mssql.Source.Open`), so a
failure leaves nothing behind. One attempt runs at a time, under a mutex.

The hint is chosen by looking for a fixed string in the driver's error, and
there are four:

| Error contains | Hint |
|---|---|
| `no instance matching` | SQL Server Browser did not return that instance. Type `host,port` instead; the port is in SQL Server Configuration Manager. |
| `x509:` | The server certificate was refused. Leave encryption on the driver default, or tick "trust the server certificate". |
| `Login failed for user` | The server refused the login. For a SQL Server login, the server must also allow SQL Server authentication. |
| `krb5` (case-insensitive) | Check the ticket with `klist -c <cache>`, and type the server's fully qualified name: Kerberos matches the name the service is registered under. |

`internal/web` does not know SQL Server. It takes the options as an opaque
value and the attempt as a function:

```go
// ConnectFunc tries one connection from the posted body. On failure it
// returns a *ConnectError carrying the status and the message to show.
type ConnectFunc func(ctx context.Context, body []byte) (ConnectResult, error)

type ConnectResult struct {
	DSN        string `json:"dsn"` // redacted
	EnvWritten string `json:"env_written"`
	EnvError   string `json:"env_error"`
}

type ConnectError struct {
	Status        int
	Message, Hint string
}

func (l *Listener) AskConnection(ctx context.Context, options any, connect ConnectFunc) error
```

`main` supplies the function: decode into `mssql.ConnParams`, `BuildDSN`,
open a source, keep it for step 3 of section 3.1, and write `.env` when asked.
Only the 200 path returns, and `AskConnection` shuts its server down after
that response has been written.

## 7. Writing `.env`

The checkbox shows the absolute path of the file `-env` named (default
`./.env`, made absolute at startup with `filepath.Abs`). When the method
carries a password, the label adds that it will be stored in clear text.

The file is written only after a successful connection, by a new function:

```go
// Set makes key=value the definition of key in the file at path.
func Set(path, key, value string) error
```

- The first line defining the key, with or without an `export ` prefix, is
  replaced; any later definition of the same key is removed, since `Load`
  keeps the first and would never read them anyway.
- Every other line, comments included, is kept byte for byte, in order.
- If no line defines the key, `key=value` is appended, after a newline if
  the file does not end with one.
- A missing file is created with mode 0600. An existing file keeps its mode.
- The write goes to a temporary file in the same directory, then
  `os.Rename` over the original.
- A value containing `\n` or `\r` is refused.
- The value is written unquoted. A connection string from `BuildDSN`
  contains no whitespace, no quote and no newline, and `Load` reads such a
  value back unchanged; the tests check that round trip rather than rely on
  it.

A failed write does not undo the connection. The page shows `env_error` as a
warning next to the redacted connection string.

## 8. Logging

On success: `connected to <redacted DSN>`, and when written,
`wrote SQLTOP_CONN to <path>`. On failure: nothing at all, since the page
shows the error. The password never reaches the log.

## 9. Tests

Each assertion is seen failing first, by breaking the code it asserts, as the
project's rules require.

`internal/source/mssql`, no server needed:

- `BuildDSN` on a table of cases, each parsed back with go-mssqldb's own
  `msdsn.Parse` and checked field by field (host, port, instance, user,
  password, database, encryption, `Parameters`), so what is checked is what
  the driver understands, not the string. The table covers every row of
  section 4.1, every method of section 4.2, a password of
  ``p@ss:w/o#r%d ?&=`` plus a non-ASCII character, `CORP\dba` as a domain
  login, the three encryption values with and without trust, and the Kerberos
  parameters.
- Every form error listed in section 5, one case each.
- `KerberosDefaults`: `FILE:/x`, `/x` without a prefix, `KCM:`, `KEYRING:`,
  unset with `/tmp/krb5cc_<uid>` present, unset with it absent, and
  `KRB5_CONFIG` set and unset.
- `AuthMethods` on the current platform contains `sql` and `domain`, and
  contains `windows` exactly when `runtime.GOOS` is `windows`.

`internal/source/mssql`, against the container (`SQLTOP_TEST_DSN`):

- A `ConnParams` of method `sql` with the server typed as `127.0.0.1,<port>`
  and the container's login builds a string that `Source.Open` connects with.

`internal/dotenv`:

- `Set` on: a missing file (created, mode 0600), a file without the key, a
  file with the key once, with `export` in front, twice, a file without a
  final newline, and a value with a newline (refused, file untouched). Each
  result is read back with `Load` in a clean environment.

`internal/web`, with a fake `ConnectFunc`:

- Both routes refuse a request without the token, and a wrong `Host`.
- `POST /api/connect` refuses other methods; a second POST while the first is
  inside the function gets 409; a `*ConnectError` comes out with its status,
  message and hint.
- The handover: after a successful POST, `AskConnection` returns; a
  `web.Server` built on the same `Listener` then answers `GET /api/status`
  with the same token, on the same address. One request is sent while the
  handover is in progress and must be answered by the monitor, not refused.
- `AskConnection` returns `context.Canceled` when its context is cancelled.

The browser test (`internal/web/e2e_test.go`), with a fake `ConnectFunc`:

- The connect page renders; choosing each method shows exactly its fields.
- Submitting lands on the monitor page in the same tab, with rows.

## 10. What cannot be verified here

Written down as open, the way `docs/SPECS.md` section 14 already lists
Kerberos against a real domain:

- A named instance through SQL Server Browser. SQL Server on Linux has no
  Browser service, so the containers cannot exercise `db01\SALES` without a
  port.
- NTLM and Kerberos against a real Active Directory domain.
- `winsspi` on a Windows machine joined to a domain.

## 11. Changes to other documents

`docs/SPECS.md`:

- Section 3, the Auth row: SQL Server authentication, Windows through NTLM
  and through Kerberos from Linux, Windows current account on Windows.
  Microsoft Entra is not linked, with the reason of section 2 here, and moves
  to section 13.
- Section 3.3: add NTLM, and the credential cache trap of section 4.3.
- Section 4.3: starting without a connection string serves the connect page
  on the same address and token.

`README.md`: a short table of connection strings by case (default instance,
named instance, explicit port, SQL login, domain account, Kerberos ticket),
and a line saying that starting without one opens the connect page.
