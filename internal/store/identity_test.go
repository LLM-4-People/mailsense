package store

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Reported addresses are stored as the source gave them, replaced only
// as a whole; declared ones come from the configuration (review L0 rules
// §1.9: one place each, joined in Owners); the identity normalizes both.
func TestOwnersJoinReportedAndDeclared(t *testing.T) {
	k1, k2 := source.StoreKey("src", "S1"), source.StoreKey("src", "S2")
	s, cfg := open(t, map[string]any{source.SettingDeclared: []any{k1 + "=amelie@example.org", k1 + "=Amelie.Durand@example.com",
		k2 + "=李娜@例子.中国"}})
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "S1"}
	if err := s.SaveReported(ctx, ref, []string{"Amelie.Durand@example.com", "/o=x/cn=amelie"}); err != nil {
		t.Fatal(err)
	}
	owners, err := s.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o1 := owners[k1]
	if !slices.Equal(o1.Reported, []string{"Amelie.Durand@example.com", "/o=x/cn=amelie"}) || len(o1.Declared) != 2 ||
		!slices.Equal(o1.Identity.Addresses, []string{"amelie.durand@example.com", "/o=x/cn=amelie", "amelie@example.org"}) ||
		!o1.Identity.Owns("amelie+x@example.org") {
		t.Fatalf("store 1: %+v", o1)
	}
	if o2 := owners[k2]; len(o2.Reported) != 0 || !o2.Identity.Owns("李娜@xn--fsqu00a.xn--fiqs8s") {
		t.Fatalf("store 2 (declared only, IDN): %+v", o2)
	}
	// A declaration removed from the settings is gone at once.
	if err := cfg.Set(source.SettingDeclared, []any{}); err != nil {
		t.Fatal(err)
	}
	if owners, _ = s.Owners(ctx); len(owners[k1].Declared) != 0 || len(owners) != 1 {
		t.Fatalf("after undeclaring: %+v", owners)
	}
	if err := s.SaveReported(ctx, ref, nil); err != nil {
		t.Fatal(err)
	}
	if owners, _ = s.Owners(ctx); len(owners) != 0 {
		t.Fatalf("a complete read reporting nothing replaces the identity: %+v", owners)
	}
}

func TestOwnerIdentityNamesComeFromSentMail(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	m := sent("owner-name", "2026-10-20T09:00:00Z",
		mail.Recipient{Kind: mail.To, Name: "Client", Address: "client@example.test"})
	m.SenderName, m.SenderAddress = "Amélie Durand", mailtest.Owner
	id := answerMessage(t, s, m)
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonSentByOwner)

	owner, err := s.OwnerIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.Names) != 1 || owner.Names[0] != "Amélie Durand" {
		t.Fatalf("owner names %+v, want the stored sent signature", owner.Names)
	}
	if len(owner.Addresses) != 1 || !strings.EqualFold(owner.Addresses[0], mailtest.Owner) {
		t.Fatalf("owner addresses %+v", owner.Addresses)
	}
}

// The picking screen's one query: every discovered mailbox with its
// selection and identity, then picked keys no discovery has seen.
func TestMailboxViewsAndPersistence(t *testing.T) {
	k1, k2, gone := source.StoreKey("src", "S1"), source.StoreKey("src", "S2"), source.StoreKey("src", "S9")
	s, _ := open(t, map[string]any{source.SettingSelected: []any{k1, gone}, source.SettingDeclared: []any{k2 + "=desk@example.org"}})
	ctx := t.Context()
	_ = s.UpsertSource(ctx, "src", "outlook", "src", source.Capabilities{OwnerIdentity: true, MultiFolder: true})
	boxes := []source.Mailbox{{Key: k1, SourceID: "src", StoreID: "S1", Name: "Amélie", Kind: mail.StorePrimary,
		Default: true, Cached: true, Folders: 3, Items: 9, IdentityComplete: true}, {Key: k2, SourceID: "src", StoreID: "S2",
		Name: "Archive", Kind: mail.StoreArchive, Folders: -1, Items: -1, Error: "0x80040115"}}
	if err := s.SaveMailboxes(ctx, "src", boxes); err != nil {
		t.Fatal(err)
	}
	_ = s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "S1"}, []string{"amelie@example.com"})
	views, err := s.MailboxViews(ctx)
	if err != nil || len(views) != 3 {
		t.Fatalf("%v %+v", err, views)
	}
	want0 := boxes[0]
	want0.Addresses = []string{"amelie@example.com"}
	if !reflect.DeepEqual(views[0].Mailbox, want0) || !views[0].Selected || !views[0].Discovered || views[0].Identity.Primary() != "amelie@example.com" {
		t.Fatalf("picked: %+v", views[0])
	}
	if !reflect.DeepEqual(views[1].Mailbox, boxes[1]) || views[1].Selected || views[1].Declared[0] != "desk@example.org" {
		t.Fatalf("unpicked, declared: %+v", views[1])
	}
	if v := views[2]; v.Key != gone || v.Discovered || !v.Selected {
		t.Fatalf("picked, never discovered: %+v", v)
	}
	caps, _ := s.SourceCapabilities(ctx)
	if !caps["src"].OwnerIdentity || !caps["src"].MultiFolder || caps["src"].Headers {
		t.Fatalf("capabilities %+v", caps)
	}
	// Misuse: another source's mailbox, a wrong key, an unknown kind.
	for _, bad := range []source.Mailbox{{Key: source.StoreKey("x", "S1"), SourceID: "x", StoreID: "S1", Kind: mail.StorePrimary},
		{Key: "store-000000000000", SourceID: "src", StoreID: "S1", Kind: mail.StorePrimary},
		{Key: k1, SourceID: "src", StoreID: "S1", Kind: "file"}} {
		if err := s.SaveMailboxes(ctx, "src", []source.Mailbox{bad}); err == nil {
			t.Errorf("%+v must be refused", bad)
		}
	}
	if views, _ := s.MailboxViews(ctx); len(views) != 3 {
		t.Fatal("a refused list keeps the previous one")
	}
}
