//go:build fixture

package search

// Search over the legacy fixture (scripts/fixture-import.sh): the whole
// export indexed with the lexical fake embedder, then precision checks of
// full text, hybrid ranking, known-item retrieval, filters, coverage and
// the degraded path, with search latency. Aggregates only in the output.

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/store"
)

type fixtureWorld struct {
	st   *store.Store
	s    *Searcher
	fake *embedtest.Server
}

func TestFixtureSearch(t *testing.T) {
	cfg, st := fixturetest.Import(t, fixturetest.Path(t), map[string]any{"embed.batch_size": 32, "http.retry.max_attempts": 1})
	fake := embedtest.New(t, &embedtest.Fake{})
	embedtest.Install(t, cfg, fake.URL)
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	src := embed.NewSource(engine.Deps{Config: cfg, Secrets: sec, Locator: embedtest.Locator{URL: fake.URL}})
	ix := index.New(cfg, st, nil, src, nil)
	start := time.Now()
	rep, err := ix.Run(t.Context(), index.Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FIXTURE index embedded=%d eligible=%d wall=%s truncated=%d", rep.Embedded, rep.After.Eligible, time.Since(start).Round(time.Second), rep.Truncated)
	w := &fixtureWorld{st: st, s: newSearcher(t, cfg, Deps{Store: st, Embedder: src, Coverage: ix, Gate: learningtest.Open{}}), fake: fake}
	roles, _ := st.CountByRole(t.Context())
	total := 0
	for _, n := range roles {
		total += n
	}
	if rep.After.Indexed != rep.After.Eligible || rep.After.Eligible != total {
		t.Fatalf("FIXTURE every message is indexed: %+v", rep.After)
	}
	w.termPrecision(t)
	w.knownItems(t)
	w.filters(t)
	w.degraded(t)
}

// termPrecision: every full-text hit contains the (accent-folded) term.
// Logged only: how many of the full-text top 5 the fused top 10 keeps
// (RRF ranks what both rankings found above the top of either alone) and
// how many similarity-only hits contain the term (the fake's hashing).
func (w *fixtureWorld) termPrecision(t *testing.T) {
	terms := strings.Split(os.Getenv("FIXTURE_TERMS"), ",")
	if len(terms) == 0 || terms[0] == "" {
		t.Fatal("FIXTURE_TERMS (words of the export's language, comma-separated) is required")
	}
	for _, term := range terms {
		res := w.search(t, Query{Text: term, Limit: 10})
		text, textOK, fused, fusedOK, top5 := 0, 0, 0, 0, 0
		for _, h := range res.Hits {
			if h.TextRank >= 1 && h.TextRank <= 5 {
				top5++
			}
			ok := w.contains(t, h.RowID, term)
			fused++
			if ok {
				fusedOK++
			}
			if h.TextRank > 0 {
				text++
				if ok {
					textOK++
				}
			}
		}
		t.Logf("FIXTURE term=%q text_matches=%d text_precision=%d/%d text_top5_kept=%d fused_containing=%d/%d covered=%d", term, res.TextMatches, textOK, text, top5, fusedOK, fused, res.Semantic.Covered)
		if text == 0 || textOK != text || !res.Semantic.Used {
			t.Errorf("FIXTURE %q: precision too low", term)
		}
	}
}

func (w *fixtureWorld) contains(t *testing.T, row int64, term string) bool {
	m, err := w.st.MessageByRow(t.Context(), row)
	if err != nil {
		t.Fatal(err)
	}
	all := []string{m.Subject, m.Body, m.SenderName, m.SenderAddress, m.SentRepresentingAddress}
	for _, r := range m.Recipients {
		all = append(all, r.Name, r.Address)
	}
	return strings.Contains(mail.Fold(strings.Join(all, " ")), mail.Fold(term))
}

// knownItems: a message's own subject finds it in the top 10 (hybrid and
// full text alone; threads share subjects, so not every query can), with
// the search latency of those queries over every vector of the export.
func (w *fixtureWorld) knownItems(t *testing.T) {
	ids, _ := w.st.FilterIDs(t.Context(), store.SearchFilter{})
	slices.Sort(ids)
	var lat []float64
	hybrid, text, n := 0, 0, 0
	for i := 0; i < len(ids) && n < 200; i += 97 {
		m, _ := w.st.MessageByRow(t.Context(), ids[i])
		if len(mail.Words(m.Subject)) < 4 {
			continue
		}
		n++
		start := time.Now()
		res := w.search(t, Query{Text: m.Subject, Limit: 10})
		lat = append(lat, float64(time.Since(start).Microseconds())/1000)
		if slices.ContainsFunc(res.Hits, func(h Hit) bool { return h.RowID == ids[i] }) {
			hybrid++
		}
		if fts, _ := w.st.SearchText(t.Context(), m.Subject, store.SearchFilter{}, 10); slices.ContainsFunc(fts.Hits, func(h store.Hit) bool { return h.RowID == ids[i] }) {
			text++
		}
	}
	p50, _ := mathx.Percentile(lat, 0.5)
	p95, _ := mathx.Percentile(lat, 0.95)
	t.Logf("FIXTURE known_item queries=%d hybrid_hit@10=%d fts_hit@10=%d latency_ms p50=%.1f p95=%.1f", n, hybrid, text, p50, p95)
	if n < 100 || hybrid*100 < n*75 || text*100 < n*75 {
		t.Errorf("FIXTURE known-item retrieval too low: %d/%d", hybrid, n)
	}
}

// filters: every hit satisfies the filter in both rankings.
func (w *fixtureWorld) filters(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	res := w.search(t, Query{Text: "merci", Filter: store.SearchFilter{Roles: []mail.FolderRole{mail.RoleSent}, Since: since}, Limit: 50})
	bad := 0
	for _, h := range res.Hits {
		at, ok := mail.Parse(h.Instant)
		if h.Role != mail.RoleSent || !ok || at.Before(since) {
			bad++
		}
	}
	t.Logf("FIXTURE filter=sent-since-2026 hits=%d violations=%d", len(res.Hits), bad)
	if bad != 0 || len(res.Hits) == 0 {
		t.Errorf("FIXTURE filtered hits: %d of %d violate the filter", bad, len(res.Hits))
	}
}

// degraded: with the embedder down, full text answers and says so.
func (w *fixtureWorld) degraded(t *testing.T) {
	w.fake.Fake.SetStatus(500)
	defer w.fake.Fake.SetStatus(0)
	res := w.search(t, Query{Text: "facture", Limit: 10})
	t.Logf("FIXTURE degraded hits=%d notice=%q", len(res.Hits), strings.SplitN(res.Notice, "(", 2)[0])
	if res.Semantic.Used || len(res.Hits) == 0 || !strings.HasPrefix(res.Notice, "full-text results only") {
		t.Errorf("FIXTURE degraded search: %+v", res.Semantic)
	}
}

func (w *fixtureWorld) search(t *testing.T, q Query) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	res, err := w.s.Search(ctx, q)
	if err != nil {
		t.Fatal(fmt.Errorf("search %q: %w", q.Text, err))
	}
	return res
}
