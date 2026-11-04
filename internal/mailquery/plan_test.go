package mailquery

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeContactAddressesExactAndUnique(t *testing.T) {
	p := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
		TimeRange: TimeAll, Limit: 10, Order: OrderDesc,
		ContactAddresses: []string{" Ada@Example.com ", "ada@example.com", "grace@example.com"}}
	got := p.Normalize()
	if len(got.ContactAddresses) != 2 || got.ContactAddresses[0] != "ada@example.com" ||
		got.ContactAddresses[1] != "grace@example.com" {
		t.Fatalf("normalized addresses %+v", got.ContactAddresses)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("normalized plan invalid: %v", err)
	}
}

func TestNormalizeDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   Plan
		want Plan
	}{
		{"contacts", Plan{Kind: KindContacts}, Plan{Kind: KindContacts, Action: ActionRank,
			Direction: DirectionBoth, Metric: MetricMessages, TimeRange: TimeAll, Limit: 15, Order: OrderDesc}},
		{"messages", Plan{Kind: KindMessages}, Plan{Kind: KindMessages, Action: ActionCount,
			Direction: DirectionBoth, TimeRange: TimeAll, Limit: 10, Order: OrderDesc}},
		{"time", Plan{Kind: KindTime}, Plan{Kind: KindTime, Action: ActionList,
			Direction: DirectionBoth, GroupBy: GroupMonth, TimeRange: TimeAll, Limit: 12, Order: OrderDesc}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Normalize()
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("normalize: got %+v, want %+v", got, c.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("default plan invalid: %v", err)
			}
		})
	}
}

func TestValidateRejectsBadPlans(t *testing.T) {
	base := Plan{Kind: KindContacts, Action: ActionRank, Direction: DirectionBoth,
		Metric: MetricMessages, TimeRange: TimeAll, Limit: 15, Order: OrderDesc}
	cases := []struct {
		name string
		edit func(*Plan)
	}{
		{"unknown kind", func(p *Plan) { p.Kind = "people" }},
		{"contacts count", func(p *Plan) { p.Action = ActionCount }},
		{"bad direction", func(p *Plan) { p.Direction = "sideways" }},
		{"bad metric", func(p *Plan) { p.Metric = "replies" }},
		{"zero limit", func(p *Plan) { p.Limit = 0 }},
		{"huge limit", func(p *Plan) { p.Limit = 51 }},
		{"bad order", func(p *Plan) { p.Order = "newest" }},
		{"bad range", func(p *Plan) { p.TimeRange = "someday" }},
		{"bad since", func(p *Plan) { p.Since = "26/10/2026" }},
		{"reversed dates", func(p *Plan) { p.Since, p.Until = "2026-10-26", "2026-10-01" }},
		{"control char", func(p *Plan) { p.Contact = "ada\x00" }},
		{"too long", func(p *Plan) { p.Text = strings.Repeat("x", 201) }},
		{"empty contact address", func(p *Plan) { p.ContactAddresses = []string{""} }},
		{"malformed contact address", func(p *Plan) { p.ContactAddresses = []string{"not-an-address"} }},
		{"contact address space", func(p *Plan) { p.ContactAddresses = []string{"ada lovelace@example.com"} }},
		{"too many contact addresses", func(p *Plan) {
			p.ContactAddresses = make([]string, MaxContactAddresses+1)
			for i := range p.ContactAddresses {
				p.ContactAddresses[i] = "p" + strings.Repeat("x", i%3) + "@example.com"
			}
		}},
		{"contact address too long", func(p *Plan) {
			p.ContactAddresses = []string{strings.Repeat("a", MaxContactAddressChars) + "@example.com"}
		}},
		{"messages metric", func(p *Plan) { p.Kind, p.Action, p.Metric = KindMessages, ActionCount, MetricThreads }},
		{"time group", func(p *Plan) { p.Kind, p.Action, p.GroupBy = KindTime, ActionList, "year" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base
			c.edit(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("accepted %+v", p)
			}
		})
	}
}

func TestResolveRelativeRangesInOwnerZone(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no Europe/Paris")
	}
	now := time.Date(2026, 10, 26, 15, 30, 0, 0, paris)
	cases := []struct {
		rangeName TimeRange
		since     string
		until     string
	}{
		{TimeToday, "2026-10-25T23:00:00Z", "2026-10-26T23:00:00Z"},
		{TimeYesterday, "2026-10-24T22:00:00Z", "2026-10-25T23:00:00Z"},
		{TimeLast7Days, "2026-10-19T22:00:00Z", "2026-10-26T23:00:00Z"},
		{TimeLast30Days, "2026-09-26T22:00:00Z", "2026-10-26T23:00:00Z"},
		{TimeThisMonth, "2026-09-30T22:00:00Z", "2026-10-31T23:00:00Z"},
		{TimeLastMonth, "2026-08-31T22:00:00Z", "2026-09-30T22:00:00Z"},
		{TimeThisYear, "2025-12-31T23:00:00Z", "2026-12-31T23:00:00Z"},
		{TimeLastYear, "2024-12-31T23:00:00Z", "2025-12-31T23:00:00Z"},
		{TimeAll, "", ""},
	}
	for _, c := range cases {
		t.Run(string(c.rangeName), func(t *testing.T) {
			p := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
				TimeRange: c.rangeName, Limit: 10, Order: OrderDesc}
			got, window, err := p.Resolve(now, paris)
			if err != nil {
				t.Fatal(err)
			}
			if got.Since != c.since || got.Until != c.until || window.Since != c.since || window.Until != c.until {
				t.Fatalf("got %q..%q window %+v, want %q..%q", got.Since, got.Until, window, c.since, c.until)
			}
		})
	}
}

func TestResolveExplicitDatesAreInclusiveInOwnerZone(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no Europe/Paris")
	}
	p := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
		Since: "2026-10-01", Until: "2026-10-01", Limit: 10, Order: OrderDesc}
	got, _, err := p.Resolve(time.Now(), paris)
	if err != nil {
		t.Fatal(err)
	}
	if got.Since != "2026-09-30T22:00:00Z" || got.Until != "2026-10-01T22:00:00Z" {
		t.Fatalf("same-day bounds %q..%q", got.Since, got.Until)
	}
}

func TestResolveAcrossDSTUsesLocalMidnight(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no Europe/Paris")
	}
	now := time.Date(2026, 3, 29, 12, 0, 0, 0, paris) // DST starts 29 March 2026
	p := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
		TimeRange: TimeThisMonth, Limit: 10, Order: OrderDesc}
	got, _, err := p.Resolve(now, paris)
	if err != nil {
		t.Fatal(err)
	}
	if got.Since != "2026-02-28T23:00:00Z" || got.Until != "2026-03-31T22:00:00Z" {
		t.Fatalf("DST month %q..%q", got.Since, got.Until)
	}
}
