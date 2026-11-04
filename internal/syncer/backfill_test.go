package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// htmlConn is a snapConn whose detail read declares an HTML body, like the
// Outlook connector after the HTML capture change.
type htmlConn struct {
	*snapConn
	html    string
	fail    map[string]error
	block   chan struct{}
	entered chan struct{}
}

func (c *htmlConn) ReadDetails(ctx context.Context, ref source.ItemRef) (mail.Details, error) {
	if c.entered != nil {
		close(c.entered)
		c.entered = nil
	}
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return mail.Details{}, ctx.Err()
		}
	}
	if err := c.fail[ref.ID]; err != nil {
		return mail.Details{}, err
	}
	d, err := c.snapConn.ReadDetails(ctx, ref)
	if err != nil {
		return d, err
	}
	d.BodyHTML, d.BodyHTMLKnown = c.html+ref.ID, true
	return d, nil
}

func seedOldMessages(t *testing.T, s *Syncer) *snapConn {
	t.Helper()
	c := newSnapConn()
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBackfillStoresHTMLOnlyAndKeepsTheLearningBody(t *testing.T) {
	s, st, bus, cfg := setup(t, nil)
	base := seedOldMessages(t, s)
	if n, err := st.CountBackfill(t.Context(), "fake"); err != nil || n != 4 {
		t.Fatalf("missing before %d %v", n, err)
	}
	h := &htmlConn{snapConn: base, html: "<p>rich ", fail: map[string]error{}}
	m := jobs.NewManager(cfg, bus, nil)
	j, err := s.StartBackfill(m, h)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("backfill did not finish")
	}
	if info := j.Info(); info.Status != runstate.Succeeded || info.Kind != BackfillJobKind || info.Name != "fake" {
		t.Fatalf("job %+v", info)
	}
	if n, _ := st.CountBackfill(t.Context(), "fake"); n != 0 {
		t.Fatalf("still missing %d", n)
	}
	got, _ := st.GetMessage(t.Context(), "fake", "store-1", "i1")
	if got.BodyHTML != "<p>rich i1" || !got.BodyHTMLKnown {
		t.Fatalf("html %+v", got)
	}
	if got.Body != "corps de i1" {
		t.Fatalf("plain body changed: %q", got.Body)
	}
	if res, _ := st.Search(t.Context(), "corps", 10); res.Total == 0 {
		t.Fatal("learning search lost the body")
	}
}

func TestBackfillSkipsUnreadableItemsAndKeepsGoing(t *testing.T) {
	s, st, bus, cfg := setup(t, nil)
	base := seedOldMessages(t, s)
	h := &htmlConn{snapConn: base, html: "<p>", fail: map[string]error{
		"i2": &source.ItemError{Code: "0x8004010F", Err: errors.New("gone")},
	}}
	m := jobs.NewManager(cfg, bus, nil)
	j, err := s.StartBackfill(m, h)
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if info := j.Info(); info.Status != runstate.Succeeded {
		t.Fatalf("an item failure failed the job: %+v", info)
	}
	if n, _ := st.CountBackfill(t.Context(), "fake"); n != 1 {
		t.Fatalf("the unreadable row must stay missing: %d", n)
	}
	if !strings.Contains(j.Info().Progress.Message, "skipped") {
		t.Fatalf("progress %+v", j.Info().Progress)
	}
}

func TestBackfillReportsASourceThatCannotReRead(t *testing.T) {
	s, st, bus, cfg := setup(t, nil)
	seedOldMessages(t, s)
	m := jobs.NewManager(cfg, bus, nil)
	j, err := s.StartBackfill(m, &fakeConn{id: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if info := j.Info(); info.Status != runstate.Succeeded || !strings.Contains(info.Progress.Message, "cannot re-read") {
		t.Fatalf("job %+v", info)
	}
	if n, _ := st.CountBackfill(t.Context(), "fake"); n != 4 {
		t.Fatalf("nothing should be marked known: %d", n)
	}
}

func TestBackfillIsOneAtATimeAndCancelable(t *testing.T) {
	s, _, bus, cfg := setup(t, nil)
	base := seedOldMessages(t, s)
	block := make(chan struct{})
	entered := make(chan struct{})
	h := &htmlConn{snapConn: base, html: "<p>", fail: map[string]error{}, block: block, entered: entered}
	m := jobs.NewManager(cfg, bus, nil)
	j, err := s.StartBackfill(m, h)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := s.StartBackfill(m, h); !errors.Is(err, ErrBackfillBusy) {
		t.Fatalf("second start: %v", err)
	}
	j.Cancel()
	close(block)
	select {
	case <-j.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled backfill did not stop")
	}
	if info := j.Info(); info.Status != runstate.Cancelled {
		t.Fatalf("status %q", info.Status)
	}
}
