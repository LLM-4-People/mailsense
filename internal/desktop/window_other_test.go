//go:build !windows

package desktop

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestOpenWithoutDisplayIsNotAnError(t *testing.T) {
	called := false
	err := open("http://127.0.0.1:54321/#token=abc", false, func(string) error {
		called = true
		return errors.New("must not launch")
	})
	if err != nil {
		t.Fatalf("headless open: %v", err)
	}
	if called {
		t.Fatal("a headless session launched a browser")
	}
}

func TestOpenPassesTheValidatedURL(t *testing.T) {
	const target = "http://127.0.0.1:54321/#token=abc"
	var got string
	if err := open(target, true, func(url string) error { got = url; return nil }); err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("launcher got %q, want %q", got, target)
	}
	if err := open("https://example.com/#token=x", true, func(string) error { t.Fatal("opened a remote URL"); return nil }); err == nil {
		t.Fatal("a remote URL was accepted")
	}
}

func TestOpenReportsLauncherFailure(t *testing.T) {
	want := errors.New("no opener")
	if err := open("http://127.0.0.1:54321/#token=abc", true, func(string) error { return want }); !errors.Is(err, want) {
		t.Fatalf("open error = %v, want %v", err, want)
	}
}

// The browser launcher must not carry the bearer token in its argv: a local
// user can read another process's command line. It receives a 0600 redirect
// file instead, and the token stays in that private file until the browser
// has read it.
func TestLauncherReceivesNoToken(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	const target = "http://127.0.0.1:54321/#token=" + token
	page, cleanup, err := bootstrapPage(target)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Contains(page, token) {
		t.Fatalf("the launcher URL carries the token: %s", page)
	}
	if !strings.HasPrefix(page, "file://") {
		t.Fatalf("launcher URL %q is not a private file", page)
	}
	path := strings.TrimPrefix(page, "file://")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap page mode %v, want 0600", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), target) {
		t.Fatalf("bootstrap page does not redirect to the app: %s", body)
	}
}
