//go:build !windows

package desktop

import (
	"errors"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Open opens the local app in the default browser when a display exists. It
// never waits for the browser: startup must not block on a desktop that is
// missing, headless or slow. A headless session is not an error; the caller
// keeps the API running and can print the (token-free) base URL.
func Open(raw string) error {
	return open(raw, hasDisplay(), launchBrowser)
}

// open is Open's testable core: validate first, do nothing without a
// display, and let the launcher receive exactly the validated URL.
func open(raw string, display bool, launch func(string) error) error {
	target, err := localURL(raw)
	if err != nil {
		return err
	}
	if !display {
		return nil
	}
	return launch(target)
}

func hasDisplay() bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func launchBrowser(target string) error {
	page, cleanup, err := bootstrapPage(target)
	if err != nil {
		return err
	}
	name, args, err := browserCommand(runtime.GOOS, page)
	if err != nil {
		cleanup()
		return err
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		cleanup()
		return err
	}
	// The browser reads the redirect after the launcher returns. Keep the
	// 0600 file briefly, then remove the whole private directory.
	time.AfterFunc(2*time.Minute, cleanup)
	return cmd.Process.Release()
}

// bootstrapPage writes a one-shot HTML redirect and returns its file URL.
// The launcher receives only this path: the bearer token in the app URL is
// never an argv value, where another local user could read it from
// /proc/<pid>/cmdline or ps while the app runs.
func bootstrapPage(target string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "mailsense-launch-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	path := filepath.Join(dir, "open.html")
	page := "<!doctype html><meta charset=\"utf-8\"><title>" + html.EscapeString(WindowTitle) + "</title>" +
		"<meta http-equiv=\"refresh\" content=\"0;url=" + html.EscapeString(target) + "\">" +
		"<p><a href=\"" + html.EscapeString(target) + "\">Open " + html.EscapeString(WindowTitle) + "</a></p>\n"
	if err := os.WriteFile(path, []byte(page), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return "file://" + filepath.ToSlash(path), cleanup, nil
}

// browserCommand is the one browser-launch vocabulary outside Windows.
func browserCommand(goos, target string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{target}, nil
	case "linux", "freebsd", "openbsd", "netbsd":
		path, err := exec.LookPath("xdg-open")
		if err != nil {
			return "", nil, errors.New("desktop: xdg-open is not installed")
		}
		return path, []string{target}, nil
	default:
		return "", nil, errors.New("desktop: no browser opener for this platform")
	}
}
