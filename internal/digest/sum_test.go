package digest

import (
	"encoding/hex"
	"testing"
)

// TestSumIsTheRawHexPrefix: Sum and Hex are the same SHA-256 prefix, one raw
// and one written in hex.
func TestSumIsTheRawHexPrefix(t *testing.T) {
	data := []byte("mailsense")
	for _, n := range []int{0, -1, 1, 8, 16, 32, 33} {
		sum := Sum(data, n)
		want := 32
		if n > 0 && n < 32 {
			want = n
		}
		if len(sum) != want {
			t.Fatalf("Sum(data, %d) has %d bytes, want %d", n, len(sum), want)
		}
		if full := Sum(data, 0); len(full) != 32 || string(sum) != string(full[:len(sum)]) {
			t.Fatalf("Sum(data, %d) is not a prefix of the full digest", n)
		}
		hexPrefix := hex.EncodeToString(sum)
		if got := Hex(data, len(hexPrefix)); got != hexPrefix {
			t.Fatalf("Hex(data, %d) = %q, Sum says %q", len(hexPrefix), got, hexPrefix)
		}
	}
	if Hex(data, 0) != hex.EncodeToString(Sum(data, 0)) {
		t.Fatal("Hex(data, 0) is not the full digest")
	}
}
