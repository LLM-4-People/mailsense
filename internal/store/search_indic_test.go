package store

import (
	"fmt"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Round 30: unicode61 treats a dependent vowel sign (matra) as a separator,
// so ki, kii, ku and kuu word-index to the same base consonant and a word
// search returns all four. Routing the matra-based Indic scripts to the
// substring index keeps the sign: the two-rune terms are scanned and only
// the exact form matches.
func TestSearchFindsIndicMatras(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	var batch []mail.Message
	want := map[string]string{} // term → message id
	for _, sample := range mailtest.IndicMatras() {
		for i, term := range sample.Terms {
			id := fmt.Sprintf("%s-%d", sample.Script, i)
			m := mailtest.Message("src", id)
			m.Subject = "notes"
			m.Body = term
			batch = append(batch, m)
			want[term] = id
		}
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	if len(want) != 40 {
		t.Fatalf("fixture terms are not distinct: %d", len(want))
	}
	for _, sample := range mailtest.IndicMatras() {
		for _, term := range sample.Terms {
			res, err := s.Search(ctx, term, 10)
			if err != nil {
				t.Fatalf("%q: %v", term, err)
			}
			var got []string
			for _, h := range res.Hits {
				got = append(got, h.ID)
			}
			slices.Sort(got)
			if res.Total != 1 || fmt.Sprint(got) != fmt.Sprint([]string{want[term]}) {
				t.Errorf("%s %q: hits %v (total %d), want only %q — the matra must not be dropped",
					sample.Script, term, got, res.Total, want[term])
			}
			if len(res.Terms) != 1 || res.Terms[0].Mode != ModeScan {
				t.Errorf("%s %q: terms %+v, want one scan (two runes)", sample.Script, term, res.Terms)
			}
		}
	}
}

// A quoted phrase in a matra-based Indic script stays one phrase on the
// substring index: a two-rune phrase is one scan term (exact matra), a
// three-rune phrase is a substring term. कमल remains a documented substring
// residual: it is a prefix of कमला, so both messages match.
func TestSearchPhraseKeepsIndicSubstringMode(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	docs := []struct{ id, subject string }{
		{"i", "कि"}, {"ii", "की"}, {"u", "कु"}, {"uu", "कू"},
		{"kamala", "कमला"}, {"kamal", "कमल"},
	}
	var batch []mail.Message
	for _, d := range docs {
		m := mailtest.Message("src", d.id)
		m.Subject, m.Body = d.subject, "x"
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  []string
		mode  TermMode
	}{
		{`"कि"`, []string{"i"}, ModeScan},
		{`"कमला"`, []string{"kamala"}, ModeSubstring},
		{`"कमल"`, []string{"kamal", "kamala"}, ModeSubstring},
	} {
		res, err := s.Search(ctx, tc.query, 10)
		if err != nil {
			t.Fatalf("%q: %v", tc.query, err)
		}
		var got []string
		for _, h := range res.Hits {
			got = append(got, h.ID)
		}
		slices.Sort(got)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) || res.Total != len(tc.want) {
			t.Errorf("%q: hits %v (total %d, terms %+v), want %v", tc.query, got, res.Total, res.Terms, tc.want)
		}
		if len(res.Terms) != 1 || res.Terms[0].Mode != tc.mode {
			t.Errorf("%q: terms %+v, want one %s", tc.query, res.Terms, tc.mode)
		}
	}
}
