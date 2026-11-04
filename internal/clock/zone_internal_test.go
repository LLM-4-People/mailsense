package clock

import (
	"testing"
	"time"
)

// The proposal is built from what the machine says, never from the zone
// this test runs in.
func TestProposalFromTheMachinesSettings(t *testing.T) {
	paris, err := Zone("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	p := propose(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(paris), "Europe/Paris")
	if p != (Proposal{IANA: "Europe/Paris", Abbrev: "CEST", Offset: "+02:00"}) {
		t.Fatalf("%+v", p)
	}
	if p := propose(time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", -(3*3600+1800))), ""); p.Offset != "-03:30" {
		t.Fatalf("%+v", p)
	}
	for _, c := range []struct{ tz, link, want string }{
		{"Asia/Tokyo", "", "Asia/Tokyo"},
		{":Asia/Tokyo", "", "Asia/Tokyo"},
		{"", "/usr/share/zoneinfo/America/Sao_Paulo", "America/Sao_Paulo"},
		{"Local", "/usr/share/zoneinfo/Etc/UTC", "Etc/UTC"},
		{"Mars/Base", "", ""},
		{"", "/etc/localtime", ""},
		{"", "/usr/share/zoneinfo/Local", ""},
	} {
		if got := ianaFrom(c.tz, c.link); got != c.want {
			t.Errorf("ianaFrom(%q, %q) = %q, want %q", c.tz, c.link, got, c.want)
		}
	}
}
