//go:build !windows

package desktop

import (
	"html"
	"os"
	"path/filepath"
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
