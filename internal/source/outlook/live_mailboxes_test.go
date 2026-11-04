//go:build windows && outlooklive

package outlook

// TestLiveMailboxes verifies the explicit mailbox selection, the reported
// owner identity and the learning gate against the real profile (user
// decision 2026-10-15), read-only and in about a minute: every store is
// listed with none picked; a run with nothing picked reads no message;
// picking the default store records its owner identity (COUNTS only:
// primary SMTP, SMTP aliases, Exchange DNs); the learning gate stays
// closed. The report holds counts, kinds, flags and hashes only.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

type mailboxSummary struct {
	Kind             mail.StoreKind `json:"kind"`
	KeyHash          string         `json:"key_hash"`
	Default          bool           `json:"default"`
	Cached           bool           `json:"cached"`
	Selected         bool           `json:"selected"`
	Folders          int            `json:"folders"`
	Items            int64          `json:"items"`
	Online           bool           `json:"online"`
	Addresses        int            `json:"addresses"` // reported owner addresses (picked stores only)
	IdentityComplete bool           `json:"identity_complete"`
}

type identitySummary struct {
	Addresses int  `json:"addresses"`
	Primary   bool `json:"primary_smtp"`
	SMTP      int  `json:"smtp_aliases"`
	DNs       int  `json:"exchange_dns"`
}

type mailboxesReport struct {
	Started         string              `json:"started"`
	Stores          int                 `json:"stores"`
	BeforePick      []mailboxSummary    `json:"before_pick"`
	NothingPicked   bool                `json:"nothing_picked_error"`
	MessagesBefore  int                 `json:"messages_before_pick"`
	ItemReadsBefore int64               `json:"item_and_table_reads_before_pick"`
	AfterPick       []mailboxSummary    `json:"after_pick"`
	Identity        identitySummary     `json:"identity"`
	MessagesAfter   int                 `json:"messages_after_pick"` // discovery only: still none
	Learning        map[string]string   `json:"learning_stages"`
	Blockers        map[string][]string `json:"blockers"` // stage → codes
	GateOpen        bool                `json:"gate_open"`
	DiscoverSeconds float64             `json:"discover_seconds"`
}

func TestLiveMailboxes(t *testing.T) {
	ctx := liveContext(t)
	dir, err := filepath.Abs("live-mailboxes")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	policy := fsx.Replace{Attempts: cfg.Int("fs.replace_attempts"), Delay: cfg.Duration("fs.replace_retry_delay")}
	_ = fsx.RemoveAll(dir, policy)
	defer func() {
		if err := fsx.RemoveAll(dir, policy); err != nil {
			t.Errorf("LIVE could not remove the temporary database: %v", err)
		}
	}()
	bus := events.New()
	st, err := store.Open(ctx, cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := New("outlook", cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := syncer.New(st, cfg, bus, nil)
	livetest.Limit(t, cfg) // before the first call: -live.rate, unpicked stores listed but not walked
	rep := mailboxesReport{Started: time.Now().UTC().Format(time.RFC3339), Learning: map[string]string{}, Blockers: map[string][]string{}}

	// 1. Discover with nothing picked: every store listed, none picked.
	start := time.Now()
	boxes, err := s.Discover(ctx, c)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	rep.DiscoverSeconds = time.Since(start).Seconds()
	rep.Stores, rep.BeforePick = len(boxes), summaries(boxes)
	for _, b := range boxes {
		if b.Selected {
			t.Errorf("LIVE a store is preselected: kind %s", b.Kind)
		}
	}
	// 2. A run with nothing picked reads no message.
	reads := callsOf(c, "Folder.GetTable") + callsOf(c, "Namespace.GetItemFromID")
	_, err = s.Run(ctx, c, nil)
	rep.NothingPicked = errors.Is(err, syncer.ErrNothingPicked)
	rep.ItemReadsBefore = callsOf(c, "Folder.GetTable") + callsOf(c, "Namespace.GetItemFromID") - reads
	rep.MessagesBefore = storedMessages(t, st)

	// 3. Pick the user's own (default) store: its identity is recorded.
	livetest.Pick(t, cfg, c.Mailboxes(), *livePick)
	if boxes, err = s.Discover(ctx, c); err != nil {
		t.Fatalf("discover after pick: %v", err)
	}
	rep.AfterPick = summaries(boxes)
	owners, _ := st.Owners(ctx)
	for _, o := range owners {
		addrs := o.Identity.Addresses
		rep.Identity.Addresses += len(addrs)
		for i, a := range addrs {
			switch {
			case i == 0 && !mail.IsLegacyDN(a):
				rep.Identity.Primary = true
			case mail.IsLegacyDN(a):
				rep.Identity.DNs++
			default:
				rep.Identity.SMTP++
			}
		}
	}
	rep.MessagesAfter = storedMessages(t, st)

	// 4. The learning gate stays closed (nothing synced, stages pending).
	m, err := learning.New(func() string { return source.Scope(cfg.Snapshot()) }, st, bus, nil,
		syncer.MailboxesStage(cfg, st), learning.MachineStage(cfg, nil), syncer.SyncStage(cfg, st),
		learning.NotBuilt(learning.StageEvidence, "evidence engine"))
	if err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stg := range status.Stages {
		rep.Learning[stg.ID] = string(stg.State)
		for _, b := range stg.Blockers {
			rep.Blockers[stg.ID] = append(rep.Blockers[stg.ID], b.Code)
		}
	}
	rep.GateOpen = m.Allow(ctx) == nil
	t.Logf("LIVE mailboxes stores=%d preselected=0 nothing_picked=%v messages_before=%d reads_before=%d identity=%+v messages_after=%d gate_open=%v learning=%v",
		rep.Stores, rep.NothingPicked, rep.MessagesBefore, rep.ItemReadsBefore, rep.Identity, rep.MessagesAfter, rep.GateOpen, rep.Learning)
	if !rep.NothingPicked || rep.MessagesBefore != 0 || rep.ItemReadsBefore != 0 || rep.MessagesAfter != 0 || rep.GateOpen || rep.Identity.Addresses == 0 {
		t.Errorf("LIVE expectations failed: %+v", rep)
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := fsx.WriteFileAtomic(*liveReport, b, fsx.FilePerm, policy); err != nil {
		t.Fatal(err)
	}
}

func summaries(boxes []store.MailboxView) []mailboxSummary {
	var out []mailboxSummary
	for _, b := range boxes {
		out = append(out, mailboxSummary{Kind: b.Kind, KeyHash: hash(b.Key), Default: b.Default, Cached: b.Cached, Selected: b.Selected,
			Folders: b.Folders, Items: b.Items, Online: b.Online(), Addresses: len(b.Addresses), IdentityComplete: b.IdentityComplete})
	}
	return out
}

func callsOf(c *Connector, member string) int64 { return c.count.snapshot()[member].Calls }

func storedMessages(t *testing.T, st *store.Store) int {
	counts, err := st.CountByRole(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, v := range counts {
		n += v
	}
	return n
}
