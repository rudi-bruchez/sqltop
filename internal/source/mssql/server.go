package mssql

import (
	"context"
	"database/sql"
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

func (s *Source) SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error) {
	out := model.ServerSample{At: time.Now(), Figures: map[string]model.Figure{}}

	switch tier {
	case model.TierCounters:
		raw, err := s.readCounters(ctx)
		if err != nil {
			return out, err
		}
		out.Figures = s.counter.apply(out.At, raw)

	case model.TierSpace:
		if err := s.readSpace(ctx, out.Figures); err != nil {
			return out, err
		}

	case model.TierCPUHistory:
		if err := s.readCPUHistory(ctx, out.Figures); err != nil {
			return out, err
		}
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

// hasInstanceWideView reports whether sys.dm_os_performance_counters and
// tempdb.sys.dm_db_file_space_usage are worth attempting. Both require VIEW
// SERVER STATE (VIEW SERVER PERFORMANCE STATE from SQL Server 2022), which
// probe already tested as CapInstanceWideView for on-premises and Managed
// Instance. Azure SQL Database is the one server this was never tested on -
// probe skips the whole HAS_PERMS_BY_NAME check there, since a scoped single
// database answers permission questions differently - but the views
// themselves exist and answer for the current database under a permission
// commonly granted there. IsAzureSQLDB alone stands in for the untested
// capability rather than skipping the query outright: the object is not
// absent, only differently gated.
func hasInstanceWideView(info model.ServerInfo, caps model.Capabilities) bool {
	return info.IsAzureSQLDB || caps.Has(model.CapInstanceWideView)
}

func (s *Source) readCounters(ctx context.Context) (map[string]int64, error) {
	info, caps := s.snapshot()
	if !hasInstanceWideView(info, caps) {
		// Known in advance from the probe: a login without the right would
		// otherwise pay for a failing round trip every second, forever.
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
			// The one case hasInstanceWideView could not rule out ahead of
			// time: an Azure SQL Database tier whose service objective needs
			// a role this login does not hold.
			return nil, nil
		}
		return nil, fmt.Errorf("mssql: counters: %w", err)
	}

	raw := make(map[string]int64, len(counterDefs))
	for _, d := range counterDefs {
		for k, v := range byName {
			obj, n, _ := strings.Cut(k, "|")
			if !strings.HasSuffix(obj, d.object) {
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
// needs a version gate. It does need the capability gate hasInstanceWideView
// applies in readSpace: the permission this view requires is the same one
// the performance counters need.
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
	info, caps := s.snapshot()

	into["tempdb_used_mb"] = model.Figure{Unit: "MB", Available: false}
	into["tempdb_free_mb"] = model.Figure{Unit: "MB", Available: false}
	if hasInstanceWideView(info, caps) {
		var usedMB, freeMB float64
		err := s.queryRow(ctx, spaceQuery, &usedMB, &freeMB)
		switch {
		case err == nil:
			into["tempdb_used_mb"] = model.Figure{Value: usedMB, Unit: "MB", Available: true}
			into["tempdb_free_mb"] = model.Figure{Value: freeMB, Unit: "MB", Available: true}
		case isCapabilityAbsent(err):
			// Left unavailable above: the Azure SQL Database case
			// hasInstanceWideView could not rule out ahead of time.
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
		if err := s.queryRow(ctx, versionStoreQuery, &kb); err == nil {
			into["version_store_mb"] = model.Figure{Value: kb / 1024.0, Unit: "MB", Available: true}
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
	if err := s.queryRow(ctx, cpuHistoryQuery, &sqlCPU, &idle); err != nil {
		return nil // absent history is not an error, it is an unavailable figure
	}
	into["sql_cpu_percent"] = model.Figure{Value: float64(sqlCPU), Unit: "%", Available: true}
	into["other_cpu_percent"] = model.Figure{Value: float64(100 - idle - sqlCPU), Unit: "%", Available: true}
	return nil
}
