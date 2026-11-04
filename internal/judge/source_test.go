package judge

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
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

func price(v float64) *float64 { return &v }

// judgeConfig is a temp config with one generator record selected as the
// judge.
func judgeConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := configtest.New(t, nil)
	p := engine.Provider{Protocol: engine.ProtocolOpenAIChat, BaseURL: "http://127.0.0.1:9/v1",
		Path: "/chat/completions", Model: "g-1", APIKeySecret: "GEN_KEY", ContextTokens: 10000,
		CharsPerToken: 3, ReserveTokensPerByte: 1, ReserveOutputTokens: 100,
		ResponseFormat: engine.ResponseFormatJSON, PriceInput: price(1), PriceOutput: price(2)}
	raw, err := json.Marshal(map[string]engine.Provider{"gen": p})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "providers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("judge.provider", "gen"); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSourceBuildsTheConfiguredJudge(t *testing.T) {
	src := NewSource(engine.Deps{Config: judgeConfig(t), Secrets: fakeSecrets{"GEN_KEY": "k"}})
	j, err := src.Judge("")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := j.PromptDigest(profile.Questions)
	if err != nil {
		t.Fatal(err)
	}
	if j.Name() != "gen" || j.Model() != "g-1" || digest == "" {
		t.Fatalf("judge %s/%s %q", j.Name(), j.Model(), digest)
	}
	if _, err := src.Judge("nope"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("an unknown record was accepted as a judge: %v", err)
	}
}

func TestSourceRefusesAnUnconfiguredJudge(t *testing.T) {
	src := NewSource(engine.Deps{Config: configtest.New(t, nil), Secrets: fakeSecrets{}})
	if _, err := src.Judge(""); err == nil || !strings.Contains(err.Error(), "no chat generator is configured") {
		t.Fatalf("unconfigured judge: %v", err)
	}
}

func TestSourceCostIsRealAndConservative(t *testing.T) {
	src := NewSource(engine.Deps{Config: judgeConfig(t), Secrets: fakeSecrets{"GEN_KEY": "k"}})
	one, ok := src.Cost(1)
	if !ok || one <= 0 {
		t.Fatalf("one message: %v %v", one, ok)
	}
	ten, ok := src.Cost(10)
	if !ok || math.Abs(ten-10*one) > 1e-12 {
		t.Fatalf("ten messages: %v, want %v", ten, 10*one)
	}
	if zero, ok := src.Cost(0); !ok || zero != 0 {
		t.Fatalf("zero messages: %v %v", zero, ok)
	}
	if _, ok := NewSource(engine.Deps{Config: configtest.New(t, nil)}).Cost(1); ok {
		t.Fatal("a source without secrets priced a judge")
	}
}

func TestSourceRefusesWithoutConfig(t *testing.T) {
	if _, err := NewSource(engine.Deps{}).Judge(""); !errors.Is(err, errConfig) {
		t.Fatalf("want errConfig, got %v", err)
	}
}
