// Package mssql is the SQL Server implementation of source.Source.
//
// Everything here is read-only. No object is created, nothing is configured,
// no trace flag is set: spec section 2.
package mssql

import (
	"context"
	"database/sql"
	"database/sql/driver"
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

	// requestsQuery is what SampleRequests actually sends, built once by
	// Identify from the version and capabilities it just found: r.dop does
	// not exist before SQL Server 2016, and sys.dm_db_task_space_usage needs
	// a right a login may not hold. New sets it from a zero-value
	// model.ServerInfo and model.Capabilities, which is already the fully
	// conservative build - see buildRequestsQuery - so a SampleRequests
	// called before Identify still returns real rows, with dop and
	// tempdb_mb pinned at zero, rather than erroring or blocking on a
	// version nobody has probed yet.
	requestsQuery string
}

func New() *Source {
	return &Source{
		counter:       newCounterState(),
		requestsQuery: buildRequestsQuery(model.ServerInfo{}, 0),
	}
}

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

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("mssql: connect: %w", err)
	}

	// pool is set before pinning because connLocked reads it, and pinning
	// here is what performs the first spid read. Both writes happen under
	// the same mutex that guards every other read of the field, even though
	// Open runs before anything else can call in: a field written outside
	// the lock two lines from a Lock() reads as an oversight rather than as
	// a fact about Open. On failure connLocked has already closed whatever
	// connection it grabbed from the pool without assigning it to s.db, so
	// there is nothing left pinned to leak; only the pool needs closing.
	s.mu.Lock()
	s.pool = pool
	_, err = s.connLocked(ctx)
	if err != nil {
		s.pool = nil
	}
	s.mu.Unlock()
	if err != nil {
		pool.Close()
		return fmt.Errorf("mssql: pinning a connection: %w", err)
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
// pool if the previous one was declared dead, and re-reading @@SPID onto it:
// a re-pinned connection is a different session, and s.spid would otherwise
// keep naming a session that no longer exists. The request sampler excludes
// the tool's own session by writing @@SPID directly into its query, not by
// comparing against s.spid; s.spid is read only by tests today. Callers must
// hold s.mu.
//
// Returns sql.ErrConnDone, the same failure a dead pinned connection reports,
// if Close has already torn the pool down; nothing here may dereference a nil
// pool.
func (s *Source) connLocked(ctx context.Context) (*sql.Conn, error) {
	if s.db != nil {
		return s.db, nil
	}
	if s.pool == nil {
		return nil, sql.ErrConnDone
	}
	conn, err := s.pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var spid int64
	if err := conn.QueryRowContext(ctx, spidQuery).Scan(&spid); err != nil {
		// Not yet pinned to s.db, so it would otherwise never be closed: the
		// pool cannot reclaim a connection it considers still in use.
		conn.Close()
		return nil, err
	}
	s.db = conn
	s.spid = spid
	return conn, nil
}

// queryRow runs one query under the lock that serialises every call this
// Source makes, holding it for the whole query-plus-scan cycle rather than
// just the call, since a *sql.Conn can have another batch put on the wire
// while a previous result set is still streaming.
//
// It also repairs the pinned connection, on the call that discovers the
// break rather than waiting for a later one. A connection failing while the
// driver is still sending the query surfaces directly as driver.ErrBadConn;
// database/sql then closes the *sql.Conn, and sql.ErrConnDone is what every
// later call on it gets. But a connection dying while a result set is being
// read back comes through neither: go-mssqldb marks it bad internally and
// hands back the raw read error (a *net.OpError, verified against this
// container by killing the session mid-query), and the sentinel comes only
// on the following call. Checking connDeadLocked, which reads the driver's
// own connectionGood bookkeeping through Conn.Raw with no network I/O,
// catches that case on this same call instead of the next one. Source does
// not retry the query that just failed, and it does not decide when the
// caller should try again - that stays the collector's job.
func (s *Source) queryRow(ctx context.Context, query string, dest ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connLocked(ctx)
	if err != nil {
		return err
	}
	err = conn.QueryRowContext(ctx, query).Scan(dest...)
	s.repairLocked(conn, err)
	return err
}

// query runs one query under the same lock queryRow uses, holding it for the
// whole query-plus-scan cycle rather than just the call that starts it: a
// *sql.Conn can have another batch put on the wire while a previous result
// set is still streaming, and here the result set streams over the pinned
// connection across the whole rows.Next() loop, not just QueryContext.
//
// scan is called once per row, with s.mu held; it must not call back into
// Source. rows.Close() runs inside an inner closure so it happens - even on
// a panic in scan, during the unwind - before repairLocked gets to look at
// the connection: closing a stream and then asking whether the connection
// survived it is a different question from asking mid-stream.
func (s *Source) query(ctx context.Context, q string, scan func(*sql.Rows) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connLocked(ctx)
	if err != nil {
		return err
	}

	err = func() error {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}()

	s.repairLocked(conn, err)
	return err
}

// repairLocked closes and drops the pinned connection when err shows it is
// dead, so the next connLocked call re-pins a fresh one from the pool
// instead of handing out a connection nothing can use again. Callers must
// hold s.mu.
//
// Close it, not just drop the reference: database/sql's pool still counts
// this *sql.Conn as open and in use until Close says otherwise, and with
// MaxOpenConns(1) the next pool.Conn(ctx) inside connLocked would block
// forever waiting for a slot that only Close frees. The underlying
// transport close is local, not a network round trip to a server that may
// already be gone, so this does not block on the dead connection either.
//
// conn.Close() runs before s.db is cleared, not the other way around, even
// though nothing can observe the field between the two statements while
// s.mu is held: clearing s.db first would leave the invariant "the pool has
// a free slot whenever s.db is nil" false for the duration of the Close
// call, and the next person reading this in a different order would have no
// way to tell that from an oversight.
func (s *Source) repairLocked(conn *sql.Conn, err error) {
	if err != nil && (errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) || connDeadLocked(conn)) {
		conn.Close()
		s.db = nil
	}
}

// connDeadLocked reports whether the driver has already marked conn dead,
// without any network round trip: go-mssqldb's *Conn implements
// driver.Validator, and IsValid just returns its own connectionGood flag.
// Safe to call right after a query on the same connection returns, since Raw
// only hands out the underlying driver connection for the duration of the
// callback and nothing else is using it concurrently under s.mu.
func connDeadLocked(conn *sql.Conn) bool {
	dead := false
	_ = conn.Raw(func(driverConn any) error {
		if v, ok := driverConn.(driver.Validator); ok && !v.IsValid() {
			dead = true
		}
		return nil
	})
	return dead
}

// isCapabilityAbsent reports whether err is the server itself answering "no"
// - permission denied, or the object does not exist on this version - rather
// than the connection having failed. The order of the checks matters:
// mssql.ServerError, which the driver returns when the server hits a fatal
// error that aborts the process and severs the connection, wraps an
// mssql.Error, so testing for mssql.Error first would misclassify the abort
// that killed the connection as an ordinary "capability absent" answer, and
// that misclassification is exactly what let a broken connection during the
// last probe evaluated leave the whole capability set silently
// under-reported with a nil error. driver.ErrBadConn and sql.ErrConnDone are
// the other two shapes a broken connection takes here. Spec section 3.1.
func isCapabilityAbsent(err error) bool {
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return false
	}
	var srvErr mssql.ServerError
	if errors.As(err, &srvErr) {
		return false
	}
	var capErr mssql.Error
	return errors.As(err, &capErr)
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
	// EngineEdition 5 is Azure SQL Database, 8 is Managed Instance. The two
	// are kept apart because they differ in what they expose: a scoped single
	// database has no instance-wide DMVs, a managed instance does.
	info.IsAzureSQLDB = engineEdition == 5
	info.IsAzureMI = engineEdition == 8

	if !info.IsAzure() && info.MajorVersion > 0 && info.MajorVersion < 11 {
		return info, 0, fmt.Errorf("%w (found %s)", ErrVersionTooOld, info.ProductVersion)
	}

	caps, err := s.probe(ctx, info)
	if err != nil {
		return info, 0, fmt.Errorf("mssql: probing capabilities: %w", err)
	}

	// Task 9's sampling goroutine reads s.info/s.caps, while the collector
	// may be re-identifying, so the write goes under the same lock every
	// query uses. requestsQuery is rebuilt here rather than kept as a fixed
	// const so it matches what this server and this login actually support.
	s.mu.Lock()
	s.info, s.caps = info, caps
	s.requestsQuery = buildRequestsQuery(info, caps)
	s.mu.Unlock()

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
		case isCapabilityAbsent(err):
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
		case err != nil && !isCapabilityAbsent(err):
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
			// both Azure engines, whose 12.0.x product version would
			// otherwise read as 2014 here. Below that it needs trace flag
			// 7412, which the tool will not set, so the feature is simply
			// absent. Spec section 3.
			from: "sys.dm_exec_query_statistics_xml(@@SPID)",
			cap:  model.CapLivePlanProgress,
			skip: !info.IsAzure() && info.MajorVersion < 15,
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

func (s *Source) SampleServer(context.Context, model.Tier) (model.ServerSample, error) {
	return model.ServerSample{Figures: map[string]model.Figure{}}, nil // task 9
}
