package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// migrations returns the embedded migrations, ordered and gap-free.
func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return nil, fmt.Errorf("store: migration %q must be named NNNN_name.sql", e.Name())
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration versions must be 1..n without gaps; found %d at position %d", m.version, i+1)
		}
	}
	return out, nil
}

// migrate applies pending migrations, each in its own transaction, tracking
// the version in PRAGMA user_version. A database newer than this binary is refused.
func (s *Store) migrate(ctx context.Context) error {
	all, err := migrations()
	if err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(all) {
		return fmt.Errorf("store: database schema version %d is newer than this build (%d)", current, len(all))
	}
	for _, m := range all[current:] {
		if err := s.apply(ctx, m); err != nil {
			return err
		}
		s.log.Info("migration applied", "version", m.version, "name", m.name)
		publish(s, TopicMigrated, Migrated{Version: m.version, Name: m.name})
	}
	return nil
}

func (s *Store) apply(ctx context.Context, m migration) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		_, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version))
		return err
	})
}
