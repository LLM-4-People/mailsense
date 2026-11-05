package desktop

import (
	"html"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBootstrapPageCarriesTheWindowTitle: the generated redirect page reads the
// one title constant instead of spelling the brand again.
func TestBootstrapPageCarriesTheWindowTitle(t *testing.T) {
	target := "http://127.0.0.1:8080/#token=abc"
	url, cleanup, err := bootstrapPage(target)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	path := strings.TrimPrefix(url, "file://")
	body, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, want := range []string{
		"<title>" + WindowTitle + "</title>",
		">Open " + WindowTitle + "<",
		"0;url=" + html.EscapeString(target),
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the bootstrap page lacks %q: %s", want, page)
		}
	}
}

// TestBootstrapPageIsPrivateContainedAndCleanedUp is the platform-neutral
// contract of the shared page (both launchers use it): a file URL whose
// page is the only entry of its own private directory, 0600 inside 0700 on
// POSIX, carrying the redirect but not the launcher URL's token, and
// removed by cleanup.
func TestBootstrapPageIsPrivateContainedAndCleanedUp(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	target := "http://127.0.0.1:54321/#token=" + token
	page, cleanup, err := bootstrapPage(target)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.HasPrefix(page, "file://") {
		t.Fatalf("launcher URL %q is not a private file", page)
	}
	if strings.Contains(page, token) {
		t.Fatalf("the launcher URL carries the token: %s", page)
	}
	path := filepath.FromSlash(strings.TrimPrefix(page, "file://"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" { // chmod on Windows only toggles the read-only bit
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("bootstrap page mode %v, want 0600", info.Mode().Perm())
		}
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("bootstrap directory mode %v, want 0700", dirInfo.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "open.html" {
		t.Fatalf("the bootstrap directory holds %d entries, want only open.html", len(entries))
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), target) {
		t.Fatalf("bootstrap page does not redirect to the app: %s", body)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the private directory %s: %v", dir, err)
	}
}
