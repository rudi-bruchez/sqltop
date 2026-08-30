package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

// maxSnapshotBody bounds what the snapshot endpoint will read. A snapshot
// is the visible page with every row of the grid written out in full, which
// at eight hundred rows is a few hundred kilobytes; this is generous enough
// that no honest one is refused and small enough that a runaway client
// cannot make the process read forever.
const maxSnapshotBody = 16 << 20

// snapshotDir is where the s command writes. Beside the executable, which
// is where spec section 7 puts it: a portable install carries its own
// snapshots, and a DBA who copied the binary to a jump box finds them next
// to it rather than in a home directory they may not have.
//
// A package variable so the tests can point it somewhere temporary. Not an
// environment lookup, for the reason config's own seams are not: a variable
// nobody can set from outside cannot silently redirect where a user's files
// land.
var snapshotDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "snapshots"), nil
}

// snapshot writes the posted page to snapshots/server-yyyy-mm-dd-hhmmss.html.
//
// The body is composed by the browser rather than rendered here, because
// what the s command saves is what is on screen: the filters in force, the
// sort, the dashboard as it stands. Rendering that a second time in Go
// would be a second implementation of the whole interface, kept in step
// with the first by hope.
func (s *Server) snapshot(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, req.Body, maxSnapshotBody))
	if err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(rw, "empty snapshot", http.StatusBadRequest)
		return
	}

	dir, err := snapshotDir()
	if err != nil {
		http.Error(rw, "could not work out where the executable lives", http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(rw, "could not create "+dir, http.StatusInternalServerError)
		return
	}

	path, err := writeSnapshot(dir, time.Now(), body)
	if err != nil {
		http.Error(rw, "could not write the snapshot", http.StatusInternalServerError)
		return
	}
	writeJSON(rw, map[string]string{"path": path})
}

// writeSnapshot creates the file without ever overwriting one. The name has
// one second of resolution, so two presses of s inside the same second
// would otherwise land on each other; the suffix is not a feature, it is
// the alternative to losing a file somebody asked for.
func writeSnapshot(dir string, at time.Time, body []byte) (string, error) {
	base := at.Format("server-2006-01-02-150405")
	for n := 1; n <= 9; n++ {
		name := base + ".html"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.html", base, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := f.Write(body); err != nil {
			return "", err
		}
		return path, f.Close()
	}
	return "", fmt.Errorf("web: nine snapshots already exist for %s", base)
}

// periodRequest is what the f command posts: the new base period for the
// request tier, written the way the configuration file writes one.
type periodRequest struct {
	Period string `json:"period"`
}

// period changes how often the tool samples the monitored server. Spec
// section 7's f command.
//
// It changes the sampling rate, not merely the rate at which the browser
// redraws, because that is the number that costs the monitored server
// anything: a DBA who slows the tool down on a struggling instance means
// the instance, not their screen. It does not touch the file; a rate picked
// for the next ten minutes is not a preference.
//
// The value is validated by assembling the configuration it would produce
// and running config.Validate over it, so a period from the interface
// passes exactly the floor and ceiling a period typed into the file does.
func (s *Server) period(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in periodRequest
	if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 1<<10)).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	d, err := time.ParseDuration(in.Period)
	if err != nil {
		http.Error(rw, fmt.Sprintf("%q is not a duration", in.Period), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	cfg.Tiers.Requests = config.Duration(d)
	if err := cfg.Validate(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	s.col.SetPeriod(model.TierRequests, d)
	writeJSON(rw, map[string]string{"period": d.String()})
}
