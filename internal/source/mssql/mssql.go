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
	"sync"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	_ "github.com/microsoft/go-mssqldb/integratedauth/krb5"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// ErrVersionTooOld is returned for anything below SQL Server 2012, where the
// tool refuses to connect rather than failing query by query. Spec section 3.
var ErrVersionTooOld = errors.New("sqltop: SQL Server 2012 or later is required")

type Source struct {
	// mu serialises every query this Source sends. A pooled *sql.DB with
	// MaxOpenConns(1) gave callers that serialisation for free: a second
	// goroutine calling QueryRowContext blocked until the first got its
	// connection back. A pinned *sql.Conn does not do that on its own -
	// database/sql documents it as unsafe for concurrent use, and grabConn
	// only read-locks before handing the same driverConn to every caller.
	// Tasks 9 and 11 call SampleServer and SampleRequests from separate
	// goroutines on one Source, so this Source has to reproduce the
	// serialisation the pool used to provide by hand.
	mu sync.Mutex

	// pool is never queried directly. It exists so a dead pinned connection
	// can be replaced: the pool used to open a fresh connection transparently
	// after a failover, and pinning had to keep that ability explicitly.
	pool *sql.DB
	// db is the pinned connection every query actually runs on, held for the
	// Source's lifetime so Cost's cumulative counters are never reset out
	// from under it. Nil between a dead connection being discovered and the
	// next query re-pinning a fresh one.
	db *sql.Conn

	spid    int64
	info    model.ServerInfo
	caps    model.Capabilities
	counter *counterState
}

func New() *Source { return &Source{counter: newCounterState()} }

// sessionInit runs once, when the pinned connection is first established, and
// again on the rare re-pin after Source has discovered the previous
// connection is dead - pinning means there is no reset in between to run it
// automatically, so the constant has to stay ready for that case.
//
// READ UNCOMMITTED because a monitoring tool must not take shared locks on the
// server it is watching. NOCOUNT because the row counts are noise on the wire.
const sessionInit = `SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED; SET NOCOUNT ON;`

const spidQuery = `SELECT @@SPID OPTION (RECOMPILE, MAXDOP 1)`

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
	s.pool = pool

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		s.pool = nil
		return fmt.Errorf("mssql: connect: %w", err)
	}
	if err := s.queryRow(ctx, spidQuery, &s.spid); err != nil {
		pool.Close()
		s.pool = nil
		return fmt.Errorf("mssql: reading own spid: %w", err)
	}
	return nil
}

func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
	if s.pool == nil {
		return nil
	}
	pool := s.pool
	s.pool = nil
	return pool.Close()
}

// connLocked returns the pinned connection, re-pinning a fresh one from the
// pool if the previous one was declared dead. Callers must hold s.mu.
func (s *Source) connLocked(ctx context.Context) (*sql.Conn, error) {
	if s.db != nil {
		return s.db, nil
	}
	conn, err := s.pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	s.db = conn
	return conn, nil
}

// queryRow runs one query under the lock that serialises every call this
// Source makes, holding it for the whole query-plus-scan cycle rather than
// just the call, since a *sql.Conn can have another batch put on the wire
// while a previous result set is still streaming.
//
// It also repairs the pinned connection: once the driver declares it dead
// with driver.ErrBadConn, database/sql closes the *sql.Conn for good and
// every later call on it fails with sql.ErrConnDone ("connection is already
// closed"). Source notices that and clears the pinned connection so the next
// call re-pins a fresh one from the pool. It does not retry the query that
// just failed, and it does not decide when the caller should try again -
// that stays the collector's job.
func (s *Source) queryRow(ctx context.Context, query string, dest ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connLocked(ctx)
	if err != nil {
		return err
	}
	err = conn.QueryRowContext(ctx, query).Scan(dest...)
	if errors.Is(err, sql.ErrConnDone) {
		s.db = nil
	}
	return err
}

// isServerError reports whether err is something SQL Server itself raised
// (permission denied, an object absent on this version) rather than a
// transport or driver failure. The two must not be confused: the first means
// a capability is absent, the second means the connection broke and probing
// further capabilities on it would be meaningless. Spec section 3.1.
func isServerError(err error) bool {
	var srvErr mssql.Error
	return errors.As(err, &srvErr)
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
	var name, machine, edition, version sql.NullString
	var engineEdition int

	err := s.queryRow(ctx, identifyQuery, &name, &machine, &edition, &version, &engineEdition)
	if err != nil {
		return info, 0, fmt.Errorf("mssql: identify: %w", err)
	}
	info.Instance = name.String
	info.Host = machine.String
	info.Edition = edition.String
	info.ProductVersion = version.String
	info.MajorVersion = majorVersion(version.String)
	// EngineEdition 5 is Azure SQL Database, 8 is Managed Instance.
	info.IsAzureSQLDB = engineEdition == 5

	if !info.IsAzureSQLDB && info.MajorVersion > 0 && info.MajorVersion < 11 {
		return info, 0, fmt.Errorf("%w (found %s)", ErrVersionTooOld, info.ProductVersion)
	}

	caps, err := s.probe(ctx, info)
	if err != nil {
		return info, 0, fmt.Errorf("mssql: probing capabilities: %w", err)
	}
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
//
// A probe returning an error means the connection itself broke mid-probe,
// not that a capability is absent; Identify propagates that rather than
// reporting every remaining capability as unavailable.
func (s *Source) probe(ctx context.Context, info model.ServerInfo) (model.Capabilities, error) {
	var caps model.Capabilities

	// can reports whether the login can read from a DM object at all. The
	// object is wrapped in SELECT COUNT(*) FROM (SELECT TOP (1) 1 ...) so an
	// empty result set is a value, zero, rather than sql.ErrNoRows: a server
	// with no version-store activity, or no in-flight plan chunky enough to
	// report live progress, must not read as a login that lacks the right.
	// An error the server itself raised (permission denied, the object does
	// not exist on this version) means the capability is absent; any other
	// error means the connection died mid-probe and is returned instead.
	can := func(from string) (bool, error) {
		var n int
		q := "SELECT COUNT(*) FROM (SELECT TOP (1) 1 AS x FROM " + from + ") AS probe OPTION (RECOMPILE, MAXDOP 1)"
		err := s.queryRow(ctx, q, &n)
		switch {
		case err == nil:
			return true, nil
		case isServerError(err):
			return false, nil
		default:
			return false, err
		}
	}

	if !info.IsAzureSQLDB {
		// Ask the server what the login holds rather than counting visible
		// sessions. Counting works in practice, since an instance always has
		// dozens of system sessions, but it answers a different question and
		// would be wrong the day it is asked on something quieter.
		var granted int
		err := s.queryRow(ctx,
			`SELECT CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
			              OR HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE') = 1
			         THEN 1 ELSE 0 END
			 OPTION (RECOMPILE, MAXDOP 1)`, &granted)
		switch {
		case err == nil && granted == 1:
			caps = caps.With(model.CapInstanceWideView)
		case err != nil && !isServerError(err):
			return 0, err
		}
	}

	checks := []struct {
		from string
		cap  model.Capability
		skip bool
	}{
		{from: "sys.dm_os_schedulers", cap: model.CapSchedulerLoad},
		{from: "sys.dm_os_wait_stats", cap: model.CapWaitStatsCumulative},
		{from: "sys.dm_db_task_space_usage", cap: model.CapTempdbPerTask},
		{from: "sys.dm_tran_version_store_space_usage", cap: model.CapVersionStoreUsage},
		{
			from: "sys.dm_os_ring_buffers WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'",
			cap:  model.CapRingBufferCPU,
			skip: info.IsAzureSQLDB,
		},
		{
			// Lightweight profiling v3 is on by default from 2019 and on
			// Azure SQL Database. Below that it needs trace flag 7412, which
			// the tool will not set, so the feature is simply absent. Spec
			// section 3.
			from: "sys.dm_exec_query_statistics_xml(@@SPID)",
			cap:  model.CapLivePlanProgress,
			skip: !info.IsAzureSQLDB && info.MajorVersion < 15,
		},
	}
	for _, c := range checks {
		if c.skip {
			continue
		}
		ok, err := can(c.from)
		if err != nil {
			return 0, err
		}
		if ok {
			caps = caps.With(c.cap)
		}
	}
	return caps, nil
}

const costQuery = `
SELECT cpu_time, logical_reads
FROM sys.dm_exec_sessions
WHERE session_id = @@SPID
OPTION (RECOMPILE, MAXDOP 1)`

func (s *Source) Cost(ctx context.Context) (model.Cost, error) {
	var c model.Cost
	if err := s.queryRow(ctx, costQuery, &c.CPUMs, &c.LogicalReads); err != nil {
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
