package evidence

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// pinsFile pins, per signal, its version and the digest of its values over
// pinCorpus.
const pinsFile = "testdata/signal-pins.json"

// pinCorpus is a small multilingual set of messages that exercises the
// extractors (dates of every shape, a year 0000, quotes and attributions,
// signatures, amounts, links, questions, replies).
func pinCorpus() []messageSpec {
	return []messageSpec{
		{"p1", "ann@example.org", 0, "Bonjour,\nLe rapport est attendu le 20/10/2026 à 10:00, merci de confirmer ?\n\nLe lun. 12 oct. 2026 à 09:00, Bob <bob@example.org> a écrit :\n\tAncien texte"},
		{"p2", "bob@example.org", 1, "Hi,\nplease pay USD 1,250.00 before 03/04/2026 or on Friday.\n-- \nBob\n+1 555 123 4567"},
		{"p3", "carl@example.net", 2, "Form placeholder: 0000-10-20, then 2026-10-21.\nSee https://example.net/x and www.example.net."},
		{"p4", "dora@example.com", 3, "Hallo,\nam 20.10. um 14:30 Uhr? Bitte bis Montag.\n\n> Am 11.10.2026 schrieb Eva <eva@example.com>:\n> alter Text\nDanke."},
		{"p5", "eva@example.jp", 4, "了解しました。2026年10月20日に伺います。\n\n2026年10月12日(月) 9:00 Ann <ann@example.org>：\n\n\t元の本文"},
		{"p6", "fadi@example.ae", 5, "شكرا لك، الاجتماع يوم ٢٠ أكتوبر ٢٠٢٦؟"},
		{"p7", "mailtest-owner", 6, "Merci, c'est noté.\n\nAgenda with ann@example.org on 20 Oct 2026:\n\tBudget\n\tHiring\nThanks"},
	}
}

type messageSpec struct {
	id, sender string
	hours      int
	body       string
}

// Review of the crash-safety round, #10: date_unresolved's values changed
// while its version stayed 1, so stored values were reused as current.
// Every signal's values over pinCorpus are pinned with its effective
// version (its Version, the settings and the pinned data it declares):
// values that change while it stays the same fail here
// (EVIDENCE_PIN_WRITE=1 rewrites the pins after a bump).
func TestSignalValuesChangeOnlyWithTheirVersion(t *testing.T) {
	w := newWorld(t, everything, nil)
	for _, s := range pinCorpus() {
		sender := s.sender
		if sender == "mailtest-owner" {
			w.write(sentMsg(s.id, "ann@example.org", s.hours, s.body))
			continue
		}
		w.write(msg(s.id, sender, s.hours, s.body))
	}
	w.run()
	values := map[string][]Value{}
	for _, s := range pinCorpus() {
		rec := w.record(s.id)
		for name, v := range rec {
			values[name] = append(values[name], v)
		}
	}
	schema, err := newSchema(Signals(), w.cfg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]pin{}
	for i, sig := range schema.Signals {
		raw, err := json.Marshal(values[sig.Name])
		if err != nil {
			t.Fatal(err)
		}
		got[sig.Name] = pin{Key: schema.Keys[i], Values: digest.Hex(raw, 16)}
	}
	if os.Getenv("EVIDENCE_PIN_WRITE") == "1" {
		raw, _ := json.MarshalIndent(got, "", "  ")
		if err := fsx.WriteFileAtomic(pinsFile, append(raw, '\n'), fsx.FilePerm, fsx.Replace{Attempts: 1}); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(pinsFile)
	if err != nil {
		t.Fatalf("%v: run EVIDENCE_PIN_WRITE=1 go test -run TestSignalValuesChangeOnlyWithTheirVersion ./internal/evidence", err)
	}
	var pinned map[string]pin
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatal(err)
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range got {
			if !yield(k) {
				return
			}
		}
	})
	for _, name := range names {
		g, p := got[name], pinned[name]
		switch {
		case p.Key == "":
			t.Errorf("%s is not pinned: rewrite the pins (EVIDENCE_PIN_WRITE=1)", name)
		case g.Key == p.Key && g.Values != p.Values:
			t.Errorf("%s %s: its values changed without a new version (raise Version, or the data it declares), then rewrite the pins", name, g.Key)
		case g.Key != p.Key: // a new version, setting or data digest: pin it
			t.Errorf("%s: effective version %s, pinned %s: rewrite the pins (EVIDENCE_PIN_WRITE=1)", name, g.Key, p.Key)
		}
	}
}

// pin is a signal's effective version (Schema.Keys: its version, the
// settings and pinned data it reads) and the digest of its values.
type pin struct {
	Key    string `json:"key"`
	Values string `json:"values"`
}
