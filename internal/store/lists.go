package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Source is one registered source.
type Source struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// FolderInfo is one stored folder with its message count.
type FolderInfo struct {
	ID        int64       `json:"id"`
	Folder    mail.Folder `json:"folder"`
	Active    bool        `json:"active"`
	Messages  int         `json:"messages"`
	Cursor    string      `json:"cursor"`
	UpdatedAt string      `json:"updated_at"`
}

// Run is one recorded sync run.
type Run struct {
	ID         int64          `json:"id"`
	SourceID   string         `json:"source_id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	State      runstate.State `json:"state"`
	Totals     RunTotals      `json:"totals"`
	Error      string         `json:"error"`
}

// ListSources returns every source.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	return query(ctx, s.db, "SELECT id, kind, name, created_at, updated_at FROM sources ORDER BY id", nil,
		func(r *sql.Rows, v *Source) error { return r.Scan(&v.ID, &v.Kind, &v.Name, &v.CreatedAt, &v.UpdatedAt) })
}

// ListFolders returns the folders of a source with message counts and cursors.
func (s *Store) ListFolders(ctx context.Context, sourceID string) ([]FolderInfo, error) {
	return query(ctx, s.db, `SELECT f.id, f.source_id, f.store_id, f.store_name, f.store_kind, f.path, f.role, COALESCE(f.item_count, -1),
			f.remote_id, f.active,
			(SELECT COUNT(*) FROM messages m WHERE m.folder_id = f.id), COALESCE(c.cursor, ''), f.updated_at
		FROM folders f LEFT JOIN sync_cursors c ON c.folder_id = f.id WHERE f.source_id = ? ORDER BY f.store_id, f.path`,
		[]any{sourceID}, func(r *sql.Rows, v *FolderInfo) error {
			var role, kind string
			err := r.Scan(&v.ID, &v.Folder.SourceID, &v.Folder.StoreID, &v.Folder.StoreName, &kind, &v.Folder.Path, &role,
				&v.Folder.ItemCount, &v.Folder.RemoteID, &v.Active, &v.Messages, &v.Cursor, &v.UpdatedAt)
			v.Folder.Role, v.Folder.StoreKind = mail.FolderRole(role), mail.StoreKind(kind)
			return err
		})
}

// listRuns is the one run-history read: newest first, at most limit, over one
// run table. The caller names its own columns after status; the id/started/
// finished/status frame, the order and the limit clamp are shared.
func listRuns[T any](ctx context.Context, s *Store, table string, columns []string,
	limit int, scan func(*sql.Rows, *T) error) ([]T, error) {
	all := append([]string{"id", "started_at", "finished_at", "status"}, columns...)
	return query(ctx, s.db, "SELECT "+strings.Join(all, ", ")+" FROM "+table+" ORDER BY id DESC LIMIT ?",
		[]any{max(limit, 1)}, scan)
}

// ListRuns returns the most recent sync runs (newest first), at most limit.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	return listRuns(ctx, s, "sync_runs",
		[]string{"source_id", "seen", "written", "skipped", "duplicates", "altered", "error"},
		limit, func(r *sql.Rows, v *Run) error {
			var st, altered string
			if err := r.Scan(&v.ID, &v.StartedAt, &v.FinishedAt, &st, &v.SourceID, &v.Totals.Seen, &v.Totals.Written,
				&v.Totals.Skipped, &v.Totals.Duplicates, &altered, &v.Error); err != nil {
				return err
			}
			v.State = runstate.State(st)
			if altered == "{}" {
				return nil
			}
			return json.Unmarshal([]byte(altered), &v.Totals.Altered)
		})
}

// query runs q and scans every row into a T. It accepts the pool or a
// transaction, so the same read works in and out of a batch.
func query[T any](ctx context.Context, qr queryer, q string, args []any, scan func(*sql.Rows, *T) error) ([]T, error) {
	rows, err := qr.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// placeholders is a list of n bind parameters (at least one: an empty IN
// list is a syntax error, callers bind a value no row has).
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", max(n, 1)), ", ") }

// idSet is the one way to pass a list of row ids to SQL: one JSON array
// parameter read through json_each, so a list of any length (a mass
// removal, a million-message subset) never exceeds SQLite's limit of
// bound parameters. Use as "x IN "+idSetSQL with idSet(ids) as the arg.
const idSetSQL = "(SELECT value FROM json_each(?))"

// stringSetSQL is the one JSON-set membership subquery for text values: use as
// "x IN "+stringSetSQL with jsonList(values) as the arg. It is the same shape
// as idSetSQL, so the subquery is written once and the two names document which
// value type the caller binds.
const stringSetSQL = idSetSQL

func idSet(ids []int64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	b.WriteByte(']')
	return b.String()
}
