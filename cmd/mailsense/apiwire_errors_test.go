package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestWireErrorIdentitiesComeFromTheAPI: the wiring answers a missing mailbox
// pick, an unreadable triage profile and a failed provider test with the API's
// one constructor each. It used to re-declare the wire code and message at
// five sites, so a wording change in internal/api never reached them.
func TestWireErrorIdentitiesComeFromTheAPI(t *testing.T) {
	data, err := os.ReadFile("apiwire.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, literal := range []string{`"no_mailbox_picked"`, `"invalid_profile"`, `"provider_test_failed"`} {
		if strings.Contains(source, literal) {
			t.Errorf("apiwire.go re-declares %s instead of using the API constructor", literal)
		}
	}
	for call, want := range map[string]int{
		"api.ErrNoMailboxPicked()": 2, // the sync and the backfill starters
		"api.ErrInvalidProfile()":  2, // the question catalogue and the label batch
		"api.ProviderTestFailed()": 1,
	} {
		if got := bytes.Count(data, []byte(call)); got != want {
			t.Errorf("apiwire.go calls %s %d time(s), want %d", call, got, want)
		}
	}
}
