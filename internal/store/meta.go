package store

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/buildinfo"
)

// Meta keys. created_* are written once; last_open_* on every Open — the
// UI shows which build created and last used the database.
const (
	metaCreatedAt       = "created_at"
	metaCreatedVersion  = "created_version"
	metaLastOpenAt      = "last_open_at"
	metaLastOpenVersion = "last_open_version"
)

// querier is the read half of *sql.DB and *sql.Tx: the meta helpers work
// inside a transaction and outside one.
type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// readMeta reads one meta value (false: the key was never written).
func readMeta(ctx context.Context, q querier, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// putMeta writes one meta value: the one upsert of the table.
func putMeta(ctx context.Context, ex execer, key, value string) error {
	_, err := ex.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func (s *Store) recordOpen(ctx context.Context) error {
	ts := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		// The previous value is captured before the overwrite: after Open it
		// exists nowhere else, and the update check's What's New marker reads
		// it (PreviousOpenVersion).
		previous, _, err := readMeta(ctx, tx, metaLastOpenVersion)
		if err != nil {
			return err
		}
		s.previousOpenVersion = previous
		for _, kv := range [][3]string{
			{metaCreatedAt, ts, "keep"}, {metaCreatedVersion, buildinfo.Version, "keep"},
			{metaLastOpenAt, ts, "replace"}, {metaLastOpenVersion, buildinfo.Version, "replace"},
		} {
			if kv[2] == "keep" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING", kv[0], kv[1]); err != nil {
					return err
				}
				continue
			}
			if err := putMeta(ctx, tx, kv[0], kv[1]); err != nil {
				return err
			}
		}
		return nil
	})
}

// PreviousOpenVersion returns the version the database was last opened with
// before this run ("" on a first open). Open captures it before recordOpen
// overwrites the meta key; it is the one fact the What's New marker compares.
func (s *Store) PreviousOpenVersion() string { return s.previousOpenVersion }

// Meta returns the metadata table.
func (s *Store) Meta(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM meta")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// metaLearnedScope is the learning scope (the picked mailboxes) that was
// fully learned once (learning.Latch).
const metaLearnedScope = "learned_scope"

// LearnedScope returns the scope that was fully learned ("" = none yet).
func (s *Store) LearnedScope(ctx context.Context) (string, error) {
	v, _, err := readMeta(ctx, s.db, metaLearnedScope)
	return v, err
}

// SetLearnedScope records a fully learned scope.
func (s *Store) SetLearnedScope(ctx context.Context, scope string) error {
	return putMeta(ctx, s.db, metaLearnedScope, scope)
}

// metaAutoLabels is the number of messages the automatic label loop has
// bought in this database. It is stored with the mailbox so a restart
// cannot restart the spend.
const metaAutoLabels = "labels_auto_messages"

// AutoLabelsBought returns how many messages the automatic label loop has
// bought (0 when it never ran).
func (s *Store) AutoLabelsBought(ctx context.Context) (int, error) {
	v, ok, err := readMeta(ctx, s.db, metaAutoLabels)
	if err != nil || !ok {
		return 0, err
	}
	n, _ := strconv.Atoi(v)
	return n, nil
}

// AddAutoLabelsBought records n more messages bought by the automatic
// label loop.
func (s *Store) AddAutoLabelsBought(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		v, _, err := readMeta(ctx, tx, metaAutoLabels)
		if err != nil {
			return err
		}
		bought, _ := strconv.Atoi(v)
		return putMeta(ctx, tx, metaAutoLabels, strconv.Itoa(bought+n))
	})
}
