package syncer

import (
	"testing"
)

// Review 1a bug 5: sync.skip_ratio_fatal = 0 failed every non-empty folder.
func TestSkipRatioZeroMeansAnySkipIsFatal(t *testing.T) {
	s, _, _, _ := setup(t, map[string]any{"sync.skip_ratio_fatal": 0.0})
	if _, err := s.Run(t.Context(), &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i")}}, nil); err != nil {
		t.Fatalf("a folder without skips must pass: %v", err)
	}
}

// Review 1a bug 4: two runs of one source interleaved and failed reconciliation.
func TestConcurrentRunOfSameSourceIsRefused(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	block := make(chan struct{})
	entered := make(chan struct{})
	first := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i")}, block: block, entered: entered}
	done := make(chan error, 1)
	go func() { _, err := s.Run(t.Context(), first, nil); done <- err }()
	<-entered
	second := &fakeConn{id: "fake", version: 9, items: map[string][]item{"Inbox": items(3, inbox, "j")}}
	_, err := s.Run(t.Context(), second, nil)
	close(block)
	if err == nil {
		t.Fatal("a second run of a source that is already syncing must be refused")
	}
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}
}
