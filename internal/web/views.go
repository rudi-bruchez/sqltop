package web

import (
	"encoding/json"
	"net/http"
)

// The three views of spec section 7 that are not projections of the
// retention window. Each is one GET that runs its query and answers; there
// is no tier and no period, because a query only runs while somebody has
// that tab open. That is the same rule the request grid follows from the
// other end (it drops the rows nobody reads rather than paying to describe
// them), and for the lock aggregate it is the whole reason the view is
// affordable at all.
//
// The rows are plain JSON objects rather than the positional arrays the
// grid uses. The grid's arrays bought back a third of a payload sent 800
// rows at a time, once a second; these are tens of rows at human pace, and
// the same trick here would be cost without a saving.

// sessionRow is one open user session on the wire.
type sessionRow struct {
	SPID      int64   `json:"spid"`
	Login     string  `json:"login"`
	Host      string  `json:"host"`
	Program   string  `json:"program"`
	Status    string  `json:"status"`
	Database  string  `json:"database"`
	Connected int64   `json:"connected"`
	Idle      int64   `json:"idle"`
	OpenTran  int     `json:"open_tran"`
	TranAge   int64   `json:"tran_age"`
	CPUMs     int64   `json:"cpu_ms"`
	Reads     int64   `json:"logical_reads"`
	Writes    int64   `json:"writes"`
	MemoryMB  float64 `json:"memory_mb"`
}

func (s *Server) sessions(rw http.ResponseWriter, req *http.Request) {
	rows, err := s.col.Sessions(req.Context())
	if err != nil {
		viewError(rw, err)
		return
	}
	out := make([]sessionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionRow{
			SPID: r.SessionID, Login: r.Login, Host: r.Host, Program: r.Program,
			Status: r.Status, Database: r.Database,
			Connected: r.ConnectedSec, Idle: r.IdleSec,
			OpenTran: r.OpenTran, TranAge: r.TranSec,
			CPUMs: r.CPUMs, Reads: r.Reads, Writes: r.Writes, MemoryMB: r.MemoryMB,
		})
	}
	writeJSON(rw, map[string]any{"rows": out})
}

// tranRow is one open transaction, lockRow one group of locks.
type tranRow struct {
	XID        int64   `json:"xid"`
	SPID       int64   `json:"spid"`
	Name       string  `json:"name"`
	Age        int64   `json:"age"`
	State      string  `json:"state"`
	Type       string  `json:"type"`
	Database   string  `json:"database"`
	Databases  int     `json:"databases"`
	LogMB      float64 `json:"log_mb"`
	LogRecords int64   `json:"log_records"`
}

type lockRow struct {
	SPID         int64  `json:"spid"`
	Database     string `json:"database"`
	ResourceType string `json:"resource_type"`
	Object       string `json:"object"`
	Mode         string `json:"mode"`
	Status       string `json:"status"`
	Count        int64  `json:"count"`
}

func (s *Server) transactions(rw http.ResponseWriter, req *http.Request) {
	trans, locks, err := s.col.Transactions(req.Context())
	if err != nil {
		viewError(rw, err)
		return
	}
	tr := make([]tranRow, 0, len(trans))
	for _, r := range trans {
		tr = append(tr, tranRow{
			XID: r.TransactionID, SPID: r.SessionID, Name: r.Name, Age: r.ElapsedSec,
			State: r.State, Type: r.Type, Database: r.Database, Databases: r.Databases,
			// Megabytes, because a transaction that has written 4.2 GB of
			// log is the interesting one and nobody reads that in bytes.
			LogMB: float64(r.LogBytes) / (1024 * 1024), LogRecords: r.LogRecords,
		})
	}
	lr := make([]lockRow, 0, len(locks))
	for _, r := range locks {
		lr = append(lr, lockRow{
			SPID: r.SessionID, Database: r.Database, ResourceType: r.ResourceType,
			Object: r.Object, Mode: r.Mode, Status: r.Status, Count: r.Count,
		})
	}
	writeJSON(rw, map[string]any{"rows": tr, "locks": lr})
}

type logRow struct {
	Database      string  `json:"database"`
	SizeMB        float64 `json:"size_mb"`
	UsedMB        float64 `json:"used_mb"`
	UsedPercent   float64 `json:"used_percent"`
	ReuseWait     string  `json:"reuse_wait"`
	RecoveryModel string  `json:"recovery_model"`
	State         string  `json:"state"`
}

func (s *Server) logs(rw http.ResponseWriter, req *http.Request) {
	rows, err := s.col.LogSpace(req.Context())
	if err != nil {
		viewError(rw, err)
		return
	}
	out := make([]logRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, logRow{
			Database: r.Database, SizeMB: r.SizeMB, UsedMB: r.UsedMB,
			UsedPercent: r.UsedPercent, ReuseWait: r.ReuseWait,
			RecoveryModel: r.RecoveryModel, State: r.State,
		})
	}
	writeJSON(rw, map[string]any{"rows": out})
}

// viewError answers with the reason rather than a bare 500. These views
// fail for one reason far more often than any other, a login that cannot
// see past its own session, and a page that said only "error" would send
// the operator looking at the wrong thing.
func viewError(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(rw).Encode(map[string]string{"error": err.Error()})
}
