package engine_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

func writeProviders(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const deciderJSON = `{
  "label": "Local decider",
  "protocol": "systemone",
  "base_url": "http://127.0.0.1:9/v1",
  "path": "/systemone",
  "model": "m-1",
  "api_key_secret": "DECIDER_KEY",
  "max_questions": 16,
  "context_tokens": 100000,
  "chars_per_token": 3,
  "reserve_tokens_per_byte": 1,
  "probability_tolerance": 0.02,
  "price_input_per_mtok": 0.05,
  "price_output_per_mtok": 0
}`

const generatorJSON = `{
  "label": "Local generator",
  "protocol": "openai_chat",
  "base_url": "http://127.0.0.1:9/v1",
  "path": "/chat/completions",
  "model": "g-1",
  "api_key_secret": "GENERATOR_KEY",
  "context_tokens": 100000,
  "chars_per_token": 3,
  "reserve_tokens_per_byte": 1,
  "reserve_output_tokens": 1024,
  "response_format": "json_object",
  "price_input_per_mtok": 0.1,
  "price_output_per_mtok": 0.2
}`

const embedderJSON = `{
  "label": "Local embedder",
  "protocol": "openai_embeddings",
  "base_url": "http://127.0.0.1:9",
  "path": "/v1/embeddings",
  "model": "e-1",
  "chars_per_token": 5,
  "context_tokens": 2048,
  "embedding": {
    "dimensions": 8,
    "matryoshka": [4],
    "query_template": "query: {text}",
    "document_template": "title: {title} | text: {text}",
    "empty_title": "none",
    "norm_tolerance": 0.01,
    "max_batch": 8
  }
}`

func recordsJSON(records map[string]string) string {
	var b strings.Builder
	b.WriteString("{")
	first := true
	for _, id := range slices.Sorted(maps.Keys(records)) {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"` + id + `":` + records[id])
	}
	b.WriteString("}")
	return b.String()
}

func fullProviders() map[string]string {
	return map[string]string{"dec": deciderJSON, "gen": generatorJSON, "emb": embedderJSON}
}

func TestEmptyProviderFileMeansNoProviders(t *testing.T) {
	cfg := configtest.New(t, nil)
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Len() != 0 || len(cat.Names("")) != 0 || len(cat.List()) != 0 {
		t.Fatalf("a missing provider file must be empty, got %+v", cat.List())
	}
	for _, body := range []string{"", " \n\t", "{}"} {
		writeProviders(t, cfg.Paths().DataDir, body)
		cat, err = engine.LoadCatalog(cfg)
		if err != nil || cat.Len() != 0 {
			t.Fatalf("empty provider file %q: %v %+v", body, err, cat)
		}
	}
}

func TestRecordsAreStrictAndValidated(t *testing.T) {
	cfg := configtest.New(t, nil)
	writeProviders(t, cfg.Paths().DataDir, recordsJSON(fullProviders()))
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dec, ok := cat.Get("dec")
	if !ok || dec.Kind() != engine.KindDecider || dec.Protocol != engine.ProtocolSystemOne {
		t.Fatalf("decider: %+v", dec)
	}
	if u, err := dec.URL(); err != nil || u != "http://127.0.0.1:9/v1/systemone" {
		t.Fatalf("url %q %v", u, err)
	}
	gen, _ := cat.Get("gen")
	if gen.Kind() != engine.KindGenerator {
		t.Fatalf("generator: %+v", gen)
	}
	if u, err := gen.URL(); err != nil || u != "http://127.0.0.1:9/v1/chat/completions" {
		t.Fatalf("generator url %q %v", u, err)
	}
	emb, _ := cat.Get("emb")
	if emb.Kind() != engine.KindEmbedder || emb.Embedding == nil || emb.Embedding.Dimensions != 8 {
		t.Fatalf("embedder: %+v", emb)
	}
	if got := cat.Names(engine.KindGenerator); len(got) != 1 || got[0] != "gen" {
		t.Fatalf("generator names %v", got)
	}
	reg := engine.NewRegistry()
	decision.Register(reg)
	embed.Register(reg)
	generate.Register(reg)
	if d := reg.Protocols(engine.KindDecider); len(d) != 2 {
		t.Fatalf("decider protocols %v", d)
	}
	if d := reg.Protocols(engine.KindGenerator); len(d) != 1 || d[0] != generate.Protocol {
		t.Fatalf("generator protocols %v", d)
	}
	for _, p := range cat.List() {
		if !slices.Contains(reg.Protocols(p.Kind()), p.Protocol) {
			t.Errorf("record %s uses an unregistered protocol %s", p.ID, p.Protocol)
		}
	}
}

func TestInvalidProviderSetReportsEveryProblem(t *testing.T) {
	cfg := configtest.New(t, nil)
	writeProviders(t, cfg.Paths().DataDir, recordsJSON(map[string]string{
		"dec": `{"protocol": "systemone", "base_url": "ftp://x", "path": "systemone/{region}", "model": "m",
			"api_key_secret": "lower", "max_questions": 0, "context_tokens": 10, "chars_per_token": 3,
			"reserve_tokens_per_byte": 1, "probability_tolerance": 0.02,
			"retry": {"statuses": [402, 500, 503]}}`,
		"Bad Name": `{"protocol": "oracle", "base_url": "http://x", "path": "/", "model": "m",
			"api_key_secret": "BAD", "context_tokens": 10, "chars_per_token": 3}`,
		"surprise": `{"protocol": "openai_chat", "base_url": "http://x", "path": "/q", "model": "m",
			"api_key_secret": "K", "context_tokens": 10, "chars_per_token": 3, "reserve_tokens_per_byte": 1,
			"reserve_output_tokens": 2, "price_input_per_mtok": 1, "price_output_per_mtok": 1, "surprise": true}`,
		"emb": `{"protocol": "openai_embeddings", "base_url": "http://x", "path": "/v1/embeddings", "model": "m",
			"chars_per_token": 5, "context_tokens": 128, "embedding": {"dimensions": 0}}`,
		"leaky": `{"protocol": "openai_chat", "base_url": "https://user:pass@example.test/v1?key=sentinel-credential", "path": "/q", "model": "m",
			"api_key_secret": "K", "context_tokens": 10, "chars_per_token": 3, "reserve_tokens_per_byte": 1,
			"reserve_output_tokens": 2, "price_input_per_mtok": 1, "price_output_per_mtok": 1}`,
	}))
	_, err := engine.LoadCatalog(cfg)
	if err == nil {
		t.Fatal("invalid providers accepted")
	}
	for _, want := range []string{
		"Bad Name/id", `Bad Name/protocol: must be one of openai_chat, openai_decisions, openai_embeddings, systemone, got "oracle"`,
		"dec/base_url", "unknown placeholder {region}", "dec/path: must start with /", "dec/api_key_secret",
		"402 cannot be retried", "500 cannot be retried", `unknown field "surprise"`,
		"emb/embedding", "dimensions must be ≥ 1", "leaky/base_url",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	for _, secret := range []string{"user:pass", "sentinel-credential"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the validation error echoes %q:\n%v", secret, err)
		}
	}
	var problems *engine.ValidationError
	if !errors.As(err, &problems) || len(problems.Problems) < 5 {
		t.Fatalf("every problem is reported: %v", err)
	}
	for _, junk := range []string{`[]`, `{"providers": []}`, `{"dec": 3}`, `{"dec": {"id": "dec"}}`, `{`} {
		writeProviders(t, cfg.Paths().DataDir, junk)
		if _, err := engine.LoadCatalog(cfg); err == nil {
			t.Errorf("junk %s accepted", junk)
		}
	}
}

func TestSelectIsExplicitAboutUnconfiguredRoles(t *testing.T) {
	cfg := configtest.New(t, nil)
	writeProviders(t, cfg.Paths().DataDir, recordsJSON(fullProviders()))
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"empty": "", "missing": "nope", "wrong kind": "gen"} {
		if _, err := cat.Select(id, engine.KindDecider); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p, err := cat.Select("dec", engine.KindDecider); err != nil || p.ID != "dec" {
		t.Fatalf("select: %v %+v", err, p)
	}
}

func TestFingerprintIgnoresIdentityAndTracksTheModel(t *testing.T) {
	cfg := configtest.New(t, nil)
	writeProviders(t, cfg.Paths().DataDir, recordsJSON(fullProviders()))
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := cat.Get("dec")
	same := dec
	same.ID, same.Label = "renamed", "Another label"
	if dec.Fingerprint() != same.Fingerprint() {
		t.Fatal("a rename or label change must not change the fingerprint")
	}
	for name, edit := range map[string]func(*engine.Provider){
		"model":    func(p *engine.Provider) { p.Model = "m-2" },
		"base_url": func(p *engine.Provider) { p.BaseURL = "http://127.0.0.1:10/v1" },
		"path":     func(p *engine.Provider) { p.Path = "/other" },
		"protocol": func(p *engine.Provider) { p.Protocol = engine.ProtocolOpenAIDecisions },
	} {
		changed := dec
		edit(&changed)
		if changed.Fingerprint() == dec.Fingerprint() {
			t.Errorf("%s change must invalidate the fingerprint", name)
		}
	}
	emb, _ := cat.Get("emb")
	changed := emb
	changed.Embedding = &engine.EmbeddingSpec{}
	*changed.Embedding = *emb.Embedding
	changed.Embedding.Dimensions = 4
	if changed.Fingerprint() == emb.Fingerprint() {
		t.Error("an embedding spec change must invalidate the fingerprint")
	}
}

func TestSaveCatalogWritesTheRecordSet(t *testing.T) {
	cfg := configtest.New(t, nil)
	base := configtest.New(t, nil)
	writeProviders(t, base.Paths().DataDir, recordsJSON(fullProviders()))
	cat, err := engine.LoadCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.SaveCatalog(cfg, cat); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.Paths().DataDir, "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `"id"`) || !strings.HasPrefix(text, "{\n  \"dec\"") {
		t.Fatalf("the file is keyed by id and has no record id field:\n%s", text)
	}
	reloaded, err := engine.LoadCatalog(cfg)
	if err != nil || reloaded.Len() != 3 {
		t.Fatalf("reload: %v %+v", err, reloaded)
	}
	// Deleting every record writes an empty document, not the shipped one.
	if err := engine.SaveCatalog(cfg, mustBuild(t, cfg, nil)); err != nil {
		t.Fatal(err)
	}
	empty, err := engine.LoadCatalog(cfg)
	if err != nil || empty.Len() != 0 {
		t.Fatalf("empty catalog: %v %+v", err, empty)
	}
}

func mustBuild(t *testing.T, cfg *config.Config, records []engine.Provider) *engine.Catalog {
	t.Helper()
	cat, err := engine.BuildCatalog(cfg, records)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

type noSecrets struct{ secrets.Store }

func TestRegistryRefusesWhatItCannotBuild(t *testing.T) {
	cfg := configtest.New(t, nil)
	writeProviders(t, cfg.Paths().DataDir, recordsJSON(map[string]string{
		"dec": deciderJSON,
		"gen": generatorJSON,
		"emb": embedderJSON,
	}))
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := engine.NewRegistry()
	decision.Register(reg)
	embed.Register(reg)
	generate.Register(reg)
	deps := engine.Deps{Config: cfg, Secrets: noSecrets{}}
	for _, tc := range []struct {
		id   string
		want string
	}{
		{"missing", "not configured"},
		{"emb", "not a decider"},
		{"gen", "not a decider"},
	} {
		if _, err := reg.Decider(cat, tc.id, deps); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", tc.id, tc.want, err)
		}
	}
	if _, err := reg.Generator(cat, "dec", deps); err == nil || !strings.Contains(err.Error(), "not a generator") {
		t.Errorf("a decider as generator: %v", err)
	}
	if g, err := reg.Generator(cat, "gen", deps); err != nil || g.Name() != "gen" || g.Model() != "g-1" {
		t.Errorf("generator: %v %+v", err, g)
	}
	if _, err := reg.Decider(cat, "dec", engine.Deps{Config: cfg}); err == nil {
		t.Fatal("a provider without a secret store is refused")
	}
	d, err := reg.Decider(cat, "dec", deps)
	if err != nil || d.Name() != "dec" || d.Model() != "m-1" {
		t.Fatalf("decider: %v", err)
	}
	if d.Fingerprint() == "" {
		t.Fatal("every decider exposes its record fingerprint")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registering a protocol twice must panic")
		}
	}()
	decision.Register(reg)
}

func TestSuggestSecretNameIsRecordDerived(t *testing.T) {
	for id, want := range map[string]string{
		"my-decider":  "PROVIDER_MY_DECIDER_API_KEY",
		"acme_claude": "PROVIDER_ACME_CLAUDE_API_KEY",
	} {
		if got := engine.SuggestSecretName(id); got != want {
			t.Errorf("%s: %q", id, got)
		}
		if err := secrets.ValidName(engine.SuggestSecretName(id)); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}
