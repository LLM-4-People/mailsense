package store

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// seedPhraseMailbox writes three messages whose Latin words appear adjacent,
// reversed and apart, the distinction a quoted phrase must keep.
func seedPhraseMailbox(t *testing.T) *Store {
	t.Helper()
	s, _ := open(t, nil)
	docs := []struct{ id, subject, body string }{
		{"adjacent", "bonjour merci", "x"},
		{"reversed", "merci bonjour", "x"},
		{"apart", "hello", "bonjour tout le monde, merci"},
	}
	var batch []mail.Message
	for _, d := range docs {
		m := mailtest.Message("src", d.id)
		m.Subject, m.Body = d.subject, d.body
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(t.Context(), 0, batch); err != nil {
		t.Fatal(err)
	}
	return s
}

// A quoted span is one FTS phrase, not its words ANDed: adjacency and word
// order must both hold. Unquoted words keep the AND behaviour.
func TestSearchHonorsQuotedPhrases(t *testing.T) {
	s := seedPhraseMailbox(t)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{`"bonjour merci"`, []string{"adjacent"}},
		{`"merci bonjour"`, []string{"reversed"}},
		{"bonjour merci", []string{"adjacent", "apart", "reversed"}},
	} {
		res, err := s.Search(t.Context(), tc.query, 10)
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
	}
}

// Quotes, fullwidth quotes, NUL and default ignorables must never reach FTS
// syntax: the folded query is parsed once and sanitized, so an empty folded
// query is a typed empty search and a quote inside a word is neutralized.
func TestSearchFoldedQuotesAndNulAreNeutralized(t *testing.T) {
	s := seedPhraseMailbox(t)
	ctx := t.Context()
	for _, q := range []string{"＂", `"`, "\x00", "\u200b", `  "" `} {
		if _, err := s.Search(ctx, q, 5); !errors.Is(err, ErrEmptySearch) {
			t.Errorf("%q must be a typed empty search, got %v", q, err)
		}
	}
	for _, q := range []string{`"unbalanced`, "a＂b", "a\x00b", `"bonjour merci"`} {
		if _, err := s.Search(ctx, q, 5); err != nil {
			t.Errorf("%q must not error, got %v", q, err)
		}
	}
}

// Curly quotes are punctuation, not phrase delimiters: they stay inside the
// terms, so a bare pair finds nothing and “a b” is two ANDed words.
func TestSearchCurlyQuotesAreNotPhraseDelimiters(t *testing.T) {
	s := seedPhraseMailbox(t)
	ctx := t.Context()
	res, err := s.Search(ctx, "“”", 10)
	if err != nil || res.Total != 0 || len(res.Hits) != 0 {
		t.Fatalf("curly quotes alone: %+v %v", res, err)
	}
	res, err = s.Search(ctx, "“a b”", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Terms) != 2 || res.Terms[0].Mode != ModeWord || res.Terms[1].Mode != ModeWord {
		t.Fatalf("“a b” must be two word terms, not one phrase: %+v", res.Terms)
	}
	if res.Total != 0 {
		t.Fatalf("no message has both a and b: %+v", res)
	}
}

// Folding maps the fullwidth quotes to ASCII quote delimiters, so a
// fullwidth-quoted span is the same phrase as its ASCII spelling.
func TestSearchFullwidthQuotedPhrase(t *testing.T) {
	s := seedPhraseMailbox(t)
	ctx := t.Context()
	ascii, err := s.Search(ctx, `"bonjour merci"`, 10)
	if err != nil {
		t.Fatal(err)
	}
	full, err := s.Search(ctx, "＂bonjour merci＂", 10)
	if err != nil {
		t.Fatal(err)
	}
	if full.Total != 1 || ascii.Total != full.Total || len(full.Hits) != 1 || full.Hits[0].ID != "adjacent" {
		t.Fatalf("fullwidth phrase must equal the ASCII phrase: ascii %+v full %+v", ascii, full)
	}
	if len(full.Terms) != 1 || full.Terms[0].Term != "bonjour merci" || full.Terms[0].Mode != ModeWord {
		t.Fatalf("fullwidth phrase terms %+v", full.Terms)
	}
}

// A mixed MATCH+scan plan still ANDs the scan term; only an all-scan recall
// plan ORs its scans. The plan shape states the same distinction.
func TestSearchMixedMatchAndScanStillAnds(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	docs := []struct{ id, subject string }{
		{"both", "alpha 会议"},
		{"alpha", "alpha"},
		{"meeting", "会议"},
		{"report", "报告"},
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
	for _, anyTerm := range []bool{false, true} {
		res, err := s.searchText(ctx, "alpha 会议", SearchFilter{}, 10, anyTerm)
		if err != nil {
			t.Fatalf("anyTerm=%v: %v", anyTerm, err)
		}
		if res.Total != 1 || len(res.Hits) != 1 || res.Hits[0].ID != "both" {
			t.Fatalf("anyTerm=%v: mixed MATCH+scan must AND the scan: %+v", anyTerm, res)
		}
		if len(res.Terms) != 2 || res.Terms[0].Mode != ModeSubstring || res.Terms[1].Mode != ModeScan {
			t.Fatalf("anyTerm=%v: plan terms %+v", anyTerm, res.Terms)
		}
	}
	all, err := s.SearchTextAny(ctx, "会议 报告", SearchFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range all.Hits {
		got = append(got, h.ID)
	}
	slices.Sort(got)
	if all.Total != 3 || fmt.Sprint(got) != "[both meeting report]" {
		t.Fatalf("all-scan recall must OR: hits %v total %d terms %+v", got, all.Total, all.Terms)
	}
}

// The all-scan recall form ORs its terms: two short CJK words on two
// disjoint messages must both match, while the strict form still ANDs them.
func TestSearchAnyScanTermsAreOred(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	var batch []mail.Message
	for _, d := range []struct{ id, subject string }{{"meeting", "会议"}, {"report", "报告"}} {
		m := mailtest.Message("src", d.id)
		m.Subject, m.Body = d.subject, "x"
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	strict, err := s.SearchText(ctx, "会议 报告", SearchFilter{}, 10)
	if err != nil || strict.Total != 0 {
		t.Fatalf("strict scan terms must AND: %+v %v", strict, err)
	}
	any, err := s.SearchTextAny(ctx, "会议 报告", SearchFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range any.Hits {
		got = append(got, h.ID)
	}
	slices.Sort(got)
	if any.Total != 2 || fmt.Sprint(got) != "[meeting report]" {
		t.Fatalf("recall scan terms must OR: hits %v total %d terms %+v", got, any.Total, any.Terms)
	}
}

// A quoted phrase in a spaceless script stays a phrase on the substring
// index; a short quoted phrase is still one scan term, not split words.
func TestSearchPhraseKeepsCJKSubstringMode(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	docs := []struct{ id, subject, body string }{
		{"cjk-adjacent", "見積書送付", "x"},
		{"cjk-reversed", "送付見積書", "x"},
		{"cjk-apart", "連絡", "見積書の件について送付します"},
	}
	var batch []mail.Message
	for _, d := range docs {
		m := mailtest.Message("src", d.id)
		m.Subject, m.Body = d.subject, d.body
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
		{`"見積書送付"`, []string{"cjk-adjacent"}, ModeSubstring},
		{"見積書 送付", []string{"cjk-adjacent", "cjk-apart", "cjk-reversed"}, ""},
		{`"送付"`, []string{"cjk-adjacent", "cjk-apart", "cjk-reversed"}, ModeScan},
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
		if tc.mode != "" && (len(res.Terms) != 1 || res.Terms[0].Mode != tc.mode) {
			t.Errorf("%q: terms %+v, want one %s", tc.query, res.Terms, tc.mode)
		}
	}
}
