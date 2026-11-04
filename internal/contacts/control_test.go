package contacts

import "testing"

// TestValidRejectsC1Control: the address check is the mail owner's
// unicode.IsControl predicate, so U+0080..U+009F are refused like every other
// control character (the predicate mailquery and calendar now share).
func TestValidRejectsC1Control(t *testing.T) {
	for _, r := range []rune{0x00, 0x1f, 0x7f, 0x80, 0x85, 0x9f} {
		if Valid("ada" + string(r) + "@example.test") {
			t.Fatalf("an address with U+%04X was accepted", r)
		}
	}
	if !Valid("ada@example.test") || !Valid(" Ada@Example.test ") {
		t.Fatal("a normal address was refused")
	}
}
