// Package dotenv reads KEY=VALUE pairs from a file into the environment.
//
// Copied rather than depended on, per the project's standard-library-first
// rule. An absent file is a no-op: secrets may legitimately come from a real
// export instead, and an explicit export always wins over the file.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
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
		if _, taken := os.LookupEnv(key); !taken {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
