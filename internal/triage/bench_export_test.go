package triage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// exportWorld is 40 owner messages in four scripts, every tenth from a
// list, and a legacy file classifying 3 of them "security" (rare) plus
// one unreadable line.
func exportWorld(t *testing.T) (*world, string) {
	w := newWorld(t, nil)
	scripts := []string{"Bonjour, pouvez-vous confirmer ?", "请在星期五之前发送报告。", "هل يمكنك المراجعة؟", "Пришлите отчёт."}
	for i := range 40 {
		w.mail(fmt.Sprintf("s%d@x%d.org", i, i%3), fmt.Sprint("C", i), "Sujet", time.Duration(i)*time.Hour, 0)
	}
	n := 0
	var lines []string
	_ = w.st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		m := r.Message
		m.Body = scripts[n%len(scripts)]
		if n%10 == 0 {
			m.Headers = "List-Id: <l.example.org>"
		}
		n++
		cat := "work"
		if r.RowID%13 == 0 {
			cat = "security" // rare: 3 of 40
		}
		lines = append(lines, fmt.Sprintf(`{"classification": {"message_id": %q, "answers": {"category": {"value": %q}}}}`, m.ID, cat))
		return w.st.UpsertMessages(t.Context(), 0, []mail.Message{m})
	})
	w.ownAll()
	legacy := filepath.Join(t.TempDir(), "classifications.jsonl")
	if err := os.WriteFile(legacy, []byte(strings.Join(lines, "\n")+"\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return w, legacy
}

// The export is offline, seeded, stratified, floors rare categories,
// counts unreadable legacy lines, uses the benchmark's ids and writes
// private files only.
func TestGoldExport(t *testing.T) {
	w, legacy := exportWorld(t)
	dir := filepath.Join(t.TempDir(), "sample")
	o := goldexport.Options{Size: 10, Seed: 7, RareFloor: 3, Legacy: legacy, Dir: dir}
	rep, err := w.tr.ExportGold(t.Context(), w.st, o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Eligible != 40 || rep.Sampled != 10 || rep.Categories["security"] != [2]int{3, 3} || rep.LegacyBadLines != 1 {
		t.Fatalf("%+v", rep)
	}
	kinds, scriptsSeen := map[string]bool{}, map[string]bool{}
	for _, s := range rep.Strata {
		kinds[s.SenderKind], scriptsSeen[s.Script] = true, true
	}
	if !kinds[goldexport.SenderAutomated] || !kinds[goldexport.SenderExternal] || !scriptsSeen["Han"] || !scriptsSeen["Arabic"] || !scriptsSeen["Cyrillic"] {
		t.Fatalf("strata by sender kind and script: %+v", rep.Strata)
	}
	raw, err := os.ReadFile(filepath.Join(dir, goldexport.InputFile))
	if err != nil || strings.Count(string(raw), "\n") != 10 {
		t.Fatalf("%v %d", err, strings.Count(string(raw), "\n"))
	}
	for _, name := range []string{goldexport.InputFile, goldexport.TaxonomyFile, goldexport.StrataFile} {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", name, err, st)
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v", st.Mode())
	}
	o.Dir = filepath.Join(t.TempDir(), "b")
	if again, _ := w.tr.ExportGold(t.Context(), w.st, o); fmt.Sprint(again.Strata) != fmt.Sprint(rep.Strata) {
		t.Fatal("the same seed draws the same sample")
	}
	if w.dec.count() != 0 {
		t.Fatal("no provider call")
	}
}

func TestGoldExportRefusesMisuse(t *testing.T) {
	w, _ := exportWorld(t)
	dir := t.TempDir()
	for name, bad := range map[string]goldexport.Options{"size": {Size: 0, Dir: dir}, "too big": {Size: 99, Dir: dir}, "no dir": {Size: 1},
		"negative floor": {Size: 1, RareFloor: -1, Dir: dir}, "missing legacy": {Size: 1, Dir: dir, Legacy: "/missing"}} {
		if _, err := w.tr.ExportGold(t.Context(), w.st, bad); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}
