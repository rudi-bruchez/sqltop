package mssql

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestWithAppNameNamesTheConnection. What shows in program_name is the
// first thing a DBA looks at when an unexpected query appears on their
// server, and "go-mssqldb" tells them nothing about which tool or which
// build produced it.
func TestWithAppNameNamesTheConnection(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string // a substring the result must carry
	}{
		{"url form", "sqlserver://host:1433?database=x", "app+name=sqltop"},
		{"url form with credentials", "sqlserver://sa:p%40ss@host:1433", "app+name=sqltop"},
		{"url form with no query at all", "sqlserver://host", "app+name=sqltop"},
		{"ado form", "server=host;user id=sa;password=x", ";app name=sqltop"},
		{"ado form ending in a semicolon", "server=host;", "app name=sqltop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withAppName(tc.dsn)
			if !strings.Contains(got, tc.want) {
				t.Errorf("withAppName(%q) = %q, want it to carry %q", tc.dsn, got, tc.want)
			}
			if !strings.Contains(got, AppName[len("sqltop "):]) {
				t.Errorf("withAppName(%q) = %q, and the version is what identifies the build", tc.dsn, got)
			}
		})
	}
}

// TestWithAppNameLeavesAnExplicitOneAlone. Somebody who named their
// connection did it for a reason: a firewall rule, or a Resource Governor
// classifier that reads exactly that string.
func TestWithAppNameLeavesAnExplicitOneAlone(t *testing.T) {
	for _, dsn := range []string{
		"sqlserver://host?app+name=my+monitor",
		"sqlserver://host?app%20name=my+monitor",
		"server=host;app name=my monitor",
		"server=host;Application Name=my monitor",
	} {
		if got := withAppName(dsn); got != dsn {
			t.Errorf("withAppName(%q) = %q; an explicit name must win", dsn, got)
		}
	}
}

// TestWithAppNameDoesNotCorruptTheCredentials is the one that matters if
// this is wrong: a DSN carries a password, and a mangled one is a tool that
// cannot connect at all. A shape neither parser recognises is handed back
// untouched rather than guessed at.
func TestWithAppNameDoesNotCorruptTheCredentials(t *testing.T) {
	dsn := "sqlserver://sa:p%40ss%3Bword@host:1433?database=CRM"
	got := withAppName(dsn)
	if !strings.Contains(got, "sa:p%40ss%3Bword@host:1433") {
		t.Errorf("withAppName mangled the credentials: %q", got)
	}
	if !strings.Contains(got, "database=CRM") {
		t.Errorf("withAppName lost a parameter: %q", got)
	}
	if opaque := "not a dsn at all"; withAppName(opaque) != opaque {
		t.Errorf("withAppName rewrote something it does not understand: %q", withAppName(opaque))
	}
}

// TestTheServerSeesTheApplicationName is the whole point, checked against a
// real engine rather than against the string this package built.
func TestTheServerSeesTheApplicationName(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identify(t, s, ctx)

	rows, err := s.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	for _, r := range rows {
		if r.SessionID == s.spid {
			if r.Program != AppName {
				t.Errorf("the server sees this connection as %q, want %q", r.Program, AppName)
			}
			return
		}
	}
	t.Fatalf("the tool's own session %d is not in the session list", s.spid)
}
