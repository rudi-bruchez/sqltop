package window

import (
	"sort"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// Flatten reorders rows so that every blocker sits immediately above what it
// blocks, and fills Depth. The result is a flat list with an indentation
// level, not a tree: the wire protocol carries no nesting (spec section 4).
//
// Three cases the engine really produces have to survive: a session reporting
// itself as its own blocker, a cycle between two sessions, and a blocker whose
// own request finished between the read of one row and the next. None may drop
// a row, because a request missing from the grid is worse than one shown at
// the wrong indentation.
func Flatten(rows []model.RequestSample) []model.RequestSample {
	children := make(map[int64][]model.RequestSample, len(rows))
	present := make(map[int64]bool, len(rows))
	for _, r := range rows {
		present[r.Ref.SessionID] = true
	}

	var roots []model.RequestSample
	for _, r := range rows {
		parent := r.BlockedBy
		if parent == 0 || parent == r.Ref.SessionID || !present[parent] {
			roots = append(roots, r)
			continue
		}
		children[parent] = append(children[parent], r)
	}

	byRef := func(s []model.RequestSample) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].Ref.SessionID != s[j].Ref.SessionID {
				return s[i].Ref.SessionID < s[j].Ref.SessionID
			}
			return s[i].Ref.RequestID < s[j].Ref.RequestID
		})
	}
	byRef(roots)
	for k := range children {
		byRef(children[k])
	}

	out := make([]model.RequestSample, 0, len(rows))
	// Identity is the full RequestRef: one session can hold several concurrent
	// requests, so keying this by session id alone would drop all but one of
	// them. Parenthood stays keyed by session id, because blocking_session_id
	// names a session and not a request; a blocker's children hang under the
	// first row emitted for that session, which emitted tracks separately so
	// they are attached once rather than under every sibling.
	seen := make(map[model.RequestRef]bool, len(rows))
	emitted := make(map[int64]bool, len(rows))

	var walk func(r model.RequestSample, depth int)
	walk = func(r model.RequestSample, depth int) {
		if seen[r.Ref] {
			return
		}
		seen[r.Ref] = true
		r.Depth = depth
		out = append(out, r)

		if emitted[r.Ref.SessionID] {
			return
		}
		emitted[r.Ref.SessionID] = true
		for _, c := range children[r.Ref.SessionID] {
			walk(c, depth+1)
		}
	}

	for _, r := range roots {
		walk(r, 0)
	}

	// A pure cycle has no root, so nothing above reached it. Emit whatever is
	// left at depth zero rather than losing it.
	if len(out) < len(rows) {
		leftovers := make([]model.RequestSample, 0, len(rows)-len(out))
		for _, r := range rows {
			if !seen[r.Ref] {
				leftovers = append(leftovers, r)
			}
		}
		byRef(leftovers)
		for _, r := range leftovers {
			walk(r, 0)
		}
	}
	return out
}
