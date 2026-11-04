package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestStoresShareOneValueContract: lookup and sortedNames are the store
// plumbing, so the file store and the environment store answer a missing name
// and a list order identically. They used to spell both themselves.
func TestStoresShareOneValueContract(t *testing.T) {
	file, err := openFileStore(filepath.Join(t.TempDir(), "secrets.bin"), xorCipher{}, noRetry)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CHARLIE_KEY", "ALPHA_KEY", "BRAVO_KEY"} {
		if err := file.Set(name, "v-"+name); err != nil {
			t.Fatal(err)
		}
	}
	stores := []struct {
		name  string
		store Store
		bravo string
	}{
		{"file", file, "v-BRAVO_KEY"},
		// Written in an order the map iteration does not keep.
		{"env", newEnvStore([]string{
			"MAILSENSE_SECRET_CHARLIE_KEY=3",
			"MAILSENSE_SECRET_ALPHA_KEY=1",
			"MAILSENSE_SECRET_BRAVO_KEY=2",
		}), "2"},
	}
	want := []string{"ALPHA_KEY", "BRAVO_KEY", "CHARLIE_KEY"}
	for _, tc := range stores {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store
			if _, err := store.Get("MISSING_KEY"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a missing name answered %v, want ErrNotFound", err)
			}
			// The one exception is a malformed name: validation, not absence.
			if _, err := store.Get("bad name"); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("an invalid name answered %v, want a validation error", err)
			}
			if value, err := store.Get("BRAVO_KEY"); err != nil || value != tc.bravo {
				t.Fatalf("Get(BRAVO_KEY) = %q, %v", value, err)
			}
			names, err := store.List()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(names, want) {
				t.Fatalf("List = %v, want %v", names, want)
			}
		})
	}
}

// TestValuePlumbingHasOneWriter: the map read and the names order live in
// values.go; a store that re-spells them can drift the absence answer or the
// order.
func TestValuePlumbingHasOneWriter(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	inlineRead := regexp.MustCompile(`ok := s\.values\[name\]`)
	readers := []string{}
	for _, entry := range files {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if inlineRead.Match(data) {
			readers = append(readers, name)
		}
	}
	if len(readers) != 0 {
		t.Fatalf("the stores read their map inline in %v; they belong to lookup in values.go", readers)
	}
	owner, err := os.ReadFile("values.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(owner), "v, ok := values[name]") {
		t.Fatal("lookup no longer owns the one map read")
	}
}
