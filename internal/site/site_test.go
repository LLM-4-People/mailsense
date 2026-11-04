package site

import (
	"bytes"
	"encoding/xml"
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
	first, second := value.Generated(), value.Generated()
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
		landing := string(value.Landing(lang))
		for _, want := range []string{
			value.Features.Title.For(lang),
			value.Difference.Title.For(lang),
			value.Runs.Title.For(lang),
			value.Start.Title.For(lang),
			value.Status.Title.For(lang),
		} {
			if !strings.Contains(landing, want) {
				t.Errorf("landing %s is missing %q", lang, want)
			}
		}
		if strings.Contains(landing, "{install}") || strings.Contains(landing, "{privacy}") {
			t.Errorf("landing %s keeps an unresolved placeholder", lang)
		}
		readme := string(value.Readme(lang))
		for _, want := range []string{
			value.Build.Title.For(lang),
			value.License.Title.For(lang),
			"AGPL-3.0-or-later",
		} {
			if !strings.Contains(readme, want) {
				t.Errorf("readme %s is missing %q", lang, want)
			}
		}
	}
}

func TestAssetsAreValidSVG(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	generated := value.Generated()
	for _, name := range []string{"docs/assets/mark.svg", "docs/assets/logo.svg", "docs/assets/banner-en.svg", "docs/assets/banner-fr.svg"} {
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
}

// TestWriteAndCheckAgree proves the check mode sees exactly what the write
// mode would produce, in a temporary root.
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
