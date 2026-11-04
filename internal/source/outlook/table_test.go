package outlook

import (
	"errors"
	"reflect"
	"testing"
)

// readRows is the one table pager: it delivers every row of the page set
// once, in order, whether column 0 is a mail table's binary long-term
// EntryID or an address book's text EntryID.
func TestReadRowsDeliversEveryRowForBothEntryIDKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  entryKey
		ids  []any
	}{
		{"mail table", binaryEntryID, []any{[]byte("a"), []byte("b"), []byte("c")}},
		{"contacts table", textEntryID, []any{"a", "b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newFake()
			items := make([]*fakeItem, len(tc.ids))
			for i, id := range tc.ids {
				items[i] = &fakeItem{props: map[string]any{builtinEntryID: id}}
			}
			tbl := o.obj("Table", &fakeTable{cols: []string{builtinEntryID}, items: items})
			defer tbl.Release()
			var got []any
			err := readRows(tbl, 2, 1, tc.key, func(vals []any) bool {
				got = append(got, vals[0])
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.ids) {
				t.Fatalf("rows %v, want %v", got, tc.ids)
			}
			if o.open != 1 { // the table itself, still held by the test
				t.Fatalf("%d objects open", o.open)
			}
		})
	}
}

// The pager's layout check keeps one message per fault and names the kind of
// EntryID the table was asked for, so a swapped kind is not silently read.
func TestReadRowsLayoutErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  entryKey
		val  any
		want string
	}{
		{"a mail table must return the binary id", binaryEntryID, "text-id",
			"outlook: Table.GetArray returned an unexpected layout: column 0 holds string, not the binary EntryID"},
		{"an address book must return the text id", textEntryID, []byte("bin-id"),
			"outlook: Table.GetArray returned an unexpected layout: column 0 holds []uint8, not the text EntryID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newFake()
			tbl := o.obj("Table", &fakeTable{cols: []string{builtinEntryID},
				items: []*fakeItem{{props: map[string]any{builtinEntryID: tc.val}}}})
			defer tbl.Release()
			err := readRows(tbl, 10, 1, tc.key, func([]any) bool { return true })
			if !errors.Is(err, errLayout) {
				t.Fatalf("err %v is not a layout error", err)
			}
			if err.Error() != tc.want {
				t.Fatalf("error text %q, want %q", err, tc.want)
			}
		})
	}
	if err := checkRow([]any{[]byte("a")}, 2, binaryEntryID); err == nil ||
		err.Error() != "outlook: Table.GetArray returned an unexpected layout: a row has 1 values for 2 columns" {
		t.Fatalf("short row error %v", err)
	}
	// An unset EntryID is a value Outlook legitimately leaves empty.
	for _, key := range []entryKey{binaryEntryID, textEntryID} {
		if err := checkRow([]any{nil, "x"}, 2, key); err != nil {
			t.Fatalf("unset EntryID refused: %v", err)
		}
	}
}

// SyncContacts reads through the shared pager with a text EntryID: a row
// without a name or an address is skipped, the rows around it still arrive,
// progress follows the delivered count, and a binary EntryID is a layout
// error, not a contact with an empty id.
func TestSyncContactsUsesTheSharedPager(t *testing.T) {
	o := profile()
	book := &fakeFolder{name: "Contacts", eid: hexID("contacts-primary"), class: "IPF.Contact", itemType: 2}
	book.items = []*fakeItem{
		{props: map[string]any{builtinEntryID: "c1", propDisplayName: "Grace Hopper", propEmail1: "grace@example.test"}},
		{props: map[string]any{builtinEntryID: "c2", propDisplayName: "Ada Lovelace", propEmail1: "ada@example.test"}},
		{props: map[string]any{builtinEntryID: "c3"}}, // no name, no address: an empty entry
		{props: map[string]any{builtinEntryID: "c4", propDisplayName: "Katherine Johnson"}},
	}
	o.stores[1].defaults[10] = book
	o.stores[1].root.children = append(o.stores[1].root.children, book)
	c, _ := testConnector(t, o, map[string]any{"outlook.table.page_rows": 2})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	cols, err := c.ContactCollections(t.Context())
	if err != nil || len(cols) != 1 {
		t.Fatalf("collections %+v %v", cols, err)
	}
	sink := &testContactSink{}
	before := o.open
	if err := c.SyncContacts(t.Context(), cols[0], sink); err != nil {
		t.Fatal(err)
	}
	if o.open != before {
		t.Fatalf("%d COM objects left open by the contact page walk", o.open-before)
	}
	var ids []string
	for _, ct := range sink.list {
		ids = append(ids, ct.EntryID)
	}
	if want := []string{"c1", "c2", "c4"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("contacts %v, want %v", ids, want)
	}
	if n := len(sink.progress); n == 0 || sink.progress[n-1].Done != int64(len(ids)) {
		t.Fatalf("progress %+v", sink.progress)
	}

	// The same address book with a binary EntryID is the mail table's shape:
	// the shared pager reports the swapped kind instead of dropping rows.
	book.items[0].props[builtinEntryID] = []byte("binary")
	err = c.SyncContacts(t.Context(), cols[0], &testContactSink{})
	if !errors.Is(err, errLayout) {
		t.Fatalf("a binary contact EntryID must be a layout error: %v", err)
	}
}
