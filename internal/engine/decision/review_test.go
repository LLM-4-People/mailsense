package decision

import (
	"encoding/json"
	"errors"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// Review 2a #3 (repro D1, failed first: the error dropped chunk 1): a
// failure in chunk 2 keeps the paid chunk-1 answers in the result, so the
// caller can store them and ask only the missing questions next time.
func TestFailedLaterChunkKeepsEarlierAnswers(t *testing.T) {
	qs := taxonomy17()
	ok := chunkReplies(qs, 16)
	srv := newServer(t, ok[0], reply{500, "oops"})
	h := newAdapter(t, provider(srv, "systemone"))
	res, err := h.ad.Decide(t.Context(), "state", qs)
	if err == nil || len(res.Answers) != 16 || res.Usage.Calls != 2 || srv.hits() != 2 {
		t.Fatalf("chunk 1 answers must survive a chunk 2 failure: err=%v answers=%d calls=%d", err, len(res.Answers), res.Usage.Calls)
	}
	if _, missing := res.Answers["q16"]; missing {
		t.Fatal("the failed chunk's question has no answer")
	}
}

// Review 2a #9 (repro D3, failed first: accepted): probabilities that do
// not sum to 1 and a choice that is not the most probable are contract
// errors (vendor semantics verified on provider.example.test and 335,870 legacy answers).
func TestInconsistentProbabilitiesAreRejected(t *testing.T) {
	qs := []engine.Question{{Name: "c", Type: engine.Choice, Instructions: "x", Options: []engine.Option{{Name: "a"}, {Name: "b"}}},
		{Name: "s", Type: engine.Score, Instructions: "x", Levels: []string{"l0", "l1", "l2"}}}
	body := `{"answers":{"c":{"type":"choice","choice":"a","probabilities":{"a":0.05,"b":0.99},"confidence":1},
	"s":{"type":"score","score":2,"probabilities":{"0":1,"1":1,"2":1},"confidence":0.5}},"usage":{"input_tokens":5}}`
	srv := newServer(t, reply{200, body})
	h := newAdapter(t, provider(srv, "systemone"))
	if res, err := h.ad.Decide(t.Context(), "state", qs); !errors.Is(err, ErrContract) || len(res.Answers) != 0 {
		t.Fatalf("contradictory answers accepted: %v %+v", err, res.Answers)
	}
	// Ondelle rounds probabilities to two decimals: the documented example passes.
	body = `{"answers":{"c":{"type":"choice","choice":"a","probabilities":{"a":0.5,"b":0.5},"confidence":0.5},
	"s":{"type":"score","score":1.86,"probabilities":{"0":0,"1":0.13,"2":0.87},"confidence":0.79}},"usage":{"input_tokens":5}}`
	srv = newServer(t, reply{200, body})
	h = newAdapter(t, provider(srv, "systemone"))
	if _, err := h.ad.Decide(t.Context(), "state", qs); err != nil {
		t.Fatalf("rounded vendor answers and ties pass: %v", err)
	}
}

// Review 2a #8 (failed first: 0 tokens recorded): a 500 and a failure after
// the request was written may have been billed; they are accounted at the
// estimate. A declined (retryable) or 4xx status costs nothing.
func TestPossiblyBilledFailuresAreAccountedAtTheEstimate(t *testing.T) {
	qs := taxonomy17()[:1]
	for name, c := range map[string]struct {
		status    int
		estimated bool
	}{"500": {500, true}, "402": {402, false}, "503 exhausted": {503, false}} {
		srv := newServer(t, reply{c.status, `{}`})
		p := provider(srv, "systemone")
		one := 1
		p.Retry.MaxAttempts = &one
		h := newAdapter(t, p)
		res, _ := h.ad.Decide(t.Context(), "state", qs)
		if (res.Usage.InputTokens > 0) != c.estimated || h.sink.recs[0].Estimated != c.estimated || (res.Usage.EstimatedUSD > 0) != c.estimated {
			t.Errorf("%s: usage %+v rec %+v", name, res.Usage, h.sink.recs[0])
		}
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	p := provider(&server{Server: srv}, "systemone")
	h := newAdapterWith(t, p, httpx.WithBaseTransport(&http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}))
	res, err := h.ad.Decide(t.Context(), "state", qs)
	if !errors.Is(err, httpx.ErrAfterWrite) || res.Usage.InputTokens == 0 || !h.sink.recs[0].Estimated {
		t.Fatalf("a read timeout after the write is accounted: %v %+v", err, res.Usage)
	}
}

// Rules review §6: a response beyond the 16 MiB safety cap is refused.
func TestOversizedResponseIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for range 17 {
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()
	h := newAdapter(t, provider(&server{Server: srv}, "systemone"))
	if _, err := h.ad.Decide(t.Context(), "state", taxonomy17()[:1]); !errors.Is(err, ErrHTTP) || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("oversized response: %v", err)
	}
}

// guard is a test budget: it admits `allow` calls, recording ceilings
// and settled usage.
type guard struct {
	allow    int
	ceilings []engine.Estimate
	settled  []engine.Usage
}

func (g *guard) Reserve(c engine.Estimate) bool {
	if len(g.ceilings) >= g.allow {
		return false
	}
	g.ceilings = append(g.ceilings, c)
	return true
}

func (g *guard) Settle(_ engine.Estimate, u engine.Usage) { g.settled = append(g.settled, u) }

// Leftover of review 2a (budget overshoot): every call reserves its
// ceiling first and settles after; a refused call is not sent and the
// earlier chunk's answers are kept.
func TestEveryCallIsReservedBeforeItIsSent(t *testing.T) {
	qs := taxonomy17()
	srv := newServer(t, chunkReplies(qs, 16)...)
	h := newAdapter(t, provider(srv, "systemone"))
	g := &guard{allow: 1}
	res, err := h.ad.Decide(engine.WithReserver(t.Context(), g), "state", qs)
	if !errors.Is(err, engine.ErrBudget) || srv.hits() != 1 || len(res.Answers) != 16 || len(g.settled) != 1 || g.settled[0].InputTokens != 477 {
		t.Fatalf("one call reserved, sent and settled; the second refused before sending: %v hits=%d answers=%d settled=%+v", err, srv.hits(), len(res.Answers), g.settled)
	}
	body, _ := json.Marshal(srv.bodies[0])
	if c := g.ceilings[0]; c.Calls != 1 || c.InputTokens < int64(len(body))-16 || c.EstimatedUSD <= h.ad.p.Cost(477, 0) {
		t.Fatalf("the ceiling is one token per request byte (%d bytes): %+v", len(body), c)
	}
}

// Review 2b (low): the ceiling covers the output price too; a provider
// with an output price must say how much output a call may bill.
func TestTheCeilingCoversTheOutputPrice(t *testing.T) {
	srv := newServer(t, chunkReplies(taxonomy17(), 16)...)
	p := provider(srv, "systemone")
	p.PriceOutput, p.ReserveOutputTokens = price(2), 1000
	h := newAdapter(t, p)
	g := &guard{allow: 1}
	_, _ = h.ad.Decide(engine.WithReserver(t.Context(), g), "state", taxonomy17()[:1])
	if c := g.ceilings[0]; c.EstimatedUSD < 1000*2/1e6 {
		t.Fatalf("output reserved: %+v", c)
	}
	cfg := configtest.New(t, nil)
	writeProvidersFile(t, cfg, `{"dec": {"protocol": "systemone", "base_url": "http://127.0.0.1:9/v1",
		"path": "/systemone", "model": "m", "api_key_secret": "K", "max_questions": 8,
		"context_tokens": 1000, "chars_per_token": 3, "reserve_tokens_per_byte": 1,
		"probability_tolerance": 0.02, "price_input_per_mtok": 1, "price_output_per_mtok": 1}}`)
	if _, err := engine.LoadCatalog(cfg); err == nil || !strings.Contains(err.Error(), "reserve_output_tokens") {
		t.Fatalf("an output price without reserve_output_tokens is refused: %v", err)
	}
}

func writeProvidersFile(t *testing.T, cfg *config.Config, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "providers.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
