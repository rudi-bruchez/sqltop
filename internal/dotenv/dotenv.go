// Package dotenv reads KEY=VALUE pairs from a file into the environment.
//
// Copied rather than depended on, per the project's standard-library-first
// rule. An absent file is a no-op: secrets may legitimately come from a real
// export instead, and an explicit export always wins over the file.
package dotenv

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Load reads path into the environment and returns one warning per line it
// could not parse. A malformed line is a warning rather than an error
// because the project rule is that a missing secret degrades a feature and
// never stops the tool; but skipping it in silence, which is what this used
// to do, turns a colon typed instead of an equals sign into "no instance to
// connect to" at startup with nothing to connect the two. Reported after an
// external reviewer typed exactly that typo and watched the error say
// nothing useful.
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
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s line %d: no = in %q, ignored", path, n, line))
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
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
