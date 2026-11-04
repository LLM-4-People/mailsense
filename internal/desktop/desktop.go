// Package desktop opens Mailsense's local interface: a WebView2 window on
// Windows (the shipped desktop form) and the default browser elsewhere, when
// a display exists. It never contains API logic and never sees a token
// outside the URL it was handed.
package desktop

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Window geometry and title, shared by the Windows window and its tests.
const (
	WindowTitle  = "Mailsense"
	WindowWidth  = 1280
	WindowHeight = 840
)

// localURL validates the one URL this package may open: an http URL on
// 127.0.0.1 with an explicit port, no credentials and no query. The token is
// a fragment and is preserved exactly as given. Refusing everything else
// keeps a bad caller from turning the desktop shell into a generic URL
// opener.
func localURL(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("desktop: empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("desktop: invalid URL")
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("desktop: only a plain loopback HTTP URL may be opened")
	}
	if u.Hostname() != "127.0.0.1" || u.Port() == "" {
		return "", fmt.Errorf("desktop: the URL must name 127.0.0.1 and an explicit port")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("desktop: the URL port is not valid")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("desktop: the URL must point at the app root")
	}
	if !strings.HasPrefix(u.Fragment, "token=") || len(u.Fragment) == len("token=") {
		return "", fmt.Errorf("desktop: the URL has no launch token fragment")
	}
	return raw, nil
}
