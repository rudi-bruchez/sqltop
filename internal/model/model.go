// Package model holds the engine-neutral types. Nothing here may name a DMV,
// a showplan, or any other SQL Server concept: that is the whole point of the
// source abstraction in spec section 4.1.
package model

import "time"

// Tier names a collection schedule. Spec section 10.
type Tier int

const (
	TierRequests Tier = iota
	TierCounters
	TierSpace
	TierCPUHistory
)

func (t Tier) String() string {
	switch t {
	case TierRequests:
		return "requests"
	case TierCounters:
		return "counters"
	case TierSpace:
		return "space"
	case TierCPUHistory:
		return "cpuHistory"
	}
	return "unknown"
}

// Capability is something a source may or may not be able to do on this
// server, at this version, with these rights.
type Capability uint32

const (
	CapLivePlanProgress Capability = 1 << iota
	CapInstanceWideView
	CapTempdbPerTask
	CapWaitStatsCumulative
	CapSchedulerLoad
	CapKillSession
	CapVersionStoreUsage
	CapRingBufferCPU
	// CapRequestDOP reports whether sys.dm_exec_requests carries a dop
	// column at all: it does not exist before SQL Server 2016, so on an
	// older server the request grid's dop figure is a substituted literal
	// zero, not a measurement. This is a version fact rather than a
	// permission, but it travels on the wire exactly like the others so the
	// UI has one channel, caps, to decide what to greet as real and what to
	// grey as unavailable, rather than a second, separate signal for
	// version-gated columns.
	CapRequestDOP
)

type Capabilities uint32

func Caps(list ...Capability) Capabilities {
	var c Capabilities
	for _, x := range list {
		c |= Capabilities(x)
	}
	return c
}

func (c Capabilities) Has(x Capability) bool          { return c&Capabilities(x) != 0 }
func (c Capabilities) With(x Capability) Capabilities { return c | Capabilities(x) }

// RequestRef identifies one running request across ticks.
type RequestRef struct {
	SessionID int64
	RequestID int32
}

// RequestSample is one observation of one active request at one instant.
// One row per sample, never one row per query: a request active for twelve
// minutes must leave a series that can be replayed. Spec section 4.
type RequestSample struct {
	At  time.Time
	Ref RequestRef

	Status    string
	Database  string
	Login     string
	Host      string
	Program   string
	Command   string
	BlockedBy int64
	// Depth is filled by the window, not the source: flattening the blocking
	// chain is engine-neutral work. Spec section 4.
	Depth int

	ElapsedMs       int64
	CPUMs           int64
	LogicalReads    int64
	PhysicalReads   int64
	Writes          int64
	TempdbMB        float64
	MemoryGrantMB   float64
	DOP             int
	OpenTran        int
	PercentComplete float64

	WaitType     string
	WaitMs       int64
	WaitResource string

	IsolationLevel string
	QueryHash      string
	// SQLText is sent once per session in the reference table, not on every
	// tick. Spec section 4.
	SQLText string
}

// Figure is one dashboard number. Available reports whether this source can
// produce it at all, which is different from it being zero.
//
// JSON tags added for internal/web's wire protocol (task 12 fix round 1):
// without them the field carries a capitalised Go name onto the wire while
// every other payload type uses short lowercase tags, which a JavaScript
// client would have to special-case.
type Figure struct {
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Available bool    `json:"available"`
}

// ServerSample is one observation of the instance as a whole.
type ServerSample struct {
	At      time.Time
	Figures map[string]Figure
}

type ServerInfo struct {
	Instance       string
	Host           string
	Edition        string
	ProductVersion string
	MajorVersion   int
	IsAzureSQLDB   bool
	IsAzureMI      bool
	StartedAt      time.Time
}

// IsAzure reports whether this is one of the Azure engines, whose reported
// product version says nothing about their feature level: both answer 12.0.x
// while sitting at or above the newest boxed release. Every decision that
// would otherwise read MajorVersion to gate a feature has to ask this first,
// or Azure gets treated as SQL Server 2014 and loses columns it has.
//
// IsAzureSQLDB on its own stays the right question for the other kind of
// decision, the one about what a scoped single database does not have at all.
func (i ServerInfo) IsAzure() bool { return i.IsAzureSQLDB || i.IsAzureMI }

// Plan is deliberately opaque. Showplan XML, an EXPLAIN tree and a MySQL plan
// have nothing in common, so the renderer dispatches on Format rather than the
// model pretending they unify. Spec section 4.1.
type Plan struct {
	Format  string // "showplan-xml" for SQL Server
	Payload []byte
	Live    bool // carries in-flight row counts
}

// Cost is what the tool has spent on the server, read from its own session.
// Spec section 10.
type Cost struct {
	At           time.Time
	CPUMs        int64
	LogicalReads int64
}
