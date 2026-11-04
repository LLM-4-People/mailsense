package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// BenchmarkRoutePageInstantOrder35k measures a cold triage page over the
// 35k-message fixture the installer sees. Messages are inserted newest first,
// so row id descending is oldest first: the old page (ORDER BY message_id
// DESC) and the instant order disagree on this fixture. Every iteration drops
// the cached total, so the reported time is the cold page + total read.
func BenchmarkRoutePageInstantOrder35k(b *testing.B) {
	if testing.Short() {
		b.Skip("35k fixture: skipped in -short")
	}
	cfg := configtest.New(b, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	s, err := Open(b.Context(), cfg, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertSource(b.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		b.Fatal(err)
	}
	const messages = 35000
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	msgs := make([]mail.Message, 0, messages)
	for i := range messages {
		at := base.Add(-time.Duration(i) * time.Minute).Format(mail.TimeLayout)
		msgs = append(msgs, mail.Message{
			SourceID: "src", StoreID: "store-1", StoreName: "Me",
			FolderPath: "Inbox", FolderRole: mail.RoleInbox, ID: fmt.Sprintf("bench-%05d", i),
			Subject: "bench", SenderAddress: "a@example.org", ReceivedAt: at, SentOn: at, AddressedToMe: mail.AddressedTo,
		})
	}
	if err := s.UpsertMessages(b.Context(), 0, msgs); err != nil {
		b.Fatal(err)
	}
	var ids []int64
	if err := s.EachMessage(b.Context(), MessageFilter{}, func(r *MessageRow) error {
		ids = append(ids, r.RowID)
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	verdicts := make([]VerdictRow, len(ids))
	for i, id := range ids {
		verdicts[i] = VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}
	}
	if err := s.WriteVerdicts(b.Context(), "bench", verdicts); err != nil {
		b.Fatal(err)
	}
	tx, err := s.db.BeginTx(b.Context(), nil)
	if err != nil {
		b.Fatal(err)
	}
	insert, err := tx.PrepareContext(b.Context(), `INSERT INTO route_answers
		(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, 'fp-bench', 1, ?, 0, '2026-11-03T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	for _, id := range ids {
		if _, err := insert.ExecContext(b.Context(), id, routeJSON); err != nil {
			b.Fatal(err)
		}
	}
	if err := insert.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := s.db.ExecContext(b.Context(), "ANALYZE"); err != nil {
		b.Fatal(err)
	}
	filter := MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}
	where, args := filter.sql(visibleSQL)
	planArgs := append(append([]any{"fp-bench"}, args...), 100, 0)
	rows, err := s.db.QueryContext(b.Context(), "EXPLAIN QUERY PLAN "+routePageQuery(where), planArgs...)
	if err != nil {
		b.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			b.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	_ = rows.Close()
	if strings.Contains(plan.String(), "TEMP B-TREE") {
		b.Fatalf("route page sorts at %d rows:\n%s", messages, plan.String())
	}
	b.Logf("route page plan at %d rows:\n%s", messages, plan.String())

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.routeTotalsMu.Lock()
		s.routeTotals = nil
		s.routeTotalsMu.Unlock()
		refs, total, err := s.RoutePage(b.Context(), "fp-bench", filter, 50, 0)
		if err != nil {
			b.Fatal(err)
		}
		if total != messages || len(refs) != 50 || refs[0].MessageID != ids[0] {
			b.Fatalf("cold page: %d refs, total %d, first %d (newest %d)", len(refs), total, refs[0].MessageID, ids[0])
		}
	}
}
