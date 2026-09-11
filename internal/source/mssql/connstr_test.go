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
