package site

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPageTableCoversEveryNavKey is the table's contract: one row per nav key,
// both language files, one renderer per generated path, and one position per
// public page in the release list.
func TestPageTableCoversEveryNavKey(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	pages := value.Pages()
	byKey := map[string]Page{}
	owners := map[string]string{}
	for _, page := range pages {
		if page.Key == "" {
			t.Fatal("page table has an empty key")
		}
		if _, dup := byKey[page.Key]; dup {
			t.Fatalf("page key %q appears twice", page.Key)
		}
		byKey[page.Key] = page
		if page.EN == "" || page.FR == "" {
			t.Errorf("page %q has no English or French file", page.Key)
		}
		for _, file := range []string{page.EN, page.FR} {
			if owner, dup := owners[file]; dup && owner != page.Key {
				t.Errorf("pages %q and %q share %s", owner, page.Key, file)
			}
			owners[file] = page.Key
		}
	}
	for _, item := range value.Nav {
		page, ok := byKey[item.Key]
		if !ok {
			t.Errorf("nav key %q has no page", item.Key)
			continue
		}
		if !page.Nav {
			t.Errorf("nav key %q is not marked as navigation", item.Key)
		}
		if page.Label != item.Label {
			t.Errorf("nav key %q label = %+v, want %+v", item.Key, page.Label, item.Label)
		}
		if !strings.HasSuffix(page.EN, "/"+pageFile(item.Key)) || !strings.HasSuffix(page.FR, "/"+pageFile(item.Key)) {
			t.Errorf("nav key %q does not follow the key to file convention: %s, %s", item.Key, page.EN, page.FR)
		}
	}

	public := value.PublicPages()
	if len(public) == 0 {
		t.Fatal("PublicPages is empty")
	}
	if public[0] != "index.md" {
		t.Errorf("first public page = %q, want index.md", public[0])
	}
	counts := map[string]int{}
	for _, name := range public {
		counts[name]++
	}
	for name, count := range counts {
		if count != 1 {
			t.Errorf("public page %q appears %d times, want exactly once", name, count)
		}
	}
	if len(public) != len(value.Nav)+1 {
		t.Errorf("public pages = %d, want the landing page plus %d nav pages", len(public), len(value.Nav))
	}

	renderers := map[string]int{}
	for _, page := range pages {
		if page.Render == nil {
			continue
		}
		seen := map[string]bool{}
		for _, file := range []string{page.EN, page.FR} {
			if file == "" || seen[file] {
				continue
			}
			seen[file] = true
			renderers[file]++
		}
	}
	generated := GeneratedPaths()
	if len(generated) != len(renderers) {
		t.Errorf("generated paths = %d, rendered files = %d", len(generated), len(renderers))
	}
	for _, name := range generated {
		if renderers[name] != 1 {
			t.Errorf("generated path %s has %d renderers, want exactly one", name, renderers[name])
		}
	}
	if err := value.validateFiles(root(t)); err != nil {
		t.Errorf("authored pages are incomplete: %v", err)
	}
}

// TestAddingNavKeyWithoutFileFailsCheck proves the failure path: a nav entry is
// not enough. The English file is demanded first, then the French file, and the
// check is clean only when both exist.
func TestAddingNavKeyWithoutFileFailsCheck(t *testing.T) {
	sourceRoot := root(t)
	dir := t.TempDir()
	source, err := os.ReadFile(filepath.Join(sourceRoot, SourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SourcePath), source, 0o644); err != nil {
		t.Fatal(err)
	}
	seedAuthored(t, sourceRoot, dir)

	if _, err := Check(dir); err != nil {
		t.Fatalf("baseline check failed: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(source, &doc); err != nil {
		t.Fatal(err)
	}
	nav, ok := doc["nav"].([]any)
	if !ok {
		t.Fatal("site.json has no nav array")
	}
	doc["nav"] = append(nav, map[string]any{
		"key":   "release_notes",
		"label": map[string]any{"en": "Release notes", "fr": "Notes de version"},
	})
	edited, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SourcePath), append(edited, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Check(dir)
	if err == nil || !strings.Contains(err.Error(), "docs/release-notes.md") {
		t.Fatalf("check with a missing English page = %v, want a missing docs/release-notes.md error", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "docs", "release-notes.md"), []byte("# Release notes\n\n<!-- nav:start -->\n<!-- nav:end -->\n\n<!-- footer:start -->\n<!-- footer:end -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Check(dir)
	if err == nil || !strings.Contains(err.Error(), "docs/fr/release-notes.md") {
		t.Fatalf("check with a missing French page = %v, want a missing docs/fr/release-notes.md error", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "docs", "fr", "release-notes.md"), []byte("# Notes de version\n\n<!-- nav:start -->\n<!-- nav:end -->\n\n<!-- footer:start -->\n<!-- footer:end -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(dir); err != nil {
		t.Fatalf("check with both pages failed: %v", err)
	}
}

func TestDuplicateNavKeyFailsValidate(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	value.Nav = append(value.Nav, value.Nav[0])
	err = value.Validate()
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("Validate with a duplicate nav key = %v, want a duplicate page error", err)
	}
}

func TestPageWithoutRendererFails(t *testing.T) {
	problems := validatePages([]Page{{
		Key: "orphan",
		EN:  "docs/orphan.md",
		FR:  "docs/fr/orphan.md",
	}})
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), "no renderer") {
		t.Fatalf("validatePages = %v, want a missing renderer problem", problems)
	}
}

func TestSharedGeneratedPathFails(t *testing.T) {
	render := func(Site, string) []byte { return []byte("x") }
	problems := validatePages([]Page{
		{Key: "first", EN: "docs/first.md", FR: "docs/fr/first.md", Render: render},
		{Key: "second", EN: "docs/first.md", FR: "docs/fr/second.md", Render: render},
	})
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), "share the file") {
		t.Fatalf("validatePages = %v, want a shared file problem", problems)
	}
}
