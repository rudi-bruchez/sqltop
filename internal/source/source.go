// Package source is the seam between sqltop and any database engine.
//
// Being agnostic does not mean pretending every engine is the same. It means
// the model is neutral and every source declares what it can actually do, so
// the interface adapts instead of the model lying. Spec section 4.1.
package source

import (
	"context"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

type Source interface {
	// Open connects. It must not create, alter or configure anything on the
	// server: sqltop is read-only.
	Open(ctx context.Context, dsn string) error
	Close() error

	// Identify returns instance metadata and what this source can deliver on
	// this server, at this version, with these rights. It probes rather than
	// inferring from the version alone.
	Identify(ctx context.Context) (model.ServerInfo, model.Capabilities, error)

	// SampleRequests is the hot path, called on the requests tier.
	SampleRequests(ctx context.Context) ([]model.RequestSample, error)

	// SampleServer feeds the dashboard on the slower tiers.
	SampleServer(ctx context.Context, tier model.Tier) (model.ServerSample, error)

	// Cost reports what this connection has spent on the server so far,
	// cumulative. The collector differentiates it. Spec section 10.
	Cost(ctx context.Context) (model.Cost, error)

	// Sessions, Transactions and LogSpace feed the views of spec section 7
	// that are not projections of the retention window. They are on demand,
	// like QueryText and Plan below and for the same reason: the lock
	// aggregate walks the whole lock manager, and paying for that on a
	// timer nobody is watching is exactly what section 2 rules out.
	Sessions(ctx context.Context) ([]model.SessionSample, error)
	// PlanProgress reports how far one running statement has got through
	// its plan. On demand and per request, never on a tier, and never for
	// a request nobody is watching.
	PlanProgress(ctx context.Context, ref model.RequestRef) ([]model.PlanNode, error)
	// SessionWaits reports what one session has waited on, since the engine
	// last reset its counters, which a pooled checkout does.
	SessionWaits(ctx context.Context, spid int64) ([]model.SessionWait, error)
	Transactions(ctx context.Context) ([]model.TransactionSample, []model.LockSample, error)
	LogSpace(ctx context.Context) ([]model.LogSpaceSample, error)

	// QueryText and Plan are on demand only and must never be called from a
	// polling loop.
	QueryText(ctx context.Context, ref model.RequestRef) (string, error)
	Plan(ctx context.Context, ref model.RequestRef, live bool) (model.Plan, error)
}

// CaptureHandle identifies one running capture. Opaque above this package.
type CaptureHandle struct {
	Name      string
	SessionID int64
	Started   time.Time
}

// Capturer is optional and deliberately not part of Source. Spec section 4.1:
// PostgreSQL and MySQL have no equivalent of a ring buffer target, and an
// abstraction assuming one would be wrong on two engines out of three.
//
// The only interface in this tool that writes to the monitored server, and
// nothing calls it unless the operator passed the flag that permits that.
type Capturer interface {
	// CanCapture reports whether a capture is possible here, and says why
	// not when it is not. A greyed key with no explanation is the failure
	// this project has already fixed twice in the dashboard.
	CanCapture(ctx context.Context) (bool, string, error)

	// SweepCaptures drops the event sessions under this tool's prefix that
	// are dead by construction, and never one that might be alive and
	// belong to another instance of sqltop.
	SweepCaptures(ctx context.Context) (dropped int, err error)

	// RunningCaptures reports the other captures alive on this instance, so
	// a second watcher of one session knows it is doubling the cost.
	RunningCaptures(ctx context.Context) ([]model.CaptureNote, error)

	// WatchedSession answers the only question the capture manager cannot
	// answer for itself: is the session it started on still that session.
	// The login time moves when a pooled connection is reset, and ok is
	// false when the session is gone.
	WatchedSession(ctx context.Context, spid int64) (login time.Time, ok bool, err error)

	StartCapture(ctx context.Context, spid int64) (CaptureHandle, error)

	// PollCapture returns the statements past mark, and what was lost. The
	// caller replaces its mark with the returned Seen and never with Total:
	// on a truncated read those differ, and Total would step over what the
	// document could not carry.
	PollCapture(ctx context.Context, h CaptureHandle, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error)

	StopCapture(ctx context.Context, h CaptureHandle) error
}
