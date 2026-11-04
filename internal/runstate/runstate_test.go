package runstate

import (
	"context"
	"errors"
	"testing"
)

func TestOf(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	boom := errors.New("boom")
	if Of(t.Context(), nil) != Succeeded || Of(t.Context(), boom) != Failed || Of(cancelled, boom) != Cancelled || Of(cancelled, nil) != Succeeded {
		t.Fatal("Of")
	}
}
