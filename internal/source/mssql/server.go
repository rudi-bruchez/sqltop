package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// countersQuery pulls only the catalogue's rows. object_name is CHAR-padded,
// hence the LTRIM/RTRIM comparison rather than equality.
var countersQuery = buildCountersQuery()

func buildCountersQuery() string {
	var names []string
	seen := map[string]bool{}
	for _, d := range counterDefs {
		for _, n := range []string{d.name, d.baseName} {
			if n != "" && !seen[n] {
				seen[n] = true
				names = append(names, "N'"+strings.ReplaceAll(n, "'", "''")+"'")
			}
		}
	}
	return `
SELECT RTRIM(LTRIM(object_name)), RTRIM(LTRIM(counter_name)), cntr_value
FROM sys.dm_os_performance_counters
WHERE RTRIM(LTRIM(counter_name)) IN (` + strings.Join(names, ",") + `)
  AND (instance_name IS NULL OR RTRIM(LTRIM(instance_name)) IN (N'', N'_Total'))
OPTION (RECOMPILE, MAXDOP 1)`
}

// SampleServer feeds the dashboard on the slower tiers. Called before
// Identify has ever run, s.caps is the zero value, so every capability-gated
// read below finds nothing granted and the sample comes back with every
// figure present and unavailable rather than an error: an honest, empty
// dashboard rather than a crash. Task 11 orders Identify before the first
// SampleServer call, so this is a startup-ordering guard rather than a path
// the collector is meant to exercise on every tick.
func (s *Source) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	out := model.ServerSample{At: time.Now()}

	switch tier {
	case model.TierCounters:
		raw, err := s.readCounters(ctx)
		if err != nil {
			return out, err
		}
		// counterState.apply is safe to call concurrently with a query on
		// this Source only because the collection plan runs one goroutine
		// per tier (spec section 10); apply itself mutates counterState's
		// prev/prevAt/seeded fields with no locking of its own. Taking s.mu
		// here costs nothing on the one-goroutine-per-tier path and stops
		// that invariant from being the only thing standing between this
		// call and a data race.
		s.mu.Lock()
		out.Figures = s.counter.apply(out.At, raw)
		s.mu.Unlock()

	case model.TierSpace:
		out.Figures = map[string]model.Figure{}
		if err := s.readSpace(ctx, out.Figures); err != nil {
			return out, err
		}

	case model.TierCPUHistory:
		out.Figures = map[string]model.Figure{}
		if err := s.readCPUHistory(ctx, out.Figures); err != nil {
			return out, err
		}

	default:
		return out, fmt.Errorf("mssql: sample server: unsupported tier %s", tier)
	}
	return out, nil
}

// snapshot returns the version and capabilities Identify last found, taken
// under the same lock every query on this Source uses. Identify may be
// re-probing on the collector's goroutine while a sampling tier reads these
// fields on its own, and mssql.go's Identify writes both under s.mu for
// exactly this reason.
func (s *Source) snapshot() (model.ServerInfo, model.Capabilities) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info, s.caps
}

func (s *Source) readCounters(ctx context.Context) (map[string]int64, error) {
	_, caps := s.snapshot()
	if !caps.Has(model.CapInstanceWideView) {
		// Known in advance from the probe, on every server including Azure
		// SQL Database (mssql.go's probe tests the capability there too, by
		// reading the two views themselves rather than asking
		// HAS_PERMS_BY_NAME): a login without the right would otherwise pay
		// for a failing round trip every second, forever.
		return nil, nil
	}

	byName := map[string]int64{}
	err := s.query(ctx, countersQuery, func(rows *sql.Rows) error {
		var object, name string
		var value int64
		if err := rows.Scan(&object, &name, &value); err != nil {
			return err
		}
		byName[object+"|"+name] = value
		return nil
	})
	if err != nil {
		if isCapabilityAbsent(err) {
			// The probe answered "yes" and rights changed since, or a rare
			// permission edge the probe's own query did not hit. Either way
			// the server itself said no, not the connection.
			return nil, nil
		}
		return nil, fmt.Errorf("mssql: counters: %w", err)
	}

	raw := make(map[string]int64, len(counterDefs))
	for _, d := range counterDefs {
		for k, v := range byName {
			obj, n, _ := strings.Cut(k, "|")
			// ":"+d.object, not d.object alone: two object names that share
			// a trailing word - "Buffer Manager" and "Memory Manager" both
			// end in "Manager" - must not be able to match each other's
			// suffix. The colon is what SQL Server always puts right before
			// the object name, so anchoring on it makes this an exact match
			// at the same cost as the loose one.
			if !strings.HasSuffix(obj, ":"+d.object) {
				continue
			}
			if n == d.name {
				raw[d.key] = v
			}
			if d.baseName != "" && n == d.baseName {
				raw[baseKey(d.key)] = v
			}
		}
	}
	return raw, nil
}

// spaceQuery covers only what the performance counters do not. Server memory
// lives in the counter catalogue instead, both because it belongs in the same
// single round trip as the rest and because memory pressure moves faster than
// the five second space tier would show it.
//
// Every column read here has been on sys.dm_db_file_space_usage since before
// SQL Server 2012, the tool's own floor (spec section 3), so nothing here
// needs a version gate. It does need the capability gate readSpace applies:
// the permission this view requires is the same one the performance
// counters need, CapInstanceWideView.
const spaceQuery = `
SELECT
    (SELECT SUM(user_object_reserved_page_count + internal_object_reserved_page_count
              + version_store_reserved_page_count) * 8.0 / 1024.0
       FROM tempdb.sys.dm_db_file_space_usage),
    (SELECT SUM(unallocated_extent_page_count) * 8.0 / 1024.0
       FROM tempdb.sys.dm_db_file_space_usage)
OPTION (RECOMPILE, MAXDOP 1)`

// versionStoreQuery is its own view and its own capability, not part of
// spaceQuery above: sys.dm_tran_version_store_space_usage is cheap by
// documentation, since it does not walk individual version records, while
// dm_db_file_space_usage's version-store column would. Gated on
// CapVersionStoreUsage, which probe already tested directly rather than
// inferring from the version - the view exists from SQL Server 2016 SP2
// onward plus both Azure engines, and a probe answers that in one shape
// instead of three.
const versionStoreQuery = `
SELECT ISNULL(SUM(reserved_space_kb), 0) FROM sys.dm_tran_version_store_space_usage
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) readSpace(ctx context.Context, into map[string]model.Figure) error {
	_, caps := s.snapshot()

	into["tempdb_used_mb"] = model.Figure{Unit: "MB", Available: false}
	into["tempdb_free_mb"] = model.Figure{Unit: "MB", Available: false}
	if caps.Has(model.CapInstanceWideView) {
		var usedMB, freeMB float64
		err := s.queryRow(ctx, spaceQuery, &usedMB, &freeMB)
		switch {
		case err == nil:
			into["tempdb_used_mb"] = model.Figure{Value: usedMB, Unit: "MB", Available: true}
			into["tempdb_free_mb"] = model.Figure{Value: freeMB, Unit: "MB", Available: true}
		case isCapabilityAbsent(err):
			// Left unavailable above: the probe said yes and rights changed
			// since, the same edge readCounters allows for.
		default:
			return fmt.Errorf("mssql: space: %w", err)
		}
	}

	// Version store size and tempdb free space are deliberately kept apart
	// from the counter catalogue. Both also exist as counters, but this tier
	// reads them from sys.dm_db_file_space_usage and
	// sys.dm_tran_version_store_space_usage, which spec section 6 names and
	// which are per-database rather than instance-wide. Two sources for one
	// number is how dashboards start disagreeing with themselves.
	into["version_store_mb"] = model.Figure{Unit: "MB", Available: false}
	if caps.Has(model.CapVersionStoreUsage) {
		var kb float64
		err := s.queryRow(ctx, versionStoreQuery, &kb)
		switch {
		case err == nil:
			into["version_store_mb"] = model.Figure{Value: kb / 1024.0, Unit: "MB", Available: true}
		case isCapabilityAbsent(err):
			// Left unavailable above.
		default:
			return fmt.Errorf("mssql: version store: %w", err)
		}
	}
	return nil
}

// cpuHistoryQuery reads the most recent scheduler-monitor record. The engine
// writes one a minute and keeps 256; both figures are its own, not settings.
const cpuHistoryQuery = `
SELECT TOP (1)
    record.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int'),
    record.value('(./Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int')
FROM (
    SELECT CONVERT(xml, record) AS record, timestamp
    FROM sys.dm_os_ring_buffers
    WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
      AND record LIKE '%<SystemHealth>%'
) AS x
ORDER BY timestamp DESC
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) readCPUHistory(ctx context.Context, into map[string]model.Figure) error {
	// The keys always exist so one tile can be unavailable without its
	// neighbours disappearing. Spec section 4.1.
	into["sql_cpu_percent"] = model.Figure{Unit: "%", Available: false}
	into["other_cpu_percent"] = model.Figure{Unit: "%", Available: false}

	// CapRingBufferCPU is already the right gate on its own: probe tests it
	// directly except on Azure SQL Database, which it skips outright because
	// a scoped single database has no OS-level ring buffer to read - the
	// object itself is absent there, not just differently permissioned, so
	// IsAzureSQLDB does not need to be asked again here.
	_, caps := s.snapshot()
	if !caps.Has(model.CapRingBufferCPU) {
		return nil
	}
	var sqlCPU, idle int
	err := s.queryRow(ctx, cpuHistoryQuery, &sqlCPU, &idle)
	switch {
	case err == nil:
		// fall through to the figures below
	case errors.Is(err, sql.ErrNoRows) || isCapabilityAbsent(err):
		// No row at all is the normal state for the first minute after an
		// instance starts: the engine writes one SchedulerMonitor record a
		// minute, and probe grants the capability from a COUNT(*), which
		// answers whether the login may read the buffer rather than whether
		// there is anything in it yet. An empty buffer is an unavailable
		// figure, not a failure.
		return nil
	default:
		return fmt.Errorf("mssql: cpu history: %w", err)
	}

	into["sql_cpu_percent"] = model.Figure{Value: float64(sqlCPU), Unit: "%", Available: true}

	// SystemIdle is not populated by SQL Server on Linux: every record this
	// tool has read from a Linux container's ring buffer carries idle == 0
	// while ProcessUtilization moves normally, which is also the platform
	// this project's own tests run on. Publishing other = 100 - idle - sqlCPU
	// there would report other processes pegged at 100% forever on a box
	// sitting idle, exactly the plausible-looking lie Available exists to
	// prevent. This costs a true reading on a genuinely saturated Windows
	// host, where idle is populated and the subtraction means something, but
	// unavailable beats wrong.
	if idle > 0 {
		other := 100 - idle - sqlCPU
		if other < 0 {
			// Rounding across two independently sampled percentages can
			// undershoot zero by a point or two; a negative "other" is not
			// a plausible reading either.
			other = 0
		}
		into["other_cpu_percent"] = model.Figure{Value: float64(other), Unit: "%", Available: true}
	}
	return nil
}
