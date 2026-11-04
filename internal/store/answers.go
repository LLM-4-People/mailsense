package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Answer pagination bounds: the label history read of the Answers screen
// and the hub's recent answers. The API validates its query against them;
// the store is the one owner of the numbers.
const (
	AnswerDefaultLimit = 50
	AnswerMaxLimit     = 100
)

// LabelCount is how many labels one question has from one source over the
// visible owner mail.
type LabelCount struct {
	Question string
	Source   string
	Count    int
}

// labelCountsQuery is the one query shape of the label count. The labels
// table drives the join (CROSS JOIN is SQLite's order barrier), so an empty
// labels table finishes after one empty covering-index scan and never visits
// a message; driving from the owner population instead paid a whole-mailbox
// read for zero labels. The message and scope joins are inner loops entered
// once per label row, so the read scales with labels, not with mail.
func labelCountsQuery(vis string) string {
	return `SELECT l.question, l.source, COUNT(*) FROM labels l
		CROSS JOIN messages m ON m.id = l.message_id
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + vis + ` AND ` + ownerWhere + `
		GROUP BY l.question, l.source ORDER BY l.question, l.source`
}

// LabelCounts counts the labels of every question by source over visible
// owner mail (visibleSQL and the scope owner verdict). The labels table is
// the driver: zero labels is an empty read.
func (s *Store) LabelCounts(ctx context.Context) ([]LabelCount, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	return query(ctx, s.db, labelCountsQuery(vis), nil,
		func(r *sql.Rows, c *LabelCount) error { return r.Scan(&c.Question, &c.Source, &c.Count) })
}

// reviewCountsQuery is the one query shape of the review-queue count: judge
// labels whose message and question carry no manual label, over the same
// triage.folder_roles population the queue's MemberIDs reads. Decodability
// is deliberately absent from the predicate: the review population is
// presence-based (layer1.reviewCandidate is the queue side of the same
// rule), and an unreadable manual answer still answers the question. It
// drives from labels for the same reason as labelCountsQuery.
func reviewCountsQuery(vis string, roles []mail.FolderRole) (string, []any) {
	args := []any{LabelJudge}
	roleCond := ""
	if len(roles) > 0 {
		cond, roleArgs := rolesSQL(roles)
		roleCond = " AND " + cond
		args = append(args, roleArgs...)
	}
	args = append(args, LabelManual)
	return `SELECT l.question, COUNT(*) FROM labels l
		CROSS JOIN messages m ON m.id = l.message_id
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + vis + ` AND ` + ownerWhere + ` AND l.source = ?` + roleCond + `
		AND NOT EXISTS (SELECT 1 FROM labels manual WHERE manual.message_id = l.message_id
			AND manual.question = l.question AND manual.source = ?)
		GROUP BY l.question ORDER BY l.question`, args
}

// ReviewCounts counts the judge labels of visible owner mail whose message
// and question carry no manual label: the review queue's population (one
// task per message and question). Value decodability is not part of the
// population: an unreadable judge label counts, and an unreadable manual
// label still answers the question.
func (s *Store) ReviewCounts(ctx context.Context) (map[string]int, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	roles := TriageFolderRoles(s.cfg)
	q, args := reviewCountsQuery(vis, roles)
	out := map[string]int{}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var question string
		var n int
		if err := rows.Scan(&question, &n); err != nil {
			return nil, err
		}
		out[question] = n
	}
	return out, rows.Err()
}

// AnswerRow is one current label of visible owner mail: the raw stored
// value and its rowid (the pagination cursor). It carries no message
// content.
type AnswerRow struct {
	ID        int64
	MessageID int64
	Question  string
	Source    string
	Value     string
	At        string
}

// AnswerFilter selects current labels of visible owner mail, newest first
// (labels.rowid descending). Cursor is the last rowid of the previous page,
// so the next page starts older. Limit 0 means AnswerDefaultLimit.
type AnswerFilter struct {
	Question  string
	Source    string // manual or judge ("" = both)
	MessageID int64  // 0 = every message
	Cursor    int64  // 0 = first page
	Limit     int
}

// answerRowsFromSQL is the one join shape of the answer history. The labels
// table drives the join (CROSS JOIN is SQLite's order barrier), so the read
// scales with labels instead of walking the owner population: an empty
// labels table finishes after one empty scan and never visits a message. The
// message, folder and scope joins are inner loops entered once per label row.
// ORDER BY l.rowid DESC is then the labels table's own rowid order, with no
// temporary sort.
const answerRowsFromSQL = ` FROM labels l
	CROSS JOIN messages m ON m.id = l.message_id
	JOIN folders f ON f.id = m.folder_id
	JOIN scope_verdicts v ON v.message_id = m.id`

// answerRowsQuery is the one statement of the answer history.
func answerRowsQuery(where string) string {
	return `SELECT l.rowid, l.message_id, l.question, l.source, l.value, l.created_at` +
		answerRowsFromSQL + ` WHERE ` + where + ` ORDER BY l.rowid DESC LIMIT ?`
}

// AnswerRows returns one page of current manual/judge labels of visible
// owner mail, newest first, and whether another page follows. Values are
// returned raw; the API decodes nothing here and no message column is
// selected.
func (s *Store) AnswerRows(ctx context.Context, f AnswerFilter) ([]AnswerRow, bool, error) {
	limit := f.Limit
	if limit == 0 {
		limit = AnswerDefaultLimit
	}
	if limit < 1 || limit > AnswerMaxLimit {
		return nil, false, fmt.Errorf("store: answer rows: limit must be between 1 and %d, got %d", AnswerMaxLimit, f.Limit)
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, false, err
	}
	where := []string{vis, ownerWhere, "l.source IN (?, ?)"}
	args := []any{LabelManual, LabelJudge}
	if f.Source != "" {
		where = append(where, "l.source = ?")
		args = append(args, f.Source)
	}
	if f.Question != "" {
		where = append(where, "l.question = ?")
		args = append(args, f.Question)
	}
	if f.MessageID > 0 {
		where = append(where, "l.message_id = ?")
		args = append(args, f.MessageID)
	}
	if f.Cursor > 0 {
		where = append(where, "l.rowid < ?")
		args = append(args, f.Cursor)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, answerRowsQuery(strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []AnswerRow
	for rows.Next() {
		var a AnswerRow
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Question, &a.Source, &a.Value, &a.At); err != nil {
			return nil, false, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// MailboxCounts returns the visible owner message count and the number of
// picked stores with visible mail. It never counts hidden (unpicked) mail.
func (s *Store) MailboxCounts(ctx context.Context) (messages, stores int, err error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id
			JOIN scope_verdicts v ON v.message_id = m.id WHERE `+vis+` AND `+ownerWhere+`),
		(SELECT COUNT(DISTINCT f.store_key) FROM folders f WHERE `+vis+`)`).Scan(&messages, &stores)
	return messages, stores, err
}
