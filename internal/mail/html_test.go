package mail

import (
	"strings"
	"testing"
)

func TestTruncateHTMLKeepsCompleteTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
		cut  bool
	}{
		{"fits", "<p>short</p>", 100, "<p>short</p>", false},
		{"drops an unfinished tag", `<p>one</p><div class="x`, 14, "<p>one</p>", true},
		{"cuts text between runes", "<p>éé😀</p>", 4, "<p>é", true},
		{"a bare ampersand is not an entity", "<p>a&amp;b</p>", 5, "<p>a&", true},
		{"does not split an entity", "<p>a&amp;b</p>", 8, "<p>a", true},
		{"zero", "<p>x</p>", 0, "", true},
		{"tags only over budget", "<p></p>", 2, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cut := TruncateHTML(tc.in, tc.n)
			if got != tc.want || cut != tc.cut {
				t.Fatalf("TruncateHTML(%q, %d) = %q, %v; want %q, %v", tc.in, tc.n, got, cut, tc.want, tc.cut)
			}
			if UTF16Len(got) > tc.n {
				t.Fatalf("result %q is %d units, over %d", got, UTF16Len(got), tc.n)
			}
		})
	}
}

func TestTruncateHTMLNeverExceedsTheBudget(t *testing.T) {
	in := strings.Repeat(`<p class="x">Ceci &amp; cela 😀</p>`, 1000)
	for n := 0; n < 600; n += 7 {
		got, cut := TruncateHTML(in, n)
		if UTF16Len(got) > n || (!cut && got != in) {
			t.Fatalf("n=%d: %d units, cut=%v", n, UTF16Len(got), cut)
		}
		if strings.Count(got, "<") != strings.Count(got, ">") {
			t.Fatalf("n=%d: split tag: %q", n, got)
		}
	}
}

func TestTruncateHTMLKeepsInvalidBytes(t *testing.T) {
	// Decoding happens in the connector, but the truncator must not panic on
	// bytes it never expects to see.
	got, cut := TruncateHTML("<p>\xff\xfe</p>", 4)
	if !cut || len(got) == 0 {
		t.Fatalf("got %q, cut %v", got, cut)
	}
}
