package search

import "testing"

// The query vector estimate is exact: one float32 per dimension. Pin it so a
// budget change and the 64-entry window cannot silently disagree about the
// largest pinned model.
func TestQueryVectorWeighIsExact(t *testing.T) {
	for _, dims := range []int{1, 384, 768, 1024, 4096} {
		if got, want := weighQueryVector(make([]float32, dims)), dims*4; got != want {
			t.Fatalf("a %d-dimension vector weighs %d, want %d", dims, got, want)
		}
	}
	if got := queryCacheSize * weighQueryVector(make([]float32, 4096)); got > queryCacheMaxBytes {
		t.Fatalf("the %d-entry window of 4096-dimension vectors weighs %d, over the %d-byte budget",
			queryCacheSize, got, queryCacheMaxBytes)
	}
}
