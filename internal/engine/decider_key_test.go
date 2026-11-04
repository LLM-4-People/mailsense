package engine

import "testing"

// TestDeciderKeyIsTheOneIdentity: the provider record and the live decider
// name the same paid cache entry. The shape lives once, so a model or
// fingerprint change cannot reach one caller only.
func TestDeciderKeyIsTheOneIdentity(t *testing.T) {
	if got, want := DeciderKey("provider.example.test", "deepseek-v4.1", "fp-1"), "provider.example.test/deepseek-v4.1/fp-1"; got != want {
		t.Fatalf("DeciderKey = %q, want %q", got, want)
	}
	p := Provider{ID: "provider.example.test", Model: "deepseek-v4.1", Protocol: "openai", BaseURL: "https://api.example.test"}
	if got, want := p.Key(), DeciderKey(p.ID, p.Model, p.Fingerprint()); got != want {
		t.Fatalf("Provider.Key = %q, want the shared identity %q", got, want)
	}
}
