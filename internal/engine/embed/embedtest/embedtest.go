// Package embedtest is a fake OpenAI-compatible embeddings server for
// tests (the adapter, the index, search and the sidecar's fake
// llama-server process). Its vectors are deterministic and lexical: the
// hashing trick over folded words, so texts that share words are similar
// and a search test can check precision without a real model.
package embedtest

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// Dims is the fake model's native length (a 768-dimensional retrieval model).
const Dims = 768

// templateWords are the task-template words every input carries; they are
// ignored so that only content makes texts similar.
var templateWords = map[string]bool{"task": true, "search": true, "result": true, "query": true, "title": true, "text": true, "none": true}

// Vector is the fake embedding of a text: each folded word of three or
// more characters adds ±1 at two hashed positions; unit length.
func Vector(text string, dims int) []float32 {
	v := make([]float32, dims)
	for _, w := range strings.FieldsFunc(mail.Fold(text), func(r rune) bool { return !mail.IsWordRune(r) }) {
		if len([]rune(w)) < 3 || templateWords[w] {
			continue
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(w))
		x := h.Sum64()
		for k := range 2 {
			bits := x >> (32 * k)
			sign := float32(1)
			if bits&(1<<31) != 0 {
				sign = -1
			}
			v[int(bits%uint64(dims))] += sign
		}
	}
	if vector.Norm(v) == 0 {
		v[0] = 1
	}
	out, _ := vector.Truncate(v, dims)
	return out
}

// Mode corrupts responses the way broken backends do.
type Mode string

// Modes.
const (
	Good      Mode = ""
	HalfNorm  Mode = "half-norm"  // every vector at length 0.5 (fp16 overflow-like)
	NullValue Mode = "null-value" // a null inside the vector
	Short     Mode = "short"      // one value missing
	BadIndex  Mode = "bad-index"  // two items with index 0
	NotJSON   Mode = "not-json"   // `nan` literals, as a C printf would emit
)

// Server is a running fake.
type Server struct {
	*httptest.Server
	Fake *Fake
}

// New starts a fake on a test server (closed with the test).
func New(t testing.TB, f *Fake) *Server {
	t.Helper()
	f.stop = make(chan struct{})
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(f.stop) }) // runs first: hanging handlers end
	return &Server{Server: s, Fake: f}
}

// Fake is the handler: /health and /v1/embeddings. Zero value: healthy,
// no key, 768 dimensions, good vectors.
type Fake struct {
	Key   string        // required bearer key ("" = none)
	Dims  int           // native length (0 = Dims)
	Delay time.Duration // per input (throughput tests)
	// Hang makes embeddings requests wait until the caller gives up (a
	// server that never answers).
	Hang bool
	// MaxInputBytes refuses an input longer than this, as llama-server
	// refuses one over its per-slot context with HTTP 500 (0 = no limit).
	MaxInputBytes int
	mu            sync.Mutex
	mode          Mode
	status        int // forced status for /v1/embeddings (0 = 200)
	loading       bool
	calls         atomic.Int64
	inputs        atomic.Int64
	stop          chan struct{} // closed when the test ends (nil: handlers run in a fake process)
}

// SetMode switches the corruption mode.
func (f *Fake) SetMode(m Mode) { f.mu.Lock(); f.mode = m; f.mu.Unlock() }

// SetStatus forces an HTTP status for embeddings requests (0 = normal).
func (f *Fake) SetStatus(s int) { f.mu.Lock(); f.status = s; f.mu.Unlock() }

// SetLoading makes /health answer 503 (the model is loading).
func (f *Fake) SetLoading(b bool) { f.mu.Lock(); f.loading = b; f.mu.Unlock() }

// Calls is the number of embeddings requests served.
func (f *Fake) Calls() int64 { return f.calls.Load() }

// Inputs is the number of texts embedded.
func (f *Fake) Inputs() int64 { return f.inputs.Load() }

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	mode, status, loading := f.mode, f.status, f.loading
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/health" && loading:
		http.Error(w, `{"error":{"code":503,"message":"Loading model"}}`, http.StatusServiceUnavailable)
	case r.URL.Path == "/health":
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	case r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost:
		http.NotFound(w, r)
	case f.Key != "" && r.Header.Get("Authorization") != "Bearer "+f.Key:
		http.Error(w, `{"error":{"code":401,"message":"Invalid API Key"}}`, http.StatusUnauthorized)
	case status != 0:
		f.calls.Add(1)
		http.Error(w, `{"error":{"message":"forced"}}`, status)
	default:
		f.embed(w, r, mode)
	}
}

func (f *Fake) embed(w http.ResponseWriter, r *http.Request, mode Mode) {
	f.calls.Add(1)
	if f.Hang {
		select {
		case <-r.Context().Done():
		case <-f.stop:
		}
		return
	}
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Input) == 0 {
		http.Error(w, `{"error":{"message":"bad input"}}`, http.StatusBadRequest)
		return
	}
	for _, in := range req.Input {
		if f.MaxInputBytes > 0 && len(in) > f.MaxInputBytes {
			http.Error(w, fmt.Sprintf(`{"error":{"code":500,"message":"input (%d tokens) is too large to process"}}`, len(in)), http.StatusInternalServerError)
			return
		}
	}
	f.inputs.Add(int64(len(req.Input)))
	time.Sleep(f.Delay * time.Duration(len(req.Input)))
	dims := f.Dims
	if dims == 0 {
		dims = Dims
	}
	var b strings.Builder
	tokens := 0
	b.WriteString(`{"object":"list","model":"fake","data":[`)
	for i, in := range req.Input {
		tokens += len(strings.Fields(in))
		if i > 0 {
			b.WriteString(",")
		}
		idx := i
		if mode == BadIndex {
			idx = 0
		}
		fmt.Fprintf(&b, `{"object":"embedding","index":%d,"embedding":%s}`, idx, encodeVector(Vector(in, dims), mode))
	}
	fmt.Fprintf(&b, `],"usage":{"prompt_tokens":%d,"total_tokens":%d}}`, tokens, tokens)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, b.String())
}

func encodeVector(v []float32, mode Mode) string {
	parts := make([]string, 0, len(v))
	for i, x := range v {
		switch {
		case mode == HalfNorm:
			x /= 2
		case mode == Short && i == len(v)-1:
			continue
		}
		s := fmt.Sprintf("%g", x)
		if i == 0 && mode == NullValue {
			s = "null"
		}
		if i == 0 && mode == NotJSON {
			s = "nan"
		}
		parts = append(parts, s)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// ProviderID is the record id Install writes.
const ProviderID = "test-embedder"

// Install writes one complete embedder record pointing at url and selects
// it in embed.provider (tests that use the real catalog and Source).
func Install(t testing.TB, cfg *config.Config, url string) {
	t.Helper()
	install(t, cfg, engine.Provider{Protocol: engine.ProtocolOpenAIEmbeddings, BaseURL: url, Path: "/v1/embeddings",
		Model: "test-embedding-model", CharsPerToken: 5, ContextTokens: Context, Embedding: spec()})
}

// InstallRuntime writes the managed-runtime embedder record (base URL and
// key come from the sidecar Locator) and selects it.
func InstallRuntime(t testing.TB, cfg *config.Config, runtime, model string) {
	t.Helper()
	install(t, cfg, engine.Provider{Protocol: engine.ProtocolOpenAIEmbeddings, Runtime: runtime, Path: "/v1/embeddings",
		Model: model, CharsPerToken: 5, Embedding: spec()})
}

func spec() *engine.EmbeddingSpec {
	return &engine.EmbeddingSpec{Dimensions: Dims, Matryoshka: []int{512, 256, 128},
		QueryTemplate:    "task: search result | query: {text}",
		DocumentTemplate: "title: {title} | text: {text}",
		EmptyTitle:       "none", NormTolerance: 0.01, MaxBatch: 32, ReservedTokens: 8}
}

func install(t testing.TB, cfg *config.Config, p engine.Provider) {
	t.Helper()
	raw, err := json.Marshal(map[string]engine.Provider{ProviderID: p})
	if err != nil {
		t.Fatal(err)
	}
	path := cfg.Paths().Resolve(cfg.String(engine.ProvidersFileSetting))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("embed.provider", ProviderID); err != nil {
		t.Fatal(err)
	}
}

// Locator is a fixed managed endpoint for tests (engine.Locator): the
// runtime catalog's context and a fixed model revision.
type Locator struct {
	URL string
	Key string
}

// Revision is the fake model file's revision.
const Revision = "0123456789abcdef"

// Context is the fake runtime's per-input context (llama-server's slot).
const Context = 2048

// Locate hands out the endpoint.
func (l Locator) Locate(context.Context, string, string) (engine.Endpoint, error) {
	return engine.Endpoint{URL: l.URL, Key: l.Key}, nil
}

// Failed is ignored (the endpoint never moves).
func (Locator) Failed(string, string, engine.Endpoint) {}

// Model is the fake model's revision and context.
func (Locator) Model(string, string) (engine.ModelInfo, error) {
	return engine.ModelInfo{Revision: Revision, ContextTokens: Context}, nil
}
