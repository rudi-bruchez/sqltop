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
