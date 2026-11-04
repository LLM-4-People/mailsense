package store

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// TestFoldersProjectionHasOneOwner: a message's further folders are selected by
// one projection. The item read and the detail base read used to spell the same
// JSON subquery twice, so a change to the pair reached one read only.
func TestFoldersProjectionHasOneOwner(t *testing.T) {
	items, err := os.ReadFile("items.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(items), "const foldersSQL ="); got != 1 {
		t.Fatalf("items.go defines foldersSQL %d times, want once", got)
	}
	if got := strings.Count(string(items), "json_group_array(json_array(f2.path, f2.role))"); got != 1 {
		t.Errorf("items.go spells the folders projection %d times, want once in foldersSQL", got)
	}
	details, err := os.ReadFile("details.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(details), "json_group_array(json_array(f2.path, f2.role))") {
		t.Error("details.go spells the folders projection itself; use foldersSQL")
	}
	if !strings.Contains(string(details), "foldersSQL") {
		t.Error("details.go does not read the shared folders projection")
	}
}

// TestFoldersReadAgreesBetweenItemsAndDetailBase: both reads return the same
// further folders for one message, in the same path order.
func TestFoldersReadAgreesBetweenItemsAndDetailBase(t *testing.T) {
	s, _ := open(t, nil)
	ids := seedMsgs(t, s, mailtest.Message("src", "msg1"))
	rowID := ids["msg1"]
	ftsExec(t, s, `INSERT INTO folders (source_id, store_id, store_name, store_kind, path, role, remote_id, updated_at)
		VALUES ('src', 'store-1', 'Amélie', 'other', 'Archive', 'archive', 'r1', '2026-10-12T00:00:00Z')`)
	ftsExec(t, s, `INSERT INTO message_folders (message_id, folder_id)
		SELECT ?, id FROM folders WHERE source_id = 'src' AND path = 'Archive'`, rowID)

	var item mail.Message
	if err := readItems(t.Context(), s.db, rowID, &item); err != nil {
		t.Fatal(err)
	}
	bases, err := loadDetailBases(t.Context(), s.db, "src", []source.ItemRef{{StoreID: "store-1", ID: "msg1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bases) != 1 {
		t.Fatalf("loadDetailBases returned %d bases, want one", len(bases))
	}
	var base mail.Message
	for _, b := range bases {
		base = b.m
	}
	if len(item.Folders) != 1 || len(base.Folders) != 1 {
		t.Fatalf("folders differ: items %v, detail base %v", item.Folders, base.Folders)
	}
	if item.Folders[0] != base.Folders[0] {
		t.Fatalf("folders differ: items %v, detail base %v", item.Folders, base.Folders)
	}
	if got, want := item.Folders[0].Path, "Archive"; got != want {
		t.Fatalf("folder path = %q, want %q", got, want)
	}
}
