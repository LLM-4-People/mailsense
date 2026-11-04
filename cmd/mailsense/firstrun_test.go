package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/onboarding"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// firstRunConn is a deterministic source for the first-open and pick wiring
// tests: one primary store whose owner is reported, no messages.
type firstRunConn struct {
	id string
	mu sync.Mutex
	// discovers counts Discover calls.
	discovers int
}

func (c *firstRunConn) ID() string   { return c.id }
func (c *firstRunConn) Kind() string { return "fake" }
func (c *firstRunConn) Capabilities() source.Capabilities {
	return source.Capabilities{Incremental: true, OwnerIdentity: true}
}

func (c *firstRunConn) Discover(context.Context) ([]mail.Folder, error) {
	c.mu.Lock()
	c.discovers++
	c.mu.Unlock()
	return []mail.Folder{{SourceID: c.id, StoreID: "store-1", StoreName: "Owner", StoreKind: mail.StorePrimary,
		Path: "Inbox", Role: mail.RoleInbox, ItemCount: 0}}, nil
}

func (c *firstRunConn) Mailboxes() []source.Mailbox {
	return []source.Mailbox{{Key: source.StoreKey(c.id, "store-1"), SourceID: c.id, StoreID: "store-1",
		Name: "Owner", Kind: mail.StorePrimary, Default: true, Folders: 1, Items: 0,
		Addresses: []string{"owner@example.com"}, IdentityComplete: true}}
}

func (c *firstRunConn) Sync(context.Context, mail.Folder, source.Cursor, source.Sink) (source.Cursor, error) {
	return "", nil
}

func (c *firstRunConn) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.discovers
}

func firstRunWorld(t *testing.T, overrides map[string]any) (*store.Store, *config.Config, *syncer.Syncer) {
	t.Helper()
	cfg := configtest.New(t, overrides)
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, cfg, syncer.New(st, cfg, nil, nil)
}

func TestRunSeedsTheLocalEmbedderOnFirstStart(t *testing.T) {
	check, err := sidecar.LoadCatalog(configtest.New(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := check.LocalEmbedder(sidecar.MachinePlatform()); !ok {
		t.Skip("no pinned local embedder for this platform")
	}
	dir := t.TempDir()
	shutdown := newShutdownRequest()
	if err := shutdown.Request(t.Context()); err != nil {
		t.Fatal(err)
	}
	var r reports
	if code := runWithShutdown([]string{"MAILSENSE_DATA_DIR=" + dir}, runtime.GOOS, "", r.report, shutdown); code != 0 || r.err != nil {
		t.Fatalf("exit %d, report %v", code, r.err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "providers.json"))
	if err != nil {
		t.Fatalf("the local embedder provider file was not written: %v", err)
	}
	var records map[string]json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	if _, ok := records[onboarding.LocalProviderID]; !ok {
		t.Fatalf("provider file lacks %s: %s", onboarding.LocalProviderID, data)
	}
	settings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(settings, &values); err != nil {
		t.Fatal(err)
	}
	if values["embed.provider"] != onboarding.LocalProviderID {
		t.Fatalf("embed.provider = %v", values["embed.provider"])
	}
}

func TestRunStartsWithAMalformedProviderFile(t *testing.T) {
	dir := t.TempDir()
	const junk = `{"local-embedder":`
	path := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(path, []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	shutdown := newShutdownRequest()
	if err := shutdown.Request(t.Context()); err != nil {
		t.Fatal(err)
	}
	var r reports
	if code := runWithShutdown([]string{"MAILSENSE_DATA_DIR=" + dir}, runtime.GOOS, "", r.report, shutdown); code != 0 || r.err != nil {
		t.Fatalf("exit %d, report %v", code, r.err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != junk {
		t.Fatalf("the malformed provider file was changed: %q", data)
	}
}

func TestFirstOpenDiscoveryRunsOnceOnAnUntouchedInstall(t *testing.T) {
	ctx := t.Context()
	st, cfg, s := firstRunWorld(t, nil)
	c := &firstRunConn{id: "outlook"}
	if err := firstOpenDiscovery(ctx, st, cfg, s, []source.Connector{c}); err != nil {
		t.Fatal(err)
	}
	if c.calls() != 1 {
		t.Fatalf("Discover calls = %d", c.calls())
	}
	views, err := st.MailboxViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Selected || views[0].Key != source.StoreKey("outlook", "store-1") {
		t.Fatalf("mailboxes after first open: %+v", views)
	}
	if err := firstOpenDiscovery(ctx, st, cfg, s, []source.Connector{c}); err != nil {
		t.Fatal(err)
	}
	if c.calls() != 1 {
		t.Fatalf("a second call rediscovered the source: %d", c.calls())
	}
}

func TestFirstOpenDiscoveryIsANoOpWithASelection(t *testing.T) {
	ctx := t.Context()
	st, cfg, s := firstRunWorld(t, map[string]any{source.SettingSelected: []any{source.StoreKey("outlook", "store-1")}})
	c := &firstRunConn{id: "outlook"}
	if err := firstOpenDiscovery(ctx, st, cfg, s, []source.Connector{c}); err != nil {
		t.Fatal(err)
	}
	if c.calls() != 0 {
		t.Fatalf("a selected mailbox still triggered discovery: %d", c.calls())
	}
}

func TestFirstOpenDiscoveryIsANoOpWithAnExistingMailbox(t *testing.T) {
	ctx := t.Context()
	st, cfg, s := firstRunWorld(t, nil)
	c := &firstRunConn{id: "outlook"}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := firstOpenDiscovery(ctx, st, cfg, s, []source.Connector{c}); err != nil {
		t.Fatal(err)
	}
	if c.calls() != 1 {
		t.Fatalf("an existing mailbox still triggered discovery: %d", c.calls())
	}
}

// stop must cancel the discovery and wait for the pass, so a first-open
// SaveMailboxes can never run while the store is closing underneath it.
func TestFirstOpenDiscoveryIsJoinedOnShutdown(t *testing.T) {
	original := firstOpenRun
	t.Cleanup(func() { firstOpenRun = original })
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	firstOpenRun = func(ctx context.Context, _ *store.Store, _ *config.Config, _ *syncer.Syncer, _ []source.Connector) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}
	shutdown := newShutdownRequest()
	ready := make(chan struct{})
	stop := startFirstOpenDiscovery(&app{registry: source.NewRegistry(nil)}, shutdown, ready)
	close(ready)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first-open discovery never started")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel the discovery context")
	}
	select {
	case <-stopped:
		t.Fatal("stop returned while the discovery pass was still running")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the discovery pass finished")
	}
}

func TestPickStartsTheFirstSyncAndRecordsTheOwner(t *testing.T) {
	ctx := t.Context()
	st, cfg, s := firstRunWorld(t, nil)
	c := &firstRunConn{id: "outlook"}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	reg := source.NewRegistry(nil)
	if err := reg.Add(c); err != nil {
		t.Fatal(err)
	}
	jm := jobs.NewManager(cfg, nil, nil)
	api := mailboxesAPI{w: &wire{app: &app{cfg: cfg, st: st, syncer: s, jobs: jm, registry: reg}}}
	if _, err := api.Pick(ctx, []string{source.StoreKey("outlook", "store-1")}); err != nil {
		t.Fatal(err)
	}
	list := jm.List()
	if len(list) != 1 || list[0].Kind != syncer.JobKind || list[0].Name != "outlook" {
		t.Fatalf("jobs after pick: %+v", list)
	}
	j, ok := jm.Get(list[0].ID)
	if !ok {
		t.Fatal("job missing")
	}
	select {
	case <-j.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the first sync did not finish")
	}
	owners, err := st.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := owners[source.StoreKey("outlook", "store-1")]
	if len(owner.Reported) != 1 || owner.Reported[0] != "owner@example.com" {
		t.Fatalf("owner identity after the first sync: %+v", owner)
	}
}
