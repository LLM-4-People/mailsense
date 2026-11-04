package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EFixturesAreShared: the boot status shape and the one-shot gate live in
// fixtures/data.ts and fixtures/test.ts; a spec that restates them is a second
// owner.
func TestE2EFixturesAreShared(t *testing.T) {
	specs, err := filepath.Glob("../../e2e/specs/*.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) == 0 {
		t.Fatal("no e2e specs found")
	}
	for _, file := range specs {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if strings.Contains(source, `{ version: "e2e", time_zone: "UTC", data_dir: "/tmp/e2e" }`) {
			t.Errorf("%s restates the status view; use fixtures/data.ts statusView()", filepath.Base(file))
		}
		if strings.Contains(source, "function deferred(") {
			t.Errorf("%s defines its own deferred gate; use fixtures/test.ts deferred()", filepath.Base(file))
		}
		if strings.Contains(source, "function mockCompanies(") || strings.Contains(source, "function companyMocks(") {
			t.Errorf("%s defines its own companies mock; use fixtures/companies.ts mockCompanies()", filepath.Base(file))
		}
	}
}
