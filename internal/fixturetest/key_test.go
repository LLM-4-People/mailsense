package fixturetest

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// TestKeyIsTheDigestSumPrefix: the cache key is the raw form of the one short
// hash, not a private SHA-256.
func TestKeyIsTheDigestSumPrefix(t *testing.T) {
	for _, input := range []string{"", "a", "the quick brown fox"} {
		if got, want := Key(input), [8]byte(digest.Sum([]byte(input), 8)); got != want {
			t.Fatalf("Key(%q) = %x, want %x", input, got, want)
		}
	}
	data, err := os.ReadFile("vectorcache.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "crypto/sha256") {
		t.Fatal("vectorcache.go hashes by hand; use digest.Sum")
	}
}
