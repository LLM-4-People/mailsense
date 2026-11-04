package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// UpsertSource registers or renames a source and records what its data
// holds. New capabilities change what derived views read (Generation).
func (s *Store) UpsertSource(ctx context.Context, id, kind, name string, caps source.Capabilities) error {
	if id == "" || kind == "" {
		return errors.New("store: source id and kind are required")
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		return err
	}
	var old string
	if err := s.db.QueryRowContext(ctx, "SELECT capabilities FROM sources WHERE id = ?", id).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old != string(raw) {
		defer s.touch()
	}
	ts := s.now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO sources (id, kind, name, capabilities, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, name = excluded.name, capabilities = excluded.capabilities,
		updated_at = excluded.updated_at`, id, kind, name, string(raw), ts, ts)
	return err
}

// SourceCapabilities returns the recorded capabilities of every source.
func (s *Store) SourceCapabilities(ctx context.Context) (map[string]source.Capabilities, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, capabilities FROM sources")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]source.Capabilities{}
	for rows.Next() {
		var id, raw string
		var c source.Capabilities
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, fmt.Errorf("store: capabilities of source %q: %w", id, err)
		}
		out[id] = c
	}
	return out, rows.Err()
}

// Folder writes. Discovery is authoritative for a folder's metadata
// (UpsertFolder); the message write path only makes sure the folder row
// exists and never changes what discovery recorded. An unknown item count
// (NULL) never overwrites a known one.
const (
	folderUpsertSQL = `INSERT INTO folders (source_id, store_id, store_name, store_kind, path, role, item_count, remote_id, updated_at,
		store_key, picked)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source_id, store_id, path) DO UPDATE SET store_name = excluded.store_name, store_kind = excluded.store_kind,
		role = excluded.role,
		item_count = COALESCE(excluded.item_count, folders.item_count), remote_id = excluded.remote_id, active = 1,
		updated_at = excluded.updated_at
	RETURNING id`
	folderEnsureSQL = `INSERT INTO folders (source_id, store_id, store_name, store_kind, path, role, item_count, remote_id, updated_at,
		store_key, picked)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source_id, store_id, path) DO UPDATE SET id = folders.id
	RETURNING id`
)

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// writeFolder writes a folder row; picked says whether its store is picked
// now (a new row; existing rows follow applySelection).
func writeFolder(ctx context.Context, q rowQuerier, sqlText string, f mail.Folder, ts string, picked bool) (int64, error) {
	if f.SourceID == "" || f.Path == "" {
		return 0, errors.New("store: folder needs a source id and a path")
	}
	if _, err := mail.ParseRole(string(f.Role)); err != nil {
		return 0, err
	}
	if !f.StoreKind.Valid() {
		return 0, fmt.Errorf("store: folder %q: unknown store kind %q", f.Path, f.StoreKind)
	}
	var count any
	if f.ItemCount >= 0 {
		count = f.ItemCount
	}
	var id int64
	if err := q.QueryRowContext(ctx, sqlText, f.SourceID, f.StoreID, f.StoreName, string(f.StoreKind),
		f.Path, string(f.Role), count, f.RemoteID, ts, source.StoreKey(f.SourceID, f.StoreID), boolInt(picked)).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: folder %q: %w", f.Path, err)
	}
	return id, nil
}

// UpsertFolder records a discovered folder (active again if it was not)
// and returns its id.
func (s *Store) UpsertFolder(ctx context.Context, f mail.Folder) (int64, error) {
	defer s.touchVisibility()
	if err := s.applySelection(ctx); err != nil {
		return 0, err
	}
	return writeFolder(ctx, s.db, folderUpsertSQL, f, s.now(), s.pickedKey(source.StoreKey(f.SourceID, f.StoreID)))
}

// SetFolderActive marks a stored folder active or inactive. An inactive
// folder (store disabled, folder excluded: still existing, no longer
// synced) keeps its mail, outside scope and statistics; the next discovery
// of it reactivates it and its cursor resumes incrementally.
func (s *Store) SetFolderActive(ctx context.Context, folderID int64, active bool) error {
	defer s.touchVisibility()
	_, err := s.db.ExecContext(ctx, "UPDATE folders SET active = ? WHERE id = ?", boolInt(active), folderID)
	return err
}

// DeleteFolder removes a folder that no longer exists and has no messages
// left (its cursor goes by cascade).
func (s *Store) DeleteFolder(ctx context.Context, folderID int64) error {
	defer s.touch()
	_, err := s.db.ExecContext(ctx, "DELETE FROM folders WHERE id = ? AND NOT EXISTS (SELECT 1 FROM messages WHERE folder_id = ?)",
		folderID, folderID)
	return err
}
