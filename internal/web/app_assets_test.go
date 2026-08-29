package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestGridUpdatePathNeverRebuildsRowsWithInnerHTML guards the single mistake
// that would throw away the number this renderer exists for: docs/SPECS.md
// section 10.1 measured the hand-rolled, virtualised grid at 4.8 ms per
// refresh over 800 rows against 46.8 ms for Tabulator, with zero frozen time
// and zero lost selections. That number depends entirely on layout(), the
// per-tick update path in assets/app.js, touching only the handful of cells
// that actually changed. Rebuilding a whole row, or #gridBody itself,
// through innerHTML is easy to introduce while adding a column (a stray
// `entry.tr.innerHTML = ...` reads almost the same as the correct
// `entry.tds[c].innerHTML = ...`) and invisible on code review, since both
// versions render correctly; only the timing regresses. Reading the shipped
// file and failing on any innerHTML write inside layout() other than a
// single pooled cell is the cheapest check that actually catches it.
func TestGridUpdatePathNeverRebuildsRowsWithInnerHTML(t *testing.T) {
	src, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}

	body := extractFunctionBody(t, string(src), "layout")

	re := regexp.MustCompile(`\.innerHTML\s*=`)
	found := false
	for _, line := range strings.Split(body, "\n") {
		if !re.MatchString(line) {
			continue
		}
		found = true
		if !strings.Contains(line, "tds[") {
			t.Fatalf("layout() writes innerHTML on something other than a single pooled cell: %q; this is exactly the regression that would cost the 4.8ms figure in docs/SPECS.md section 10.1", strings.TrimSpace(line))
		}
	}
	if !found {
		// layout() rewriting nothing at all is also wrong, just a different
		// bug than the one this test exists to catch; fail loudly rather
		// than passing on a renderer that no longer updates cells this way.
		t.Fatal("layout() no longer writes innerHTML anywhere; this test expects the per-cell rewrite it was written to guard")
	}
}

// extractFunctionBody returns the source between the braces of
// `function name(...) { ... }`, found by counting braces from the opening
// one. It is deliberately not a JS parser, only enough to isolate one
// function's body in a file this package controls.
func extractFunctionBody(t *testing.T, src, name string) string {
	t.Helper()
	marker := "function " + name + "("
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("no function named %q in app.js", name)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("function %q has no body", name)
	}
	start := i + open
	depth := 0
	for p := start; p < len(src); p++ {
		switch src[p] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : p+1]
			}
		}
	}
	t.Fatalf("function %q body never closes", name)
	return ""
}
