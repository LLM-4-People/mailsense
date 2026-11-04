package outlook

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestDateConversion(t *testing.T) {
	cases := []struct {
		d    Date
		want time.Time
	}{
		{0, time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)},
		{45000.5, time.Date(2023, 3, 15, 12, 0, 0, 0, time.UTC)},
		{45000.499999999, time.Date(2023, 3, 15, 12, 0, 0, 0, time.UTC)}, // float noise rounds to the ms
		{-1.25, time.Date(1899, 12, 29, 6, 0, 0, 0, time.UTC)},           // negative: fraction counts forward
		{949998, time.Date(4501, 1, 1, 0, 0, 0, 0, time.UTC)},            // Outlook's "no date"
	}
	for _, c := range cases {
		if got := c.d.UTC(); !got.Equal(c.want) {
			t.Errorf("%v → %s, want %s", c.d, got, c.want)
		}
	}
	for _, tm := range []time.Time{t0, time.Date(1899, 12, 29, 6, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 7, 30, 15, 0, time.UTC)} {
		if back := DateOf(tm).UTC(); !back.Equal(tm) {
			t.Errorf("round trip %s → %s", tm, back)
		}
	}
	times := mail.Times{NoDateYearAtMost: 1900, NoDateYearAtLeast: 4500}
	if asTime(Date(949998), times) != "" || asTime("not a date", times) != "" || asTime(PropError(hrNotFound), times) != "" {
		t.Fatal("sentinel, junk and missing dates must be empty")
	}
}

// TestLocalInstantConversion pins the built-in-member conversion: the OADate
// carries a local wall clock, so the same civil time maps to different UTC
// instants on either side of a DST change (2026 changes in America/Toronto
// are March 8 and November 1).
func TestLocalInstantConversion(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	toronto, err := time.LoadLocation("America/Toronto")
	if err != nil {
		t.Skipf("America/Toronto tzdata unavailable: %v", err)
	}
	time.Local = toronto
	wall := func(month time.Month, day, hour int) Date {
		return DateOf(time.Date(2026, month, day, hour, 0, 0, 0, time.UTC))
	}
	cases := []struct {
		name string
		d    Date
		want time.Time
	}{
		{"winter standard time", wall(time.January, 15, 9), time.Date(2026, 1, 15, 14, 0, 0, 0, time.UTC)},
		{"before spring forward", wall(time.March, 6, 9), time.Date(2026, 3, 6, 14, 0, 0, 0, time.UTC)},
		{"after spring forward", wall(time.March, 9, 9), time.Date(2026, 3, 9, 13, 0, 0, 0, time.UTC)},
		{"before fall back", wall(time.October, 30, 9), time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC)},
		{"after fall back", wall(time.November, 2, 9), time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.LocalInstant(); !got.Equal(c.want) {
				t.Fatalf("%v local → %s, want %s", float64(c.d), got, c.want)
			}
		})
	}
}

// PROGRESS finding 1 / PORTING §1: the legacy exporter called
// .ToUniversalTime() on PropertyAccessor dates, which are already UTC, so
// they were shifted by the machine's UTC offset. A schema-name date must
// come out as exactly the instant Outlook stored, whatever time.Local is.
func TestSchemaDatesAreNotConvertedAgain(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("EDT", -4*3600)
	defer func() { time.Local = old }()
	stored := time.Date(2026, 10, 12, 16, 0, 0, 0, time.UTC) // PT_SYSTIME, UTC
	times := mail.Times{NoDateYearAtMost: 1900, NoDateYearAtLeast: 4500}
	if got := asTime(DateOf(stored), times); got != "2026-10-12T16:00:00Z" {
		t.Fatalf("schema date shifted: %s", got)
	}
	o := profile()
	it := o.stores[1].root.children[0].items[0]
	it.props[propLastVerbTime] = DateOf(stored)
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", sk); err != nil {
		t.Fatal(err)
	}
	d, err := c.ReadDetails(t.Context(), refOf(sk.msgs[len(sk.msgs)-1]))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range sk.msgs {
		if m.ID == it.id() && (m.LastVerbExecutionTime != "2026-10-12T16:00:00Z" || m.ReceivedAt != "2026-10-01T12:00:00Z") {
			t.Fatalf("table dates shifted: verb %s received %s", m.LastVerbExecutionTime, m.ReceivedAt)
		}
	}
	if d.LocalCommitTime == "" {
		t.Fatal("local commit time comes from the detail pass")
	}
}
