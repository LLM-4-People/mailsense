//go:build fixture

package layer1

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// harnessInputs wraps the store's inputs for the dev harness: the
// excluded messages (gold dev and test, optionally their conversation-
// mates) never get a target from any source; the population can be
// restricted (the E1 sample); the vectors are the experiment's.
type harnessInputs struct {
	*StoreInputs
	exclude map[int64]bool
	only    map[int64]bool // nil: every member
	space   string
	set     *vector.Set
}

func (h *harnessInputs) Members(ctx context.Context) ([]Member, error) {
	all, err := h.StoreInputs.Members(ctx)
	if err != nil || h.only == nil {
		return all, err
	}
	var out []Member
	for _, m := range all {
		if h.only[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

func (h *harnessInputs) MemberIDs(ctx context.Context) ([]int64, error) {
	all, err := h.StoreInputs.MemberIDs(ctx)
	if err != nil || h.only == nil {
		return all, err
	}
	var out []int64
	for _, id := range all {
		if h.only[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (h *harnessInputs) Vectors(context.Context) (string, *vector.Set, error) {
	return h.space, h.set, nil
}

func (h *harnessInputs) Labels(ctx context.Context) ([]store.LabelRow, error) {
	all, err := h.StoreInputs.Labels(ctx)
	var out []store.LabelRow
	for _, l := range all {
		if !h.exclude[l.MessageID] {
			out = append(out, l)
		}
	}
	return out, err
}

func (h *harnessInputs) Decisions(ctx context.Context, key store.TriageKey, fn func(int64, map[string]engine.Answer) error) error {
	return h.StoreInputs.Decisions(ctx, key, func(id int64, a map[string]engine.Answer) error {
		if h.exclude[id] {
			return nil
		}
		return fn(id, a)
	})
}

// Rules: an excluded message's rule firings teach nothing either (the
// guard caught them on the first run: 393 dev/test messages had a rule
// target).
func (h *harnessInputs) Rules(ctx context.Context, eval map[int64]map[string]json.RawMessage) ([]RuleInfo, map[int64][]int, error) {
	rules, fires, err := h.StoreInputs.Rules(ctx, eval)
	for id := range fires {
		if h.exclude[id] {
			delete(fires, id)
		}
	}
	return rules, fires, err
}

// guard fails the test when an excluded message has a training target or
// an evaluation label of any question (the dev split must stay unseen).
func guard(t *testing.T, exclude map[int64]bool) func(*Trained) error {
	return func(tr *Trained) error {
		bad := 0
		for id := range tr.TargetIDs() {
			if exclude[id] {
				bad++
			}
		}
		if bad > 0 {
			return fmt.Errorf("GUARD: %d excluded (gold dev/test) messages have a training target or an evaluation label", bad)
		}
		t.Logf("LAYER1 guard: no excluded message (%d) has a target", len(exclude))
		return nil
	}
}

// cachedVectors builds the vector set of a purpose from the private
// vector cache (no embedding here): the members' inputs as the purpose
// builds them, looked up by exact input. A member whose input is not
// cached has no vector (pending). saveTo, when set, also writes them into
// the store as the index would (the document space: the evidence's
// template signal reads it).
func cachedVectors(t *testing.T, cache *fixturetest.VectorCache, p purpose, e engine.Embedder, st *store.Store, ids []int64, saveTo bool) (*vector.Set, int) {
	t.Helper()
	texts, err := st.MessageTexts(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	revs := map[int64]int64{}
	if saveTo {
		states, err := st.EmbeddingStates(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			revs[s.RowID] = s.Revision
		}
	}
	set := &vector.Set{}
	var rows []store.EmbeddingRow
	missing := 0
	version := mail.DataDigest()[:16]
	for _, id := range ids {
		m, ok := texts[id]
		if !ok {
			continue
		}
		in := e.Input(p.text(m))
		v, err := cache.Get(e.Space(), e.Dimensions(), in)
		if err != nil {
			t.Fatal(err)
		}
		if v == nil {
			missing++
			continue
		}
		q := vector.Quantize(v)
		set.Put(id, q.Values, q.Scale)
		if saveTo {
			rows = append(rows, store.EmbeddingRow{MessageID: id, Space: e.Space(), TextHash: digest.Hex([]byte(in), 16),
				TextVersion: version, Revision: revs[id], Vector: q})
		}
	}
	if saveTo {
		for i := 0; i < len(rows); i += 2000 {
			if err := st.SaveEmbeddings(t.Context(), rows[i:min(i+2000, len(rows))]); err != nil {
				t.Fatal(err)
			}
		}
	}
	return set, missing
}
