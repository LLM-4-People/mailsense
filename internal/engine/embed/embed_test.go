package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

type sink struct {
	mu   sync.Mutex
	recs []engine.UsageRecord
}

func (s *sink) AddUsage(_ context.Context, r engine.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return nil
}

// locator serves a fixed endpoint (or an error), counting calls and
// failure reports.
type locator struct {
	ep     engine.Endpoint
	err    error
	calls  int
	failed []engine.Endpoint
	next   *engine.Endpoint // after a failure report, hand out this one
}

func (l *locator) Failed(_, _ string, ep engine.Endpoint) {
	l.failed = append(l.failed, ep)
	if l.next != nil {
		l.ep = *l.next
	}
}

func (l *locator) Model(string, string) (engine.ModelInfo, error) {
	return engine.ModelInfo{Revision: embedtest.Revision, ContextTokens: embedtest.Context}, nil
}

func (l *locator) Locate(_ context.Context, runtime, model string) (engine.Endpoint, error) {
	l.calls++
	if runtime != "llama-server" || model != "embeddinggemma-2" {
		return engine.Endpoint{}, fmt.Errorf("unexpected %s/%s", runtime, model)
	}
	return l.ep, l.err
}

// preset is a complete managed embedder record aimed at the test locator.
func preset(t *testing.T, _ *config.Config) engine.Provider {
	t.Helper()
	return engine.Provider{
		ID: "emb", Label: "Test embedder", Protocol: Protocol, Runtime: "llama-server", Path: "/v1/embeddings",
		Model: "embeddinggemma-2", CharsPerToken: 5,
		Embedding: &engine.EmbeddingSpec{
			Dimensions: 768, Matryoshka: []int{512, 256, 128},
			QueryTemplate:    "task: search result | query: {text}",
			DocumentTemplate: "title: {title} | text: {text}",
			EmptyTitle:       "none", NormTolerance: 0.01, MaxBatch: 32, ReservedTokens: 8,
		},
	}
}

// install writes p as providers.json and selects it (the Source reads the
// real file and role setting).
func install(t *testing.T, cfg *config.Config, p engine.Provider) {
	t.Helper()
	p.ID = ""
	data, err := json.Marshal(map[string]engine.Provider{"emb": p})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "providers.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("embed.provider", "emb"); err != nil {
		t.Fatal(err)
	}
}

type world struct {
	cfg  *config.Config
	fake *embedtest.Server
	loc  *locator
	sink *sink
	bus  *events.Bus
	a    *Adapter
}

func newWorld(t *testing.T, overrides map[string]any) *world {
	t.Helper()
	w := &world{bus: events.New(), sink: &sink{}}
	t.Cleanup(w.bus.Close)
	w.cfg = configtest.New(t, overrides)
	w.fake = embedtest.New(t, &embedtest.Fake{Key: "k1"})
	w.loc = &locator{ep: engine.Endpoint{URL: w.fake.URL, Key: "k1"}}
	sec, _ := secrets.Open(w.cfg, nil, nil, nil)
	a, err := New(preset(t, w.cfg), engine.Deps{Config: w.cfg, Bus: w.bus, Secrets: sec, Sink: w.sink, Locator: w.loc,
		Clock: clocktest.New(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	w.a = a
	return w
}

func docs(n int) []engine.Text {
	out := make([]engine.Text, n)
	for i := range out {
		out[i] = engine.Text{Purpose: engine.PurposeDocument, Title: fmt.Sprint("Subject ", i), Body: fmt.Sprintf("body number %d about invoices", i)}
	}
	return out
}

func TestEmbedBatchesInOrderAndRecordsUsage(t *testing.T) {
	w := newWorld(t, map[string]any{"embed.batch_size": 2})
	sub, _ := events.Subscribe(w.bus, engine.TopicUsage, 16)
	texts := docs(5)
	got, err := w.a.Embed(t.Context(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Vectors) != 5 || w.fake.Fake.Calls() != 3 || got.Usage.Calls != 3 || got.Usage.InputTokens == 0 {
		t.Fatalf("5 texts in batches of 2 = 3 calls: %d vectors, %d calls, usage %+v", len(got.Vectors), w.fake.Fake.Calls(), got.Usage)
	}
	for i, v := range got.Vectors {
		want := embedtest.Vector(w.a.Input(texts[i]), embedtest.Dims)
		if c := vector.Cosine(v, want); c < 0.9999 {
			t.Fatalf("vector %d is not its text's (cos %v)", i, c)
		}
	}
	if len(w.sink.recs) != 3 || w.sink.recs[0].Provider != "emb" || w.sink.recs[0].Failed {
		t.Fatalf("usage persisted per call: %+v", w.sink.recs)
	}
	if rec := <-sub.C; rec.Calls != 1 {
		t.Fatalf("usage published: %+v", rec)
	}
	if w.loc.calls != 3 {
		t.Fatalf("the managed endpoint is located per call (it can move after a restart): %d", w.loc.calls)
	}
}

func TestInputTemplatesAndBudget(t *testing.T) {
	w := newWorld(t, map[string]any{"embed.max_input_tokens": 16}) // 16 tokens × 5 chars = 80 characters
	q := w.a.Input(engine.Text{Purpose: engine.PurposeQuery, Body: "  réunion\n\tbudget  "})
	if q != "task: search result | query: réunion budget" {
		t.Fatalf("query template, whitespace collapsed: %q", q)
	}
	d := w.a.Input(engine.Text{Purpose: engine.PurposeDocument, Body: "hello"})
	if d != "title: none | text: hello" {
		t.Fatalf("document template with the empty title: %q", d)
	}
	long := engine.Text{Purpose: engine.PurposeDocument, Title: strings.Repeat("t", 30), Body: strings.Repeat("b", 200)}
	in := w.a.Input(long)
	if want := "title: " + strings.Repeat("t", 30) + " | text: " + strings.Repeat("b", 50); in != want {
		t.Fatalf("title first, then the body up to the budget:\n%q\n%q", in, want)
	}
	res, err := w.a.Embed(t.Context(), []engine.Text{long, {Purpose: engine.PurposeQuery, Body: "short"}})
	if err != nil || res.Truncated != 1 {
		t.Fatalf("one text was cut: %+v %v", res.Truncated, err)
	}
	if !strings.HasPrefix(w.a.Space(), "openai_embeddings/embeddinggemma-2/768/") || w.a.Dimensions() != 768 {
		t.Fatalf("space %q", w.a.Space())
	}
}

// The vector space excludes the record id and label (a rename keeps
// vectors) and includes the model, dimensions and task templates (each
// starts a new space).
func TestSpaceExcludesIdentityAndTracksModelTemplates(t *testing.T) {
	cfg := configtest.New(t, nil)
	fake := embedtest.New(t, &embedtest.Fake{})
	loc := embedtest.Locator{URL: fake.URL}
	space := func(p engine.Provider) string {
		t.Helper()
		a, err := New(p, engine.Deps{Config: cfg, Locator: loc})
		if err != nil {
			t.Fatal(err)
		}
		return a.Space()
	}
	p := preset(t, cfg)
	base := space(p)
	renamed := p
	renamed.ID, renamed.Label = "another-id", "Another label"
	if got := space(renamed); got != base {
		t.Fatalf("a record id or label change must keep the vector space: %q -> %q", base, got)
	}
	model := p
	model.Model = "embeddinggemma-3"
	if got := space(model); got == base {
		t.Fatal("a model change must start a new vector space")
	}
	tmpl := p
	spec := *p.Embedding
	spec.DocumentTemplate = "passage: {text}"
	tmpl.Embedding = &spec
	if got := space(tmpl); got == base {
		t.Fatal("a task template change must start a new vector space")
	}
	cfgDim := configtest.New(t, map[string]any{"embed.dimensions": 256})
	a, err := New(p, engine.Deps{Config: cfgDim, Locator: loc})
	if err != nil {
		t.Fatal(err)
	}
	if a.Space() == base {
		t.Fatal("a dimensions change must start a new vector space")
	}
}

func TestMatryoshkaTruncationIsUnitLength(t *testing.T) {
	w := newWorld(t, map[string]any{"embed.dimensions": 256})
	res, err := w.a.Embed(t.Context(), docs(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.Vectors {
		if len(v) != 256 || math.Abs(vector.Norm(v)-1) > 1e-5 {
			t.Fatalf("cut to 256 and re-normalised: %d %v", len(v), vector.Norm(v))
		}
	}
	if !strings.Contains(w.a.Space(), "/256/") {
		t.Fatalf("the length is part of the space: %s", w.a.Space())
	}
}

func TestBadSettingsAreRefusedAtBuild(t *testing.T) {
	for name, over := range map[string]map[string]any{
		"dimensions not a model length": {"embed.dimensions": 300},
		"budget above the context":      {"embed.max_input_tokens": 4096},
	} {
		cfg := configtest.New(t, over)
		if _, err := New(preset(t, cfg), engine.Deps{Config: cfg}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	cfg := configtest.New(t, nil)
	p := preset(t, cfg)
	p.Protocol = "other"
	if _, err := New(p, engine.Deps{Config: cfg}); err == nil {
		t.Fatal("another protocol is refused")
	}
}

func TestCorruptVectorsAreRejectedAtTheBoundary(t *testing.T) {
	for _, mode := range []embedtest.Mode{embedtest.HalfNorm, embedtest.NullValue, embedtest.Short, embedtest.BadIndex, embedtest.NotJSON} {
		w := newWorld(t, nil)
		w.fake.Fake.SetMode(mode)
		res, err := w.a.Embed(t.Context(), docs(2))
		if !errors.Is(err, ErrContract) || res.Vectors != nil {
			t.Errorf("%s: %v (vectors %d)", mode, err, len(res.Vectors))
		}
		if len(w.sink.recs) != 1 || !w.sink.recs[0].Failed {
			t.Errorf("%s: the failed call is recorded: %+v", mode, w.sink.recs)
		}
	}
}

func TestDecodeRejectsOutOfRangeIndex(t *testing.T) {
	spec := &engine.EmbeddingSpec{Dimensions: 1, NormTolerance: 0.01}
	for _, index := range []string{"2147483648", "9223372036854775808", "-1", "1.5", `"0"`} {
		raw := []byte(`{"data":[{"index":` + index + `,"embedding":[1]}],"usage":{}}`)
		vectors, tokens, err := decode(raw, 1, spec, 1)
		if !errors.Is(err, ErrContract) {
			t.Errorf("index %s: want ErrContract, got %v", index, err)
		}
		if vectors != nil || tokens != 0 {
			t.Errorf("index %s: accepted %d vectors and %d tokens", index, len(vectors), tokens)
		}
	}
}

func TestServerErrorsAreClassified(t *testing.T) {
	cases := map[int]error{
		http.StatusBadRequest:          ErrRejected,
		http.StatusForbidden:           ErrAuth,
		http.StatusRequestTimeout:      ErrTransient,
		http.StatusTooManyRequests:     ErrTransient,
		http.StatusBadGateway:          ErrTransient,
		http.StatusServiceUnavailable:  ErrTransient,
		http.StatusGatewayTimeout:      ErrTransient,
		http.StatusInternalServerError: ErrServer,
	}
	for status, want := range cases {
		w := newWorld(t, nil)
		w.fake.Fake.SetStatus(status)
		if _, err := w.a.Embed(t.Context(), docs(1)); !errors.Is(err, want) {
			t.Errorf("%d: %v", status, err)
		}
	}
	// The contrast for the managed 500 above: a remote provider's 500 is
	// about the provider, not one input, so it is provider-wide.
	cfg := configtest.New(t, nil)
	remote := embedtest.New(t, &embedtest.Fake{})
	p := preset(t, cfg)
	p.Runtime, p.BaseURL, p.APIKeySecret, p.ContextTokens = "", remote.URL, "", 2048
	a, err := New(p, engine.Deps{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	remote.Fake.SetStatus(http.StatusInternalServerError)
	if _, err := a.Embed(t.Context(), docs(1)); !errors.Is(err, ErrTransient) || !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("a remote 500 is provider-wide: %v", err)
	}
	if remote.Fake.Calls() != 1 {
		t.Fatalf("a remote 500 is not retried or fanned out: %d calls", remote.Fake.Calls())
	}
	w := newWorld(t, nil)
	w.loc.ep.Key = "wrong"
	if _, err := w.a.Embed(t.Context(), docs(1)); !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("a rejected key makes the provider unavailable: %v", err)
	}
	if _, err := w.a.Embed(t.Context(), []engine.Text{{Purpose: "summary", Body: "x"}}); !errors.Is(err, ErrInput) {
		t.Fatalf("unknown purpose: %v", err)
	}
}

func TestLoadingServerIsRetriedThroughHTTPX(t *testing.T) {
	w := newWorld(t, nil)
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			http.Error(rw, "Loading model", http.StatusServiceUnavailable)
			return
		}
		w.fake.Fake.ServeHTTP(rw, r)
	}))
	defer srv.Close()
	w.loc.ep.URL = srv.URL
	if _, err := w.a.Embed(t.Context(), docs(1)); err != nil || n != 2 {
		t.Fatalf("503 while loading is retried (http.retry.statuses): %v after %d", err, n)
	}
}

// The embed adapter installs the record's retry override exactly as the
// decision and generate adapters do: without it, 529 is final and 503
// always uses the global attempt count.
func TestProviderRetryOverrides(t *testing.T) {
	cfg := configtest.New(t, nil)
	clk := clocktest.New(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	remote := func(url string, p engine.Provider) *Adapter {
		t.Helper()
		p.Runtime, p.BaseURL, p.APIKeySecret, p.ContextTokens = "", url, "", 2048
		a, err := New(p, engine.Deps{Config: cfg}, httpx.WithClock(clk))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	fake := embedtest.New(t, &embedtest.Fake{})
	var mu sync.Mutex
	hits := 0
	overloaded := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		first := hits == 1
		mu.Unlock()
		if first {
			http.Error(rw, `{"error":{"message":"overloaded"}}`, 529)
			return
		}
		fake.Fake.ServeHTTP(rw, r)
	}))
	defer overloaded.Close()
	p := preset(t, cfg)
	p.Retry.Statuses = []int{429, 529}
	if _, err := remote(overloaded.URL, p).Embed(t.Context(), docs(1)); err != nil || hits != 2 {
		t.Fatalf("the record's 529 override is retried: %v after %d calls", err, hits)
	}

	limited := 0
	unavailable := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		limited++
		mu.Unlock()
		http.Error(rw, `{"error":{"message":"unavailable"}}`, http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	one := 1
	p = preset(t, cfg)
	p.Retry.MaxAttempts = &one
	if _, err := remote(unavailable.URL, p).Embed(t.Context(), docs(1)); !errors.Is(err, ErrTransient) || limited != 1 {
		t.Fatalf("the record's max_attempts override: %v after %d calls", err, limited)
	}
}

func TestUnreachableOrUnmanagedServerIsUnavailable(t *testing.T) {
	w := newWorld(t, map[string]any{"http.retry.max_attempts": 1})
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close() // a port nothing listens on any more
	w.loc.ep.URL = gone.URL
	if _, err := w.a.Embed(t.Context(), docs(1)); !errors.Is(err, ErrNoServer) || !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("nothing listening: %v", err)
	}
	w.loc.err = fmt.Errorf("not installed (%w)", engine.ErrUnavailable)
	if _, err := w.a.Embed(t.Context(), docs(1)); !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("the locator's error passes through: %v", err)
	}
	if _, err := New(preset(t, w.cfg), engine.Deps{Config: w.cfg}); !errors.Is(err, ErrNoServer) {
		t.Fatalf("a managed provider without a locator: %v", err)
	}
}

// Review 2b #2 (failed first: ErrUnavailable, the index run stopped): a
// runtime that died between calls is reported to the Locator, which hands
// out the restarted runtime; the batch goes there once more.
func TestDeadRuntimeIsReportedAndTheBatchSentToItsRestart(t *testing.T) {
	w := newWorld(t, map[string]any{"http.retry.max_attempts": 1})
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	restarted := w.loc.ep
	w.loc.ep.URL, w.loc.next = gone.URL, &restarted
	if _, err := w.a.Embed(t.Context(), docs(1)); err != nil {
		t.Fatalf("served by the restarted runtime: %v", err)
	}
	if len(w.loc.failed) != 1 || w.loc.failed[0].URL != gone.URL || w.loc.calls != 2 {
		t.Fatalf("the dead endpoint is reported once and located again: %+v (%d locates)", w.loc.failed, w.loc.calls)
	}
}

// Review 2b #8: the model file's revision is part of the vector space.
func TestModelRevisionIsPartOfTheSpace(t *testing.T) {
	w := newWorld(t, nil)
	if !strings.HasSuffix(w.a.Space(), "/"+embedtest.Revision[:12]) {
		t.Fatalf("space %q", w.a.Space())
	}
}

func TestExternalProviderUsesItsURLAndSecret(t *testing.T) {
	fake := embedtest.New(t, &embedtest.Fake{Key: "secret-1"})
	cfg := configtest.New(t, nil)
	p := preset(t, cfg)
	p.Runtime, p.BaseURL, p.APIKeySecret, p.ContextTokens = "", fake.URL, "EMB_KEY", 2048
	sec, _ := secrets.Open(cfg, []string{"MAILSENSE_SECRET_EMB_KEY=secret-1"}, nil, nil)
	a, err := New(p, engine.Deps{Config: cfg, Secrets: sec})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Embed(t.Context(), docs(1)); err != nil {
		t.Fatal(err)
	}
	empty, _ := secrets.Open(cfg, nil, nil, nil)
	b, _ := New(p, engine.Deps{Config: cfg, Secrets: empty})
	if _, err := b.Embed(t.Context(), docs(1)); !errors.Is(err, ErrNoKey) {
		t.Fatalf("missing key: %v", err)
	}
	p.APIKeySecret = ""
	fake.Fake.Key = ""
	c, _ := New(p, engine.Deps{Config: cfg, Secrets: empty})
	if _, err := c.Embed(t.Context(), docs(1)); err != nil {
		t.Fatalf("a keyless local server: %v", err)
	}
}

func TestCancelledEmbedIsNotRecordedAsAFailure(t *testing.T) {
	w := newWorld(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.a.Embed(ctx, docs(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if len(w.sink.recs) != 0 {
		t.Fatalf("a cancelled call says nothing about the provider: %+v", w.sink.recs)
	}
}

// Found by the local real-model run: 2,560 characters of one mail made
// 2,089 tokens, over llama-server's 2,048-token slot, and the whole batch
// failed. An input is now never longer in bytes than the context minus the
// reserved tokens (a byte-fallback tokenizer makes at most one token per
// byte).
func TestInputNeverExceedsTheServerContextInBytes(t *testing.T) {
	w := newWorld(t, nil)
	limit := 2048 - 8
	for name, body := range map[string]string{
		"dense ascii": strings.Repeat("a1/b2?c3=", 400),
		"multi-byte":  strings.Repeat("日本語のメール", 500),
		"emoji":       strings.Repeat("🙂", 3000),
	} {
		in := w.a.Input(engine.Text{Purpose: engine.PurposeDocument, Title: "Subject", Body: body})
		if len(in) > limit || !utf8.ValidString(in) || !strings.HasPrefix(in, "title: Subject | text: ") {
			t.Errorf("%s: %d bytes (limit %d), valid %v", name, len(in), limit, utf8.ValidString(in))
		}
		q := w.a.Input(engine.Text{Purpose: engine.PurposeQuery, Body: body})
		if len(q) > limit || !utf8.ValidString(q) {
			t.Errorf("%s query: %d bytes", name, len(q))
		}
	}
	w.fake.Fake.MaxInputBytes = limit
	if _, err := w.a.Embed(t.Context(), []engine.Text{{Purpose: engine.PurposeDocument, Body: strings.Repeat("日本", 3000)}}); err != nil {
		t.Fatalf("a server with a %d-byte context accepts it: %v", limit, err)
	}
	if got := headBytes("aé", 2); got != "a" {
		t.Fatalf("cut on a character boundary: %q", got)
	}
}

// Rules review 2b §8: the source keeps one embedder while nothing changed
// and builds a new one after a setting or the providers file changed; its
// status shows the provider's health.
func TestSourceRebuildsOnlyAfterAChange(t *testing.T) {
	cfg := configtest.New(t, nil)
	fake := embedtest.New(t, &embedtest.Fake{})
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	p := preset(t, cfg)
	install(t, cfg, p)
	src := NewSource(engine.Deps{Config: cfg, Secrets: sec, Locator: embedtest.Locator{URL: fake.URL}})
	a, err := src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := src.Embedder(); b != a {
		t.Fatal("unchanged: the same embedder (one HTTP client)")
	}
	if err := cfg.Set("embed.dimensions", 256); err != nil {
		t.Fatal(err)
	}
	c, err := src.Embedder()
	if err != nil || c == a || c.Dimensions() != 256 {
		t.Fatalf("a changed setting builds a new one: %v", err)
	}
	p.Label = "Renamed label"
	install(t, cfg, p)
	if d, _ := src.Embedder(); d == c {
		t.Fatal("a changed providers file builds a new one")
	}
	if _, err := c.Embed(t.Context(), docs(1)); err != nil {
		t.Fatal(err)
	}
	if st, err := src.Status(); err != nil || st.Provider != "emb" || st.Calls != 1 || !st.Healthy {
		t.Fatalf("status: %+v %v", st, err)
	}
}
