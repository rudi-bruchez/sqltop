package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestGridUpdatePathNeverWritesMarkupOutsideItsSetupRegion guards the
// single mistake that would throw away the number this renderer exists
// for: docs/SPECS.md section 10.1 measured the hand-rolled, virtualised
// grid at 4.8 ms per refresh over 800 rows against 46.8 ms for Tabulator,
// with zero frozen time and zero lost selections. That number depends on
// the per-tick update path touching only the handful of cells that
// actually changed; rebuilding a whole row, or #gridBody itself, through
// innerHTML, outerHTML or insertAdjacentHTML is easy to introduce while
// adding a column and invisible on code review, since the mistaken
// version still renders correctly and only the timing regresses.
//
// Fix round 1, task 14: this replaces an earlier version scoped to
// layout()'s own function body, found by brace-counting its name. A
// reviewer mutated the shipped app.js six ways and found that version
// wrong on four of them: it went green when the identical row rebuild
// moved into a helper function layout() called (the single most natural
// refactor someone adding a column would make, since the mistake simply
// stopped being inside layout() by name); green when a trailing comment
// happened to contain the substring "tds["; red, with a misleading
// message, on a legitimate per-cell insertAdjacentHTML, an API it never
// knew about; and it would have stayed green on the identical mistake
// spelled with outerHTML instead of innerHTML too, which it also never
// checked for.
//
// So this scans the whole file rather than one named function, for all
// three markup-writing APIs, with comments stripped first so a match can
// only ever come from real code. A write outside the explicitly marked
// setup region (head(), which legitimately rebuilds the header row once
// from a fixed column list) is allowed only when it targets a single
// pooled cell, entry.tds[c], the shape layout() actually uses; anything
// else, wherever in the file it lives, fails. This is an allowlist
// someone has to edit on purpose when the render path's real shape
// changes, which is the point: silence must never be this test's default
// answer to an unfamiliar shape.
func TestGridUpdatePathNeverWritesMarkupOutsideItsSetupRegion(t *testing.T) {
	src, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")

	setupStart, setupEnd := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "setup-region: begin") {
			setupStart = i
		}
		if strings.Contains(line, "setup-region: end") {
			setupEnd = i
		}
	}
	if setupStart < 0 || setupEnd < 0 || setupEnd < setupStart {
		t.Fatal("could not find the \"setup-region: begin\" / \"setup-region: end\" markers in assets/app.js; this test cannot tell setup work from the render path without them")
	}

	writeRE := regexp.MustCompile(`\.(innerHTML|outerHTML)\s*=|\.insertAdjacentHTML\s*\(`)
	allowedRE := regexp.MustCompile(`entry\.tds\[[^\]]*\]\.((innerHTML|outerHTML)\s*=|insertAdjacentHTML\s*\()`)

	foundAllowed := false
	for i, raw := range lines {
		code := stripLineComment(raw)
		if !writeRE.MatchString(code) {
			continue
		}
		if i > setupStart && i < setupEnd {
			continue // inside the marked setup region: head()'s one legitimate write
		}
		if allowedRE.MatchString(code) {
			foundAllowed = true
			continue
		}
		t.Fatalf("assets/app.js:%d writes markup outside the setup region on something other than a single pooled cell (entry.tds[c]): %q; this is exactly the regression that would cost the 4.8ms figure in docs/SPECS.md section 10.1", i+1, strings.TrimSpace(code))
	}
	if !foundAllowed {
		t.Fatal("no per-cell markup write (entry.tds[c].innerHTML/outerHTML/insertAdjacentHTML) found anywhere outside the setup region; this test expects the per-cell rewrite it was written to guard, so its disappearance is itself a regression worth failing on")
	}
}

// stripLineComment truncates line at its first "//" line-comment marker, so
// the scan above only ever matches real code, never a comment that happens
// to contain a string like "tds[" or "innerHTML". "//" immediately preceded
// by ":" is treated as part of a URL rather than a comment marker; app.js
// carries none of those today, but the guard should not become fragile the
// day it does.
func stripLineComment(line string) string {
	for i := 0; i < len(line)-1; i++ {
		if line[i] == '/' && line[i+1] == '/' {
			if i > 0 && line[i-1] == ':' {
				continue
			}
			return line[:i]
		}
	}
	return line
}
