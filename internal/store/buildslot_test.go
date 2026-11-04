package store

import (
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// TestBuildSlotBuildsOncePerGeneration is the table form of the single-flight
// contract shared by the two generation-keyed caches: a batch of readers at
// one (generation, configuration) starts exactly one build and all receive its
// value; a repeated batch at the same pair starts none; a mail write moves the
// generation and the next batch starts exactly one more.
//
// The hook holds the first build open so the other readers are genuinely
// concurrent with it; without it a fast build could finish before the rest
// arrive and the test would not observe the join.
func TestBuildSlotBuildsOncePerGeneration(t *testing.T) {
	caches := []struct {
		name      string
		setHook   func(*Store, func())
		read      func(*Store) (any, error)
		builds    func(*Store) uint64
		installed func(*Store) (any, uint64, bool)
	}{
		{
			name:    "company book",
			setHook: func(s *Store, hook func()) { s.bookBuildHook = hook },
			read: func(s *Store) (any, error) {
				book, err := s.CompanyBook(t.Context())
				return book, err
			},
			builds: func(s *Store) uint64 { return s.book.buildCount() },
			installed: func(s *Store) (any, uint64, bool) {
				value, gen, ok := s.book.installedValue()
				return value, gen, ok
			},
		},
		{
			name:    "company counterparts",
			setHook: func(s *Store, hook func()) { s.counterpartsBuildHook = hook },
			read: func(s *Store) (any, error) {
				counterparts, err := s.CompanyCounterparts(t.Context())
				return counterparts, err
			},
			builds: func(s *Store) uint64 { return s.counterparts.buildCount() },
			installed: func(s *Store) (any, uint64, bool) {
				value, gen, ok := s.counterparts.installedValue()
				return value, gen, ok
			},
		},
	}

	const readers = 8
	for _, cache := range caches {
		t.Run(cache.name, func(t *testing.T) {
			s, _ := open(t, nil)
			seedCompanyMailbox(t, s)

			// batch runs readers concurrently and returns their values. hold
			// makes the first build wait until every reader has joined.
			batch := func(hold bool) any {
				t.Helper()
				started, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				if hold {
					cache.setHook(s, func() {
						once.Do(func() {
							close(started)
							<-release
						})
					})
					defer cache.setHook(s, nil)
				}
				values := make([]any, readers)
				errs := make([]error, readers)
				begin := make(chan struct{})
				var wg sync.WaitGroup
				for i := range readers {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						<-begin
						values[i], errs[i] = cache.read(s)
					}(i)
				}
				close(begin)
				if hold {
					select {
					case <-started:
					case <-time.After(10 * time.Second):
						t.Fatal("the build never started")
					}
					// Give the other readers time to reach the join before the
					// build is allowed to finish.
					time.Sleep(25 * time.Millisecond)
					close(release)
				}
				wg.Wait()
				for i, err := range errs {
					if err != nil {
						t.Fatalf("reader %d: %v", i, err)
					}
					if values[i] != values[0] {
						t.Fatalf("reader %d received a different value than reader 0", i)
					}
				}
				return values[0]
			}

			first := batch(true)
			if got := cache.builds(s); got != 1 {
				t.Fatalf("builds after one concurrent batch = %d, want 1", got)
			}
			// Same generation and configuration: served from the installed
			// value, no second build even under concurrency.
			if second := batch(false); second != first {
				t.Fatal("a repeated batch at the same generation built a new value")
			}
			if got := cache.builds(s); got != 1 {
				t.Fatalf("builds after a repeated batch = %d, want 1", got)
			}

			// A mail write moves the generation; the next batch starts exactly
			// one build, whatever the mailbox size.
			message := mailtest.Message("src", "slot-next")
			message.SenderName, message.SenderAddress = "Next", "next@atelier-nord.test"
			message.ReceivedAt, message.SentOn = "2026-10-21T09:00:00Z", "2026-10-21T09:00:00Z"
			message.ConversationID = "C-slot-next"
			mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)

			// The book may legitimately return its previous value when the
			// address book is unchanged; the contract is one build, not a new
			// pointer.
			batch(true)
			if got := cache.builds(s); got != 2 {
				t.Fatalf("builds after the generation moved = %d, want 2", got)
			}
			if _, _, installed := cache.installed(s); !installed {
				t.Fatal("the batch after the write installed no value")
			}
		})
	}
}
