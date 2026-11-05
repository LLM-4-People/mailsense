package world

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// trendNow is the fixed anchor of the trend tests: its local month (Europe/
// Paris) is 2026-10, so the buckets end with the partial anchor month and the
// complete months before it run 2025-11..2026-09.
var trendNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func trendPtr(days int) *int { return &days }

// trendBuckets builds n consecutive zero-filled month buckets from start (a
// "2006-01" key) and replaces custom ones. Month is always the key: a bucket
// exists whether or not the organization sent mail in it, exactly as the
// store zero-fills its own month axis.
func trendBuckets(t *testing.T, start string, n int, custom map[string]CompanyMonth) []CompanyMonth {
	t.Helper()
	first, err := time.Parse("2006-01", start)
	if err != nil {
		t.Fatalf("bucket start %q: %v", start, err)
	}
	months := make([]CompanyMonth, n)
	for i := range months {
		months[i] = CompanyMonth{Month: first.AddDate(0, i, 0).Format("2006-01")}
	}
	for key, month := range custom {
		found := false
		for i := range months {
			if months[i].Month == key {
				months[i] = month
				months[i].Month = key
				found = true
			}
		}
		if !found {
			t.Fatalf("custom month %q is not in the bucket range", key)
		}
	}
	return months
}

// trendView builds a view whose all-time totals are the bucket sums, so the
// trend's mail guard sees exactly the mail the windows see.
func trendView(t *testing.T, months []CompanyMonth, lastIn, lastOut string) ProfileView {
	t.Helper()
	view := ProfileView{Months: months, LastInboundAt: lastIn, LastOutboundAt: lastOut}
	for _, month := range months {
		view.Messages += month.Messages
		view.Sent += month.Sent
		view.Received += month.Received
	}
	return view
}

// TestCompanyTrendGolden pins the trend's one semantics: equal windows of
// complete months, the partial anchor month excluded, zero months counted as
// inactive months inside their window, and a status that stays
// "insufficient_history" until two-way exchange, six active months and twelve
// baseline messages all hold.
func TestCompanyTrendGolden(t *testing.T) {
	day := func(messages, sent, received int) CompanyMonth {
		return CompanyMonth{Messages: messages, Sent: sent, Received: received}
	}
	cases := []struct {
		name string
		view ProfileView
		want *ProfileTrend
	}{
		{
			name: "ok compares equal five-month windows and ignores the anchor month",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(3, 1, 2),
				"2026-01": day(3, 2, 1),
				"2026-02": day(3, 1, 2),
				"2026-03": day(2, 1, 1),
				"2026-04": day(1, 0, 1),
				"2026-05": day(2, 1, 1),
				"2026-10": day(4, 2, 2), // partial anchor: never compared
			}), "2026-10-20T09:00:00Z", "2026-10-06T08:00:00Z"),
			want: &ProfileTrend{
				Status: TrendOK, TwoWay: true,
				DaysSinceInbound: trendPtr(6), DaysSinceOutbound: trendPtr(20),
				Recent:   TrendWindow{Months: 5, Messages: 2, Sent: 1, Received: 1, ActiveMonths: 1},
				Baseline: TrendWindow{Months: 5, Messages: 12, Sent: 5, Received: 7, ActiveMonths: 5},
			},
		},
		{
			name: "insufficient without six active months",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(12, 6, 6),
				"2026-05": day(2, 1, 1),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendInsufficientHistory, TwoWay: true,
				Recent:   TrendWindow{Months: 5, Messages: 2, Sent: 1, Received: 1, ActiveMonths: 1},
				Baseline: TrendWindow{Months: 5, Messages: 12, Sent: 6, Received: 6, ActiveMonths: 1},
			},
		},
		{
			name: "insufficient below twelve baseline messages",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(3, 1, 2),
				"2026-01": day(2, 1, 1),
				"2026-02": day(2, 1, 1),
				"2026-03": day(2, 1, 1),
				"2026-04": day(2, 1, 1),
				"2026-05": day(2, 1, 1),
				"2026-06": day(1, 1, 0),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendInsufficientHistory, TwoWay: true,
				Recent:   TrendWindow{Months: 5, Messages: 3, Sent: 2, Received: 1, ActiveMonths: 2},
				Baseline: TrendWindow{Months: 5, Messages: 11, Sent: 5, Received: 6, ActiveMonths: 5},
			},
		},
		{
			name: "zero months stay inside their window as inactive months",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(3, 1, 2),
				"2026-02": day(6, 3, 3),
				"2026-04": day(3, 1, 2),
				"2026-05": day(1, 1, 0),
				"2026-06": day(1, 0, 1),
				"2026-07": day(1, 1, 0),
				"2026-08": day(1, 0, 1),
				"2026-09": day(1, 1, 0),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendOK, TwoWay: true,
				Recent:   TrendWindow{Months: 5, Messages: 5, Sent: 3, Received: 2, ActiveMonths: 5},
				Baseline: TrendWindow{Months: 5, Messages: 12, Sent: 5, Received: 7, ActiveMonths: 3},
			},
		},
		{
			name: "one-way inbound mail never compares",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(6, 0, 6),
				"2026-01": day(6, 0, 6),
				"2026-05": day(2, 0, 2),
				"2026-06": day(2, 0, 2),
				"2026-07": day(2, 0, 2),
				"2026-08": day(2, 0, 2),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendInsufficientHistory, TwoWay: false,
				Recent:   TrendWindow{Months: 5, Messages: 8, Sent: 0, Received: 8, ActiveMonths: 4},
				Baseline: TrendWindow{Months: 5, Messages: 12, Sent: 0, Received: 12, ActiveMonths: 2},
			},
		},
		{
			name: "one-way outbound mail never compares",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(6, 6, 0),
				"2026-01": day(6, 6, 0),
				"2026-05": day(2, 2, 0),
				"2026-06": day(2, 2, 0),
				"2026-07": day(2, 2, 0),
				"2026-08": day(2, 2, 0),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendInsufficientHistory, TwoWay: false,
				Recent:   TrendWindow{Months: 5, Messages: 8, Sent: 8, Received: 0, ActiveMonths: 4},
				Baseline: TrendWindow{Months: 5, Messages: 12, Sent: 12, Received: 0, ActiveMonths: 2},
			},
		},
		{
			name: "fewer buckets scale the windows down, still equal",
			view: trendView(t, trendBuckets(t, "2026-03", 8, map[string]CompanyMonth{
				"2026-04": day(4, 2, 2),
				"2026-05": day(4, 2, 2),
				"2026-06": day(4, 2, 2),
				"2026-07": day(2, 1, 1),
				"2026-08": day(2, 1, 1),
				"2026-09": day(2, 1, 1),
				"2026-10": day(5, 2, 3),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendOK, TwoWay: true,
				Recent:   TrendWindow{Months: 3, Messages: 6, Sent: 3, Received: 3, ActiveMonths: 3},
				Baseline: TrendWindow{Months: 3, Messages: 12, Sent: 6, Received: 6, ActiveMonths: 3},
			},
		},
		{
			name: "the partial anchor month never enters a window",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(3, 1, 2),
				"2026-01": day(3, 2, 1),
				"2026-02": day(3, 1, 2),
				"2026-03": day(2, 1, 1),
				"2026-04": day(2, 1, 1),
				"2026-10": day(100, 50, 50),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendInsufficientHistory, TwoWay: true,
				Recent:   TrendWindow{Months: 5},
				Baseline: TrendWindow{Months: 5, Messages: 13, Sent: 6, Received: 7, ActiveMonths: 5},
			},
		},
		{
			name: "two complete months form equal one-month windows",
			view: trendView(t, trendBuckets(t, "2026-08", 3, map[string]CompanyMonth{
				"2026-08": day(12, 6, 6),
				"2026-09": day(2, 1, 1),
			}), "", ""),
			want: &ProfileTrend{
				Status: TrendInsufficientHistory, TwoWay: true,
				Recent:   TrendWindow{Months: 1, Messages: 2, Sent: 1, Received: 1, ActiveMonths: 1},
				Baseline: TrendWindow{Months: 1, Messages: 12, Sent: 6, Received: 6, ActiveMonths: 1},
			},
		},
		{
			name: "too few complete months form no window",
			view: trendView(t, trendBuckets(t, "2026-09", 2, map[string]CompanyMonth{
				"2026-09": day(3, 1, 2),
				"2026-10": day(1, 1, 0),
			}), "2026-10-01T09:00:00Z", ""),
			want: &ProfileTrend{
				Status:           TrendInsufficientHistory,
				DaysSinceInbound: trendPtr(25),
			},
		},
		{
			name: "no mail has no trend",
			view: ProfileView{},
			want: nil,
		},
		{
			name: "a future instant reads zero and a junk instant has no age",
			view: trendView(t, trendBuckets(t, "2025-11", 12, map[string]CompanyMonth{
				"2025-12": day(3, 1, 2),
				"2026-01": day(3, 2, 1),
				"2026-02": day(3, 1, 2),
				"2026-03": day(2, 1, 1),
				"2026-04": day(1, 0, 1),
				"2026-05": day(2, 1, 1),
			}), "2026-11-30T09:00:00Z", "junk"),
			want: &ProfileTrend{
				Status: TrendOK, TwoWay: true,
				DaysSinceInbound: trendPtr(0), DaysSinceOutbound: nil,
				Recent:   TrendWindow{Months: 5, Messages: 2, Sent: 1, Received: 1, ActiveMonths: 1},
				Baseline: TrendWindow{Months: 5, Messages: 12, Sent: 5, Received: 7, ActiveMonths: 5},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := companyTrend(tc.view, trendNow); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("companyTrend = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestProfileCarriesTheCadenceTrend: a real seeded profile attaches the trend
// derived from its own store rows. Atelier Nord's one in-window month is
// September (one received message); the four October messages sit in the
// partial anchor month and must not enter the recent window, and the only
// sent message of the compared span is October's, so the span is one-way and
// the status stays insufficient_history.
func TestProfileCarriesTheCadenceTrend(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Profile(t.Context(), "c:atelier nord", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	want := &ProfileTrend{
		Status: TrendInsufficientHistory, TwoWay: false,
		DaysSinceInbound: trendPtr(6), DaysSinceOutbound: trendPtr(20),
		Recent:   TrendWindow{Months: 5, Messages: 1, Sent: 0, Received: 1, ActiveMonths: 1},
		Baseline: TrendWindow{Months: 5},
	}
	if !reflect.DeepEqual(view.Trend, want) {
		t.Fatalf("seeded trend = %+v, want %+v", view.Trend, want)
	}
}

// seedCadenceMail adds one undeclared correspondent (d:cadence.test) whose
// mail fills both compared windows: a five-month baseline of fourteen messages
// across five active months and a two-month recent side of six two-way
// messages, plus one message in the partial anchor month the trend must
// ignore.
func seedCadenceMail(t *testing.T, st *store.Store) {
	t.Helper()
	const address = "nadia@cadence.test"
	seeded := []struct {
		month    string
		received int
		sent     int
	}{
		{"2025-12", 2, 1}, {"2026-01", 1, 2}, {"2026-02", 2, 1},
		{"2026-03", 1, 2}, {"2026-04", 1, 1},
		{"2026-05", 2, 2}, {"2026-06", 1, 1},
		{"2026-10", 1, 0},
	}
	var msgs []mail.Message
	for _, window := range seeded {
		at := func(day int) string { return fmt.Sprintf("%s-%02dT09:00:00Z", window.month, day) }
		for i := range window.received {
			msgs = append(msgs, worldRecv(
				fmt.Sprintf("cad-%s-r%d", window.month, i), address, "Nadia Cadence",
				at(10+i*2), fmt.Sprintf("C-cad-%s-r%d", window.month, i),
				"Cadence note "+window.month))
		}
		for i := range window.sent {
			msgs = append(msgs, worldSent(
				fmt.Sprintf("cad-%s-s%d", window.month, i), at(12+i*2),
				fmt.Sprintf("C-cad-%s-s%d", window.month, i), "Cadence reply "+window.month,
				mail.Recipient{Kind: mail.To, Name: "Nadia Cadence", Address: address}))
		}
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64, len(msgs))
	if err := st.EachMessage(t.Context(), store.MessageFilter{}, func(row *store.MessageRow) error {
		ids[row.Message.ID] = row.RowID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	verdicts := make([]store.VerdictRow, 0, len(msgs))
	for _, m := range msgs {
		verdict, reason := scope.Owner, scope.ReasonRecipientMatch
		if m.FolderRole == mail.RoleSent {
			reason = scope.ReasonSentByOwner
		}
		verdicts = append(verdicts, store.VerdictRow{RowID: ids[m.ID], Verdict: verdict, Reason: reason})
	}
	if err := st.WriteVerdicts(t.Context(), "test", verdicts); err != nil {
		t.Fatal(err)
	}
}

// TestProfileTrendComparesSeededHistory proves the whole path with enough
// real store mail: the profile carries an "ok" trend whose equal windows are
// the seeded complete months, the partial anchor month's message is excluded
// from them, and the days-since ages are anchored on the profile's own
// direction instants.
func TestProfileTrendComparesSeededHistory(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	seedCadenceMail(t, st)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Profile(t.Context(), "d:cadence.test", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastInboundAt != "2026-10-10T09:00:00Z" || view.LastOutboundAt != "2026-06-12T09:00:00Z" {
		t.Fatalf("direction instants %s / %s", view.LastInboundAt, view.LastOutboundAt)
	}
	want := &ProfileTrend{
		Status: TrendOK, TwoWay: true,
		DaysSinceInbound: trendPtr(16), DaysSinceOutbound: trendPtr(136),
		Recent:   TrendWindow{Months: 5, Messages: 6, Sent: 3, Received: 3, ActiveMonths: 2},
		Baseline: TrendWindow{Months: 5, Messages: 14, Sent: 7, Received: 7, ActiveMonths: 5},
	}
	if !reflect.DeepEqual(view.Trend, want) {
		t.Fatalf("seeded trend = %+v, want %+v", view.Trend, want)
	}
}

// TestProfileWeightCountsTheTrend: the profile cache's byte estimate must
// account for a present trend, or the budget silently undercounts it.
func TestProfileWeightCountsTheTrend(t *testing.T) {
	view := trendView(t, trendBuckets(t, "2025-11", 12, nil), "", "")
	without := weighProfile(profileValue{view: view})
	view.Trend = &ProfileTrend{Status: TrendInsufficientHistory}
	with := weighProfile(profileValue{view: view})
	if got := with - without; got != profileTrendBytes {
		t.Fatalf("a present trend adds %d bytes to the profile estimate, want %d", got, profileTrendBytes)
	}
}
