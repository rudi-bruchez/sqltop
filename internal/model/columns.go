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
		{"spid", "spid", 60, true},
		{"blocking_depth", "depth", 70, depth},
		{"status", "status", 90, true},
		{"database", "database", 110, true},
		{"login", "login", 100, true},
		{"host", "host", 95, wide},
		{"program", "program", 200, true},
		{"command", "command", 110, true},
		{"wait_type", "wait type", 150, true},
		{"wait_ms", "wait ms", 85, true},
		{"elapsed", "elapsed ms", 100, true},
		{"cpu_ms", "cpu ms", 90, true},
		{"logical_reads", "reads", 95, wide},
		{"writes", "writes", 90, wide},
		{"tempdb_mb", "tempdb MB", 100, wide},
		{"memory_grant_mb", "grant MB", 95, wide},
		{"dop", "dop", 55, wide},
		// Blank on everything but BACKUP, DBCC and a handful of others,
		// so it is off until somebody is watching one of those.
		{"percent_complete", "progress", 90, false},
		{"blocked_by", "blocked by", 95, true},
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
		{"spid", "spid", 60, true},
		{"login", "login", 130, true},
		{"host", "host", 110, true},
		{"program", "program", 220, true},
		{"status", "status", 80, true},
		{"database", "database", 110, true},
		{"connected", "connected", 100, true},
		{"idle", "idle", 90, true},
		{"open_tran", "open tran", 85, true},
		// The reason this view exists: a session idle for an hour with a
		// transaction open for an hour.
		{"tran_age", "tran age", 100, true},
		{"cpu_ms", "cpu ms", 90, true},
		{"logical_reads", "reads", 95, false},
		{"writes", "writes", 90, false},
		{"memory_mb", "memory MB", 95, false},
	}},

	// x rather than t: xact is the engine's own abbreviation, in
	// XACT_STATE and in every sys.dm_tran_ view, and t is the throughput
	// view of section 7.
	{ID: "transactions", Title: "transactions", Key: "x", Columns: []Column{
		{"xid", "transaction", 130, false},
		{"spid", "spid", 60, true},
		{"name", "name", 150, true},
		{"age", "age", 100, true},
		{"state", "state", 110, true},
		{"type", "type", 100, true},
		{"database", "database", 130, true},
		{"databases", "databases", 90, false},
		{"log_mb", "log MB", 90, true},
		{"log_records", "log records", 100, false},
	}},

	// Part of the transactions view rather than a tab of its own: an open
	// transaction and what it has locked are one question.
	{ID: "locks", Title: "locks held", Columns: []Column{
		{"spid", "spid", 60, true},
		{"database", "database", 130, true},
		{"resource_type", "resource", 100, true},
		{"object", "object", 220, true},
		{"mode", "mode", 80, true},
		{"status", "status", 80, true},
		{"count", "locks", 90, true},
	}},

	{ID: "logs", Title: "transaction logs", Key: "l", Columns: []Column{
		{"database", "database", 160, true},
		{"size_mb", "size MB", 100, true},
		{"used_mb", "active MB", 100, true},
		{"used_percent", "used", 90, true},
		// What is actually stopping the log being reused, which the
		// percentage on its own never says.
		{"reuse_wait", "reuse wait", 160, true},
		{"recovery_model", "recovery", 110, true},
		{"state", "state", 90, false},
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
