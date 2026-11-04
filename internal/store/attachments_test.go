package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

func TestPruneAttachmentBlobsUsesThePartialIndex(t *testing.T) {
	s, _ := open(t, nil)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN " + sqlPruneAttachmentBlobs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, detail := range plan {
		if strings.Contains(detail, "attachments_sha256") {
			return
		}
	}
	t.Fatalf("prune plan does not use attachments_sha256: %q", plan)
}

func inlineMessage(id string, data []byte) mail.Message {
	m := mailtest.Message("src", id)
	m.BodyHTMLKnown = true
	m.BodyHTML = `<p><img src="cid:logo@example.test" alt="logo"></p>`
	m.Attachments = []mail.Attachment{{
		Name: "logo.png", ContentType: "image/png", Inline: true,
		ContentID: "logo@example.test", ContentLocation: "images/logo.png", Data: data,
	}}
	return m
}

func messageRowID(t *testing.T, s *Store, id string) int64 {
	t.Helper()
	var rowID int64
	if err := s.db.QueryRow("SELECT id FROM messages WHERE source_id = 'src' AND msg_id = ?", id).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	return rowID
}

// A signature logo repeated across a thread is stored once by SHA-256, and
// the attachment identity survives the message round trip without the bytes.
func TestInlineAssetBlobsAreContentAddressed(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{inlineMessage("a", png), inlineMessage("b", png)}); err != nil {
		t.Fatal(err)
	}
	var blobs int
	if err := s.db.QueryRow("SELECT count(*) FROM attachment_blobs").Scan(&blobs); err != nil || blobs != 1 {
		t.Fatalf("blob rows = %d, %v; one content address expected", blobs, err)
	}
	var inlineRefs, checked int
	if err := s.db.QueryRow("SELECT has_inline_refs, inline_assets_checked FROM messages WHERE msg_id = 'a'").Scan(&inlineRefs, &checked); err != nil ||
		inlineRefs != 1 || checked != 1 {
		t.Fatalf("message markers refs=%d checked=%d: %v", inlineRefs, checked, err)
	}
	got, err := s.GetMessage(ctx, "src", "store-1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments: %+v", got.Attachments)
	}
	att := got.Attachments[0]
	if att.ContentID != "logo@example.test" || att.ContentLocation != "images/logo.png" ||
		att.SHA256 == "" || len(att.Data) != 0 || att.Size != int64(len(png)) {
		t.Fatalf("attachment identity: %+v", att)
	}
	rowA := messageRowID(t, s, "a")
	rowB := messageRowID(t, s, "b")
	mailQueryVerdict(t, s, rowA, scope.Owner, scope.ReasonSentByOwner)
	mailQueryVerdict(t, s, rowB, scope.Owner, scope.ReasonSentByOwner)
	for _, row := range []int64{rowA, rowB} {
		data, err := s.AttachmentData(ctx, row, 0)
		if err != nil {
			t.Fatal(err)
		}
		if string(data.Bytes) != string(png) || data.SHA256 != att.SHA256 || data.ContentType != "image/png" {
			t.Fatalf("asset row %d: %+v", row, data)
		}
	}
	// A rewrite that drops one reference keeps the shared blob; dropping the
	// last reference removes it instead of leaking it forever.
	without := inlineMessage("a", nil)
	without.Attachments = nil
	if err := s.UpsertMessages(ctx, 0, []mail.Message{without}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM attachment_blobs").Scan(&blobs); err != nil || blobs != 1 {
		t.Fatalf("shared blob removed too early: %d %v", blobs, err)
	}
	without = inlineMessage("b", nil)
	without.Attachments = nil
	if err := s.UpsertMessages(ctx, 0, []mail.Message{without}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM attachment_blobs").Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("orphan blob kept: %d %v", blobs, err)
	}
}

func TestAttachmentDataRespectsOwnerScopeAndAvailability(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := inlineMessage("a", nil) // metadata only: an old message or a dropped part
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	row := messageRowID(t, s, "a")
	mailQueryVerdict(t, s, row, scope.Owner, scope.ReasonSentByOwner)
	if _, err := s.AttachmentData(ctx, row, 0); !errors.Is(err, ErrAssetUnavailable) {
		t.Fatalf("metadata-only attachment = %v; want ErrAssetUnavailable", err)
	}
	if _, err := s.AttachmentData(ctx, row, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent position = %v; want ErrNotFound", err)
	}
	mailQueryVerdict(t, s, row, scope.Shared, scope.ReasonSentByOwner)
	if _, err := s.AttachmentData(ctx, row, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner attachment = %v; want ErrNotFound", err)
	}
}
