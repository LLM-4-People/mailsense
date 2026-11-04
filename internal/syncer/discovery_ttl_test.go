package syncer

import (
	"testing"
	"time"
)

// TestSteadyStateMailReusesStoredFolders protects the discovery budget: a
// full pass discovers once, then mail-only passes reuse the stored active
// folders until sync.discovery.interval expires.
func TestSteadyStateMailReusesStoredFolders(t *testing.T) {
	s, _, _, _ := setup(t, map[string]any{"sync.discovery.interval": "10m"})
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": items(2, inbox, "i"),
		"Sent":  items(1, sent, "s"),
	}}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatalf("full run: %v", err)
	}
	if c.discovers != 1 {
		t.Fatalf("full run discoveries = %d, want 1", c.discovers)
	}
	for i := range 2 {
		rep, err := s.runFolders(t.Context(), c, nil, false, nil)
		if err != nil {
			t.Fatalf("mail pass %d: %v", i+1, err)
		}
		if len(rep.Folders) != 2 {
			t.Fatalf("mail pass %d folders = %d, want the 2 stored folders", i+1, len(rep.Folders))
		}
		if c.discovers != 1 {
			t.Fatalf("mail pass %d discoveries = %d, want 1 (stored folders reused)", i+1, c.discovers)
		}
	}
	s.mu.Lock()
	s.discovered[c.ID()] = time.Now().Add(-11 * time.Minute)
	s.mu.Unlock()
	if _, err := s.runFolders(t.Context(), c, nil, false, nil); err != nil {
		t.Fatalf("expired mail pass: %v", err)
	}
	if c.discovers != 2 {
		t.Fatalf("discoveries after TTL = %d, want 2", c.discovers)
	}
}

// TestFailedStoredFolderForcesRediscovery proves a stale stored path is not
// retried every minute: one failed mail pass invalidates the discovery, and
// the next pass reads the source's folder list again.
func TestFailedStoredFolderForcesRediscovery(t *testing.T) {
	s, _, _, _ := setup(t, map[string]any{"sync.discovery.interval": "10m"})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatalf("full run: %v", err)
	}
	c.failOn = "Inbox"
	c.version = 2
	if rep, err := s.runFolders(t.Context(), c, nil, false, nil); err == nil || rep.Status == "" {
		t.Fatal("a failed stored folder must fail the mail pass")
	}
	if c.discovers != 1 {
		t.Fatalf("failed mail pass discoveries = %d, want the stored list reused", c.discovers)
	}
	c.failOn = ""
	if _, err := s.runFolders(t.Context(), c, nil, false, nil); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	if c.discovers != 2 {
		t.Fatalf("discoveries after a failed stored folder = %d, want 2", c.discovers)
	}
}
