package model

// Column is one grid column: the name the configuration file and the
// interface use for it, its heading, its width in pixels, and whether it is
// drawn when nothing says otherwise.
//
// Field is the readable name of spec section 8.1, not the terse name the
// same value carries on the wire. The two are deliberately different: the
// wire spends its bytes 800 times a second and the file is read by a person
// once a month, so "logical_reads" belongs in one and "rd" in the other.
type Column struct {
	Field string
	Title string
	Width int
	// Width is a floor, not a size: the tables lay out to their content, so
	// a column is as wide as the wider of this and what is in it. It is set
	// close to the heading, because anything more is space taken from the
	// column that has something to say. The widest column of each view
	// absorbs whatever the window has left over; see head() in app.js.
	//
	// Default is what a column does when the configuration says nothing
	// about it, which is also what a column added by a later version does
	// to everybody who already saved a layout. Most are on; the two that
	// are off are off because they are usually blank or already visible
	// some other way, not because they are unimportant.
	Default bool
}

// ViewDef is one of the views of spec section 7: its identifier in the
// configuration file, its tab label, its keyboard shortcut, and the columns
// it can show in the order it shows them.
//
// Key is empty for a list that is part of another view rather than a tab of
// its own, which today is the lock list inside the transactions view. It is
// a view for the purpose of configuring its columns, and not one for the
// purpose of switching to it.
type ViewDef struct {
	ID      string
	Title   string
	Key     string
	Columns []Column
}

// requestColumns is shared by the requests and blocking views: the same
// rows, read two ways. The two lists differ only in their defaults, which
// is why they are built from one function rather than written twice.
func requestColumns(depth, wide bool) []Column {
	return []Column{
		{"spid", "spid", 48, true},
		{"blocking_depth", "depth", 56, depth},
		{"status", "status", 70, true},
		{"database", "database", 90, true},
		{"login", "login", 90, true},
		{"host", "host", 80, wide},
		{"program", "program", 160, true},
		{"command", "command", 90, true},
		{"wait_type", "wait type", 110, true},
		{"wait_ms", "wait ms", 70, true},
		{"elapsed", "elapsed ms", 90, true},
		{"cpu_ms", "cpu ms", 70, true},
		{"logical_reads", "reads", 70, wide},
		{"writes", "writes", 70, wide},
		{"tempdb_mb", "tempdb MB", 80, wide},
		{"memory_grant_mb", "grant MB", 80, wide},
		{"dop", "dop", 44, wide},
		// Blank on everything but BACKUP, DBCC and a handful of others,
		// so it is off until somebody is watching one of those.
		{"percent_complete", "progress", 80, false},
		{"blocked_by", "blocked by", 90, true},
		{"sql_text", "SQL text", 520, true},
	}
}

// ViewCatalogue is every view and every column each one can draw. It lives
// in Go rather than only in the browser for the reason DashboardCatalogue
// does: the configuration file has to be able to list every column with an
// explicit true or false, so a user can reorder and hide without knowing
// the names by heart. internal/web/catalogue_test.go checks the shipped
// app.js against it so the two cannot drift.
var ViewCatalogue = []ViewDef{
	// The chain depth is drawn as the indentation of the SQL text in the
	// requests view, so as a column of its own there it repeats what is on
	// screen. In the blocking view the depth is the point, and the wide
	// resource columns are not, so the two defaults are mirror images.
	{ID: "requests", Title: "requests", Key: "r", Columns: requestColumns(false, true)},
	{ID: "blocking", Title: "blocking", Key: "b", Columns: requestColumns(true, false)},

	{ID: "sessions", Title: "sessions", Key: "u", Columns: []Column{
		{"spid", "spid", 48, true},
		{"login", "login", 110, true},
		{"host", "host", 90, true},
		{"program", "program", 200, true},
		{"status", "status", 70, true},
		{"database", "database", 90, true},
		// Two clocks, not one. connected is the physical connection's age;
		// since reset is the age of the current use of it, because a pooled
		// connection handed back and taken out again is reset and that
		// moves login_time to now. The counters that follow are reset with
		// it, which is why since_reset sits immediately before them: the
		// scope belongs next to what it scopes.
		{"connected", "connected", 80, true},
		{"since_reset", "since reset", 90, true},
		{"idle", "idle", 70, true},
		{"open_tran", "open tran", 76, true},
		// The reason this view exists: a session idle for an hour with a
		// transaction open for an hour.
		{"tran_age", "tran age", 76, true},
		{"cpu_ms", "cpu ms", 70, true},
		{"logical_reads", "reads", 70, false},
		{"writes", "writes", 70, false},
		{"memory_mb", "memory MB", 80, false},
	}},

	// x rather than t: xact is the engine's own abbreviation, in
	// XACT_STATE and in every sys.dm_tran_ view, and t is the throughput
	// view of section 7.
	{ID: "transactions", Title: "transactions", Key: "x", Columns: []Column{
		{"xid", "transaction", 110, false},
		{"spid", "spid", 48, true},
		{"name", "name", 150, true},
		{"age", "age", 70, true},
		{"state", "state", 90, true},
		{"type", "type", 80, true},
		{"database", "database", 110, true},
		{"databases", "databases", 80, false},
		{"log_mb", "log MB", 70, true},
		{"log_records", "log records", 90, false},
	}},

	// Part of the transactions view rather than a tab of its own: an open
	// transaction and what it has locked are one question.
	{ID: "locks", Title: "locks held", Columns: []Column{
		{"spid", "spid", 48, true},
		{"database", "database", 110, true},
		{"resource_type", "resource", 80, true},
		{"object", "object", 200, true},
		{"mode", "mode", 60, true},
		{"status", "status", 70, true},
		{"count", "locks", 70, true},
	}},

	// Part of the request view rather than a tab of its own: it describes
	// one running statement, so it belongs under the row that named it.
	{ID: "plan", Title: "plan progress", Columns: []Column{
		{"node", "node", 56, true},
		{"operator", "operator", 220, true},
		{"object", "object", 160, true},
		{"rows", "rows", 90, true},
		{"estimated", "estimated", 90, true},
		{"progress", "of estimate", 90, true},
		{"threads", "threads", 70, false},
		// Only maintained under full profiling. Lightweight profiling,
		// which is what is on by default from SQL Server 2019 and what
		// this feature relies on, keeps row counts and leaves these at
		// zero, so they are off: a column that reads zero on every server
		// anybody will point this at is the plausible-looking wrong
		// answer, not a measurement.
		{"elapsed_ms", "elapsed ms", 90, false},
		{"cpu_ms", "cpu ms", 70, false},
		{"reads", "reads", 70, false},
	}},

	// What one session has been seen doing over the retention window. Part
	// of the request view rather than a tab: it is about the selected row.
	{ID: "history", Title: "session history", Columns: []Column{
		{"last_seen", "last seen", 90, true},
		{"seen_for", "seen for", 90, true},
		// How many ticks it was seen in, which is the only honest measure
		// of duration here: the window samples, it does not record.
		{"samples", "samples", 76, false},
		{"command", "command", 90, true},
		{"database", "database", 100, true},
		// On by default because a session id is reused, so two unrelated
		// logins can hold the same number inside one window. Seeing the
		// login and the program is what makes that visible rather than
		// silently merged.
		{"login", "login", 100, true},
		{"program", "program", 160, true},
		{"max_elapsed", "max elapsed", 100, true},
		{"max_cpu", "max cpu ms", 90, true},
		{"max_reads", "max reads", 90, false},
		{"top_wait", "waited on", 130, true},
		{"sql_text", "SQL text", 520, true},
	}},

	// One session's accumulated waits. Reset when a pooled connection is
	// handed out again, so they cover the current use of it.
	{ID: "sessionwaits", Title: "session waits", Columns: []Column{
		{"wait_type", "wait type", 180, true},
		{"share", "share", 80, true},
		{"wait_ms", "wait ms", 90, true},
		{"waits", "waits", 80, true},
		{"max_wait_ms", "longest ms", 90, true},
		// The part of the wait that was the thread queueing for a
		// scheduler after being signalled, rather than waiting for the
		// resource. High signal time is a CPU pressure reading, not a
		// resource one.
		{"signal_ms", "signal ms", 90, false},
	}},

	{ID: "logs", Title: "transaction logs", Key: "l", Columns: []Column{
		{"database", "database", 150, true},
		{"size_mb", "size MB", 80, true},
		{"used_mb", "active MB", 80, true},
		{"used_percent", "used", 60, true},
		// What is actually stopping the log being reused, which the
		// percentage on its own never says.
		{"reuse_wait", "reuse wait", 160, true},
		{"recovery_model", "recovery", 90, true},
		{"state", "state", 70, false},
	}},
}

// ViewByID returns the catalogue entry for a view.
func ViewByID(id string) (ViewDef, bool) {
	for _, v := range ViewCatalogue {
		if v.ID == id {
			return v, true
		}
	}
	return ViewDef{}, false
}
