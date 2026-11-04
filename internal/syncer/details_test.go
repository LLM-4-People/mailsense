package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// snapConn delivers partial messages through Snapshot and completes them
// through ReadDetails, like the Outlook connector.
type snapConn struct {
	fakeConn
	present  map[string][]string // folder → ids currently there
	stamps   map[string]string   // id → stamp
	failItem map[string]error    // id → ReadDetails error
	cut      map[string]bool     // id → its detail read cut the header block
	read     []string
}

func (c *snapConn) Sync(ctx context.Context, f mail.Folder, _ source.Cursor, sink source.Sink) (source.Cursor, error) {
	var versions []source.Version
	msgs := map[string]mail.Message{}
	for _, id := range c.present[f.Path] {
		m := mailtest.Message(c.id, id)
		m.FolderPath, m.Partial, m.Body, m.Recipients = f.Path, true, "", nil
		m.FolderRole = f.Role
		m.LastModificationTime, m.Stamp = c.stamps[id], c.stamps[id]
		m.ReceivedAt = "2026-10-0" + id[len(id)-1:] + "T00:00:00Z"
		msgs[id] = m
		versions = append(versions, source.Version{ID: id, Stamp: m.Stamp})
	}
	want, err := sink.Snapshot(ctx, versions, true)
	if err != nil {
		return "", err
	}
	for _, id := range c.present[f.Path] {
		if want[id] {
			if err := sink.Message(ctx, msgs[id]); err != nil {
				return "", err
			}
		}
	}
	return "", nil // always reread (no cursor)
}

func (c *snapConn) ReadDetails(_ context.Context, ref source.ItemRef) (mail.Details, error) {
	c.read = append(c.read, ref.ID)
	if err := c.failItem[ref.ID]; err != nil {
		return mail.Details{}, err
	}
	d := mail.Details{Body: "corps de " + ref.ID, Recipients: []mail.Recipient{{Kind: mail.To, Address: "amelie.durand@example.com"}}}
	if c.cut[ref.ID] {
		d.Alterations = []mail.Alteration{{Code: mail.AltHeadersTruncated, Detail: "70000"}}
	}
	return d, nil
}

func newSnapConn() *snapConn {
	return &snapConn{fakeConn: fakeConn{id: "fake"},
		present:  map[string][]string{"Inbox": {"i1", "i2", "i3"}, "Sent": {"s4"}},
		stamps:   map[string]string{"i1": "2026-10-01T00:00:00Z", "i2": "2026-10-01T00:00:00Z", "i3": "2026-10-01T00:00:00Z", "s4": "2026-10-01T00:00:00Z"},
		failItem: map[string]error{}}
}

// The detail pass never sleeps itself: pacing the source is its
// connector's job at its one call choke point (Outlook: outlook.pace.*),
// so there is no second, duplicate pause here.
func TestDetailsPassCompletesNewestFirstInBatches(t *testing.T) {
	clk := clocktest.New(time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC))
	s, st, _, _ := setup(t, map[string]any{"sync.details.batch_size": 2})
	s.clock = clk
	c := newSnapConn()
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Details == nil || rep.Details.Completed != 4 || rep.Details.Pending != 0 {
		t.Fatalf("%v %+v", err, rep.Details)
	}
	if strings.Join(c.read, ",") != "s4,i3,i2,i1" {
		t.Fatalf("details order %v (newest first)", c.read)
	}
	if n := len(clk.Sleeps()); n != 0 {
		t.Fatalf("the detail pass must not pause on its own: %d sleeps", n)
	}
	m, _ := st.GetMessage(t.Context(), "fake", "store-1", "i1")
	if m.Partial || m.Body != "corps de i1" {
		t.Fatalf("%+v", m)
	}
	// Nothing changed: the snapshot wants nothing, no detail is read again.
	c.read = nil
	if rep, err := s.Run(t.Context(), c, nil); err != nil || rep.Totals.Seen != 0 || len(c.read) != 0 {
		t.Fatalf("%v seen %d read %v", err, rep.Totals.Seen, c.read)
	}
}

// sync.details.max_per_run: a short verification run completes only the
// newest N; the rest stay partial and the next run continues.
func TestDetailsPassIsCappedPerRun(t *testing.T) {
	s, _, _, _ := setup(t, map[string]any{"sync.details.batch_size": 2, "sync.details.max_per_run": 3})
	c := newSnapConn()
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Details.Completed != 3 || !rep.Details.Capped || rep.Details.Pending != 1 {
		t.Fatalf("%v %+v", err, rep.Details)
	}
	if strings.Join(c.read, ",") != "s4,i3,i2" {
		t.Fatalf("capped read %v (the newest 3)", c.read)
	}
	c.read = nil
	if rep, err = s.Run(t.Context(), c, nil); err != nil || rep.Details.Completed != 1 || rep.Details.Capped || strings.Join(c.read, ",") != "i1" {
		t.Fatalf("the next run continues: %v %+v %v", err, rep.Details, c.read)
	}
}

// sync.details.settle_after: a message received moments ago (the source
// may still be downloading it) waits for a later run.
func TestDetailsPassDefersUnsettledMessages(t *testing.T) {
	clk := clocktest.New(time.Date(2026, 10, 4, 0, 3, 0, 0, time.UTC)) // s4 arrived 3 minutes ago
	s, _, _, _ := setup(t, map[string]any{"sync.details.settle_after": "5m"})
	s.clock = clk
	c := newSnapConn()
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Details.Completed != 3 || rep.Details.Pending != 1 || strings.Join(c.read, ",") != "i3,i2,i1" {
		t.Fatalf("%v %+v %v", err, rep.Details, c.read)
	}
	clk.Advance(3 * time.Minute)
	c.read = nil
	if rep, err = s.Run(t.Context(), c, nil); err != nil || rep.Details.Pending != 0 || strings.Join(c.read, ",") != "s4" {
		t.Fatalf("settled: %v %+v %v", err, rep.Details, c.read)
	}
}

// L0 leftover: a header block the detail read had to cut is disclosed on
// the message itself (stored with it) and counted in the run, not only in
// the connector's statistics.
func TestDetailAlterationsAreStoredPerMessageAndCounted(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := newSnapConn()
	c.cut = map[string]bool{"i2": true}
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Details == nil || rep.Details.Altered[mail.AltHeadersTruncated] != 1 {
		t.Fatalf("%v %+v", err, rep.Details)
	}
	cut, _ := st.GetMessage(t.Context(), "fake", "store-1", "i2")
	whole, _ := st.GetMessage(t.Context(), "fake", "store-1", "i1")
	if len(cut.Alterations) != 1 || cut.Alterations[0].Code != mail.AltHeadersTruncated || len(whole.Alterations) != 0 {
		t.Fatalf("stored: %+v / %+v", cut.Alterations, whole.Alterations)
	}
}

func TestDetailItemErrorsAreCountedFatalErrorsStop(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := newSnapConn()
	c.failItem["i2"] = &source.ItemError{Code: "0x8004010F", Err: errors.New("gone")}
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Details.Failed != 1 || rep.Details.Failures["0x8004010F"] != 1 || rep.Details.Pending != 1 {
		t.Fatalf("%v %+v", err, rep.Details)
	}
	c2 := newSnapConn()
	c2.id = "fake2"
	c2.failItem["s4"] = errors.New("Outlook closed")
	rep, err = s.Run(t.Context(), c2, nil)
	if err == nil || rep.Status == "succeeded" || rep.Details.Completed != 0 {
		t.Fatalf("a non-item error stops the pass: %v %+v", err, rep.Details)
	}
	if n, _ := st.CountPending(t.Context(), "fake2", time.Time{}); n != 4 {
		t.Fatalf("pending stay pending: %d", n)
	}
}

// An unavailable source (its application crashed) stops the detail pass
// at once: no further item is tried.
func TestUnavailableSourceStopsTheDetailPass(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := newSnapConn()
	c.failItem["s4"] = fmt.Errorf("outlook: %w", source.Unavailable("Outlook crashed"))
	rep, err := s.Run(t.Context(), c, nil)
	if !errors.Is(err, source.ErrUnavailable) || strings.Join(c.read, ",") != "s4" || rep.Status == "succeeded" {
		t.Fatalf("unavailable: %v read %v", err, c.read)
	}
}

func TestCompleteSnapshotRemovesVanishedMessagesAtTheEnd(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := newSnapConn()
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	c.present["Inbox"] = []string{"i1"}
	c.stamps["i1"] = "2026-10-05T00:00:00Z"
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Removed.Deleted != 2 || rep.Folders[0].Removed.Deleted != 2 || rep.Folders[0].Snapshot != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	if rep.Totals.Written != 1 || rep.Details.Completed != 1 {
		t.Fatalf("only the changed message is rewritten and re-read: %+v %+v", rep.Totals, rep.Details)
	}
	if counts, _ := st.CountByRole(t.Context()); counts[mail.RoleInbox] != 1 {
		t.Fatalf("%v", counts)
	}
}

// Review 1b: one failing folder (an offline delegate mailbox) used to abort
// the whole run; now the other folders still sync and the run reports it.
func TestFailingFolderDoesNotAbortOtherFolders(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i"), "Sent": items(2, sent, "s")}, failOn: "Inbox"}
	rep, err := s.Run(t.Context(), c, nil)
	if err == nil || !strings.Contains(err.Error(), "folder Inbox") || rep.Folders[0].Error == "" || rep.Folders[1].Error != "" {
		t.Fatalf("%v %+v", err, rep.Folders)
	}
	if counts, _ := st.CountByRole(t.Context()); counts[mail.RoleSent] != 2 {
		t.Fatalf("Sent must still sync: %v", counts)
	}
	if rep.Scope == nil {
		t.Fatal("scope is still recomputed")
	}
}

func TestOutputFailureStillAbortsTheRun(t *testing.T) {
	if !isOutput(output(errors.New("disk full"))) || isOutput(errors.New("x")) || output(nil) != nil {
		t.Fatal("output marker")
	}
	wrapped := fmt.Errorf("ctx: %w", output(store.ErrNotFound))
	if !isOutput(wrapped) || !errors.Is(wrapped, store.ErrNotFound) {
		t.Fatal("marker survives wrapping and keeps the cause")
	}
}
