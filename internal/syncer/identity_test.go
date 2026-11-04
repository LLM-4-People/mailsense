package syncer

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// errOf drops the mailbox views of a selection or identity write, for tests
// that only care whether the change was refused.
func errOf(_ []store.MailboxView, err error) error { return err }

// Review L0 bugs #1 / rules §1.1 (failed first: identities [], verdicts
// {"":5}, ErrNothingPicked): a picked store that goes offline, or whose
// identity is read only in part, keeps its recorded identity and its
// verdicts, and the run says it waits for the store.
func TestAnOfflineOrPartlyReadStoreKeepsItsIdentityAndVerdicts(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i"), "Sent": items(2, sent, "s")}}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := st.VerdictCounts(t.Context())
	if before[scope.Owner] != 5 {
		t.Fatalf("online: %v", before)
	}
	for _, step := range []struct {
		name             string
		offline, partial bool
	}{{"offline", true, false}, {"partly read", false, true}, {"partly, again", false, true}} {
		name := step.name
		c.offline, c.partial = step.offline, step.partial
		c.version++
		rep, err := s.Run(t.Context(), c, nil)
		owners, _ := st.Owners(t.Context())
		after, _ := st.VerdictCounts(t.Context())
		if owners[fakeKey].Identity.Primary() != mailtest.Owner || after[scope.Owner] != 5 || after[""] != 0 {
			t.Fatalf("%s: identity %+v, verdicts %v", name, owners[fakeKey], after)
		}
		if c.offline && (!errors.Is(err, ErrMailboxUnreadable) || rep.Waiting != 1) {
			t.Fatalf("%s: the run waits for the store: %v %+v", name, err, rep)
		}
		if !c.offline && (err != nil || rep.IdentityKept != 1) {
			t.Fatalf("%s: %v %+v", name, err, rep)
		}
	}
	// A complete read replaces the identity again.
	c.partial = false
	c.version++
	if rep, err := s.Run(t.Context(), c, nil); err != nil || rep.IdentityKept != 0 {
		t.Fatalf("complete: %v %+v", err, rep)
	}
}

// The selection and identity actions check their keys against the
// discovered mailboxes; misuse is refused without changing anything.
func TestSelectionAndIdentityActions(t *testing.T) {
	s, st, bus, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := &fakeConn{id: "fake", noOwner: true, items: map[string][]item{"Inbox": items(2, inbox, "i")}}
	if _, err := s.Discover(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	views, _ := events.Subscribe(bus, TopicMailboxes, 8)
	for name, err := range map[string]error{
		"empty pick":        errOf(s.Pick(ctx)),
		"never discovered":  errOf(s.Pick(ctx, source.StoreKey("fake", "nope"))),
		"not an address":    errOf(s.Declare(ctx, fakeKey, "not an address")),
		"two addresses":     errOf(s.Declare(ctx, fakeKey, "a@example.org, b@example.org")),
		"declare elsewhere": errOf(s.Declare(ctx, source.StoreKey("fake", "nope"), "a@example.org")),
		"empty unpick":      errOf(s.Unpick(ctx)),
	} {
		if err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if len(cfg.Strings(source.SettingSelected)) != 0 || len(cfg.Strings(source.SettingDeclared)) != 0 {
		t.Fatal("refused actions change nothing")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(errOf(s.Pick(ctx, fakeKey, fakeKey)))
	must(errOf(s.Pick(ctx, fakeKey)))
	if got := cfg.Strings(source.SettingSelected); len(got) != 1 {
		t.Fatalf("duplicates are no-ops: %v", got)
	}
	must(errOf(s.Declare(ctx, fakeKey, "Amelie.Durand@example.com")))
	must(errOf(s.Declare(ctx, fakeKey, "amelie.durand@example.com")))
	must(errOf(s.Declare(ctx, fakeKey, "李娜@例子.中国")))
	box, _ := s.Mailboxes(ctx)
	if len(box) != 1 || !box[0].Selected || len(box[0].Declared) != 2 || box[0].Identity.Primary() != "amelie.durand@example.com" {
		t.Fatalf("view: %+v", box)
	}
	if v := <-views.C; len(v) != 1 {
		t.Fatalf("every change publishes the views: %+v", v)
	}
	must(errOf(s.Undeclare(ctx, fakeKey, "AMELIE.DURAND@example.com")))
	must(errOf(s.Undeclare(ctx, fakeKey, "nobody@example.org")))
	must(errOf(s.Unpick(ctx, fakeKey)))
	must(errOf(s.Unpick(ctx, fakeKey))) // twice: a no-op
	if len(cfg.Strings(source.SettingSelected)) != 0 || len(cfg.Strings(source.SettingDeclared)) != 1 {
		t.Fatalf("after undo: %v %v", cfg.Strings(source.SettingSelected), cfg.Strings(source.SettingDeclared))
	}
	st2, _ := MailboxesStage(cfg, st).Status(ctx)
	if st2.State != learning.Blocked || st2.Blockers[0].Actions[0].ID != learning.ActionPickMailboxes {
		t.Fatalf("nothing picked: %+v", st2)
	}
}
