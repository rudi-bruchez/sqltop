// Package mssql is the SQL Server implementation of source.Source.
//
// Everything here is read-only. No object is created, nothing is configured,
// no trace flag is set: spec section 2.
package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	_ "github.com/microsoft/go-mssqldb/integratedauth/krb5"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// ErrVersionTooOld is returned for anything below SQL Server 2012, where the
// tool refuses to connect rather than failing query by query. Spec section 3.
var ErrVersionTooOld = errors.New("sqltop: SQL Server 2012 or later is required")

type Source struct {
	pool    *sql.DB
	db      *sql.Conn
	spid    int64
	info    model.ServerInfo
	caps    model.Capabilities
	counter *counterState
}

func New() *Source { return &Source{counter: newCounterState()} }

// sessionInit runs on every new or reset session, so the settings survive a
// reconnection rather than being a one-off after the first connect.
//
// READ UNCOMMITTED because a monitoring tool must not take shared locks on the
// server it is watching. NOCOUNT because the row counts are noise on the wire.
const sessionInit = `SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED; SET NOCOUNT ON;`

func (s *Source) Open(ctx context.Context, dsn string) error {
	connector, err := mssql.NewConnector(dsn)
	if err != nil {
		return fmt.Errorf("mssql: open: %w", err)
	}
	connector.SessionInitSQL = sessionInit
	pool := sql.OpenDB(connector)

	// One connection, always the same one, because Cost reads @@SPID and a
	// pool would hand us a different session on every call.
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(0)

	// Pinned with pool.Conn, not borrowed per query from pool.QueryRowContext:
	// database/sql resets the session (a TDS reset-connection flag, which SQL
	// Server treats as a soft re-login and uses to zero the session's
	// cumulative cpu_time and logical_reads) every time an idle pooled
	// connection is checked out again, and with a one-connection pool that
	// would mean every single query. Cost depends on those counters staying
	// cumulative across calls, so the connection is held for the source's
	// whole lifetime and never given back to the pool in between.
	conn, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return fmt.Errorf("mssql: connect: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT @@SPID").Scan(&s.spid); err != nil {
		conn.Close()
		pool.Close()
		return fmt.Errorf("mssql: reading own spid: %w", err)
	}
	s.pool = pool
	s.db = conn
	return nil
}

func (s *Source) Close() error {
	if s.db != nil {
		s.db.Close()
	}
	if s.pool == nil {
		return nil
	}
	return s.pool.Close()
}

const identifyQuery = `
SELECT
    CAST(SERVERPROPERTY('ServerName')      AS nvarchar(256)),
    CAST(SERVERPROPERTY('MachineName')     AS nvarchar(256)),
    CAST(SERVERPROPERTY('Edition')         AS nvarchar(256)),
    CAST(SERVERPROPERTY('ProductVersion')  AS nvarchar(64)),
    CAST(SERVERPROPERTY('EngineEdition')   AS int)
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) Identify(ctx context.Context) (model.ServerInfo, model.Capabilities, error) {
	var info model.ServerInfo
	var machine, version sql.NullString
	var name sql.NullString
	var engineEdition int

	err := s.db.QueryRowContext(ctx, identifyQuery).
		Scan(&name, &machine, &info.Edition, &version, &engineEdition)
	if err != nil {
		return info, 0, fmt.Errorf("mssql: identify: %w", err)
	}
	info.Instance = name.String
	info.Host = machine.String
	info.ProductVersion = version.String
	info.MajorVersion = majorVersion(version.String)
	// EngineEdition 5 is Azure SQL Database, 8 is Managed Instance.
	info.IsAzureSQLDB = engineEdition == 5

	if !info.IsAzureSQLDB && info.MajorVersion > 0 && info.MajorVersion < 11 {
		return info, 0, fmt.Errorf("%w (found %s)", ErrVersionTooOld, info.ProductVersion)
	}

	caps := s.probe(ctx, info)
	s.info, s.caps = info, caps
	return info, caps, nil
}

// majorVersion turns "15.0.4335.1" into 15. Zero when it cannot tell, which
// makes the version-gated capabilities fall back to probing.
func majorVersion(product string) int {
	head, _, _ := strings.Cut(product, ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0
	}
	return n
}

// probe asks the server what actually works rather than inferring rights from
// the version. A login can hold VIEW SERVER STATE on paper and be denied one
// view, and Azure SQL Database quietly returns only the current session when
// the right is missing instead of raising an error. Spec section 3.1.
func (s *Source) probe(ctx context.Context, info model.ServerInfo) model.Capabilities {
	var caps model.Capabilities

	// Every probe carries the hint too, so nothing this package sends can
	// take a parallel worker or leave a plan behind.
	can := func(query string) bool {
		var one int
		err := s.db.QueryRowContext(ctx, query+" OPTION (RECOMPILE, MAXDOP 1)").Scan(&one)
		return err == nil
	}

	if !info.IsAzureSQLDB {
		// Ask the server what the login holds rather than counting visible
		// sessions. Counting works in practice, since an instance always has
		// dozens of system sessions, but it answers a different question and
		// would be wrong the day it is asked on something quieter.
		var granted int
		if err := s.db.QueryRowContext(ctx,
			`SELECT CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
			              OR HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE') = 1
			         THEN 1 ELSE 0 END
			 OPTION (RECOMPILE, MAXDOP 1)`).
			Scan(&granted); err == nil && granted == 1 {
			caps = caps.With(model.CapInstanceWideView)
		}
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_os_schedulers`) {
		caps = caps.With(model.CapSchedulerLoad)
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_os_wait_stats`) {
		caps = caps.With(model.CapWaitStatsCumulative)
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_db_task_space_usage`) {
		caps = caps.With(model.CapTempdbPerTask)
	}
	if can(`SELECT TOP (1) 1 FROM sys.dm_tran_version_store_space_usage`) {
		caps = caps.With(model.CapVersionStoreUsage)
	}
	if !info.IsAzureSQLDB && can(
		`SELECT TOP (1) 1 FROM sys.dm_os_ring_buffers WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'`) {
		caps = caps.With(model.CapRingBufferCPU)
	}
	// Lightweight profiling v3 is on by default from 2019 and on Azure SQL
	// Database. Below that it needs trace flag 7412, which the tool will not
	// set, so the feature is simply absent. Spec section 3.
	if info.IsAzureSQLDB || info.MajorVersion >= 15 {
		if can(`SELECT TOP (1) 1 FROM sys.dm_exec_query_statistics_xml(@@SPID)`) {
			caps = caps.With(model.CapLivePlanProgress)
		}
	}
	return caps
}

const costQuery = `
SELECT cpu_time, logical_reads
FROM sys.dm_exec_sessions
WHERE session_id = @@SPID
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) Cost(ctx context.Context) (model.Cost, error) {
	var c model.Cost
	if err := s.db.QueryRowContext(ctx, costQuery).Scan(&c.CPUMs, &c.LogicalReads); err != nil {
		return c, fmt.Errorf("mssql: own cost: %w", err)
	}
	c.At = time.Now()
	return c, nil
}

// errNotInThisPlan keeps mssql.Source satisfying source.Source from this task
// onward. The on-demand pair ships with the UI plan, where the plan panel that
// consumes them lives; a real implementation with no caller would be code
// nothing exercises.
var errNotInThisPlan = errors.New("mssql: query text and plan retrieval arrive with the UI plan")

func (s *Source) QueryText(context.Context, model.RequestRef) (string, error) {
	return "", errNotInThisPlan
}

func (s *Source) Plan(context.Context, model.RequestRef, bool) (model.Plan, error) {
	return model.Plan{}, errNotInThisPlan
}

func (s *Source) SampleRequests(context.Context) ([]model.RequestSample, error) {
	return nil, nil // task 8
}

func (s *Source) SampleServer(context.Context, model.Tier) (model.ServerSample, error) {
	return model.ServerSample{Figures: map[string]model.Figure{}}, nil // task 9
}
