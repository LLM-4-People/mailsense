package store

import (
	"context"
	"fmt"
	"os"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Storage settings (internal/config/schema.json). The store owns them: the
// per-connection cache and mapping sizes, the WAL high-water mark and the run
// history are its facts, not a caller's policy.
const (
	SettingWALCheckpointMB   = "store.wal_checkpoint_mb"
	SettingRetainRuns        = "store.retain.runs"
	SettingRetainTriage      = "store.retain.triage_keys"
	SettingMaintainEvery     = "store.maintenance_interval"
	SettingWALAutocheckpoint = "store.wal_autocheckpoint"
	SettingCacheSizeMB       = "store.cache_size_mb"
	SettingMmapSizeMB        = "store.mmap_size_mb"
)

// MaintenanceReport is what one maintenance pass did.
type MaintenanceReport struct {
	WALBytes          int64 `json:"wal_bytes"`
	Checkpoint        bool  `json:"checkpoint"`
	RunsDeleted       int64 `json:"runs_deleted"`
	TriageKeysDeleted int64 `json:"triage_keys_deleted"`
}

// WALPath is the sidecar file SQLite appends to the database.
func (s *Store) WALPath() string { return s.path + "-wal" }

// WALSize is the current WAL file size (0 when absent).
func (s *Store) WALSize() int64 {
	fi, err := os.Stat(s.WALPath())
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Maintain runs the store's periodic housekeeping: it keeps the newest
// keepRuns rows of each run table, truncates the WAL when it is over
// store.wal_checkpoint_mb, and refreshes the planner statistics. Each step
// is best-effort and reported; a busy checkpoint is retried next pass.
func (s *Store) Maintain(ctx context.Context, keepRuns int) (MaintenanceReport, error) {
	report := MaintenanceReport{WALBytes: s.WALSize()}
	if keepRuns > 0 {
		n, err := s.pruneRuns(ctx, keepRuns)
		if err != nil {
			return report, err
		}
		report.RunsDeleted = n
	}
	// A question-spec or decider change writes a new full copy of the paid
	// triage answers. Only the newest keys can be read under the current
	// profile; keeping every old copy grows the database without bound.
	if keep := s.cfg.Int(SettingRetainTriage); keep > 0 {
		n, err := s.pruneTriageKeys(ctx, keep)
		if err != nil {
			return report, err
		}
		report.TriageKeysDeleted = n
	}
	if err := s.checkpointIfLarge(ctx); err != nil {
		return report, fmt.Errorf("store: checkpoint: %w", err)
	}
	if report.WALBytes != s.WALSize() {
		report.Checkpoint, report.WALBytes = true, s.WALSize()
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return report, fmt.Errorf("store: optimize: %w", err)
	}
	return report, nil
}

// pruneNewest keeps the newest keep rows of one history table and returns
// how many it removed. keep is clamped to at least one everywhere: a zero
// from a setting or a caller must never wipe the history it exists to keep
// (two callers already clamped, the others did not).
func pruneNewest(ctx context.Context, ex execer, table, key, order string, keep int) (int64, error) {
	// order is the complete ORDER BY expression (directions included).
	res, err := ex.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+key+" NOT IN (SELECT "+key+" FROM "+table+" ORDER BY "+order+" LIMIT ?)", max(keep, 1))
	if err != nil {
		return 0, fmt.Errorf("store: prune %s: %w", table, err)
	}
	return res.RowsAffected()
}

// pruneRuns keeps the newest keep rows of the run tables whose history no
// view needs beyond a look back. Evidence runs have their own owner
// (evidence.runs.keep); this never touches them.
func (s *Store) pruneRuns(ctx context.Context, keep int) (int64, error) {
	var total int64
	for _, table := range []string{tableLayer1Runs, tableTriageRuns, tableSyncRuns} {
		n, err := pruneNewest(ctx, s.db, table, "id", "id DESC", keep)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// pruneBatch bounds one triage-key DELETE. Each batch is its own implicit
// transaction, so a large prune releases the write lock between batches
// instead of holding it for the whole pass: every other store write can
// interleave with a 69k-row cleanup.
const pruneBatch = 1000

// triagePruneSQL is the one batched triage-key prune statement. The kept key
// list is a materialized LIST SUBQUERY: SQLite evaluates it once per DELETE,
// not once per outer row like the correlated NOT EXISTS it replaced. The
// rowid list is bounded by pruneBatch, and the deterministic tie-breaks make
// repeated passes keep the same keys.
func triagePruneSQL(table, at string) string {
	return `DELETE FROM ` + table + `
		WHERE rowid IN (
			SELECT rowid FROM ` + table + `
			WHERE (fingerprint, decider) NOT IN (
				SELECT fingerprint, decider FROM ` + table + `
				GROUP BY fingerprint, decider
				ORDER BY MAX(` + at + `) DESC, fingerprint DESC, decider DESC
				LIMIT ?)
			LIMIT ?)`
}

// pruneTriageKeys keeps the newest keep keys of each triage cache table.
// A key is one question-spec fingerprint and decider; old copies are never
// read once the profile or decider moves on. Evicting a key means returning
// to it later re-bills its answers, which is the honest trade for a bounded
// database.
func (s *Store) pruneTriageKeys(ctx context.Context, keep int) (int64, error) {
	keep = max(keep, 1)
	var total int64
	for _, table := range []struct{ name, at string }{
		{"triage_decisions", "decided_at"},
		{"triage_failures", "failed_at"},
	} {
		query := triagePruneSQL(table.name, table.at)
		for {
			res, err := s.db.ExecContext(ctx, query, keep, pruneBatch)
			if err != nil {
				return total, fmt.Errorf("store: prune %s: %w", table.name, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return total, fmt.Errorf("store: prune %s: %w", table.name, err)
			}
			total += n
			if n == 0 {
				break
			}
		}
	}
	return total, nil
}

// checkpointIfLarge truncates the WAL when it is over the configured
// ceiling. journal_size_limit bounds what a checkpoint leaves behind;
// wal_autocheckpoint (both in the DSN, storagePragmas) folds the ordinary
// commits. This is the one owner of "over the ceiling", shared by startup,
// the maintenance worker and the layer-1 commit.
func (s *Store) checkpointIfLarge(ctx context.Context) error {
	limit := int64(s.cfg.Int(SettingWALCheckpointMB)) << 20
	if limit <= 0 || s.WALSize() <= limit {
		return nil
	}
	return s.Checkpoint(ctx)
}

// runTables are the run histories that share the started_at/status/error
// shape. A row left in "running" belongs to a process that is gone: nothing
// can finish it, and the stage that reads the newest row would wait forever.
var runTables = []string{tableSyncRuns, tableTriageRuns, tableIndexRuns, tableEvidenceRuns, tableLayer1Runs}

// closeInterruptedRuns marks the runs a previous process left open. Open
// calls it before any run can start, so every "running" row at that moment is
// a crash, a kill or a shutdown that could not finish: reporting it as
// cancelled is the honest state, and it lets the next run (or the reconcile)
// take over instead of a stage waiting on a run nobody is executing.
func (s *Store) closeInterruptedRuns(ctx context.Context) (int64, error) {
	var total int64
	now := s.now()
	for _, table := range runTables {
		res, err := s.db.ExecContext(ctx, "UPDATE "+table+" SET finished_at = ?, status = ?, error = CASE WHEN error = '' THEN ? ELSE error END WHERE status = ?",
			now, string(runstate.Cancelled), "interrupted by a restart", string(runstate.Running))
		if err != nil {
			return total, fmt.Errorf("store: close interrupted %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
