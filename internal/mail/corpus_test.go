package mail_test

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Review L0 rules §7: the shared multilingual corpus drives the
// script-agnostic helpers — every sample folds idempotently and keeps its
// search term, CompatView is idempotent, CleanBody keeps its own text,
// its dominant script is the declared one, and its IDN sender has a
// registrable domain.
func TestCorpusThroughTheTextHelpers(t *testing.T) {
	for _, c := range mailtest.Corpus() {
		text := c.Subject + "\n" + c.Body
		if f := mail.Fold(text); mail.Fold(f) != f || !strings.Contains(f, mail.Fold(c.Term)) {
			t.Errorf("%s: fold %q (term %q)", c.Lang, f, c.Term)
		}
		if v := mail.CompatView(text); mail.CompatView(v) != v {
			t.Errorf("%s: CompatView is not idempotent", c.Lang)
		}
		if strings.TrimSpace(mail.CleanBody(c.Body)) == "" {
			t.Errorf("%s: CleanBody emptied the message's own text", c.Lang)
		}
		if got := mail.Scripts(text).Dominant(); got != c.Script {
			t.Errorf("%s: dominant script %q, want %q", c.Lang, got, c.Script)
		}
		if mail.RegistrableDomain(c.Sender) == "" {
			t.Errorf("%s: no registrable domain for %q", c.Lang, c.Sender)
		}
	}
}

// CompatView folds width and drops the default-ignorable controls (bidi
// marks, zero-width joiners, variation selectors), the same set Fold
// drops; Domains.Shared answers for registrable domains only.
func TestCompatViewAndSharedDomains(t *testing.T) {
	for in, want := range map[string]string{
		"ＲＥ：Ｓｕｊｅｔ":   "RE:Sujet",
		"a b":        "a b",
		"wrote‏:":    "wrote:",
		"zero‍width": "zerowidth",
		"ok️":        "ok",
		"؀١٢":        "؀١٢", // a prepended concatenation mark is not a control
	} {
		if got := mail.CompatView(in); got != want {
			t.Errorf("CompatView(%q) = %q, want %q", in, got, want)
		}
	}
	d := mail.NewDomains([]string{"mail.example.net", "", "not a domain"})
	if !d.Shared("example.net") || d.Shared("mail.example.net") || d.Shared("example.org") || d.Shared("") {
		t.Fatal("Shared takes registrable domains")
	}
}
