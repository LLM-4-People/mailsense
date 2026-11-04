package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// TestShutdownJoinsThroughTheGroup: a started job is tracked by the manager's
// one join group. Shutdown waits for it, the count returns to zero, and the next
// Start is refused by the same closed gate.
func TestShutdownJoinsThroughTheGroup(t *testing.T) {
	m := NewManager(configtest.New(t, nil), nil, nil)
	j, err := m.Start("k", "quick", func(context.Context, func(Progress)) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	if got := m.group.Active(); got != 0 {
		t.Fatalf("the join group still tracks %d job(s)", got)
	}
	if _, err := m.Start("k", "after", func(context.Context, func(Progress)) error { return nil }); !errors.Is(err, ErrShutdown) {
		t.Fatalf("Start after Shutdown = %v, want ErrShutdown", err)
	}
	if got := m.group.Active(); got != 0 {
		t.Fatalf("a refused Start tracked %d job(s)", got)
	}
}
