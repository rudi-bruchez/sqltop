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
