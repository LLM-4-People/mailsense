package site

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// catalogFacts is the truth test's own reading of the pinned catalog. It does
// not call deriveFacts, so a bug in the production derivation cannot make the
// test agree with itself.
type catalogFacts struct {
	Runtimes map[string]struct {
		Platforms map[string]struct {
			Variants map[string]struct {
				Artifacts []struct {
					Size int64 `json:"size"`
				} `json:"artifacts"`
			} `json:"variants"`
		} `json:"platforms"`
	} `json:"runtimes"`
	Models map[string]struct {
		Size int64 `json:"size"`
	} `json:"models"`
	LocalEmbedder struct {
		Runtime string `json:"runtime"`
		Model   string `json:"model"`
	} `json:"local_embedder"`
}

func catalogMB(t *testing.T, root string) (windows, linux int) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CatalogPath)))
	if err != nil {
		t.Fatal(err)
	}
	var catalog catalogFacts
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	runtime, ok := catalog.Runtimes[catalog.LocalEmbedder.Runtime]
	if !ok {
		t.Fatalf("catalog has no runtime %q", catalog.LocalEmbedder.Runtime)
	}
	model, ok := catalog.Models[catalog.LocalEmbedder.Model]
	if !ok {
		t.Fatalf("catalog has no model %q", catalog.LocalEmbedder.Model)
	}
	total := func(platform string) int64 {
		entry, ok := runtime.Platforms[platform]
		if !ok {
			t.Fatalf("catalog has no platform %q", platform)
		}
		names := make([]string, 0, len(entry.Variants))
		for name := range entry.Variants {
			names = append(names, name)
		}
		if len(names) != 1 {
			t.Fatalf("platform %q has %d variants, want one", platform, len(names))
		}
		sort.Strings(names)
		var bytes int64
		for _, artifact := range entry.Variants[names[0]].Artifacts {
			bytes += artifact.Size
		}
		return bytes + model.Size
	}
	return int((total("windows/amd64") + 500_000) / 1_000_000),
		int((total("linux/amd64") + 500_000) / 1_000_000)
}

// missingFacts returns the phrases a document does not state. It is small and
// separate so its failure path can be tested directly.
func missingFacts(text string, wants []string) []string {
	folded := oneLine(text)
	var missing []string
	for _, want := range wants {
		if !strings.Contains(folded, want) {
			missing = append(missing, want)
		}
	}
	return missing
}

// TestPublicFactsMatchTheirOwners is the one truth check for facts stated in
// more than one place. The runtime size comes from the pinned catalog, the
// license from LICENSE, and the platform claim from the build tags that
// implement it.
func TestPublicFactsMatchTheirOwners(t *testing.T) {
	root := root(t)
	value, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	win, linux := catalogMB(t, root)
	if value.Facts.RuntimeWindowsMB != win || value.Facts.RuntimeLinuxMB != linux {
		t.Fatalf("rendered facts = %d/%d MB, catalog says %d/%d MB",
			value.Facts.RuntimeWindowsMB, value.Facts.RuntimeLinuxMB, win, linux)
	}

	for _, check := range []struct {
		file  string
		wants []string
	}{
		{"docs/install.md", []string{fmt.Sprintf("%d MB", win), fmt.Sprintf("%d MB", linux)}},
		{"docs/first-run.md", []string{fmt.Sprintf("%d MB", win), fmt.Sprintf("%d MB", linux)}},
		{"docs/fr/install.md", []string{fmt.Sprintf("%d Mo", win), fmt.Sprintf("%d Mo", linux)}},
		{"docs/fr/first-run.md", []string{fmt.Sprintf("%d Mo", win), fmt.Sprintf("%d Mo", linux)}},
	} {
		if missing := missingFacts(readDoc(t, root, check.file), check.wants); len(missing) > 0 {
			t.Errorf("%s does not state %v", check.file, missing)
		}
	}

	license := readDoc(t, root, "LICENSE")
	if !strings.Contains(license, "GNU AFFERO GENERAL PUBLIC LICENSE") || !strings.Contains(license, "Version 3") {
		t.Fatal("LICENSE is not the GNU Affero General Public License, version 3")
	}
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		readme := string(value.Readme(lang))
		if !strings.Contains(landing, LicenseID) || !strings.Contains(readme, LicenseID) {
			t.Errorf("%s surface does not state %s", lang, LicenseID)
		}
	}

	// Jekyll rewrites Markdown links, not raw HTML anchors. A generated landing
	// that names a .md target in HTML ships a broken link.
	rawMarkdownHref := regexp.MustCompile(`href="[^"]+\.md"`)
	for _, lang := range []string{"en", "fr"} {
		landing := string(value.Landing(lang))
		if match := rawMarkdownHref.FindString(landing); match != "" {
			t.Errorf("%s landing has a raw HTML anchor to a Markdown file: %s", lang, match)
		}
	}

	hostWindows := readDoc(t, root, "internal/source/outlook/host_windows.go")
	hostOther := readDoc(t, root, "internal/source/outlook/host_other.go")
	if !strings.Contains(hostWindows, "//go:build windows") {
		t.Error("the Outlook host is not Windows-only")
	}
	if !strings.Contains(hostOther, "//go:build !windows") || !strings.Contains(hostOther, "ErrUnsupported") {
		t.Error("the non-Windows Outlook host is not an explicit unsupported source")
	}
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		for _, want := range []string{"Outlook", "Linux"} {
			if !strings.Contains(landing, want) {
				t.Errorf("%s landing does not state the %s platform limit", lang, want)
			}
		}
	}

	// The public surface bans em dashes and spaced double-hyphen punctuation.
	for _, name := range append(GeneratedPaths(), value.ManagedPaths()...) {
		body := readDoc(t, root, name)
		for _, bad := range []string{"\u2014", "\u2013", " -- "} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains banned punctuation %q", name, bad)
			}
		}
	}

	css := readDoc(t, root, "docs/assets/main.scss")
	for _, bad := range []string{"@import", "fonts.googleapis", "http://"} {
		if strings.Contains(css, bad) {
			t.Errorf("the stylesheet makes an external request or import: %q", bad)
		}
	}
}

// TestTruthCheckBites proves the fact check rejects a stale number instead of
// only accepting the current one.
func TestTruthCheckBites(t *testing.T) {
	stale := "the local runtime adds about 999 MB on Windows"
	if missing := missingFacts(stale, []string{"342 MB"}); len(missing) != 1 {
		t.Fatalf("missingFacts on a stale size = %v, want one miss", missing)
	}
	if missing := missingFacts(stale, []string{"999 MB"}); len(missing) != 0 {
		t.Fatalf("missingFacts on a matching size = %v, want none", missing)
	}
}
