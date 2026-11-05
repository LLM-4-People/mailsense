//go:build !windows

package desktop

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	cmd.Env = launcherEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		cleanup()
		return err
	}
	// The browser reads the redirect after the launcher returns. Keep the
	// 0600 file briefly, then remove the whole private directory.
	time.AfterFunc(bootstrapPageTTL, cleanup)
	return cmd.Process.Release()
}

// launcherEnv keeps only the variables a GUI browser launcher needs: PATH
// to find the browser and its helpers, the session handles, the temp
// directory and the locale. The app's MAILSENSE_* variables — secrets and
// credential-bearing overrides — never reach the browser process tree.
// The allow-list style (case-insensitive names) mirrors the sidecar's
// childEnv in internal/sidecar/process.go.
func launcherEnv(base []string) []string {
	var out []string
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(name)
		if strings.HasPrefix(up, "MAILSENSE_") || !launcherEnvAllowed(up) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// launcherEnvAllowed reports whether one upper-cased variable name is kept:
// an exact name a launcher reads, or one of the XDG_*/LC_* families.
func launcherEnvAllowed(up string) bool {
	for _, name := range launcherEnvNames {
		if up == name {
			return true
		}
	}
	for _, prefix := range launcherEnvPrefixes {
		if strings.HasPrefix(up, prefix) {
			return true
		}
	}
	return false
}

var launcherEnvNames = []string{"PATH", "HOME", "DISPLAY", "WAYLAND_DISPLAY", "TMPDIR", "TERM", "LANG"}
var launcherEnvPrefixes = []string{"XDG_", "LC_"}

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
