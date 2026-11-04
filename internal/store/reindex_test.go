package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Review L0 rules §7: entries written under another ftsVersion (an older
// fold) are rewritten when the store opens, in batches, and the version is
// recorded; a store already at the version is left alone.
func TestOpenReindexesAnOlderFullTextForm(t *testing.T) {
	s, cfg := open(t, map[string]any{"sync.batch_size": 2})
	ctx := t.Context()
	var batch []mail.Message
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		m := mailtest.Message("src", id)
		m.Body = "ｶﾀﾛｸﾞ réunion " + id
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	// Simulate an index written by an older fold: wrong entries and a stale
	// folded-text version (the coverage version stays current, so only a
	// text-version change forces the full rebuild).
	if _, err := s.db.ExecContext(ctx, "DELETE FROM messages_fts; DELETE FROM messages_fts_tri; UPDATE meta SET value = 'old' WHERE key = 'fts_text_version'"); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Search(ctx, "カタログ", 10); res.Total != 0 {
		t.Fatalf("the index is gone: %+v", res)
	}
	_ = s.Close()
	again, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	for _, q := range []string{"カタログ", "reunion", "ｶﾀ"} {
		if res, err := again.Search(ctx, q, 10); err != nil || res.Total != 5 {
			t.Fatalf("%q after the re-index: %+v %v", q, res, err)
		}
	}
	var v string
	if err := again.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'fts_version'").Scan(&v); err != nil || v != ftsVersion {
		t.Fatalf("version %q %v", v, err)
	}
}

// Round 30: a store written before the Indic scripts joined the substring
// coverage has no trigram rows for their messages. The version bump makes
// Open rewrite both indexes once: every Devanagari message gets a trigram
// row and the exact matra is found by the scan.
func TestOpenReindexesIndicSubstringCoverage(t *testing.T) {
	s, cfg := open(t, map[string]any{"sync.batch_size": 1})
	ctx := t.Context()
	var batch []mail.Message
	for _, d := range []struct{ id, body string }{{"ki", "कि"}, {"kii", "की"}} {
		m := mailtest.Message("src", d.id)
		m.Body = d.body
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	// Simulate a store written under the old coverage: no trigram rows. The
	// folded-text version stays current, so this is a trigram-only reindex.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM messages_fts_tri; UPDATE meta SET value = 'old' WHERE key = 'fts_coverage_version'"); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Search(ctx, "कि", 10); res.Total != 0 {
		t.Fatalf("the trigram index is gone: %+v", res)
	}
	_ = s.Close()
	again, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	var tri int
	if err := again.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts_tri").Scan(&tri); err != nil || tri != 2 {
		t.Fatalf("trigram rows %d %v, want one per Devanagari message", tri, err)
	}
	res, err := again.Search(ctx, "कि", 10)
	if err != nil || res.Total != 1 || len(res.Hits) != 1 || res.Hits[0].ID != "ki" || res.Terms[0].Mode != ModeScan {
		t.Fatalf("after the re-index: %+v %v", res, err)
	}
}
