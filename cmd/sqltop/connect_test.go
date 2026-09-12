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
