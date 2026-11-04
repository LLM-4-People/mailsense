package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// PreviewAnswer is one message's threshold-free layer-1 answers under a
// profile: the JSON map question → engine.Answer in the Given shape (no
// abstention) and the run and message revision it was computed at.
// DecidedAt is stamped by the store when a commit writes the row.
type PreviewAnswer struct {
	MessageID int64
	Revision  int64
	RunID     int64
	Answers   string
}

// PreviewRecord is one message's decoded preview answers with the row's
// metadata: the revision they were computed at, the run that wrote them
// and the time.
type PreviewRecord = answerRecord[map[string]engine.Answer]

// LoadPreviewRecords returns the preview answers of the given messages
// with their revision and time. A message with no row is absent (never
// zero-filled), and an unreadable row fails the whole read: a preview
// answer is generated display data, never silently an empty answer.
func (s *Store) LoadPreviewRecords(ctx context.Context, fingerprint string, ids []int64) (map[int64]PreviewRecord, error) {
	return loadAnswerRecords[map[string]engine.Answer](s, ctx, previewAnswerTable, fingerprint, ids)
}

// streamPreviewAnswers writes the preview answers each yields, inside tx.
func (s *Store) streamPreviewAnswers(ctx context.Context, tx *sql.Tx, fingerprint string, each func(fn func(PreviewAnswer) error) error) error {
	return streamAnswerTable(s, ctx, tx, previewAnswerTable, fingerprint, each)
}

// PrunePreviewAnswers removes every fingerprint's preview answers but
// keep's: the rows of an old profile are unreachable (they are read by the
// current fingerprint only). It returns how many rows it removed.
func (s *Store) PrunePreviewAnswers(ctx context.Context, keep string) (int64, error) {
	return pruneAnswerTable(ctx, s.db, previewAnswerTable, keep)
}

// prunePreviewAnswers is PrunePreviewAnswers inside the caller's transaction;
// the shared answer-table owner holds the empty-key guard and the one DELETE.
func prunePreviewAnswers(ctx context.Context, ex execer, keep string) (int64, error) {
	return pruneAnswerTable(ctx, ex, previewAnswerTable, keep)
}

// clearPreviewAnswers removes every preview answer. The table is read only
// while learning.test_mode is on; a commit or an open that observes the
// preview off makes all of it unreachable, so it is deleted instead of
// keeping a second full answer copy on disk.
func clearPreviewAnswers(ctx context.Context, ex execer) (int64, error) {
	res, err := ex.ExecContext(ctx, "DELETE FROM route_preview_answers")
	if err != nil {
		return 0, fmt.Errorf("store: clear preview answers: %w", err)
	}
	return res.RowsAffected()
}
