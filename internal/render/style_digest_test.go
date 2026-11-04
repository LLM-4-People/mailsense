package render

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// TestCompiledIdsUseTheOneShortHash: the data-ms hook and the image-box class
// are short hashes from digest, not a second hand-rolled SHA-256.
func TestCompiledIdsUseTheOneShortHash(t *testing.T) {
	s := newStyleSheet()
	attr := s.inline("color: #123456; font-family: Arial")
	decls := sanitizeDeclarations("color: #123456; font-family: Arial")
	if decls == "" {
		t.Fatal("the sample declarations were not kept")
	}
	want := digest.Hex([]byte(decls), 16)
	if attr != ` data-ms="`+want+`"` {
		t.Fatalf("inline = %q, want the digest.Hex hook %q", attr, want)
	}
	if len(want) != 16 {
		t.Fatalf("hook = %q, want 16 hex digits", want)
	}

	box := s.box("10px", "20px")
	boxDecls := "display:inline-block;vertical-align:middle;max-width:100%;width:10px;height:20px;"
	if wantBox := "mail-image-box-" + digest.Hex([]byte(boxDecls), 12); box != wantBox {
		t.Fatalf("box = %q, want %q", box, wantBox)
	}
	if !strings.Contains(s.text(), `.`+box+`{`) || !strings.Contains(s.text(), `[data-ms="`+want+`"]{`) {
		t.Fatalf("the sheet does not carry both generated rules: %q", s.text())
	}
}

// TestRenderHasOneHashOwner: render imports neither crypto/sha256 nor
// encoding/hex; every short hash goes through digest.
func TestRenderHasOneHashOwner(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, banned := range []string{`"crypto/sha256"`, `"encoding/hex"`} {
			if strings.Contains(source, banned) {
				t.Errorf("%s imports %s; short hashes belong to digest", name, banned)
			}
		}
	}
}
