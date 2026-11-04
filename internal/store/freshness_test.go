package store

import (
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestNewestReceivedAtIsTheMailboxFreshness(t *testing.T) {
	s, _ := open(t, nil)
	if at, err := s.NewestReceivedAt(t.Context()); err != nil || !at.IsZero() {
		t.Fatalf("empty store: %v %v", at, err)
	}
	seedMsgs(t, s,
		msgAt("old", mail.RoleInbox, "2026-10-01T08:00:00Z"),
		msgAt("new", mail.RoleInbox, "2026-11-03T09:30:00Z"),
		// A sent item's mail instant can be newer through sent_on; the
		// freshness evidence is still when mail was received.
		func() mail.Message {
			m := msgAt("sent-newer", mail.RoleSent, "2026-10-30T08:00:00Z")
			m.SentOn = "2026-11-03T10:00:00Z"
			return m
		}(),
	)
	at, err := s.NewestReceivedAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("newest received: got %v want %v", at, want)
	}
}

// A row that is not in the stored timestamp form fails the read: freshness is
// evidence, so a malformed stamp must not silently become the zero time (or
// be repaired by a second parser).
func TestNewestReceivedAtRejectsAMalformedTimestamp(t *testing.T) {
	s, _ := open(t, nil)
	ids := seedMsgs(t, s, msgAt("bad", mail.RoleInbox, "2026-11-03T09:30:00Z"))
	if _, err := s.db.ExecContext(t.Context(),
		"UPDATE messages SET received_at = '2026-11-03 09:30:00' WHERE id = ?", ids["bad"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewestReceivedAt(t.Context()); err == nil || !strings.Contains(err.Error(), "2026-11-03 09:30:00") {
		t.Fatalf("a malformed received_at must fail with the raw value, got %v", err)
	}
}
