package engine_test

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
)

// TestAdapterRegisterAddsOnlyItsKind pins the composition rule the engine
// factories rely on: each adapter package registers its own kind and nothing
// else, so a factory states the kinds it builds (qa and judge: generate;
// embed.Source: embed; triage.Engines: all three) and cannot inherit another
// kind by accident. A single helper in engine cannot own this: every adapter
// imports engine, so engine importing them back would be a cycle.
func TestAdapterRegisterAddsOnlyItsKind(t *testing.T) {
	kinds := []engine.Kind{engine.KindDecider, engine.KindEmbedder, engine.KindGenerator}
	for _, tc := range []struct {
		adapter  string
		register func(*engine.Registry)
		kind     engine.Kind
	}{
		{"decision", decision.Register, engine.KindDecider},
		{"embed", embed.Register, engine.KindEmbedder},
		{"generate", generate.Register, engine.KindGenerator},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			reg := engine.NewRegistry()
			tc.register(reg)
			if got := reg.Protocols(tc.kind); len(got) == 0 {
				t.Fatalf("no %s protocol was registered", tc.kind)
			}
			for _, other := range kinds {
				if other == tc.kind {
					continue
				}
				if got := reg.Protocols(other); len(got) != 0 {
					t.Errorf("registering %s also registered %s protocols %v", tc.kind, other, got)
				}
			}
		})
	}
}

// TestUnregisteredKindIsRefused: GeneratorFor builds from a provider record
// without a catalog, so it exposes the registry miss directly. A registry
// serving the other two kinds must still refuse a generator when
// generate.Register was never called, naming the protocol instead of
// substituting another kind's factory.
func TestUnregisteredKindIsRefused(t *testing.T) {
	reg := engine.NewRegistry()
	decision.Register(reg)
	embed.Register(reg)
	_, err := reg.GeneratorFor(engine.Provider{ID: "gen", Protocol: engine.ProtocolOpenAIChat}, engine.Deps{})
	if err == nil {
		t.Fatal("a generator was built without generate.Register")
	}
	for _, want := range []string{engine.ProtocolOpenAIChat, "supported:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
