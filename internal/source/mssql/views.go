package mssql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// errNoInstanceWideView is what the three views below return to a login
// that cannot see past its own session. An error rather than an empty list:
// a list of one session, or of no transactions, is a plausible answer that
// happens to be a lie, and the whole Available convention exists to stop
// this tool giving those.
var errNoInstanceWideView = errors.New("mssql: this login cannot see the whole instance; the session, transaction and log views need VIEW SERVER STATE")

// The durations are computed on the server, against the server's clock, for
// the same reason the request grid's elapsed time is: the tool may be on
// another machine, in another time zone, with a clock that is minutes out,
// and a transaction reported as running for negative four minutes is worse
// than no figure at all. Seconds rather than milliseconds because DATEDIFF
// in milliseconds overflows a little past 24 days and a session open since
// last month is exactly what this view is for.

const sessionsQuery = `
SELECT s.session_id,
       ISNULL(s.login_name, N''), ISNULL(s.host_name, N''), ISNULL(s.program_name, N''),
       ISNULL(s.status, N''), ISNULL(DB_NAME(s.database_id), N''),
       ISNULL(DATEDIFF(second, s.login_time, SYSDATETIME()), 0),
       ISNULL(DATEDIFF(second, s.last_request_end_time, SYSDATETIME()), 0),
       s.cpu_time, s.reads, s.writes, s.memory_usage,
       s.open_transaction_count,
       ISNULL(DATEDIFF(second, t.oldest_begin, SYSDATETIME()), 0)
FROM sys.dm_exec_sessions AS s
OUTER APPLY (
    SELECT MIN(tx.transaction_begin_time) AS oldest_begin
    FROM sys.dm_tran_session_transactions AS stx
    INNER JOIN sys.dm_tran_active_transactions AS tx ON tx.transaction_id = stx.transaction_id
    WHERE stx.session_id = s.session_id
) AS t
WHERE s.is_user_process = 1
OPTION (MAXDOP 1)`

// Sessions lists every open user session. Cheap: one row per connection,
// and the OUTER APPLY reads two views that hold one row per open
// transaction, not one per lock or per request.
func (s *Source) Sessions(ctx context.Context) ([]model.SessionSample, error) {
	if _, caps := s.snapshot(); !caps.Has(model.CapInstanceWideView) {
		return nil, errNoInstanceWideView
	}
	var out []model.SessionSample
	err := s.query(ctx, sessionsQuery, func(rows *sql.Rows) error {
		var r model.SessionSample
		var memoryPages int64
		if err := rows.Scan(&r.SessionID, &r.Login, &r.Host, &r.Program, &r.Status, &r.Database,
			&r.ConnectedSec, &r.IdleSec, &r.CPUMs, &r.Reads, &r.Writes, &memoryPages,
			&r.OpenTran, &r.TranSec); err != nil {
			return err
		}
		// memory_usage counts 8 KB pages, which is the engine's unit and
		// nobody else's.
		r.MemoryMB = float64(memoryPages) * 8 / 1024
		out = append(out, r)
		return nil
	})
	return out, err
}

const transactionsQuery = `
SELECT tx.transaction_id, stx.session_id,
       ISNULL(tx.name, N''),
       ISNULL(DATEDIFF(second, tx.transaction_begin_time, SYSDATETIME()), 0),
       tx.transaction_type, tx.transaction_state,
       ISNULL(DB_NAME(dbt.database_id), N''), ISNULL(dbt.db_count, 0),
       ISNULL(dbt.log_bytes, 0), ISNULL(dbt.log_records, 0)
FROM sys.dm_tran_active_transactions AS tx
INNER JOIN sys.dm_tran_session_transactions AS stx ON stx.transaction_id = tx.transaction_id
LEFT JOIN (
    SELECT transaction_id,
           MIN(database_id) AS database_id,
           COUNT(*) AS db_count,
           SUM(database_transaction_log_bytes_used) AS log_bytes,
           SUM(database_transaction_log_record_count) AS log_records
    FROM sys.dm_tran_database_transactions
    GROUP BY transaction_id
) AS dbt ON dbt.transaction_id = tx.transaction_id
WHERE stx.is_user_transaction = 1
OPTION (MAXDOP 1)`

// locksQuery aggregates rather than listing. sys.dm_tran_locks has one row
// per lock, so a single large statement puts millions in it; grouping on
// the server means the wire and the browser see tens of rows whatever the
// engine is holding, and the scan is the only cost left.
//
// Only OBJECT locks get a name. OBJECT_NAME takes a database id, so it
// resolves across databases without a context switch; page, key and row
// locks name a partition instead, and turning one of those into an object
// name means a query inside that database, per database, which is not
// something to do while somebody is watching a screen. An empty name is
// "not resolvable cheaply", never "no object".
//
// The TOP of two thousand caps the grouped result, not the scan. The
// grouping already collapses a million row locks on one table into one
// line, so the cap only bites on a session holding locks on more than two
// thousand distinct objects, which is a schema-wide operation rather than
// anything somebody is reading rows of.
//
// The EXISTS narrows the grouping to sessions that actually hold a user
// transaction, which is what the view above lists: a shared lock taken and
// released inside an autocommit statement is not something anybody is
// looking for here.
const locksQuery = `
SELECT TOP (2000)
       l.request_session_id,
       ISNULL(DB_NAME(l.resource_database_id), N''),
       RTRIM(l.resource_type),
       ISNULL(CASE WHEN l.resource_type = 'OBJECT'
                   THEN OBJECT_NAME(l.resource_associated_entity_id, l.resource_database_id) END, N''),
       RTRIM(l.request_mode), RTRIM(l.request_status), COUNT(*)
FROM sys.dm_tran_locks AS l
WHERE l.request_session_id > 0
  AND EXISTS (SELECT 1 FROM sys.dm_tran_session_transactions AS stx
              WHERE stx.session_id = l.request_session_id AND stx.is_user_transaction = 1)
GROUP BY l.request_session_id, l.resource_database_id, l.resource_type,
         CASE WHEN l.resource_type = 'OBJECT'
              THEN OBJECT_NAME(l.resource_associated_entity_id, l.resource_database_id) END,
         l.request_mode, l.request_status
ORDER BY COUNT(*) DESC
OPTION (MAXDOP 1)`

// Transactions lists the open user transactions and, alongside them, what
// each holding session has locked. Two round trips, both on demand.
func (s *Source) Transactions(ctx context.Context) ([]model.TransactionSample, []model.LockSample, error) {
	if _, caps := s.snapshot(); !caps.Has(model.CapInstanceWideView) {
		return nil, nil, errNoInstanceWideView
	}

	var trans []model.TransactionSample
	err := s.query(ctx, transactionsQuery, func(rows *sql.Rows) error {
		var r model.TransactionSample
		var kind, state int
		if err := rows.Scan(&r.TransactionID, &r.SessionID, &r.Name, &r.ElapsedSec,
			&kind, &state, &r.Database, &r.Databases, &r.LogBytes, &r.LogRecords); err != nil {
			return err
		}
		r.Type, r.State = transactionType(kind), transactionState(state)
		trans = append(trans, r)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	var locks []model.LockSample
	err = s.query(ctx, locksQuery, func(rows *sql.Rows) error {
		var r model.LockSample
		if err := rows.Scan(&r.SessionID, &r.Database, &r.ResourceType, &r.Object,
			&r.Mode, &r.Status, &r.Count); err != nil {
			return err
		}
		locks = append(locks, r)
		return nil
	})
	return trans, locks, err
}

// transactionType and transactionState translate the two integer codes
// sys.dm_tran_active_transactions reports. The numbers are documented and
// stable; the strings are what an operator reads.
func transactionType(v int) string {
	switch v {
	case 1:
		return "read/write"
	case 2:
		return "read-only"
	case 3:
		return "system"
	case 4:
		return "distributed"
	}
	return "unknown"
}

func transactionState(v int) string {
	switch v {
	case 0:
		return "uninitialised"
	case 1:
		return "not started"
	case 2:
		return "active"
	case 3:
		return "ended"
	case 4:
		return "commit started"
	case 5:
		return "prepared"
	case 6:
		return "committed"
	case 7:
		return "rolling back"
	case 8:
		return "rolled back"
	}
	return "unknown"
}

// logSpaceQuery reads the per-database log figures from the performance
// counters rather than from sys.dm_db_log_space_usage, which returns one
// row for the current database only and would mean a context switch per
// database. The counters carry every database in one instance-wide read.
//
// Used size is the active portion: the part of the log that cannot be
// reused yet. log_reuse_wait_desc from sys.databases is next to it because
// it is the answer somebody looking at a full log actually wants, which the
// percentage on its own never gives.
//
// sys.dm_db_log_stats would add the size since the last backup, which is
// finer, but it is 2016 and later and is database-scoped like
// dm_db_log_space_usage. Not worth a query per database on a screen that
// refreshes.
const logSpaceQuery = `
SELECT d.name, d.recovery_model_desc, d.log_reuse_wait_desc, d.state_desc,
       ISNULL(pc.size_kb, 0), ISNULL(pc.used_kb, 0), ISNULL(pc.percent_used, 0)
FROM sys.databases AS d
LEFT JOIN (
    SELECT instance_name,
           MAX(CASE WHEN counter_name = N'Log File(s) Size (KB)' THEN cntr_value END) AS size_kb,
           MAX(CASE WHEN counter_name = N'Log File(s) Used Size (KB)' THEN cntr_value END) AS used_kb,
           MAX(CASE WHEN counter_name = N'Percent Log Used' THEN cntr_value END) AS percent_used
    FROM sys.dm_os_performance_counters
    WHERE object_name LIKE N'%Databases%'
      AND counter_name IN (N'Log File(s) Size (KB)', N'Log File(s) Used Size (KB)', N'Percent Log Used')
    GROUP BY instance_name
) AS pc ON pc.instance_name = d.name
OPTION (MAXDOP 1)`

// LogSpace lists every database's transaction log: how big it is, how much
// of it is active, and what is stopping the rest being reused.
func (s *Source) LogSpace(ctx context.Context) ([]model.LogSpaceSample, error) {
	if _, caps := s.snapshot(); !caps.Has(model.CapInstanceWideView) {
		return nil, errNoInstanceWideView
	}
	var out []model.LogSpaceSample
	err := s.query(ctx, logSpaceQuery, func(rows *sql.Rows) error {
		var r model.LogSpaceSample
		var sizeKB, usedKB, percent int64
		if err := rows.Scan(&r.Database, &r.RecoveryModel, &r.ReuseWait, &r.State,
			&sizeKB, &usedKB, &percent); err != nil {
			return err
		}
		r.SizeMB = float64(sizeKB) / 1024
		r.UsedMB = float64(usedKB) / 1024
		r.UsedPercent = float64(percent)
		out = append(out, r)
		return nil
	})
	return out, err
}
