package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The profile wire sends attendee_count, never the attendee array. The
// Companies module must read that count; the calendar's attendee details are
// not part of the profile response.
func TestCompaniesReadTheWireAttendeeCount(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "24-companies.js"))
	if err != nil {
		t.Fatal(err)
	}
	module := string(source)
	if !strings.Contains(module, "event.attendee_count") {
		t.Fatal("the Companies meeting row does not read event.attendee_count")
	}
	if strings.Contains(module, "event.attendees") {
		t.Fatal("the Companies meeting row still reads the service-side attendee array")
	}
}
