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
