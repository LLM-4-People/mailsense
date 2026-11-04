package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// holdWriteLock takes the database's write lock on its own connection until
// the returned function runs.
func holdWriteLock(t *testing.T, cfg *config.Config) func() {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(Path(cfg), baseParams(0)))
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

func TestBusyRetryCompletesAfterLockRelease(t *testing.T) {
	s, cfg := open(t, map[string]any{
		"store.busy_timeout":        "20ms",
		"index.store_busy_attempts": 10,
		"index.store_busy_delay":    "20ms",
	})
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "seed")}); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := s.EachMessage(ctx, MessageFilter{}, func(r *MessageRow) error {
		if r.Message.ID == "seed" {
			rowID = r.RowID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rowID == 0 {
		t.Fatal("seed message not stored")
	}

	// A sync flush waits out a writer that holds the lock for about twice
	// store.busy_timeout.
	release := holdWriteLock(t, cfg)
	time.AfterFunc(60*time.Millisecond, release)
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "flushed")}); err != nil {
		t.Fatalf("upsert under a released lock: %v", err)
	}
	release()

	// A paid triage decision waits out the same writer.
	release = holdWriteLock(t, cfg)
	time.AfterFunc(60*time.Millisecond, release)
	key := TriageKey{Fingerprint: "busy", Decider: "test"}
	err := s.SaveDecision(ctx, key, Decision{MessageID: rowID, Revision: 1, StateHash: "h", Answers: `{}`})
	if err != nil {
		t.Fatalf("save decision under a released lock: %v", err)
	}
	release()
	stored, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored decisions %v", stored)
	}
}

func TestBusyRetryStopsOnCancel(t *testing.T) {
	s, cfg := open(t, map[string]any{
		"store.busy_timeout":        "10ms",
		"index.store_busy_attempts": 10,
		"index.store_busy_delay":    "2s",
	})
	release := holdWriteLock(t, cfg)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "cancelled")})
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("cancel took %s, the full retry budget", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled write did not return")
	}
}

func TestBusyRetryDoesNotRetryOtherErrors(t *testing.T) {
	s, _ := open(t, map[string]any{
		"index.store_busy_attempts": 5,
		"index.store_busy_delay":    "10ms",
	})
	attempts := 0
	err := s.busy.WithBusyRetry(t.Context(), "test write", func() error {
		attempts++
		return errors.New("constraint failed")
	})
	if err == nil || attempts != 1 || IsBusy(err) || errors.Is(err, ErrStoreBusy) {
		t.Fatalf("attempts %d, err %v", attempts, err)
	}
}

// The pair is a live setting: a runtime change applies to the next write,
// without reopening the store. The policy must read it per call, not capture
// it at Open.
func TestBusyRetryReadsTheSettingPerCall(t *testing.T) {
	s, cfg := open(t, map[string]any{
		"store.busy_timeout":        "10ms",
		"index.store_busy_attempts": 1,
		"index.store_busy_delay":    "10ms",
	})
	release := holdWriteLock(t, cfg)
	err := s.UpsertMessages(t.Context(), 0, []mail.Message{mailtest.Message("src", "before")})
	release()
	if !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("one attempt must exhaust the budget: %v", err)
	}

	if err := cfg.Set("index.store_busy_attempts", 5); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("index.store_busy_delay", "10ms"); err != nil {
		t.Fatal(err)
	}
	release = holdWriteLock(t, cfg)
	time.AfterFunc(60*time.Millisecond, release)
	if err := s.UpsertMessages(t.Context(), 0, []mail.Message{mailtest.Message("src", "after")}); err != nil {
		t.Fatalf("the changed budget did not apply: %v", err)
	}
	release()
}

// Every Q&A write goes through the store's one busy policy. A writer that
// releases inside the retry window must not fail the answer that the API is
// about to return to the user; a writer that never releases must surface the
// busy error instead of a silent success.
func TestSaveQARetriesABusyWrite(t *testing.T) {
	s, cfg := open(t, map[string]any{
		"store.busy_timeout":        "20ms",
		"index.store_busy_attempts": 10,
		"index.store_busy_delay":    "20ms",
	})
	ctx := t.Context()
	qa := QAHistory{
		Question: "busy question", Answer: "busy answer", Mode: "generated",
		Intent: "freeform", Language: "fr", Counts: map[string]int{"total": 1},
	}
	withLock := func(hold time.Duration, write func() error) error {
		t.Helper()
		release := holdWriteLock(t, cfg)
		time.AfterFunc(hold, release)
		err := write()
		release()
		return err
	}

	var saved QAHistory
	err := withLock(60*time.Millisecond, func() error {
		var err error
		saved, err = s.SaveQA(ctx, qa)
		return err
	})
	if err != nil {
		t.Fatalf("SaveQA under a released lock: %v", err)
	}
	if saved.ID <= 0 || saved.CreatedAt == "" {
		t.Fatalf("saved row %+v", saved)
	}
	if err := withLock(60*time.Millisecond, func() error {
		return s.SaveQAPlan(ctx, strings.Repeat("a", 64), "fr", testContactPlan())
	}); err != nil {
		t.Fatalf("SaveQAPlan under a released lock: %v", err)
	}
	var cleared int64
	if err := withLock(60*time.Millisecond, func() error {
		var err error
		cleared, err = s.ClearQA(ctx)
		return err
	}); err != nil || cleared != 1 {
		t.Fatalf("ClearQA under a released lock: %d %v", cleared, err)
	}
	if err := s.SaveQAPlan(ctx, strings.Repeat("b", 64), "fr", testContactPlan()); err != nil {
		t.Fatal(err)
	}
	if err := withLock(60*time.Millisecond, func() error {
		var err error
		cleared, err = s.ClearQAPlans(ctx)
		return err
	}); err != nil || cleared != 1 {
		t.Fatalf("ClearQAPlans under a released lock: %d %v", cleared, err)
	}

	release := holdWriteLock(t, cfg)
	defer release()
	if _, err := s.SaveQA(ctx, qa); !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("a permanent lock returned %v, want ErrStoreBusy", err)
	}
}
