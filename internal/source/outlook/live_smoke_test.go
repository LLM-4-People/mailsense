//go:build windows && outlooklive

package outlook

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// TestSmoke checks the COM layer quickly (about a minute): Discover,
// one Sync of the picked store's Inbox (-live.pick: the profile's default
// store) into a counting sink, and the details of its newest messages
// (at most 30, within -live.details). Every call runs under the live
// limits (livetest.Open: -live.rate, unpicked stores not walked). Prints
// aggregates only.
func TestSmoke(t *testing.T) {
	ctx := t.Context()
	dir, _ := filepath.Abs("live-smoke")
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	phases, _ := events.Subscribe(bus, TopicPhase, 4096)
	c, err := New("outlook", cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer func() {
		for len(phases.C) > 0 {
			p := <-phases.C
			if p.Phase == "discover-store" {
				t.Logf("SMOKE store kind=%s folders=%d calls=%d time=%s", p.Kind, p.Rows, p.Calls, p.Duration.Round(time.Millisecond))
			}
		}
	}()
	budget := openLive(t, ctx, c, cfg)
	start := time.Now()
	folders, err := c.Discover(ctx)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	t.Logf("SMOKE discover %d folders in %s", len(folders), time.Since(start).Round(time.Millisecond))
	start, calls := time.Now(), c.Stats().Calls
	if _, err := c.Discover(ctx); err != nil {
		t.Fatalf("discover again: %v", err)
	}
	t.Logf("SMOKE warm discover in %s, %d calls", time.Since(start).Round(time.Millisecond), c.Stats().Calls-calls)
	var inbox *mail.Folder
	for i, f := range folders {
		if f.StoreKind == mail.StorePrimary && f.Role == mail.RoleInbox {
			inbox = &folders[i]
		}
	}
	if inbox == nil {
		t.Fatal("no primary inbox")
	}
	columnAcceptance(t, ctx, c, *inbox)
	sk := &countSink{}
	start = time.Now()
	if _, err := c.Sync(ctx, *inbox, "", sk); err != nil {
		t.Fatalf("sync: %v", err)
	}
	t.Logf("SMOKE inbox items=%d snapshot=%d emitted=%d skips=%d in %s", inbox.ItemCount, sk.snapshot, len(sk.msgs), sk.skips,
		time.Since(start).Round(time.Millisecond))
	sort.Slice(sk.msgs, func(i, j int) bool { return sk.msgs[i].ReceivedAt > sk.msgs[j].ReceivedAt })
	failed := 0
	for _, m := range sk.msgs[:budget.Take(t, min(30, len(sk.msgs)))] {
		if _, err := c.ReadDetails(ctx, source.ItemRef{StoreID: m.StoreID, ID: m.ID}); err != nil {
			failed++
			t.Logf("SMOKE details failure code=%s", Code(err))
			if unavailable(err) {
				t.Fatalf("SMOKE Outlook unavailable: %v", err)
			}
		}
	}
	empty := map[string]int{}
	for _, m := range sk.msgs {
		for name, v := range map[string]string{"received": m.ReceivedAt, "sender": m.SenderAddress, "subject": m.Subject,
			"internet_id": m.InternetMessageID, "addressed": m.AddressedToMe, "modified": m.LastModificationTime} {
			if v == "" {
				empty[name]++
			}
		}
	}
	b, _ := json.Marshal(c.Stats())
	t.Logf("SMOKE details failed=%d empty=%v", failed, empty)
	t.Logf("SMOKE stats %s", b)
}

// columnAcceptance asks Outlook for every table column the connector uses,
// one at a time next to the EntryID, on the primary Inbox: a column the
// table refuses fails the whole GetArray (PidTagLocalCommitTime did).
func columnAcceptance(t *testing.T, ctx context.Context, c *Connector, inbox mail.Folder) {
	st := c.folders[folderKey{inbox.StoreID, inbox.Path}]
	var refused []string
	err := c.op(ctx, func(ns Object, s settings) error {
		v, err := ns.Call("GetFolderFromID", st.entryID, st.storeID)
		if err != nil {
			return err
		}
		f := v.(Object)
		defer f.Release()
		for _, col := range fullColumns(true)[1:] {
			cols := []column{idColumns[0], col}
			tbl, err := openTable(f, cols)
			if err == nil {
				err = readRows(tbl, 5, len(cols), binaryEntryID, func([]any) bool { return false })
				tbl.Release()
			}
			if unavailable(err) { // a lost session (or a busy Outlook) is no refused column: stop calling
				return err
			}
			if err != nil {
				refused = append(refused, col.name+" "+Code(err))
			}
		}
		return nil
	})
	t.Logf("SMOKE columns checked=%d refused=%v", len(fullColumns(true))-1, refused)
	if err != nil || len(refused) > 0 {
		t.Fatalf("table columns refused: %v %v", refused, err)
	}
}

type countSink struct {
	snapshot, skips int
	msgs            []mail.Message
}

func (s *countSink) Message(_ context.Context, m mail.Message) error {
	s.msgs = append(s.msgs, m)
	return nil
}
func (s *countSink) Skip(string, error)       { s.skips++ }
func (s *countSink) Progress(source.Progress) {}
func (s *countSink) Snapshot(_ context.Context, items []source.Version, _ bool) (map[string]bool, error) {
	s.snapshot = len(items)
	want := map[string]bool{}
	for _, it := range items {
		want[it.ID] = true
	}
	return want, nil
}
