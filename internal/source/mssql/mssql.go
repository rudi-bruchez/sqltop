// Package mssql is the SQL Server implementation of source.Source.
//
// Everything here is read-only, with one exception: capture.go implements
// source.Capturer, and behind the -capture flag it creates and drops exactly
// one named Extended Events session. Spec section 2.
package mssql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	_ "github.com/microsoft/go-mssqldb/integratedauth/krb5"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
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
	// versionStore differentiates the version store's size into the growth
	// rate spec section 6 asks for. Owned by the space tier's goroutine,
	// touched only under mu like everything else on this struct. A value
	// rather than a pointer so a zero-value Source, which the gate tests
	// build directly, has a usable one without New having run.
	versionStore rateState

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

	// captureAllowed comes from -capture and is the whole of the permission
	// to write to the monitored server. Nothing in this package creates,
	// starts or drops an event session while it is false, the sweep
	// included: a sweep is a DROP like any other.
	captureAllowed bool
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

const spidQuery = `SELECT @@SPID OPTION (MAXDOP 1)`

// AppName is what this tool calls itself on the connection, and therefore
// what shows in program_name, in sys.dm_exec_sessions, in an Extended Events
// session and in whatever the DBA is already using to watch their own
// server. The version is in it on purpose: the first question about any
// unexpected load is which build produced it, and "go-mssqldb" answers
// neither half of that.
//
// It is not what the tool filters its own requests with. The grid does that
// with @@SPID inside the query, which is exact, survives a reconnection
// changing the session id, and does not hide a colleague's sqltop watching
// the same instance from the other side of the building. Hiding that would
// be hiding a real session that is really costing the server something,
// which is the opposite of what this tool is for; anyone who wants it gone
// has a program filter on the grid.
var AppName = "sqltop " + buildinfo.Version

// withAppName adds the application name to a DSN that does not already set
// one. An explicit one always wins: somebody who named their connection did
// it for a reason, most likely a firewall rule or a Resource Governor
// classifier that reads it.
//
// Both DSN shapes go-mssqldb accepts have to be handled: the URL form
// (sqlserver://host?database=x) and the ADO form (server=x;user id=y). In
// both cases the parameter is appended to the string rather than the string
// being rebuilt, because a DSN carries a password and round-tripping one
// through url.Parse and url.String re-encodes the userinfo. The result
// decodes to the same password, so it works, but a function that silently
// rewrites a credential is one nobody should have to reason about; a test
// caught this doing exactly that to p%40ss%3Bword.
//
// A DSN of neither shape is handed back unchanged rather than guessed at.
// Failing to name the connection is a cosmetic loss and corrupting it is
// not.
func withAppName(dsn string) string {
	if hasAppName(dsn) {
		return dsn
	}
	if strings.HasPrefix(strings.ToLower(dsn), "sqlserver://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "app+name=" + url.QueryEscape(AppName)
	}
	if strings.Contains(dsn, "=") {
		sep := ";"
		if strings.HasSuffix(strings.TrimSpace(dsn), ";") {
			sep = ""
		}
		return dsn + sep + "app name=" + AppName
	}
	return dsn
}

// hasAppName reports whether the DSN already names the application, under
// either of the two spellings the driver accepts.
func hasAppName(dsn string) bool {
	lower := strings.ToLower(dsn)
	for _, k := range []string{"app name", "app+name", "app%20name", "application name"} {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func (s *Source) Open(ctx context.Context, dsn string) error {
	connector, err := mssql.NewConnector(withAppName(dsn))
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

// exec runs a statement that returns nothing, on the same terms as queryRow:
// one at a time on the pinned connection, with a dead connection handed to
// repairLocked rather than retried. Only the capture uses it, behind its own
// flag; see capture.go.
func (s *Source) exec(ctx context.Context, q string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connLocked(ctx)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, q)
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
OPTION (MAXDOP 1)`

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
	info.Deployment = s.deployment(ctx, engineEdition)

	if !info.IsAzure() && info.MajorVersion > 0 && info.MajorVersion < 11 {
		return info, 0, fmt.Errorf("%w (found %s)", ErrVersionTooOld, info.ProductVersion)
	}

	caps, err := s.probe(ctx, info)
	if err != nil {
		return info, 0, fmt.Errorf("mssql: probing capabilities: %w", err)
	}

	// CapRequestDOP is a version fact, not a login right, so it is decided
	// here rather than inside probe, which only asks the server what a login
	// can read. It rides in caps because caps is what reaches the browser,
	// and buildRequestsQuery gates the column on the same bit, so the grid
	// greys it on exactly the servers where the query substitutes a literal
	// zero. See supportsRequestDOP in requests.go.
	if supportsRequestDOP(info) {
		caps = caps.With(model.CapRequestDOP)
	}

	// Discovered once here, alongside every other server fact, rather than
	// on every counters tick: see readCommittedSnapshotAnywhere. A failure
	// reading it is treated as "no", the safe default, rather than failing
	// Identify itself over a fact only one dashboard tile depends on.
	info.HasReadCommittedSnapshot = s.readCommittedSnapshotAnywhere(ctx)

	// Uptime, the last item on the first row of spec section 6's dashboard
	// table. Read on the same terms as the fact above: a failure leaves the
	// zero time, which the wire maps to "unknown" and the page renders as
	// no uptime at all rather than as a duration counted from the year 1.
	info.StartedAt = s.startTime(ctx)

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

// readCommittedSnapshotQuery answers whether any database on the instance
// has read committed snapshot isolation on. sys.databases is a catalogue
// view: a login only sees the rows for databases it may see at all, which on
// Azure SQL Database is just the one it is scoped to, and that is exactly
// the question worth asking there too.
const readCommittedSnapshotQuery = `
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM sys.databases WHERE is_read_committed_snapshot_on = 1
) THEN 1 ELSE 0 END
OPTION (MAXDOP 1)`

// readCommittedSnapshotAnywhere gates the longest-running-transaction
// figure: Transactions:Longest Transaction Running Time is only populated
// under read committed snapshot isolation (spec section 6), and there is no
// per-counter way to ask the DMV that directly. A query failure here (a
// login that cannot even see its own row in sys.databases, or a connection
// that broke mid-probe) comes back false, the same "unavailable" a genuine
// absence produces, rather than failing the whole of Identify over a fact
// one dashboard tile depends on.
func (s *Source) readCommittedSnapshotAnywhere(ctx context.Context) bool {
	var on int
	if err := s.queryRow(ctx, readCommittedSnapshotQuery, &on); err != nil {
		return false
	}
	return on == 1
}

// managedMarkerQuery asks whether the databases the two managed services
// install are present. DB_ID needs no permission of its own; it answers
// NULL both for a database that does not exist and for one the login may
// not see, which is why this can only ever be a positive detection.
//
// rdsadmin is Amazon RDS for SQL Server, cloudsqladmin is Google Cloud SQL.
// Neither service reports itself through EngineEdition, which says Standard
// or Enterprise exactly as a machine in a cupboard would, so a marker
// database is the only thing there is to go on.
const managedMarkerQuery = `
SELECT CASE WHEN DB_ID('rdsadmin') IS NULL THEN 0 ELSE 1 END,
       CASE WHEN DB_ID('cloudsqladmin') IS NULL THEN 0 ELSE 1 END
OPTION (MAXDOP 1)`

// deployment names where this engine runs, with the certainty each source
// deserves. EngineEdition is the engine describing itself and is taken as
// fact. The marker databases are taken as fact when present and as nothing
// when absent. Everything left over is the default, and the default is
// deliberately not called "on premises": nothing available here can tell a
// server in a cupboard from a virtual machine in somebody's cloud running
// an ordinary SQL Server, and claiming otherwise would be exactly the kind
// of plausible, unfounded answer this tool refuses to give about a figure.
//
// A failure of the marker query leaves the default rather than an error.
// This is a label, and losing a connection over a label would be absurd.
// knownDeployment reads info under the lock Identify writes it under. Empty
// until Identify has finished, which callers have to treat as "not yet
// known" rather than as a deployment. Never called from anything already
// holding s.mu.
func (s *Source) knownDeployment() model.Deployment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info.Deployment
}

func (s *Source) deployment(ctx context.Context, engineEdition int) model.Deployment {
	switch engineEdition {
	case 5:
		return model.DeploymentAzureSQLDB
	case 8:
		return model.DeploymentAzureMI
	case 6, 9:
		// 6 is Synapse dedicated, 9 is Synapse serverless. Named rather
		// than left in the default because Synapse is neither on premises
		// nor a virtual machine and saying so would be wrong.
		return model.DeploymentAzureSynapse
	case 11:
		return model.DeploymentAzureSQLEdge
	}

	var rds, gcp int
	if err := s.queryRow(ctx, managedMarkerQuery, &rds, &gcp); err != nil {
		return model.DeploymentOnPremisesOrVM
	}
	switch {
	case rds == 1:
		return model.DeploymentAmazonRDS
	case gcp == 1:
		return model.DeploymentGoogleCloudSQL
	}
	return model.DeploymentOnPremisesOrVM
}

// startTimeQuery reads when the instance last started. sys.dm_os_sys_info is
// one of the few OS-level views Azure SQL Database does expose, and
// sqlserver_start_time is present on it there as well as on-premises, so
// this needs no Azure branch. It does need VIEW SERVER STATE, which is why
// the caller tolerates a failure rather than propagating it.
//
// It is the one read in this file with no capability gate in front of it,
// which an external review flagged and which is deliberate: readCounters
// gates because it would otherwise pay for a failing round trip every
// second forever, and that reasoning does not reach a query that runs once
// at connection and once more after a reconnection. A gate here would be a
// probe query to avoid a probe query.
const startTimeQuery = `
SELECT sqlserver_start_time FROM sys.dm_os_sys_info
OPTION (MAXDOP 1)`

// startTime returns the instance start time, or the zero time when it
// cannot be read. Zero is a real answer here, not a silent failure: the
// wire sends zero as "no start time", and the page shows no uptime rather
// than a fabricated one. Deliberately not an error: a login that cannot
// read sys.dm_os_sys_info can still monitor its own sessions, and losing
// the whole connection over one dashboard field would be the wrong trade.
//
// The value comes back as the server's own local wall clock with no
// offset, so it is interpreted in the same location the driver hands it
// over in and never adjusted here. A server in another time zone from the
// browser will show an uptime off by that difference, which is worth
// knowing about and is not worth guessing a correction for.
func (s *Source) startTime(ctx context.Context) time.Time {
	var at time.Time
	if err := s.queryRow(ctx, startTimeQuery, &at); err != nil {
		return time.Time{}
	}
	return at
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

// canQueryTemplate is can's shape, named so the query-hint sweep in
// mssql_test.go can see it without duplicating it: %s is the DM object,
// substituted with fmt.Sprintf rather than concatenation.
const canQueryTemplate = "SELECT COUNT(*) FROM (SELECT TOP (1) 1 AS x FROM %s) AS probe OPTION (MAXDOP 1)"

// instanceWideViewGrantQuery answers, on a real instance or a managed
// instance, whether this login holds the right that gates
// sys.dm_os_performance_counters and sys.dm_db_file_space_usage: VIEW
// SERVER STATE, or VIEW SERVER PERFORMANCE STATE from SQL Server 2022.
// Azure SQL Database has no server-level state to hold that right over, so
// probe answers the same question for it a different way - see the
// IsAzureSQLDB branch below.
const instanceWideViewGrantQuery = `
SELECT CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
              OR HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER PERFORMANCE STATE') = 1
         THEN 1 ELSE 0 END
OPTION (MAXDOP 1)`

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
		q := fmt.Sprintf(canQueryTemplate, from)
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

	// CapInstanceWideView means whichever right actually gates the two
	// server.go tiers that need it: VIEW SERVER STATE or VIEW SERVER
	// PERFORMANCE STATE on a real instance or a managed instance, and the
	// database-scoped equivalent on Azure SQL Database, which has no
	// server-level state to hold that right over. Different rights, same
	// question: can this login read sys.dm_os_performance_counters and
	// sys.dm_db_file_space_usage.
	if !info.IsAzureSQLDB {
		// Ask the server what the login holds rather than counting visible
		// sessions. Counting works in practice, since an instance always has
		// dozens of system sessions, but it answers a different question and
		// would be wrong the day it is asked on something quieter.
		var granted int
		err := s.queryRow(ctx, instanceWideViewGrantQuery, &granted)
		switch {
		case err == nil && granted == 1:
			caps = caps.With(model.CapInstanceWideView)
		case err != nil && !isCapabilityAbsent(err):
			return 0, err
		}
	} else {
		// A scoped single database cannot be asked about server-level
		// state, so the question becomes whether the login can actually
		// read the two views the capability gates, tested the same way
		// every other DMV capability here is tested. Both are required:
		// either succeeding alone would leave the other tier gated open
		// onto a query that fails every tick.
		countersOK, err := can("sys.dm_os_performance_counters")
		if err != nil {
			return 0, err
		}
		spaceOK, err := can("tempdb.sys.dm_db_file_space_usage")
		if err != nil {
			return 0, err
		}
		if countersOK && spaceOK {
			caps = caps.With(model.CapInstanceWideView)
		}
	}

	// CapKillSession has no entry in this list and is never set anywhere in
	// this file. The kill flow is spec section 9.1, and it does not ship
	// until the UI plan lands it; setting the capability with nothing able
	// to act on it would be premature. It is worth naming explicitly rather
	// than leaving the gap to be rediscovered as a bug: on the wire, a
	// login that never gets this capability is indistinguishable from one
	// that genuinely lacks ALTER ANY CONNECTION, since nothing here probes
	// for that right either. Whoever adds the kill flow adds the probe (it
	// would look like the entries below: a can() check against something
	// that needs the right, or, since ALTER ANY CONNECTION is not itself
	// queryable as a DMV, a direct HAS_PERMS_BY_NAME call) and the entry in
	// this list together.
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
			// SQL Server 2016 and later, and both Azure engines, whose
			// 12.0.x product version would otherwise read as 2014 here.
			from: "sys.dm_exec_session_wait_stats",
			cap:  model.CapSessionWaitStats,
			skip: !info.IsAzure() && info.MajorVersion < 13,
		},
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
OPTION (MAXDOP 1)`

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

// estimatedPlanQueryTemplate reads the compiled plan of one running
// statement, which is the estimated plan: the shape the optimiser chose,
// with its estimates, before any of it ran.
//
// sys.dm_exec_text_query_plan rather than sys.dm_exec_query_plan, and with
// the statement offsets rather than without. The offsets give the statement
// the request is on rather than the whole batch it came from, and the text
// form returns nvarchar rather than xml, which is what stops a deeply
// nested plan failing outright: the xml form refuses a plan more than a
// hundred and twenty-eight levels deep, and those are exactly the plans
// somebody wants to look at.
//
// The two substitutions are a session id and a request id, integers by
// type before they reach here.
const estimatedPlanQueryTemplate = `
SELECT ISNULL(p.query_plan, N'')
FROM sys.dm_exec_requests AS r
CROSS APPLY sys.dm_exec_text_query_plan(r.plan_handle, r.statement_start_offset, r.statement_end_offset) AS p
WHERE r.session_id = %d AND r.request_id = %d
OPTION (MAXDOP 1)`

// livePlanQueryTemplate reads the plan of a running statement with the row
// counts it has produced so far, which is what lightweight profiling keeps.
// Same showplan XML as the estimated plan, with an actual next to every
// estimate, and it is the artefact worth saving: an estimate that turned
// out wrong is only visible beside what actually happened.
//
// It needs the same lightweight profiling PlanProgress does, so it is gated
// on the same capability. The function takes a session id; the request id
// narrows it to the statement under MARS, where one session runs several.
const livePlanQueryTemplate = `
SELECT ISNULL(CAST(x.query_plan AS nvarchar(max)), N'')
FROM sys.dm_exec_query_statistics_xml(%d) AS x
WHERE x.request_id = %d
OPTION (MAXDOP 1)`

// Plan returns a running request's plan as showplan XML: with the row
// counts it has produced so far when live is asked for and the server can
// keep them, and as the optimiser compiled it otherwise.
// The two queries are built and sent in their own branches rather than
// through a shared helper taking a query string. TestEveryQuerySentComesFromTheCatalogue
// resolves one level of data flow, from a local assignment to a catalogued
// name, and a query arriving as a function parameter is invisible to it.
// That check refusing this is the check working.
func (s *Source) Plan(ctx context.Context, ref model.RequestRef, live bool) (model.Plan, error) {
	var xml string
	var err error
	if live {
		if _, caps := s.snapshot(); !caps.Has(model.CapLivePlanProgress) {
			return model.Plan{}, errNoPlanProgress
		}
		q := fmt.Sprintf(livePlanQueryTemplate, ref.SessionID, ref.RequestID)
		err = s.queryRow(ctx, q, &xml)
	} else {
		q := fmt.Sprintf(estimatedPlanQueryTemplate, ref.SessionID, ref.RequestID)
		err = s.queryRow(ctx, q, &xml)
	}
	return planResult(xml, err, ref, live)
}

// planResult turns what came back into a Plan or into an error a person can
// act on.
func planResult(xml string, err error, ref model.RequestRef, live bool) (model.Plan, error) {
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return model.Plan{}, fmt.Errorf("mssql: session %d is not running request %d any more", ref.SessionID, ref.RequestID)
	case isCapabilityAbsent(err):
		return model.Plan{}, fmt.Errorf("mssql: this login may not read execution plans: %w", err)
	default:
		return model.Plan{}, err
	}
	if xml == "" {
		return model.Plan{}, fmt.Errorf("mssql: session %d has no plan to show; the engine keeps none for some statements, and drops one under memory pressure", ref.SessionID)
	}
	return model.Plan{Format: "showplan-xml", Payload: []byte(xml), Live: live}, nil
}
