// Command sqltop is a top for SQL servers.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"go.yaml.in/yaml/v3"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
	"github.com/rudi-bruchez/sqltop/internal/source/mssql"
	"github.com/rudi-bruchez/sqltop/internal/web"
	"github.com/rudi-bruchez/sqltop/internal/window"
)

func main() {
	configPath := flag.String("config", "", "path to sqltop.yaml (default: beside the binary, then the user config directory)")
	envPath := flag.String("env", ".env", "path to the .env file holding secrets")
	showConfig := flag.Bool("show-config", false, "print the resolved configuration and exit")
	writeConfig := flag.Bool("write-config", false, "write a complete sqltop.yaml, every dashboard tile listed, and exit")
	noBrowser := flag.Bool("no-browser", false, "do not open the interface in a browser at startup")
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

	envWarnings, err := dotenv.Load(*envPath)
	for _, w := range envWarnings {
		log.Printf("warning: %s", w)
	}
	if err != nil {
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
		// YAML, because that is what the file is. This printed JSON for a
		// release after the format changed, which meant it emitted Go field
		// names rather than the keys anybody could put back in a file.
		out, err := yaml.Marshal(cfg)
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(out)
		return
	}

	// -write-config exists so nobody has to know a tile's name to switch it
	// off. It writes every dashboard group and every figure the catalogue
	// knows, each with its own switch, on top of whatever was already
	// configured.
	if *writeConfig {
		if cfg.Layouts == nil {
			cfg.Layouts = map[string]config.Layout{}
		}
		l := cfg.Layouts["default"]
		l.Dashboard = cfg.Dashboard()
		cfg.Layouts["default"] = l
		path, err := config.Save(cfg)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintln(os.Stderr, "wrote", path)
		return
	}

	dsn := os.Getenv("SQLTOP_CONN")
	if len(cfg.Instances) > 0 && cfg.Instances[0].DSN != "" {
		dsn = os.ExpandEnv(cfg.Instances[0].DSN)
	}
	if dsn == "" {
		log.Fatal("no instance to connect to: set SQLTOP_CONN in .env, or add one to sqltop.yaml")
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
	srv = srv.WithDashboard(cfg.Dashboard())
	// The token in this URL is exactly what URL's own doc comment names as
	// its cost: printing it here sends it to stderr, and from there to
	// whatever captures this process's output, journald, a CI log, a
	// terminal scrollback, with whatever permissions that carries. Accepted
	// for the same reason URL accepts putting it in the address at all;
	// see that comment for the full reasoning rather than repeating it here.
	log.Printf("sqltop on %s", srv.URL())

	// Opened by default, because the interface is the tool and a URL with a
	// token in it is not something anybody enjoys retyping. A failure is a
	// line in the log and nothing more: most machines a DBA logs into have
	// no desktop at all, and refusing to run there would be worse than
	// printing the address and letting them paste it.
	if !*noBrowser {
		if name, err := web.OpenBrowser(srv.URL()); err != nil {
			log.Printf("could not open a browser with %s (%v); the address above is what to paste, or pass -no-browser to stop trying", name, err)
		}
	}
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
	<-colDone
}
