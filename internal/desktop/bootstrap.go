package desktop

import (
	"html"
	"os"
	"path/filepath"
	"time"
)

// bootstrapPageTTL is how long the redirect page is kept once the launcher
// has returned: long enough for a slow browser to read it.
const bootstrapPageTTL = 2 * time.Minute

// bootstrapPage writes a one-shot HTML redirect and returns its file URL.
// Every launcher receives only this path: the bearer token in the app URL
// is never an argv value, where another local user could read it from
// /proc/<pid>/cmdline or ps while the app runs. The caller owns cleanup:
// the page is removed when the launcher cannot start, or after
// bootstrapPageTTL once the launcher has read it.
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
