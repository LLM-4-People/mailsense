package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiagnosticDetailsHasOneOwner: the English diagnostic disclosure is built
// once. It used to be hand-built in seven modules with different paragraph
// classes and one missing language attribute, so the same technical block read
// differently depending on which screen showed it.
func TestDiagnosticDetailsHasOneOwner(t *testing.T) {
	files, err := filepath.Glob("js/*.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no UI modules found")
	}
	total := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		count := strings.Count(source, "blocker-diagnostic")
		total += count
		if count > 0 && filepath.Base(file) != "01-components.js" {
			t.Errorf("%s builds its own diagnostic disclosure; use diagnosticDetails", file)
		}
	}
	if total != 1 {
		t.Errorf("the diagnostic disclosure markup is built %d times, want once in diagnosticDetails", total)
	}
	components, err := os.ReadFile("js/01-components.js")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(components), "function diagnosticDetails(text, options = {})"); got != 1 {
		t.Errorf("01-components.js defines diagnosticDetails %d times, want once", got)
	}
	for _, c := range []struct{ file, call string }{
		{"js/11-http.js", "diagnosticDetails(english)"},
		{"js/11-http.js", `diagnosticDetails(diagnostic, { paragraphClass: "fine" })`},
		{"js/22-ask.js", `diagnosticDetails(failure.diagnostic, { paragraphClass: "fine" })`},
		{"js/25-sources.js", `summary: "sources.backfill.details"`},
		{"js/26-labels.js", `diagnosticDetails(selectedBlocker.blocker.detail, { paragraphClass: "fine" })`},
		{"js/33-search.js", `diagnosticDetails(result.notice, { summary: "search.notice.diagnostic" })`},
		{"js/52-init.js", `diagnosticDetails(failure.diagnostic, { paragraphClass: "fine mono" })`},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), c.call) {
			t.Errorf("%s does not use the one diagnostic: %s", c.file, c.call)
		}
	}
}
