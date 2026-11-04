// Package store owns the SQLite database: schema and migrations, messages,
// recipients, the FTS5 indexes (one owner), sync cursors and runs, scope
// verdicts, metadata, and the validated read-only SQL runner.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Store is the read-write database handle.
type Store struct {
	db      *sql.DB
	cfg     *config.Config
	bus     *events.Bus
	clock   clock.Clock
	path    string
	log     *slog.Logger
	gen     atomic.Uint64
	mailGen atomic.Uint64
	// routeGen counts route-answer writes only (see RouteGeneration).
	routeGen atomic.Uint64
	// labelGen counts label and visibility writes (see LabelGeneration).
	labelGen atomic.Uint64
	// embedGen counts embedding writes only (see EmbeddingGeneration).
	embedGen atomic.Uint64
	// applied is the selection (source.Scope) folders.picked reflects.
	applied atomic.Pointer[string]
	selMu   sync.Mutex
	// counts caches the All-mail role counts per generation, store and
	// sender. It is invalidated by any mailbox write (touch bumps gen),
	// never by a read.
	countsMu sync.Mutex
	counts   map[messageCountsKey]messageCountsEntry
	// routeTotals caches the RoutePage total per mail and route generation,
	// fingerprint and filter. Every page of one open asks for the same total.
	routeTotalsMu sync.Mutex
	routeTotals   map[routePageTotalKey]int
	// book is the one company book of a mail generation and shared-domain
	// configuration. It is read-only after build; the address book and the
	// company list would otherwise build it twice for one request.
	book          buildSlot[*CompanyBook]
	bookBuildHook func() // test seam before a build reads the database
	// counterparts is the one all-time pair-fact read of a mail generation
	// and configuration. The company ranking and every company profile fold
	// it instead of streaming the same counterpart population again.
	counterparts          buildSlot[*CompanyCounterparts]
	counterpartsBuildHook func() // test seam before a build reads the database
	// cands caches the entity resolver's candidate read per mail generation,
	// terms and limit. It is one full-mailbox scan; Ask repeats the same
	// names while the mail has not moved.
	candMu sync.Mutex
	cands  map[personCandidatesKey][]PersonCandidate
	// busy is the one SQLITE_BUSY/LOCKED retry of every bounded write.
	busy BusyPolicy
	// mailHoldMu guards the mail-write holds. A producer that writes a run
	// of message/detail batches holds the mail generation so a 50k-message
	// sync invalidates every derived view once per folder, not once per
	// batch. A write outside a hold still bumps immediately.
	mailHoldMu sync.Mutex
	mailHolds  int
	mailDirty  bool
}

// Path returns the database file configured by store.file (also used by
// OpenReadOnly before any Store exists).
func Path(cfg *config.Config) string { return cfg.Paths().Resolve(cfg.String("store.file")) }

// Open opens (creating if needed) and migrates the database at Path(cfg),
// then records this build in the meta table. bus and clk may be nil (no
// events; wall clock).
func Open(ctx context.Context, cfg *config.Config, bus *events.Bus, clk clock.Clock) (*Store, error) {
	path := Path(cfg)
	if err := fsx.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	params := baseParams(cfg.Duration("store.busy_timeout"))
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "synchronous("+strings.ToUpper(cfg.String("store.synchronous"))+")")
	for _, pragma := range storagePragmas(cfg) {
		params.Add("_pragma", pragma)
	}
	params.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", dsn(path, params))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.Int(SettingMaxOpenConns))
	s := &Store{db: db, cfg: cfg, bus: bus, clock: clock.Or(clk), path: path, log: logx.For("store")}
	s.busy = newBusyPolicy(cfg, s.clock, s.log)
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := s.recordOpen(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := s.reindexFTS(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: rebuild full-text indexes: %w", path, err)
	}
	// The migrations and the FTS rebuild are one large write each and cannot
	// checkpoint mid-transaction. Without this pass the WAL they leave waits
	// for the first maintenance worker, minutes after the UI is up.
	// A previous profile can leave a full preview-answer copy behind; it is
	// unreachable while the explicit test mode is off. Clear it before the
	// checkpoint so the delete's WAL is bounded by the same pass.
	if !cfg.Preview() {
		if n, err := clearPreviewAnswers(ctx, s.db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: open %s: clear preview answers: %w", path, err)
		} else if n > 0 {
			s.log.Info("preview answers cleared", "rows", n)
		}
	}
	if err := s.checkpointIfLarge(ctx); err != nil {
		s.log.Warn("startup checkpoint skipped", "err", err)
	}
	if n, err := s.closeInterruptedRuns(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	} else if n > 0 {
		s.log.Warn("runs interrupted by the previous shutdown were closed", "runs", n)
	}
	if err := s.repairSavedText(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: repair saved answer text: %w", path, err)
	}
	// SQLite creates the database, WAL and shared-memory file with the
	// process umask. A 0644 database in a restored directory exposes every
	// body to another local user; the modes have one owner here.
	if err := s.privateFiles(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: private file modes: %w", path, err)
	}
	s.log.Info("store opened", "path", path)
	return s, nil
}

// privateFiles sets the database and its SQLite side files to FilePerm. A
// missing side file (already checkpointed) is not an error.
func (s *Store) privateFiles() error {
	for _, name := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if err := os.Chmod(name, fsx.FilePerm); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Close checkpoints the WAL before closing the pool: a clean shutdown should
// not leave the largest transaction's log for the next start to replay. A
// busy checkpoint is bounded and only warned; closing is not the place to
// hang the process.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Checkpoint(ctx); err != nil {
		s.log.Warn("shutdown checkpoint skipped", "err", err)
	}
	err := s.db.Close()
	s.dropDerivedCaches()
	return err
}

// dropDerivedCaches releases every in-memory view derived from the database.
// After Close no reader may answer from a cache: the next read must fail at
// the closed pool instead of returning a population nothing can refresh.
func (s *Store) dropDerivedCaches() {
	s.countsMu.Lock()
	s.counts = nil
	s.countsMu.Unlock()

	s.routeTotalsMu.Lock()
	s.routeTotals = nil
	s.routeTotalsMu.Unlock()

	s.book.reset()
	s.counterparts.reset()

	s.candMu.Lock()
	s.cands = nil
	s.candMu.Unlock()
}

// inTx runs fn in one transaction on a pooled connection.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return inTxOn(ctx, conn, fn)
}

// inTxOn runs fn in one transaction on conn, which the caller owns. It is
// the one transaction policy of the store: a caller that already holds a
// dedicated connection (the layer-1 staging commit) uses the same BEGIN
// IMMEDIATE and the same rollback-on-error as every other write.
func inTxOn(ctx context.Context, conn *sql.Conn, fn func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// A panic in fn unwinds through this defer, so the connection is never
	// returned to the pool with an open transaction (and its write lock).
	// Rollback after a successful Commit is a no-op that returns ErrTxDone.
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// inReadTx runs fn in one read-only transaction. A read that makes more than
// one statement (the counterpart build, the thread-state join) must observe a
// single database snapshot, or a write that commits between the statements
// mixes two revisions. This is the one place that policy lives: read-only
// begin, rollback on every return path, no commit.
func inReadTx[T any](ctx context.Context, db *sql.DB, label string, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, fmt.Errorf("store: %s: begin read: %w", label, err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// IsBusy reports whether err is SQLITE_BUSY/LOCKED (another writer held the lock
// for longer than store.busy_timeout).
func IsBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

func (s *Store) now() string { return s.clock.Now().UTC().Format(mail.TimeLayout) }

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
