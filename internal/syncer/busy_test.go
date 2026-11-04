package syncer

import (
	"context"
	"database/sql"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// lockStoreWrites holds the database's write lock on its own connection
// until the returned function runs.
func lockStoreWrites(t *testing.T, cfg *config.Config) func() {
	t.Helper()
	db, err := sql.Open("sqlite", "file://"+(&url.URL{Path: store.Path(cfg)}).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
			_ = db.Close()
		})
	}
}

type runResult struct {
	rep Report
	err error
}

// A transient writer lock that outlives store.busy_timeout is retried by the
// store write owner: the sync run succeeds instead of aborting.
func TestSyncFlushRetriesBusy(t *testing.T) {
	s, st, _, cfg := setup(t, map[string]any{
		"sync.batch_size":           2,
		"store.busy_timeout":        "20ms",
		"index.store_busy_attempts": 10,
		"index.store_busy_delay":    "20ms",
	})
	if err := st.UpsertSource(t.Context(), "fake", "fake", "fake", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	block, entered := make(chan struct{}), make(chan struct{})
	c := &fakeConn{id: "fake", block: block, entered: entered, items: map[string][]item{
		"Inbox": items(2, inbox, "i"),
		"Sent":  items(1, sent, "s"),
	}}
	done := make(chan runResult, 1)
	go func() {
		rep, err := s.Run(t.Context(), c, nil)
		done <- runResult{rep: rep, err: err}
	}()
	<-entered
	release := lockStoreWrites(t, cfg)
	defer release()
	close(block)
	time.AfterFunc(100*time.Millisecond, release)

	res := <-done
	if res.err != nil || res.rep.Status != runstate.Succeeded || res.rep.Totals.Written != 3 {
		t.Fatalf("run: %+v %v", res.rep, res.err)
	}
}
