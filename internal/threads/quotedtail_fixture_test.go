//go:build fixture

package threads

// Measurement for the queued layer-0 experiment "quoted tail by parent
// match" (docs/LAYER1.md): how many messages' cleaned own text ends in
// lines that are already in the message they answer (a quoted signature
// or contact table Outlook rendered without the tab), and how many gold
// dev messages are among them. Aggregates only.

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// minTail is the fewest trailing lines counted as a quoted tail.
const minTail = 3

func normLine(l string) string {
	return mail.Fold(strings.Join(mail.Words(strings.TrimLeft(strings.TrimSpace(l), "> \t")), " "))
}

func TestFixtureQuotedTails(t *testing.T) {
	cfg, st := fixturetest.Import(t, fixturetest.Path(t), nil)
	set, err := SettingsFrom(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set.Roles = CausalRoles()
	a, err := Load(t.Context(), st, set)
	if err != nil {
		t.Fatal(err)
	}
	byMID := map[string]int64{}
	parent := map[int64]string{}
	var ids []int64
	gold := map[int64]string{}
	var dev map[string]goldset.Answers
	if dir := os.Getenv("LAYER1_GOLD_DIR"); dir != "" {
		g, err := goldset.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		dev = g.Of(goldset.Dev)
	}
	_ = st.EachMessage(t.Context(), store.MessageFilter{OwnerOnly: true}, func(r *store.MessageRow) error {
		ids = append(ids, r.RowID)
		self, parents := mail.ThreadIDs(&r.Message)
		if self != "" {
			byMID[self] = r.RowID
		}
		if len(parents) > 0 {
			parent[r.RowID] = parents[0]
		}
		if _, ok := dev[goldexport.ID(&r.Message)]; ok {
			gold[r.RowID] = "dev"
		}
		return nil
	})
	texts, err := st.MessageTexts(t.Context(), ids)
	_ = set
	if err != nil {
		t.Fatal(err)
	}
	var withParent, tails, devTails, devWithParent, tailLines, afterBlock int
	for _, id := range ids {
		pid, ok := byMID[parent[id]]
		if !ok { // no header: the conversation's latest earlier message, as known at its instant
			if _, earlier, found := a.EarlierMembers(id); found && len(earlier) > 0 {
				pid, ok = earlier[len(earlier)-1].RowID, true
			}
		}
		if !ok {
			continue
		}
		withParent++
		if gold[id] == "dev" {
			devWithParent++
		}
		have := map[string]bool{}
		for _, l := range strings.Split(texts[pid].Body, "\n") {
			if n := normLine(l); n != "" {
				have[n] = true
			}
		}
		// the case CleanBody keeps (review 3): a tab-indented quote block
		// followed by more lines; only the lines after the block count
		var own []string
		indented, block := 0, false
		for _, l := range strings.Split(mail.CleanBody(texts[id].Body), "\n") {
			if strings.HasPrefix(l, "\t") && strings.TrimSpace(l) != "" {
				indented++
				if indented >= minTail {
					block, own = true, nil
				}
				continue
			}
			indented = 0
			if n := normLine(l); n != "" && block {
				own = append(own, n)
			}
		}
		if !block {
			continue
		}
		afterBlock++
		k := 0
		for k < len(own) && have[own[len(own)-1-k]] {
			k++
		}
		if k >= minTail {
			tails++
			tailLines += k
			if gold[id] == "dev" {
				devTails++
			}
		}
	}
	t.Logf("FIXTURE quoted tails: %d messages answer a stored parent; %d keep text after a tab-indented block; %d of them end in ≥%d lines found in the parent (%d lines in all); dev: %d (of %d answering a parent)",
		withParent, afterBlock, tails, minTail, tailLines, devTails, devWithParent)
}
