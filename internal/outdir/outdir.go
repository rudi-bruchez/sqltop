// Package outdir resolves the directories sqltop writes beside its own
// executable, and creates files in them without ever overwriting one.
//
// Beside the executable rather than in a home directory because spec section
// 7 puts them there: a portable install carries its own output, and a DBA who
// copied the binary to a jump box finds it next to the binary.
package outdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Beside names a directory next to the running executable. It does not
// create it; Create does, when there is something to put in it.
func Beside(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

// Create opens a new file and hands it back still open, for a caller that
// writes over time. The names have one second of resolution, so two within
// the same second would otherwise land on each other; the numeric suffix is
// not a feature, it is the alternative to losing a file somebody asked for.
func Create(dir, base, ext string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	for n := 1; n <= 9; n++ {
		name := base + ext
		if n > 1 {
			name = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return f, path, nil
	}
	return nil, "", fmt.Errorf("outdir: nine files already exist for %s", base)
}

// Write is Create for a caller that has the whole body already.
func Write(dir, base, ext string, body []byte) (string, error) {
	f, path, err := Create(dir, base, ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
