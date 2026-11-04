package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFileStoreOptionalFile: an absent secret file is a writable empty store
// (first run), while a path that exists but cannot be read as a file is an
// error. The store used to spell the os.ReadFile/ErrNotExist check itself;
// it now reads through fsx.ReadOptional, whose contract these two cases pin.
func TestFileStoreOptionalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.bin")
	s, err := openFileStore(path, xorCipher{}, noRetry)
	if err != nil || s == nil {
		t.Fatalf("absent file: %v %v", s, err)
	}
	if names, err := s.List(); err != nil || len(names) != 0 {
		t.Fatalf("absent file opened with %v: %v", names, err)
	}
	if err := s.Set("ANTHROPIC_API_KEY", "sk-test"); err != nil {
		t.Fatalf("an empty store is not writable: %v", err)
	}

	dirPath := filepath.Join(t.TempDir(), "secrets.bin")
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := openFileStore(dirPath, xorCipher{}, noRetry); err == nil {
		t.Fatal("a directory at the secret path opened as an empty store")
	}
}
