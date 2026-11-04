package site

import (
	"bytes"
	"encoding/xml"
	"html"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func root(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// TestGeneratedFilesAreCurrent is the drift guard: a hand edit to a generated
// page or asset fails here, and the message names the command that repairs it.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	stale, err := Check(root(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 {
		t.Fatalf("stale generated files: %v\nrun: go run ./cmd/gensite -write", stale)
	}
}

func TestSourceIsValid(t *testing.T) {
	if _, err := Load(root(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	first, err := value.Generated()
	if err != nil {
		t.Fatal(err)
	}
	second, err := value.Generated()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range GeneratedPaths() {
		if !bytes.Equal(first[name], second[name]) {
			t.Errorf("%s is not deterministic", name)
		}
	}
}

func TestBothLanguagesCarryEverySection(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "fr"} {
		landing := html.UnescapeString(string(value.Landing(lang)))
		for _, want := range []string{
			value.Differences.Title.For(lang),
			value.Boundary.Title.For(lang),
			value.Limits.Title.For(lang),
			value.Install.Title.For(lang),
			value.Status.Title.For(lang),
		} {
			if !strings.Contains(landing, want) {
				t.Errorf("landing %s is missing %q", lang, want)
			}
		}
		for _, want := range []string{"{install}", "{privacy}", "{repository}", "{license}"} {
			if strings.Contains(landing, want) {
				t.Errorf("landing %s keeps the placeholder %s", lang, want)
			}
		}
		readme := string(value.Readme(lang))
		for _, want := range []string{
			value.Build.Title.For(lang),
			value.License.Title.For(lang),
			LicenseID,
			value.Install.Command,
			value.DocsSite,
		} {
			if !strings.Contains(readme, want) {
				t.Errorf("readme %s is missing %q", lang, want)
			}
		}
		if !strings.Contains(readme, value.readmeLinks(lang)["first_run"]) {
			t.Errorf("readme %s does not link to the first-run guide", lang)
		}
	}
}

func TestAssetsAreValidSVG(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := value.Generated()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range GeneratedPaths() {
		if !strings.HasSuffix(name, ".svg") {
			continue
		}
		checked++
		body, ok := generated[name]
		if !ok {
			t.Errorf("%s is not generated", name)
			continue
		}
		decoder := xml.NewDecoder(bytes.NewReader(body))
		var sawSVG, sawTitle, sawDesc bool
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s is not valid XML: %v", name, err)
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			switch start.Name.Local {
			case "svg":
				sawSVG = true
			case "title":
				sawTitle = true
			case "desc":
				sawDesc = true
			}
		}
		if !sawSVG || !sawTitle || !sawDesc {
			t.Errorf("%s lacks an svg root, title, or desc", name)
		}
	}
	if checked == 0 {
		t.Fatal("no generated SVG assets were checked")
	}
}

// TestWriteAndCheckAgree proves the check mode sees exactly what the write
// mode would produce, in a temporary root.
// seedAuthored copies the table's authored pages and the runtime catalog into
// a temporary root so the write and check modes can be exercised without a
// full checkout.
func seedAuthored(t *testing.T, sourceRoot, dir string) {
	t.Helper()
	value, err := Load(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "fr"} {
		for _, name := range value.PublicPagePaths(lang) {
			body, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	catalog, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(CatalogPath)))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, filepath.FromSlash(CatalogPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, catalog, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAndCheckAgree(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(root(t), SourcePath))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SourcePath), source, 0o644); err != nil {
		t.Fatal(err)
	}
	seedAuthored(t, root(t), dir)
	if _, err := Write(dir); err != nil {
		t.Fatal(err)
	}
	stale, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("write then check reports stale files: %v", stale)
	}
	// A hand edit must be visible to the check.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err = Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != "README.md" {
		t.Fatalf("check after hand edit = %v, want [README.md]", stale)
	}
}
