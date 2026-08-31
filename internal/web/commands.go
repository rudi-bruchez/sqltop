package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
	"github.com/rudi-bruchez/sqltop/internal/outdir"
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
var snapshotDir = func() (string, error) { return outdir.Beside("snapshots") }

// planDir is where the plan command writes, on the same terms.
var planDir = func() (string, error) { return outdir.Beside("plans") }

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

	path, err := outdir.Write(dir, time.Now().Format("server-2006-01-02-150405"), ".html", body)
	if err != nil {
		http.Error(rw, "could not write the snapshot", http.StatusInternalServerError)
		return
	}
	writeJSON(rw, map[string]string{"path": path})
}

// plansave writes the selected request's estimated plan to
// plans/plan-<spid>-yyyy-mm-dd-hhmmss.sqlplan beside the binary. The
// extension is what SQL Server Management Studio opens as a plan rather
// than as text, which is the point of saving one at all.
func (s *Server) plansave(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref, err := refFromQuery(req)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	// The live plan first, because it is the one worth having: an estimate
	// that turned out wrong is only visible beside what actually happened.
	// A server that cannot keep row counts, or a statement it is not
	// profiling, falls back to the plan as compiled rather than to nothing,
	// and the answer says which arrived so a directory of saved plans is
	// self-describing.
	plan, err := s.col.Plan(req.Context(), ref, true)
	if err != nil {
		plan, err = s.col.Plan(req.Context(), ref, false)
	}
	if err != nil {
		viewError(rw, err)
		return
	}
	kind := "estimated"
	if plan.Live {
		kind = "live"
	}

	dir, err := planDir()
	if err != nil {
		http.Error(rw, "could not work out where the executable lives", http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(rw, "could not create "+dir, http.StatusInternalServerError)
		return
	}
	base := fmt.Sprintf("plan-%d-%s-%s", ref.SessionID, kind, time.Now().Format("2006-01-02-150405"))
	path, err := outdir.Write(dir, base, ".sqlplan", plan.Payload)
	if err != nil {
		http.Error(rw, "could not write the plan", http.StatusInternalServerError)
		return
	}
	writeJSON(rw, map[string]string{"path": path, "kind": kind})
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

	// A clone, not the shared value: Config carries maps, and Validate
	// walks them. See cloneConfig.
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()
	cfg.Tiers.Requests = config.Duration(d)
	if err := cfg.Validate(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	s.col.SetPeriod(model.TierRequests, d)
	writeJSON(rw, map[string]string{"period": d.String()})
}
