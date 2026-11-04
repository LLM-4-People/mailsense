package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// xorCipher stands in for DPAPI; failOn makes it fail.
type xorCipher struct{ fail bool }

func (c xorCipher) protect(p []byte) ([]byte, error) {
	if c.fail {
		return nil, errors.New("boom")
	}
	out := bytes.Clone(p)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out, nil
}

func (c xorCipher) unprotect(s []byte) ([]byte, error) { return c.protect(s) }

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.bin")
	s, err := openFileStore(path, xorCipher{}, noRetry)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("ANTHROPIC_API_KEY", "sk-test-value-123"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("SUBQ_KEY", "q"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "sk-test-value-123") {
		t.Fatal("file must not contain plaintext")
	}
	reopened, err := openFileStore(path, xorCipher{}, noRetry)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := reopened.Get("ANTHROPIC_API_KEY"); v != "sk-test-value-123" {
		t.Fatalf("get: %q", v)
	}
	if names, _ := reopened.List(); len(names) != 2 || names[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("list: %v", names)
	}
	if err := reopened.Delete("SUBQ_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get("SUBQ_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if err := reopened.Delete("SUBQ_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestFileStoreMisuse(t *testing.T) {
	dir := t.TempDir()
	s, _ := openFileStore(filepath.Join(dir, "s.bin"), xorCipher{}, noRetry)
	for _, bad := range []string{"", "lower", "WITH SPACE", "../X", strings.Repeat("A", 65)} {
		if err := s.Set(bad, "v"); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
		if _, err := s.Get(bad); err == nil {
			t.Errorf("get %q must be rejected", bad)
		}
	}
	if err := s.Set("A", ""); err == nil {
		t.Error("empty value must be rejected")
	}
	failing, _ := openFileStore(filepath.Join(dir, "f.bin"), xorCipher{fail: true}, noRetry)
	if err := failing.Set("A", "v"); err == nil {
		t.Error("encryption failure must surface")
	}
	if _, err := failing.Get("A"); !errors.Is(err, ErrNotFound) {
		t.Error("a failed Set must not change the store")
	}
	corrupt := filepath.Join(dir, "c.bin")
	_ = os.WriteFile(corrupt, []byte("not json"), 0o600)
	if _, err := openFileStore(corrupt, xorCipher{}, noRetry); err == nil {
		t.Error("corrupt file must be rejected")
	}
	if _, err := openFileStore(corrupt, xorCipher{fail: true}, noRetry); err == nil {
		t.Error("undecryptable file must be rejected")
	}
	null, _ := xorCipher{}.protect([]byte(`null`))
	_ = os.WriteFile(corrupt, null, 0o600)
	if _, err := openFileStore(corrupt, xorCipher{}, noRetry); err == nil {
		t.Error("a JSON null secret file must be rejected")
	}
	badName, _ := xorCipher{}.protect([]byte(`{"lower":"x"}`))
	_ = os.WriteFile(corrupt, badName, 0o600)
	if _, err := openFileStore(corrupt, xorCipher{}, noRetry); err == nil {
		t.Error("invalid stored names must be rejected")
	}
}

func noRetry() fsx.Replace { return fsx.Replace{} }

func TestObservedStorePublishesAndRegisters(t *testing.T) {
	inner, _ := openFileStore(filepath.Join(t.TempDir(), "s.bin"), xorCipher{}, noRetry)
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicChanged, 4)
	s := &observed{Store: inner, bus: bus}
	if s.Kind() != "dpapi" || s.ReadOnly() {
		t.Fatal("kind")
	}
	if err := s.Set("API_KEY", "observed-secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("API_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("API_KEY"); err == nil {
		t.Fatal("double delete must fail and publish nothing")
	}
	if a, b := <-sub.C, <-sub.C; a.Action != "set" || b.Action != "delete" || a.Name != "API_KEY" || len(sub.C) != 0 {
		t.Fatalf("events %+v %+v", a, b)
	}
}
