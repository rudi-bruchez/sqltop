// Command sqltop is a top for SQL servers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
	"github.com/rudi-bruchez/sqltop/internal/source/mssql"
	"github.com/rudi-bruchez/sqltop/internal/web"
	"github.com/rudi-bruchez/sqltop/internal/window"
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

	// Announced before anything can fail, because the first question about
	// any report from this tool is which build produced it, and a startup
	// that dies on a bad configuration file or an unreachable server is
	// exactly the report that arrives without one. The same string is what
	// the interface header shows, so a screenshot and a log agree.
	log.Print(buildinfo.String())

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
	// colDone closes once col.Run actually returns, not merely once ctx is
	// cancelled: the wait below on it is what keeps the deferred src.Close
	// above from firing while a tier goroutine is still mid-query against
	// that same connection (fix round 1, task 14). col.Run's own error is
	// only logged, not fatal: by the time it returns, ctx is already
	// cancelled and shutdown is already under way, so there is nothing left
	// to abort.
	colDone := make(chan struct{})
	go func() {
		defer close(colDone)
		if err := col.Run(ctx); err != nil {
			log.Printf("collector stopped: %v", err)
		}
	}()

	srv, err := web.NewServer(col, win, cfg.Server)
	if err != nil {
		log.Fatal(err)
	}
	// The token in this URL is exactly what URL's own doc comment names as
	// its cost: printing it here sends it to stderr, and from there to
	// whatever captures this process's output, journald, a CI log, a
	// terminal scrollback, with whatever permissions that carries. Accepted
	// for the same reason URL accepts putting it in the address at all;
	// see that comment for the full reasoning rather than repeating it here.
	log.Printf("sqltop on %s", srv.URL())
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
	<-colDone
}
