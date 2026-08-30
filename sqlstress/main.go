// Command sqlstress puts a synthetic workload on a SQL Server database so that
// sqltop has something to watch. It is a test and demonstration aid, not part
// of the product: it is the only thing in this repository that writes to a
// server, and every statement it sends rolls its writes back.
//
// The workload itself lives in .sql files rather than in this code. Adding a
// query to a demonstration means dropping a file in the queries directory,
// which keeps the Go side down to a loop and a clock.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"github.com/rudi-bruchez/sqltop/internal/dotenv"
)

// conf is deliberately small. Anything that varies per run is a flag; the file
// holds what a given demonstration is, so it can be committed and replayed.
//
// The connection string is not here and never will be: it carries a password,
// and the project's rule is that secrets come from the environment.
// duration is sqlstress's own, deliberately not sqltop's config.Duration.
// It borrowed that type once and the borrowing broke the moment sqltop's
// configuration format changed from JSON to YAML: this file is JSON, the
// shared type stopped speaking JSON, and nothing caught it because this
// binary has no tests. A load generator has no business depending on the
// wire format of the tool it exercises.
type duration time.Duration

func (d duration) Std() time.Duration { return time.Duration(d) }
func (d duration) String() string     { return time.Duration(d).String() }

func (d duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration is written as a string like \"60s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration: %w", s, err)
	}
	*d = duration(v)
	return nil
}

type conf struct {
	Threads  int      `json:"threads"`
	Duration duration `json:"duration"`
	Pause    duration `json:"pause"`
	Queries  string   `json:"queries"`
	Database string   `json:"database"`
}

func defaults() conf {
	return conf{
		Threads:  8,
		Duration: duration(60 * time.Second),
		Pause:    duration(200 * time.Millisecond),
		Queries:  "queries",
		Database: "PachadataFormation",
	}
}

// query is one .sql file, read once at startup and then only sent.
type query struct {
	name string
	text string
}

// stat is per worker per query, so the workers never share it and the whole
// run needs no lock. They are summed once, after everyone has stopped.
type stat struct {
	runs  int
	fails int
	total time.Duration
	max   time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sqlstress:", err)
		os.Exit(1)
	}
}

func run() error {
	confPath := flag.String("config", "sqlstress.json", "configuration file")
	envPath := flag.String("env", ".env", "environment file holding SQLSTRESS_DSN")
	threads := flag.Int("threads", 0, "override the configured thread count")
	durationFlag := flag.Duration("duration", 0, "override the configured duration")
	flag.Parse()

	envWarnings, err := dotenv.Load(*envPath)
	for _, w := range envWarnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if err != nil {
		return err
	}

	cfg, err := loadConf(*confPath)
	if err != nil {
		return err
	}
	if *threads > 0 {
		cfg.Threads = *threads
	}
	if *durationFlag > 0 {
		cfg.Duration = duration(*durationFlag)
	}

	// The queries directory is resolved relative to the configuration file, so
	// running sqlstress from anywhere finds the same workload.
	dir := cfg.Queries
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(*confPath), dir)
	}
	queries, err := loadQueries(dir)
	if err != nil {
		return err
	}

	dsn := os.Getenv("SQLSTRESS_DSN")
	if dsn == "" {
		// The test container's own variable, so a machine already set up for
		// the integration tests needs nothing more.
		dsn = os.Getenv("SQLTOP_TEST_DSN")
	}
	if dsn == "" {
		return errors.New("set SQLSTRESS_DSN (or SQLTOP_TEST_DSN) to the server to load")
	}
	if cfg.Database != "" && !strings.Contains(strings.ToLower(dsn), "database=") {
		dsn = addDatabase(dsn, cfg.Database)
	}

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	// One connection per worker, kept open: a worker that had to wait for a
	// connection would be measuring the pool rather than the server.
	db.SetMaxOpenConns(cfg.Threads)
	db.SetMaxIdleConns(cfg.Threads)
	db.SetConnMaxLifetime(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := describe(ctx, db); err != nil {
		return err
	}
	fmt.Printf("%d threads, %s, %d queries, %s between calls\n\n",
		cfg.Threads, cfg.Duration, len(queries), cfg.Pause)

	deadline, cancel := context.WithTimeout(ctx, cfg.Duration.Std())
	defer cancel()

	stats := make([][]stat, cfg.Threads)
	var done atomic.Int64
	var failed atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < cfg.Threads; w++ {
		stats[w] = make([]stat, len(queries))
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			work(deadline, db, queries, mine(queries, w), cfg.Pause.Std(), stats[w], &done, &failed)
		}(w)
	}

	started := time.Now()
	go heartbeat(deadline, started, &done, &failed)
	wg.Wait()

	report(queries, stats, time.Since(started))
	return nil
}

// mine is the list of queries a given worker runs, as indexes into queries.
//
// A file whose name ends in -solo is run by worker zero alone. The blocker is
// the reason: eight threads each holding exclusive locks for a few seconds
// queue up behind one another, the run turns into one long blocking chain,
// and every other query in the workload is starved. One blocker is enough to
// make the blocking view show something; eight is a different demonstration,
// and not the one being asked for.
// The list is rotated by the worker number so that a run with several threads
// spreads across the workload from the first call instead of every thread
// starting on the same file.
func mine(queries []query, worker int) []int {
	var idx []int
	for n := range queries {
		if worker != 0 && strings.HasSuffix(queries[n].name, "-solo") {
			continue
		}
		idx = append(idx, n)
	}
	if len(idx) == 0 {
		return nil
	}
	at := worker % len(idx)
	return append(idx[at:], idx[:at]...)
}

// work runs its own queries in a loop until the deadline. Round robin rather
// than a random pick, because a demonstration that cannot be repeated is not
// much of a demonstration.
func work(ctx context.Context, db *sql.DB, queries []query, mine []int, pause time.Duration, into []stat, done, failed *atomic.Int64) {
	if len(mine) == 0 {
		return
	}
	for i := 0; ; i++ {
		if ctx.Err() != nil {
			return
		}
		n := mine[i%len(mine)]
		start := time.Now()
		err := execute(ctx, db, queries[n].text)
		took := time.Since(start)

		s := &into[n]
		s.runs++
		s.total += took
		if took > s.max {
			s.max = took
		}
		if err != nil {
			// A cancelled context is the clock running out, not a failure.
			if ctx.Err() != nil {
				return
			}
			s.fails++
			failed.Add(1)
		}
		done.Add(1)

		if pause > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
	}
}

// execute sends one batch and drains it. Draining matters: a query whose rows
// are never read leaves the server pushing them into a full network buffer,
// and the load being simulated would not be the load written in the file.
func execute(ctx context.Context, db *sql.DB, text string) error {
	rows, err := db.QueryContext(ctx, text)
	if err != nil {
		return err
	}
	defer rows.Close()
	for {
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !rows.NextResultSet() {
			return rows.Err()
		}
	}
}

func heartbeat(ctx context.Context, started time.Time, done, failed *atomic.Int64) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			elapsed := now.Sub(started)
			n := done.Load()
			fmt.Printf("\r%5.0fs  %6d calls  %6.1f/s  %d failed  ",
				elapsed.Seconds(), n, float64(n)/elapsed.Seconds(), failed.Load())
		}
	}
}

func report(queries []query, stats [][]stat, elapsed time.Duration) {
	fmt.Printf("\r%-34s %8s %8s %10s %10s\n", "query", "runs", "failed", "avg", "max")
	var total stat
	for n := range queries {
		var s stat
		for w := range stats {
			s.runs += stats[w][n].runs
			s.fails += stats[w][n].fails
			s.total += stats[w][n].total
			if stats[w][n].max > s.max {
				s.max = stats[w][n].max
			}
		}
		fmt.Printf("%-34s %8d %8d %10s %10s\n",
			queries[n].name, s.runs, s.fails, average(s), s.max.Round(time.Millisecond))
		total.runs += s.runs
		total.fails += s.fails
		total.total += s.total
		if s.max > total.max {
			total.max = s.max
		}
	}
	fmt.Printf("\n%d calls in %s, %.1f per second, %d failed\n",
		total.runs, elapsed.Round(time.Second),
		float64(total.runs)/elapsed.Seconds(), total.fails)
}

func average(s stat) time.Duration {
	if s.runs == 0 {
		return 0
	}
	return (s.total / time.Duration(s.runs)).Round(time.Millisecond)
}

// describe prints what is about to be loaded. sqlstress writes, even if it
// rolls back, so it says out loud which server and which database it reached
// before it starts.
func describe(ctx context.Context, db *sql.DB) error {
	var server, database, version string
	err := db.QueryRowContext(ctx,
		`SELECT CONVERT(varchar(128), SERVERPROPERTY('ServerName')), DB_NAME(),
		        CONVERT(varchar(128), SERVERPROPERTY('ProductVersion'))`).
		Scan(&server, &database, &version)
	if err != nil {
		return err
	}
	fmt.Printf("%s (SQL Server %s), database %s\n", server, version, database)
	return nil
}

func loadConf(path string) (conf, error) {
	cfg := defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Threads < 1 {
		return cfg, fmt.Errorf("%s: threads must be at least 1", path)
	}
	if cfg.Duration <= 0 {
		return cfg, fmt.Errorf("%s: duration must be positive", path)
	}
	return cfg, nil
}

// loadQueries reads every .sql file in dir, in name order. The numeric
// prefixes in the shipped files are there to make that order deliberate.
func loadQueries(dir string) ([]query, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("%s: no .sql files", dir)
	}
	queries := make([]query, 0, len(names))
	for _, name := range names {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		queries = append(queries, query{
			name: strings.TrimSuffix(name, ".sql"),
			text: string(text),
		})
	}
	return queries, nil
}

// addDatabase appends the configured database to a connection string that does
// not name one, so the same DSN used by the integration tests reaches the
// demonstration database without being edited.
func addDatabase(dsn, database string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "database=" + database
}
