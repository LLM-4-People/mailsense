package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// A malformed providers.json is a file problem, not a transient read failure:
// the panel must list the broken records and fields instead of offering only
// a retry that cannot repair the file.
func TestProviderLoadShowsTheFileProblems(t *testing.T) {
	src := readUIModule(t, "41-providers.js")
	for _, want := range []string{
		"function providerLoadProblems(error)",
		"error.problems",
		"providerLoadProblems(providers.error)",
		`h("ul", { class: "provider-load-problems" }`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("41-providers.js lacks %q", want)
		}
	}
	css, err := fs.ReadFile(Files, "css/70-settings.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".provider-load-problems") {
		t.Error("70-settings.css does not style the provider problem list")
	}
}
