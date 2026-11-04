//go:build round31measure

package store

// Round 31 measurement harness: the split text/coverage reindex on disposable
// copies of the prepared 33,842-message store and the 10,040-message Hindi
// store. Built only with -tags round31measure; it is not part of the shipped
// code or the normal test gate.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
)

func round31Config(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func round31FileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func round31EnvInt(name string) int64 {
	if v := os.Getenv(name); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

// round31WriteVersions simulates the version state the mode measures. It runs
// on a disposable copy, before Open, on its own connection.
func round31WriteVersions(t *testing.T, path, mode string) {
	t.Helper()
	params := baseParams(5 * time.Second)
	params.Set("_txlock", "immediate")
	params.Add("_pragma", "journal_mode(WAL)")
	db, err := sql.Open("sqlite", dsn(path, params))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	upsert := func(key, value string) {
		exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	}
	switch mode {
	case "tri":
		exec("DELETE FROM meta WHERE key IN (?, ?)", metaFTSTextVersion, metaFTSCoverageVersion)
		upsert(metaFTSVersion, ftsTextVersion)
	case "full", "word":
		upsert(metaFTSTextVersion, "old")
		upsert(metaFTSCoverageVersion, ftsCoverageVersion)
		upsert(metaFTSVersion, ftsCoverageVersion)
	case "both":
		upsert(metaFTSTextVersion, "old")
		upsert(metaFTSCoverageVersion, "old")
		upsert(metaFTSVersion, "old")
	case "adopt":
		exec("DELETE FROM meta WHERE key IN (?, ?)", metaFTSTextVersion, metaFTSCoverageVersion)
		upsert(metaFTSVersion, ftsCoverageVersion)
	case "no-indic", "synth":
		upsert(metaFTSTextVersion, ftsTextVersion)
		upsert(metaFTSCoverageVersion, ftsTextVersion)
		upsert(metaFTSVersion, ftsTextVersion)
	default:
		t.Fatalf("unknown ROUND31_MODE %q", mode)
	}
}

type round31Snapshot struct {
	dbBytes, walBytes int64
	pageSize          int64
	pageCount         int64
	freelist          int64
	messages          int64
	wordRows          int64
	triRows           int64
	wordBytes         int64
	triBytes          int64
	groupBytes        int64
	wordChecksum      uint64
	triChecksum       uint64
	textVersion       string
	coverageVersion   string
	legacyVersion     string
}

func round31ReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	params := baseParams(5 * time.Second)
	params.Set("mode", "ro")
	params.Add("_pragma", "query_only(1)")
	db, err := sql.Open("sqlite", dsn(path, params))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("read-only open: %v", err)
	}
	return db
}

func round31Scalar(t *testing.T, db *sql.DB, q string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(context.Background(), q).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func round31Checksum(t *testing.T, db *sql.DB, table string) uint64 {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT rowid FROM "+table+" ORDER BY rowid")
	if err != nil {
		t.Fatalf("checksum %s: %v", table, err)
	}
	defer rows.Close()
	h := fnv.New64a()
	var buf [8]byte
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint64(buf[:], uint64(id))
		_, _ = h.Write(buf[:])
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return h.Sum64()
}

func round31SnapshotOf(t *testing.T, path, label string) round31Snapshot {
	t.Helper()
	db := round31ReadOnly(t, path)
	defer db.Close()
	var s round31Snapshot
	s.dbBytes = round31FileSize(path)
	s.walBytes = round31FileSize(path + "-wal")
	s.pageSize = round31Scalar(t, db, "PRAGMA page_size")
	s.pageCount = round31Scalar(t, db, "PRAGMA page_count")
	s.freelist = round31Scalar(t, db, "PRAGMA freelist_count")
	s.messages = round31Scalar(t, db, "SELECT COUNT(*) FROM messages")
	s.wordRows = round31Scalar(t, db, "SELECT COUNT(*) FROM messages_fts")
	s.triRows = round31Scalar(t, db, "SELECT COUNT(*) FROM messages_fts_tri")
	s.wordBytes = round31Scalar(t, db, "SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name LIKE 'messages_fts%' AND name NOT LIKE 'messages_fts_tri%'")
	s.triBytes = round31Scalar(t, db, "SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name LIKE 'messages_fts_tri%'")
	s.groupBytes = round31Scalar(t, db, "SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name LIKE 'messages_fts%'")
	s.wordChecksum = round31Checksum(t, db, "messages_fts")
	s.triChecksum = round31Checksum(t, db, "messages_fts_tri")
	versions := map[string]string{}
	rows, err := db.QueryContext(context.Background(), "SELECT key, value FROM meta WHERE key IN (?, ?, ?)",
		metaFTSTextVersion, metaFTSCoverageVersion, metaFTSVersion)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		versions[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	s.textVersion, s.coverageVersion, s.legacyVersion = versions[metaFTSTextVersion], versions[metaFTSCoverageVersion], versions[metaFTSVersion]
	fmt.Printf("ROUND31 %s db_bytes=%d wal_bytes=%d page_size=%d page_count=%d freelist=%d free_bytes=%d\n",
		label, s.dbBytes, s.walBytes, s.pageSize, s.pageCount, s.freelist, s.freelist*s.pageSize)
	fmt.Printf("ROUND31 %s messages=%d word_rows=%d tri_rows=%d word_bytes=%d tri_bytes=%d group_bytes=%d\n",
		label, s.messages, s.wordRows, s.triRows, s.wordBytes, s.triBytes, s.groupBytes)
	fmt.Printf("ROUND31 %s word_checksum=%016x tri_checksum=%016x\n", label, s.wordChecksum, s.triChecksum)
	fmt.Printf("ROUND31 %s fts_text_version=%q fts_coverage_version=%q fts_version=%q\n",
		label, s.textVersion, s.coverageVersion, s.legacyVersion)
	return s
}

func round31Open(t *testing.T, cfg *config.Config) (*Store, time.Duration) {
	t.Helper()
	start := time.Now()
	s, err := Open(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, time.Since(start)
}

// TestRound31Measure runs one mode on one disposable copy. ROUND31_DIR is the
// copy's data directory and ROUND31_MODE one of tri, full, adopt, word, both,
// no-indic or synth. ROUND31_EXACT_DELETE_ID optionally deletes one trigram
// row before the exact reindexTri pass (measure one insert).
func TestRound31Measure(t *testing.T) {
	dir := os.Getenv("ROUND31_DIR")
	if dir == "" {
		t.Skip("ROUND31_DIR not set")
	}
	mode := os.Getenv("ROUND31_MODE")
	if mode == "" {
		mode = "tri"
	}
	cfg := round31Config(t, dir)
	path := Path(cfg)
	fmt.Printf("ROUND31 mode=%s dir=%s\n", mode, dir)
	round31WriteVersions(t, path, mode)
	before := round31SnapshotOf(t, path, "before")

	s, cold := round31Open(t, cfg)
	fmt.Printf("ROUND31 cold_open_ms=%.3f\n", float64(cold.Microseconds())/1000)
	coldSnap := round31SnapshotOf(t, path, "cold")
	fmt.Printf("ROUND31 delta db_bytes=%d wal_bytes=%d word_bytes=%d tri_bytes=%d word_rows=%d tri_rows=%d\n",
		coldSnap.dbBytes-before.dbBytes, coldSnap.walBytes-before.walBytes,
		coldSnap.wordBytes-before.wordBytes, coldSnap.triBytes-before.triBytes,
		coldSnap.wordRows-before.wordRows, coldSnap.triRows-before.triRows)

	if mode == "adopt" {
		fmt.Printf("ROUND31 exact_tri skipped=adopt\n")
	} else {
		if id := round31EnvInt("ROUND31_EXACT_DELETE_ID"); id > 0 {
			if _, err := s.db.ExecContext(context.Background(), "DELETE FROM messages_fts_tri WHERE rowid = ?", id); err != nil {
				t.Fatal(err)
			}
			fmt.Printf("ROUND31 exact_deleted_rowid=%d\n", id)
		}
		start := time.Now()
		res, err := s.reindexTri(context.Background())
		total := time.Since(start)
		if err != nil {
			t.Fatalf("reindexTri: %v", err)
		}
		fmt.Printf("ROUND31 exact_tri total_ms=%.3f scan_ms=%.3f write_ms=%.3f scanned=%d inserted=%d deleted=%d\n",
			float64(total.Microseconds())/1000, float64(res.scan.Microseconds())/1000,
			float64(res.write.Microseconds())/1000, res.scanned, res.inserted, res.deleted)
		round31SnapshotOf(t, path, "exact")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, warm := round31Open(t, cfg)
	fmt.Printf("ROUND31 warm_open_ms=%.3f\n", float64(warm.Microseconds())/1000)
	round31SnapshotOf(t, path, "warm")
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}
