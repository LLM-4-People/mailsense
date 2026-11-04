package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Round 31: the single FTS version split into a folded-text version and a
// trigram-coverage version. A text change rewrites both indexes; a coverage
// change scans the messages and rewrites only the trigram rows.

func ftsTestStore(t *testing.T, overrides map[string]any, messages []mail.Message) (*Store, *config.Config) {
	t.Helper()
	s, cfg := open(t, overrides)
	if len(messages) > 0 {
		if err := s.UpsertMessages(t.Context(), 0, messages); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	return s, cfg
}

func reopenFTSStore(t *testing.T, cfg *config.Config) *Store {
	t.Helper()
	s, err := Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ftsTestMessage(id string) mail.Message { return mailtest.Message("src", id) }

func ftsExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func ftsRowID(t *testing.T, s *Store, msgID string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT id FROM messages WHERE msg_id = ?", msgID).Scan(&id); err != nil {
		t.Fatalf("row id of %q: %v", msgID, err)
	}
	return id
}

func ftsRowIDs(t *testing.T, s *Store, table string) []int64 {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "SELECT rowid FROM "+table+" ORDER BY rowid")
	if err != nil {
		t.Fatalf("rowids of %s: %v", table, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

const (
	ftsWordIndexWhere = "name LIKE 'messages_fts%' AND name NOT LIKE 'messages_fts_tri%'"
	ftsTriIndexWhere  = "name LIKE 'messages_fts_tri%'"
)

func ftsDBStat(t *testing.T, s *Store, where string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE "+where).Scan(&n); err != nil {
		t.Fatalf("dbstat: %v", err)
	}
	return n
}

func ftsMetaValue(t *testing.T, s *Store, key string) (string, bool) {
	t.Helper()
	var v string
	err := s.db.QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("meta %s: %v", key, err)
	}
	return v, true
}

func ftsSetVersion(t *testing.T, s *Store, key, value string) {
	t.Helper()
	ftsExec(t, s, "INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
}

func ftsSetVersions(t *testing.T, s *Store, text, coverage, legacy string) {
	t.Helper()
	ftsSetVersion(t, s, metaFTSTextVersion, text)
	ftsSetVersion(t, s, metaFTSCoverageVersion, coverage)
	ftsSetVersion(t, s, metaFTSVersion, legacy)
}

// ftsLegacyVersions removes the split keys and leaves one legacy value, as a
// store written before the split has it.
func ftsLegacyVersions(t *testing.T, s *Store, legacy string) {
	t.Helper()
	ftsExec(t, s, "DELETE FROM meta WHERE key IN (?, ?)", metaFTSTextVersion, metaFTSCoverageVersion)
	ftsSetVersion(t, s, metaFTSVersion, legacy)
}

func ftsClearVersions(t *testing.T, s *Store) {
	t.Helper()
	ftsExec(t, s, "DELETE FROM meta WHERE key IN (?, ?, ?)", metaFTSTextVersion, metaFTSCoverageVersion, metaFTSVersion)
}

func ftsAssertVersions(t *testing.T, s *Store, text, coverage, legacy string) {
	t.Helper()
	for _, want := range []struct{ key, value string }{
		{metaFTSTextVersion, text},
		{metaFTSCoverageVersion, coverage},
		{metaFTSVersion, legacy},
	} {
		got, ok := ftsMetaValue(t, s, want.key)
		if !ok || got != want.value {
			t.Errorf("meta %s = %q (present %v), want %q", want.key, got, ok, want.value)
		}
	}
}

func ftsAssertIDs(t *testing.T, label string, got, want []int64) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func ftsContainsAll(t *testing.T, label string, got, want []int64) {
	t.Helper()
	for _, id := range want {
		if !slices.Contains(got, id) {
			t.Fatalf("%s = %v, does not contain %d", label, got, id)
		}
	}
}

func TestSplitLegacyFTSVersion(t *testing.T) {
	for _, tc := range []struct {
		name           string
		in             string
		text, coverage string
	}{
		{"missing", "", "", ""},
		{"word form", "3-nfkc-casefold", ftsTextVersion, ftsTextVersion},
		{"coverage form", "4-indic-substring", ftsTextVersion, ftsCoverageVersion},
		{"older", "2-fold", "", ""},
		{"unknown", "99-future", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, coverage := splitLegacyFTSVersion(tc.in)
			if text != tc.text || coverage != tc.coverage {
				t.Fatalf("splitLegacyFTSVersion(%q) = (%q, %q), want (%q, %q)",
					tc.in, text, coverage, tc.text, tc.coverage)
			}
		})
	}
}

// A legacy version-3 store has trigram rows for Hangul but not for the Indic
// scripts. Opening it rewrites only the trigram coverage: the word index
// rowids and allocated bytes stay identical.
func TestTriOnlyReindexOnLegacyStore(t *testing.T) {
	latin, indic, hangul := ftsTestMessage("latin"), ftsTestMessage("indic"), ftsTestMessage("hangul")
	latin.Body, indic.Body, hangul.Body = "bonjour", "कि", "견적서"
	s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 2}, []mail.Message{latin, indic, hangul})
	indicID := ftsRowID(t, s, "indic")
	hangulID := ftsRowID(t, s, "hangul")
	// A version-3 store covered Hangul but not the Indic scripts.
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid = ?", indicID)
	ftsLegacyVersions(t, s, ftsTextVersion)

	wordIDs := ftsRowIDs(t, s, "messages_fts")
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), wordIDs)
	if got := ftsDBStat(t, again, ftsWordIndexWhere); got != wordBytes {
		t.Fatalf("word index bytes %d, want %d (the word index must not be rewritten)", got, wordBytes)
	}
	ftsAssertIDs(t, "trigram rowids", ftsRowIDs(t, again, "messages_fts_tri"), []int64{indicID, hangulID})
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)

	res, err := again.Search(t.Context(), "कि", 10)
	if err != nil || res.Total != 1 || len(res.Hits) != 1 || res.Hits[0].ID != "indic" || res.Terms[0].Mode != ModeScan {
		t.Fatalf("कि after the coverage reindex: %+v %v", res, err)
	}
}

func TestTriOnlyReindexAddsOnlyMissingRows(t *testing.T) {
	var messages []mail.Message
	terms := []string{"कि", "की", "कु", "कू"}
	for i, term := range terms {
		m := ftsTestMessage([]string{"a", "b", "c", "d"}[i])
		m.Body = term
		messages = append(messages, m)
	}
	s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 2}, messages)
	ids := make([]int64, len(messages))
	for i := range messages {
		ids[i] = ftsRowID(t, s, messages[i].ID)
	}
	// Only the first two rows exist, as if a previous coverage run had
	// stopped after two inserts.
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid IN (?, ?)", ids[2], ids[3])
	kept := ftsRowIDs(t, s, "messages_fts_tri")
	ftsAssertIDs(t, "kept trigram rowids", kept, ids[:2])
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)

	wordIDs := ftsRowIDs(t, s, "messages_fts")
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	got := ftsRowIDs(t, again, "messages_fts_tri")
	ftsAssertIDs(t, "trigram rowids", got, ids)
	ftsContainsAll(t, "trigram rowids", got, kept)
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), wordIDs)
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

// A message that no longer belongs to the substring coverage loses its stale
// trigram row; a message that still belongs keeps it.
func TestTriOnlyReindexRemovesStaleRows(t *testing.T) {
	latin, indic := ftsTestMessage("latin"), ftsTestMessage("indic")
	latin.Body, indic.Body = "bonjour", "कि"
	s, cfg := ftsTestStore(t, nil, []mail.Message{latin, indic})
	latinID, indicID := ftsRowID(t, s, "latin"), ftsRowID(t, s, "indic")
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid = ?", indicID)
	// A stale row for the Latin message, plus a missing Indic row: the
	// coverage pass must delete the first and insert the second.
	if _, err := s.db.ExecContext(t.Context(), ftsTriInsertSQL, latinID,
		mail.Fold("bonjour"), mail.Fold("bonjour"), "", ""); err != nil {
		t.Fatal(err)
	}
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordIDs := ftsRowIDs(t, s, "messages_fts")
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	ftsAssertIDs(t, "trigram rowids", ftsRowIDs(t, again, "messages_fts_tri"), []int64{indicID})
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), wordIDs)
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

// The coverage predicate reads every indexed field, recipients included.
func TestTriOnlyReindexChecksAllFields(t *testing.T) {
	subject, body, sender, recipient := ftsTestMessage("subject"), ftsTestMessage("body"), ftsTestMessage("sender"), ftsTestMessage("recipient")
	subject.Subject = "कि"
	body.Body = "कि"
	sender.SenderName = "कि"
	recipient.Recipients = []mail.Recipient{{Kind: mail.To, Name: "कि", Address: "ki@example.org"}}
	messages := []mail.Message{subject, body, sender, recipient}
	s, cfg := ftsTestStore(t, nil, messages)
	ids := make([]int64, len(messages))
	for i := range messages {
		ids[i] = ftsRowID(t, s, messages[i].ID)
	}
	ftsExec(t, s, "DELETE FROM messages_fts_tri")
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordIDs := ftsRowIDs(t, s, "messages_fts")
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	ftsAssertIDs(t, "trigram rowids", ftsRowIDs(t, again, "messages_fts_tri"), ids)
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), wordIDs)
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
	res, err := again.Search(t.Context(), "कि", 10)
	if err != nil || res.Total != len(messages) {
		t.Fatalf("कि in every field: %+v %v", res, err)
	}
}

func TestTriOnlyReindexNoIndicMail(t *testing.T) {
	var messages []mail.Message
	for i := range 20 {
		m := ftsTestMessage("latin-" + string(rune('a'+i)))
		m.Body = "bonjour " + m.ID
		messages = append(messages, m)
	}
	s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 7}, messages)
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordIDs := ftsRowIDs(t, s, "messages_fts")
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	if got := ftsRowIDs(t, again, "messages_fts_tri"); len(got) != 0 {
		t.Fatalf("trigram rows %v, want none", got)
	}
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), wordIDs)
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

func TestTriOnlyReindexPreservesWordSearch(t *testing.T) {
	a, b := ftsTestMessage("a"), ftsTestMessage("b")
	a.Subject, a.Body, b.Subject, b.Body = "budget review", "the budget review", "notes", "कि"
	c, d := ftsTestMessage("c"), ftsTestMessage("d")
	c.Subject, c.Body, d.Subject, d.Body = "deuxième budget", "le deuxième budget", "notes", "की"
	s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 1}, []mail.Message{a, b, c, d})
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid IN (?, ?)", ftsRowID(t, s, "b"), ftsRowID(t, s, "d"))
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	res, err := again.Search(t.Context(), "budget", 10)
	if err != nil || res.Total != 2 || res.Terms[0].Mode != ModeWord {
		t.Fatalf("word search after the coverage reindex: %+v %v", res, err)
	}
	res, err = again.Search(t.Context(), "कि", 10)
	if err != nil || res.Total != 1 || res.Hits[0].ID != "b" || res.Terms[0].Mode != ModeScan {
		t.Fatalf("scan search after the coverage reindex: %+v %v", res, err)
	}
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
}

// A folded-text change rewrites both indexes, even when the coverage version
// is current.
func TestWordVersionBumpRebuildsBothIndexes(t *testing.T) {
	latin, indic := ftsTestMessage("latin"), ftsTestMessage("indic")
	latin.Body, indic.Body = "bonjour", "कि"
	s, cfg := ftsTestStore(t, nil, []mail.Message{latin, indic})
	ftsExec(t, s, "DELETE FROM messages_fts; DELETE FROM messages_fts_tri")
	ftsSetVersions(t, s, "old", ftsCoverageVersion, ftsCoverageVersion)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), []int64{ftsRowID(t, again, "latin"), ftsRowID(t, again, "indic")})
	ftsAssertIDs(t, "trigram rowids", ftsRowIDs(t, again, "messages_fts_tri"), []int64{ftsRowID(t, again, "indic")})
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
	res, err := again.Search(t.Context(), "bonjour", 10)
	if err != nil || res.Total != 1 || res.Hits[0].ID != "latin" {
		t.Fatalf("word search after the text bump: %+v %v", res, err)
	}
}

func TestBothVersionsBumpRebuildsOnce(t *testing.T) {
	latin, indic := ftsTestMessage("latin"), ftsTestMessage("indic")
	latin.Body, indic.Body = "bonjour", "कि"
	s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 1}, []mail.Message{latin, indic})
	ftsExec(t, s, "DELETE FROM messages_fts; DELETE FROM messages_fts_tri")
	ftsSetVersions(t, s, "old", "old", "old")
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	if got := len(ftsRowIDs(t, again, "messages_fts")); got != 2 {
		t.Fatalf("word rows %d, want 2", got)
	}
	if got := len(ftsRowIDs(t, again, "messages_fts_tri")); got != 1 {
		t.Fatalf("trigram rows %d, want 1", got)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

// A Round 30 store (legacy 4-indic-substring) already holds the current
// content of both indexes. It adopts the split versions without any index
// work and seeds the two new keys.
func TestRound30StoreAdoptsSplitVersionsWithoutReindex(t *testing.T) {
	latin, indic := ftsTestMessage("latin"), ftsTestMessage("indic")
	latin.Body, indic.Body = "bonjour", "कि"
	s, cfg := ftsTestStore(t, nil, []mail.Message{latin, indic})
	wordIDs := ftsRowIDs(t, s, "messages_fts")
	triIDs := ftsRowIDs(t, s, "messages_fts_tri")
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	triBytes := ftsDBStat(t, s, ftsTriIndexWhere)
	ftsLegacyVersions(t, s, ftsCoverageVersion)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	ftsAssertIDs(t, "word rowids", ftsRowIDs(t, again, "messages_fts"), wordIDs)
	ftsAssertIDs(t, "trigram rowids", ftsRowIDs(t, again, "messages_fts_tri"), triIDs)
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
	if n := ftsDBStat(t, again, ftsTriIndexWhere); n != triBytes {
		t.Fatalf("trigram index bytes %d, want %d", n, triBytes)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

func TestNewStoreWritesBothVersions(t *testing.T) {
	s, _ := open(t, nil)
	ftsAssertVersions(t, s, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

// A coverage pass interrupted after some inserts leaves the version old. The
// next open finds the inserted rows, adds only the missing ones and deletes
// none.
func TestTriReindexResumesAfterPartialWrite(t *testing.T) {
	var messages []mail.Message
	for _, id := range []string{"a", "b", "c"} {
		m := ftsTestMessage(id)
		m.Body = "कि"
		messages = append(messages, m)
	}
	s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 1}, messages)
	ids := make([]int64, len(messages))
	for i := range messages {
		ids[i] = ftsRowID(t, s, messages[i].ID)
	}
	// One row was inserted before the interruption; two are missing.
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid IN (?, ?)", ids[1], ids[2])
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	_ = s.Close()

	again := reopenFTSStore(t, cfg)
	ftsAssertIDs(t, "trigram rowids", ftsRowIDs(t, again, "messages_fts_tri"), ids)
	if n := ftsDBStat(t, again, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

func TestTriReindexCanceledLeavesVersionOld(t *testing.T) {
	indicA, indicB := ftsTestMessage("a"), ftsTestMessage("b")
	indicA.Body, indicB.Body = "कि", "की"
	s, cfg := ftsTestStore(t, nil, []mail.Message{indicA, indicB})
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid = ?", ftsRowID(t, s, "b"))
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)
	triBefore := ftsRowIDs(t, s, "messages_fts_tri")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.reindexFTS(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("reindexFTS(canceled) = %v, want context.Canceled", err)
	}
	ftsAssertVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	ftsAssertIDs(t, "trigram rowids after cancel", ftsRowIDs(t, s, "messages_fts_tri"), triBefore)
	if n := ftsDBStat(t, s, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes %d, want %d", n, wordBytes)
	}

	// The next open retries and completes the coverage.
	_ = s.Close()
	again := reopenFTSStore(t, cfg)
	if got := len(ftsRowIDs(t, again, "messages_fts_tri")); got != 2 {
		t.Fatalf("trigram rows after retry %d, want 2", got)
	}
	ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

func TestTriOnlyReindexIsIdempotent(t *testing.T) {
	indicA, indicB := ftsTestMessage("a"), ftsTestMessage("b")
	indicA.Body, indicB.Body = "कि", "की"
	s, _ := ftsTestStore(t, nil, []mail.Message{indicA, indicB})
	ftsExec(t, s, "DELETE FROM messages_fts_tri WHERE rowid = ?", ftsRowID(t, s, "b"))
	ftsSetVersions(t, s, ftsTextVersion, ftsTextVersion, ftsTextVersion)
	wordBytes := ftsDBStat(t, s, ftsWordIndexWhere)

	if err := s.reindexFTS(t.Context()); err != nil {
		t.Fatalf("first reindexFTS: %v", err)
	}
	triIDs := ftsRowIDs(t, s, "messages_fts_tri")
	triBytes := ftsDBStat(t, s, ftsTriIndexWhere)
	if n := ftsDBStat(t, s, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes after the coverage pass %d, want %d", n, wordBytes)
	}
	if err := s.reindexFTS(t.Context()); err != nil {
		t.Fatalf("second reindexFTS: %v", err)
	}
	ftsAssertIDs(t, "trigram rowids after the second pass", ftsRowIDs(t, s, "messages_fts_tri"), triIDs)
	if n := ftsDBStat(t, s, ftsTriIndexWhere); n != triBytes {
		t.Fatalf("trigram index bytes after the second pass %d, want %d", n, triBytes)
	}
	if n := ftsDBStat(t, s, ftsWordIndexWhere); n != wordBytes {
		t.Fatalf("word index bytes after the second pass %d, want %d", n, wordBytes)
	}
	ftsAssertVersions(t, s, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
}

func TestLegacyMissingOrUnknownVersionRebuildsBoth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Store)
	}{
		{"missing", ftsClearVersions},
		{"older", func(t *testing.T, s *Store) { ftsLegacyVersions(t, s, "2-fold") }},
		{"unknown", func(t *testing.T, s *Store) { ftsLegacyVersions(t, s, "99-future") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latin, indic := ftsTestMessage("latin"), ftsTestMessage("indic")
			latin.Body, indic.Body = "bonjour", "कि"
			s, cfg := ftsTestStore(t, map[string]any{"sync.batch_size": 1}, []mail.Message{latin, indic})
			ftsExec(t, s, "DELETE FROM messages_fts; DELETE FROM messages_fts_tri")
			tc.setup(t, s)
			_ = s.Close()

			again := reopenFTSStore(t, cfg)
			if got := len(ftsRowIDs(t, again, "messages_fts")); got != 2 {
				t.Fatalf("word rows %d, want 2", got)
			}
			if got := len(ftsRowIDs(t, again, "messages_fts_tri")); got != 1 {
				t.Fatalf("trigram rows %d, want 1", got)
			}
			ftsAssertVersions(t, again, ftsTextVersion, ftsCoverageVersion, ftsCoverageVersion)
		})
	}
}
