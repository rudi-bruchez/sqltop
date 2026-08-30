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
//
// The noise filter: what it drops, why it lives in an inner derived table,
// and where it ended up after the PowerShell prototype's own predicate
// turned out not to be safe to port as written.
//
// docs/sqltop.psm1, around line 657, the prototype spec section 3 names as
// this project's ancestor, keeps a row if its status is not background,
// sleeping, suspended or dormant, or it holds an open transaction, or it is
// a blocker, or it is a parallel worker, or it uses tempdb. Ported
// literally, that predicate passed every test until
// TestSampleRequestsSeesALongQuery: a session sitting inside a bare
// WAITFOR DELAY, no explicit transaction, showed status 'suspended' and
// open_transaction_count 0, because SQL Server only opens an implicit
// transaction around a statement that actually writes; a read-only wait or
// scan never does. That is not a background system row - it is exactly a
// long-running request a DBA would open this tool to look at - and the
// ported predicate hid it. So this query does not test status or open
// transactions at all. It tests sys.dm_exec_sessions.is_user_process
// instead: 0 for the scheduler's own worker threads (TASK MANAGER, LOG
// WRITER, RESOURCE MONITOR, BRKR TASK, PARALLEL REDO TASK and the rest of
// the internal bookkeeping that filled 66 of 70 rows on an idle
// container), 1 for every session that is actually a login, whatever its
// status. A real session is kept unconditionally, not by reconstructing
// "is this real work" from status and transaction state the way the
// prototype had to; a system worker is dropped unless it is itself
// blocking or blocked, or running a parallel plan. This is not a knob:
// there is no option to turn it off, because the point is to stop paying
// for a row nobody asked to see, not to make that configurable.
//
// It has to run before the tempdb OUTER APPLY below, not alongside it in
// one flat WHERE, because that OUTER APPLY is the expensive part this fix
// exists to avoid paying for on a row about to be dropped anyway. Measured
// directly with SET STATISTICS TIME against the container under sqlstress
// load, 20 executions each: an earlier version of this WHERE that named
// the apply's own output as one of its OR branches forced the optimizer to
// evaluate it for every row before it could decide whether to keep the
// row, which is exactly the cost this fix exists to remove - that shape
// cost more than the unfiltered query it was meant to replace (752 ms
// against 292 ms of CPU time over 20 runs). Filtering first in an inner
// derived table, so the apply only ever runs against rows that already
// survived on cheap session and request columns, removed that regression.
//
// Three things did not survive the port, each for a reason found by
// measurement or by the test above rather than assumed:
//
//   - Filtering by status and open_transaction_count is dropped, not
//     merely reordered: see TestSampleRequestsSeesALongQuery above. A
//     read-only request can be genuinely active, suspended, and worth
//     seeing without ever opening a transaction, so that pair could not be
//     trusted to mean "this is real work" the way the prototype used it.
//     is_user_process is not an approximation of the same idea with a
//     smaller gap; it is the actual thing the prototype's five-part
//     heuristic was trying to reconstruct from status text, read directly
//     off the session instead.
//   - "Is a blocker" is dropped. Expressed as a second scan of
//     sys.dm_exec_requests, the only way to ask "does any row name my
//     session as its blocker" without re-deriving the whole set, it
//     measured at roughly double the query's own cost, because the DMV
//     itself is not free to materialise. Every real blocking session is
//     covered anyway, because is_user_process already keeps it
//     unconditionally; what this hatch would still rescue is narrower and
//     named honestly rather than quietly dropped: a genuine system worker,
//     PARALLEL REDO TASK on an Always On secondary applying redo is the
//     concrete case, blocking a user session while not itself flagged as a
//     blocker anywhere this query looks. That gap is real and is not
//     closed here; closing it costs as much as this fix saves. The
//     session it blocks is not lost either way: "is blocked" is its own
//     condition and does not depend on finding the blocker at all.
//   - "Is a parallel child" has no row to keep here at all: a parallel
//     worker never gets its own row in sys.dm_exec_requests the way it did
//     in the prototype's sys.sysprocesses, one row per worker thread
//     included. The nearest analogue, keeping the coordinator row of a
//     request actually running in parallel, is dop > 1 instead, read from
//     the same column the query already carries for the grid; on a server
//     where dop cannot be read at all it degrades to "0 > 1", always false
//     and therefore harmless, the same substitution pattern
//     buildRequestsQuery already uses for the column itself. This one is
//     mostly redundant with is_user_process (a parallel plan almost always
//     belongs to a real login) but costs nothing to keep for the rare
//     system-parallel case.
//
// What is kept, unconditionally: "is blocked"
// (ISNULL(r.blocking_session_id, 0) <> 0) is its own condition on the
// row's own column, not derived from any other row, so it costs nothing
// beyond reading a column the query already fetches. The requirement it
// exists for is not negotiable - the tool must never silently hide either
// side of a block - and unlike "is a blocker" above, honouring it adds no
// measurable cost.
const requestsQueryTemplate = `
SELECT
    r.session_id,
    r.request_id,
    RTRIM(r.status),
    ISNULL(DB_NAME(r.database_id), ''),
    ISNULL(r.login_name, ''),
    ISNULL(r.host_name, ''),
    ISNULL(r.program_name, ''),
    RTRIM(ISNULL(r.command, '')),
    ISNULL(r.blocking_session_id, 0),
    r.total_elapsed_time,
    r.cpu_time,
    r.logical_reads,
    r.reads,
    r.writes,
    %s,
    ISNULL(r.granted_query_memory, 0) * 8.0 / 1024.0,
    r.dop,
    r.open_transaction_count,
    ISNULL(r.percent_complete, 0),
    CASE ISNULL(r.transaction_isolation_level, 0)
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
FROM (
    SELECT
        r.session_id, r.request_id, r.status, r.database_id, r.command,
        r.blocking_session_id, r.total_elapsed_time, r.cpu_time, r.logical_reads,
        r.reads, r.writes, r.granted_query_memory, %s AS dop, r.percent_complete,
        r.wait_type, r.wait_time, r.wait_resource, r.query_hash, r.sql_handle,
        r.statement_start_offset, r.statement_end_offset,
        s.login_name, s.host_name, s.program_name,
        ISNULL(s.open_transaction_count, 0) AS open_transaction_count,
        s.transaction_isolation_level
    FROM sys.dm_exec_requests AS r
        LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = r.session_id
    WHERE r.session_id <> @@SPID
      AND (
            ISNULL(s.is_user_process, 1) = 1
         OR ISNULL(r.blocking_session_id, 0) <> 0
         OR %s > 1
      )
) AS r
    OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) AS t
    %s
OPTION (MAXDOP 1)`

// buildRequestsQuery renders requestsQueryTemplate for one server. Two
// things in the fixed query text do not hold everywhere:
//
// r.dop does not exist on sys.dm_exec_requests before SQL Server 2016; on
// an older server the plain column reference fails the whole query with
// "Invalid column name 'dop'", leaving the grid permanently empty on a
// version spec section 3 promises works. Below 2016, and not Azure SQL
// Database (which is always current), the DOP column becomes the literal
// 0 instead. The gate is model.CapRequestDOP, which Identify sets from
// supportsRequestDOP below. It is a version fact rather than a login right,
// so a login can lack every optional right and still get the column on SQL
// Server 2022, but it rides in caps because caps is what reaches the
// browser, and the column has to be greyed on exactly the servers where
// this substitution happens. One condition, in one place, read here and on
// the wire. dopExpr is used twice: once to build the inner derived table's
// dop column, once in its WHERE clause, which cannot reference that
// column's own alias (WHERE evaluates before the SELECT list exists).
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
func supportsRequestDOP(info model.ServerInfo) bool {
	return info.IsAzure() || info.MajorVersion >= 13
}

func buildRequestsQuery(info model.ServerInfo, caps model.Capabilities) string {
	dopExpr := "0"
	if caps.Has(model.CapRequestDOP) {
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

	return fmt.Sprintf(requestsQueryTemplate, tempdbExpr, dopExpr, dopExpr, tempdbJoin)
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
