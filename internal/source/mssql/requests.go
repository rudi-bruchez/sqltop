package mssql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// requestsQuery is the hot path, run once per requests tier.
//
// Two deliberate choices. The statement text comes from the offsets rather
// than the whole batch, so the grid shows what is running now. And the tool
// filters out its own spid: reporting its own collection query as server
// activity would be both noise and a small lie.
const requestsQuery = `
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
    ISNULL(tsu.tempdb_pages, 0) / 128.0,
    ISNULL(r.granted_query_memory, 0) * 8.0 / 1024.0,
    ISNULL(r.dop, 0),
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
    OUTER APPLY (
        SELECT SUM(u.user_objects_alloc_page_count
                 + u.internal_objects_alloc_page_count) AS tempdb_pages
        FROM sys.dm_db_task_space_usage AS u
        WHERE u.session_id = r.session_id
    ) AS tsu
WHERE r.session_id <> @@SPID
OPTION (RECOMPILE, MAXDOP 1)`

// query runs one query under the lock that serialises every call this Source
// makes, holding it for the whole query-plus-scan cycle rather than just the
// call - like queryRow, but for a result set that streams over the pinned
// connection instead of a single row. rows.Next() can put more bytes on the
// wire while a previous call is mid-flight, so the lock has to stay held
// across the whole loop, not just the QueryContext call that starts it.
//
// scan is called once per row, with s.mu held; it must not call back into
// Source.
//
// Repairs the pinned connection on the same failure conditions queryRow
// does, and for the same reason: a break can surface as the QueryContext
// call failing outright, or - if the connection dies while the result set is
// still being read back - only as a raw read error with the sentinel
// arriving on a later call. Checking connDeadLocked after the loop catches
// that case on this same call. See queryRow's comment for the full story.
func (s *Source) query(ctx context.Context, q string, scan func(*sql.Rows) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connLocked(ctx)
	if err != nil {
		return err
	}

	rows, err := conn.QueryContext(ctx, q)
	if err == nil {
		for rows.Next() {
			if err = scan(rows); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}

	if err != nil && (errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) || connDeadLocked(conn)) {
		conn.Close()
		s.db = nil
	}
	return err
}

func (s *Source) SampleRequests(ctx context.Context) ([]model.RequestSample, error) {
	now := time.Now()

	var out []model.RequestSample
	err := s.query(ctx, requestsQuery, func(rows *sql.Rows) error {
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
		r.At = now
		r.Ref.RequestID = requestID.Int32
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mssql: sample requests: %w", err)
	}
	return out, nil
}
