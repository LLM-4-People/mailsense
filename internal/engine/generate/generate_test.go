package generate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

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

func (s *sink) last() engine.UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recs) == 0 {
		return engine.UsageRecord{}
	}
	return s.recs[len(s.recs)-1]
}

type server struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
	bodies []map[string]any
	auth   []string
	paths  []string
}

func newServer(t *testing.T, status int, body string) *server {
	s := &server{status: status, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		s.mu.Lock()
		s.bodies = append(s.bodies, doc)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *server) request(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

const goodBody = `{"model":"gemma-4-31b","choices":[{"message":{"role":"assistant","content":"{\"answers\":[]}"}}],
	"usage":{"prompt_tokens":26,"completion_tokens":6}}`

func price(v float64) *float64 { return &v }

// preset is a complete generator record aimed at srv.
func preset(t *testing.T, srv *server) engine.Provider {
	t.Helper()
	return engine.Provider{ID: "gen", Protocol: Protocol, BaseURL: srv.URL + "/v1", Path: "/chat/completions",
		Model: "gemma-4-31b", APIKeySecret: "GEN_KEY", ContextTokens: 262128, CharsPerToken: 3,
		ReserveTokensPerByte: 1, ReserveOutputTokens: 2048, ResponseFormat: engine.ResponseFormatJSON,
		PriceInput: price(0.144), PriceOutput: price(0.42)}
}

type harness struct {
	ad   *Adapter
	sink *sink
}

func newAdapter(t *testing.T, p engine.Provider, secrets secrets.Store) harness {
	t.Helper()
	h := harness{sink: &sink{}}
	ad, err := New(p, engine.Deps{Config: configtest.New(t, nil), Secrets: secrets, Sink: h.sink})
	if err != nil {
		t.Fatal(err)
	}
	h.ad = ad
	return h
}

func TestGenerateSendsTheOpenAIChatContract(t *testing.T) {
	srv := newServer(t, http.StatusOK, goodBody)
	h := newAdapter(t, preset(t, srv), fakeSecrets{"GEN_KEY": "nw-test-key"})
	est, err := h.ad.Plan("You label email.", "Questions: []\n\nMessage:\nSubject: x")
	if err != nil || est.Calls != 1 || est.InputTokens < 1 || est.EstimatedUSD <= 0 || !est.PriceKnown {
		t.Fatalf("plan %+v %v", est, err)
	}
	gen, err := h.ad.Generate(t.Context(), "You label email.", "Questions: []\n\nMessage:\nSubject: x")
	if err != nil || gen.Model != "gemma-4-31b" || !strings.Contains(gen.Content, "answers") {
		t.Fatalf("generate %+v %v", gen, err)
	}
	if gen.Usage.Calls != 1 || gen.Usage.InputTokens != 26 || gen.Usage.OutputTokens != 6 {
		t.Fatalf("usage %+v", gen.Usage)
	}
	if want := 26*0.144/1e6 + 6*0.42/1e6; gen.Usage.EstimatedUSD != want {
		t.Fatalf("usd %v, want %v", gen.Usage.EstimatedUSD, want)
	}
	body := srv.request(0)
	if body["model"] != "gemma-4-31b" || body["temperature"] != float64(0) || body["max_tokens"] != float64(2048) {
		t.Fatalf("request %v", body)
	}
	if rf, _ := body["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Fatalf("response_format %v", body["response_format"])
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["role"] != "user" {
		t.Fatalf("messages %v", msgs)
	}
	if srv.auth[0] != "Bearer nw-test-key" || srv.paths[0] != "/v1/chat/completions" {
		t.Fatalf("auth %q path %q", srv.auth[0], srv.paths[0])
	}
	rec := h.sink.last()
	if rec.Provider != "gen" || rec.Model != "gemma-4-31b" || rec.Failed || rec.InputTokens != 26 || rec.OutputTokens != 6 {
		t.Fatalf("record %+v", rec)
	}
}

func TestGenerateOmitsJSONModeWhenUnset(t *testing.T) {
	srv := newServer(t, http.StatusOK, goodBody)
	p := preset(t, srv)
	p.ResponseFormat = ""
	h := newAdapter(t, p, fakeSecrets{"GEN_KEY": "k"})
	if _, err := h.ad.Generate(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if _, has := srv.request(0)["response_format"]; has {
		t.Fatalf("response_format sent although unset: %v", srv.request(0))
	}
}

func TestGenerateProseOverridesTheRecordJSONMode(t *testing.T) {
	srv := newServer(t, http.StatusOK, `{"model":"gemma-4-31b","choices":[{"message":{"role":"assistant","content":"Nadia sent the invoice [1]."}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	h := newAdapter(t, preset(t, srv), fakeSecrets{"GEN_KEY": "k"})
	gen, err := h.ad.Generate(t.Context(), "You summarize one conversation.", "Conversation [1]: invoice",
		engine.GenerateOptions{Prose: true})
	if err != nil || gen.Content == "" {
		t.Fatalf("generate %+v %v", gen, err)
	}
	if _, has := srv.request(0)["response_format"]; has {
		t.Fatalf("a prose call must omit the record's JSON mode: %v", srv.request(0))
	}
}

func TestGenerateRefusesOversizedBeforeAnyCall(t *testing.T) {
	srv := newServer(t, http.StatusOK, goodBody)
	p := preset(t, srv)
	p.ContextTokens = 10
	h := newAdapter(t, p, fakeSecrets{"GEN_KEY": "k"})
	if _, err := h.ad.Plan("s", "u"); !errors.Is(err, engine.ErrContextBudget) {
		t.Fatalf("plan: %v", err)
	}
	if _, err := h.ad.Generate(t.Context(), "s", "u"); !errors.Is(err, engine.ErrContextBudget) || srv.hits() != 0 {
		t.Fatalf("generate: %v hits %d", err, srv.hits())
	}
}

func TestGenerateReservesBeforeSpending(t *testing.T) {
	srv := newServer(t, http.StatusOK, goodBody)
	p := preset(t, srv)
	h := newAdapter(t, p, fakeSecrets{"GEN_KEY": "k"})
	est, err := h.ad.Plan("s", "u")
	if err != nil {
		t.Fatal(err)
	}
	tight := engine.NewBudgetGuard(engine.Budget{MaxCalls: 1, MaxUSD: est.EstimatedUSD / 2})
	if _, err := h.ad.Generate(engine.WithReserver(t.Context(), tight), "s", "u"); !errors.Is(err, engine.ErrBudget) || srv.hits() != 0 {
		t.Fatalf("tight budget: %v hits %d", err, srv.hits())
	}
	if tight.Usage().Calls != 0 {
		t.Fatalf("a refused call was accounted: %+v", tight.Usage())
	}
	ok := engine.NewBudgetGuard(engine.Budget{MaxCalls: 1, MaxUSD: 1})
	if _, err := h.ad.Generate(engine.WithReserver(t.Context(), ok), "s", "u"); err != nil || srv.hits() != 1 {
		t.Fatalf("allowed call: %v hits %d", err, srv.hits())
	}
	if ok.Usage().Calls != 1 || ok.Usage().InputTokens != 26 || ok.Usage().OutputTokens != 6 {
		t.Fatalf("guard usage %+v", ok.Usage())
	}
}

func TestGenerateErrorClassesAndMalformedAnswers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"no credits", 402, `{"error":{"message":"Add credits"}}`, engine.ErrInsufficientCredits},
		{"bad key", 401, `{"error":{"message":"bad key"}}`, engine.ErrAuth},
		{"rejected", 400, `{"error":{"message":"bad request"}}`, engine.ErrRejected},
		{"no choices", 200, `{"model":"m","usage":{"prompt_tokens":1,"completion_tokens":1}}`, ErrContract},
		{"empty content", 200, `{"choices":[{"message":{"content":"  "}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, ErrContract},
		{"bad usage", 200, `{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":-1,"completion_tokens":1}}`, ErrContract},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(t, c.status, c.body)
			h := newAdapter(t, preset(t, srv), fakeSecrets{"GEN_KEY": "k"})
			_, err := h.ad.Generate(t.Context(), "s", "u")
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
			if c.status != http.StatusOK && !h.sink.last().Failed {
				t.Fatalf("a failed call was not recorded: %+v", h.sink.last())
			}
		})
	}
}

func TestGenerateEstimatesMissingUsage(t *testing.T) {
	srv := newServer(t, http.StatusOK, `{"model":"gemma-4-31b","choices":[{"message":{"content":"{\"answers\":[]}"}}]}`)
	h := newAdapter(t, preset(t, srv), fakeSecrets{"GEN_KEY": "k"})
	gen, err := h.ad.Generate(t.Context(), "s", "u")
	if err != nil || gen.Usage.InputTokens < 1 || gen.Usage.OutputTokens < 1 {
		t.Fatalf("estimate %+v %v", gen.Usage, err)
	}
	if rec := h.sink.last(); !rec.Estimated || rec.InputTokens != gen.Usage.InputTokens {
		t.Fatalf("record %+v", rec)
	}
}

func TestGenerateRefusesMissingKey(t *testing.T) {
	srv := newServer(t, http.StatusOK, goodBody)
	h := newAdapter(t, preset(t, srv), fakeSecrets{})
	if _, err := h.ad.Generate(t.Context(), "s", "u"); !errors.Is(err, engine.ErrNoKey) || srv.hits() != 0 {
		t.Fatalf("missing key: %v hits %d", err, srv.hits())
	}
}

func TestRegisterAddsTheProtocol(t *testing.T) {
	reg := engine.NewRegistry()
	Register(reg)
	if d := reg.Protocols(engine.KindGenerator); len(d) != 1 || d[0] != Protocol {
		t.Fatalf("protocols %v", d)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registering a generator protocol twice must panic")
		}
	}()
	Register(reg)
}
