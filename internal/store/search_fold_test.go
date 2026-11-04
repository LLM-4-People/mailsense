package store

import (
	"fmt"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Review L0 bugs #2, #4, #5 (failed first: every query marked * found
// nothing): both indexes hold the whole-string fold; the scan of short
// substring terms compares folded text over the same fields as MATCH
// (sender and recipients too); a message whose only spaceless-script
// text is a recipient's name is in the substring index.
func TestSearchFoldsAndScansEveryField(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	docs := []struct{ id, subject, body, sender, recipient string }{
		{"nfd", "notes", "Compte rendu de la réunion de mardi", "", ""},
		{"arabic", "notes", "اَلْاِجْتِمَاعُ غَدًا", "", ""},
		{"kana-half", "notes", "ｶﾀﾛｸﾞ 送付", "", ""},
		{"hangul-nfd", "notes", "견적서 안내", "", ""},
		{"recipient-han", "plain", "plain text", "", "田中太郎"},
		{"sender-han", "plain", "plain text", "山田花子", ""},
		{"mixed", "Le résumé est prêt 見積書 Ré", "x", "", ""},
		{"thai", "notes", "ประชุมพรุ่งนี้ ใบเสนอราคา", "", ""},
		{"indic-body", "notes", "बैठक की योजना", "", ""},
		{"recipient-indic", "plain", "plain text", "", "कमला"},
	}
	var batch []mail.Message
	for _, d := range docs {
		m := mailtest.Message("src", d.id)
		m.Subject, m.Body, m.SenderName = d.subject, d.body, d.sender
		if d.recipient != "" {
			m.Recipients = []mail.Recipient{{Kind: mail.To, Name: d.recipient, Address: "tanaka@example.com"}}
		}
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		"réunion": {"nfd"}, "reunion": {"nfd"},
		"الاجتماع": {"arabic"},                            // *
		"カタログ":     {"kana-half"}, "ｶﾀﾛｸﾞ": {"kana-half"}, // *
		"カタ": {"kana-half"}, "ｶﾀ": {"kana-half"}, // * scan
		"견적서":     {"hangul-nfd"},    // *
		"田中":      {"recipient-han"}, // * scan over recipients
		"田中太郎":    {"recipient-han"}, // * trigram over recipients
		"山田":      {"sender-han"},    // * scan over the sender
		"見積書 ré":  {"mixed"},         // * folded scan of a Latin term
		"見積書 re":  {"mixed"},         // *
		"ประชุม":  {"thai"},
		"ใบเสนอ":  {"thai"},
		"की":      {"indic-body"},      // * scan of the exact matra
		"कमला":    {"recipient-indic"}, // * substring over recipients
		"कीx":     nil,
		"ประชุมx": nil,
	} {
		res, err := s.Search(ctx, q, 10)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var got []string
		for _, h := range res.Hits {
			got = append(got, h.ID)
		}
		slices.Sort(got)
		if fmt.Sprint(got) != fmt.Sprint(want) || res.Total != len(want) {
			t.Errorf("%q: hits %v (total %d, terms %+v), want %v", q, got, res.Total, res.Terms, want)
		}
	}
}

// A filter that matches nothing must be distinguishable from "no filter":
// the vector scan reads a nil id set as all messages.
func TestFilterIDsKeepsAnEmptySetNonNil(t *testing.T) {
	s, _ := open(t, nil)
	ids, err := s.FilterIDs(t.Context(), SearchFilter{Roles: []mail.FolderRole{mail.RoleDrafts}})
	if err != nil {
		t.Fatal(err)
	}
	if ids == nil || len(ids) != 0 {
		t.Fatalf("empty filter result %#v", ids)
	}
}
