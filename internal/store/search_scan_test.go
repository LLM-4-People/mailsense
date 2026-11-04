package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// scanCounter counts the statements a search issues: a short page needs the
// hit scan only, a full page also needs the capped count.
type scanCounter struct {
	queryer
	queries int
}

func (c *scanCounter) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.queryer.QueryContext(ctx, q, args...)
}

func (c *scanCounter) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.queries++
	return c.queryer.QueryRowContext(ctx, q, args...)
}

// packedMail is n messages that all match "budget", with distinct BM25 ranks
// (a different occurrence count and length) and instants, so both order keys
// are deterministic and a parity comparison is exact.
func packedMail(n int) []mail.Message {
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	out := make([]mail.Message, n)
	for i := range out {
		m := mailtest.Message("src", fmt.Sprintf("m%02d", i))
		m.Subject = "budget report"
		if i < 10 {
			m.Subject += " zebra"
		}
		m.Body = strings.Repeat("budget ", i%3+1) + strings.Repeat("detail ", i%7)
		m.ReceivedAt = base.Add(time.Duration(i) * time.Minute).Format(mail.TimeLayout)
		out[i] = m
	}
	return out
}

// searchTextTwoScan is the previous shape of searchText: a COUNT(*) over the
// match, then a second scan for the hits. It is the reference the one-scan
// implementation is compared against (results) and benchmarked with (work).
func (s *Store) searchTextTwoScan(ctx context.Context, qr queryer, text string, f SearchFilter, limit int, anyTerm bool) (SearchResult, error) {
	var res SearchResult
	p, err := planQuery(text, anyTerm)
	if err != nil {
		return res, err
	}
	res.Terms = p.terms
	snap := s.cfg.Snapshot()
	if maxHits := snap.Int("search.max_hits"); limit < 1 || limit > maxHits {
		limit = maxHits
	}
	where, args, err := s.where(ctx, &f)
	if err != nil {
		return res, err
	}
	from, all, rank := p.from(where, args)
	countCap := snap.Int("search.count_cap")
	if err := qr.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1"+from+" LIMIT ?)", append(all, countCap+1)...).Scan(&res.Total); err != nil {
		return res, err
	}
	if res.Total > countCap {
		res.Total, res.TotalCapped = countCap, true
	}
	res.Hits, err = query(ctx, qr, "SELECT m.id, m.source_id, m.store_id, m.msg_id, m.subject, m.received_at, "+rank+from+
		" ORDER BY 7, "+instantSQL+" DESC LIMIT ?", append(all, limit), func(r *sql.Rows, h *Hit) error {
		return r.Scan(&h.RowID, &h.SourceID, &h.StoreID, &h.ID, &h.Subject, &h.ReceivedAt, &h.Rank)
	})
	return res, err
}

func hitIDs(res SearchResult) []string {
	out := make([]string, len(res.Hits))
	for i, h := range res.Hits {
		out[i] = h.ID
	}
	return out
}

// The capped total keeps its contract: exact when the page holds the whole
// match (from the hit scan alone), the cap when there are more. The count
// scan runs only for a full page, and is bounded at cap+1 rows.
func TestSearchCountsFromTheHitScan(t *testing.T) {
	s, _ := open(t, map[string]any{"search.count_cap": 7, "search.max_hits": 50})
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, packedMail(12)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		limit     int
		cap       int
		wantTotal int
		capped    bool
		wantHits  int
		queries   int
	}{
		// A full page cannot answer the total by itself: the capped count runs
		// (and is bounded at cap+1 rows).
		{"full page, capped", 5, 7, 7, true, 5, 2},
		{"full page, exactly the cap", 7, 7, 7, true, 7, 1},
		{"full page, one hit", 1, 7, 7, true, 1, 2},
		// A page the match fits is the whole match: no count scan at all.
		{"short page, under the cap", 20, 20, 12, false, 12, 1},
		{"short page, total above the cap", 50, 7, 7, true, 12, 1},
		{"short page, limit above the cap", 20, 7, 7, true, 12, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.cfg.Set("search.count_cap", tc.cap); err != nil {
				t.Fatal(err)
			}
			q := &scanCounter{queryer: s.db}
			got, err := s.searchTextOn(ctx, q, "budget", SearchFilter{}, tc.limit, false)
			if err != nil {
				t.Fatal(err)
			}
			if q.queries != tc.queries {
				t.Fatalf("the search issued %d statements, want %d", q.queries, tc.queries)
			}
			if got.Total != tc.wantTotal || got.TotalCapped != tc.capped || len(got.Hits) != tc.wantHits {
				t.Fatalf("total %d capped %v hits %d, want %d/%v/%d",
					got.Total, got.TotalCapped, len(got.Hits), tc.wantTotal, tc.capped, tc.wantHits)
			}
			want, err := s.searchTextTwoScan(ctx, s.db, "budget", SearchFilter{}, tc.limit, false)
			if err != nil {
				t.Fatal(err)
			}
			if got.Total != want.Total || got.TotalCapped != want.TotalCapped ||
				strings.Join(hitIDs(got), ",") != strings.Join(hitIDs(want), ",") {
				t.Fatalf("one scan %d/%v %v, two scans %d/%v %v",
					got.Total, got.TotalCapped, hitIDs(got), want.Total, want.TotalCapped, hitIDs(want))
			}
			for i := range got.Hits {
				if got.Hits[i].Rank != want.Hits[i].Rank {
					t.Fatalf("hit %d rank %v, want %v", i, got.Hits[i].Rank, want.Hits[i].Rank)
				}
			}
		})
	}
}

// Below the cap the total is exact even when the page shows fewer rows.
func TestSearchTotalIsExactBelowTheCap(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, packedMail(3)); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchText(ctx, "budget", SearchFilter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || res.TotalCapped || len(res.Hits) != 2 {
		t.Fatalf("3 matches, page 2: total %d capped %v hits %d", res.Total, res.TotalCapped, len(res.Hits))
	}
}

// FilterIDsSince is the append a cached set gets after a sync: only the new
// mail, matching the filter, above the watermark.
func TestFilterIDsSinceListsOnlyNewMail(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, packedMail(3)); err != nil {
		t.Fatal(err)
	}
	all, err := s.FilterIDs(ctx, SearchFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("%v: %v", all, err)
	}
	watermark := all[len(all)-1]
	if ids, maxID, err := s.FilterIDsSince(ctx, SearchFilter{}, watermark); err != nil || len(ids) != 0 || maxID != watermark {
		t.Fatalf("no new mail: %v %d %v", ids, maxID, err)
	}
	more := mailtest.Message("src", "new")
	more.Subject, more.Body = "budget again", "budget budget"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{more}); err != nil {
		t.Fatal(err)
	}
	ids, maxID, err := s.FilterIDsSince(ctx, SearchFilter{}, watermark)
	if err != nil || len(ids) != 1 || maxID <= watermark || ids[0] != maxID {
		t.Fatalf("the new message: %v max %d (watermark %d) %v", ids, maxID, watermark, err)
	}
	// A filter the new message does not match returns an empty slice, never
	// nil: the semantic scan reads nil as "no filter".
	ids, _, err = s.FilterIDsSince(ctx, SearchFilter{Roles: []mail.FolderRole{mail.RoleSent}}, watermark)
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("empty delta: %#v %v", ids, err)
	}
}

// FilterSelects is the exact membership test over a candidate page: the
// owner boundary, the folder filters and a removal are all answered from
// the store's current state.
func TestFilterSelectsIsTheExactFilter(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	msgs := packedMail(3)
	msgs[1].FolderRole, msgs[1].FolderPath = mail.RoleSent, "Sent"
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	ids, _ := s.FilterIDs(ctx, SearchFilter{})
	if len(ids) != 3 {
		t.Fatalf("ids %v", ids)
	}
	ok, err := s.FilterSelects(ctx, SearchFilter{}, ids)
	if err != nil || len(ok) != 3 {
		t.Fatalf("all selected: %v %v", ok, err)
	}
	sent, _ := s.FilterIDs(ctx, SearchFilter{Roles: []mail.FolderRole{mail.RoleSent}})
	ok, err = s.FilterSelects(ctx, SearchFilter{Roles: []mail.FolderRole{mail.RoleSent}}, ids)
	if err != nil || len(ok) != 1 || !ok[sent[0]] {
		t.Fatalf("role filter: %v (sent %v) %v", ok, sent, err)
	}
	if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: ids[0], Verdict: scope.Shared, Reason: scope.ReasonNotAddressed}}); err != nil {
		t.Fatal(err)
	}
	ok, err = s.FilterSelects(ctx, SearchFilter{OwnerOnly: true}, ids)
	if err != nil || ok[ids[0]] {
		t.Fatalf("owner boundary: %v %v", ok, err)
	}
	if _, err := s.RemoveMessages(ctx, []int64{ids[2]}); err != nil {
		t.Fatal(err)
	}
	ok, err = s.FilterSelects(ctx, SearchFilter{}, ids)
	if err != nil || ok[ids[2]] || len(ok) != 2 {
		t.Fatalf("removal: %v %v", ok, err)
	}
}

// FilterStamp is the cache's change detector: a folder sync that only
// refreshes the item count and timestamp must not look like a change; a
// role, store kind, visibility or selection change must; and a verdict write
// must move the scope stamp past the previous build second.
func TestFilterStampFollowsVisibilityNotFolderNoise(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	msgs := packedMail(2)
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	first, err := s.FilterStamp(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.MaxID == 0 || first.Folders == "" || first.Verdicts != 0 || first.ScopedAt != "" {
		t.Fatalf("stamp %+v", first)
	}
	f := mail.FolderOf(&msgs[0])
	f.ItemCount = 99
	if _, err := s.UpsertFolder(ctx, f); err != nil {
		t.Fatal(err)
	}
	again, err := s.FilterStamp(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Folders != first.Folders {
		t.Fatalf("a folder item count changed the visibility stamp:\n%s\n%s", first.Folders, again.Folders)
	}
	if again.MaxID != first.MaxID {
		t.Fatalf("the row-id watermark moved without a write: %+v -> %+v", first, again)
	}
	f.Role = mail.RoleArchive
	if _, err := s.UpsertFolder(ctx, f); err != nil {
		t.Fatal(err)
	}
	changed, _ := s.FilterStamp(ctx, true)
	if changed.Folders == first.Folders {
		t.Fatal("a changed folder role left the visibility stamp alone")
	}
	ids, _ := s.FilterIDs(ctx, SearchFilter{})
	if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: ids[0], Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
		t.Fatal(err)
	}
	scoped, _ := s.FilterStamp(ctx, true)
	if scoped.Verdicts != 1 || scoped.ScopedAt == "" || scoped.ScopedAt < changed.Now {
		t.Fatalf("verdict write not visible in the stamp: %+v (previous now %q)", scoped, changed.Now)
	}
}

// TestMessageFieldsMatchesTheFilterSQL pins the one classification the
// filter cache reads: a filter whose rendered predicate reads the message
// row (or its further folders) cannot be extended with new mail; one that
// reads only folder rows and scope verdicts can.
func TestMessageFieldsMatchesTheFilterSQL(t *testing.T) {
	s, _ := open(t, nil)
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	yes := true
	filters := map[string]SearchFilter{
		"all mail":        {},
		"owner only":      {OwnerOnly: true},
		"stores":          {Stores: []string{"src/store-1"}},
		"since":           {Since: when},
		"until":           {Until: when},
		"roles":           {Roles: []mail.FolderRole{mail.RoleInbox}},
		"sender":          {Sender: "elodie@example.org"},
		"has attachments": {HasAttachments: &yes},
		"owner and date":  {OwnerOnly: true, Since: when},
		"store and roles": {Stores: []string{"src/store-1"}, Roles: []mail.FolderRole{mail.RoleInbox}},
	}
	messageRow := regexp.MustCompile(`\bm\.`)
	for name, f := range filters {
		where, _, err := s.where(t.Context(), &f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if reads := messageRow.MatchString(where); reads != f.MessageFields() {
			t.Errorf("%s: MessageFields %v, predicate reads the message row %v:\n%s", name, f.MessageFields(), reads, where)
		}
	}
}

// The previous shape ran the capped count before every hit scan, even when
// the page held the whole match. This benchmark keeps the full list and the
// new-mail append side by side, and the two search shapes below.
func benchPacked(b *testing.B, n int) *Store {
	b.Helper()
	cfg := configtest.New(b, map[string]any{
		"search.count_cap": 500, "search.max_hits": 200,
		source.SettingSelected: []any{source.StoreKey("src", "store-1")},
	})
	s, err := Open(b.Context(), cfg, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertSource(b.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		b.Fatal(err)
	}
	if err := s.UpsertMessages(b.Context(), 0, packedMail(n)); err != nil {
		b.Fatal(err)
	}
	return s
}

func BenchmarkSearchTextPageCount(b *testing.B) {
	s := benchPacked(b, 2000)
	b.ResetTimer()
	for range b.N {
		if _, err := s.SearchText(b.Context(), "budget", SearchFilter{}, 20); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchTextCountAlways(b *testing.B) {
	s := benchPacked(b, 2000)
	b.ResetTimer()
	for range b.N {
		if _, err := s.searchTextTwoScan(b.Context(), s.db, "budget", SearchFilter{}, 20, false); err != nil {
			b.Fatal(err)
		}
	}
}

// The filter-set rebuild (a full mailbox list) against the append after a
// sync (one row-id range read).
func BenchmarkFilterIDsAll(b *testing.B) {
	s := benchPacked(b, 2000)
	b.ResetTimer()
	for range b.N {
		if ids, err := s.FilterIDs(b.Context(), SearchFilter{}); err != nil || len(ids) == 0 {
			b.Fatal(len(ids), err)
		}
	}
}

func BenchmarkFilterIDsSince(b *testing.B) {
	s := benchPacked(b, 2000)
	ids, err := s.FilterIDs(b.Context(), SearchFilter{})
	if err != nil || len(ids) == 0 {
		b.Fatal(len(ids), err)
	}
	after := ids[len(ids)-10]
	b.ResetTimer()
	for range b.N {
		if _, _, err := s.FilterIDsSince(b.Context(), SearchFilter{}, after); err != nil {
			b.Fatal(err)
		}
	}
}

// A page the match fits is answered by the hit scan alone: the count that
// the previous shape always ran is gone.
func BenchmarkSearchTextShortPage(b *testing.B) {
	s := benchPacked(b, 2000)
	b.ResetTimer()
	for range b.N {
		if _, err := s.SearchText(b.Context(), "zebra", SearchFilter{}, 20); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchTextShortPageCountAlways(b *testing.B) {
	s := benchPacked(b, 2000)
	b.ResetTimer()
	for range b.N {
		if _, err := s.searchTextTwoScan(b.Context(), s.db, "zebra", SearchFilter{}, 20, false); err != nil {
			b.Fatal(err)
		}
	}
}
