package model

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

	ConnectedSec int64
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
