package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dottedSettingLiteral matches a settings key spelled inline at a cmd read.
var dottedSettingLiteral = regexp.MustCompile(`cfg\.(?:Int|Duration|Bool|String|Float)\("[a-z0-9_]+\.[a-z0-9_.]+"\)`)

// TestNoDottedSettingLiterals: cmd reads a setting through the package constant
// that owns its key, never through a literal that can drift from the schema.
func TestNoDottedSettingLiterals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if match := dottedSettingLiteral.Find(data); match != nil {
			t.Errorf("%s reads the setting %s inline; use the owning package's constant", file, match)
		}
	}
}
