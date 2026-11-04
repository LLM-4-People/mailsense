// Package onboarding performs the first-run setup that must happen before
// the engines are wired: it turns the pinned local embedder into an ordinary
// provider record in the owner's provider file. It never starts a runtime,
// downloads a model, calls a paid provider, or overwrites an owner's choice.
package onboarding

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

// LocalProviderID is the stable id of the generated local embedder record.
const LocalProviderID = "local-embedder"

// settingEmbedProvider is the settings key onboarding reads and writes.
const settingEmbedProvider = "embed.provider"

// EmbeddingsPath is the managed runtime's OpenAI-compatible endpoint.
const EmbeddingsPath = "/v1/embeddings"

// EnsureLocalEmbedder writes the pinned local embedder to the owner's
// provider file and selects it when embed.provider is still at its default
// (empty and untouched by settings.json or the environment). Every other
// case is a no-op, including an explicit empty value. The provider file is
// written before the role, so a partial failure is retried on the next
// start.
func EnsureLocalEmbedder(ctx context.Context, cfg *config.Config) error {
	return ensure(ctx, cfg, sidecar.MachinePlatform())
}

// ensure is the testable form with an explicit platform.
func ensure(ctx context.Context, cfg *config.Config, platform string) error {
	log := logx.For("onboarding")
	snap := cfg.Snapshot()
	if snap.String(settingEmbedProvider) != "" || snap.Layer(settingEmbedProvider) != config.LayerDefault {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		log.Warn("provider file is invalid; the local embedder was not seeded", "err", err)
		return err
	}
	pin, ok := localPin(cfg, platform)
	if !ok {
		return nil
	}

	// A record with the stable id is never overwritten. It is selected only
	// when it is the same local embedder; otherwise the id is a collision
	// and the role stays empty for the owner to resolve.
	if existing, ok := cat.Get(LocalProviderID); ok {
		if !sameLocal(existing, pin) {
			log.Warn("local embedder id is already used by another provider; leaving the role empty",
				"provider", LocalProviderID, "runtime", pin.Runtime, "model", pin.Model)
			return nil
		}
		return selectLocal(cfg, existing)
	}
	// Reuse an owner's equivalent managed record instead of creating a
	// duplicate: one local model, one record.
	for _, p := range cat.List() {
		if sameLocal(p, pin) {
			return selectLocal(cfg, p)
		}
	}

	spec := pin.Embedding
	rec := engine.Provider{
		ID:            LocalProviderID,
		Label:         pin.Model,
		Protocol:      engine.ProtocolOpenAIEmbeddings,
		Path:          EmbeddingsPath,
		Model:         pin.Model,
		Runtime:       pin.Runtime,
		CharsPerToken: pin.CharsPerToken,
		Embedding:     &spec,
	}
	next, err := engine.BuildCatalog(cfg, append(cat.Records(), rec))
	if err != nil {
		log.Error("cannot build the local embedder record", "err", err)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := engine.SaveCatalog(cfg, next); err != nil {
		log.Error("cannot write the local embedder record; nothing was selected", "err", err)
		return err
	}
	return selectLocal(cfg, rec)
}

// localPin loads the runtime catalog and resolves the platform's pinned
// local embedder. A missing pin, platform build or complete spec is logged
// and skipped: the existing no_embedder blocker stays.
func localPin(cfg *config.Config, platform string) (sidecar.LocalEmbedder, bool) {
	log := logx.For("onboarding")
	cat, err := sidecar.LoadCatalog(cfg)
	if err != nil {
		log.Warn("runtime catalog is invalid; the local embedder was not seeded", "err", err)
		return sidecar.LocalEmbedder{}, false
	}
	pin, ok := cat.LocalEmbedder(platform)
	if !ok {
		log.Info("no complete local embedder is pinned for this platform", "platform", platform)
		return sidecar.LocalEmbedder{}, false
	}
	return pin, true
}

// sameLocal reports whether p is already the pinned managed embedder. The
// id and label are display data; the runtime and model are the identity.
func sameLocal(p engine.Provider, pin sidecar.LocalEmbedder) bool {
	return p.Kind() == engine.KindEmbedder && p.Runtime == pin.Runtime && p.Model == pin.Model
}

// selectLocal makes the record the semantic embedder. The record already
// exists on disk (written or reused), so a failure here is retried on the
// next start by the existing-record path.
func selectLocal(cfg *config.Config, p engine.Provider) error {
	if err := cfg.Set(settingEmbedProvider, p.ID); err != nil {
		logx.For("onboarding").Error("local embedder record exists but the role could not be selected; retrying next start",
			"provider", p.ID, "err", err)
		return fmt.Errorf("select %s: %w", p.ID, err)
	}
	logx.For("onboarding").Info("local embedder selected", "provider", p.ID, "runtime", p.Runtime, "model", p.Model)
	return nil
}
