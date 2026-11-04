//go:build fixture || embedlocal

package layer1

// The experiment sample shared by the embedding job (embedlocal) and the
// fixture harness (fixture): every gold message plus a stratified draw
// of the other owner messages by their legacy category.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// sampleSeed fixes the experiment draw.
const sampleSeed = "20261015"

// harnessEnv reads a harness variable, or skips the test without it.
func harnessEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set (scripts/layer1-fixture.sh sets it)", name)
	}
	return v
}

// harnessContext ends when the harness's stop file appears
// (scripts/layer1-fixture.sh stop: a graceful stop, never a kill) or
// before the test's deadline; a stale stop file is removed first.
func harnessContext(t *testing.T, mode string) context.Context {
	t.Helper()
	stop := filepath.Join(filepath.Dir(harnessEnv(t, "LAYER1_CACHE_DIR")), "stop-"+mode)
	_ = os.Remove(stop)
	return livetest.Context(t, stop)
}

// goldOf opens the sealed gold sample of LAYER1_GOLD_DIR.
func goldOf(t *testing.T) *goldset.Set {
	t.Helper()
	g, err := goldset.Open(harnessEnv(t, "LAYER1_GOLD_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// rowsByGoldID maps every owner message's gold id (goldexport.ID) to its row.
func rowsByGoldID(t *testing.T, st *store.Store) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := st.EachMessage(t.Context(), store.MessageFilter{OwnerOnly: true}, func(r *store.MessageRow) error {
		out[goldexport.ID(&r.Message)] = r.RowID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// experimentSample is every gold message (all splits: their vectors are
// needed to evaluate) plus n other owner messages drawn by legacy
// category (largest remainder, seeded order), sorted by row.
// providerOverrides lets the harness read the same provider catalog the app
// uses. configtest builds a temporary data directory, so without them the
// catalog is empty and the fixture cannot find its embedder. The names match
// scripts/embed-local.sh: EMBEDLOCAL_PROVIDERS / EMBEDLOCAL_PROVIDER.
func providerOverrides(overrides map[string]any) map[string]any {
	if p := os.Getenv("EMBEDLOCAL_PROVIDERS"); p != "" {
		overrides[engine.ProvidersFileSetting] = p
	}
	if id := os.Getenv("EMBEDLOCAL_PROVIDER"); id != "" {
		overrides["embed.provider"] = id
	}
	return overrides
}

func experimentSample(t *testing.T, st *store.Store, gold *goldset.Set, n int) []int64 {
	t.Helper()
	legacy, err := goldexport.LegacyCategories(harnessEnv(t, "LAYER1_LEGACY"), 1<<26)
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsByGoldID(t, st)
	type drawn struct {
		key string // the seeded order: a digest of the seed and the gold-style id
		row int64
	}
	byCat := map[string][]drawn{}
	var out []int64
	err = st.EachMessage(t.Context(), store.MessageFilter{OwnerOnly: true}, func(r *store.MessageRow) error {
		id := goldexport.ID(&r.Message)
		if _, isGold := gold.Splits[id]; isGold {
			return nil
		}
		if c := legacy.Categories[r.Message.ID]; c != "" {
			byCat[c] = append(byCat[c], drawn{digest.Hex([]byte(sampleSeed+"/"+id), 16), r.RowID})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for id := range gold.Splits {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("gold id %s names no owner message of the fixture", id)
		}
		out = append(out, row)
	}
	cats := slices.Sorted(func(yield func(string) bool) {
		for c := range byCat {
			if !yield(c) {
				return
			}
		}
	})
	sizes := make([]int, len(cats))
	for i, c := range cats {
		sizes[i] = len(byCat[c])
	}
	for i, k := range mathx.Allocate(sizes, n) {
		ids := byCat[cats[i]]
		slices.SortFunc(ids, func(a, b drawn) int { return strings.Compare(a.key, b.key) })
		for _, d := range ids[:k] {
			out = append(out, d.row)
		}
	}
	slices.Sort(out)
	return out
}

// purpose is one way of embedding a message for experiment E1: the
// index's document input (template "": the provider's title/text
// template), or a query-style task prompt of the model card over the
// subject and the cleaned body (the classification and clustering
// prompts have no title slot).
type purpose struct{ name, template string }

var purposes = []purpose{
	{"document", ""},
	{"classification", "task: classification | query: {text}"},
	{"clustering", "task: clustering | query: {text}"},
}

func purposeNamed(t *testing.T, name string) purpose {
	t.Helper()
	for _, p := range purposes {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("unknown purpose %q", name)
	return purpose{}
}

// embedder is the configured embedder with the purpose's template (a
// new vector space: the template is part of it). Building it starts
// nothing; only Embed calls the runtime.
func (p purpose) embedder(t *testing.T, deps engine.Deps) engine.Embedder {
	t.Helper()
	cat, err := engine.LoadCatalog(deps.Config)
	if err != nil {
		t.Fatal(err)
	}
	prov, ok := cat.Get(deps.Config.String("embed.provider"))
	if !ok {
		t.Fatal("no embedder provider")
	}
	if p.template != "" {
		spec := *prov.Embedding
		spec.QueryTemplate = p.template
		// The template is part of the embedding fingerprint: changing it
		// already gives the purpose its own vector space.
		prov.Embedding = &spec
	}
	e, err := embed.New(prov, deps)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// text is the purpose's input for a message: exactly the index's for
// "document" (so the full index run finds them cached).
func (p purpose) text(m store.MessageText) engine.Text {
	if p.template == "" {
		return engine.Text{Purpose: engine.PurposeDocument, Title: m.Subject, Body: mail.CleanBody(m.Body)}
	}
	return engine.Text{Purpose: engine.PurposeQuery, Body: m.Subject + "\n" + mail.CleanBody(m.Body)}
}
