package goldexport

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func strata(t *testing.T) *Strata {
	s := NewStrata()
	for row := int64(1); row <= 40; row++ {
		cat := "work"
		if row%13 == 0 {
			cat = "security" // rare: 3 of 40
		}
		s.Add(Key{Script: "Latin", Category: cat, SenderKind: SenderExternal, Year: "2026"}, row)
	}
	return s
}

func TestDrawIsSeededFlooredAndProportional(t *testing.T) {
	o := Options{Size: 10, Seed: 7, RareFloor: 3, Dir: "x"}
	rep, err := strata(t).Report(o)
	if err != nil {
		t.Fatal(err)
	}
	rows := Draw(rep, o)
	if len(rows) != 10 || rep.Sampled != 10 || rep.Categories["security"] != [2]int{3, 3} || rep.Categories["work"] != [2]int{37, 7} {
		t.Fatalf("%v %+v", rows, rep)
	}
	again, _ := strata(t).Report(o)
	if !slices.Equal(Draw(again, o), rows) {
		t.Fatal("the same seed draws the same rows")
	}
	if _, err := strata(t).Report(Options{Size: 41, Dir: "x"}); err == nil {
		t.Fatal("a size above the eligible mail is refused")
	}
}

func TestOptionsAndSenderKind(t *testing.T) {
	for _, bad := range []Options{{Size: 0, Dir: "d"}, {Size: 1}, {Size: 1, RareFloor: -1, Dir: "d"}} {
		if bad.Check() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	orgs, d := map[string]bool{"example.org": true}, mail.Domains{}
	for m, want := range map[*mail.Message]string{
		{SenderAddress: "a@mail.example.org"}:                                 SenderInternal,
		{SenderAddress: "a@example.net"}:                                      SenderExternal,
		{SenderAddress: "a@example.org", Headers: "Precedence: bulk"}:         SenderAutomated,
		{SenderAddress: "a@example.net", Headers: "List-Id: <l.example.net>"}: SenderAutomated,
	} {
		if got := SenderKind(m, orgs, d); got != want {
			t.Errorf("%+v: %s, want %s", m, got, want)
		}
	}
}

// Unreadable legacy lines are counted (reported in strata.json), never
// silently skipped; a missing file is an error.
func TestLegacyCategoriesCountsBadLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.jsonl")
	data := `{"classification": {"message_id": "m1", "answers": {"category": {"value": "work"}}}}` + "\nnot json\n\n{}\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := LegacyCategories(p, 1<<20)
	if err != nil || l.Categories["m1"] != "work" || l.BadLines != 2 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := LegacyCategories(filepath.Join(t.TempDir(), "missing"), 1<<20); err == nil {
		t.Fatal("a missing file is an error")
	}
	if l, err := LegacyCategories("", 1); err != nil || len(l.Categories) != 0 {
		t.Fatal("no file: no categories")
	}
}
