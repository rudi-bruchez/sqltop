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
- No Kerberos. The binary links go-mssqldb's `krb5` provider, but the pure Go
  library under it (gokrb5 v8.4.4, the latest release, February 2023) cannot
  read the configuration of the distributions where a DBA is most likely to
  have a ticket:
  - the stock Fedora and RHEL `/etc/krb5.conf` (package `krb5-libs`,
    unmodified) contains `dns_canonicalize_hostname = fallback`, which gokrb5
    parses as a boolean and rejects, failing the whole file with
    `invalid krb5 config libdefaults section line (dns_canonicalize_hostname = fallback): invalid boolean value`,
    before any ticket is looked at (measured by three reviewers and by the
    author, on Fedora 44);
  - gokrb5 ignores `includedir`, so realms and KDCs declared under
    `/etc/krb5.conf.d/`, where IPA and SSSD enrolment put them, are invisible
    to it;
  - it reads a credential cache only from a file, while those distributions
    default to `KEYRING:` (`/etc/krb5.conf`) or `KCM:`
    (`/etc/krb5.conf.d/kcm_default_ccache`), so a plain `kinit` leaves a
    ticket it cannot use.

  A form cannot make that work, and a form field taking a krb5.conf path
  would add a defect of its own: gokrb5's parse errors quote the offending
  line, so anyone holding the token could read one line of any file the tool
  can read, one attempt at a time. Kerberos stays possible through a
  hand-written connection string, with a krb5.conf gokrb5 can parse and a
  `FILE:` cache; section 11 puts that in `docs/SPECS.md` section 3.3 and the
  README.
- Nothing is written to `sqltop.yaml`. Secrets never go there
  (`docs/SPECS.md` section 8.3); the only file this page may write is `.env`,
  and only when asked.

## 3. Startup

### 3.1 The flow

1. `main` resolves the connection string exactly as today: `SQLTOP_CONN`,
   overridden by `cfg.Instances[0].DSN` after `os.ExpandEnv`.
2. The listening socket and the token are created first, by a new
   `web.Listen(cfg.Server)`. Today `web.NewServer` does both inside itself;
   they move out so that two servers can share them in turn. `NewServer`
   keeps its signature for the tests and calls `Listen`; a second
   constructor, `NewServerOn(c, w, l *Listener)`, takes an existing one.
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

Two behaviours change, both on purpose:

- A configured instance whose DSN expands to empty (`dsn: ${SQLTOP_CONN}`
  with the variable unset) exits with `log.Fatal` today. It now opens the
  connect page, like having no instance at all.
- The listener now binds before the connection is attempted, so a busy port
  fails before the server is contacted rather than after. Same failure,
  earlier, with nothing sent to the server.

Nothing is sent to the monitored server before an attempt, and a failed
attempt closes the source it created. The read-only rule of `docs/SPECS.md`
section 2 is unaffected: this phase runs no query of its own beyond the login
and the session initialisation `mssql.Source.Open` already performs.

### 3.2 Handing the socket over

The two phases serve on one `*net.TCPListener` so that the address, the port
and the token in the already-open tab stay valid, and so that a connection
arriving during the handover waits in the kernel's accept queue and is
accepted by whichever server calls `Accept` next.

The obstacle is `http.Server.Shutdown`, which closes every listener it
served on. The connect phase therefore serves on a wrapper whose `Close` does
not close the socket but sets its accept deadline to now:

```go
type handoff struct{ *net.TCPListener }

func (h handoff) Close() error { return h.SetDeadline(time.Now()) }
```

The connect phase stops through the existing `gracefulShutdown`
(`internal/web/server.go`): `Shutdown` with `shutdownGrace` (two seconds),
then `Close` if connections are still open. Both mark the server as shutting
down and call the wrapper's `Close`; the blocked `Accept` returns a timeout
error; `http.Server.Serve` sees that it is shutting down and returns
`http.ErrServerClosed` (the first test after `l.Accept()` in net/http
`server.go`). Both `Shutdown` and `Close` wait for the serve loop to exit
before returning (`listenerGroup.Wait()`), so `AskConnection` then clears the
deadline with `SetDeadline(time.Time{})` and the next server's `Accept`
blocks normally.

The grace matters. A browser opens connections speculatively and may leave
one idle; net/http holds `Shutdown` for five seconds on a connection that has
sent nothing (the `StateNew` rule in `closeIdleConns`), measured at 5.84 s by
one reviewer, and a connection stuck mid-request would hold it forever. With
the grace, the handover takes at most two seconds, and a connection dropped
by the fallback `Close` is one the page retries (section 3.3).

### 3.3 The browser side of the handover

After a successful attempt the page does not reload at once. The connect
phase may still be running when the reload arrives, and it would answer with
the connect page again. The page instead polls `GET /api/status?t=<token>`
every 250 ms. The connect phase does not serve that route (404); the monitor
does (200). A network error, which is what a connection dropped during the
handover looks like, is treated like a 404: wait and poll again. On the first
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

A named instance without a port costs a UDP request to SQL Server Browser on
port 1434. When the port is refused, the lookup fails at once (measured
against the container: `no instance matching 'NOPE' returned from host
'127.0.0.1'` in under a millisecond). When the request is dropped silently,
as a firewall does, the driver waits for the whole attempt's context before
failing (measured by a reviewer at the context's deadline exactly). While an
attempt with an instance and no port is running, the page says so: "resolving
the instance through SQL Server Browser; if it does not answer, this fails
after 30 seconds".

### 4.2 Authentication methods

The list depends on the operating system the binary runs on
(`runtime.GOOS`), and the page receives it from the server rather than
deciding it itself.

| Id | Label | Fields | Offered on |
|---|---|---|---|
| `sql` | SQL Server login | login, password | all |
| `windows` | Windows, current account | none | windows only |
| `domain` | Windows, domain account | `DOMAIN\user`, password | all |

`windows` works because go-mssqldb registers `winsspi` as the default
integrated provider in Windows builds (`auth_windows.go`), used whenever the
connection string carries no user.

`domain` goes through whichever provider is the platform's default. On
Windows that is `winsspi` again, which accepts a `DOMAIN\user` and password
(`integratedauth/winsspi/winsspi.go`) and negotiates Kerberos or NTLM itself.
Elsewhere it is `ntlm` (`auth_unix.go`), which go-mssqldb selects when the
user name contains a backslash (`integratedauth/ntlm/ntlm.go`, `getAuth`;
without the backslash it falls back to SQL authentication, which is why the
form requires one). Outside Windows the page states under the fields that a
domain policy can refuse NTLM, and that Kerberos is then the way, by a
hand-written connection string (section 2).

### 4.3 The folded fields

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
  - strict (`encrypt=strict`, TDS 8, SQL Server 2022 and later). No trust
    checkbox: the driver forces trust off in this mode whatever the
    parameter says (`parseTLS`).

## 5. Building the connection string

In Go, in `internal/source/mssql/connstr.go`, never in the page's
JavaScript: `net/url` is what escapes a password containing `@`, `:`, `/`,
`#`, `%` or a space, and the backslash of a domain account, correctly.

```go
// ConnParams is what the connect page posts.
type ConnParams struct {
	Server   string `json:"server"`   // any form of section 4.1
	Auth     string `json:"auth"`     // sql, windows, domain
	Login    string `json:"login"`    // sql: login; domain: DOMAIN\user
	Password string `json:"password"` // sql and domain only
	Database string `json:"database"` // optional
	Encrypt  string `json:"encrypt"`  // "", "true" or "strict"
	Trust    bool   `json:"trust"`    // only read when Encrypt is "true"
}

// BuildDSN returns a sqlserver:// URL, or an error naming the field at fault.
func BuildDSN(p ConnParams) (string, error)

// AuthMethods lists the methods of section 4.2 for this platform, in order.
// Fields names which of login and password the page shows for a method, and
// Note is the line under them (the NTLM remark of section 4.2).
func AuthMethods() []AuthMethod // {ID, Label string; Fields []string; Note string}
```

The URL is assembled as a `url.URL` value: `Scheme: "sqlserver"`,
`User: url.UserPassword(login, password)` for `sql` and `domain` and nil
otherwise, `Host` as the host joined with the port when there is one
(`net.JoinHostPort`, which also brackets an IPv6 address), `Path` as `/`
plus the instance when there is one, and `RawQuery` from a `url.Values` with
`database`, `encrypt` and `trustservercertificate`.

Rejected as form errors, before any connection is attempted: an empty
server, an unknown auth id or one not offered on this platform, an empty
login for `sql` and `domain`, a `domain` login without a backslash, an empty
password for `domain`, an encrypt value outside the three. An empty password
is accepted for `sql`: a SQL login created with `CHECK_POLICY = OFF` can
have one, and refusing it would send that user back to writing the string by
hand.

What the page shows after success, and what the log prints, is
`url.URL.Redacted()` of that URL, which replaces the password with `xxxxx`.
It teaches the format. To reuse it, the whole connection string goes into
`.env` as `SQLTOP_CONN`, which is what the checkbox of section 7 does, and
`sqltop.yaml` refers to it whole, as `dsn: ${SQLTOP_CONN}`, the form
`docs/SPECS.md` section 8.3 already documents. Substituting a variable for
the password alone does not work: `os.ExpandEnv` inserts it unescaped, and a
password containing `@` or `:` then parses as a different host without any
error.

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
{"methods": [{"id": "sql", "label": "SQL Server login", "fields": ["login", "password"]}],
 "env_path": "/home/dba/sqltop/.env",
 "save": true}
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
failed `Open` already closes what it created (`mssql.Source.Open` closes the
pool on both of its failure paths), so a failure leaves nothing behind but
the failed-login entry any client leaves in the server's error log. One
attempt runs at a time: the handler takes a `sync.Mutex` with `TryLock` and
answers 409 when it cannot, rather than queueing the second request behind a
thirty-second attempt.

The hint is chosen by looking for a fixed string in the driver's error, and
there are four:

| Error contains | Hint |
|---|---|
| `no instance matching` | SQL Server Browser did not return that instance. Type `host,port` instead; the port is in SQL Server Configuration Manager. |
| `x509:` | The server certificate was refused. Leave encryption on the driver default, or choose "required" and tick "trust the server certificate". |
| `TLS Handshake failed` | The server did not complete the TLS handshake. With "strict", the server must support TDS 8 (SQL Server 2022 and later, with strict encryption configured); otherwise choose another encryption mode. |
| `Login failed for user` | The server refused the login. For a SQL Server login, the server must also allow SQL Server authentication. |

`no instance matching` covers both ways a Browser lookup fails: when the
Browser answers without the instance, and when it does not answer at all,
since go-mssqldb only logs the error of the UDP request (`tds.go`, in
`dialConnection`) and then returns the one `ParseBrowserData` produces from
the empty answer.

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
Only the 200 path returns, and `AskConnection` stops its server (section 3.2)
after that response has been written.

## 7. Writing `.env`

The checkbox shows the absolute path of the file `-env` named (default
`./.env`, made absolute at startup with `filepath.Abs`). When the method
carries a password, the label adds that it will be stored in clear text.

The file is written only after a successful connection, by a new function:

```go
// Set makes key=value the definition of key in the file at path.
func Set(path, key, value string) error
```

- A line defines the key exactly when `Load` would read it as the key's
  definition: after `strings.TrimSpace`, not empty, not starting with `#`,
  with an `export ` prefix removed, cut at the first `=`, and the part before
  it equal to the key once trimmed. So `SQLTOP_CONN = x`, `  SQLTOP_CONN=x`
  and `export SQLTOP_CONN=x` all define it. The rule is one shared function
  that `Load` also calls, so the two cannot drift apart.
- The first line defining the key is replaced by `key=value`; any later
  definition of the same key is removed, since `Load` keeps the first and
  would never read them anyway.
- Every other line, comments included, is kept byte for byte, in order.
- If no line defines the key, `key=value` is appended, after a newline if
  the file does not end with one.
- The write goes to a temporary file created in the same directory with
  `os.CreateTemp`, then `os.Rename` over the original. `os.Rename` installs
  the temporary file's inode, mode included, so the temporary file is first
  given the mode the original had (`os.Chmod` with the original's
  `Mode().Perm()`). A missing file is created with mode 0600.
- A value containing `\n` or `\r` is refused.
- The value is written unquoted. A connection string from `BuildDSN`
  contains no whitespace, no quote and no newline, and `Load` reads such a
  value back unchanged; the tests check that round trip rather than rely on
  it.

A failed write does not undo the connection. The page shows `env_error` as a
warning next to the redacted connection string.

A save the next run would not read is not offered. When `sqltop.yaml` names
an instance whose `dsn` is set and does not mention `SQLTOP_CONN` (a
`dsn: ${OTHER}` whose variable is unset, say), the options carry
`"save": false`, the checkbox is replaced by one line saying that saving to
`.env` would change nothing, and a `save_env` posted anyway is ignored.

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
  password, database, encryption, trust), so what is checked is what the
  driver understands, not the string. The table covers every row of section
  4.1, every method of section 4.2, a password of ``p@ss:w/o#r%d ?&=`` plus a
  non-ASCII character, an empty `sql` password, `CORP\dba` as a domain login,
  an IPv6 host with and without a port, and the three encryption values with
  and without trust.
- Every form error listed in section 5, one case each.
- `AuthMethods` on the current platform contains `sql` and `domain`, and
  contains `windows` exactly when `runtime.GOOS` is `windows`.
- The hint table: each of the four strings selects its hint, and an error
  containing none selects none.

`internal/source/mssql`, against the container (`SQLTOP_TEST_DSN`):

- A `ConnParams` of method `sql` with the server typed as `127.0.0.1,<port>`
  and the container's login builds a string that `Source.Open` connects with.
- The same with a wrong password returns an error containing
  `Login failed for user`, and with `encrypt=strict` one containing
  `TLS Handshake failed`, so the hint table is checked against the driver
  rather than against this document.

`internal/dotenv`:

- `Set` on: a missing file (created, mode 0600), a file without the key, a
  file with the key once, with `export` in front, with spaces around the
  `=`, with leading spaces, twice, a file without a final newline, an
  existing file of mode 0640 (still 0640 afterwards), and a value with a
  newline (refused, file untouched). Each result is read back with `Load` in
  a clean environment.

`internal/web`, with a fake `ConnectFunc`:

- Both routes refuse a request without the token, and a wrong `Host`.
- `POST /api/connect` refuses other methods; a second POST while the first is
  inside the function gets 409 at once, not after the first returns; a
  `*ConnectError` comes out with its status, message and hint.
- The handover: after a successful POST, `AskConnection` returns; a
  `web.Server` built on the same `Listener` with `NewServerOn` then answers
  `GET /api/status` with the same token, on the same address. One request is
  sent while the handover is in progress and must be answered by the monitor,
  not refused.
- The handover with an idle connection the connect phase accepted and that
  never sent a request: `AskConnection` still returns within `shutdownGrace`
  plus a margin, not after five seconds.
- `AskConnection` returns `context.Canceled` when its context is cancelled.

The browser test (`internal/web/e2e_test.go`), with a fake `ConnectFunc`:

- The connect page renders; choosing each method shows exactly its fields.
- Submitting lands on the monitor page in the same tab, with rows.

## 10. What cannot be verified here

Written down as open, the way `docs/SPECS.md` section 14 already lists
Kerberos against a real domain:

- A named instance whose SQL Server Browser answers with the instance's
  port. The Linux containers run no Browser and refuse the request, so only
  the failure path (`no instance matching`) is measurable here, and the
  silent-drop timeout of section 4.1 only with a firewall rule added for the
  purpose.
- NTLM against a real Active Directory domain.
- `winsspi` on a Windows machine joined to a domain, both for the current
  account and for a `DOMAIN\user` with a password.

## 11. Changes to other documents

`docs/SPECS.md`:

- Section 3, the Auth row: SQL Server authentication; Windows through the
  current account on Windows and through a domain account everywhere (NTLM
  outside Windows); Kerberos from Linux by hand-written connection string
  only, under the conditions of section 3.3. Microsoft Entra is not linked,
  with the reason of section 2 here, and moves to section 13.
- Section 3.3: the `krb5` provider builds and links as stated, but "verified"
  there covers the build only. Record the three limits of section 2 here
  (the stock Fedora and RHEL krb5.conf rejected, `includedir` ignored, file
  caches only), and what a working hand-written string needs: a krb5.conf
  gokrb5 can parse, named with `krb5-configfile`, and a `FILE:` cache named
  with `krb5-credcachefile`, obtained with
  `kinit -c FILE:/tmp/krb5cc_$(id -u) user@REALM`.
- Section 4.3: starting without a connection string serves the connect page
  on the same address and token.

`README.md`: a short table of connection strings by case (default instance,
named instance, explicit port, SQL login, domain account, and Kerberos by
hand with the conditions above), a line saying that starting without one
opens the connect page, and the `dsn: ${SQLTOP_CONN}` form for reusing it in
`sqltop.yaml`.
