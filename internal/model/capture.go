package model

import "time"

// CapturedStatement is one completed batch or RPC on a watched session.
//
// Durations are microseconds because Extended Events reports microseconds,
// and a 400 microsecond batch rounded to zero milliseconds is a zero that
// lies about exactly the statement this feature exists to show.
type CapturedStatement struct {
	At            time.Time `json:"at"`
	Kind          string    `json:"kind"` // "batch" or "rpc"
	Object        string    `json:"object,omitempty"`
	Text          string    `json:"text"`
	DurationUs    int64     `json:"duration_us"`
	CPUUs         int64     `json:"cpu_us"`
	LogicalReads  int64     `json:"logical_reads"`
	PhysicalReads int64     `json:"physical_reads"`
	Writes        int64     `json:"writes"`
	RowCount      int64     `json:"row_count"`
	Result        string    `json:"result,omitempty"`
	Database      string    `json:"database,omitempty"`
	Application   string    `json:"application,omitempty"`
	User          string    `json:"user,omitempty"`
}

// CaptureProgress is what a drain reports alongside the statements. Missed
// and Dropped are different losses: Missed passed through the buffer between
// two reads, Dropped never reached the buffer. Reporting one as the other
// would hide which is happening, and they have different cures.
type CaptureProgress struct {
	Total int64
	// Seen is the absolute index the caller has consumed through after this
	// read. Equal to Total on a whole read, lower on a truncated one, and it
	// is what the caller must adopt as its next mark.
	Seen    int64
	Missed  int64
	Dropped int64
	// Truncated means the document held less than the buffer: newer events
	// are in the buffer and could not be returned this time. Placement
	// still holds, and Missed stays exact wherever the buffer evicts, which
	// it always does under the caps this tool sets.
	//
	// It does not hold for a buffer that never evicts. There the oldest
	// events stay put, every read returns the same window, Seen stops
	// advancing and Missed reads zero while the workload runs past unseen.
	// Only this flag would say so. Anything raising the event or memory cap
	// far enough to overflow a 4 MB document has to face that.
	Truncated bool
}

// CaptureNote is another capture running on the same instance, named by what
// it watches rather than by what it is called on the server. Event session
// names are a SQL Server concept and spec section 4.1 keeps those below the
// source seam.
type CaptureNote struct {
	// Name is the source's own identifier for the capture, never rendered.
	// It is here so a manager can tell its own capture from somebody else's
	// when both watch one session, which is the case the panel most needs
	// to report and the one a session id cannot distinguish.
	Name      string `json:"-"`
	SessionID int64  `json:"session_id"`
	// AgeSec is computed on the server. create_time is local server time and
	// the driver hands it back tagged UTC, so any absolute instant crossing
	// this seam is wrong by the server's offset; a difference is not.
	AgeSec int64 `json:"age_sec"`
}

// StopReason is why a capture ended. Every one is shown to the user, so every
// one has wording, and the zero value is silent because it is what a running
// capture holds.
type StopReason int

const (
	StopNotStopped StopReason = iota
	StopByKey
	StopByShutdown
	StopByBrowserGone
	StopBySessionGone
	StopBySessionReused
	StopByTimeCap
	StopByServerLost
)

func (r StopReason) String() string {
	switch r {
	case StopByKey:
		return "stopped"
	case StopByShutdown:
		return "sqltop is shutting down"
	case StopByBrowserGone:
		return "the browser went away"
	case StopBySessionGone:
		return "the session ended"
	case StopBySessionReused:
		return "the connection pool handed the session to someone else"
	case StopByTimeCap:
		return "the ten minute cap"
	case StopByServerLost:
		return "the server could not be reached"
	}
	return ""
}

// CaptureState is the whole of what the panel needs, in one value.
type CaptureState struct {
	Available  bool          `json:"available"`
	Why        string        `json:"why,omitempty"`
	Active     bool          `json:"active"`
	SessionID  int64         `json:"session_id"`
	StartedAt  time.Time     `json:"started_at"`
	Stopped    string        `json:"stopped"`
	Statements int           `json:"statements"`
	Missed     int64         `json:"missed"`
	Dropped    int64         `json:"dropped"`
	Unknown    bool          `json:"unknown"`
	File       string        `json:"file,omitempty"`
	Others     []CaptureNote `json:"others"`
}
