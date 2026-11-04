package outlook

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// TestMain pins the package tests to UTC by default: their fixtures use fixed
// civil timestamps, so a host zone would shift every emitted calendar instant.
// The tests that exercise another zone set time.Local themselves and restore
// this default.
func TestMain(m *testing.M) {
	old := time.Local
	time.Local = time.UTC
	code := m.Run()
	time.Local = old
	os.Exit(code)
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// tNow is the fake clock's start: weeks after the profile's mail arrived
// (sync.details.settle_after defers mail received moments ago).
var tNow = t0.Add(30 * 24 * time.Hour)

// profile is a primary mailbox (5 inbox, 3 sent) and one online delegate
// mailbox (2 inbox, 2 sent) whose owner the address book resolves.
func profile() *fakeOutlook {
	o := newFake()
	var in, sent, din, dsent []*fakeItem
	for i := range 5 {
		in = append(in, newItem(i, t0.Add(time.Duration(i)*time.Hour)))
	}
	for i := range 3 {
		it := newItem(10+i, t0.Add(time.Duration(i)*time.Hour))
		it.props[propSenderSMTP] = "amelie.durand@example.com"
		sent = append(sent, it)
	}
	for i := range 2 {
		din = append(din, newItem(20+i, t0.Add(time.Duration(i)*time.Hour)))
		it := newItem(30+i, t0.Add(time.Duration(i)*time.Hour))
		it.props[propSenderSMTP] = "amelie.durand@example.com" // the user sent on the mailbox's behalf
		dsent = append(dsent, it)
	}
	primary := mailbox("amelie.durand@example.com", "S-PRIMARY", 0, in, sent)
	primary.isDefault, primary.cached = true, true
	delegate := mailbox("Service Desk", "S-DESK", 1, din, dsent)
	delegate.ownerEID = hexID("desk-owner")
	o.book[hexID("desk-owner")] = fakeEntry{smtp: "support@example.com"}
	o.book["me"] = fakeEntry{smtp: "amelie.durand@example.com", dn: meDN,
		proxies: []string{"smtp:amelie@example.org", "SIP:amelie.durand@example.com", "SMTP:amelie.durand@example.com", "X500:" + oldDN, "SPO:x"}}
	o.currentUser = "me"
	o.stores = []*fakeStore{delegate, primary}
	return o
}

// The profile user's Exchange DNs (current and former).
const (
	meDN  = "/o=Example/ou=Exchange Administrative Group (FYDIBOHF23SPDLT)/cn=Recipients/cn=amelie"
	oldDN = "/o=Example/ou=First Administrative Group/cn=Recipients/cn=adurand"
)

// The keys of the profile's stores.
var (
	primaryKey = source.StoreKey("outlook", "S-PRIMARY")
	deskKey    = source.StoreKey("outlook", "S-DESK")
)

// bothStores picks both stores (the default picks none), unless overrides
// choose the stores themselves.
func bothStores(overrides map[string]any) map[string]any {
	out := map[string]any{source.SettingSelected: []any{primaryKey, deskKey}}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func testConnector(t *testing.T, o *fakeOutlook, overrides map[string]any) (*Connector, *clocktest.Fake) {
	t.Helper()
	clk := fakeClock(o)
	c, err := newConnector("outlook", configtest.New(t, bothStores(overrides)), nil, clk, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		if o.open != 0 {
			t.Errorf("%d COM objects never released", o.open)
		}
		if len(o.misuse) > 0 {
			t.Errorf("misuse of the object model: %v", o.misuse)
		}
	})
	return c, clk
}

// memSink is a source.Sink that keeps everything and wants every id.
type memSink struct {
	msgs     []mail.Message
	skips    int
	versions []source.Version
	complete bool
	known    map[string]string // id → stamp already stored
}

func (s *memSink) Message(_ context.Context, m mail.Message) error {
	s.msgs = append(s.msgs, m)
	return nil
}
func (s *memSink) Skip(string, error)       { s.skips++ }
func (s *memSink) Progress(source.Progress) {}
func (s *memSink) Snapshot(_ context.Context, items []source.Version, complete bool) (map[string]bool, error) {
	s.versions, s.complete = items, complete
	want := map[string]bool{}
	for _, it := range items {
		if s.known[it.ID] != it.Stamp {
			want[it.ID] = true
		}
	}
	return want, nil
}

func folderByRole(t *testing.T, folders []mail.Folder, kind mail.StoreKind, role mail.FolderRole) mail.Folder {
	t.Helper()
	for _, f := range folders {
		if f.StoreKind == kind && f.Role == role {
			return f
		}
	}
	t.Fatalf("no %s %s folder in %+v", kind, role, folders)
	return mail.Folder{}
}

func callsTo(o *fakeOutlook, key string) int {
	n := 0
	for _, c := range o.calls {
		if c == key {
			n++
		}
	}
	return n
}

func refOf(m mail.Message) source.ItemRef { return source.ItemRef{StoreID: m.StoreID, ID: m.ID} }

// fakeClock gives o a fake clock at tNow (every call then takes 1 ms).
func fakeClock(o *fakeOutlook) *clocktest.Fake {
	clk := clocktest.New(tNow)
	o.clock = clk
	return clk
}

// unpaced is a pacing that never waits (guard unit tests).
func unpaced(clk *clocktest.Fake) pacing {
	open := paceLimits{Share: 1, Rate: 1e9}
	return pacing{p: &pacer{clock: clk, idle: func() (time.Duration, error) { return time.Hour, nil }, log: logx.For("test")},
		s: paceSettings{idle: open, active: open, idleAfter: time.Minute}, factor: 1}
}
