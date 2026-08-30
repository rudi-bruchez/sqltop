package web

import (
	"os/exec"
	"runtime"
)

// OpenBrowser asks the desktop to open url in whatever the user's default
// browser is. It reports the command it used, or an error, and the caller
// treats a failure as a line in the log rather than as a reason not to
// start: a tool that refuses to run on a machine with no desktop, which is
// most of the machines a DBA logs into, would be worse than one that prints
// a URL and lets them paste it.
//
// One helper per platform, all three from os/exec, no CGO and no dependency.
// Linux and the BSDs go through xdg-open, which is the desktop-neutral
// launcher every environment installs; macOS has open; Windows uses cmd's
// start, with the empty string that start takes as a window title, because
// without it a quoted URL is read as the title and nothing opens.
//
// The URL carries the per-run token, so it reaches the argument list of a
// child process, where ps shows it to any other local account. That is the
// same exposure the URL already has from being printed to the log, and it
// is why the token is per run and the server is bound to loopback; it is
// not a new one. Spec section 4.3.
func OpenBrowser(url string) (string, error) {
	name, args := browserCommand(url)
	cmd := exec.Command(name, args...)
	// Detached: the browser outlives this call, and its output belongs to
	// the desktop rather than to this tool's log.
	if err := cmd.Start(); err != nil {
		return name, err
	}
	// Reaped in the background so a browser that exits does not stay a
	// zombie for the life of the process, which can be days.
	go func() { _ = cmd.Wait() }()
	return name, nil
}

func browserCommand(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "cmd", []string{"/c", "start", "", url}
	default:
		return "xdg-open", []string{url}
	}
}
