package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// TestHasControlCoversC0DelAndC1: the one predicate is unicode.IsControl, so
// U+0080..U+009F are control characters here and in every caller.
func TestHasControlCoversC0DelAndC1(t *testing.T) {
	for r := rune(0); r <= 0x9f; r++ {
		if got, want := HasControl("a"+string(r)+"b"), unicode.IsControl(r); got != want {
			t.Fatalf("HasControl(U+%04X) = %v, want %v", r, got, want)
		}
	}
	for _, r := range []rune{'a', 'é', '\u2028', '\U0001F600'} {
		if HasControl("a" + string(r) + "b") {
			t.Fatalf("HasControl(U+%04X) = true", r)
		}
	}
}

// TestNoAsciiOnlyControlPredicate: the address, calendar and query trust
// boundaries read mail.HasControl instead of keeping the ASCII-only
// `r < 0x20 || r == 0x7f` predicate that let U+0080..U+009F through.
func TestNoAsciiOnlyControlPredicate(t *testing.T) {
	for _, dir := range []string{"../mailquery", "../calendar", "../contacts"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files under %s", dir)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			if strings.Contains(source, "r < 0x20") {
				t.Errorf("%s keeps an ASCII-only control predicate; use mail.HasControl", file)
			}
		}
	}
}
