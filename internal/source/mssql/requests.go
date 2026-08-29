package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// requestsQueryTemplate is the hot path, rendered once per server by
// buildRequestsQuery and run once per requests tier thereafter.
//
// Two deliberate choices in the fixed part. The statement text comes from
// the offsets rather than the whole batch, so the grid shows what is
// running now. And the tool filters out its own spid: reporting its own
// collection query as server activity would be both noise and a small lie.
//
// tempdb_mb is allocation minus deallocation (spec section 8.1), not
// allocation alone: a request that spilled 4 GB to tempdb and freed it
// again must not keep reporting 4096 MB forever, or sorting by tempdb_mb
// would rank it above a request actually holding 2 GB right now.
const requestsQueryTemplate = `
SELECT
    r.session_id,
    r.request_id,
    RTRIM(r.status),
    ISNULL(DB_NAME(r.database_id), ''),
    ISNULL(s.login_name, ''),
    ISNULL(s.host_name, ''),
    ISNULL(s.program_name, ''),
    RTRIM(ISNULL(r.command, '')),
    ISNULL(r.blocking_session_id, 0),
    r.total_elapsed_time,
    r.cpu_time,
    r.logical_reads,
    r.reads,
    r.writes,
    %s,
    ISNULL(r.granted_query_memory, 0) * 8.0 / 1024.0,
    %s,
    ISNULL(s.open_transaction_count, 0),
    ISNULL(r.percent_complete, 0),
    CASE ISNULL(s.transaction_isolation_level, 0)
        WHEN 0 THEN '' WHEN 1 THEN 'read uncommitted' WHEN 2 THEN 'read committed'
        WHEN 3 THEN 'repeatable read' WHEN 4 THEN 'serializable' WHEN 5 THEN 'snapshot'
        ELSE '' END,
    RTRIM(ISNULL(r.wait_type, '')),
    ISNULL(r.wait_time, 0),
    RTRIM(ISNULL(r.wait_resource, '')),
    ISNULL(CONVERT(varchar(34), r.query_hash, 1), ''),
    ISNULL(SUBSTRING(t.text,
        (r.statement_start_offset / 2) + 1,
        ((CASE r.statement_end_offset
            WHEN -1 THEN DATALENGTH(t.text)
            ELSE r.statement_end_offset
          END - r.statement_start_offset) / 2) + 1), '')
FROM sys.dm_exec_requests AS r
    LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = r.session_id
    OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) AS t
    %s
WHERE r.session_id <> @@SPID
OPTION (RECOMPILE, MAXDOP 1)`

// buildRequestsQuery renders requestsQueryTemplate for one server. Two
// things in the fixed query text do not hold everywhere:
//
// r.dop does not exist on sys.dm_exec_requests before SQL Server 2016; on
// an older server the plain column reference fails the whole query with
// "Invalid column name 'dop'", leaving the grid permanently empty on a
// version spec section 3 promises works. Below 2016, and not Azure SQL
// Database (which is always current), the DOP column becomes the literal
// 0 instead.
//
// sys.dm_db_task_space_usage, behind the tempdb figure, needs a right the
// probe already checked for as model.CapTempdbPerTask. A login without it
// would otherwise get a clean Identify and then a permanently empty grid,
// the one tier missing the gate every other capability-dependent query in
// this package already has. Without the capability, both the join and the
// column it feeds are dropped; tempdb_mb reads 0.
//
// Either substitution is a literal, not a NULL or a missing column, so the
// column count and the Scan list in SampleRequests stay fixed at 25
// regardless of which server or login this is.
//
// Called once, right after Identify has version and capabilities in hand -
// not per sample, since nothing here changes between two calls to
// SampleRequests on the same connection.
func buildRequestsQuery(info model.ServerInfo, caps model.Capabilities) string {
	dopExpr := "0"
	if info.IsAzure() || info.MajorVersion >= 13 {
		dopExpr = "ISNULL(r.dop, 0)"
	}

	tempdbExpr := "CAST(0 AS float)"
	tempdbJoin := ""
	if caps.Has(model.CapTempdbPerTask) {
		tempdbExpr = "ISNULL(tsu.tempdb_pages, 0) / 128.0"
		tempdbJoin = `OUTER APPLY (
        SELECT SUM(u.user_objects_alloc_page_count - u.user_objects_dealloc_page_count
                 + u.internal_objects_alloc_page_count - u.internal_objects_dealloc_page_count) AS tempdb_pages
        FROM sys.dm_db_task_space_usage AS u
        WHERE u.session_id = r.session_id
    ) AS tsu`
	}

	return fmt.Sprintf(requestsQueryTemplate, tempdbExpr, dopExpr, tempdbJoin)
}

func (s *Source) SampleRequests(ctx context.Context) ([]model.RequestSample, error) {
	s.mu.Lock()
	q := s.requestsQuery
	s.mu.Unlock()

	var out []model.RequestSample
	err := s.query(ctx, q, func(rows *sql.Rows) error {
		var r model.RequestSample
		var requestID sql.NullInt32
		if err := rows.Scan(
			&r.Ref.SessionID, &requestID, &r.Status, &r.Database,
			&r.Login, &r.Host, &r.Program, &r.Command, &r.BlockedBy,
			&r.ElapsedMs, &r.CPUMs, &r.LogicalReads, &r.PhysicalReads, &r.Writes,
			&r.TempdbMB, &r.MemoryGrantMB, &r.DOP, &r.OpenTran, &r.PercentComplete,
			&r.IsolationLevel,
			&r.WaitType, &r.WaitMs, &r.WaitResource, &r.QueryHash, &r.SQLText,
		); err != nil {
			return err
		}
		r.Ref.RequestID = requestID.Int32
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mssql: sample requests: %w", err)
	}

	// Stamped once the answer is actually in hand, the same way Cost stamps
	// At after queryRow returns rather than before asking for it: a wait for
	// s.mu, or the query itself, must not be counted as part of how current
	// the sample is.
	now := time.Now()
	for i := range out {
		out[i].At = now
	}
	return out, nil
}
