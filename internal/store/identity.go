package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// SaveReported replaces the owner addresses a source reported for one
// store, in order (primary first). The syncer calls it only after a
// complete read of the identity (source.Mailbox.IdentityComplete): an
// offline or partly read store never replaces what is recorded.
// Addresses are stored as reported; scope.NewIdentity normalizes them.
func (s *Store) SaveReported(ctx context.Context, ref StoreRef, addrs []string) error {
	defer s.touch()
	ts := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM owner_identities WHERE source_id = ? AND store_id = ?",
			ref.SourceID, ref.StoreID); err != nil {
			return err
		}
		for pos, a := range addrs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO owner_identities (source_id, store_id, address, kind, position, computed_at)
				VALUES (?, ?, ?, 'reported', ?, ?) ON CONFLICT DO NOTHING`, ref.SourceID, ref.StoreID, a, pos, ts); err != nil {
				return err
			}
		}
		return nil
	})
}

// reported returns the recorded reported addresses by store key.
func (s *Store) reported(ctx context.Context) (map[string][]string, error) {
	type row struct{ src, store, addr string }
	rows, err := query(ctx, s.db, `SELECT source_id, store_id, address FROM owner_identities ORDER BY source_id, store_id, position`, nil,
		func(r *sql.Rows, v *row) error { return r.Scan(&v.src, &v.store, &v.addr) })
	out := map[string][]string{}
	for _, r := range rows {
		k := source.StoreKey(r.src, r.store)
		out[k] = append(out[k], r.addr)
	}
	return out, err
}

// Owner is the owner identity of one store: what its source reported (the
// last complete read) plus what the user declares
// (source.SettingDeclared), joined here and nowhere else.
type Owner struct {
	Reported []string       `json:"reported"`
	Declared []string       `json:"declared"`
	Identity scope.Identity `json:"identity"`
}

// OwnerIdentity is the mailbox owner as Ask may name them: the display
// names that owner-sent mail carries and the canonical owner addresses. It
// is read from owner-facing mail only; a shared/delegate store never
// contributes a name here.
type OwnerIdentity struct {
	Names     []string `json:"names"`
	Addresses []string `json:"addresses"`
}

// OwnerIdentity reads the owner's display names from the newest owner-sent
// mail and the picked stores' canonical owner addresses. The name is not
// configured: it is what the mailbox itself signs.
func (s *Store) OwnerIdentity(ctx context.Context) (OwnerIdentity, error) {
	out := OwnerIdentity{Names: []string{}, Addresses: []string{}}
	vis, err := s.visible(ctx)
	if err != nil {
		return out, err
	}
	owners, err := s.Owners(ctx)
	if err != nil {
		return out, err
	}
	keys := s.pickedKeys()
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		for _, address := range owners[key].Identity.Addresses {
			if address != "" && !seen[address] {
				seen[address] = true
				out.Addresses = append(out.Addresses, address)
			}
		}
	}
	names, err := query[string](ctx, s.db, `SELECT m.sender_name FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+vis+` AND `+ownerContentWhere+` AND `+sentByOwnerWhere+`
			AND trim(m.sender_name) <> ''
		GROUP BY m.sender_name
		ORDER BY COUNT(*) DESC, MAX(`+instantSQL+`) DESC
		LIMIT 3`, nil, func(rows *sql.Rows, name *string) error {
		return rows.Scan(name)
	})
	if err != nil {
		return out, fmt.Errorf("store: owner names: %w", err)
	}
	out.Names = append(out.Names, names...)
	return out, nil
}

// Owners returns the owner of every store key that has reported or
// declared addresses (scope.subaddress_separators applied).
func (s *Store) Owners(ctx context.Context) (map[string]Owner, error) {
	return s.owners(ctx, s.cfg.Snapshot())
}

// owners is Owners from one configuration snapshot: a caller that keys a
// cache by the snapshot reads every config input from the same values.
func (s *Store) owners(ctx context.Context, snap *config.Snapshot) (map[string]Owner, error) {
	rep, err := s.reported(ctx)
	if err != nil {
		return nil, err
	}
	decl, seps := source.Declared(snap), snap.Strings("scope.subaddress_separators")
	out := map[string]Owner{}
	for _, m := range []map[string][]string{rep, decl} {
		for k := range m {
			out[k] = Owner{Reported: rep[k], Declared: decl[k], Identity: scope.NewIdentity(seps, rep[k], decl[k])}
		}
	}
	return out, nil
}
