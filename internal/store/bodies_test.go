package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func withHTML(m mail.Message, html string, truncated bool) mail.Message {
	m.BodyHTML, m.BodyHTMLKnown, m.BodyHTMLTruncated = html, true, truncated
	return m
}

func TestBodyHTMLRoundTripAndPreservation(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := withHTML(mailtest.Message("src", "1"), "<p>rich <b>body</b></p>", true)
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMessage(ctx, "src", "store-1", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.BodyHTML != m.BodyHTML || !got.BodyHTMLKnown || !got.BodyHTMLTruncated {
		t.Fatalf("round trip %+v", got)
	}
	if got.Body != m.Body {
		t.Fatalf("plain body changed: %q", got.Body)
	}

	// A writer that does not know HTML (archive, an older connector) must
	// not erase the stored one.
	blind := mailtest.Message("src", "1")
	blind.Body = "new plain body"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{blind}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetMessage(ctx, "src", "store-1", "1"); got.BodyHTML != m.BodyHTML || !got.BodyHTMLKnown || !got.BodyHTMLTruncated {
		t.Fatalf("unknown write erased html: %+v", got)
	}

	// A reader that declares no HTML for a plain message replaces it with an
	// empty known body (a mail can genuinely have no HTML part).
	plain := withHTML(mailtest.Message("src", "1"), "", false)
	plain.Body = "plain only"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{plain}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetMessage(ctx, "src", "store-1", "1"); got.BodyHTML != "" || !got.BodyHTMLKnown || got.BodyHTMLTruncated {
		t.Fatalf("known empty html not stored: %+v", got)
	}
}

func TestPartialMergeKeepsBodyHTML(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	full := withHTML(mailtest.Message("src", "1"), "<p>kept</p>", false)
	full.Stamp = "s1"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{full}); err != nil {
		t.Fatal(err)
	}
	p := partial("1", full.ReceivedAt, full.Stamp)
	if err := s.UpsertMessages(ctx, 0, []mail.Message{p}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetMessage(ctx, "src", "store-1", "1")
	if m.Partial || m.BodyHTML != "<p>kept</p>" || !m.BodyHTMLKnown {
		t.Fatalf("partial merge lost html: %+v", m)
	}
}

func TestEachMessageOnlyLoadsHTMLEWhenAsked(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := withHTML(mailtest.Message("src", "1"), "<p>display</p>", false)
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	var plain, rich mail.Message
	if err := s.EachMessage(ctx, MessageFilter{RowIDs: []int64{1}}, func(r *MessageRow) error { plain = r.Message; return nil }); err != nil {
		t.Fatal(err)
	}
	if plain.BodyHTML != "" || plain.BodyHTMLKnown {
		t.Fatalf("a list read loaded display html: %+v", plain)
	}
	if err := s.EachMessage(ctx, MessageFilter{RowIDs: []int64{1}, WithBodyHTML: true}, func(r *MessageRow) error { rich = r.Message; return nil }); err != nil {
		t.Fatal(err)
	}
	if rich.BodyHTML != "<p>display</p>" || !rich.BodyHTMLKnown {
		t.Fatalf("a render read did not load display html: %+v", rich)
	}
}

func TestMissingBodyHTMLAndApply(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	msgs := []mail.Message{mailtest.Message("src", "a"), mailtest.Message("src", "b"), withHTML(mailtest.Message("src", "known"), "<p>x</p>", false)}
	unpicked := mailtest.Message("src", "hidden")
	unpicked.StoreID = "store-3"
	msgs = append(msgs, unpicked)
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := s.db.QueryRow("SELECT revision FROM messages WHERE msg_id = 'a'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountBackfill(ctx, "src"); err != nil || n != 2 {
		t.Fatalf("count %d %v", n, err)
	}
	first, err := s.BackfillNewest(ctx, "src", BackfillCursor{}, 1)
	if err != nil || len(first) != 1 || first[0].Key.ID == "" {
		t.Fatalf("first %+v %v", first, err)
	}
	rest, err := s.BackfillNewest(ctx, "src", BackfillCursor{Instant: first[0].Instant, RowID: first[0].RowID}, 10)
	if err != nil || len(rest) != 1 || rest[0].Key.ID == "known" || rest[0].Key.ID == "hidden" {
		t.Fatalf("rest %+v %v", rest, err)
	}
	if all, _ := s.BackfillNewest(ctx, "src", BackfillCursor{}, 10); len(all) != 2 {
		t.Fatalf("all %+v", all)
	}

	updates := []BackfillUpdate{
		{Key: first[0].Key, HTML: "<p>one</p>", Known: true},
		{Key: source.ItemRef{StoreID: "store-1", ID: "gone"}, HTML: "<p>gone</p>", Known: true},
		{Key: rest[0].Key, HTML: "", Known: true},
	}
	n, err := s.ApplyBackfill(ctx, "src", updates)
	if err != nil || n != 2 {
		t.Fatalf("apply %d %v", n, err)
	}
	if n, _ := s.CountBackfill(ctx, "src"); n != 0 {
		t.Fatalf("still missing %d", n)
	}
	var after int64
	_ = s.db.QueryRow("SELECT revision FROM messages WHERE msg_id = 'a'").Scan(&after)
	if after != before {
		t.Fatalf("display html bumped revision %d -> %d", before, after)
	}
	got, _ := s.GetMessage(ctx, "src", "store-1", first[0].Key.ID)
	if got.BodyHTML != "<p>one</p>" || !got.BodyHTMLKnown {
		t.Fatalf("applied html %+v", got)
	}
	got, _ = s.GetMessage(ctx, "src", "store-1", rest[0].Key.ID)
	if got.BodyHTML != "" || !got.BodyHTMLKnown {
		t.Fatalf("known empty html %+v", got)
	}
	// The plain body is still the learning/search body.
	if res, _ := s.Search(ctx, "peaufiner", 10); res.Total != 3 {
		t.Fatalf("search changed: %d", res.Total)
	}
}

// The backfill starts with the mail the owner opens first: the newest
// instant, with a sent item's SentOn, and the cursor continues backwards.
func TestBackfillNewestOrdersRecentFirst(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	old := mailtest.Message("src", "old")
	old.ReceivedAt = "2025-01-01T00:00:00Z"
	recent := mailtest.Message("src", "recent")
	recent.ReceivedAt = "2026-01-01T00:00:00Z"
	sent := mailtest.Message("src", "sent")
	sent.FolderRole, sent.FolderPath = mail.RoleSent, "Sent"
	sent.ReceivedAt, sent.SentOn = "2025-06-01T00:00:00Z", "2026-06-01T00:00:00Z"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{old, recent, sent}); err != nil {
		t.Fatal(err)
	}
	first, err := s.BackfillNewest(ctx, "src", BackfillCursor{}, 1)
	if err != nil || len(first) != 1 || first[0].Key.ID != "sent" {
		t.Fatalf("first %+v %v", first, err)
	}
	rest, err := s.BackfillNewest(ctx, "src", BackfillCursor{Instant: first[0].Instant, RowID: first[0].RowID}, 10)
	if err != nil || len(rest) != 2 || rest[0].Key.ID != "recent" || rest[1].Key.ID != "old" {
		t.Fatalf("rest %+v %v", rest, err)
	}
}

// The backfill base read is bounded: the old loop issued one SELECT per
// update inside the write transaction.
func TestBackfillBaseReadIsBounded(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	var msgs []mail.Message
	var updates []BackfillUpdate
	for i := range 50 {
		m := mailtest.Message("src", fmt.Sprintf("m%02d", i))
		msgs = append(msgs, m)
		updates = append(updates, BackfillUpdate{
			Key: source.ItemRef{StoreID: m.StoreID, ID: m.ID}, HTML: "<p>x</p>", Known: true,
		})
	}
	seedMsgs(t, s, msgs...)
	counter := &countingQueryer{queryer: s.db}
	bases, err := loadBackfillBases(ctx, counter, "src", updates)
	if err != nil {
		t.Fatal(err)
	}
	if len(bases) != len(updates) {
		t.Fatalf("read %d of %d bases", len(bases), len(updates))
	}
	if counter.queries != 1 {
		t.Fatalf("the batch made %d reads for %d updates, want 1", counter.queries, len(updates))
	}
}

func TestApplyBackfillNeverOverwritesKnownBody(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "1")}); err != nil {
		t.Fatal(err)
	}
	key := source.ItemRef{StoreID: "store-1", ID: "1"}
	if n, err := s.ApplyBackfill(ctx, "src", []BackfillUpdate{{Key: key, HTML: "<p>first read</p>", Known: true}}); err != nil || n != 1 {
		t.Fatalf("first apply %d %v", n, err)
	}
	// A concurrent sync marked the row known after the backfill listed it.
	// The backfill's older read must not replace the fresher HTML.
	if n, err := s.ApplyBackfill(ctx, "src", []BackfillUpdate{{Key: key, HTML: "<p>stale read</p>", Known: true}}); err != nil || n != 0 {
		t.Fatalf("second apply %d %v", n, err)
	}
	got, err := s.GetMessage(ctx, "src", "store-1", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.BodyHTML != "<p>first read</p>" || !got.BodyHTMLKnown {
		t.Fatalf("known html overwritten: %+v", got)
	}
}

func TestApplyBackfillRejectsBadInput(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "1")}); err != nil {
		t.Fatal(err)
	}
	key := source.ItemRef{StoreID: "store-1", ID: "1"}
	if _, err := s.ApplyBackfill(ctx, "src", []BackfillUpdate{{Key: key, HTML: "<p>\xff</p>", Known: true}}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := s.ApplyBackfill(ctx, "src", []BackfillUpdate{{Key: key, HTML: "<p>x</p>"}}); err == nil {
		t.Fatal("html without a declared read accepted")
	}
	if n, _ := s.CountBackfill(ctx, "src"); n != 1 {
		t.Fatalf("a rejected batch changed the store: %d", n)
	}
}

func TestBackfillRepairsInlineAssetsOfOldMail(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := withHTML(mailtest.Message("src", "old"), `<p><img src="cid:logo@example.test"></p>`, false)
	m.Attachments = []mail.Attachment{{
		Name: "logo.png", ContentType: "image/png", Inline: true,
		ContentID: "logo@example.test", // no bytes: stored by the older build
	}}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	// Simulate a row migrated from the build before inline assets existed.
	if _, err := s.db.Exec("UPDATE messages SET inline_assets_checked = 0 WHERE msg_id = 'old'"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountBackfill(ctx, "src"); err != nil || n != 1 {
		t.Fatalf("candidate count %d %v", n, err)
	}
	refs, err := s.BackfillNewest(ctx, "src", BackfillCursor{}, 10)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs %+v %v", refs, err)
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	n, err := s.ApplyBackfill(ctx, "src", []BackfillUpdate{{
		Key: refs[0].Key, HTML: m.BodyHTML, Known: true,
		Attachments: []mail.Attachment{{
			Name: "logo.png", ContentType: "image/png", Inline: true,
			ContentID: "logo@example.test", Data: png,
		}},
		AssetsKnown: true,
	}})
	if err != nil || n != 1 {
		t.Fatalf("apply %d %v", n, err)
	}
	if n, _ := s.CountBackfill(ctx, "src"); n != 0 {
		t.Fatalf("repaired message still selected: %d", n)
	}
	var inlineRefs int
	if err := s.db.QueryRow("SELECT has_inline_refs FROM messages WHERE msg_id = 'old'").Scan(&inlineRefs); err != nil || inlineRefs != 1 {
		t.Fatalf("inline refs flag after backfill = %d, %v; want 1", inlineRefs, err)
	}
	got, err := s.GetMessage(ctx, "src", "store-1", "old")
	if err != nil || len(got.Attachments) != 1 || got.Attachments[0].SHA256 == "" {
		t.Fatalf("attachment identity after backfill: %+v %v", got.Attachments, err)
	}
	row := messageRowID(t, s, "old")
	mailQueryVerdict(t, s, row, scope.Owner, scope.ReasonSentByOwner)
	asset, err := s.AttachmentData(ctx, row, 0)
	if err != nil || string(asset.Bytes) != string(png) {
		t.Fatalf("backfilled bytes %+v %v", asset, err)
	}
}

func TestBodyHTMLTruncatedFlagIsPerMessage(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := withHTML(mailtest.Message("src", "1"), "<p>"+strings.Repeat("x", 10)+"</p>", true)
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetMessage(ctx, "src", "store-1", "1")
	if !got.BodyHTMLTruncated {
		t.Fatal("truncation flag lost")
	}
}
