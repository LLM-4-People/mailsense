package decision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/httpx"
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
func (fakeSecrets) Set(string, string) error  { return secrets.ErrReadOnly }
func (fakeSecrets) Delete(string) error       { return secrets.ErrReadOnly }
func (f fakeSecrets) List() ([]string, error) { return nil, nil }
func (fakeSecrets) Kind() string              { return "fake" }
func (fakeSecrets) ReadOnly() bool            { return true }

// sink records usage.
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

// server answers each request with the next reply (status, body); the
// last reply repeats. It records request bodies and auth headers.
type server struct {
	*httptest.Server
	mu      sync.Mutex
	replies []reply
	bodies  []map[string]any
	auth    []string
	paths   []string
}

type reply struct {
	status int
	body   string
}

func newServer(t *testing.T, replies ...reply) *server {
	s := &server{replies: replies}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var doc map[string]any
		_ = json.Unmarshal(b, &doc)
		s.mu.Lock()
		s.bodies = append(s.bodies, doc)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.paths = append(s.paths, r.URL.Path)
		rep := s.replies[min(len(s.bodies), len(s.replies))-1]
		s.mu.Unlock()
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func price(v float64) *float64 { return &v }

// provider returns a systemone record aimed at srv.
func provider(srv *server, protocol string) engine.Provider {
	return engine.Provider{ID: "fake", Protocol: protocol, BaseURL: srv.URL + "/v1", Path: "/systemone",
		Model: "m-1", APIKeySecret: "FAKE_KEY", MaxQuestions: 16, ContextTokens: 100000, CharsPerToken: 3, ProbabilityTolerance: 0.02, ReserveTokensPerByte: 1,
		PriceInput: price(0.05), PriceOutput: price(0),
		NoulFold: "{instructions}\nYes: {true}\nNo: {false}"}
}

type harness struct {
	ad   *Adapter
	sink *sink
	bus  *events.Bus
	clk  *clocktest.Fake
}

func newAdapter(t *testing.T, p engine.Provider) harness {
	t.Helper()
	return newAdapterWith(t, p)
}

func newAdapterWith(t *testing.T, p engine.Provider, opts ...httpx.Option) harness {
	t.Helper()
	h := harness{sink: &sink{}, bus: events.New(), clk: clocktest.New(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))}
	t.Cleanup(h.bus.Close)
	ad, err := New(p, engine.Deps{Config: configtest.New(t, nil), Bus: h.bus, Secrets: fakeSecrets{"FAKE_KEY": "sk-test-0123456789abcdef"},
		Sink: h.sink, Clock: h.clk}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	h.ad = ad
	return h
}

func desc(s string) *string { return &s }

// taxonomy17 mimics the legacy set: 17 questions of the three types.
func taxonomy17() []engine.Question {
	var qs []engine.Question
	for i := range 17 {
		name := "q" + engine.LevelKey(i)
		switch i % 3 {
		case 0:
			qs = append(qs, engine.Question{Name: name, Type: engine.Noul, Instructions: "Is it?", True: "yes", False: "no"})
		case 1:
			qs = append(qs, engine.Question{Name: name, Type: engine.Choice, Instructions: "Which?",
				Options: []engine.Option{{Name: "b", Description: desc("bee")}, {Name: "a"}}})
		default:
			qs = append(qs, engine.Question{Name: name, Type: engine.Score, Instructions: "How much?", Levels: []string{"none", "some", "lots"}})
		}
	}
	return qs
}

// systemoneAnswers is a valid systemone body for qs.
func systemoneAnswers(qs []engine.Question, inputTokens int) string {
	answers := map[string]any{}
	for _, q := range qs {
		switch q.Type {
		case engine.Noul:
			answers[q.Name] = map[string]any{"type": "noul", "noul": 0.92}
		case engine.Choice:
			answers[q.Name] = map[string]any{"type": "choice", "choice": "a", "probabilities": map[string]any{"a": 0.7, "b": 0.3}, "confidence": 0.4}
		case engine.Score:
			answers[q.Name] = map[string]any{"type": "score", "score": 1.86, "legend": map[string]any{"0": "none"},
				"probabilities": map[string]any{"0": 0, "1": 0.13, "2": 0.87}, "confidence": 0.79}
		}
	}
	b, _ := json.Marshal(map[string]any{"model": "m-1", "answers": answers, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0}})
	return string(b)
}

// chunkReplies answers each chunk of qs (in order) validly.
func chunkReplies(qs []engine.Question, n int) []reply {
	var out []reply
	for _, c := range chunks(qs, n) {
		out = append(out, reply{200, systemoneAnswers(c, 477)})
	}
	return out
}
