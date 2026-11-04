package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// SaveMailboxes replaces the recorded mailbox list of one source with what
// its last discovery saw (picked or not). Every mailbox must belong to the
// source, carry its StoreKey and a valid kind.
func (s *Store) SaveMailboxes(ctx context.Context, sourceID string, list []source.Mailbox) error {
	for _, m := range list {
		if m.SourceID != sourceID || m.Key != source.StoreKey(m.SourceID, m.StoreID) || !m.Kind.Valid() {
			return fmt.Errorf("store: invalid mailbox %q (source %q, kind %q) for source %q", m.Key, m.SourceID, m.Kind, sourceID)
		}
	}
	defer s.touch()
	ts := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM mailboxes WHERE source_id = ?", sourceID); err != nil {
			return err
		}
		for _, m := range list {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mailboxes (source_id, store_id, key, name, kind, is_default, cached,
				folders, items, counted_at, error, discovered_at, identity_complete) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				m.SourceID, m.StoreID, m.Key, m.Name, string(m.Kind), boolInt(m.Default), boolInt(m.Cached),
				m.Folders, m.Items, m.CountedAt, m.Error, ts, boolInt(m.IdentityComplete)); err != nil {
				return fmt.Errorf("store: mailbox %q: %w", m.Key, err)
			}
		}
		return nil
	})
}

// mailboxes returns every recorded mailbox of every source, ordered by
// source and name (addresses: MailboxViews).
func (s *Store) mailboxes(ctx context.Context) ([]source.Mailbox, error) {
	return query(ctx, s.db, `SELECT source_id, store_id, key, name, kind, is_default, cached, folders, items, counted_at, error,
		identity_complete FROM mailboxes ORDER BY source_id, name, key`, nil, func(r *sql.Rows, v *source.Mailbox) error {
		var kind string
		err := r.Scan(&v.SourceID, &v.StoreID, &v.Key, &v.Name, &kind, &v.Default, &v.Cached, &v.Folders, &v.Items,
			&v.CountedAt, &v.Error, &v.IdentityComplete)
		v.Kind = mail.StoreKind(kind)
		return err
	})
}

// SyncCounts is how far one store's mail is stored, completed and judged.
type SyncCounts struct {
	Folders   int `json:"folders"`    // visible folders
	Unsynced  int `json:"unsynced"`   // of those, never read completely (no cursor)
	Messages  int `json:"messages"`   // stored messages of visible folders
	Partial   int `json:"partial"`    // waiting for their details
	SetAside  int `json:"set_aside"`  // of those, set aside after their detail read made the source fail
	Unjudged  int `json:"unjudged"`   // without a current scope verdict
	OwnerMail int `json:"owner_mail"` // judged owner scope
}

// SyncProgress returns SyncCounts per (source id, store id) of every store
// with a visible folder (visibleSQL). Unjudged uses EachStaleScope's stale
// rule: a verdict is missing, older than the message, or was judged against
// another owner identity.
func (s *Store) SyncProgress(ctx context.Context) (map[[2]string]SyncCounts, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	out := map[[2]string]SyncCounts{}
	type folderRow struct {
		src, store string
		folders    int
		unsynced   int
	}
	folders, err := query(ctx, s.db, `SELECT f.source_id, f.store_id, COUNT(*), SUM(c.folder_id IS NULL) FROM folders f
		LEFT JOIN sync_cursors c ON c.folder_id = f.id WHERE `+vis+` GROUP BY 1, 2`, nil,
		func(r *sql.Rows, v *folderRow) error { return r.Scan(&v.src, &v.store, &v.folders, &v.unsynced) })
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		out[[2]string{f.src, f.store}] = SyncCounts{Folders: f.folders, Unsynced: f.unsynced}
	}
	// The stale rule is EachStaleScope's: a verdict is current only when it
	// exists at the message revision and was computed against the store's
	// owner identity. Grouping by the stored digest keeps one SQL statement:
	// the aggregate is summed per store here, where the identity is known.
	type msgRow struct {
		src, store, identity                    string
		messages, partial, aside, unjudged, own int
	}
	msgs, err := query(ctx, s.db, `SELECT m.source_id, m.store_id, COALESCE(v.identity, ''), COUNT(*), SUM(m.partial),
		SUM(m.partial = 1 AND `+quarantinedSQL+`), SUM(v.message_id IS NULL OR v.revision <> m.revision), SUM(`+ownerWhere+`)
		FROM messages m JOIN folders f ON f.id = m.folder_id AND `+vis+`
		LEFT JOIN scope_verdicts v ON v.message_id = m.id GROUP BY 1, 2, 3`, nil,
		func(r *sql.Rows, v *msgRow) error {
			var own sql.NullInt64
			err := r.Scan(&v.src, &v.store, &v.identity, &v.messages, &v.partial, &v.aside, &v.unjudged, &own)
			v.own = int(own.Int64)
			return err
		})
	if err != nil {
		return nil, err
	}
	owners, err := s.owners(ctx, s.cfg.Snapshot())
	if err != nil {
		return nil, err
	}
	digests := make(map[string]string, len(owners))
	for key, o := range owners {
		if o.Identity.Known() {
			digests[key] = o.Identity.Digest()
		}
	}
	for _, m := range msgs {
		k := [2]string{m.src, m.store}
		c := out[k]
		c.Messages += m.messages
		c.Partial += m.partial
		c.SetAside += m.aside
		c.OwnerMail += m.own
		// A group judged under another digest is stale whole, whatever its
		// revisions; a group at the current digest only owes the missing or
		// revision-stale rows. A store with no identity owes every verdict.
		if dg, known := digests[source.StoreKey(m.src, m.store)]; !known || m.identity != dg {
			c.Unjudged += m.messages
		} else {
			c.Unjudged += m.unjudged
		}
		out[k] = c
	}
	return out, nil
}
