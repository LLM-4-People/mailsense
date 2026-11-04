package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

func pinFor(t *testing.T, cfg *config.Config, platform string) sidecar.LocalEmbedder {
	t.Helper()
	cat, err := sidecar.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := cat.LocalEmbedder(platform)
	if !ok {
		t.Skipf("no pinned local embedder for %s", platform)
	}
	return pin
}

func providerPath(cfg *config.Config) string {
	return cfg.Paths().Resolve(cfg.String(engine.ProvidersFileSetting))
}

func readProviderFile(t *testing.T, cfg *config.Config) ([]byte, bool) {
	t.Helper()
	data, err := os.ReadFile(providerPath(cfg))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return data, true
}

func writeProviderFile(t *testing.T, cfg *config.Config, records map[string]engine.Provider) {
	t.Helper()
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providerPath(cfg), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func localRecord(pin sidecar.LocalEmbedder) engine.Provider {
	spec := pin.Embedding
	return engine.Provider{Label: "owner label", Protocol: engine.ProtocolOpenAIEmbeddings,
		Path: EmbeddingsPath, Model: pin.Model, Runtime: pin.Runtime,
		CharsPerToken: pin.CharsPerToken, Embedding: &spec}
}

func TestEnsureSeedsThePinnedRecordAndIsIdempotent(t *testing.T) {
	cfg := configtest.New(t, nil)
	pin := pinFor(t, cfg, sidecar.MachinePlatform())
	if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("embed.provider"); got != LocalProviderID {
		t.Fatalf("embed.provider = %q", got)
	}
	if cfg.Snapshot().Layer("embed.provider") != config.LayerFile {
		t.Fatalf("role layer = %q", cfg.Snapshot().Layer("embed.provider"))
	}
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cat.Get(LocalProviderID)
	if !ok {
		t.Fatal("the local record was not written")
	}
	if p.Label != pin.Model || p.Protocol != engine.ProtocolOpenAIEmbeddings || p.Path != EmbeddingsPath ||
		p.Model != pin.Model || p.Runtime != pin.Runtime || p.BaseURL != "" || p.APIKeySecret != "" ||
		p.ContextTokens != 0 || p.CharsPerToken != pin.CharsPerToken {
		t.Fatalf("generated record: %+v", p)
	}
	if p.Embedding == nil || !reflect.DeepEqual(*p.Embedding, pin.Embedding) {
		t.Fatalf("embedding spec: %+v", p.Embedding)
	}
	if _, err := engine.BuildCatalog(cfg, cat.Records()); err != nil {
		t.Fatalf("the generated record must pass the write-path rules: %v", err)
	}
	before, ok := readProviderFile(t, cfg)
	if !ok {
		t.Fatal("provider file missing")
	}

	if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := readProviderFile(t, cfg)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("a second ensure rewrote the owner's provider file")
	}
	if got := cfg.String("embed.provider"); got != LocalProviderID {
		t.Fatalf("second ensure changed the role: %q", got)
	}
}

func TestEnsureLeavesAnExplicitRoleAlone(t *testing.T) {
	for name, value := range map[string]string{"non-empty": "owner-choice", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			cfg := configtest.New(t, map[string]any{"embed.provider": value})
			if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			if got := cfg.String("embed.provider"); got != value {
				t.Fatalf("role = %q", got)
			}
			if layer := cfg.Snapshot().Layer("embed.provider"); layer != config.LayerFile {
				t.Fatalf("the explicit layer was replaced: %q", layer)
			}
			if _, ok := readProviderFile(t, cfg); ok {
				t.Fatal("the provider file was written despite an explicit role")
			}
		})
	}
}

func TestEnsureLeavesAnExplicitEnvironmentValueAlone(t *testing.T) {
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: t.TempDir()},
		Environ: []string{"MAILSENSE_EMBED_PROVIDER="}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Snapshot().Layer("embed.provider") != config.LayerEnv {
		t.Fatalf("layer = %q", cfg.Snapshot().Layer("embed.provider"))
	}
	if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("embed.provider"); got != "" {
		t.Fatalf("role = %q", got)
	}
	if _, ok := readProviderFile(t, cfg); ok {
		t.Fatal("the provider file was written despite an explicit environment value")
	}
}

func TestEnsureLeavesAMalformedProviderFileAloneAndRetries(t *testing.T) {
	cfg := configtest.New(t, nil)
	const junk = `{"local-embedder":`
	if err := os.WriteFile(providerPath(cfg), []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalEmbedder(t.Context(), cfg); err == nil {
		t.Fatal("a malformed provider file must be reportable")
	}
	data, _ := readProviderFile(t, cfg)
	if string(data) != junk {
		t.Fatalf("the malformed file was changed: %q", data)
	}
	if got := cfg.String("embed.provider"); got != "" {
		t.Fatalf("role = %q", got)
	}
	if err := os.Remove(providerPath(cfg)); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
		t.Fatalf("the next start must retry: %v", err)
	}
	if got := cfg.String("embed.provider"); got != LocalProviderID {
		t.Fatalf("role after retry = %q", got)
	}
}

func TestEnsureReusesAnExistingLocalRecord(t *testing.T) {
	cfg := configtest.New(t, nil)
	pin := pinFor(t, cfg, sidecar.MachinePlatform())

	t.Run("same id", func(t *testing.T) {
		cfg := configtest.New(t, nil)
		writeProviderFile(t, cfg, map[string]engine.Provider{LocalProviderID: localRecord(pin)})
		before, _ := readProviderFile(t, cfg)
		if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		if got := cfg.String("embed.provider"); got != LocalProviderID {
			t.Fatalf("role = %q", got)
		}
		after, _ := readProviderFile(t, cfg)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("an existing record was overwritten")
		}
	})

	t.Run("different id", func(t *testing.T) {
		cfg := configtest.New(t, nil)
		writeProviderFile(t, cfg, map[string]engine.Provider{"owner-embedder": localRecord(pin)})
		before, _ := readProviderFile(t, cfg)
		if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		if got := cfg.String("embed.provider"); got != "owner-embedder" {
			t.Fatalf("role = %q (the equivalent record must be reused)", got)
		}
		after, _ := readProviderFile(t, cfg)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("a duplicate local record was written")
		}
	})
}

func TestEnsureDoesNotOverwriteAnIDCollision(t *testing.T) {
	cfg := configtest.New(t, nil)
	pin := pinFor(t, cfg, sidecar.MachinePlatform())
	spec := pin.Embedding
	other := engine.Provider{Label: "other", Protocol: engine.ProtocolOpenAIEmbeddings, Path: EmbeddingsPath,
		Model: "other-model", Runtime: "other-runtime", CharsPerToken: 5, Embedding: &spec}
	writeProviderFile(t, cfg, map[string]engine.Provider{LocalProviderID: other})
	before, _ := readProviderFile(t, cfg)

	if err := EnsureLocalEmbedder(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("embed.provider"); got != "" {
		t.Fatalf("a colliding id must leave the role empty: %q", got)
	}
	after, _ := readProviderFile(t, cfg)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("a colliding record was overwritten")
	}
}

func TestEnsureSkipsWhenThePinSpecOrPlatformIsMissing(t *testing.T) {
	cases := map[string]struct {
		platform string
		runtimes string
	}{
		"no local pin":      {runtimes: `{"local_embedder": null}`},
		"no platform build": {platform: "plan9/386"},
		"no embedding spec": {runtimes: `{"local_embedder": {"runtime": "llama-server", "model": "no-spec"},
			"models": {"no-spec": {"name": "no-spec.gguf", "url": "https://example.invalid/no-spec.gguf",
				"sha256": "` + strings.Repeat("a", 64) + `", "size": 1, "unpack": ""}}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := configtest.New(t, nil)
			if tc.runtimes != "" {
				if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "runtimes.json"), []byte(tc.runtimes), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			platform := tc.platform
			if platform == "" {
				platform = sidecar.MachinePlatform()
			}
			if err := ensure(t.Context(), cfg, platform); err != nil {
				t.Fatalf("an unavailable pin is a skip, not an error: %v", err)
			}
			if got := cfg.String("embed.provider"); got != "" {
				t.Fatalf("role = %q", got)
			}
			if _, ok := readProviderFile(t, cfg); ok {
				t.Fatal("the provider file was written without a usable pin")
			}
		})
	}
}

func TestEnsureHonorsCancellation(t *testing.T) {
	cfg := configtest.New(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := EnsureLocalEmbedder(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ensure: %v", err)
	}
	if _, ok := readProviderFile(t, cfg); ok {
		t.Fatal("a cancelled ensure wrote the provider file")
	}
}
