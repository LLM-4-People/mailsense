//go:build round30measure

package store

// Round 30 measurement harness: reindex/storage cost on a disposable copy of
// the prepared 33,842-message store and accuracy/latency on synthetic Indic
// fixtures. Built only with -tags round30measure; it is not part of the
// shipped code or the normal test gate.

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func round30Config(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func round30Open(t *testing.T, cfg *config.Config) (*Store, time.Duration) {
	t.Helper()
	start := time.Now()
	s, err := Open(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, time.Since(start)
}

func round30Size(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func round30Scalar(t *testing.T, s *Store, q string) int64 {
	t.Helper()
	var v int64
	if err := s.db.QueryRowContext(context.Background(), q).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// round30Stats prints the storage shape of an open store.
func round30Stats(t *testing.T, s *Store, label string) {
	t.Helper()
	ctx := context.Background()
	path := s.path
	fmt.Printf("ROUND30 %s db_bytes=%d wal_bytes=%d shm_bytes=%d\n", label,
		round30Size(path), round30Size(path+"-wal"), round30Size(path+"-shm"))
	var version string
	_ = s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='fts_version'").Scan(&version)
	pageSize := round30Scalar(t, s, "PRAGMA page_size")
	pageCount := round30Scalar(t, s, "PRAGMA page_count")
	free := round30Scalar(t, s, "PRAGMA freelist_count")
	fmt.Printf("ROUND30 %s fts_version=%s page_size=%d page_count=%d freelist=%d free_bytes=%d\n",
		label, version, pageSize, pageCount, free, free*pageSize)
	fmt.Printf("ROUND30 %s messages=%d word_rows=%d tri_rows=%d\n", label,
		round30Scalar(t, s, "SELECT COUNT(*) FROM messages"),
		round30Scalar(t, s, "SELECT COUNT(*) FROM messages_fts"),
		round30Scalar(t, s, "SELECT COUNT(*) FROM messages_fts_tri"))
	for _, group := range []struct {
		label string
		where string
	}{
		// dbstat's LIKE 'messages_fts%' also matches messages_fts_tri*;
		// word-only must exclude the trigram shadow tables explicitly.
		{"messages_fts", "name LIKE 'messages_fts%' AND name NOT LIKE 'messages_fts_tri%'"},
		{"messages_fts_tri", "name LIKE 'messages_fts_tri%'"},
		{"messages_fts_group", "name LIKE 'messages_fts%'"},
	} {
		rows, err := s.db.QueryContext(ctx, `SELECT name, SUM(pgsize) FROM dbstat WHERE `+group.where+` GROUP BY name ORDER BY name`)
		if err != nil {
			fmt.Printf("ROUND30 %s %s dbstat=unavailable(%v)\n", label, group.label, err)
			continue
		}
		var total int64
		for rows.Next() {
			var name string
			var n int64
			if err := rows.Scan(&name, &n); err != nil {
				t.Fatal(err)
			}
			total += n
			fmt.Printf("ROUND30 %s %s bytes[%s]=%d\n", label, group.label, name, n)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		fmt.Printf("ROUND30 %s %s total_bytes=%d\n", label, group.label, total)
	}
}

// round30Search prints one query's hits and modes.
func round30Search(t *testing.T, s *Store, label, q string) {
	t.Helper()
	res, err := s.Search(context.Background(), q, 10)
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	var ids []string
	for _, h := range res.Hits {
		ids = append(ids, h.ID)
	}
	var modes []string
	for _, term := range res.Terms {
		modes = append(modes, fmt.Sprintf("%s:%s", term.Term, term.Mode))
	}
	fmt.Printf("ROUND30 %s q=%q total=%d hits=%v modes=%v\n", label, q, res.Total, ids, modes)
}

// TestRound30Prepared measures a disposable prepared-store copy. ROUND30_DIR
// must be the copy's data directory. Set ROUND30_OLD=1 to report the
// pre-round behavior (no reindex expected, word mode).
func TestRound30Prepared(t *testing.T) {
	dir := os.Getenv("ROUND30_DIR")
	if dir == "" {
		t.Skip("ROUND30_DIR not set")
	}
	old := os.Getenv("ROUND30_OLD") == "1"
	cfg := round30Config(t, dir)
	if old {
		fmt.Println("ROUND30 mode=prepared-old")
	} else {
		fmt.Println("ROUND30 mode=prepared-after")
	}
	round30StatsBefore := func(label string) {
		path := Path(cfg)
		fmt.Printf("ROUND30 %s db_bytes=%d wal_bytes=%d shm_bytes=%d\n", label,
			round30Size(path), round30Size(path+"-wal"), round30Size(path+"-shm"))
	}
	round30StatsBefore("before_open")
	s, cold := round30Open(t, cfg)
	fmt.Printf("ROUND30 cold_open_ms=%.3f\n", float64(cold.Microseconds())/1000)
	round30Stats(t, s, "cold")
	if !old && os.Getenv("ROUND30_NO_EXACT") != "1" {
		// Exact reindex duration: make the folded-text version stale, then
		// run the one owner of the rewrite on the open store. This is a
		// second full rewrite (a stale fts_version alone now selects the
		// trigram-only coverage pass).
		if _, err := s.db.ExecContext(context.Background(), "UPDATE meta SET value='old' WHERE key='fts_text_version'"); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := s.reindexFTS(context.Background()); err != nil {
			t.Fatalf("reindexFTS: %v", err)
		}
		fmt.Printf("ROUND30 exact_reindex_ms=%.3f\n", float64(time.Since(start).Microseconds())/1000)
		round30Stats(t, s, "reindexed")
	}
	for _, q := range []string{"कि", "की", "कु", "कू", "कमल", "कमला", "हिन्दी", "हिंद", "हैलो", "है"} {
		round30Search(t, s, "prepared", q)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	round30StatsBefore("after_close")
	again, warm := round30Open(t, cfg)
	fmt.Printf("ROUND30 warm_open_ms=%.3f\n", float64(warm.Microseconds())/1000)
	round30Stats(t, again, "warm")
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}

func round30SeedStore(t *testing.T, dir string) *Store {
	t.Helper()
	cfg := round30Config(t, dir)
	if err := cfg.Set(source.SettingSelected, []any{source.StoreKey("src", "store-1")}); err != nil {
		t.Fatalf("select store: %v", err)
	}
	s, _ := round30Open(t, cfg)
	if err := s.UpsertSource(context.Background(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	return s
}

func round30Upsert(t *testing.T, s *Store, batch []mail.Message) {
	t.Helper()
	const size = 500
	for len(batch) > 0 {
		n := min(size, len(batch))
		if err := s.UpsertMessages(context.Background(), 0, batch[:n]); err != nil {
			t.Fatalf("upsert %d: %v", n, err)
		}
		batch = batch[n:]
	}
}

// TestRound30Synthetic seeds one store with the 10 scripts × 4 terms fixture
// and measures accuracy, then grows it to 1,000 and 10,000 Hindi messages and
// measures a two-rune scan. ROUND30_DIR must be an empty/new data directory.
func TestRound30Synthetic(t *testing.T) {
	dir := os.Getenv("ROUND30_DIR")
	if dir == "" {
		t.Skip("ROUND30_DIR not set")
	}
	old := os.Getenv("ROUND30_OLD") == "1"
	mode := "after"
	if old {
		mode = "before"
	}
	fmt.Printf("ROUND30 mode=synthetic-%s\n", mode)
	s := round30SeedStore(t, dir)
	var batch []mail.Message
	for _, sample := range mailtest.IndicMatras() {
		for i, term := range sample.Terms {
			m := mailtest.Message("src", fmt.Sprintf("%s-%d", sample.Script, i))
			m.Subject, m.Body = "notes", term
			batch = append(batch, m)
		}
	}
	round30Upsert(t, s, batch)
	for _, sample := range mailtest.IndicMatras() {
		for _, term := range sample.Terms {
			round30Search(t, s, "synthetic", term)
		}
	}
	// Grow to 1,000 then 10,000 Hindi messages; exactly one carries कि.
	// Before the round the query is word mode, so a scan latency has no
	// meaning (every message shares the base token).
	if !old {
		round30AddHindi(t, s, 1, 1000)
		round30Latency(t, s, "scan-ki-1000")
		round30AddHindi(t, s, 1001, 10000)
		round30Latency(t, s, "scan-ki-10000")
	}
	round30Stats(t, s, "synthetic")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func round30AddHindi(t *testing.T, s *Store, from, to int) {
	t.Helper()
	var batch []mail.Message
	for i := from; i <= to; i++ {
		m := mailtest.Message("src", fmt.Sprintf("h%05d", i))
		m.Subject, m.Body = "notes", "की"
		if i == 1 {
			m.Body = "कि"
		}
		batch = append(batch, m)
	}
	round30Upsert(t, s, batch)
}

func round30Latency(t *testing.T, s *Store, label string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Search(ctx, "कि", 10); err != nil {
		t.Fatal(err)
	}
	var ns []int64
	for range 30 {
		start := time.Now()
		res, err := s.Search(ctx, "कि", 10)
		if err != nil {
			t.Fatal(err)
		}
		if res.Total == 0 {
			t.Fatalf("%s: no exact-matra match", label)
		}
		ns = append(ns, time.Since(start).Nanoseconds())
	}
	slices.Sort(ns)
	median := time.Duration((ns[14] + ns[15]) / 2)
	p95 := time.Duration(ns[28])
	fmt.Printf("ROUND30 %s n=30 min=%s median=%s p95=%s max=%s\n", label,
		time.Duration(ns[0]), median, p95, time.Duration(ns[29]))
}
