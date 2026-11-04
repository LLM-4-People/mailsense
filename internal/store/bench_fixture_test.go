//go:build fixture

package store

// Benchmarks over a prepared fixture store (scripts/bench-layers.sh:
// PERF_STORE_DIR). The store is opened read-write because Open runs the
// migrations/repairs; no benchmark writes a row. One iteration scans the
// whole mailbox, so run with -benchtime=1x.

import (
	"os"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
)

func benchStore(b *testing.B) *Store {
	b.Helper()
	dir := os.Getenv("PERF_STORE_DIR")
	if dir == "" {
		b.Skip("PERF_STORE_DIR is not set (scripts/bench-layers.sh sets it)")
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		b.Fatal(err)
	}
	st, err := Open(b.Context(), cfg, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return st
}

func benchFingerprint(b *testing.B, s *Store) string {
	b.Helper()
	var fingerprint string
	if err := s.db.QueryRowContext(b.Context(), `SELECT fingerprint FROM route_answers LIMIT 1`).Scan(&fingerprint); err != nil {
		b.Fatalf("no route answers in the prepared store: %v", err)
	}
	return fingerprint
}

// BenchmarkEachPriorityMessage measures the full read used by tests and the
// rare fallback: every owner message with its route/preview answers and
// bounded body head.
func BenchmarkEachPriorityMessage(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	b.ResetTimer()
	for range b.N {
		rows := 0
		// nil selects every answer: the full-read fallback. The priority
		// service passes its role keys to the same read.
		if err := s.EachPriorityMessage(b.Context(), fingerprint, false, nil, func(PriorityRow) error {
			rows++
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		if rows == 0 {
			b.Fatal("no rows")
		}
	}
}

// BenchmarkEachPriorityReply measures the light first pass: route answers
// and conversation ids, no message body. It is the full-decode fallback
// (nil selection); the priority package's benchmark measures the production
// role-key selection.
func BenchmarkEachPriorityReply(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	for _, tc := range []struct {
		name    string
		preview bool
	}{
		{"preview=false", false},
		{"preview=true", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for range b.N {
				rows := 0
				if err := s.EachPriorityReply(b.Context(), fingerprint, tc.preview, nil, func(PriorityRow) error {
					rows++
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				if rows == 0 {
					b.Fatal("no rows")
				}
			}
		})
	}
}

// BenchmarkOwnerFilterIDs measures the owner-scope id list a semantic search
// materializes before every filtered vector scan.
func BenchmarkOwnerFilterIDs(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		ids, err := s.FilterIDs(b.Context(), SearchFilter{OwnerOnly: true})
		if err != nil {
			b.Fatal(err)
		}
		if len(ids) == 0 {
			b.Fatal("no ids")
		}
	}
}

// BenchmarkSearchText measures a common full-text query within the owner
// scope (the path every search and every question runs first).
func BenchmarkSearchText(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		res, err := s.SearchText(b.Context(), "projet", SearchFilter{OwnerOnly: true}, 200)
		if err != nil {
			b.Fatal(err)
		}
		if res.Total == 0 {
			b.Fatal("no matches")
		}
	}
}

// BenchmarkMessageList measures one All-Mail page with whole-mailbox counts,
// which the UI requests on every page.
func BenchmarkMessageList(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		page, err := s.MessageList(b.Context(), MessageListQuery{Limit: 100, Counts: true})
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Messages) == 0 {
			b.Fatal("no messages")
		}
	}
}

// BenchmarkCompanyBook measures one address-book materialization.
func BenchmarkCompanyBook(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		book, err := s.CompanyBook(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if book == nil {
			b.Fatal("no book")
		}
	}
}

// BenchmarkCompanyCounterparts measures one cold build of the counterpart
// population: the single scan every company ranking and every company
// profile read shares for one mail generation.
func BenchmarkCompanyCounterparts(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		counterparts, err := s.buildCompanyCounterparts(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if len(counterparts.Pairs) == 0 {
			b.Fatal("no counterpart pairs")
		}
	}
}

// BenchmarkCompanyList measures the companies screen's ranking read: one
// all-time counterpart fold, exactly what world.List asks for on boot.
func BenchmarkCompanyList(b *testing.B) {
	s := benchStore(b)
	plan := mailqueryPlan()
	plan.Metric = mailquery.MetricMessages
	b.ResetTimer()
	for range b.N {
		res, err := s.ExecuteMailQuery(b.Context(), plan, time.UTC)
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Rows) == 0 {
			b.Fatal("no ranked companies")
		}
	}
}

// BenchmarkCompanyProfile measures one deep profile from the generation's
// cached counterpart population: people, recent, thread states and summaries,
// calendar and deadlines.
func BenchmarkCompanyProfile(b *testing.B) {
	s := benchStore(b)
	plan := mailqueryPlan()
	res, err := s.ExecuteMailQuery(b.Context(), plan, time.UTC)
	if err != nil {
		b.Fatal(err)
	}
	if len(res.Rows) == 0 {
		b.Skip("no ranked companies in the prepared store")
	}
	key := res.Rows[0].Key
	now := time.Now()
	b.ResetTimer()
	for range b.N {
		if _, err := s.CompanyProfile(b.Context(), key, time.Time{}, now, 6, 30, now); err != nil {
			b.Fatal(err)
		}
	}
}

func mailqueryPlan() mailquery.Plan {
	return mailquery.Plan{
		Kind:      mailquery.KindCompanies,
		Action:    mailquery.ActionRank,
		Direction: mailquery.DirectionBoth,
		Metric:    mailquery.MetricExchanges,
		TimeRange: mailquery.TimeAll,
		Limit:     50,
		Order:     mailquery.OrderDesc,
	}
}

// BenchmarkCompanyProfiles measures the batched warm path: the five widest
// company profiles from one address-book read and the generation's cached
// counterpart population.
func BenchmarkCompanyProfiles(b *testing.B) {
	s := benchStore(b)
	res, err := s.ExecuteMailQuery(b.Context(), mailqueryPlan(), time.UTC)
	if err != nil {
		b.Fatal(err)
	}
	keys := make([]string, 0, 5)
	for _, row := range res.Rows {
		if len(keys) == 5 {
			break
		}
		keys = append(keys, row.Key)
	}
	if len(keys) == 0 {
		b.Skip("no ranked companies in the prepared store")
	}
	now := time.Now()
	b.ResetTimer()
	for range b.N {
		profiles, err := s.CompanyProfiles(b.Context(), keys, time.Time{}, now, 6, 30, now)
		if err != nil {
			b.Fatal(err)
		}
		if len(profiles) != len(keys) {
			b.Fatalf("profiles %d, want %d", len(profiles), len(keys))
		}
	}
}

func benchConversationIDs(b *testing.B, s *Store, limit int) []string {
	b.Helper()
	rows, err := s.db.QueryContext(b.Context(),
		`SELECT DISTINCT conversation_id FROM messages WHERE conversation_id <> '' ORDER BY conversation_id LIMIT ?`, limit)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			b.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		b.Skip("no conversations in the prepared store")
	}
	return ids
}

// BenchmarkThreadStates measures the cross-store state resolution over 500
// conversations, the read that dominates a cold priority build and every
// company profile.
func BenchmarkThreadStates(b *testing.B) {
	s := benchStore(b)
	ids := benchConversationIDs(b, s, 500)
	b.ResetTimer()
	for range b.N {
		states, err := s.ThreadStates(b.Context(), ids, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(states) == 0 {
			b.Fatal("no states")
		}
	}
}

// BenchmarkThreadStatesMailbox measures the whole-mailbox state resolution:
// every conversation in the fixture, not a 500-conversation page. It is the
// read whose deferred evidence must stay bounded instead of holding the
// entire frontier at once.
func BenchmarkThreadStatesMailbox(b *testing.B) {
	s := benchStore(b)
	ids := benchConversationIDs(b, s, -1) // SQLite LIMIT -1 selects every conversation
	b.ResetTimer()
	for range b.N {
		states, err := s.ThreadStates(b.Context(), ids, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(states) == 0 {
			b.Fatal("no states")
		}
	}
}

// BenchmarkThreadSummaries measures the newest-message summary read over 500
// conversations.
func BenchmarkThreadSummaries(b *testing.B) {
	s := benchStore(b)
	ids := benchConversationIDs(b, s, 500)
	b.ResetTimer()
	for range b.N {
		summaries, err := s.ThreadSummaries(b.Context(), ids)
		if err != nil {
			b.Fatal(err)
		}
		if len(summaries) == 0 {
			b.Fatal("no summaries")
		}
	}
}

// BenchmarkDueDeadlines measures the open-follow-up window every agenda,
// commitment and company profile reads.
func BenchmarkDueDeadlines(b *testing.B) {
	s := benchStore(b)
	now := time.Now()
	since, until := now.Add(-30*24*time.Hour), now.Add(90*24*time.Hour)
	b.ResetTimer()
	for range b.N {
		rows, err := s.DueDeadlines(b.Context(), since, until)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) == 0 {
			b.Fatal("no due rows")
		}
	}
}

// BenchmarkConversationDigests measures the one whole-mailbox digest read
// behind the Mail conversations page and the brief. The row-count spy also
// checks the stream folded exactly one row per message: the digest total must
// equal the coverage's threaded message count.
func BenchmarkConversationDigests(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		set, err := s.ConversationDigests(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if len(set.Conversations) == 0 {
			b.Fatal("no conversations")
		}
		messages := 0
		for _, d := range set.Conversations {
			messages += d.Messages
		}
		if messages != set.Coverage.Messages-set.Coverage.Unthreaded {
			b.Fatalf("digest messages %d, coverage %d with %d unthreaded", messages, set.Coverage.Messages, set.Coverage.Unthreaded)
		}
	}
}

// BenchmarkEmbeddingStates measures the state read behind every learning
// status refresh and rebuild.
func BenchmarkEmbeddingStates(b *testing.B) {
	s := benchStore(b)
	var roles []mail.FolderRole
	for _, role := range s.cfg.Strings("index.folder_roles") {
		roles = append(roles, mail.FolderRole(role))
	}
	b.ResetTimer()
	for range b.N {
		states, err := s.EmbeddingStates(b.Context(), roles)
		if err != nil {
			b.Fatal(err)
		}
		if len(states) == 0 {
			b.Fatal("no embedding states")
		}
	}
}

// BenchmarkRoutePage measures one triage page with its cached total: the
// production role/owner filter and a real stored fingerprint. The first call
// warms the total; the iterations are the page path a repeated open runs.
func BenchmarkRoutePage(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	var roles []mail.FolderRole
	for _, role := range s.cfg.Strings("triage.folder_roles") {
		roles = append(roles, mail.FolderRole(role))
	}
	if len(roles) == 0 {
		roles = []mail.FolderRole{mail.RoleInbox}
	}
	f := MessageFilter{Roles: roles, OwnerOnly: true}
	if _, total, err := s.RoutePage(b.Context(), fingerprint, f, 100, 0); err != nil || total == 0 {
		b.Fatalf("warm route page total %d: %v", total, err)
	}
	b.ResetTimer()
	for range b.N {
		refs, total, err := s.RoutePage(b.Context(), fingerprint, f, 100, 0)
		if err != nil {
			b.Fatal(err)
		}
		if total == 0 || len(refs) == 0 {
			b.Fatalf("route page rows %d, total %d", len(refs), total)
		}
	}
}

// BenchmarkSearchTextLimit20 measures the API-sized full-text page.
func BenchmarkSearchTextLimit20(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		res, err := s.SearchText(b.Context(), "projet", SearchFilter{OwnerOnly: true}, 20)
		if err != nil {
			b.Fatal(err)
		}
		if res.Total == 0 {
			b.Fatal("no matches")
		}
	}
}

// BenchmarkSearchHydration measures the 20-row summary read behind a search
// page.
func BenchmarkSearchHydration(b *testing.B) {
	s := benchStore(b)
	res, err := s.SearchText(b.Context(), "projet", SearchFilter{OwnerOnly: true}, 20)
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]int64, 0, len(res.Hits))
	for _, hit := range res.Hits {
		ids = append(ids, hit.RowID)
	}
	b.ResetTimer()
	for range b.N {
		sums, err := s.SummariesOwner(b.Context(), ids)
		if err != nil {
			b.Fatal(err)
		}
		if len(sums) == 0 {
			b.Fatal("no summaries")
		}
	}
}

// benchCounterpartAddresses returns the people who write to the owner most
// often, excluding the owner's own addresses. A received sender is a real
// counterpart: both the received branch (sender key) and the sent branch
// (recipient address) have a row for them, so the benchmark measures the
// mixed read rather than the owner's own outbox.
func benchCounterpartAddresses(b *testing.B, s *Store, limit int) []string {
	b.Helper()
	owners, err := s.OwnerSet(b.Context())
	if err != nil {
		b.Fatal(err)
	}
	rows, err := s.db.QueryContext(b.Context(), `SELECT m.sender_norm FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+visibleSQL+` AND `+ownerWhere+` AND `+notSentByOwnerWhere+` AND m.sender_norm <> ''
		GROUP BY m.sender_norm ORDER BY COUNT(*) DESC, m.sender_norm LIMIT 20`)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			b.Fatal(err)
		}
		if owners[address] || len(out) == limit {
			continue
		}
		out = append(out, address)
	}
	if len(out) == 0 {
		b.Skip("no counterpart addresses in the prepared store")
	}
	return out
}

// BenchmarkLatestThreadForAddresses measures the split latest-thread read on
// one address and on two addresses (one received branch per address plus one
// sent branch).
func BenchmarkLatestThreadForAddresses(b *testing.B) {
	s := benchStore(b)
	addresses := benchCounterpartAddresses(b, s, 2)
	b.Run("one", func(b *testing.B) {
		b.ResetTimer()
		for range b.N {
			if _, _, err := s.LatestThreadForAddresses(b.Context(), addresses[:1]); err != nil {
				b.Fatal(err)
			}
		}
	})
	if len(addresses) < 2 {
		return
	}
	b.Run("two", func(b *testing.B) {
		b.ResetTimer()
		for range b.N {
			if _, _, err := s.LatestThreadForAddresses(b.Context(), addresses); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkLabelCounts measures the label aggregate on the prepared store.
// The fixture has no labels, which is the startup case: the read must be an
// empty labels scan, not a mailbox probe.
func BenchmarkLabelCounts(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		if _, err := s.LabelCounts(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReviewCounts measures the review-queue aggregate on the prepared
// store (zero labels, the startup case).
func BenchmarkReviewCounts(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for range b.N {
		if _, err := s.ReviewCounts(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
