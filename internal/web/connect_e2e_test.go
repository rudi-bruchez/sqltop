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
