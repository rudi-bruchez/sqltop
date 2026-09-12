package model

import "time"

// The types below feed the views of spec section 7 that are not projections
// of the retention window. They are read on demand, while somebody is
// looking at them, and never from the polling loop: the lock aggregate in
// particular walks the whole lock manager, which is not something to pay
// for on a timer nobody is watching.

// SessionSample is one open user session: who is connected, for how long,
// how long since they last asked the server for anything, and whether they
// are holding a transaction open.
type SessionSample struct {
	SessionID int64
	Login     string
	Host      string
	Program   string
	// Status is the session's own status, which is not a request status:
	// running, sleeping, dormant, preconnect.
	Status   string
	Database string

	// ConnectedSec is the age of the physical connection, from
	// sys.dm_exec_connections. SinceResetSec is the age of the current use
	// of it: a pooled connection handed back and taken out again is reset,
	// and that reset moves login_time to now while connect_time stays. The
	// counters below are reset with it, so they count the current use and
	// not the connection.
	ConnectedSec  int64
	SinceResetSec int64
	// IdleSec is time since the last request ended. Zero while a request is
	// running, because the engine reports no end time for one that has not
	// ended.
	IdleSec int64

	CPUMs    int64
	Reads    int64
	Writes   int64
	MemoryMB float64

	OpenTran int
	// TranSec is the age of this session's oldest open transaction, zero
	// when it holds none. A session idle for an hour with a transaction
	// open for an hour is the thing this view exists to find.
	TranSec int64
}

// TransactionSample is one open user transaction.
type TransactionSample struct {
	TransactionID int64
	SessionID     int64
	Name          string
	ElapsedSec    int64
	Type          string
	State         string
	// Database is the database the transaction has written most of its log
	// in. Databases counts how many it spans, so a distributed transaction
	// is visible as one rather than shown as if it belonged to whichever
	// database sorted first.
	Database   string
	Databases  int
	LogBytes   int64
	LogRecords int64
}

// LockSample is one group of locks held or waited on by one session,
// aggregated by database, resource type, object, mode and status. Never one
// row per lock: a single statement can hold millions, and the question an
// operator is asking is which object, not which row of which page.
type LockSample struct {
	SessionID    int64
	Database     string
	ResourceType string
	// Object is filled for OBJECT locks only, and only where the login may
	// read the name. Page, key and row locks name a partition rather than
	// an object, and resolving those means a query inside each database;
	// an empty name here is "not resolvable cheaply", never "no object".
	Object string
	Mode   string
	Status string
	Count  int64
}

// LogSpaceSample is one database's transaction log.
type LogSpaceSample struct {
	Database      string
	RecoveryModel string
	// ReuseWait is what is stopping the log being truncated, which is the
	// answer somebody looking at a full log actually wants. NOTHING when
	// there is no obstacle.
	ReuseWait string
	State     string

	SizeMB float64
	// UsedMB is the active portion: the part that cannot be reused yet.
	UsedMB      float64
	UsedPercent float64
}

// PlanNode is one operator of a running statement's plan, as the engine
// reports its progress. One row per node, threads folded together: a
// parallel plan reports the same node once per worker and an operator seen
// eight times is one operator, not eight.
//
// The times and the read counts are only maintained under full profiling.
// Lightweight profiling, which is what is on by default from SQL Server
// 2019 and what this tool relies on, keeps row counts and leaves those at
// zero. They travel anyway, for a server where somebody has turned full
// profiling on, and the columns that show them are off by default.
type PlanNode struct {
	NodeID    int
	Operator  string
	Object    string
	Rows      int64
	Estimated int64
	Threads   int
	ElapsedMs int64
	CPUMs     int64
	Reads     int64
	Writes    int64
}

// StatementSeen is one statement observed on one session over the retention
// window. It is not a log: only statements that were running at a sampling
// instant appear, so at a one second tier anything shorter than a second is
// usually missed. What it answers is what a session has been doing, which
// the current tick cannot say at all.
type StatementSeen struct {
	SessionID int64
	Login     string
	Host      string
	Program   string
	Database  string
	Command   string
	SQLText   string

	FirstAt time.Time
	LastAt  time.Time
	// Samples is how many ticks this statement was seen in, which is the
	// only honest measure of how long it ran: the window samples, it does
	// not record.
	Samples int

	MaxElapsedMs int64
	MaxCPUMs     int64
	MaxReads     int64
	// TopWait is the wait type it was most often seen waiting on, and
	// TopWaitSamples how many of its samples that was.
	TopWait        string
	TopWaitSamples int
}

// QuerySeen is one statement shape seen anywhere on the server over the
// retention window, the row of the queries view. Where StatementSeen answers
// what one session has been doing, this answers what the server has been
// doing, which is where the query that is individually quick and collectively
// ruinous becomes visible.
//
// Statements are grouped by the engine's QueryHash, which is computed over
// the parameterised shape, so the same query under a thousand literals folds
// into one row. That is the whole point here and the opposite of what the
// request grid wants, where a literal change must never be hidden. The
// database is in the key as well: one shape run against two databases is two
// answers, and folding them would put one database name on a row that
// aggregated both.
type QuerySeen struct {
	QueryHash string
	Database  string
	Command   string
	// SQLText is the most recent text seen for the shape. One sample, not a
	// canonical form: with literals folded there is no such thing.
	SQLText string

	FirstAt time.Time
	LastAt  time.Time

	// Runs is how many times the shape was seen to start. Two sightings on one
	// session are the same run when they are one tick apart and the request
	// had already been running for at least that long; consecutive ticks
	// alone are not enough, since under a stretched period most statements
	// are caught once per execution and two neighbouring sightings are then
	// two executions. Not executions: a query that begins and ends between
	// two ticks is never seen at all, and one seen in forty ticks ran once.
	// It is the closest thing to a count of executions that sampling can
	// honestly produce.
	Runs int
	// Samples is how many ticks contributed, across every run and session.
	Samples  int
	Sessions int

	// The engine's request counters are cumulative for the life of a request,
	// so a run contributes its own maximum rather than the sum of its
	// samples. TotalCPUMs is then the sum over runs, which is the figure that
	// sorts this view.
	TotalCPUMs   int64
	AvgElapsedMs int64
	MaxElapsedMs int64

	TopWait        string
	TopWaitSamples int
}

// SessionWait is one wait type accumulated by one session, from
// sys.dm_exec_session_wait_stats. The engine resets these when a pooled
// connection is handed out again, so they cover the current use of the
// connection rather than its whole life, which is the same scope as the
// session counters next to them.
type SessionWait struct {
	WaitType     string
	Waits        int64
	WaitMs       int64
	MaxWaitMs    int64
	SignalMs     int64
	SharePercent float64
}
