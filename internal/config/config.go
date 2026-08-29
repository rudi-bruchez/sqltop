// Package config loads sqltop's settings and decides which file they came from.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Duration is a time.Duration that marshals as "1s", "15m".
type Duration time.Duration

func (d Duration) String() string               { return time.Duration(d).String() }
func (d Duration) Std() time.Duration           { return time.Duration(d) }
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: %q is not a duration: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

type Instance struct {
	Name string `json:"name"`
	DSN  string `json:"dsn"`
}

type Tiers struct {
	Requests   Duration `json:"requests"`
	Counters   Duration `json:"counters"`
	Space      Duration `json:"space"`
	CPUHistory Duration `json:"cpuHistory"`
	LivePlan   Duration `json:"livePlan"`
}

type Server struct {
	Port int `json:"port"`
}

type Budget struct {
	// ServerCPUMsPerSecond is the ceiling from spec section 10.
	ServerCPUMsPerSecond int `json:"serverCpuMsPerSecond"`
	// MaxSamples caps the retention window so memory stays bounded on a
	// busy server, where 15 minutes of history would otherwise grow without
	// limit. See spec section 4.2 and task 3.
	MaxSamples int `json:"maxSamples"`
}

type Config struct {
	Instances []Instance      `json:"instances"`
	Tiers     Tiers           `json:"tiers"`
	Retention Duration        `json:"retention"`
	Server    Server          `json:"server"`
	Budget    Budget          `json:"budget"`
	Layouts   json.RawMessage `json:"layouts,omitempty"`

	// Path is the file this came from, empty when built-in defaults were
	// used. The status bar names it, so it must survive loading.
	Path string `json:"-"`
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
		p := filepath.Join(dir, "sqltop.json")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", nil
}

// Load reads path over the built-in defaults, so a partial file is valid.
// An empty path yields the defaults untouched.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

// Save writes cfg back to the file it came from. When it came from defaults,
// it goes beside the binary if that directory is writable, and in the user
// configuration directory otherwise.
func Save(cfg Config) (string, error) {
	path := cfg.Path
	if path == "" {
		path = filepath.Join(binaryDir(), "sqltop.json")
		if err := writable(binaryDir()); err != nil {
			dir := userConfigDir()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("config: %w", err)
			}
			path = filepath.Join(dir, "sqltop.json")
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
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
