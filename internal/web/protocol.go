// Package web turns a collector snapshot into what the browser receives.
package web

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/collector"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

// Row is one grid line on the wire. It deliberately lacks the SQL text, the
// program name, the login and the host: those never change for a session, so
// they travel once in Refs. Section 10.1 measured SQL text at 24 % and
// program name at 7 % of a row's bytes (31 % together; login and host were
// not broken out on their own in that measurement, but they are the same
// kind of per-session invariant and travel the same way).
//
// Debt: section 10.1's other 16 % is CPU history, and it is not here at all.
// Spec section 4.4 gives it a different mechanism from the rest of this
// table: history is appended one point at a time on the client, never
// resent as a series. Nothing in this package implements that; there is no
// history field on Row or Ref, no append endpoint, and no test. The way out
// is a small ring buffer's worth of points added to Ref (history belongs
// with the session's invariants, not repeated per row) plus a client that
// appends the new point it receives instead of replacing what it already
// drew.
//
// Debt: section 8.1 lists five more grid columns this Row does not carry:
// physical_reads, wait_resource, open_tran, isolation_level, query_hash. A
// realistic row today runs about 210 bytes on the wire; adding all five
// measured out at 328 bytes, +56 %, which at 800 rows and 1 Hz is roughly
// 94 kB/s extra. Two of the five are per-statement invariants, not per-tick
// values, and belong in Ref when they land: query_hash (already used
// server-side by fingerprint below, just not sent to the client) and
// isolation_level. The other three, physical_reads, open_tran and
// wait_resource, change tick to tick and belong here on Row. Writing this
// down now is what keeps the omission a decision for whoever adds the rest
// of the grid, not something they have to rediscover.
// Row marshals as a positional array, not an object, and rowFields below
// is the order. Measured at 800 rows: the eighteen key names, their quotes
// and their colons came to 98 bytes of every 298 byte row, a third of the
// steady-state payload spent restating the same eighteen words 800 times a
// second. The client is told the order once per connection in
// SnapshotPayload.Cols rather than hard-coding it, so the two halves cannot
// drift apart: a field added here and forgotten there would otherwise shift
// every column after it silently, which is the failure mode that makes
// positional formats a bad idea when nobody sends the header.
type Row struct {
	SPID      int64   `json:"spid"`
	RequestID int32   `json:"rqid"`
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
	// Percent is ahead of its consumer: task 14's grid does not have a
	// progress column, so nothing on the client reads this field today.
	// Sent anyway because dropping it now and adding it back once the
	// column exists would be a second wire-format change for the same
	// data; the cost is a handful of extra bytes per row.
	Percent float64 `json:"pct"`
}

// rowFields is the wire order of Row's columns and the value sent as
// SnapshotPayload.Cols. TestRowFieldsMatchTheStruct checks it against Row's
// own json tags by reflection, in declaration order, so this list cannot
// fall behind the struct.
var rowFields = []string{
	"spid", "rqid", "ref", "st", "db", "cmd", "by", "d",
	"el", "cpu", "rd", "wr", "tdb", "gr", "dop", "w", "wms", "pct",
}

const hexDigits = "0123456789abcdef"

// appendJSONString writes one JSON string. Hand-rolled because this runs
// five times per row, 800 rows a second, and encoding/json's own path
// allocates a fresh []byte for each one. It matches encoding/json byte for
// byte, including the HTML escaping of < > and & that Go does by default
// and the U+FFFD substitution for invalid UTF-8; FuzzAppendJSONString
// checks that equivalence against encoding/json rather than trusting this
// comment.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8. encoding/json substitutes the replacement
			// character itself, not its escape, rather than emitting bytes
			// no JSON parser would accept. So does this.
			dst = append(dst, s[start:i]...)
			dst = utf8.AppendRune(dst, utf8.RuneError)
			i += size
			start = i
			continue
		}
		// U+2028 and U+2029 are valid JSON but are line terminators in
		// JavaScript source, so encoding/json escapes them; a payload that
		// is only ever handed to JSON.parse would not need it, but matching
		// the standard library exactly is what lets the fuzz test be a
		// simple comparison.
		if r == '\u2028' || r == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// appendJSONFloat writes one float the way encoding/json would, with the
// one difference that a NaN or an infinity becomes 0 instead of an error.
// encoding/json refuses them, which would fail the whole snapshot and blank
// the grid over one unrepresentable cell; a dashboard that loses a tick
// because one tempdb figure divided by zero somewhere upstream is a worse
// answer than a zero in that cell.
func appendJSONFloat(dst []byte, f float64) []byte {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return append(dst, '0')
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	dst = strconv.AppendFloat(dst, f, format, -1, 64)
	if format == 'e' {
		// Go prints e+09 where JSON wants e+9; encoding/json does this
		// same trim.
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

// MarshalJSON writes the row as the positional array rowFields describes.
func (r Row) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 160)
	b = append(b, '[')
	b = strconv.AppendInt(b, r.SPID, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(r.RequestID), 10)
	b = append(b, ',')
	b = appendJSONString(b, r.RefKey)
	b = append(b, ',')
	b = appendJSONString(b, r.Status)
	b = append(b, ',')
	b = appendJSONString(b, r.Database)
	b = append(b, ',')
	b = appendJSONString(b, r.Command)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.BlockedBy, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(r.Depth), 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.ElapsedMs, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.CPUMs, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.Reads, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.Writes, 10)
	b = append(b, ',')
	b = appendJSONFloat(b, r.TempdbMB)
	b = append(b, ',')
	b = appendJSONFloat(b, r.GrantMB)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(r.DOP), 10)
	b = append(b, ',')
	b = appendJSONString(b, r.WaitType)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.WaitMs, 10)
	b = append(b, ',')
	b = appendJSONFloat(b, r.Percent)
	return append(b, ']'), nil
}

// UnmarshalJSON reads the array form back. Nothing in the product needs it
// (the browser is the only consumer and it does not speak Go), but a type
// with a custom MarshalJSON and no inverse is one no test can round-trip,
// and a wire format nobody can decode in tests is a wire format nobody
// checks.
func (r *Row) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != len(rowFields) {
		return fmt.Errorf("web: row has %d columns, want %d", len(raw), len(rowFields))
	}
	targets := []any{
		&r.SPID, &r.RequestID, &r.RefKey, &r.Status, &r.Database, &r.Command,
		&r.BlockedBy, &r.Depth, &r.ElapsedMs, &r.CPUMs, &r.Reads, &r.Writes,
		&r.TempdbMB, &r.GrantMB, &r.DOP, &r.WaitType, &r.WaitMs, &r.Percent,
	}
	for i, t := range targets {
		if err := json.Unmarshal(raw[i], t); err != nil {
			return fmt.Errorf("web: row column %s: %w", rowFields[i], err)
		}
	}
	return nil
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
//
// Caps carries collector.Status.Caps, which spec section 4.1 calls the
// load-bearing piece of the whole source abstraction: the UI is supposed to
// grey what a source cannot provide and render n/a rather than a plausible
// zero, and StatusPayload is the only channel this protocol gives the
// client to learn that. It is a list of names rather than the raw bitset,
// so a JavaScript consumer can test capability membership without knowing
// Go's bit layout for model.Capabilities.
type StatusPayload struct {
	Sqltop    string `json:"sqltop"`
	Connected bool   `json:"connected"`
	Message   string `json:"message,omitempty"`
	Instance  string `json:"instance"`
	Version   string `json:"version"`
	// Host, Edition and StartedAt complete the first row of spec section
	// 6's dashboard table, "instance, host, edition, version, uptime,
	// once at connection". They are per-connection invariants and by the
	// logic of Ref above they ought to travel once rather than on every
	// tick. They do not, because the mechanism that would deliver them
	// once is the reference table, which is keyed per session of the
	// monitored server and has nothing to do with the client's own
	// connection, so this would need a second delivery mechanism built
	// for three strings. Measured at 74 bytes of a 22 kB snapshot, 0.3 %.
	// The reference table exists because SQL text and program name were
	// 31 % of the payload; this is three orders of magnitude away from
	// that and buying it a mechanism would be the abstraction rule's
	// exact failure case.
	Host    string `json:"host,omitempty"`
	Edition string `json:"edition,omitempty"`
	// Deployment is where the engine runs: see model.Deployment for what
	// each value is worth. Omitted when unknown rather than sent as an
	// empty string the page would have to special-case.
	Deployment string `json:"deployment,omitempty"`
	// StartedAt is the instance's start time in Unix milliseconds, zero
	// when unknown. Sent as an instant rather than as an uptime duration
	// so the page can count the uptime up between ticks instead of
	// freezing it at whatever the last snapshot said, and so a throttled
	// tier that slows the stream to eight seconds does not make the
	// uptime visibly stutter.
	StartedAt int64    `json:"startedAt,omitempty"`
	Caps      []string `json:"caps,omitempty"`
	// CostMsPerSecond is collector.Status.CostMsPerSecond: the tool's own
	// server CPU cost, averaged over the observation budget's sliding
	// window, spec section 10's "an instrument that claims to bound its own
	// cost should show it". Always sent, not only while throttled: before
	// that it used to reach the browser solely as an interpolated number
	// inside the throttle message, which only renders once the tool is
	// already throttled.
	CostMsPerSecond float64 `json:"costMsPerSecond"`
}

// startedAtMillis converts the instance start time for the wire, mapping
// the zero time to zero rather than to the year 1 in milliseconds. The
// client shows an uptime only for a nonzero value: a server whose start
// time was not read is one whose uptime is unknown, and "up 2025 years" is
// the plausible-looking lie the whole Available convention exists to stop.
func startedAtMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// newStatusPayload is the single place a collector.Status becomes a
// StatusPayload. It exists because there were two: Encoder.Snapshot built
// one for the stream and Server.status built another for /api/status, and
// the day host, edition and the start time were added to the struct only
// the first one learned about them, so the same tool reported different
// facts about the same server on two of its own endpoints. Adding a field
// to StatusPayload must not require remembering where else it is
// assembled.
func newStatusPayload(st collector.Status) StatusPayload {
	return StatusPayload{
		Sqltop:          buildinfo.String(),
		Connected:       st.Connected,
		Message:         st.Message,
		Instance:        st.Info.Instance,
		Version:         st.Info.ProductVersion,
		Host:            st.Info.Host,
		Edition:         st.Info.Edition,
		Deployment:      string(st.Info.Deployment),
		StartedAt:       startedAtMillis(st.Info.StartedAt),
		Caps:            capNames(st.Caps),
		CostMsPerSecond: st.CostMsPerSecond,
	}
}

// capName is checked against every bit in model.Capabilities in this fixed
// order. New capabilities in model must be added here too, or they reach
// this package's caller but never the wire.
var capName = []struct {
	cap  model.Capability
	name string
}{
	{model.CapLivePlanProgress, "livePlanProgress"},
	{model.CapInstanceWideView, "instanceWideView"},
	{model.CapTempdbPerTask, "tempdbPerTask"},
	{model.CapWaitStatsCumulative, "waitStatsCumulative"},
	{model.CapSchedulerLoad, "schedulerLoad"},
	{model.CapKillSession, "killSession"},
	{model.CapVersionStoreUsage, "versionStoreUsage"},
	{model.CapRingBufferCPU, "ringBufferCPU"},
	{model.CapRequestDOP, "requestDOP"},
}

func capNames(c model.Capabilities) []string {
	var out []string
	for _, e := range capName {
		if c.Has(e.cap) {
			out = append(out, e.name)
		}
	}
	return out
}

// SnapshotPayload is one tick as the browser sees it.
type SnapshotPayload struct {
	Seq  uint64         `json:"seq"`
	TS   int64          `json:"ts"`
	Rows []Row          `json:"rows"`
	Refs map[string]Ref `json:"refs,omitempty"`
	// Cols names the columns of every Row array, in order. Sent once per
	// connection, on the first snapshot, for the same reason the reference
	// table exists: it never changes for the life of a connection, and a
	// client that reconnects gets a fresh Encoder and is told again.
	Cols []string `json:"cols,omitempty"`
	// Figures carries the dashboard of spec section 6. It was on the wire
	// for a release before anything read it, which is why it is a map of
	// model.Figure rather than a struct: the collector merges four tiers
	// into one keyed set at different periods, and a fixed struct would
	// have to invent a zero for every tier that has not reported yet,
	// which is the one thing model.Figure.Available exists to prevent. A
	// key absent from this map and a key present with Available false are
	// deliberately different things, and the page treats them the same
	// way: no number.
	Figures map[string]model.Figure `json:"figures,omitempty"`
	Status  StatusPayload           `json:"status"`
}

// Encoder remembers which references a client already holds. One encoder per
// connected client is still the intended use (two clients may have joined at
// different times, so each needs its own view of what has already been
// sent), but mu makes a shared encoder safe rather than merely documented as
// unsafe: a lock on one map is not an abstraction worth withholding, and the
// alternative is e.sent taking a concurrent write from two goroutines,
// which is not a bug an HTTP handler's recover() can catch, only a fatal
// throw that takes the process down.
type Encoder struct {
	mu   sync.Mutex
	seq  uint64
	sent map[string]struct{} // ref keys already delivered to this client
}

// firstSnapshot reports whether the column header still has to go out. It
// is derived from seq rather than kept as its own flag: two pieces of state
// that must agree about the same thing are two pieces of state that can
// disagree.
func (e *Encoder) firstSnapshot() bool { return e.seq == 1 }

func NewEncoder() *Encoder { return &Encoder{sent: map[string]struct{}{}} }

// known reports how many sessions the encoder is currently tracking. It
// exists for the eviction test; nothing outside the package needs it.
func (e *Encoder) known() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.sent)
}

// refKey identifies one session's current reference entry. It includes a
// fingerprint of everything Ref carries, not just the session ID, so a
// session whose statement, login, host or program changes gets a new
// reference rather than the grid showing stale text under a row that has
// moved on.
//
// This was wrong in the first round in a way that matters for an operator
// who can kill sessions from this tool. SQL Server reuses session IDs
// routinely: alice on SSMS from PC1 disconnects, the server hands her
// session ID to bob on sqlcmd from PC2 running the identical SELECT 1
// (health probes and shared procedure calls make identical statement text
// across different logins ordinary, not exotic), and a key built from the
// session ID and the statement alone is unchanged. No new reference goes
// out, the client keeps alice's entry, and the grid shows bob's row
// labelled alice, SSMS, PC1. Folding Login, Host and Program into the key
// closes that.
func refKey(r model.RequestSample) string {
	return strconv.FormatInt(r.Ref.SessionID, 10) + ":" + fingerprint(r)
}

// fingerprint identifies a reference's content cheaply. It used to prefer
// QueryHash over hashing the text, on the reasoning that the engine had
// already done the work. That was the second half of the session-reuse bug:
// QueryHash is computed over the parameterised shape of a statement, so a
// session moving from WHERE id = 1 to WHERE id = 999999 keeps the same hash
// while spec section 8.1 defines sql_text as the current statement,
// extracted by offsets, literals included. Preferring the hash meant the
// grid kept showing the first literal forever.
//
// So everything that identifies a reference's content goes into one FNV-64a
// digest: Login, Host and Program (closing the session-reuse bug above),
// QueryHash when the engine supplies one (cheap and a real signal, just not
// sufficient on its own), and the SQL text itself (so a literal change is
// never missed even when QueryHash does not catch it). FNV rather than a
// cryptographic hash because nothing here is a secret, only a key to
// deduplicate on. Fields are separated by a NUL byte so "ab"+"c" cannot
// collide with "a"+"bc".
func fingerprint(r model.RequestSample) string {
	h := fnv.New64a()
	h.Write([]byte(r.Login))
	h.Write([]byte{0})
	h.Write([]byte(r.Host))
	h.Write([]byte{0})
	h.Write([]byte(r.Program))
	h.Write([]byte{0})
	h.Write([]byte(r.QueryHash))
	h.Write([]byte{0})
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
	cut := maxRefSQL
	// Back up to a rune boundary. Slicing at a raw byte count can land
	// inside a multi-byte character, producing invalid UTF-8 that a browser
	// renders as a replacement glyph on any non-ASCII batch.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n-- truncated by sqltop"
}

// Snapshot turns one tick of request samples, dashboard figures and
// collector status into what this client receives next. figures is copied
// rather than stored by reference: the collector happens to hand over a
// fresh map under its own read lock today, so aliasing it is not currently
// unsafe, but Snapshot has no way to enforce that a future caller keeps
// doing so, and the copy is a handful of entries once a second.
// model.Figure itself is passed through unwrapped either way: it already
// distinguishes a real zero from Available: false, and wrapping it here
// would only risk losing that distinction again.
func (e *Encoder) Snapshot(rows []model.RequestSample, figures map[string]model.Figure, st collector.Status) SnapshotPayload {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.seq++
	out := SnapshotPayload{
		Seq: e.seq,
		// TS is when this tick was encoded, not when any one row was
		// sampled: rows can be empty (a session churn with nothing running)
		// or span more than one sample time, and the client only needs a
		// single instant to measure its own staleness against.
		TS:     time.Now().UnixMilli(),
		Rows:   make([]Row, 0, len(rows)),
		Status: newStatusPayload(st),
	}
	if e.firstSnapshot() {
		out.Cols = rowFields
	}

	if figures != nil {
		out.Figures = make(map[string]model.Figure, len(figures))
		for k, v := range figures {
			out.Figures[k] = v
		}
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
			SPID: r.Ref.SessionID, RequestID: r.Ref.RequestID, RefKey: key, Status: r.Status, Database: r.Database,
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
