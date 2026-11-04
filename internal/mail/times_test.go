package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStampIsTheOneWireInstant: the wire form is the stored form (UTC,
// second precision, Z) and a missing instant is the empty string, so a
// response never prints a zero date and the UI can parse the value back with
// mail.Parse.
func TestStampIsTheOneWireInstant(t *testing.T) {
	if got := Stamp(time.Time{}); got != "" {
		t.Fatalf("zero instant %q, want the empty string", got)
	}
	// An offset instant renders in UTC, truncated to the second.
	at := time.Date(2026, 11, 4, 23, 15, 42, 987654321, time.FixedZone("CET", 3600))
	if got, want := Stamp(at), "2026-11-04T22:15:42Z"; got != want {
		t.Fatalf("Stamp(%v) = %q, want %q", at, got, want)
	}
	if parsed, ok := Parse(Stamp(at)); !ok || !parsed.Equal(time.Date(2026, 11, 4, 22, 15, 42, 0, time.UTC)) {
		t.Fatalf("the stamped value does not parse back: %v %v", parsed, ok)
	}
}

// TestNoPackageFormatsAWireTimestampByHand keeps the one wire form. A second
// time.RFC3339 call in a response package is how a derived fact ends up with a
// different precision or a local zone in one field.
func TestNoPackageFormatsAWireTimestampByHand(t *testing.T) {
	for _, dir := range []string{"../hub", "../brief", "../conversations", "../world"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			source, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			// Formatting only: parsing an input timestamp is a different
			// contract (the query bounds validate their own argument).
			for _, stale := range []string{"Format(time.RFC3339)", "Format(mail.TimeLayout)"} {
				if strings.Contains(string(source), stale) {
					t.Errorf("%s/%s formats a stamp by hand (%q); call mail.Stamp", dir, name, stale)
				}
			}
		}
	}
}

// TestMonthKeyRoundTrips: the month bucket key is one wire key, and the
// readers that group the same months split it at the owner. The round trip
// holds for every month in zones on both sides of the date line, and a key
// that is not a month renders empty instead of panicking a slicing caller.
func TestMonthKeyRoundTrips(t *testing.T) {
	for _, name := range []string{"UTC", "America/New_York", "Asia/Kathmandu", "Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		zone, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		for month := time.January; month <= time.December; month++ {
			// Both ends of a month, so a zone on either side of the date line
			// lands in the neighbouring local month.
			for _, instant := range []time.Time{
				time.Date(2026, month, 1, 0, 30, 0, 0, time.UTC),
				time.Date(2026, month, 31, 23, 30, 0, 0, time.UTC),
			} {
				local := instant.In(zone)
				key := MonthKey(local)
				year, mon := SplitMonth(key)
				if year != local.Format("2006") || mon != local.Format("01") {
					t.Fatalf("%s %v: key %q splits to %q/%q, want %q/%q",
						name, instant, key, year, mon, local.Format("2006"), local.Format("01"))
				}
				if want := local.Format("2006-01"); key != want {
					t.Fatalf("%s %v: key %q, want %q", name, instant, key, want)
				}
			}
		}
	}
	for _, bad := range []string{"", "2026", "2026-1", "2026/01", "2026-01-05", "xxxx-01"} {
		if year, month := SplitMonth(bad); year != "" || month != "" {
			t.Errorf("SplitMonth(%q) = %q/%q, want empty", bad, year, month)
		}
	}
}
