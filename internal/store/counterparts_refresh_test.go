package store

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// TestCounterpartRefreshAppliesOneMailWrite guards the incremental contract:
// one mail write produces one delta build, the next population reflects the
// new counterpart, and repeated reads of that generation start no build.
func TestCounterpartRefreshAppliesOneMailWrite(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	ctx := t.Context()

	first, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.fullBuilds != 1 {
		t.Fatalf("cold population performed %d full builds, want 1", first.fullBuilds)
	}
	builds := s.companyCounterpartsBuildCount()

	message := mailtest.Message("src", "newco-in")
	message.SenderName, message.SenderAddress = "New Person", "new@newco.example"
	message.ReceivedAt, message.SentOn = "2026-10-22T09:00:00Z", "2026-10-22T09:00:00Z"
	message.ConversationID = "C-newco"
	message.Subject = "New company"
	mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)

	second, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("the write did not produce a new population")
	}
	if second.fullBuilds != 1 {
		t.Fatalf("the delta performed a full scan: %d full builds", second.fullBuilds)
	}
	if !counterpartHasAddress(second, "new@newco.example") {
		t.Fatal("the new counterpart is missing from the refreshed population")
	}
	if got := s.companyCounterpartsBuildCount(); got != builds+1 {
		t.Fatalf("builds after one write = %d, want %d", got, builds+1)
	}

	res, err := s.ExecuteMailQuery(ctx, companyPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range res.Rows {
		if row.Key == "d:newco.example" && row.Messages == 1 && row.Received == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refreshed ranking does not show the new company: %+v", res.Rows)
	}

	// The generation has not moved: another read must reuse the installed
	// pointer and start no build.
	builds = s.companyCounterpartsBuildCount()
	third, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third != second {
		t.Fatal("a repeated read rebuilt the population")
	}
	if got := s.companyCounterpartsBuildCount(); got != builds {
		t.Fatalf("a repeated read started %d builds", got-builds)
	}

	// A contact write moves the mail generation but not one counterpart
	// fact: the marker scan adopts the installed population instead of
	// reading the pair rows again.
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.ReplaceContacts(ctx, ref, "Contacts", []contacts.Contact{
		{EntryID: "contact-only", DisplayName: "Contact Only", Company: "Contact Co",
			Emails: []string{"contact@contactonly.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	afterContact, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterContact != second {
		t.Fatal("a contact write rebuilt the counterpart population")
	}
	if afterContact.fullBuilds != 1 {
		t.Fatalf("a contact write performed %d full builds", afterContact.fullBuilds)
	}
}

// TestCounterpartRefreshAppliesEditsRemovalsAndVerdicts covers the three
// incremental deltas that are not additions: a message edit, a scope change
// that removes a message from owner content, and a deletion.
func TestCounterpartRefreshAppliesEditsRemovalsAndVerdicts(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	ids := seedCompanyMailbox(t, s)
	ctx := t.Context()

	first, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	edited, err := s.GetMessage(ctx, "src", "store-1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	edited.IsRead = false
	edited.Subject = "Edited counterpart subject"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{edited}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteVerdicts(ctx, "test", []VerdictRow{
		{RowID: ids["a3"], Verdict: scope.Shared, Reason: scope.ReasonRecipientsOther},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", ids["b4"]); err != nil {
		t.Fatal(err)
	}
	s.touchMail()

	second, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("the edits did not produce a new population")
	}
	if second.fullBuilds != 1 {
		t.Fatalf("the edit/removal delta performed a full scan: %d", second.fullBuilds)
	}
	editedMessage, ok := counterpartMessageOf(second, "a1")
	if !ok || editedMessage.isRead || editedMessage.subject != "Edited counterpart subject" {
		t.Fatalf("the edited message was not refreshed: %+v/%v", editedMessage, ok)
	}
	if _, ok := counterpartMessageOf(second, "a3"); ok {
		t.Fatal("a message whose verdict left owner content stayed in the population")
	}
	if _, ok := counterpartMessageOf(second, "b4"); ok {
		t.Fatal("a deleted message stayed in the population")
	}

	// The deep profile uses the same refreshed population.
	got, err := s.CompanyProfiles(ctx, []string{"c:beta inc"}, time.Time{}, time.Time{}, 0, 30,
		worldTime("2026-10-26T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	want := orderedCompanyProfiles(t, s, []string{"c:beta inc"}, time.Time{}, time.Time{}, 12, 30,
		worldTime("2026-10-26T12:00:00Z"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the refreshed profile differs from the ordered SQL fold:\n got %+v\nwant %+v", got, want)
	}
}

// TestCounterpartRefreshDetectsRowidReuse guards the physical row identity:
// SQLite can hand a deleted maximum message id to the next insert, and a
// fresh message starts at revision zero, so revision equality alone must not
// keep the deleted row's pairs.
func TestCounterpartRefreshDetectsRowidReuse(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	ctx := t.Context()

	first, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var maxID int64
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(id) FROM messages").Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", maxID); err != nil {
		t.Fatal(err)
	}
	message := mailtest.Message("src", "reused-row")
	message.SenderName, message.SenderAddress = "Reused", "reused@newco.example"
	message.ReceivedAt, message.SentOn = "2026-10-25T09:00:00Z", "2026-10-25T09:00:00Z"
	message.ConversationID = "C-reused"
	message.Subject = "A new row under the old id"
	mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)
	var reusedID int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE msg_id = 'reused-row'").Scan(&reusedID); err != nil {
		t.Fatal(err)
	}
	if reusedID != maxID {
		t.Fatalf("the insert did not reuse the deleted row id (%d != %d); the test scenario is stale", reusedID, maxID)
	}

	second, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("the reused row id kept the deleted message's population")
	}
	if !counterpartHasAddress(second, "reused@newco.example") {
		t.Fatal("the new message under the reused id is missing")
	}
	if second.fullBuilds != 1 {
		t.Fatalf("the rowid reuse refresh performed %d full builds", second.fullBuilds)
	}
}

// TestCounterpartSnapshotDuringRefresh proves a read already in flight is
// served the complete previous population, not a half-merged one, and that
// the refresh afterwards serves the new facts.
func TestCounterpartSnapshotDuringRefresh(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	ctx := t.Context()

	first, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.counterpartsBuildHook = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}

	type buildResult struct {
		counterparts *CompanyCounterparts
		err          error
	}
	done := make(chan buildResult, 1)
	s.touch() // invalidate the generation so the build starts
	go func() {
		counterparts, err := s.CompanyCounterparts(ctx)
		done <- buildResult{counterparts: counterparts, err: err}
	}()
	<-started

	message := mailtest.Message("src", "during-refresh")
	message.SenderName, message.SenderAddress = "Refresh", "refresh@newco.example"
	message.ReceivedAt, message.SentOn = "2026-10-23T09:00:00Z", "2026-10-23T09:00:00Z"
	message.ConversationID = "C-refresh"
	message.Subject = "Written during the refresh"
	mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)

	// The snapshot read must not block on the in-flight build and must see
	// the complete installed population.
	staleCh := make(chan *CompanyCounterparts, 1)
	go func() {
		stale, err := s.companyCounterpartsSnapshot(ctx)
		if err != nil {
			t.Error(err)
			staleCh <- nil
			return
		}
		staleCh <- stale
	}()
	var stale *CompanyCounterparts
	select {
	case stale = <-staleCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the snapshot read blocked on the refresh")
	}
	if stale != first {
		t.Fatal("the snapshot read did not return the installed population")
	}

	close(release)
	var got *CompanyCounterparts
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		got = result.counterparts
	case <-time.After(2 * time.Second):
		t.Fatal("the refresh did not finish")
	}
	if !counterpartHasAddress(got, "refresh@newco.example") {
		t.Fatal("the post-refresh population misses the write")
	}
	if _, ok := counterpartMessageOf(stale, "during-refresh"); ok {
		t.Fatal("the stale snapshot contains a message written during its refresh")
	}
}

// TestCounterpartOwnerChangeForcesFullScan proves the one fact the delta
// cannot apply message by message: the owner address set removes pairs from
// every message, so it invalidates the incremental base.
func TestCounterpartOwnerChangeForcesFullScan(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	ctx := t.Context()

	first, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !counterpartHasAddress(first, "personal@gmail.com") {
		t.Fatal("the fixture has no personal counterpart pair")
	}

	// The shared free-mail address is not a declared company address, so
	// declaring it internal removes the pair without changing a company
	// organization (the domain organization is already suppressed).
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(ctx, ref, []string{"alex@acme.test", "personal@gmail.com"}); err != nil {
		t.Fatal(err)
	}
	second, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.fullBuilds != first.fullBuilds+1 {
		t.Fatalf("owner change performed %d full builds, want %d", second.fullBuilds, first.fullBuilds+1)
	}
	if counterpartHasAddress(second, "personal@gmail.com") {
		t.Fatal("a newly declared owner address stayed a counterpart")
	}

	res, err := s.ExecuteMailQuery(ctx, companyPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Total != 8 {
		t.Fatalf("an owner change to a shared domain changed the ranking: %+v", res)
	}
}

// TestCounterpartIncrementalMatchesFullBuild is the parity guard for the
// delta: after a mixed batch of writes, the incrementally refreshed
// population and a fresh full scan must feed the same ranking and profile
// results.
func TestCounterpartIncrementalMatchesFullBuild(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	ids := seedCompanyMailbox(t, s)
	ctx := t.Context()

	if _, err := s.CompanyCounterparts(ctx); err != nil {
		t.Fatal(err)
	}

	message := mailtest.Message("src", "parity-new")
	message.SenderName, message.SenderAddress = "Parity", "new@parity.test"
	message.ReceivedAt, message.SentOn = "2026-10-24T09:00:00Z", "2026-10-24T09:00:00Z"
	message.ConversationID = "C-parity"
	message.Subject = "Parity addition"
	mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)

	edited, err := s.GetMessage(ctx, "src", "store-1", "b1")
	if err != nil {
		t.Fatal(err)
	}
	edited.HasAttachments = false
	edited.IsRead = false
	if err := s.UpsertMessages(ctx, 0, []mail.Message{edited}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteVerdicts(ctx, "test", []VerdictRow{
		{RowID: ids["a1"], Verdict: scope.Owner, Reason: scope.ReasonSentByOwner},
	}); err != nil {
		t.Fatal(err)
	}

	incremental, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	full, err := s.buildCompanyCounterparts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if full.fullBuilds != 1 {
		t.Fatalf("the oracle is not a fresh full build: %d", full.fullBuilds)
	}
	if incremental.fullBuilds != 1 {
		t.Fatalf("the production read performed %d full builds", incremental.fullBuilds)
	}
	if got, want := counterpartRowSet(t, incremental), counterpartRowSet(t, full); !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental rows differ from the full build:\n got %+v\nwant %+v", got, want)
	}

	book, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plans := []struct {
		name string
		edit func(*mailquery.Plan)
	}{
		{"all", nil},
		{"unread", func(p *mailquery.Plan) { p.Unread = boolValue(true) }},
		{"without attachments", func(p *mailquery.Plan) { p.HasAttachments = boolValue(false) }},
		{"sent", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionSent }},
		{"received", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionReceived }},
		{"window", func(p *mailquery.Plan) {
			p.Since, p.Until = "2026-10-03T00:00:00Z", "2026-10-09T00:00:00Z"
		}},
		{"exact addresses", func(p *mailquery.Plan) {
			p.ContactAddresses = []string{"carol@beta.test", "dave@gamma.dev"}
		}},
		{"label", func(p *mailquery.Plan) { p.Contact = "beta inc" }},
		{"exchanges", func(p *mailquery.Plan) { p.Metric = mailquery.MetricExchanges }},
	}
	for _, tc := range plans {
		t.Run(tc.name, func(t *testing.T) {
			plan := companyPlan()
			if tc.edit != nil {
				tc.edit(&plan)
			}
			got := companyQueryResult(foldCompanyCounterparts(incremental, book, plan), plan)
			want := companyQueryResult(foldCompanyCounterparts(full, book, plan), plan)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("delta ranking differs:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// counterpartMessageOf finds one message by source message id.
func counterpartMessageOf(counterparts *CompanyCounterparts, msgID string) (counterpartMessage, bool) {
	for _, message := range counterparts.Messages {
		if message.msgID == msgID {
			return message, true
		}
	}
	return counterpartMessage{}, false
}

// counterpartRowSet returns the cached stream as an order-independent list,
// the same shape a profile fold reads.
func counterpartRowSet(t *testing.T, counterparts *CompanyCounterparts) []counterpartRow {
	t.Helper()
	rows := newCachedCounterpartRows(counterparts, "", "")
	out := []counterpartRow{}
	for rows.Next() {
		row, err := rows.Row()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	key := func(r counterpartRow) string {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%t",
			r.sourceID, r.storeID, r.msgID, r.addressNorm, r.isSent, r.address, r.name,
			r.conversationID, r.instant, r.subject, r.senderName, r.hasAttachments, r.isRead)
	}
	sort.Slice(out, func(i, j int) bool { return key(out[i]) < key(out[j]) })
	return out
}

// counterpartBenchStore builds a mailbox large enough for the marker scan to
// be measurable without the fixture build tag. The two benchmarks below run
// on the same store, so their ns/op compare the identical population.
func counterpartBenchStore(b *testing.B) (*Store, int64) {
	b.Helper()
	cfg := configtest.New(b, map[string]any{
		"mail.domains.shared":  []any{"gmail.com"},
		source.SettingSelected: mailtest.Picked([]string{"src"}),
	})
	s, err := Open(b.Context(), cfg, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertSource(b.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		b.Fatal(err)
	}
	const messages = 4000
	msgs := make([]mail.Message, 0, messages)
	for i := range messages {
		message := mailtest.Message("src", fmt.Sprintf("m%04d", i))
		message.SenderName = fmt.Sprintf("Person %d", i)
		message.SenderAddress = fmt.Sprintf("person%d@company%d.test", i, i%24)
		message.ReceivedAt, message.SentOn = "2026-10-01T09:00:00Z", "2026-10-01T09:00:00Z"
		message.ConversationID = fmt.Sprintf("C-%04d", i)
		message.Subject = fmt.Sprintf("Benchmark message %d", i)
		msgs = append(msgs, message)
	}
	if err := s.UpsertMessages(b.Context(), 0, msgs); err != nil {
		b.Fatal(err)
	}
	var ids []int64
	if err := s.EachMessage(b.Context(), MessageFilter{}, func(row *MessageRow) error {
		ids = append(ids, row.RowID)
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	verdicts := make([]VerdictRow, len(ids))
	for i, id := range ids {
		verdicts[i] = VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch}
	}
	if err := s.WriteVerdicts(b.Context(), "bench", verdicts); err != nil {
		b.Fatal(err)
	}
	return s, ids[0]
}

// BenchmarkCompanyCounterpartsCold measures the full all-time pair scan.
func BenchmarkCompanyCounterpartsCold(b *testing.B) {
	s, _ := counterpartBenchStore(b)
	b.ResetTimer()
	for b.Loop() {
		counterparts, err := s.buildCompanyCounterparts(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if len(counterparts.Pairs) == 0 {
			b.Fatal("no counterpart pairs")
		}
	}
}

// BenchmarkCompanyCounterpartsRefresh measures one generation's incremental
// refresh: a marker scan plus one changed message, never the all-time pair
// scan. fullBuilds stays constant across every delta.
func BenchmarkCompanyCounterpartsRefresh(b *testing.B) {
	s, id := counterpartBenchStore(b)
	previous, err := s.buildCompanyCounterparts(b.Context())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		// The write is the trigger, not the refresh cost under test.
		b.StopTimer()
		if _, err := s.db.ExecContext(b.Context(),
			"UPDATE messages SET revision = revision + 1, subject = ? WHERE id = ?",
			fmt.Sprintf("Refresh %d", i), id); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		next, err := s.buildCompanyCounterpartsFrom(b.Context(), previous)
		if err != nil {
			b.Fatal(err)
		}
		if next.fullBuilds != previous.fullBuilds {
			b.Fatalf("refresh performed a full scan after %d deltas", i)
		}
		previous = next
	}
}
