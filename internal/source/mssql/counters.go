package mssql

import (
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// counterKind mirrors the cntr_type semantics documented for
// sys.dm_os_performance_counters. Spec section 6.
//
//	65792                  raw current value
//	272696320, 272696576   cumulative per second, the rate is a delta
//	537003264              ratio over a base counter of type 1073939712
type counterKind int

const (
	kindRaw counterKind = iota
	kindPerSecond
	kindRatio
)

type counterDef struct {
	key      string // our stable name, used by the UI
	object   string // object_name, matched with a trailing-space-tolerant LIKE
	name     string // counter_name
	kind     counterKind
	baseName string // counter_name of the base, ratios only
	unit     string
}

// counterDefs is the catalogue the collector queries. Only these rows are
// fetched: sys.dm_os_performance_counters returns roughly 1500, and pulling
// them all every second would spend the observation budget on nothing.
var counterDefs = []counterDef{
	{key: "page_life_expectancy", object: "Buffer Manager", name: "Page life expectancy", kind: kindRaw, unit: "s"},
	{key: "buffer_cache_hit_ratio", object: "Buffer Manager", name: "Buffer cache hit ratio", kind: kindRatio, baseName: "Buffer cache hit ratio base", unit: "%"},
	{key: "page_reads_sec", object: "Buffer Manager", name: "Page reads/sec", kind: kindPerSecond, unit: "/s"},
	{key: "page_writes_sec", object: "Buffer Manager", name: "Page writes/sec", kind: kindPerSecond, unit: "/s"},
	{key: "lazy_writes_sec", object: "Buffer Manager", name: "Lazy writes/sec", kind: kindPerSecond, unit: "/s"},
	{key: "batch_requests_sec", object: "SQL Statistics", name: "Batch Requests/sec", kind: kindPerSecond, unit: "/s"},
	{key: "compilations_sec", object: "SQL Statistics", name: "SQL Compilations/sec", kind: kindPerSecond, unit: "/s"},
	{key: "recompilations_sec", object: "SQL Statistics", name: "SQL Re-Compilations/sec", kind: kindPerSecond, unit: "/s"},
	{key: "full_scans_sec", object: "Access Methods", name: "Full Scans/sec", kind: kindPerSecond, unit: "/s"},
	{key: "open_transactions", object: "Transactions", name: "Transactions", kind: kindRaw, unit: ""},
	{key: "longest_transaction_s", object: "Transactions", name: "Longest Transaction Running Time", kind: kindRaw, unit: "s"},
	// Version store size and tempdb free space are deliberately absent here.
	// Both also exist as counters, but the space tier reads them from
	// sys.dm_db_file_space_usage and sys.dm_tran_version_store_space_usage,
	// which spec section 6 names and which are per-database rather than
	// instance-wide. Two sources for one number is how dashboards start
	// disagreeing with themselves.
	{key: "target_server_memory_kb", object: "Memory Manager", name: "Target Server Memory (KB)", kind: kindRaw, unit: "KB"},
	{key: "total_server_memory_kb", object: "Memory Manager", name: "Total Server Memory (KB)", kind: kindRaw, unit: "KB"},
	{key: "memory_grants_pending", object: "Memory Manager", name: "Memory Grants Pending", kind: kindRaw, unit: ""},
}

// baseKey is the map key under which a ratio's denominator is delivered.
func baseKey(key string) string { return key + "__base" }

type counterState struct {
	prev   map[string]int64
	prevAt time.Time
	seeded bool
}

func newCounterState() *counterState {
	return &counterState{prev: map[string]int64{}}
}

// apply turns one raw reading into displayable figures, differentiating what
// has to be differentiated. Figures that cannot be computed yet come back
// with Available false rather than zero: the difference between "not known"
// and "genuinely nothing" is the difference between a useful dashboard and a
// misleading one.
func (s *counterState) apply(at time.Time, raw map[string]int64) map[string]model.Figure {
	out := make(map[string]model.Figure, len(counterDefs))
	elapsed := at.Sub(s.prevAt).Seconds()

	for _, d := range counterDefs {
		cur, ok := raw[d.key]
		if !ok {
			out[d.key] = model.Figure{Unit: d.unit, Available: false}
			continue
		}

		switch d.kind {
		case kindRaw:
			out[d.key] = model.Figure{Value: float64(cur), Unit: d.unit, Available: true}

		case kindPerSecond:
			prev, had := s.prev[d.key]
			switch {
			case !s.seeded || !had || elapsed <= 0:
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			case cur < prev:
				// Went backwards: the instance restarted. Skip this tick.
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			default:
				out[d.key] = model.Figure{Value: float64(cur-prev) / elapsed, Unit: d.unit, Available: true}
			}

		case kindRatio:
			bk := baseKey(d.key)
			curBase, hasBase := raw[bk]
			prev, had := s.prev[d.key]
			prevBase, hadBase := s.prev[bk]
			dn := curBase - prevBase
			switch {
			case !s.seeded || !had || !hadBase || !hasBase:
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			case cur < prev || curBase < prevBase:
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			case dn <= 0:
				// No lookups in the interval. Reporting zero percent would
				// claim every read missed the cache.
				out[d.key] = model.Figure{Unit: d.unit, Available: false}
			default:
				out[d.key] = model.Figure{Value: float64(cur-prev) / float64(dn) * 100, Unit: d.unit, Available: true}
			}
		}
	}

	for k, v := range raw {
		s.prev[k] = v
	}
	s.prevAt = at
	s.seeded = true
	return out
}
