package store

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// A connector's kept state (source.StateKeeper: the Outlook breaker)
// survives reopening the database; misuse is refused.
func TestSourceStateIsKept(t *testing.T) {
	s, cfg := open(t, nil)
	ctx := t.Context()
	if v, err := s.LoadState(ctx, "outlook", "breaker"); err != nil || v != nil {
		t.Fatalf("nothing kept yet: %q %v", v, err)
	}
	for _, v := range []string{`{"losses":["2026-10-15T10:00:00Z"]}`, `{"stopped":true}`} {
		if err := s.SaveState(ctx, "outlook", "breaker", []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s2, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, err := s2.LoadState(ctx, "outlook", "breaker"); err != nil || string(v) != `{"stopped":true}` {
		t.Fatalf("after reopening: %q %v", v, err)
	}
	if v, _ := s2.LoadState(ctx, "other", "breaker"); v != nil {
		t.Fatal("states are per source")
	}
	if err := s2.SaveState(ctx, "", "breaker", nil); err == nil {
		t.Fatal("a state without a source id must be refused")
	}
	if err := s2.SaveState(ctx, "outlook", "", nil); err == nil {
		t.Fatal("a state without a name must be refused")
	}
}

// The detail quarantine: a set-aside message leaves the pending queue at
// its revision, is counted, comes back with a new revision or the user's
// clear; an unknown message is no error.
func TestDetailQuarantine(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	a, b := partial("a", "2026-10-02T00:00:00Z", "x"), partial("b", "2026-10-01T00:00:00Z", "x")
	if err := s.UpsertMessages(ctx, 0, []mail.Message{a, b}); err != nil {
		t.Fatal(err)
	}
	ka := source.ItemRef{StoreID: a.StoreID, ID: "a"}
	if err := s.QuarantineDetails(ctx, "src", ka, "0x80010105"); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineDetails(ctx, "src", source.ItemRef{StoreID: a.StoreID, ID: "gone"}, "x"); err != nil {
		t.Fatalf("an unknown message: %v", err)
	}
	keys, _ := s.PendingDetails(ctx, "src", 5, time.Time{})
	if n, _ := s.CountPending(ctx, "src", time.Time{}); len(keys) != 1 || keys[0].ID != "b" || n != 1 {
		t.Fatalf("pending %+v (%d)", keys, n)
	}
	if n, err := s.CountQuarantined(ctx, "src"); err != nil || n != 1 {
		t.Fatalf("set aside %d %v", n, err)
	}
	// A new revision of the message (it changed in the source) is pending again.
	a.Subject = "changed"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{a}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountQuarantined(ctx, "src"); n != 0 {
		t.Fatalf("a new revision is no longer set aside: %d", n)
	}
	if err := s.QuarantineDetails(ctx, "src", ka, "0x80010105"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ClearQuarantine(ctx, "src"); err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if n, _ := s.CountPending(ctx, "src", time.Time{}); n != 2 {
		t.Fatalf("after the clear: %d pending", n)
	}
}
