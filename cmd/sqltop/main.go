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
	go col.Run(ctx)

	srv, err := web.NewServer(col, win, cfg.Server, cfg.Tiers.Requests.Std())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sqltop on %s", srv.URL())
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
