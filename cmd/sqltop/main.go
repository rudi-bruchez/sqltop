// Command sqltop is a top for SQL servers.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
	"github.com/rudi-bruchez/sqltop/internal/model"
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
	capture := flag.Bool("capture", false, "allow the c command to create a scoped Extended Events session on the monitored server; without this the tool creates and drops nothing")
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

	// -write-config exists so nobody has to know a tile's or a column's
	// name to switch it off. It writes every dashboard group, every figure
	// and every column the catalogue knows, each with its own switch, on
	// top of whatever was already configured.
	if *writeConfig {
		if cfg.Layouts == nil {
			cfg.Layouts = map[string]config.Layout{}
		}
		l := cfg.Layouts["default"]
		l.Dashboard = cfg.Dashboard()
		if l.Views == nil {
			l.Views = map[string]config.ViewLayout{}
		}
		for _, v := range model.ViewCatalogue {
			view := l.Views[v.ID]
			view.Columns = cfg.Columns(v.ID)
			l.Views[v.ID] = view
		}
		cfg.Layouts["default"] = l
		path, err := config.Save(cfg)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintln(os.Stderr, "wrote", path)
		return
	}

	// The page offers to write here, so it names the file it will write.
	envFile, err := filepath.Abs(*envPath)
	if err != nil {
		envFile = *envPath
	}

	dsn := os.Getenv("SQLTOP_CONN")
	if len(cfg.Instances) > 0 && cfg.Instances[0].DSN != "" {
		dsn = os.ExpandEnv(cfg.Instances[0].DSN)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Bound first, so that the connect page and the monitor serve one after
	// the other on one address and one token.
	l, err := web.Listen(cfg.Server)
	if err != nil {
		log.Fatal(err)
	}

	var src *mssql.Source
	if dsn != "" {
		src = mssql.New()
		src.AllowCapture(*capture)
		if err := src.Open(ctx, dsn); err != nil {
			l.Close()
			if ctx.Err() != nil {
				return // Ctrl-C while connecting is a request to stop, not a failure
			}
			log.Fatal(err)
		}
	} else {
		log.Printf("no instance configured; connect from %s", l.URL())
		openBrowser(l.URL(), *noBrowser)
		save := saveReachesTheConnection(cfg.Instances)
		if err := l.AskConnection(ctx, connectOptions(envFile, save), attempt(envFile, save, *capture, &src)); err != nil {
			if src != nil {
				src.Close()
			}
			l.Close()
			if ctx.Err() != nil {
				return
			}
			log.Fatal(err)
		}
	}
	defer src.Close()

	// The recovery sweep is behind the flag like everything else, because it
	// is itself a DROP and a server whose operator never asked for captures
	// must see nothing created and nothing removed. It runs here, before the
	// collector takes the connection, so it is over before anything else
	// competes for it, and it says what it dropped: a tool that quietly
	// deletes objects on somebody's server is worse than one that does not.
	if *capture {
		switch n, err := src.SweepCaptures(ctx); {
		case err != nil:
			log.Printf("warning: could not look for abandoned capture sessions: %v", err)
		case n > 0:
			log.Printf("dropped %d abandoned capture session(s) left behind by an earlier run", n)
		}
	}

	win := window.New(cfg.Retention.Std(), cfg.Budget.MaxSamples)
	col := collector.New(src, win, collector.NewBudget(cfg.Budget.ServerCPUMsPerSecond, cfg.Tiers))
	// colDone closes once col.Run actually returns, not merely once ctx is
	// cancelled: the wait below on it is what keeps the deferred src.Close
	// above from firing while a tier goroutine is still mid-query against
	// that same connection. col.Run's own error is only logged, not fatal:
	// by the time it returns, ctx is already cancelled and shutdown is
	// already under way, so there is nothing left to abort.
	colDone := make(chan struct{})
	go func() {
		defer close(colDone)
		if err := col.Run(ctx); err != nil {
			log.Printf("collector stopped: %v", err)
		}
	}()

	srv := web.NewServerOn(col, win, l).WithConfig(cfg)
	// The token in this URL is exactly what URL's own doc comment names as
	// its cost: printing it here sends it to stderr, and from there to
	// whatever captures this process's output, journald, a CI log, a
	// terminal scrollback, with whatever permissions that carries. Accepted
	// for the same reason URL accepts putting it in the address at all;
	// see that comment for the full reasoning rather than repeating it here.
	log.Printf("sqltop on %s", srv.URL())
	// The connect page already opened the browser, and its tab becomes the
	// monitor by itself.
	if dsn != "" {
		openBrowser(srv.URL(), *noBrowser)
	}
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
	<-colDone
}
