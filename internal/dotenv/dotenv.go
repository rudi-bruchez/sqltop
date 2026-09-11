// Package dotenv reads KEY=VALUE pairs from a file into the environment,
// and writes one back.
//
// Copied rather than depended on, per the project's standard-library-first
// rule. An absent file is a no-op: secrets may legitimately come from a real
// export instead, and an explicit export always wins over the file.
package dotenv

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// entry is one line of the file as Load reads it.
type entry struct {
	line       string // trimmed and without its export prefix, for warnings
	key, value string
	ok         bool // false when the line has no "="
}

// read parses one raw line, or returns nil for a blank line or a comment.
// Load and Set both go through it, so the line Set replaces is exactly the
// line Load would have read as the definition.
func read(raw string) *entry {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, ok := strings.Cut(line, "=")
	return &entry{line: line, key: strings.TrimSpace(key), value: strings.TrimSpace(value), ok: ok}
}

// Load reads path into the environment and returns one warning per line it
// could not parse. A malformed line is a warning rather than an error
// because the project rule is that a missing secret degrades a feature and
// never stops the tool; but skipping it in silence turns a colon typed
// instead of an equals sign into a missing connection string, with nothing
// at startup to connect the two.
func Load(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var warnings []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		e := read(sc.Text())
		if e == nil {
			continue
		}
		if !e.ok {
			warnings = append(warnings, fmt.Sprintf("%s line %d: no = in %q, ignored", path, n, e.line))
			continue
		}
		key, value := e.key, e.value
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			warnings = append(warnings, fmt.Sprintf("%s line %d: empty name before =, ignored", path, n))
			continue
		}
		if _, taken := os.LookupEnv(key); !taken {
			if err := os.Setenv(key, value); err != nil {
				return warnings, err
			}
		}
	}
	return warnings, sc.Err()
}

// Set makes key=value the definition of key in the file at path. The first
// line Load would read as that definition is replaced and any later one
// dropped; every other line is kept byte for byte. A missing file is created
// with mode 0600, an existing one keeps its mode.
func Set(path, key, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("dotenv: the value for %s contains a line break", key)
	}
	// Through a link, the file to rewrite is its target: renaming over the
	// link would replace it with a copy and leave the target stale.
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	mode := fs.FileMode(0o600)
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = fi.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	def := key + "=" + value
	var b strings.Builder
	done := false
	for _, raw := range strings.SplitAfter(string(old), "\n") {
		if raw == "" {
			continue // SplitAfter's empty last element after a final newline
		}
		body := strings.TrimRight(raw, "\r\n")
		if e := read(body); e != nil && e.ok && e.key == key {
			if !done {
				b.WriteString(def + raw[len(body):])
				done = true
			}
			continue
		}
		b.WriteString(raw)
	}
	if !done {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(def + "\n")
	}

	// Through a temporary file, so a crash never leaves half a .env. Rename
	// installs the temporary file's mode, hence the Chmod.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
