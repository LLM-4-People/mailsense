package conversations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// batchSizes records how many ids each conversation-state read carried.
type batchSizes struct {
	storeReader
	mu      sync.Mutex
	batches []int
}

func (b *batchSizes) ThreadStates(ctx context.Context, ids []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	b.mu.Lock()
	b.batches = append(b.batches, len(ids))
	b.mu.Unlock()
	return b.storeReader.ThreadStates(ctx, ids, signals)
}

// TestStateReadsRespectTheOwnerBatch: the store owns the conversation-state
// read scale (store.ThreadStatesBatch) and a caller may not build a bigger
// read behind it. The bound used to be a second constant in this package.
func TestStateReadsRespectTheOwnerBatch(t *testing.T) {
	recorder := &batchSizes{}
	// One priority conversation leaves exactly two full batches plus one id.
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C00000": stateFor("C00000", threadintel.NoAction),
	}}
	svc, st, _, _ := newTestServiceWith(t, priority, func(s *store.Store) storeReader {
		recorder.storeReader = s
		return recorder
	})
	total := 2*store.ThreadStatesBatch + 1
	msgs := make([]mail.Message, 0, total)
	for i := range total {
		m := mailtest.Message("src", fmt.Sprintf("m%05d", i))
		m.ConversationID = fmt.Sprintf("C%05d", i)
		m.Subject = fmt.Sprintf("Subject %05d", i)
		m.SenderName, m.SenderAddress = "Sender", fmt.Sprintf("sender%05d@example.org", i)
		m.SentRepresentingAddress = m.SenderAddress
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
		m.ReceivedAt, m.SentOn = "2026-11-05T09:00:00Z", "2026-11-05T09:00:00Z"
		m.IsRead = true
		msgs = append(msgs, m)
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	var verdicts []store.VerdictRow
	if err := st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteVerdicts(t.Context(), "test", verdicts); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Page(t.Context(), Query{Limit: MaxLimit}); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	batches := append([]int(nil), recorder.batches...)
	recorder.mu.Unlock()
	if len(batches) == 0 {
		t.Fatal("the page resolved no state through the store")
	}
	sum := 0
	for _, size := range batches {
		if size > store.ThreadStatesBatch {
			t.Fatalf("a state read carried %d ids, above the owner's %d", size, store.ThreadStatesBatch)
		}
		sum += size
	}
	if sum != total-1 {
		t.Fatalf("the reads covered %d ids, want %d (all but the priority one)", sum, total-1)
	}
}

// TestSnapshotAssessmentIsTheOneStateLookup: a digest with mail but no state
// fails one way, and a resolved one is returned as stored. Every projection
// reads the state through this method, so the message is written once.
func TestSnapshotAssessmentIsTheOneStateLookup(t *testing.T) {
	want := stateFor("C1", threadintel.AwaitingOwner)
	snap := &snapshot{states: map[string]threadintel.Assessment{"C1": want}}
	got, err := snap.assessment("C1")
	if err != nil || got != want {
		t.Fatalf("assessment %+v %v", got, err)
	}
	if _, err := snap.assessment("C2"); err == nil ||
		err.Error() != `conversations: conversation "C2" has mail but no resolved state` {
		t.Fatalf("missing state error %v", err)
	}
	// The literal lives once: every projection reports it through noState.
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(source), "has mail but no resolved state"); got != 1 {
		t.Fatalf("the state failure is written %d times; report it through noState", got)
	}
	if got := strings.Count(string(source), "snap.assessment("); got != 3 {
		t.Fatalf("the three snapshot projections must read the state through assessment, got %d", got)
	}
}

// stateLessStore models a mailbox whose digests name a conversation the
// resolver cannot place: the store returns no state for it.
type stateLessStore struct{}

func (stateLessStore) MailGeneration() uint64 { return 1 }
func (stateLessStore) ConversationDigests(context.Context) (store.ConversationDigestSet, error) {
	return store.ConversationDigestSet{Conversations: []store.ConversationDigest{{
		ConversationID: "C-orphan", Subject: "Orphan", SearchText: "orphan",
		LastAt: "2026-11-05T09:00:00Z", FirstAt: "2026-11-05T09:00:00Z",
	}}}, nil
}
func (stateLessStore) ThreadStates(context.Context, []string, map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	return map[string]threadintel.Assessment{}, nil
}
func (stateLessStore) CompanyBook(context.Context) (*store.CompanyBook, error) { return nil, nil }
func (stateLessStore) ConversationParticipants(context.Context, []string, int) (map[string]store.ConversationParticipants, error) {
	return map[string]store.ConversationParticipants{}, nil
}
func (stateLessStore) ConversationSnippets(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

// TestEveryReadSurfaceReportsTheMissingState: a digest the resolver cannot
// place fails every read surface with the same message instead of painting a
// zero state. The snapshot build is the first choke point (resolveStates); the
// per-projection guards are the second, and both report through noState, so a
// surface cannot invent its own wording.
func TestEveryReadSurfaceReportsTheMissingState(t *testing.T) {
	want := `conversations: conversation "C-orphan" has mail but no resolved state`
	svc := newService(Deps{Clock: clocktest.New(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC))}, stateLessStore{})
	cases := []struct {
		name string
		call func() error
	}{
		{name: "page", call: func() error {
			_, err := svc.Page(t.Context(), Query{Limit: 10})
			return err
		}},
		{name: "page text path", call: func() error {
			_, err := svc.Page(t.Context(), Query{Limit: 10, Q: "orphan"})
			return err
		}},
		{name: "facts", call: func() error {
			_, err := svc.Facts(t.Context(), "", 5, 5)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil || err.Error() != want {
				t.Fatalf("error %v, want %q", err, want)
			}
		})
	}

	// The snapshot build itself is the first owner: the state map never leaves
	// resolveStates with a conversation missing.
	_, _, err := svc.resolveStates(t.Context(), []string{"C-orphan"})
	if err == nil || err.Error() != want {
		t.Fatalf("resolveStates error %v, want %q", err, want)
	}
}
