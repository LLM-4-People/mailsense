package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStringSetSQLHasOneOwner: the JSON-set membership subquery is written once
// and every text-set filter reads the shared name. A copy in a query is how one
// read starts binding a JSON list to a different shape.
func TestStringSetSQLHasOneOwner(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	total, owners := 0, 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		count := strings.Count(source, "(SELECT value FROM json_each(?))")
		total += count
		if count > 0 {
			owners++
			if file != "lists.go" {
				t.Errorf("%s writes the JSON-set subquery itself; use stringSetSQL", file)
			}
		}
		if strings.Contains(source, "stringSetSQL = \"") {
			t.Errorf("%s declares a second string-set literal", file)
		}
	}
	if total != 1 || owners != 1 {
		t.Errorf("the JSON-set subquery is written %d times in %d files, want once in lists.go", total, owners)
	}
	for _, file := range []string{"addressscope.go", "companybook.go", "companyprofile.go", "conversations.go",
		"people.go", "search.go", "threadstates.go", "triage.go", "world.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "stringSetSQL") {
			t.Errorf("%s does not use the shared string-set subquery", file)
		}
	}
}
