package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// seedMsgs writes messages and returns their row ids by message id.
func seedMsgs(t *testing.T, s *Store, msgs ...mail.Message) map[string]int64 {
	t.Helper()
	if err := s.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, m := range msgs {
		_, id, err := loadMessage(t.Context(), s.db, m.SourceID, m.StoreID, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		out[m.ID] = id
	}
	return out
}

func msgAt(id string, role mail.FolderRole, received string) mail.Message {
	m := mailtest.Message("src", id)
	m.FolderRole, m.FolderPath, m.ReceivedAt, m.SentOn = role, string(role), received, ""
	return m
}

func TestInstantSQLMatchesMailInstant(t *testing.T) {
	s, _ := open(t, nil)
	cases := []mail.Message{msgAt("in", mail.RoleInbox, "2026-01-02T00:00:00Z"), msgAt("sent", mail.RoleSent, "2026-01-03T00:00:00Z"),
		msgAt("sent-nosenton", mail.RoleSent, "2026-01-04T00:00:00Z"), msgAt("undated", mail.RoleInbox, "")}
	cases[1].SentOn = "2026-01-01T00:00:00Z"
	ids := seedMsgs(t, s, cases...)
	sums, err := s.Summaries(t.Context(), slices.Collect(func(yield func(int64) bool) {
		for _, id := range ids {
			yield(id)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cases {
		want, ok := m.Instant()
		got := sums[ids[m.ID]].Instant
		if (got == "") == ok || (ok && got != want.UTC().Format(mail.TimeLayout)) {
			t.Errorf("%s: SQL instant %q, mail.Instant %v %v", m.ID, got, want, ok)
		}
	}
}

func TestEmbeddingsAreReplacedIncrementallyAndFollowTheirMessage(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z"), msgAt("b", mail.RoleInbox, "2026-01-03T00:00:00Z"),
		msgAt("c", mail.RoleDeleted, "2026-01-02T00:00:00Z"))
	vec := vector.Quantize([]float32{0.6, 0.8})
	rows := []EmbeddingRow{{MessageID: ids["a"], Space: "sp", TextHash: "h1", Revision: 0, Vector: vec},
		{MessageID: ids["b"], Space: "sp", TextHash: "h2", Revision: 0, Vector: vec}, {MessageID: 99999, Space: "sp", Vector: vec}}
	if err := s.SaveEmbeddings(ctx, rows); err != nil {
		t.Fatal(err)
	}
	all, _ := s.LoadVectors(ctx, "sp", 0)
	if len(all) != 2 || all[0].Seq >= all[1].Seq || vector.Cosine(all[0].Vector.Floats(), []float32{0.6, 0.8}) < 0.999 {
		t.Fatalf("two vectors in write order (the removed message's is skipped): %+v", all)
	}
	if err := s.SaveEmbeddings(ctx, rows[:1]); err != nil {
		t.Fatal(err)
	}
	newer, _ := s.LoadVectors(ctx, "sp", all[1].Seq)
	if n, _ := s.CountVectors(ctx, "sp"); len(newer) != 1 || newer[0].MessageID != ids["a"] || n != 2 {
		t.Fatalf("a replaced vector is the only newer row and replaces the old one: %+v (count %d)", newer, n)
	}
	if err := s.ConfirmEmbeddings(ctx, map[int64]int64{ids["b"]: 7}, "v1"); err != nil {
		t.Fatal(err)
	}
	states, err := s.EmbeddingStates(ctx, []mail.FolderRole{mail.RoleInbox})
	if err != nil || len(states) != 2 || states[0].RowID != ids["b"] || states[0].Checked != 7 || states[0].TextHash != "h2" || states[1].Space != "sp" {
		t.Fatalf("inbox only, newest first, with the confirmed marker: %+v %v", states, err)
	}
	if after, _ := s.LoadVectors(ctx, "sp", newer[0].Seq); len(after) != 0 {
		t.Fatal("confirming does not rewrite the vector")
	}
	if _, err := s.RemoveMessages(ctx, []int64{ids["a"]}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountVectors(ctx, "sp"); n != 1 {
		t.Fatalf("a removed message takes its vector along: %d", n)
	}
}

func TestSearchSenderRuleIsUnicodeAndOneOwner(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	accent := msgAt("accent", mail.RoleInbox, "2026-03-01T10:00:00Z")
	accent.SenderAddress, accent.SentRepresentingAddress = "élodie@exemple.test", "élodie@exemple.test"
	plain := msgAt("plain", mail.RoleInbox, "2026-03-02T10:00:00Z")
	plain.SenderAddress, plain.SentRepresentingAddress = "elodie@exemple.test", "elodie@exemple.test"
	tag := msgAt("tag", mail.RoleSent, "2026-03-03T10:00:00Z")
	tag.SenderAddress, tag.SentRepresentingAddress = "elodie+tag@exemple.test", "elodie+tag@exemple.test"
	ids := seedMsgs(t, s, accent, plain, tag)
	for _, id := range ids {
		if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		sender string
		want   []string
	}{
		// SQLite's lower() is ASCII-only: the shared rule must use the Go
		// normalization so a non-ASCII local part matches its own case.
		{"unicode exact", "ÉLODIE@EXEMPLE.TEST", []string{"accent"}},
		{"ascii exact", "elodie@exemple.test", []string{"plain"}},
		{"domain", "@exemple.test", []string{"accent", "plain", "tag"}},
		{"plus tag", "elodie+tag@exemple.test", []string{"tag"}},
		{"no match", "nobody@exemple.test", nil},
	} {
		got, err := s.FilterIDs(ctx, SearchFilter{Sender: tc.sender})
		var want []int64
		for _, id := range tc.want {
			want = append(want, ids[id])
		}
		slices.Sort(got)
		slices.Sort(want)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: FilterIDs %v, want %v (%v)", tc.name, got, want, err)
		}
	}
}

func TestSearchFiltersApplyToBothRankings(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	in := msgAt("in", mail.RoleInbox, "2026-03-01T10:00:00Z")
	sent := msgAt("sent", mail.RoleSent, "2026-02-01T10:00:00Z")
	sent.SentOn, sent.SenderAddress, sent.HasAttachments = "2026-02-01T10:00:00Z", "me@example.com", false
	other := msgAt("other", mail.RoleInbox, "2026-04-01T10:00:00Z")
	other.SenderAddress, other.StoreID = "x_y%@weird.org", "store-2"
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared", StoreKind: mail.StoreDelegate,
		Path: "inbox", Role: mail.RoleInbox, ItemCount: -1}); err != nil {
		t.Fatal(err)
	}
	ids := seedMsgs(t, s, in, sent, other)
	for _, id := range ids {
		if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
	}
	no := false
	cases := map[string]struct {
		f    SearchFilter
		want []string
	}{
		"all":            {SearchFilter{}, []string{"in", "sent", "other"}},
		"since":          {SearchFilter{Since: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}, []string{"in", "other"}},
		"until":          {SearchFilter{Until: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}, []string{"sent"}},
		"role":           {SearchFilter{Roles: []mail.FolderRole{mail.RoleSent}}, []string{"sent"}},
		"one store":      {SearchFilter{Stores: []string{source.StoreKey("src", "store-1")}}, []string{"in", "sent"}},
		"unknown store":  {SearchFilter{Stores: []string{"store-000000000000"}}, nil},
		"sender exact":   {SearchFilter{Sender: " ME@example.com "}, []string{"sent"}},
		"sender domain":  {SearchFilter{Sender: "@example.org"}, []string{"in"}},
		"bare domain":    {SearchFilter{Sender: "weird.org"}, []string{"other"}},
		"like is quoted": {SearchFilter{Sender: "%"}, nil},
		"no attachments": {SearchFilter{HasAttachments: &no}, []string{"sent"}},
		// A delegate store may mark its own mailbox owner's mail
		// verdict=owner; owner-only search is a content boundary, not a
		// verdict-only filter.
		"owner only": {SearchFilter{OwnerOnly: true}, []string{"in", "sent"}},
	}
	for name, c := range cases {
		got, err := s.FilterIDs(ctx, c.f)
		var want []int64
		for _, id := range c.want {
			want = append(want, ids[id])
		}
		slices.Sort(got)
		slices.Sort(want)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: FilterIDs %v, want %v (%v)", name, got, want, err)
		}
		res, err := s.SearchText(ctx, "budget", c.f, 0)
		if err != nil || res.Total != len(c.want) || len(res.Hits) != len(c.want) {
			t.Errorf("%s: SearchText %d hits, want %d (%v)", name, res.Total, len(c.want), err)
		}
		if c.f.IsZero() != (name == "all") {
			t.Errorf("%s: IsZero", name)
		}
	}
	if _, err := s.SearchText(ctx, `"" `, SearchFilter{}, 0); err == nil {
		t.Fatal("an empty query is refused")
	}
	sums, _ := s.Summaries(ctx, []int64{ids["sent"], 424242})
	if len(sums) != 1 || sums[ids["sent"]].Role != mail.RoleSent || sums[ids["sent"]].Body == "" {
		t.Fatalf("summaries skip unknown ids: %+v", sums)
	}
	ownerSums, err := s.SummariesOwner(ctx, []int64{ids["in"], ids["other"]})
	if err != nil || len(ownerSums) != 1 {
		t.Fatalf("owner summaries %+v (%v)", ownerSums, err)
	}
	if _, ok := ownerSums[ids["other"]]; ok {
		t.Fatal("a delegate-store owner verdict leaked its summary")
	}
}

// Review 2b #1: write sequence numbers are never reused, also after the
// highest one's message is removed.
func TestOwnerSearchExcludesDraftsAndJunk(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	in := msgAt("in", mail.RoleInbox, "2026-03-01T10:00:00Z")
	draft := msgAt("draft", mail.RoleDrafts, "2026-03-02T10:00:00Z")
	junk := msgAt("junk", mail.RoleJunk, "2026-03-03T10:00:00Z")
	ids := seedMsgs(t, s, in, draft, junk)
	for _, id := range ids {
		if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonSentByOwner}}); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := s.FilterIDs(ctx, SearchFilter{OwnerOnly: true})
	if err != nil || len(owner) != 1 || owner[0] != ids["in"] {
		t.Fatalf("owner ids %v (%v), want only the inbox message", owner, err)
	}
	res, err := s.SearchText(ctx, "budget", SearchFilter{OwnerOnly: true}, 0)
	if err != nil || res.Total != 1 || len(res.Hits) != 1 || res.Hits[0].RowID != ids["in"] {
		t.Fatalf("owner search %+v (%v)", res, err)
	}
	sums, err := s.SummariesOwner(ctx, []int64{ids["in"], ids["draft"], ids["junk"]})
	if err != nil || len(sums) != 1 {
		t.Fatalf("owner summaries %+v (%v)", sums, err)
	}
	if _, ok := sums[ids["draft"]]; ok {
		t.Fatal("a draft leaked a summary")
	}
	if _, ok := sums[ids["junk"]]; ok {
		t.Fatal("junk leaked a summary")
	}
}

func TestEmbeddingSeqIsNeverReused(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z"), msgAt("b", mail.RoleInbox, "2026-01-02T00:00:00Z"))
	vec := vector.Quantize([]float32{1, 0})
	_ = s.SaveEmbeddings(ctx, []EmbeddingRow{{MessageID: ids["a"], Space: "sp", Vector: vec}, {MessageID: ids["b"], Space: "sp", Vector: vec}})
	all, _ := s.LoadVectors(ctx, "sp", 0)
	if _, err := s.RemoveMessages(ctx, []int64{ids["b"]}); err != nil {
		t.Fatal(err)
	}
	ids2 := seedMsgs(t, s, msgAt("c", mail.RoleInbox, "2026-01-03T00:00:00Z"))
	_ = s.SaveEmbeddings(ctx, []EmbeddingRow{{MessageID: ids2["c"], Space: "sp", Vector: vec}})
	if after, _ := s.LoadVectors(ctx, "sp", all[1].Seq); len(after) != 1 || after[0].MessageID != ids2["c"] {
		t.Fatalf("the new vector comes after the removed one's number: %+v (removed seq %d)", after, all[1].Seq)
	}
}

// The semantic coverage target counts only mail the indexer can vectorize;
// the UI's Coverage still counts every visible message honestly.
func TestIndexableIDsExcludePartialAndQuarantined(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	a := msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z")
	b := msgAt("b", mail.RoleInbox, "2026-01-02T00:00:00Z")
	b.Partial = true
	c := msgAt("c", mail.RoleInbox, "2026-01-03T00:00:00Z")
	ids := seedMsgs(t, s, a, b, c)

	states, err := s.EmbeddingStates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, st := range states {
		if st.RowID == ids["c"] {
			revision = st.Revision
		}
	}
	if err := s.RecordIndexFailures(ctx, []IndexFailure{{MessageID: ids["c"], Revision: revision, Error: "boom"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.IndexableIDs(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != ids["a"] {
		t.Fatalf("indexable ids %v, want only the complete message %d", got, ids["a"])
	}
	// Coverage keeps the partial and quarantined rows: they are visible and
	// their state is reported, they are just not vectorizable.
	all, err := s.EmbeddingStates(ctx, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("coverage states %d: %v", len(all), err)
	}
}

func TestIndexFailuresCountPerRevision(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z"))
	f := IndexFailure{MessageID: ids["a"], Revision: 0, Error: "boom"}
	_ = s.RecordIndexFailures(ctx, []IndexFailure{f, f, {MessageID: 424242}})
	if st, _ := s.EmbeddingStates(ctx, nil); st[0].Failures != 2 {
		t.Fatalf("two failures at revision 0: %+v", st)
	}
	seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z")) // revision 1
	if st, _ := s.EmbeddingStates(ctx, nil); st[0].Failures != 0 {
		t.Fatalf("a new revision is not quarantined: %+v", st)
	}
	_ = s.RecordIndexFailures(ctx, []IndexFailure{{MessageID: ids["a"], Revision: 1, Error: "x"}})
	_ = s.SaveEmbeddings(ctx, []EmbeddingRow{{MessageID: ids["a"], Space: "sp", Revision: 1, Vector: vector.Quantize([]float32{1})}})
	if st, _ := s.EmbeddingStates(ctx, nil); st[0].Failures != 0 {
		t.Fatalf("a stored vector clears the failures: %+v", st)
	}
}

// indexRunReport is the report of a seeded run; it carries the run id so a
// test can prove the kept rows are the newest ones and were not rewritten.
func indexRunReport(id int64) string {
	return fmt.Sprintf(`{"run_id":%d,"state":"failed","stopped_by":"unavailable","error":"boom"}`, id)
}

// seedIndexRuns inserts n finished failed runs, oldest first, and returns
// their ids.
func seedIndexRuns(t *testing.T, s *Store, n int) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := range n {
		id, err := s.BeginIndexRun(t.Context(), "embed/test/8")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishIndexRun(t.Context(), id, runstate.Failed, indexRunReport(id), "boom"); err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	return ids
}

// Round 26: index_runs had no retention, so a stuck embedder wrote a failed
// row every reconcile forever. PruneIndexRuns keeps the newest keep rows
// (clamped to at least one) and leaves their reports and errors intact.
func TestPruneIndexRunsKeepsNewest(t *testing.T) {
	cases := []struct {
		name string
		seed int
		keep int
		want int
	}{
		{"keep 0 clamps to 1", 10, 0, 1},
		{"keep 1", 10, 1, 1},
		{"keep 2", 10, 2, 2},
		{"keep 5", 10, 5, 5},
		{"keep 10 keeps every row", 10, 10, 10},
		{"under the cap is a no-op", 3, 10, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := open(t, nil)
			ids := seedIndexRuns(t, s, c.seed)
			pruned, err := s.PruneIndexRuns(t.Context(), c.keep)
			if err != nil {
				t.Fatal(err)
			}
			if want := int64(c.seed - c.want); pruned != want {
				t.Fatalf("keep %d: pruned %d rows, want %d", c.keep, pruned, want)
			}
			runs, err := s.ListIndexRuns(t.Context(), 1000)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != c.want {
				t.Fatalf("keep %d: %d rows left, want %d", c.keep, len(runs), c.want)
			}
			for i, run := range runs {
				wantID := ids[len(ids)-1-i]
				if run.ID != wantID {
					t.Fatalf("keep %d: row %d is %d, want the newest-first id %d", c.keep, i, run.ID, wantID)
				}
				if want := indexRunReport(run.ID); run.Report != want || run.Error != "boom" ||
					run.State != runstate.Failed || run.Space != "embed/test/8" {
					t.Fatalf("keep %d: row %d was not left intact: %+v", c.keep, run.ID, run)
				}
			}
		})
	}
	s, _ := open(t, nil)
	ids := seedIndexRuns(t, s, 2)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.PruneIndexRuns(ctx, 1); err == nil {
		t.Fatal("a cancelled context must not prune")
	}
	runs, err := s.ListIndexRuns(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != ids[1] || runs[1].ID != ids[0] {
		t.Fatalf("cancelled prune left %d rows, want the 2 seeded rows %d and %d", len(runs), ids[1], ids[0])
	}
	t.Logf("cancelled prune left both seeded rows (ids %d, %d)", runs[0].ID, runs[1].ID)
}

// syntheticFailedReport is a valid failed-run report of exactly 1,904 bytes,
// matching the report length of the prepared audit store's failed rows.
func syntheticFailedReport(id int64) string {
	base := fmt.Sprintf(`{"run_id":%d,"state":"failed","space":"embed/test/8","text_version":"0123456789abcdef",`+
		`"before":{"eligible":33842,"indexed":33840,"pending":2},"after":{"eligible":33842,"indexed":33840,"pending":2},`+
		`"selected":2,"embedded":0,"unchanged":0,"failed":2,"stopped_by":"unavailable",`+
		`"failures":[{"messages":[%d],"error":"the embedder refused the batch"}],"padding":""}`, id, id)
	return strings.Replace(base, `"padding":""`, fmt.Sprintf(`"padding":"%s"`, strings.Repeat("x", 1904-len(base))), 1)
}

// syntheticFailedError is the 299-byte error of that failed run.
func syntheticFailedError() string {
	const msg = "index: stopped, the embedder is unavailable: HTTP 401 Unauthorized from the embeddings runtime"
	return msg + strings.Repeat(" x", (299-len(msg))/2) + strings.Repeat("x", (299-len(msg))%2)
}

func indexRunPayload(t *testing.T, s *Store) (rows, bytes int64) {
	t.Helper()
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(length(started_at)+length(finished_at)+length(status)+length(space)+length(report)+length(error)), 0) FROM index_runs`).Scan(&rows, &bytes)
	if err != nil {
		t.Fatal(err)
	}
	return rows, bytes
}

// Round 26: measure the column payload of a synthetic failed-run history
// before and after retention. This is synthetic SQLite data, not a real
// mailbox: 105 finished runs with a 1,904 B report and a 299 B error
// (2,261 B of columns per row), kept at 100.
func TestPruneIndexRunsFreesThePayloadOfOldRuns(t *testing.T) {
	s, _ := open(t, nil)
	const rows, keep = 105, 100
	ids := make([]int64, rows)
	for i := range rows {
		id, err := s.BeginIndexRun(t.Context(), "embed/test/8")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishIndexRun(t.Context(), id, runstate.Failed, syntheticFailedReport(id), syntheticFailedError()); err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	beforeRows, beforeBytes := indexRunPayload(t, s)
	pruned, err := s.PruneIndexRuns(t.Context(), keep)
	if err != nil {
		t.Fatal(err)
	}
	afterRows, afterBytes := indexRunPayload(t, s)
	t.Logf("synthetic index_runs payload: %d rows / %d B -> %d rows / %d B, pruned %d", beforeRows, beforeBytes, afterRows, afterBytes, pruned)
	if beforeRows != rows || afterRows != keep || pruned != rows-keep {
		t.Fatalf("rows %d -> %d, pruned %d; want %d -> %d, pruned %d", beforeRows, afterRows, pruned, rows, keep, rows-keep)
	}
	if afterBytes >= beforeBytes {
		t.Fatalf("retention freed no payload: %d -> %d bytes", beforeBytes, afterBytes)
	}
	if perRow := (beforeBytes - afterBytes) / pruned; perRow < 2200 || perRow > 2400 {
		t.Fatalf("pruned %.1f B/row, want the synthetic failed-run shape near 2,261 B", float64(perRow))
	}
	kept, err := s.ListIndexRuns(t.Context(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, run := range kept {
		if want := ids[len(ids)-1-i]; run.ID != want {
			t.Fatalf("kept row %d is %d, want the newest-first id %d", i, run.ID, want)
		}
	}
}

// TestEmbeddingStatesUndatedSortsLast pins the empty instant's sort position:
// instant is NOT NULL DEFAULT ”, and ” sorts after every dated value under
// DESC, so the dropped leading term cannot change the order.
func TestEmbeddingStatesUndatedSortsLast(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	old := msgAt("old", mail.RoleInbox, "2026-01-01T00:00:00Z")
	undated := msgAt("undated", mail.RoleInbox, "")
	newest := msgAt("newest", mail.RoleInbox, "2026-12-01T00:00:00Z")
	equalFirst := msgAt("equal-first", mail.RoleInbox, "2026-06-01T00:00:00Z")
	equalSecond := msgAt("equal-second", mail.RoleInbox, "2026-06-01T00:00:00Z")
	ids := seedMsgs(t, s, old, undated, newest, equalFirst, equalSecond)
	states, err := s.EmbeddingStates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{ids["newest"], ids["equal-second"], ids["equal-first"], ids["old"], ids["undated"]}
	got := make([]int64, 0, len(states))
	for _, st := range states {
		got = append(got, st.RowID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("state order %v, want %v (dated newest first, undated last)", got, want)
	}
}

// TestEmbeddingStatesPlanUsesTheInstantIndex pins the read to the mailbox
// instant index: no whole-mailbox temporary sort behind every learning
// status refresh.
func TestEmbeddingStatesPlanUsesTheInstantIndex(t *testing.T) {
	s, _ := open(t, nil)
	seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z"),
		msgAt("b", mail.RoleInbox, ""), msgAt("c", mail.RoleInbox, "2026-02-01T00:00:00Z"))
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan := priorityQueryPlan(t, s, embeddingStatesQuery(vis))
	assertIndexOrderedPlan(t, "embedding states", plan, "messages_instant")
	var roles []mail.FolderRole
	for _, role := range s.cfg.Strings("index.folder_roles") {
		roles = append(roles, mail.FolderRole(role))
	}
	cond, args := rolesSQL(roles)
	rolePlan := priorityQueryPlan(t, s, embeddingStatesQuery(vis+" AND "+cond), args...)
	assertIndexOrderedPlan(t, "embedding states with roles", rolePlan, "messages_instant")
}
