// Package decision is the one decision-model adapter: shared HTTP, auth,
// retries (httpx, billing-aware, per-provider overrides), chunking at the
// provider's question cap, context-budget enforcement before sending,
// strict answer decoding at the trust boundary and usage accounting;
// two codecs carry the protocols (systemone and openai_decisions).
package decision

import (
	"maps"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Register adds every protocol of this adapter to the registry.
func Register(r *engine.Registry) {
	for _, protocol := range slices.Sorted(maps.Keys(codecs)) {
		r.RegisterDecider(protocol, func(p engine.Provider, deps engine.Deps) (engine.Decider, error) {
			return New(p, deps)
		})
	}
}
