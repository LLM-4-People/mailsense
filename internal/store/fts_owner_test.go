package store

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestFTSRebuildsShareOneReaderAndWalker: the word and trigram rebuilds read a
// message the same way and walk the id keyset the same way. A second copy of
// the projection is how one index starts folding a field the other never sees.
func TestFTSRebuildsShareOneReaderAndWalker(t *testing.T) {
	data, err := os.ReadFile("fts.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, c := range []struct{ name, body string }{
		{"readFTSMessage", "func readFTSMessage(ctx context.Context, q queryer, id int64) (mail.Message, error) {"},
		{"eachIDBatch", "func eachIDBatch(ctx context.Context, q queryer, size int, fn func(ids []int64) error) error {"},
	} {
		if got := strings.Count(source, c.body); got != 1 {
			t.Errorf("fts.go defines %s %d times, want once", c.name, got)
		}
	}
	if got := strings.Count(source, "SELECT id FROM messages WHERE id > ? ORDER BY id LIMIT ?"); got != 1 {
		t.Errorf("fts.go walks the id keyset %d times, want once in eachIDBatch", got)
	}
	if got := strings.Count(source, "+ recipientsSQL +"); got != 1 {
		t.Errorf("fts.go spells the recipients projection %d times, want once in ftsMessageSQL", got)
	}
	if got := strings.Count(source, "readFTSMessage(ctx, tx, id)"); got != 2 {
		t.Errorf("fts.go reads a message %d times, want both writers to use readFTSMessage", got)
	}
	if got := strings.Count(source, "eachIDBatch(ctx, s.db, "); got != 2 {
		t.Errorf("fts.go calls eachIDBatch %d times, want both rebuilds", got)
	}
}

// TestFTSRebuildFindsEveryMessageInBatches: a full rebuild over five messages
// with a two-row batch still indexes all of them. The walker's keyset advance
// is what this pins: a batch loop that forgets to move the cursor indexes one
// batch and stops.
func TestFTSRebuildFindsEveryMessageInBatches(t *testing.T) {
	messages := make([]mail.Message, 5)
	for i := range messages {
		messages[i] = ftsTestMessage(string(rune('a' + i)))
		messages[i].Subject = "Quarterly zebra report"
		messages[i].Body = "the quick brown fox"
	}
	s, cfg := ftsTestStore(t, map[string]any{SettingSyncBatchSize: 2}, messages)
	// The first open already indexed the messages, so clear the rows and make
	// the text version stale: the next open must rebuild all of them.
	ftsExec(t, s, "DELETE FROM messages_fts")
	ftsExec(t, s, "UPDATE meta SET value = 'stale' WHERE key = ?", metaFTSTextVersion)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := reopenFTSStore(t, cfg)
	if got, want := len(ftsRowIDs(t, again, "messages_fts")), len(messages); got != want {
		t.Fatalf("full rebuild indexed %d messages, want %d", got, want)
	}
}
