package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/LLM-4-People/mailsense/internal/textx"
)

// textRepairKey records that the one-time repair of saved question and answer
// text has run. The repair is idempotent, but the marker keeps startup from
// rewriting the table on every open.
const textRepairKey = "qa_text_encoding_v1"

// repairSavedText repairs saved questions and answers that a legacy writer
// stored double-encoded ("RÃ©sume" instead of "Résume"). It is a declared,
// counted data repair, never a silent rewrite on read: the fix is applied
// once, at the single owner of the rows, and the number of repaired sequences
// is logged. Ordinary accented text is untouched (see textx.Repair).
func (s *Store) repairSavedText(ctx context.Context) error {
	var done string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, textRepairKey).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	type fix struct {
		id       int64
		question string
		answer   string
	}
	var fixes []fix
	sequences := 0
	rows, err := s.db.QueryContext(ctx, `SELECT id, question, answer FROM qa_history`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var question, answer string
		if err := rows.Scan(&id, &question, &answer); err != nil {
			_ = rows.Close()
			return err
		}
		fixedQuestion, qn := textx.Repair(question)
		fixedAnswer, an := textx.Repair(answer)
		if qn+an == 0 {
			continue
		}
		fixes = append(fixes, fix{id: id, question: fixedQuestion, answer: fixedAnswer})
		sequences += qn + an
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	// Close the read before opening the write transaction: the pool may be
	// configured with a single connection, and an open cursor would hold it.
	if err := rows.Close(); err != nil {
		return err
	}
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, f := range fixes {
			if _, err := tx.ExecContext(ctx, `UPDATE qa_history SET question = ?, answer = ? WHERE id = ?`,
				f.question, f.answer, f.id); err != nil {
				return err
			}
		}
		return putMeta(ctx, tx, textRepairKey, "1")
	}); err != nil {
		return err
	}
	if len(fixes) > 0 {
		s.log.Info("saved answer text repaired", "rows", len(fixes), "sequences", sequences)
	}
	return nil
}
