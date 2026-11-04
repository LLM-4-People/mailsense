package source

import (
	"context"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// starterFake implements only the optional interface named by its type.
type starterFake struct{ fake }

func (starterFake) StartOutlook(context.Context) error { return nil }

type resumerFake struct{ fake }

func (resumerFake) Resume() error { return nil }

type detailFake struct{ fake }

func (detailFake) ReadDetails(context.Context, ItemRef) (mail.Details, error) {
	return mail.Details{}, nil
}

// allActionsFake implements every optional owner action.
type allActionsFake struct{ fake }

func (allActionsFake) StartOutlook(context.Context) error { return nil }
func (allActionsFake) Resume() error                      { return nil }
func (allActionsFake) ReadDetails(context.Context, ItemRef) (mail.Details, error) {
	return mail.Details{}, nil
}

// The capability list is the struct's own declaration order and names: one
// vocabulary, no second hand-written order to drift.
func TestCapabilitiesListKeepsTheDeclarationOrder(t *testing.T) {
	c := Capabilities{Calendar: true, Bodies: true, Incremental: true, ReadState: true}
	want := CapabilityList{"incremental", "bodies", "read_state", "calendar"}
	got := c.List()
	if !slices.Equal(got, want) {
		t.Fatalf("list %v, want %v", got, want)
	}
	empty := (Capabilities{}).List()
	if empty == nil || len(empty) != 0 {
		t.Fatalf("an empty capability set must be an empty list, got %#v", empty)
	}
	if len(Capabilities{}.List()) != 0 {
		t.Fatal("zero capabilities advertised")
	}
	all := Capabilities{Incremental: true, Bodies: true, Recipients: true, Deletes: true, LiveEvents: true,
		OwnerIdentity: true, AddressedToMe: true, FolderRoles: true, MessageClass: true, Headers: true,
		MultiFolder: true, Attachments: true, ReadState: true, Flags: true, Categories: true, LastVerb: true,
		Contacts: true, Calendar: true}
	if len(all.List()) != len(CapabilityNames()) {
		t.Fatalf("every capability advertised: %v, names %v", all.List(), CapabilityNames())
	}
	for _, name := range all.List() {
		if !slices.Contains(CapabilityNames(), name) {
			t.Fatalf("list advertises %q, not a capability name", name)
		}
	}
}

// The owner-action table is the one support rule: the advertised actions of a
// connector are exactly the optional interfaces it implements, in one order.
func TestOwnerActionsMatchTheOptionalInterfaces(t *testing.T) {
	wantAll := []OwnerAction{ActionStart, ActionResume, ActionRetryDetails}
	if got := OwnerActions(); !slices.Equal(got, wantAll) {
		t.Fatalf("owner actions %v, want %v", got, wantAll)
	}
	cases := []struct {
		name string
		c    Connector
		want []OwnerAction
	}{
		{"none", fake{"plain", "k"}, []OwnerAction{}},
		{"start", starterFake{fake{"s", "k"}}, []OwnerAction{ActionStart}},
		{"resume", resumerFake{fake{"r", "k"}}, []OwnerAction{ActionResume}},
		{"retry-details", detailFake{fake{"d", "k"}}, []OwnerAction{ActionRetryDetails}},
		{"all", allActionsFake{fake{"outlook", "outlook"}}, wantAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConnectorActions(tc.c)
			if got == nil || !slices.Equal(got, tc.want) {
				t.Fatalf("actions %v, want %v", got, tc.want)
			}
			for _, action := range wantAll {
				want := slices.Contains(tc.want, action)
				if got := ImplementsAction(tc.c, action); got != want {
					t.Fatalf("%s: implements %v, want %v", action, got, want)
				}
			}
			if ImplementsAction(tc.c, "telepathy") {
				t.Fatal("an unknown action must not be implemented")
			}
		})
	}
	if actions := ConnectorActions(nil); actions == nil || len(actions) != 0 {
		t.Fatalf("a nil connector owns no action: %#v", actions)
	}
}
