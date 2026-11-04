package priority

// The packed benchmark measures one complete build over a mailbox that
// overflows the cache: 20k owner rows across 4k conversations. It reports
// allocations as well as the bytes the installed snapshot retains after the
// build, so a change to the scan's retention shows up as a delta instead of
// an opinion. Run with:
//
//	go test ./internal/priority -run '^$' -bench BenchmarkBuildRankingPacked -benchmem

import (
	"fmt"
	"math"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

const (
	packedRows  = 20000
	packedConvs = 4000
)

// packedMailbox builds one deterministic mailbox shared by the benchmark and
// the retention test. Every third row is bulk, every fifth is a reply, and the
// rest mix the remaining signals so the build exercises the whole scoring
// path rather than one hot branch.
func packedMailbox(now time.Time) ([]store.PriorityRow, map[string]threadintel.Assessment) {
	rows := make([]store.PriorityRow, packedRows)
	states := make(map[string]threadintel.Assessment, packedConvs)
	for i := range rows {
		conv := fmt.Sprintf("C-%04d", i%packedConvs)
		answers := map[string]route.Answer{
			"reply_needed":    noul("reply_needed", 0.2),
			"is_question":     noul("is_question", 0.2),
			"decision_needed": noul("decision_needed", 0.2),
			"suspicious":      noul("suspicious", 0.2),
			"urgency":         score("urgency", float64(i%5)),
			"deadline":        score("deadline", float64(i%4)),
			"effort":          score("effort", float64(i%3)),
			"request_count":   score("request_count", float64(i%4)),
			"topic":           choiceAns("topic", []string{"project_work", "billing_account", "support_issue"}[i%3]),
		}
		if i%5 == 0 {
			answers["reply_needed"] = noul("reply_needed", 0.9)
		}
		if i%7 == 0 {
			answers["decision_needed"] = noul("decision_needed", 0.9)
		}
		if i%3 == 0 {
			answers["category"] = choiceAns("category", "marketing")
		} else if i%4 == 0 {
			answers["category"] = choiceAns("category", "work")
		}
		if i%11 == 0 {
			answers["asks_for"] = choiceAns("asks_for", "payment_money")
		}
		rows[i] = store.PriorityRow{
			MessageID: int64(i + 1), Revision: 1, ConversationID: conv,
			Subject: fmt.Sprintf("Subject %d", i+1), SenderName: fmt.Sprintf("Sender %d", i%17),
			SenderAddress: fmt.Sprintf("sender%d@example.com", i%17),
			ReceivedAt:    now.Add(-time.Duration(i) * time.Minute).Format(mail.TimeLayout),
			FolderRole:    mail.RoleInbox, Body: fmt.Sprintf("Body for %d", i+1), Route: answers,
		}
	}
	for i := 0; i < packedConvs; i++ {
		id := fmt.Sprintf("C-%04d", i)
		states[id] = threadintel.Assessment{ConversationID: id, State: threadintel.AwaitingOwner,
			LastMessageID: int64(i + 1), ReplyToMessageID: int64(i + 1), Messages: 3}
	}
	return rows, states
}

func packedService(tb testing.TB, rows []store.PriorityRow, states map[string]threadintel.Assessment, now time.Time) *Service {
	tb.Helper()
	cfg := configtest.New(tb, map[string]any{"learning.test_mode": false})
	svc, err := New(cfg, Deps{Store: &fakeStore{rows: rows}, Gate: allowGate{}, Config: cfg,
		Clock: &fakeClock{now: now}, Threads: &fakeThreads{states: states}, RouteKey: testRouteKey})
	if err != nil {
		tb.Fatal(err)
	}
	return svc
}

// liveHeap reports the retained heap. Two collections are needed for the
// number to settle: the first can still be sweeping spans the scan just
// dropped, which under-reports the peak a single collection would see.
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func BenchmarkBuildRankingPacked(b *testing.B) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	rows, states := packedMailbox(now)
	svc := packedService(b, rows, states, now)
	base := liveHeap()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		c, err := svc.buildRanking(b.Context(), false, now, "priority-test-route")
		if err != nil {
			b.Fatal(err)
		}
		if len(c.items) != MaxCached || len(c.states) != packedConvs {
			b.Fatalf("ranked %d items and %d states", len(c.items), len(c.states))
		}
		heap := liveHeap()
		if heap > base {
			b.ReportMetric(float64(heap-base)/float64(len(rows)), "retained-B/row")
		}
		runtime.KeepAlive(c)
	}
}

// TestScanRetentionIsOneRecordPerOwnerRow is the memory contract of the one
// scan. Per owner message the build retains exactly one rankedRow with its
// decoded evidence; the resolver's directed view (threadSignalsFor) exists
// only for the duration of its one call, and the installed snapshot holds no
// per-message state at all. The full conversation-state map is deliberately
// kept: agenda enumerates every entry to build "waiting on you" obligations,
// so it cannot be bounded by the cached top.
//
// The budget is loose on purpose. When this test was written the shape
// measured ~427 B/row; the pre-refactor shape (a rows slice plus a
// signalsByID map plus a threadSignals map) measured ~804 B/row. A cap of
// 600 B/row therefore fails the duplicate retention without depending on
// allocator noise, while leaving room for a runtime or field change.
func TestScanRetentionIsOneRecordPerOwnerRow(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	rows, states := packedMailbox(now)
	svc := packedService(t, rows, states, now)
	profile, err := taxonomy.Load(svc.cfg)
	if err != nil {
		t.Fatal(err)
	}
	retentionKeep = append(retentionKeep, rows, states, svc, profile)

	const budget = 600.0 // bytes per owner row retained by the scan
	best := math.MaxFloat64
	for range 3 {
		base := liveHeap()
		scanned, convs, err := svc.replySignals(t.Context(), profile, "priority-test-route", false)
		if err != nil {
			t.Fatal(err)
		}
		after := liveHeap()
		retentionKeep = append(retentionKeep, scanned, convs)
		if after < base {
			t.Fatalf("the scan freed heap instead of retaining it (%d -> %d)", base, after)
		}
		retained := float64(after-base) / float64(scanned.len())
		if scanned.len() != packedRows || len(convs) != packedConvs {
			t.Fatalf("scanned %d rows across %d conversations, want %d/%d",
				scanned.len(), len(convs), packedRows, packedConvs)
		}
		best = min(best, retained)
	}
	t.Logf("the scan retains %.1f B/row of owner mail (budget %.0f, pre-refactor shape measured 804)", best, budget)
	if best > budget {
		t.Fatalf("the scan retains %.1f B/row, over the %.0f B/row budget: a per-message duplicate came back", best, budget)
	}
}

// TestCachedRankingHoldsNoPerMessageState guards the snapshot itself: it may
// hold the top items, the per-conversation states and the topic census, but no
// mailbox-sized per-message map or row slice.
func TestCachedRankingHoldsNoPerMessageState(t *testing.T) {
	ranking := reflect.TypeOf(cachedRanking{})
	ranked := reflect.TypeOf(rankedRow{})
	rowBufferType := reflect.TypeOf(rowBuffer{})
	for i := 0; i < ranking.NumField(); i++ {
		field := ranking.Field(i)
		switch field.Type.Kind() {
		case reflect.Map:
			if field.Type.Key().Kind() == reflect.Int64 {
				t.Errorf("cachedRanking.%s retains a per-message map (%s)", field.Name, field.Type)
			}
		case reflect.Slice:
			if field.Type.Elem() == ranked {
				t.Errorf("cachedRanking.%s retains the scan rows (%s)", field.Name, field.Type)
			}
		case reflect.Pointer:
			if field.Type.Elem() == rowBufferType {
				t.Errorf("cachedRanking.%s retains the scan buffer", field.Name)
			}
		}
	}
}

// retentionKeep keeps the measured fixtures and results alive across the
// collections liveHeap performs, so each measurement sees only its own delta.
var retentionKeep []any
