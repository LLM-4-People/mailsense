package render

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestDangerousCSSIsOneSet: every fragment the presentation compiler refuses is
// declared once, and every validator refuses it. A token added to one list only
// used to pass the other two validators.
// wantDangerousCSS is the presentation compiler's refusal vocabulary as a
// contract. It is deliberately independent of the production lists: dropping a
// token from production must fail here, not silently shrink the test.
var wantDangerousCSS = []string{"@", "url(", "expression", "javascript:", "\\", "{", "}", "<", ">"}

var wantDangerousValueCSS = []string{"behavior:", "-moz-binding", "var(", "attr(", "env(", "calc("}

func TestDangerousCSSIsOneSet(t *testing.T) {
	if !slices.Equal(dangerousCSS, wantDangerousCSS) {
		t.Fatalf("dangerousCSS = %q, want %q", dangerousCSS, wantDangerousCSS)
	}
	if !slices.Equal(dangerousValueCSS, wantDangerousValueCSS) {
		t.Fatalf("dangerousValueCSS = %q, want %q", dangerousValueCSS, wantDangerousValueCSS)
	}
	for _, token := range wantDangerousCSS {
		if safeSelector("body" + token) {
			t.Errorf("safeSelector accepted a selector containing %q", token)
		}
		if safeMediaQuery("(min-width: 600px)" + token) {
			t.Errorf("safeMediaQuery accepted a query containing %q", token)
		}
		if safeValue("color", "#ff0000"+token) {
			t.Errorf("safeValue accepted a value containing %q", token)
		}
	}
	for _, token := range wantDangerousValueCSS {
		if safeValue("font-family", "Arial"+token) {
			t.Errorf("safeValue accepted a value containing %q", token)
		}
	}

	// The vocabulary the filters exist to admit still passes.
	for _, selector := range []string{"body", "table td a:hover", ".x[data-a='b']"} {
		if !safeSelector(selector) {
			t.Errorf("safeSelector refused the valid selector %q", selector)
		}
	}
	for _, query := range []string{"(min-width: 600px)", "screen and (max-width: 40em)"} {
		if !safeMediaQuery(query) {
			t.Errorf("safeMediaQuery refused the valid query %q", query)
		}
	}
	for _, value := range []string{"#ff0000", "rgb(1, 2, 3)", "Arial, sans-serif", "1px solid #ccc", "100%"} {
		if !safeValue("color", value) && !safeValue("font-family", value) && !safeValue("border", value) && !safeValue("width", value) {
			t.Errorf("no property accepted the valid value %q", value)
		}
	}
}

// TestDangerousCSSIsSpelledOnce: no validator keeps its own copy of the token
// lists, and every fragment appears as a literal exactly once.
func TestDangerousCSSIsSpelledOnce(t *testing.T) {
	data, err := os.ReadFile("style.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	// The two declarations are the only place a fragment is spelled, and the
	// three validators read them instead of keeping a local copy.
	for _, use := range []struct {
		what string
		want int
	}{
		{"var dangerousCSS = []string{", 1},
		{"var dangerousValueCSS = []string{", 1},
		{"containsAny(strings.ToLower(selector), dangerousCSS)", 1},
		{"containsAny(strings.ToLower(query), dangerousCSS)", 1},
		{"containsAny(lower, dangerousCSS) || containsAny(lower, dangerousValueCSS)", 1},
	} {
		if got := strings.Count(source, use.what); got != use.want {
			t.Errorf("style.go mentions %q %d time(s), want %d", use.what, got, use.want)
		}
	}
	// Every declared fragment is a literal of the declared list, not of a
	// validator's private copy: a copy would make the list above disappear.
	if strings.Count(source, "[]string{\"@\", \"url(\"") != 1 {
		t.Error("the shared token list is not spelled as one literal")
	}
}
