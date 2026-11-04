package locale

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSpaceSkipsOneWhiteSpaceCharacter: the one-character separator rule of
// patterns and amounts. A run of two spaces is a single token of two
// characters, so it is not the separator; anything else is left where it is.
func TestSpaceSkipsOneWhiteSpaceCharacter(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		at   int
		want int
	}{
		{"one space", "5 2", 1, 2},
		{"two spaces", "5  2", 1, 1},
		{"tab", "5\t2", 1, 2},
		{"no space", "52", 1, 1},
		{"past the end", "5", 4, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := space(tokens(tc.text), tc.at); got != tc.want {
				t.Fatalf("space(%q, %d) = %d, want %d", tc.text, tc.at, got, tc.want)
			}
		})
	}
}

// TestPatternSeparatorIsOneSpace: a compiled pattern's white space matches one
// separator character, not a run. The negative case is the one the moved
// helper exists for.
func TestPatternSeparatorIsOneSpace(t *testing.T) {
	c, err := compile("d M", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, fields, ok := c.match(tokens("5 2"), 0); !ok || fields[fieldDay] != 5 || fields[fieldMonth] != 2 {
		t.Fatalf("one space: %v %v", fields, ok)
	}
	if _, _, ok := c.match(tokens("5  2"), 0); ok {
		t.Fatal("a two-space run was accepted as the single separator")
	}
}

// TestTheSeparatorRuleHasOneWriter: the check lives in tokens.go's space;
// currency and pattern read it. A re-spelled inline check is exactly the
// duplication this round removed.
func TestTheSeparatorRuleHasOneWriter(t *testing.T) {
	rule := regexp.MustCompile(`tokSpace && mail\.Runes\(tks\[i\]\.text\) == 1`)
	writers := []string{}
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if rule.Match(data) {
			writers = append(writers, name)
		}
	}
	if len(writers) != 1 || writers[0] != "tokens.go" {
		t.Fatalf("the one-character space rule is written in %v; it belongs to tokens.go's space", writers)
	}
}
