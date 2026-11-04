package source

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

type fake struct{ id, kind string }

func (f fake) ID() string                 { return f.id }
func (f fake) Kind() string               { return f.kind }
func (f fake) Capabilities() Capabilities { return Capabilities{} }
func (f fake) Mailboxes() []Mailbox       { return nil }
func (f fake) Discover(context.Context) ([]mail.Folder, error) {
	return nil, nil
}
func (f fake) Sync(context.Context, mail.Folder, Cursor, Sink) (Cursor, error) { return "", nil }

func TestRegistry(t *testing.T) {
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicRegistry, 8)
	r := NewRegistry(bus)
	for _, bad := range []Connector{nil, fake{"", "k"}, fake{"x", ""}} {
		if err := r.Add(bad); err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
	if err := r.Add(fake{"b", "k"}); err != nil {
		t.Fatal(err)
	}
	_ = r.Add(fake{"a", "k"})
	if err := r.Add(fake{"a", "k"}); err == nil {
		t.Fatal("duplicate id must be rejected")
	}
	if l := r.List(); len(l) != 2 || l[0].ID() != "a" {
		t.Fatalf("list %v", l)
	}
	if _, ok := r.Get("b"); !ok || !r.Remove("b") || r.Remove("b") {
		t.Fatal("get/remove")
	}
	if _, ok := r.Get("b"); ok {
		t.Fatal("removed connector still found")
	}
	if a, b, c := <-sub.C, <-sub.C, <-sub.C; a.ID != "b" || b.ID != "a" || c.Action != "remove" || len(sub.C) != 0 {
		t.Fatalf("events %+v %+v %+v", a, b, c)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry(nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			id := string(rune('a' + i%26))
			_ = r.Add(fake{id, "k"})
			r.Get(id)
			r.List()
			r.Remove(id)
		})
	}
	wg.Wait()
}

func TestCapabilitiesByName(t *testing.T) {
	c := Capabilities{Headers: true, ReadState: true}
	for name, want := range map[string]bool{"headers": true, "read_state": true, "attachments": false, "last_verb": false} {
		if got, err := c.Has(name); err != nil || got != want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	if _, err := c.Has("telepathy"); err == nil {
		t.Error("an unknown capability must be an error")
	}
	names := CapabilityNames()
	if len(names) != 18 || names[0] != "addressed_to_me" || !slices.Contains(names, "contacts") || !slices.Contains(names, "calendar") {
		t.Errorf("names: %v", names)
	}
}
