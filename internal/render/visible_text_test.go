package render

import (
	"strings"
	"testing"
)

// TestVisibleTextIsTheOneTruncation: the image label trims and then uses the
// package's rune truncation, including the ellipsis and multibyte text.
func TestVisibleTextIsTheOneTruncation(t *testing.T) {
	for _, input := range []string{
		"",
		"   ",
		"  Ada Lovelace  ",
		"é" + strings.Repeat("è", 159),
		strings.Repeat("é", 160),
		strings.Repeat("é", 161),
		strings.Repeat("a", 500),
		"plain",
	} {
		if got, want := visibleText(input), truncateRunes(strings.TrimSpace(input), 160); got != want {
			t.Fatalf("visibleText(%q) = %q, want %q", input, got, want)
		}
	}
	if got := visibleText(strings.Repeat("é", 161)); !strings.HasSuffix(got, "…") || len([]rune(got)) != 161 {
		t.Fatalf("a long label is not truncated with one ellipsis: %q", got)
	}
}
