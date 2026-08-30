package web

import (
	"runtime"
	"strings"
	"testing"
)

// TestBrowserCommandPerPlatform pins the three command shapes. Only one of
// them can be executed on any given machine, so the other two are checked as
// the strings they are; the Windows one in particular has a trap that is
// invisible unless it is written down, since start reads its first quoted
// argument as a window title and swallows the URL.
func TestBrowserCommandPerPlatform(t *testing.T) {
	const url = "http://127.0.0.1:8420/?t=abc"
	name, args := browserCommand(url)

	switch runtime.GOOS {
	case "darwin":
		if name != "open" || len(args) != 1 || args[0] != url {
			t.Errorf("darwin: %s %v", name, args)
		}
	case "windows":
		if name != "cmd" || len(args) != 4 || args[0] != "/c" || args[1] != "start" || args[2] != "" || args[3] != url {
			t.Errorf("windows: %s %v; start needs an empty title argument or it reads the URL as the title", name, args)
		}
	default:
		if name != "xdg-open" || len(args) != 1 || args[0] != url {
			t.Errorf("%s: %s %v", runtime.GOOS, name, args)
		}
	}

	// Whatever the platform, the URL has to reach the command intact: it
	// carries the token, and a mangled one opens a page that 401s.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, url) {
		t.Errorf("the url is not in the arguments: %v", args)
	}
}
