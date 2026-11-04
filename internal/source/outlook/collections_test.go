package outlook

import (
	"errors"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// eachPickedDefaultFolder is the one walk both collection families use: it
// visits every picked store that has the kind's default folder, in store
// order, skips the stores Outlook refuses, releases every handle it opened,
// and hands the folder to the callback before releasing it.
func TestEachPickedDefaultFolderVisitsEveryPickedStore(t *testing.T) {
	o := profile()
	deskCal := &fakeFolder{name: "Calendrier", eid: hexID("desk-cal"), class: "IPF.Appointment"}
	cal := &fakeFolder{name: "Calendrier", eid: hexID("cal"), class: "IPF.Appointment"}
	book := &fakeFolder{name: "Contacts", eid: hexID("book"), class: "IPF.Contact", itemType: 2}
	o.stores[0].defaults[9] = deskCal // the delegate store has a calendar only
	o.stores[1].defaults[9] = cal
	o.stores[1].defaults[10] = book
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind collectionKind
		want []string
	}{
		{calendarCollections, []string{"S-DESK=" + deskCal.eid, "S-PRIMARY=" + cal.eid}},
		// No default address book in the delegate store: Outlook answers
		// MAPI_E_NOT_FOUND, the walk skips it and keeps the primary's.
		{contactCollections, []string{"S-PRIMARY=" + book.eid}},
	} {
		t.Run(tc.kind.label, func(t *testing.T) {
			var got []string
			before := o.open
			err := c.op(t.Context(), func(ns Object, _ settings) error {
				return c.eachPickedDefaultFolder(t.Context(), ns, tc.kind, func(st StoreInfo, folder Object) error {
					id, err := folder.Get("EntryID")
					if err != nil {
						return err
					}
					got = append(got, st.StoreID+"="+asString(id))
					return nil
				})
			})
			if err != nil {
				t.Fatalf("%s: %v", tc.kind.label, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("%s: visited %v, want %v", tc.kind.label, got, tc.want)
			}
			if o.open != before {
				t.Fatalf("%s: %d COM objects left open by the walk", tc.kind.label, o.open-before)
			}
		})
	}
}

// A lost Outlook stops the walk, returns the call's own error, and still
// releases the folder it had opened.
func TestEachPickedDefaultFolderStopsWhenOutlookIsUnavailable(t *testing.T) {
	o := profile()
	o.stores[0].defaults[9] = &fakeFolder{name: "Calendrier", eid: hexID("desk-cal"), class: "IPF.Appointment"}
	o.stores[1].defaults[9] = &fakeFolder{name: "Calendrier", eid: hexID("cal"), class: "IPF.Appointment"}
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.onCall = func(key string) {
		if key == "Folder.EntryID" {
			o.lost = true
		}
	}
	var visited []string
	err := c.op(t.Context(), func(ns Object, _ settings) error {
		return c.eachPickedDefaultFolder(t.Context(), ns, calendarCollections, func(st StoreInfo, folder Object) error {
			visited = append(visited, st.StoreID)
			_, err := folder.Get("EntryID") // Outlook answers RPC_E_DISCONNECTED from here on
			return err
		})
	})
	if !errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("a lost session must stop the walk: %v", err)
	}
	if hr, ok := HResult(err); !ok || hr != hrDisconnected {
		t.Fatalf("the call's error must reach the caller unchanged: %v", err)
	}
	if !slices.Equal(visited, []string{"S-DESK"}) {
		t.Fatalf("the walk must stop, not skip: visited %v", visited)
	}
}

// A folder the callback cannot read skips that store only: the next picked
// store is still walked and every handle still released.
func TestEachPickedDefaultFolderSkipsAFolderItCannotRead(t *testing.T) {
	o := profile()
	broken := &fakeFolder{name: "Calendrier", eid: hexID("desk-cal"), class: "IPF.Appointment", failEID: hrFail}
	ok := &fakeFolder{name: "Calendrier", eid: hexID("cal"), class: "IPF.Appointment"}
	o.stores[0].defaults[9] = broken
	o.stores[1].defaults[9] = ok
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []string
	before := o.open
	err := c.op(t.Context(), func(ns Object, _ settings) error {
		return c.eachPickedDefaultFolder(t.Context(), ns, calendarCollections, func(st StoreInfo, folder Object) error {
			id, err := folder.Get("EntryID")
			if err != nil {
				return err
			}
			got = append(got, st.StoreID+"="+asString(id))
			return nil
		})
	})
	if err != nil {
		t.Fatalf("one unreadable folder must not fail the walk: %v", err)
	}
	if !slices.Equal(got, []string{"S-PRIMARY=" + ok.eid}) {
		t.Fatalf("visited %v, want only the readable folder", got)
	}
	if o.open != before {
		t.Fatalf("%d COM objects left open by the walk", o.open-before)
	}
}
