package web

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// TestShippedPageMatchesTheDashboardCatalogue keeps the two halves of the
// dashboard in step. The catalogue in Go decides which tiles exist and what
// they are called, and the page decides how each one is drawn; a figure the
// catalogue sends with no formatter renders as a bare integer whatever its
// units, and a formatter for a figure nobody sends is a leftover. Neither
// fails anywhere on its own.
//
// The same bargain as rowFields against the Row struct, and the query
// catalogue against the call sites: the duplication is allowed to exist
// because a test refuses to let it drift.
func TestShippedPageMatchesTheDashboardCatalogue(t *testing.T) {
	src, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	block := between(string(src), "const FMT = {", "\n};")
	if block == "" {
		t.Fatal("could not find the FMT table in assets/app.js")
	}

	inPage := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}([a-z_0-9]+):`).FindAllStringSubmatch(block, -1) {
		inPage[m[1]] = true
	}
	if len(inPage) == 0 {
		t.Fatal("read no formatters out of app.js; the shape this is parsed from has changed")
	}

	inCatalogue := map[string]bool{}
	for _, g := range model.DashboardCatalogue {
		for _, f := range g.Figures {
			inCatalogue[f.Key] = true
			if !inPage[f.Key] {
				t.Errorf("figure %q is in the catalogue and has no formatter in app.js, so its tile would render as a bare integer whatever its units", f.Key)
			}
		}
	}
	for key := range inPage {
		if !inCatalogue[key] {
			t.Errorf("app.js can format %q and the catalogue never sends it: either a leftover or a tile nobody can switch on", key)
		}
	}
}

func between(s, begin, end string) string {
	i := strings.Index(s, begin)
	if i < 0 {
		return ""
	}
	rest := s[i+len(begin):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestShippedPageMatchesTheColumnCatalogue is the same bargain one level
// down, once per view. model.ViewCatalogue decides which columns exist,
// what they are called and what the configuration file may name; the CELL_
// tables in app.js decide how each one is read and drawn. A column in the
// catalogue with no cell entry is one columnsFor silently drops, so it can
// be switched on in the file and never appear; a cell entry with no
// catalogue column is dead code nobody can reach, since the drawn list only
// ever comes from the server.
//
// The view-to-table mapping is read out of app.js's own CELLS object rather
// than repeated here, so a view that gains a registry does not also need
// this test edited to notice it.
func TestShippedPageMatchesTheColumnCatalogue(t *testing.T) {
	raw, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	table := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}([a-z_0-9]+):\s*(CELL_[A-Z_]+),`).FindAllStringSubmatch(between(src, "const CELLS = {", "\n};"), -1) {
		table[m[1]] = m[2]
	}
	if len(table) == 0 {
		t.Fatal("could not read the CELLS mapping out of app.js; the shape this is parsed from has changed")
	}

	for _, v := range model.ViewCatalogue {
		name, mapped := table[v.ID]
		if !mapped {
			t.Errorf("view %q is in the catalogue and app.js has no cell table for it, so it would draw nothing", v.ID)
			continue
		}
		inPage := cellKeys(t, src, name)
		inCatalogue := map[string]bool{}
		for _, c := range v.Columns {
			inCatalogue[c.Field] = true
			if !inPage[c.Field] {
				t.Errorf("column %q is in view %q of the catalogue and %s cannot draw it, so switching it on in sqltop.yaml would do nothing", c.Field, v.ID, name)
			}
		}
		// Only checked against the views that use this table, since two
		// views share one: a key unused by this view may be used by the
		// other, so the reverse check runs once per table below.
		_ = inCatalogue
	}

	// The reverse: a cell nothing in the catalogue asks for. Collected
	// across every view that shares a table, so a key used by blocking and
	// not by requests is not reported as dead.
	used := map[string]map[string]bool{}
	for _, v := range model.ViewCatalogue {
		name := table[v.ID]
		if used[name] == nil {
			used[name] = map[string]bool{}
		}
		for _, c := range v.Columns {
			used[name][c.Field] = true
		}
	}
	for name, fields := range used {
		for key := range cellKeys(t, src, name) {
			if !fields[key] {
				t.Errorf("%s can draw %q and no view in the catalogue offers it: dead code, since the drawn list only ever comes from the server", name, key)
			}
		}
	}
}

// cellKeys reads the field names out of one CELL_ table in app.js.
func cellKeys(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	block := between(src, "const "+name+" = {", "\n};")
	if block == "" {
		t.Fatalf("could not find %s in assets/app.js", name)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}([a-z_0-9]+):`).FindAllStringSubmatch(block, -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("read no cell renderers out of %s; the shape this is parsed from has changed", name)
	}
	return out
}
