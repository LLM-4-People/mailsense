package ngram

import (
	"slices"
	"testing"
)

func TestNgrams(t *testing.T) {
	if got := Normalize("Ｃafé, 12 ÉTÉ!"); got != " café été " {
		t.Fatalf("%q", got)
	}
	if Normalize("") != " " || Normalize("123 !?") != " " {
		t.Error("text without letters is one space")
	}
	c := Count(Normalize("ab ab"), 3)
	if c["a"] != 2 || c["ab"] != 2 || c[" ab"] != 2 || c["ab "] != 2 || c["b a"] != 0 || c[" "] != 0 {
		t.Fatalf("%v", c)
	}
	if top := Top(map[string]int{"b": 2, "a": 2, "c": 5}, 2); !slices.Equal(top, []string{"c", "a"}) {
		t.Fatalf("ties in code-point order: %v", top)
	}
	if len(Top(nil, 3)) != 0 || len(Count(" ", 3)) != 0 {
		t.Error("nothing to count")
	}
}
