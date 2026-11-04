package goldset

import (
	"os"
	"strings"
	"testing"
)

// TestSealUsesTheStdlibKeyIterator: the sealer sorts the stdlib iterator; a
// hand-rolled key iterator cannot come back without failing here.
func TestSealUsesTheStdlibKeyIterator(t *testing.T) {
	data, err := os.ReadFile("seal.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if strings.Contains(source, "func mapKeys(") {
		t.Fatal("seal.go re-implements map keys; use slices.Sorted(maps.Keys(m))")
	}
	if !strings.Contains(source, "slices.Sorted(maps.Keys(") {
		t.Fatal("seal.go does not sort the stdlib key iterator")
	}
}
