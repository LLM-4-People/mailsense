package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/triage"
)

func providerAPI(t *testing.T, environ []string) (*providersAPI, *config.Config) {
	t.Helper()
	cfg := configtest.New(t, nil)
	sec, err := secrets.Open(cfg, environ, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	engines := triage.NewEngines(engine.Deps{Config: cfg, Secrets: sec})
	return &providersAPI{w: &wire{app: &app{cfg: cfg, engines: engines}}}, cfg
}

func generatorRecord(url string) engine.Provider {
	zero := 0.0
	return engine.Provider{ID: "gen", Label: "Generator", Protocol: engine.ProtocolOpenAIChat, BaseURL: url,
		Path: "/chat/completions", Model: "g-1", APIKeySecret: "PROVIDER_GEN_API_KEY", ContextTokens: 10000,
		CharsPerToken: 3, ReserveTokensPerByte: 1, ReserveOutputTokens: 128,
		ResponseFormat: engine.ResponseFormatJSON, PriceInput: &zero, PriceOutput: &zero}
}

func TestProviderAPICreateUpdateDelete(t *testing.T) {
	ctx := context.Background()
	p, _ := providerAPI(t, nil)
	info, err := p.Create(ctx, generatorRecord("http://127.0.0.1:9/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "gen" || info.Kind != engine.KindGenerator || info.KeyPresent {
		t.Fatalf("created %+v", info)
	}
	if info.ContextTokens != 10000 || info.ReserveOutputTokens != 128 || info.ResponseFormat != engine.ResponseFormatJSON ||
		info.PriceInput == nil || info.PriceOutput == nil {
		t.Fatalf("the API record lost its capability fields: %+v", info)
	}
	list, err := p.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "gen" {
		t.Fatalf("list %+v %v", list, err)
	}
	if list[0].ContextTokens != 10000 || list[0].PriceInput == nil {
		t.Fatalf("the listed record lost its capability fields: %+v", list[0])
	}
	rec := generatorRecord("http://127.0.0.1:9/v1")
	rec.Label, rec.Model = "Renamed", "g-2"
	info, err = p.Update(ctx, "gen", rec)
	if err != nil || info.Label != "Renamed" || info.Model != "g-2" {
		t.Fatalf("update %+v %v", info, err)
	}
	if err := p.Delete(ctx, "gen"); err != nil {
		t.Fatal(err)
	}
	list, err = p.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("after delete %+v %v", list, err)
	}
}

func TestProviderAPIRejectsInvalidRecords(t *testing.T) {
	ctx := context.Background()
	p, _ := providerAPI(t, nil)
	bad := generatorRecord("http://127.0.0.1:9/v1")
	bad.Protocol = "telepathy"
	if _, err := p.Create(ctx, bad); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("invalid protocol: %v", err)
	}
	good := generatorRecord("http://127.0.0.1:9/v1")
	if _, err := p.Create(ctx, good); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(ctx, good); err == nil {
		t.Fatal("a duplicate id was accepted")
	} else {
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
			t.Fatalf("duplicate: %v", err)
		}
	}
	if _, err := p.Create(ctx, engine.Provider{ID: "x", Protocol: engine.ProtocolOpenAIChat}); err == nil {
		t.Fatal("a missing record was accepted")
	}
}

func TestProviderRolesReportAvailability(t *testing.T) {
	ctx := context.Background()
	p, cfg := providerAPI(t, []string{"MAILSENSE_SECRET_PROVIDER_GEN_API_KEY=test"})
	role := func() api.ProviderRoleInfo {
		t.Helper()
		roles, err := p.Roles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range roles {
			if r.Setting == "qa.provider" {
				return r
			}
		}
		t.Fatal("qa.provider role is missing")
		return api.ProviderRoleInfo{}
	}
	if r := role(); r.Configured || r.Available || !strings.Contains(r.Detail, "not configured") {
		t.Fatalf("an empty role must be unconfigured: %+v", r)
	}
	if _, err := p.Create(ctx, generatorRecord("http://127.0.0.1:9/v1")); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("qa.provider", "gen"); err != nil {
		t.Fatal(err)
	}
	if r := role(); !r.Configured || !r.Available || r.Detail != "" {
		t.Fatalf("a configured record with its key must be available: %+v", r)
	}
	noKey := generatorRecord("http://127.0.0.1:9/v1")
	noKey.ID, noKey.Label, noKey.APIKeySecret = "nokey", "No key", "PROVIDER_MISSING_KEY"
	if _, err := p.Create(ctx, noKey); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("qa.provider", "nokey"); err != nil {
		t.Fatal(err)
	}
	if r := role(); !r.Configured || r.Available || !strings.Contains(r.Detail, "API key") {
		t.Fatalf("a configured record with no key must be configured but unavailable: %+v", r)
	}
	if err := cfg.Set("qa.provider", "ghost"); err != nil {
		t.Fatal(err)
	}
	if r := role(); r.Configured || r.Available || r.Detail == "" {
		t.Fatalf("an unknown record must be unconfigured: %+v", r)
	}
}

func TestProviderAPITestDispatchesGeneratorAndEmbedder(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"g-1","choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	env := []string{"MAILSENSE_SECRET_PROVIDER_GEN_API_KEY=k"}
	p, _ := providerAPI(t, env)
	rec := generatorRecord(srv.URL)
	if _, err := p.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	res, err := p.Test(ctx, "gen")
	if err != nil || !res.OK || res.Detail != "generator answered" || res.DetailCode != "generator_answered" || res.Model != "g-1" {
		t.Fatalf("generator test %+v %v", res, err)
	}

	fake := embedtest.New(t, &embedtest.Fake{})
	emb := engine.Provider{ID: "emb", Protocol: engine.ProtocolOpenAIEmbeddings, BaseURL: fake.URL,
		Path: "/v1/embeddings", Model: "e-1", CharsPerToken: 5, ContextTokens: embedtest.Context,
		Embedding: &engine.EmbeddingSpec{Dimensions: embedtest.Dims, Matryoshka: []int{512},
			QueryTemplate: "query: {text}", DocumentTemplate: "title: {title} | text: {text}",
			EmptyTitle: "none", NormTolerance: 0.01, MaxBatch: 8, ReservedTokens: 8}}
	if _, err := p.Create(ctx, emb); err != nil {
		t.Fatal(err)
	}
	res, err = p.Test(ctx, "emb")
	if err != nil || !res.OK || res.Detail == "" || res.Detail != "embedder returned one 768-dimensional unit vector" ||
		res.DetailCode != "embedder_answered" || res.DetailParams["dimensions"] != "768" {
		t.Fatalf("embedder test %+v %v", res, err)
	}
}

func TestProviderAPIMissingKeyAndRoleInUse(t *testing.T) {
	ctx := context.Background()
	p, cfg := providerAPI(t, nil)
	if _, err := p.Create(ctx, generatorRecord("http://127.0.0.1:9/v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Test(ctx, "gen"); !errors.Is(err, engine.ErrNoKey) {
		t.Fatalf("missing key: %v", err)
	}
	if err := cfg.Set("judge.provider", "gen"); err != nil {
		t.Fatal(err)
	}
	err := p.Delete(ctx, "gen")
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || !strings.Contains(apiErr.Message, "judge.provider") {
		t.Fatalf("role-in-use delete: %v", err)
	}
	// Changing the kind of a selected record is refused too.
	rec := generatorRecord("http://127.0.0.1:9/v1")
	rec.Protocol = engine.ProtocolSystemOne
	rec.MaxQuestions, rec.ProbabilityTolerance = 8, 0.02
	if _, err := p.Update(ctx, "gen", rec); err == nil {
		t.Fatal("a kind change on a selected record was accepted")
	}
}

func TestProviderAPICreateSuggestsTheSecretName(t *testing.T) {
	ctx := context.Background()
	p, _ := providerAPI(t, nil)
	rec := generatorRecord("http://127.0.0.1:9/v1")
	rec.APIKeySecret = ""
	info, err := p.Create(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if info.APIKeySecret != "PROVIDER_GEN_API_KEY" {
		t.Fatalf("suggested secret %q", info.APIKeySecret)
	}
}

func assertProviderAPIError(t *testing.T, err error, status int, code, message string) {
	t.Helper()
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *api.APIError", err)
	}
	if apiErr.Status != status || apiErr.Code != code || !strings.Contains(apiErr.Message, message) {
		t.Fatalf("error = %+v, want status %d code %s message containing %q", apiErr, status, code, message)
	}
}

// assertProviderCatalog checks the one saved record set and that the atomic
// save left no temporary file behind.
func assertProviderCatalog(t *testing.T, cfg *config.Config, want []engine.Provider) {
	t.Helper()
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if got := cat.Records(); !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog records = %#v, want %#v", got, want)
	}
	dir := filepath.Dir(cfg.Paths().Resolve(cfg.String(engine.ProvidersFileSetting)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read providers directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("atomic save left a temporary file: %s", entry.Name())
		}
	}
}

func TestProviderWritesShareOnePipeline(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid ids", func(t *testing.T) {
		cases := []struct {
			name    string
			call    func(*providersAPI) error
			message string
		}{
			{"create missing id", func(p *providersAPI) error {
				_, err := p.Create(ctx, engine.Provider{})
				return err
			}, "id is required"},
			{"create malformed id", func(p *providersAPI) error {
				_, err := p.Create(ctx, engine.Provider{ID: "Bad_ID"})
				return err
			}, "id must be a lower-case slug"},
			{"update malformed id", func(p *providersAPI) error {
				_, err := p.Update(ctx, "Bad_ID", engine.Provider{})
				return err
			}, "invalid provider id"},
			{"delete malformed id", func(p *providersAPI) error {
				return p.Delete(ctx, "Bad_ID")
			}, "invalid provider id"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p, _ := providerAPI(t, nil)
				assertProviderAPIError(t, tc.call(p), http.StatusUnprocessableEntity, "invalid_provider", tc.message)
			})
		}
	})

	t.Run("role in use", func(t *testing.T) {
		cases := []struct {
			name string
			call func(*providersAPI, engine.Provider) error
		}{
			{"update kind", func(p *providersAPI, rec engine.Provider) error {
				rec.Protocol = engine.ProtocolSystemOne
				_, err := p.Update(ctx, "gen", rec)
				return err
			}},
			{"delete", func(p *providersAPI, _ engine.Provider) error {
				return p.Delete(ctx, "gen")
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p, cfg := providerAPI(t, nil)
				rec := generatorRecord("http://127.0.0.1:9/v1")
				if _, err := p.Create(ctx, rec); err != nil {
					t.Fatal(err)
				}
				if err := cfg.Set("judge.provider", "gen"); err != nil {
					t.Fatal(err)
				}
				assertProviderAPIError(t, tc.call(p, rec), http.StatusConflict, "provider_in_use", "judge.provider")
			})
		}
	})

	t.Run("successful writes", func(t *testing.T) {
		cases := []struct {
			name string
			seed bool
			run  func(*providersAPI) error
			want []engine.Provider
		}{
			{"create", false, func(p *providersAPI) error {
				_, err := p.Create(ctx, generatorRecord("http://127.0.0.1:9/v1"))
				return err
			}, []engine.Provider{generatorRecord("http://127.0.0.1:9/v1")}},
			{"update", true, func(p *providersAPI) error {
				rec := generatorRecord("http://127.0.0.1:9/v1")
				rec.Label, rec.Model = "Renamed", "g-2"
				_, err := p.Update(ctx, "gen", rec)
				return err
			}, func() []engine.Provider {
				rec := generatorRecord("http://127.0.0.1:9/v1")
				rec.Label, rec.Model = "Renamed", "g-2"
				return []engine.Provider{rec}
			}()},
			{"delete", true, func(p *providersAPI) error {
				return p.Delete(ctx, "gen")
			}, []engine.Provider{}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p, cfg := providerAPI(t, nil)
				if tc.seed {
					if _, err := p.Create(ctx, generatorRecord("http://127.0.0.1:9/v1")); err != nil {
						t.Fatal(err)
					}
				}
				saves := 0
				p.saveCatalog = func(cfg *config.Config, cat *engine.Catalog) error {
					saves++
					return engine.SaveCatalog(cfg, cat)
				}
				if err := tc.run(p); err != nil {
					t.Fatal(err)
				}
				if saves != 1 {
					t.Fatalf("successful %s saved the catalog %d times, want 1", tc.name, saves)
				}
				assertProviderCatalog(t, cfg, tc.want)
			})
		}
	})
}

func TestProviderWriteRollsBackOnBuildFailure(t *testing.T) {
	ctx := context.Background()
	p, cfg := providerAPI(t, nil)
	if _, err := p.Create(ctx, generatorRecord("http://127.0.0.1:9/v1")); err != nil {
		t.Fatal(err)
	}
	before, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	beforeRecords, beforeDigest := before.Records(), before.Digest
	saves := 0
	p.saveCatalog = func(cfg *config.Config, cat *engine.Catalog) error {
		saves++
		return engine.SaveCatalog(cfg, cat)
	}

	_, err = p.writeProviders(ctx, func(cat *engine.Catalog) ([]engine.Provider, error) {
		bad := generatorRecord("http://127.0.0.1:9/v1")
		bad.ID, bad.Protocol = "bad", "telepathy"
		return append(cat.Records(), bad), nil
	})
	if err == nil {
		t.Fatal("an invalid record set was saved")
	}
	var validation *engine.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want *engine.ValidationError", err)
	}
	if saves != 0 {
		t.Fatalf("a failed build saved the catalog %d times", saves)
	}

	after, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if afterRecords := after.Records(); !reflect.DeepEqual(beforeRecords, afterRecords) || beforeDigest != after.Digest {
		t.Fatalf("failed build changed the catalog: before %#v after %#v", beforeRecords, afterRecords)
	}
}

func TestProviderWritesSerialize(t *testing.T) {
	ctx := context.Background()
	p, cfg := providerAPI(t, nil)
	rec := generatorRecord("http://127.0.0.1:9/v1")
	saves := 0
	p.saveCatalog = func(cfg *config.Config, cat *engine.Catalog) error {
		saves++
		return engine.SaveCatalog(cfg, cat)
	}

	const writers = 2
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := p.Create(ctx, rec)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	created, conflicted := 0, 0
	for err := range results {
		if err == nil {
			created++
			continue
		}
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "provider_exists" {
			t.Fatalf("concurrent create error = %v, want provider_exists", err)
		}
		conflicted++
	}
	if created != 1 || conflicted != writers-1 {
		t.Fatalf("concurrent creates: %d created, %d conflicted; want 1 and %d", created, conflicted, writers-1)
	}
	if saves != 1 {
		t.Fatalf("concurrent creates saved the catalog %d times, want 1", saves)
	}
	assertProviderCatalog(t, cfg, []engine.Provider{rec})
}
