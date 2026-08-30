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
type ViewDef struct {
	ID      string
	Title   string
	Key     string
	Columns []Column
}

// ViewCatalogue is every view and every column each one can draw. It lives
// in Go rather than only in the browser for the reason DashboardCatalogue
// does: the configuration file has to be able to list every column with an
// explicit true or false, so a user can reorder and hide without knowing
// the names by heart. internal/web/catalogue_test.go checks the shipped
// app.js against it so the two cannot drift.
var ViewCatalogue = []ViewDef{
	{ID: "requests", Title: "requests", Key: "r", Columns: []Column{
		{"spid", "spid", 60, true},
		{"status", "status", 90, true},
		{"database", "database", 110, true},
		{"login", "login", 100, true},
		{"host", "host", 95, true},
		{"program", "program", 200, true},
		{"command", "command", 110, true},
		{"wait_type", "wait type", 150, true},
		{"wait_ms", "wait ms", 85, true},
		{"elapsed", "elapsed ms", 100, true},
		{"cpu_ms", "cpu ms", 90, true},
		{"logical_reads", "reads", 95, true},
		{"writes", "writes", 90, true},
		{"tempdb_mb", "tempdb MB", 100, true},
		{"memory_grant_mb", "grant MB", 95, true},
		{"dop", "dop", 55, true},
		// Blank on everything but BACKUP, DBCC and a handful of others,
		// so it is off until somebody is watching one of those.
		{"percent_complete", "progress", 90, false},
		{"blocked_by", "blocked by", 95, true},
		// The chain depth is already drawn as the indentation of the SQL
		// text, so as a column of its own it repeats what is on screen.
		{"blocking_depth", "depth", 70, false},
		{"sql_text", "SQL text", 520, true},
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
