package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestSetCursorsSkipsEmptyAndIsAtomic(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	f := func(path string) mail.Folder {
		return mail.Folder{SourceID: "src", StoreID: "s", StoreName: "S", Path: path, Role: mail.RoleInbox, StoreKind: mail.StoreOther}
	}
	first, err := s.UpsertFolder(ctx, f("Inbox"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.UpsertFolder(ctx, f("Sent"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetCursors(ctx, map[int64]string{first: "c1", second: ""}); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := s.Cursor(ctx, first); !ok || c != "c1" {
		t.Fatalf("first cursor %q %v", c, ok)
	}
	if _, ok, _ := s.Cursor(ctx, second); ok {
		t.Fatal("an empty cursor must not create a row")
	}

	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER set_cursors_fail BEFORE INSERT ON sync_cursors
		WHEN NEW.cursor = 'boom' BEGIN SELECT RAISE(ABORT, 'cursor blocked'); END`); err != nil {
		t.Fatal(err)
	}
	err = s.SetCursors(ctx, map[int64]string{first: "c2", second: "boom"})
	if err == nil {
		t.Fatal("the failing cursor must fail the transaction")
	}
	if c, ok, _ := s.Cursor(ctx, first); !ok || c != "c1" {
		t.Fatalf("the first cursor must roll back with the transaction: %q %v", c, ok)
	}
	if _, ok, _ := s.Cursor(ctx, second); ok {
		t.Fatal("the failed transaction must insert nothing")
	}
}
