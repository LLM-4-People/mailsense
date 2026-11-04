package source

import (
	"errors"
	"fmt"
	"testing"
)

func TestUnavailableMatchesThroughWrapping(t *testing.T) {
	named := Unavailable("app: gone")
	if named.Error() != "app: gone" {
		t.Fatalf("message %q", named.Error())
	}
	wrapped := fmt.Errorf("syncer: details: %w", fmt.Errorf("%w: 0x800706BE", named))
	if !errors.Is(wrapped, ErrUnavailable) || !errors.Is(wrapped, named) {
		t.Fatal("a wrapped named error must match both itself and ErrUnavailable")
	}
	if errors.Is(errors.Join(errors.New("other")), ErrUnavailable) {
		t.Fatal("an unrelated error must not match")
	}
	if errors.Is(Unavailable("a"), Unavailable("a")) {
		t.Fatal("two named errors are distinct sentinels")
	}
}
