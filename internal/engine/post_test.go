package engine_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

// fakeSecrets is a read-only secret store.
type fakeSecrets map[string]string

func (f fakeSecrets) Get(name string) (string, error) {
	if v, ok := f[name]; ok {
		return v, nil
	}
	return "", secrets.ErrNotFound
}
func (fakeSecrets) Set(string, string) error { return secrets.ErrReadOnly }
func (fakeSecrets) Delete(string) error      { return secrets.ErrReadOnly }
func (fakeSecrets) List() ([]string, error)  { return nil, nil }
func (fakeSecrets) Kind() string             { return "fake" }
func (fakeSecrets) ReadOnly() bool           { return true }

// scriptedServer answers every request with one status and body, recording
// each request body so a test can assert against the estimate of the exact
// request an adapter sent.
type scriptedServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
	bodies [][]byte
}

func newScriptedServer(t *testing.T, status int, body string) *scriptedServer {
	t.Helper()
	s := &scriptedServer{status: status, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, raw)
		s.mu.Unlock()
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *scriptedServer) request(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

func price(v float64) *float64 { return &v }

func attempt(n int) *int { return &n }

const (
	priceInput  = 5.0 // USD per million input tokens
	priceOutput = 7.0 // USD per million output tokens
	reported    = int64(100)
)

func testDeps(t *testing.T) engine.Deps {
	t.Helper()
	return engine.Deps{
		Config:  configtest.New(t, map[string]any{"embed.dimensions": 4}),
		Secrets: fakeSecrets{"KEY": "k"},
		Clock:   clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)),
	}
}

func decisionRecord(url string) engine.Provider {
	return engine.Provider{ID: "dec", Protocol: engine.ProtocolOpenAIDecisions, BaseURL: url, Model: "m",
		APIKeySecret: "KEY", MaxQuestions: 16, ContextTokens: 100000, CharsPerToken: 4,
		ReserveTokensPerByte: 1, ProbabilityTolerance: 0.02,
		PriceInput: price(priceInput), PriceOutput: price(priceOutput), Retry: engine.Retry{MaxAttempts: attempt(1)}}
}

func generateRecord(url string) engine.Provider {
	return engine.Provider{ID: "gen", Protocol: engine.ProtocolOpenAIChat, BaseURL: url, Model: "m",
		APIKeySecret: "KEY", ContextTokens: 100000, CharsPerToken: 4, ReserveTokensPerByte: 1, ReserveOutputTokens: 16,
		PriceInput: price(priceInput), PriceOutput: price(priceOutput), Retry: engine.Retry{MaxAttempts: attempt(1)}}
}

func embedRecord(url string) engine.Provider {
	return engine.Provider{ID: "emb", Protocol: engine.ProtocolOpenAIEmbeddings, BaseURL: url, Model: "emb",
		CharsPerToken: 4, ContextTokens: 2048,
		PriceInput: price(priceInput), PriceOutput: price(priceOutput), Retry: engine.Retry{MaxAttempts: attempt(1)},
		Embedding: &engine.EmbeddingSpec{Dimensions: 4, QueryTemplate: "q: {text}", DocumentTemplate: "d: {text}",
			EmptyTitle: "none", NormTolerance: 0.01, MaxBatch: 16}}
}

func runDecision(t *testing.T, deps engine.Deps, p engine.Provider) (engine.Usage, error) {
	t.Helper()
	a, err := decision.New(p, deps)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Decide(t.Context(), "state", []engine.Question{{Name: "q", Type: engine.Noul, Instructions: "Is it?"}})
	return res.Usage, err
}

func runGenerate(t *testing.T, deps engine.Deps, p engine.Provider) (engine.Usage, error) {
	t.Helper()
	a, err := generate.New(p, deps)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := a.Generate(t.Context(), "system", "user")
	return gen.Usage, err
}

func runEmbed(t *testing.T, deps engine.Deps, p engine.Provider) (engine.Usage, error) {
	t.Helper()
	a, err := embed.New(p, deps)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Embed(t.Context(), []engine.Text{{Purpose: engine.PurposeDocument, Title: "t", Body: "hello"}})
	return res.Usage, err
}

// TestAdaptersPriceReportedUsageIdentically is the cross-adapter guard: for
// the same reported usage (100 input tokens, no output tokens) every adapter
// accounts exactly what the one shared estimator prices, so a change to the
// rule cannot leave one adapter behind.
func TestAdaptersPriceReportedUsageIdentically(t *testing.T) {
	deps := testDeps(t)
	cases := []struct {
		name   string
		body   string
		record func(string) engine.Provider
		run    func(*testing.T, engine.Deps, engine.Provider) (engine.Usage, error)
	}{
		{"decider", `{"model":"m","answers":[{"type":"predicate","probability":0.8,"confidence":0.5}],
			"usage":{"input_tokens":100,"output_tokens":0}}`, decisionRecord, runDecision},
		{"generator", `{"model":"m","choices":[{"message":{"content":"ok"}}],
			"usage":{"prompt_tokens":100,"completion_tokens":0}}`, generateRecord, runGenerate},
		{"embedder", `{"data":[{"index":0,"embedding":[1,0,0,0]}],"usage":{"prompt_tokens":100}}`, embedRecord, runEmbed},
	}
	var estimates []float64
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newScriptedServer(t, http.StatusOK, tc.body)
			p := tc.record(srv.URL)
			u, err := tc.run(t, deps, p)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if u.InputTokens != reported || u.OutputTokens != 0 {
				t.Fatalf("usage %+v, want %d input and no output", u, reported)
			}
			if want := p.Cost(reported, 0); u.EstimatedUSD != want {
				t.Fatalf("estimate %v, want the shared price %v", u.EstimatedUSD, want)
			}
			estimates = append(estimates, u.EstimatedUSD)
		})
	}
	for i := 1; i < len(estimates); i++ {
		if estimates[i] != estimates[0] {
			t.Fatalf("the same reported usage is priced differently across adapters: %v", estimates)
		}
	}
}

// TestNon2xxKeepsEachAdapterSentinel pins the error contract each adapter
// already published: the shared POST step classifies through the adapter's
// policy and never rewrites its sentinel.
func TestNon2xxKeepsEachAdapterSentinel(t *testing.T) {
	deps := testDeps(t)
	const body = `{"error":{"message":"failed"}}`
	cases := []struct {
		name                    string
		status                  int
		decider, generator, emb error
	}{
		{"400 rejected", 400, decision.ErrRejected, engine.ErrRejected, embed.ErrRejected},
		{"401 bad key", 401, decision.ErrAuth, engine.ErrAuth, embed.ErrAuth},
		{"408 timeout", 408, decision.ErrTransient, engine.ErrTransient, embed.ErrTransient},
		{"429 rate limit", 429, decision.ErrTransient, engine.ErrTransient, embed.ErrTransient},
		{"500 server", 500, decision.ErrTransient, engine.ErrTransient, embed.ErrTransient},
		{"503 unavailable", 503, decision.ErrTransient, engine.ErrTransient, embed.ErrTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newScriptedServer(t, tc.status, body)
			if _, err := runDecision(t, deps, decisionRecord(srv.URL)); !errors.Is(err, tc.decider) {
				t.Fatalf("decider: want %v, got %v", tc.decider, err)
			}
			if _, err := runGenerate(t, deps, generateRecord(srv.URL)); !errors.Is(err, tc.generator) {
				t.Fatalf("generator: want %v, got %v", tc.generator, err)
			}
			if _, err := runEmbed(t, deps, embedRecord(srv.URL)); !errors.Is(err, tc.emb) {
				t.Fatalf("embedder: want %v, got %v", tc.emb, err)
			}
		})
	}
}

// TestFailedCallKeepsTheBilledEstimateContract pins the usage contract of a
// provider-wide 500: the chat adapters bill the request at its own input
// estimate (max_usd must hold), while the embedder reports only what the
// server told it (none, so zero). The shared estimator owns the first rule;
// the embedder's record fields are unchanged.
func TestFailedCallKeepsTheBilledEstimateContract(t *testing.T) {
	deps := testDeps(t)
	srv := newScriptedServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)

	decider := decisionRecord(srv.URL)
	if u, err := runDecision(t, deps, decider); !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("decider: %v", err)
	} else if tokens := decider.Tokens(srv.request(0)); u.InputTokens != tokens || u.EstimatedUSD != decider.Cost(tokens, 0) {
		t.Fatalf("decider bills its request estimate: usage %+v, request %d tokens", u, tokens)
	}

	generator := generateRecord(srv.URL)
	if u, err := runGenerate(t, deps, generator); !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("generator: %v", err)
	} else if tokens := generator.Tokens(srv.request(1)); u.InputTokens != tokens || u.EstimatedUSD != generator.Cost(tokens, 0) {
		t.Fatalf("generator bills its request estimate: usage %+v, request %d tokens", u, tokens)
	}

	embedder := embedRecord(srv.URL)
	if u, err := runEmbed(t, deps, embedder); !errors.Is(err, embed.ErrTransient) {
		t.Fatalf("embedder: %v", err)
	} else if u.InputTokens != 0 || u.EstimatedUSD != 0 {
		t.Fatalf("the embedder reports only server tokens: usage %+v", u)
	}
}

func TestPostJSONOwnsTheRequestShape(t *testing.T) {
	cfg := configtest.New(t, nil)
	p := engine.Provider{ID: "p", Protocol: engine.ProtocolSystemOne}
	var contentType, accept, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType, accept, auth = r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	ok := engine.PostPolicy{
		Input:     func(err error) error { t.Fatalf("input: %v", err); return err },
		Transport: func(err error) error { t.Fatalf("transport: %v", err); return err },
		Read:      func(err error) error { t.Fatalf("read: %v", err); return err },
	}
	status, body, err := engine.PostJSON(t.Context(), cfg, srv.Client(), &p,
		engine.PostSpec{URL: srv.URL, Key: "secret", Body: []byte(`{"a":1}`), Accept: true}, ok)
	if err != nil || status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("post: %d %q %v", status, body, err)
	}
	if contentType != "application/json" || accept != "application/json" || auth != "Bearer secret" {
		t.Fatalf("headers: content-type %q accept %q auth %q", contentType, accept, auth)
	}
	if _, _, err := engine.PostJSON(t.Context(), cfg, srv.Client(), &p,
		engine.PostSpec{URL: srv.URL, Body: []byte(`{}`)}, ok); err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		t.Fatalf("a caller that does not ask for JSON must not send Accept: %q", accept)
	}
}

func TestPostJSONClassifiesThroughTheAdaptersPolicy(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"http.max_response_mb": 1})
	p := engine.Provider{ID: "p", Protocol: engine.ProtocolSystemOne}
	policy := func(kind string) engine.PostPolicy {
		classify := func(err error) error { return errors.Join(engine.ErrHTTP, errors.New(kind), err) }
		return engine.PostPolicy{Input: classify, Transport: classify, Read: classify}
	}

	// Input: a URL the request builder refuses.
	_, _, err := engine.PostJSON(t.Context(), cfg, http.DefaultClient, &p,
		engine.PostSpec{URL: "http://[::1"}, policy("input"))
	if !errors.Is(err, engine.ErrHTTP) || !strings.Contains(err.Error(), "input") {
		t.Fatalf("input: %v", err)
	}

	// Transport: a server that is not there.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	_, _, err = engine.PostJSON(t.Context(), cfg, http.DefaultClient, &p,
		engine.PostSpec{URL: url, Body: []byte(`{}`)}, policy("transport"))
	if !errors.Is(err, engine.ErrHTTP) || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("transport: %v", err)
	}

	// Read: a body over http.max_response_mb.
	big := newScriptedServer(t, http.StatusOK, strings.Repeat("x", 1<<20+1))
	status, body, err := engine.PostJSON(t.Context(), cfg, big.Client(), &p,
		engine.PostSpec{URL: big.URL, Body: []byte(`{}`)}, policy("read"))
	if !errors.Is(err, engine.ErrHTTP) || !strings.Contains(err.Error(), "read") || status != http.StatusOK || body != nil {
		t.Fatalf("read: %d %q %v", status, body, err)
	}
}

func TestPostJSONReturnsTheContextErrorBeforeThePolicy(t *testing.T) {
	cfg := configtest.New(t, nil)
	p := engine.Provider{ID: "p", Protocol: engine.ProtocolSystemOne}
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-started; cancel() }()
	classified := false
	_, _, err := engine.PostJSON(ctx, cfg, srv.Client(), &p, engine.PostSpec{URL: srv.URL},
		engine.PostPolicy{
			Input:     func(err error) error { classified = true; return err },
			Transport: func(err error) error { classified = true; return err },
			Read:      func(err error) error { classified = true; return err },
		})
	if !errors.Is(err, context.Canceled) || classified {
		t.Fatalf("a cancelled exchange keeps the context's class: %v (classified %v)", err, classified)
	}
}

func TestSentinelPolicyWrapsEachFailure(t *testing.T) {
	base := errors.New("base")
	pol := engine.SentinelPolicy("prov", engine.ErrInput, engine.ErrHTTP)
	in, tr, rd := pol.Input(base), pol.Transport(base), pol.Read(base)
	if !errors.Is(in, engine.ErrInput) || in.Error() != "engine: invalid request: base" {
		t.Fatalf("input: %v", in)
	}
	if !errors.Is(tr, engine.ErrHTTP) || !strings.Contains(tr.Error(), "prov") || !errors.Is(tr, base) {
		t.Fatalf("transport: %v", tr)
	}
	if !errors.Is(rd, engine.ErrHTTP) || !strings.Contains(rd.Error(), "prov") || !strings.Contains(rd.Error(), "read response") {
		t.Fatalf("read: %v", rd)
	}
}

func TestAccountedUsage(t *testing.T) {
	p := engine.Provider{CharsPerToken: 4, PriceInput: price(priceInput), PriceOutput: price(priceOutput)}
	body := []byte("12345678") // 8 code points / 4 = 2 tokens
	cases := []struct {
		name  string
		usage engine.Usage
		body  []byte
		bill  bool
		want  engine.Usage
	}{
		{"billed at the request estimate", engine.Usage{Calls: 1}, body, true,
			engine.Usage{Calls: 1, InputTokens: 2, EstimatedUSD: 2 * priceInput / 1e6}},
		{"recorded tokens stand", engine.Usage{Calls: 1, InputTokens: 10, OutputTokens: 2}, body, false,
			engine.Usage{Calls: 1, InputTokens: 10, OutputTokens: 2,
				EstimatedUSD: 10*priceInput/1e6 + 2*priceOutput/1e6}},
		{"nothing recorded is zero", engine.Usage{Calls: 1}, nil, false, engine.Usage{Calls: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.AccountedUsage(tc.usage, tc.body, tc.bill); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	unpriced := engine.Provider{CharsPerToken: 4}
	if got := unpriced.AccountedUsage(engine.Usage{InputTokens: 100}, nil, false); got.EstimatedUSD != 0 {
		t.Fatalf("an unknown price estimates 0: %+v", got)
	}
}

func TestContractErrorKeepsOneFormat(t *testing.T) {
	sentinel := errors.New("contract")
	err := engine.ContractError(sentinel, "response", "got %d answers", 3)
	if !errors.Is(err, sentinel) || err.Error() != "contract: response: got 3 answers" {
		t.Fatalf("contract error: %v", err)
	}
}
