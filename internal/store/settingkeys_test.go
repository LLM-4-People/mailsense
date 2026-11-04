package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSettingKeysHaveOneOwner: every bound the store enforces and the search
// path validates is named once here. A reader that types the key is how a
// ceiling is raised in one place and left at its old value in another.
func TestSettingKeysHaveOneOwner(t *testing.T) {
	keys := []string{
		SettingMaxOpenConns, SettingQueryMaxOpenConns, SettingQueryMaxRows, SettingQueryTimeout,
		SettingSearchMaxHits, SettingSearchCountCap, SettingSearchLimit, SettingSearchQueryMaxChars,
		SettingSearchEmbedTimeout, SettingSearchRRFK, SettingSearchSnippetChars,
	}
	dirs := []string{".", filepath.Join("..", "search")}
	for _, key := range keys {
		total := 0
		for _, dir := range dirs {
			files, err := filepath.Glob(filepath.Join(dir, "*.go"))
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
				total += strings.Count(string(data), `"`+key+`"`)
			}
		}
		if total != 1 {
			t.Errorf("setting %q is spelled %d times, want once in store/settings.go", key, total)
		}
	}
	for _, c := range []struct{ file, want string }{
		{"store.go", "cfg.Int(SettingMaxOpenConns)"},
		{"readonly.go", "cfg.Int(SettingQueryMaxOpenConns)"},
		{"readonly.go", "cfg.Int(SettingQueryMaxRows)"},
		{"readonly.go", "cfg.Duration(SettingQueryTimeout)"},
		{"search.go", "snap.Int(SettingSearchMaxHits)"},
		{"search.go", "snap.Int(SettingSearchCountCap)"},
		{filepath.Join("..", "search", "search.go"), "store.SettingSearchLimit"},
		{filepath.Join("..", "search", "search.go"), "store.SettingSearchQueryMaxChars"},
		{filepath.Join("..", "search", "search.go"), "store.SettingSearchEmbedTimeout"},
		{filepath.Join("..", "search", "search.go"), "store.SettingSearchRRFK"},
		{filepath.Join("..", "search", "search.go"), "store.SettingSearchSnippetChars"},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("%s does not read %s", c.file, c.want)
		}
	}
}
