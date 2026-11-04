package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Production Go code must never name a vendor secret or a shipped preset:
// every key name comes from a record's api_key_secret and every provider
// from the owner's file.
func TestProductionCodeNamesNoVendorSecretOrPreset(t *testing.T) {
	root := filepath.Join("..", "..")
	forbidden := []string{
		"SUBQ_API_KEY", "PROVIDER_EXAMPLE_API_KEY", "CLOUDFLARE_API_TOKEN", "PROVIDER_EXAMPLE_API_KEY", "OPENAI_API_KEY",
		"provider.example.test-", "embeddinggemma", "openai-decisions", "clef-flash", "subq-snap", "jev-1.13", "gpt-6-luna",
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist", "out", "docs", "scripts", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, name := range forbidden {
			if strings.Contains(text, name) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s names %q: provider data belongs to engine.providers.file", rel, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
