package mailquery

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func rankedPlan(kind Kind, limit int) Plan {
	return Plan{Kind: kind, Action: ActionRank, Direction: DirectionBoth,
		Metric: MetricMessages, TimeRange: TimeAll, Limit: limit, Order: OrderDesc}
}

// TestRankedEntityRuleIsOneRule: contacts and companies apply the same five
// checks, so a rule added to one kind cannot pass the other.
func TestRankedEntityRuleIsOneRule(t *testing.T) {
	for _, kind := range []struct {
		kind             Kind
		singular, plural string
		limit            int
	}{
		{KindContacts, "contact", "contacts", MaxContactLimit},
		{KindCompanies, "company", "companies", MaxCompanyLimit},
	} {
		t.Run(string(kind.kind), func(t *testing.T) {
			cases := []struct {
				name string
				edit func(*Plan)
				want string
			}{
				{"action", func(p *Plan) { p.Action = ActionCount }, "need action rank"},
				{"direction", func(p *Plan) { p.Direction = "sideways" }, "invalid direction"},
				{"metric", func(p *Plan) { p.Metric = "replies" }, "invalid " + kind.singular + " metric"},
				{"group", func(p *Plan) { p.GroupBy = GroupMonth }, kind.plural + " cannot group by time"},
				{"zero limit", func(p *Plan) { p.Limit = 0 }, kind.singular + " limit must be 1.."},
				{"huge limit", func(p *Plan) { p.Limit = kind.limit + 1 }, kind.singular + " limit must be 1.."},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					p := rankedPlan(kind.kind, kind.limit)
					c.edit(&p)
					err := p.Validate()
					if err == nil || !strings.Contains(err.Error(), c.want) {
						t.Fatalf("Validate = %v, want %q", err, c.want)
					}
				})
			}
			p := rankedPlan(kind.kind, kind.limit)
			if err := p.Validate(); err != nil {
				t.Fatalf("the bound limit %d was refused: %v", kind.limit, err)
			}
		})
	}
}

// TestDirectionRuleIsOneRule: contacts, companies, messages and time read the
// same three direction values.
func TestDirectionRuleIsOneRule(t *testing.T) {
	kinds := []Plan{
		rankedPlan(KindContacts, MaxContactLimit),
		rankedPlan(KindCompanies, MaxCompanyLimit),
		{Kind: KindMessages, Action: ActionCount, TimeRange: TimeAll, Limit: 10, Order: OrderDesc},
		{Kind: KindTime, Action: ActionList, GroupBy: GroupMonth, TimeRange: TimeAll, Limit: 12, Order: OrderDesc},
	}
	for _, dir := range []Direction{DirectionBoth, DirectionSent, DirectionReceived} {
		for i := range kinds {
			p := kinds[i]
			p.Direction = dir
			if err := p.Validate(); err != nil {
				t.Fatalf("%s with direction %q: %v", p.Kind, dir, err)
			}
		}
	}
	for i := range kinds {
		p := kinds[i]
		p.Direction = "sideways"
		if err := p.Validate(); err == nil {
			t.Fatalf("%s accepted the direction %q", p.Kind, p.Direction)
		}
	}
}

// TestWindowRuleIsOneRule: Validate and Resolve refuse the same bad bound pair
// with the same message.
func TestWindowRuleIsOneRule(t *testing.T) {
	base := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
		TimeRange: TimeAll, Limit: 10, Order: OrderDesc}
	for _, c := range []struct {
		name         string
		since, until string
		want         string
	}{
		{"reversed", "2026-10-26T00:00:00Z", "2026-10-01T00:00:00Z", "since must not be after until"},
		{"same instant", "2026-10-26T00:00:00Z", "2026-10-26T00:00:00Z", "since and until must describe a non-empty window"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := base
			p.Since, p.Until = c.since, c.until
			verr := p.Validate()
			if verr == nil || !strings.Contains(verr.Error(), c.want) {
				t.Fatalf("Validate = %v, want %q", verr, c.want)
			}
			if _, _, rerr := p.Resolve(time.Now(), time.UTC); rerr == nil || rerr.Error() != verr.Error() {
				t.Fatalf("Resolve = %v, Validate = %v: the two must agree", rerr, verr)
			}
		})
	}

	// A date-only pair is inclusive: the same day still covers a whole day.
	p := base
	p.Since, p.Until = "2026-10-26", "2026-10-26"
	if err := p.Validate(); err != nil {
		t.Fatalf("an inclusive same-day window was refused: %v", err)
	}
	if _, w, err := p.Resolve(time.Now(), time.UTC); err != nil || w.Since == w.Until {
		t.Fatalf("inclusive same-day window resolved to %+v (%v)", w, err)
	}
}

// TestContactAddressBoundCountsRunes: the address bound is the mail owner's
// rune bound, not a byte count of the same number.
func TestContactAddressBoundCountsRunes(t *testing.T) {
	base := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
		TimeRange: TimeAll, Limit: 10, Order: OrderDesc}
	domain := "@x.io"
	atBound := strings.Repeat("é", MaxContactAddressChars-len(domain)) + domain
	if got := utf8.RuneCountInString(atBound); got != MaxContactAddressChars {
		t.Fatalf("sample has %d runes, want %d", got, MaxContactAddressChars)
	}
	if len(atBound) <= MaxContactAddressChars {
		t.Fatalf("sample is %d bytes, not above the byte bound", len(atBound))
	}
	p := base
	p.ContactAddresses = []string{atBound}
	if err := p.Validate(); err != nil {
		t.Fatalf("a %d-rune address was refused: %v", MaxContactAddressChars, err)
	}
	p.ContactAddresses = []string{strings.Repeat("é", MaxContactAddressChars-len(domain)+1) + domain}
	if err := p.Validate(); err == nil {
		t.Fatal("an address one rune over the bound was accepted")
	}
}
