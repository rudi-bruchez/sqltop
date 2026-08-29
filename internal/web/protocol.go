// Package web turns a collector snapshot into what the browser receives.
package web

import (
	"hash/fnv"
	"strconv"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

// Row is one grid line on the wire. It deliberately lacks the SQL text, the
// program name, the login and the host: those never change for a session, so
// they travel once in Refs. The bench measured them as 47 % of the payload,
// resent every second for nothing. Spec section 10.1.
type Row struct {
	SPID      int64   `json:"spid"`
	RefKey    string  `json:"ref"`
	Status    string  `json:"st"`
	Database  string  `json:"db"`
	Command   string  `json:"cmd"`
	BlockedBy int64   `json:"by"`
	Depth     int     `json:"d"`
	ElapsedMs int64   `json:"el"`
	CPUMs     int64   `json:"cpu"`
	Reads     int64   `json:"rd"`
	Writes    int64   `json:"wr"`
	TempdbMB  float64 `json:"tdb"`
	GrantMB   float64 `json:"gr"`
	DOP       int     `json:"dop"`
	WaitType  string  `json:"w"`
	WaitMs    int64   `json:"wms"`
	Percent   float64 `json:"pct"`
}

// Ref holds what stays constant for one session's current statement: sent
// once, referenced afterwards by every row that shares this key.
type Ref struct {
	SQL     string `json:"sql"`
	Program string `json:"prg"`
	Login   string `json:"login"`
	Host    string `json:"host"`
}

// StatusPayload mirrors collector.Status for the wire. Message already
// carries whatever the collector has to say, tier failures included: see
// Collector.messageLocked, which assembles it from the preflight, each
// tier's own error and the cost reader before a throttle explanation ever
// gets a turn. There is nothing to unflatten here, only to pass through.
type StatusPayload struct {
	Sqltop    string `json:"sqltop"`
	Connected bool   `json:"connected"`
	Message   string `json:"message,omitempty"`
	Instance  string `json:"instance"`
	Version   string `json:"version"`
}

// SnapshotPayload is one tick as the browser sees it.
type SnapshotPayload struct {
	Seq     uint64                  `json:"seq"`
	TS      int64                   `json:"ts"`
	Rows    []Row                   `json:"rows"`
	Refs    map[string]Ref          `json:"refs,omitempty"`
	Figures map[string]model.Figure `json:"figures,omitempty"`
	Status  StatusPayload           `json:"status"`
}

// Encoder remembers which references a client already holds. One encoder per
// connected client, since two clients may have joined at different times and
// so each needs its own view of what has already been sent.
type Encoder struct {
	seq  uint64
	sent map[string]struct{} // ref keys already delivered to this client
}

func NewEncoder() *Encoder { return &Encoder{sent: map[string]struct{}{}} }

// known reports how many sessions the encoder is currently tracking. It
// exists for the eviction test; nothing outside the package needs it.
func (e *Encoder) known() int { return len(e.sent) }

// refKey identifies one session's current statement. It includes a
// fingerprint of the statement, not just the session ID, so a session that
// moves on to a different query gets a new reference rather than the grid
// showing the previous query's text under the new one.
func refKey(r model.RequestSample) string {
	return strconv.FormatInt(r.Ref.SessionID, 10) + ":" + fingerprint(r)
}

// fingerprint identifies a statement cheaply. QueryHash is what the engine
// already computed and is preferred; when it is empty (no cached plan, for
// instance) the SQL text is hashed instead. Hashed rather than compared by
// length: an earlier version of this idea used the text's length, which
// collides whenever two different statements happen to be the same size, and
// the second session would then have displayed the first one's SQL. FNV
// rather than a cryptographic hash because nothing here is a secret, only a
// key to deduplicate on.
func fingerprint(r model.RequestSample) string {
	if r.QueryHash != "" {
		return r.QueryHash
	}
	h := fnv.New64a()
	h.Write([]byte(r.SQLText))
	return strconv.FormatUint(h.Sum64(), 16)
}

// maxRefSQL caps what a reference carries. Without a cap, a multi-statement
// batch of a few megabytes would sit in the reference table and still cross
// the wire once at full size; the grid only ever needs to display it, not
// reproduce it byte for byte.
const maxRefSQL = 64 * 1024

func clip(s string) string {
	if len(s) <= maxRefSQL {
		return s
	}
	return s[:maxRefSQL] + "\n-- truncated by sqltop"
}

// Snapshot turns one tick of request samples, dashboard figures and
// collector status into what this client receives next. Figures is passed
// through as given: model.Figure already distinguishes a real zero from
// Available: false, and wrapping it here would only risk losing that
// distinction again.
func (e *Encoder) Snapshot(rows []model.RequestSample, figures map[string]model.Figure, st collector.Status) SnapshotPayload {
	e.seq++
	out := SnapshotPayload{
		Seq: e.seq,
		// TS is when this tick was encoded, not when any one row was
		// sampled: rows can be empty (a session churn with nothing running)
		// or span more than one sample time, and the client only needs a
		// single instant to measure its own staleness against.
		TS:      time.Now().UnixMilli(),
		Rows:    make([]Row, 0, len(rows)),
		Figures: figures,
		Status: StatusPayload{
			Sqltop:    buildinfo.String(),
			Connected: st.Connected,
			Message:   st.Message,
			Instance:  st.Info.Instance,
			Version:   st.Info.ProductVersion,
		},
	}

	alive := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		key := refKey(r)
		alive[key] = struct{}{}

		if _, had := e.sent[key]; !had {
			if out.Refs == nil {
				out.Refs = map[string]Ref{}
			}
			out.Refs[key] = Ref{SQL: clip(r.SQLText), Program: r.Program, Login: r.Login, Host: r.Host}
			e.sent[key] = struct{}{}
		}

		out.Rows = append(out.Rows, Row{
			SPID: r.Ref.SessionID, RefKey: key, Status: r.Status, Database: r.Database,
			Command: r.Command, BlockedBy: r.BlockedBy, Depth: r.Depth,
			ElapsedMs: r.ElapsedMs, CPUMs: r.CPUMs, Reads: r.LogicalReads, Writes: r.Writes,
			TempdbMB: r.TempdbMB, GrantMB: r.MemoryGrantMB, DOP: r.DOP,
			WaitType: r.WaitType, WaitMs: r.WaitMs, Percent: r.PercentComplete,
		})
	}

	// Drop references nobody used this tick. Without this, a server that
	// churns connections would grow this map forever: every session that
	// ever connected would keep a reference alive, whether or not it is
	// still running.
	for k := range e.sent {
		if _, still := alive[k]; !still {
			delete(e.sent, k)
		}
	}
	return out
}
