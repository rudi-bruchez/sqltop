// Package buildinfo reports which build of sqltop is running.
//
// The version is a constant, and the commit comes from the toolchain rather
// than from build flags: runtime/debug.ReadBuildInfo carries the VCS revision
// and dirty state that `go build` records on its own. A version that depends
// on the build command is a version that is wrong the first time someone
// builds it differently.
package buildinfo

import "runtime/debug"

// Version follows spec section 11: zero-major while the shape can change.
// 0.1 was the collector and a working request grid; 0.2 adds the server
// dashboard. The CHANGELOG is the authority on what each one covers.
const Version = "0.2.0"

// Revision returns the commit this binary was built from, and whether the
// tree was dirty. Both are empty and false when the build carried no VCS
// information, which happens for `go run` and in some test configurations.
func Revision() (rev string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty
}

// String is what --version prints and what the interface header shows.
func String() string {
	out := "sqltop " + Version
	rev, dirty := Revision()
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		out += " (" + rev
		if dirty {
			out += ", dirty"
		}
		out += ")"
	}
	return out
}
