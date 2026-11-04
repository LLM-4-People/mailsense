package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mail, Search, Important and Triage render the same avatar/sender/subject/
// snippet/badges skeleton. The structure has one owner; a hand-built avatar
// row in a screen is the drift this pins.
func TestMailRowsUseTheSharedSkeleton(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("js", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	for _, name := range []string{"32-mail.js", "33-search.js", "20-hub.js"} {
		if strings.Contains(read(name), `class: "mail-avatar"`) {
			t.Fatalf("js/%s hand-builds a mail row outside the shared skeleton", name)
		}
	}
	components := read("01-components.js")
	for _, required := range []string{
		"function mailRow(opts)",
		`class: "mail-row-main"`,
		`class: "mail-row-top"`,
		`class: "mail-row-time"`,
		`class: "mail-row-snippet"`,
		`class: "mail-row-why"`,
		`class: "unread-dot"`,
	} {
		if !strings.Contains(components, required) {
			t.Fatalf("mailRow lacks %q", required)
		}
	}
	for name, want := range map[string]int{"32-mail.js": 2, "33-search.js": 1, "20-hub.js": 1} {
		if got := strings.Count(read(name), "mailRow({"); got < want {
			t.Fatalf("js/%s calls mailRow %d times, want at least %d", name, got, want)
		}
	}
}
