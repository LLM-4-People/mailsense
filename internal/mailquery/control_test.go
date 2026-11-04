package mailquery

import "testing"

// TestPlanRejectsC1Control: the contact and text filters read the mail owner's
// unicode.IsControl predicate, so U+0080..U+009F are refused too.
func TestPlanRejectsC1Control(t *testing.T) {
	base := Plan{Kind: KindMessages, Action: ActionCount, Direction: DirectionBoth,
		TimeRange: TimeAll, Limit: 10, Order: OrderDesc}
	for _, r := range []rune{0x00, 0x1f, 0x7f, 0x80, 0x85, 0x9f} {
		for _, edit := range []func(*Plan){
			func(p *Plan) { p.Text = "ada" + string(r) },
			func(p *Plan) { p.Contact = "ada" + string(r) },
			func(p *Plan) { p.ContactAddresses = []string{"ada" + string(r) + "@example.test"} },
		} {
			p := base
			edit(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("a U+%04X control was accepted", r)
			}
		}
	}
	p := base
	p.Text, p.Contact = "ada lovelace", "grace"
	if err := p.Validate(); err != nil {
		t.Fatalf("normal filters were refused: %v", err)
	}
}
