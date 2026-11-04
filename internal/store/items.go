package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Attachments and folder memberships are written with their message (the
// one write path) and read back with it.

// foldersSQL is the one projection of a message's further folders: one JSON
// array of [path, role] pairs, ordered by path. The item read and the detail
// base read both select it, so a message's folder memberships cannot differ
// between the two.
const foldersSQL = `COALESCE((SELECT json_group_array(json_array(f2.path, f2.role)) FROM
	(SELECT f.path, f.role FROM message_folders mf JOIN folders f ON f.id = mf.folder_id WHERE mf.message_id = m.id ORDER BY f.path) f2), '[]')`

// itemsSQL selects a message's attachments and further folders as JSON.
const itemsSQL = `COALESCE((SELECT json_group_array(json_array(a.name, a.content_type, a.size, a.inline, a.content_id, a.content_location, a.sha256)) FROM
	(SELECT * FROM attachments WHERE message_id = m.id ORDER BY position) a), '[]'), ` + foldersSQL

// readItems loads a message's attachments and further folders.
func readItems(ctx context.Context, q queryer, rowID int64, m *mail.Message) error {
	var atts, folders string
	if err := q.QueryRowContext(ctx, "SELECT "+itemsSQL+" FROM messages m WHERE m.id = ?", rowID).Scan(&atts, &folders); err != nil {
		return err
	}
	return parseItems(atts, folders, m)
}

// parseItems decodes the two itemsSQL values into m.
func parseItems(atts, folders string, m *mail.Message) error {
	var a [][7]any
	if err := json.Unmarshal([]byte(atts), &a); err != nil {
		return fmt.Errorf("store: attachments: %w", err)
	}
	for _, x := range a {
		name, _ := x[0].(string)
		ct, _ := x[1].(string)
		size, _ := x[2].(float64)
		inline, _ := x[3].(float64)
		cid, _ := x[4].(string)
		loc, _ := x[5].(string)
		sha, _ := x[6].(string)
		m.Attachments = append(m.Attachments, mail.Attachment{Name: name, ContentType: ct, Size: int64(size),
			Inline: inline == 1, ContentID: cid, ContentLocation: loc, SHA256: sha})
	}
	var f [][2]string
	if err := json.Unmarshal([]byte(folders), &f); err != nil {
		return fmt.Errorf("store: folders: %w", err)
	}
	for _, x := range f {
		m.Folders = append(m.Folders, mail.FolderRef{Path: x[0], Role: mail.FolderRole(x[1])})
	}
	return nil
}

// FolderMembers counts the messages of each folder of a source through
// either their first folder or a further membership (folder id → count).
func (s *Store) FolderMembers(ctx context.Context, sourceID string) (map[int64]int, error) {
	type row struct {
		id int64
		n  int
	}
	rows, err := query(ctx, s.db, `SELECT folder_id, COUNT(DISTINCT message_id) FROM (
		SELECT folder_id, id AS message_id FROM messages WHERE source_id = ?
		UNION ALL SELECT mf.folder_id, mf.message_id FROM message_folders mf JOIN messages m ON m.id = mf.message_id WHERE m.source_id = ?)
		GROUP BY folder_id`, []any{sourceID, sourceID}, func(r *sql.Rows, v *row) error { return r.Scan(&v.id, &v.n) })
	out := map[int64]int{}
	for _, r := range rows {
		out[r.id] = r.n
	}
	return out, err
}
