// Package config loads sqltop's settings and decides which file they came from.
//
// The format is YAML, which is a dependency in a project whose rule is
// standard library first, so the reason travels with the code as well as the
// commit: this file is opened by hand and handed between colleagues, and JSON
// is a poor format for that. JSON is a subset of YAML and the same parser
// reads it, so an existing sqltop.json needs nothing but a rename, and
// Resolve still looks for one.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// Duration is a time.Duration written as "1s" or "15m" rather than as a
// count of nanoseconds, because a configuration file a person edits should
// say what it means.
type Duration time.Duration

func (d Duration) String() string     { return time.Duration(d).String() }
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("config: line %d: a duration is written as a string like \"1s\" or \"15m\": %w", n.Line, err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: line %d: %q is not a duration: %w", n.Line, s, err)
	}
	*d = Duration(v)
	return nil
}

// DashboardGroup is one section of the dashboard as the configuration file
// sees it: whether it starts folded, and an explicit switch for every figure
// it can hold. Every figure is listed rather than only the enabled ones, so
// the file shows what is available instead of requiring the names to be
// known in advance. Section 8.2.
//
// Plain bools: go-yaml accepts on and off for them on the way in and writes
// true and false on the way out, which is what the file will look like
// after the tool next saves it either way.
type DashboardGroup struct {
	Group   string          `yaml:"group"`
	Folded  bool            `yaml:"folded"`
	Figures map[string]bool `yaml:"figures"`
}

// Layout is one named layout. Only the dashboard is typed so far: the views
// of section 8.2 are carried through untouched, because nothing reads them
// yet and inventing their shape before something does would be guessing.
type Layout struct {
	Dashboard []DashboardGroup `yaml:"dashboard,omitempty"`
	Views     *yaml.Node       `yaml:"views,omitempty"`
}

type Instance struct {
	Name string `yaml:"name"`
	DSN  string `yaml:"dsn"`
}

type Tiers struct {
	Requests   Duration `yaml:"requests"`
	Counters   Duration `yaml:"counters"`
	Space      Duration `yaml:"space"`
	CPUHistory Duration `yaml:"cpuHistory"`
	LivePlan   Duration `yaml:"livePlan"`
}

type Server struct {
	Port int `yaml:"port"`
}

type Budget struct {
	// ServerCPUMsPerSecond is the ceiling from spec section 10.
	ServerCPUMsPerSecond int `yaml:"serverCpuMsPerSecond"`
	// MaxSamples caps the retention window so memory stays bounded on a
	// busy server, where 15 minutes of history would otherwise grow without
	// limit. See spec section 4.2 and task 3.
	MaxSamples int `yaml:"maxSamples"`
}

type Config struct {
	Instances []Instance `yaml:"instances"`
	Tiers     Tiers      `yaml:"tiers"`
	Retention Duration   `yaml:"retention"`
	Server    Server     `yaml:"server"`
	Budget    Budget     `yaml:"budget"`
	// Layouts is spec section 8.2's named layouts.
	Layouts map[string]Layout `yaml:"layouts,omitempty"`

	// Path is the file this came from, empty when built-in defaults were
	// used. The status bar names it, so it must survive loading.
	Path string `yaml:"-"`
}

// DefaultLayout builds the layout the tool ships with: every dashboard
// group present, unfolded, with every figure the catalogue knows listed and
// switched on. Written out in full rather than left empty, because the point
// of the file is that somebody can see what exists and switch a tile off
// without having to know its name in advance.
func DefaultLayout() Layout {
	l := Layout{}
	for _, g := range model.DashboardCatalogue {
		grp := DashboardGroup{Group: g.ID, Folded: false, Figures: map[string]bool{}}
		for _, f := range g.Figures {
			grp.Figures[f.Key] = true
		}
		l.Dashboard = append(l.Dashboard, grp)
	}
	return l
}

// Dashboard returns the groups and figures this configuration asks for,
// resolved against the catalogue. Anything the file does not mention keeps
// the built-in default, which is on, so a hand-written partial layout is
// valid and a figure added to a later version of the tool appears rather
// than staying invisible until somebody edits their file.
func (cfg Config) Dashboard() []DashboardGroup {
	byID := map[string]DashboardGroup{}
	var order []string
	if l, ok := cfg.Layouts["default"]; ok {
		for _, g := range l.Dashboard {
			byID[g.Group] = g
			order = append(order, g.Group)
		}
	}

	var out []DashboardGroup
	seen := map[string]bool{}
	// The file's order wins where it says anything, so a user can move a
	// group up; the catalogue supplies the rest, in its own order.
	for _, id := range append(order, catalogueIDs()...) {
		if seen[id] {
			continue
		}
		cat, known := catalogueGroup(id)
		if !known {
			// A group the file names and the catalogue does not know is
			// left out rather than rendered empty: it is a typo or a
			// leftover from an older version.
			continue
		}
		seen[id] = true
		cfgGroup, configured := byID[id]
		grp := DashboardGroup{Group: id, Folded: cfgGroup.Folded, Figures: map[string]bool{}}
		for _, f := range cat.Figures {
			on := true
			if configured {
				if v, said := cfgGroup.Figures[f.Key]; said {
					on = v
				}
			}
			grp.Figures[f.Key] = on
		}
		out = append(out, grp)
	}
	return out
}

func catalogueIDs() []string {
	out := make([]string, 0, len(model.DashboardCatalogue))
	for _, g := range model.DashboardCatalogue {
		out = append(out, g.ID)
	}
	return out
}

func catalogueGroup(id string) (model.DashboardGroup, bool) {
	for _, g := range model.DashboardCatalogue {
		if g.ID == id {
			return g, true
		}
	}
	return model.DashboardGroup{}, false
}

func Default() Config {
	return Config{
		Tiers: Tiers{
			Requests:   Duration(time.Second),
			Counters:   Duration(time.Second),
			Space:      Duration(5 * time.Second),
			CPUHistory: Duration(time.Minute),
			LivePlan:   Duration(2 * time.Second),
		},
		Retention: Duration(15 * time.Minute),
		Server:    Server{Port: 8420},
		Budget:    Budget{ServerCPUMsPerSecond: 50, MaxSamples: 500_000},
	}
}

// Package-level seams the tests override with temporary directories. They are
// variables rather than environment lookups on purpose: an environment
// variable would also let a user silently redirect where their configuration
// is read from, which is a surprise nobody asked for.
var (
	binaryDir = func() string {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		return filepath.Dir(exe)
	}

	userConfigDir = func() string {
		d, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		return filepath.Join(d, "sqltop")
	}
)

// Resolve returns the configuration file to use, or "" when there is none and
// built-in defaults apply. Order: explicit, beside the binary, user directory.
// An explicit path that does not exist is an error rather than a silent
// fallback, because a typo must not look like a working default.
func Resolve(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config: %s: %w", explicit, err)
		}
		return explicit, nil
	}
	for _, dir := range []string{binaryDir(), userConfigDir()} {
		if dir == "" {
			continue
		}
		// .yaml first, then .json, at each location before moving on to
		// the next: an install that predates the format change keeps
		// working without a rename, and a directory holding both is
		// answered by the current format rather than by directory order.
		for _, name := range []string{"sqltop.yaml", "sqltop.json"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", nil
}

// Load reads path over the built-in defaults, so a partial file is valid.
// An empty path yields the defaults untouched.
//
// Every value is validated before it is handed back, because nothing between
// here and the collector clamps a bad one: a "requests" tier of "0s" is not
// a typo the tool can absorb, it is a tight loop against the monitored
// server, and a "maxSamples" of 0 is a grid that never shows a row. Spec
// section 8.3 already treats a missing explicit --config path as a startup
// error rather than a silent fallback; this extends the same principle to
// every other field the file can set.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	cfg.Path = path
	if err := cfg.validate(); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// minTierPeriod is the floor below which a tier period is rejected rather
// than accepted and clamped later. 100 ms is generous: the fastest tier
// defaults to 1 s, so this floor is ten times faster than anything the tool
// actually asks for, and still far enough from zero that a typo cannot land
// on it by accident.
const minTierPeriod = 100 * time.Millisecond

// The ceilings. An external review pointed out that validate had floors and
// no ceilings, which leaves the same class of hole the missing floors did:
// a value nobody would type on purpose that the program accepts and then
// behaves strangely because of. These are not policy, they are typo
// detection, so each is set well beyond any legitimate configuration.
//
//	maxTierPeriod   an hour between samples is not monitoring, and the
//	                CPU history tier's own minute is the slowest thing the
//	                spec asks for
//	maxRetention    a day of history, when the default is fifteen minutes
//	maxSamplesCap   at roughly 200 bytes a sample this is about 2 GB, which
//	                is where "bounded memory" stops meaning anything
//	maxBudgetMs     1000 ms of server CPU per second is one whole core of
//	                the monitored server spent on watching it; past that
//	                the budget is not a budget and the throttle can never
//	                intervene, which is exactly what the zero-period bug
//	                did from the other end
const (
	maxTierPeriod = time.Hour
	maxRetention  = 24 * time.Hour
	maxSamplesCap = 10_000_000
	maxBudgetMs   = 1000
)

// validate rejects a configuration that would either hammer the monitored
// server or silently produce an empty tool. Every check names the field and
// the value it rejected, so a typo reads as an error message rather than as
// a working default that happens to be wrong.
func (cfg Config) validate() error {
	tiers := []struct {
		field string
		d     Duration
	}{
		{"tiers.requests", cfg.Tiers.Requests},
		{"tiers.counters", cfg.Tiers.Counters},
		{"tiers.space", cfg.Tiers.Space},
		{"tiers.cpuHistory", cfg.Tiers.CPUHistory},
		{"tiers.livePlan", cfg.Tiers.LivePlan},
	}
	for _, t := range tiers {
		if t.d.Std() < minTierPeriod {
			return fmt.Errorf("%s: %s is below the floor of %s", t.field, t.d, minTierPeriod)
		}
		if t.d.Std() > maxTierPeriod {
			return fmt.Errorf("%s: %s is above the ceiling of %s; a tier that slow is not monitoring", t.field, t.d, maxTierPeriod)
		}
	}
	if cfg.Retention.Std() <= 0 {
		return fmt.Errorf("retention: %s must be positive", cfg.Retention)
	}
	if cfg.Retention.Std() > maxRetention {
		return fmt.Errorf("retention: %s is above the ceiling of %s", cfg.Retention, maxRetention)
	}
	if cfg.Budget.MaxSamples <= 0 {
		return fmt.Errorf("budget.maxSamples: %d must be positive", cfg.Budget.MaxSamples)
	}
	if cfg.Budget.MaxSamples > maxSamplesCap {
		return fmt.Errorf("budget.maxSamples: %d is above the ceiling of %d, which is already about 2 GB of retained samples", cfg.Budget.MaxSamples, maxSamplesCap)
	}
	if cfg.Budget.ServerCPUMsPerSecond <= 0 {
		return fmt.Errorf("budget.serverCpuMsPerSecond: %d must be positive", cfg.Budget.ServerCPUMsPerSecond)
	}
	if cfg.Budget.ServerCPUMsPerSecond > maxBudgetMs {
		return fmt.Errorf("budget.serverCpuMsPerSecond: %d is above the ceiling of %d, a whole core of the monitored server; past that the throttle can never intervene", cfg.Budget.ServerCPUMsPerSecond, maxBudgetMs)
	}
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port: %d is out of range 1-65535", cfg.Server.Port)
	}
	return nil
}

// Save writes cfg back to the file it came from. When it came from defaults,
// it goes beside the binary if that directory is writable, and in the user
// configuration directory otherwise.
func Save(cfg Config) (string, error) {
	path := cfg.Path
	if path == "" {
		path = filepath.Join(binaryDir(), "sqltop.yaml")
		if err := writable(binaryDir()); err != nil {
			dir := userConfigDir()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("config: %w", err)
			}
			path = filepath.Join(dir, "sqltop.yaml")
		}
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return path, nil
}

func writable(dir string) error {
	if dir == "" {
		return errors.New("no directory")
	}
	f, err := os.CreateTemp(dir, ".sqltop-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
