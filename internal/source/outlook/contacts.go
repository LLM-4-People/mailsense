package outlook

import (
	"context"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// ContactCollections lists the address books of the picked stores: each
// store's default Contacts folder and its immediate subfolders that hold
// contact items. Folder names are never matched; the item class decides.
func (c *Connector) ContactCollections(ctx context.Context) ([]source.ContactCollection, error) {
	var out []source.ContactCollection
	err := c.op(ctx, func(ns Object, _ settings) error {
		return c.eachPickedDefaultFolder(ctx, ns, contactCollections, func(st StoreInfo, folder Object) error {
			col, err := c.contactCollection(st, folder, true)
			if err != nil {
				return err
			}
			if col != nil {
				out = append(out, *col)
			}
			children, err := c.contactChildren(st, folder)
			if err != nil {
				return err
			}
			out = append(out, children...)
			return nil
		})
	})
	return out, err
}

// contactChildren lists the contact subfolders of a Contacts folder. The
// walk is one level deep: nested address books are rare and the default
// folder already covers the normal case.
func (c *Connector) contactChildren(st StoreInfo, parent Object) ([]source.ContactCollection, error) {
	v, err := parent.Get("Folders")
	if err != nil {
		return nil, err
	}
	folders := v.(Object)
	defer folders.Release()
	var out []source.ContactCollection
	cur, err := folders.Call("GetFirst")
	if err != nil {
		return nil, err
	}
	for cur != nil {
		item := cur
		f := item.(Object)
		kind, err := f.Get("DefaultItemType")
		if err != nil {
			f.Release()
			continue
		}
		if asInt(kind).N != itemTypeContact {
			f.Release()
			continue
		}
		col, err := c.contactCollection(st, f, false)
		f.Release()
		if err != nil {
			return nil, err
		}
		if col != nil {
			out = append(out, *col)
		}
		cur, err = folders.Call("GetNext")
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// contactCollection reads a folder's identity, or nil when the folder does
// not hold contact items.
func (c *Connector) contactCollection(st StoreInfo, folder Object, isDefault bool) (*source.ContactCollection, error) {
	if !isDefault {
		kind, err := folder.Get("DefaultItemType")
		if err != nil {
			return nil, err
		}
		if asInt(kind).N != itemTypeContact {
			return nil, nil
		}
	}
	entry, err := folder.Get("EntryID")
	if err != nil {
		return nil, err
	}
	name, err := folder.Get("Name")
	if err != nil {
		return nil, err
	}
	col := source.ContactCollection{SourceID: c.id, StoreID: st.StoreID, StoreKey: st.Key,
		EntryID: asString(entry), Name: asString(name), Path: asString(name), Default: isDefault}
	if col.EntryID == "" {
		return nil, nil
	}
	return &col, nil
}

// contactCols is the table view of an address book. EntryID is a string
// on a Contacts table (unlike the mail table's binary long-term id), and
// the display/company fields are read by property tag because their
// localized names are not stable across Outlook versions.
type contactColumn struct {
	name string
	dst  func(*contactRow) *string
	time bool
}

var contactCols = []contactColumn{
	{builtinEntryID, func(r *contactRow) *string { return &r.entry }, false},
	{propDisplayName, func(r *contactRow) *string { return &r.name }, false},
	{propCompanyName, func(r *contactRow) *string { return &r.company }, false},
	{propDepartment, func(r *contactRow) *string { return &r.department }, false},
	{propJobTitle, func(r *contactRow) *string { return &r.title }, false},
	{propBusinessPhone, func(r *contactRow) *string { return &r.phone }, false},
	{propMobilePhone, func(r *contactRow) *string { return &r.mobile }, false},
	{propEmail1, func(r *contactRow) *string { return &r.email1 }, false},
	{propEmail2, func(r *contactRow) *string { return &r.email2 }, false},
	{propEmail3, func(r *contactRow) *string { return &r.email3 }, false},
	{propLastModified, func(r *contactRow) *string { return &r.modified }, true},
}

// contactColumnNames lists the address-book columns, in row order, for the
// shared table opener.
func contactColumnNames() []string {
	names := make([]string, len(contactCols))
	for i, col := range contactCols {
		names[i] = col.name
	}
	return names
}

type contactRow struct {
	entry, name, company, department, title string
	phone, mobile                           string
	email1, email2, email3, modified        string
}

// SyncContacts streams one address book through a table: one COM call per
// page instead of one per property per contact. It reads the whole folder;
// the syncer replaces the stored copy only after it returns nil.
func (c *Connector) SyncContacts(ctx context.Context, col source.ContactCollection, sink source.ContactSink) error {
	return c.op(ctx, func(ns Object, s settings) error {
		item, err := ns.Call("GetFolderFromID", col.EntryID, col.StoreID)
		if err != nil {
			return err
		}
		folder := item.(Object)
		defer folder.Release()
		t, err := openTableColumns(folder, contactColumnNames())
		if err != nil {
			return err
		}
		defer t.Release()
		read := 0
		var failure error
		err = readRows(t, s.pageRows, len(contactCols), textEntryID, func(vals []any) bool {
			ct := contactFromRow(vals)
			if ct == nil {
				return true // a distribution list or an empty entry
			}
			ct.SourceID, ct.StoreID, ct.StoreKey, ct.FolderPath = col.SourceID, col.StoreID, col.StoreKey, col.Path
			if failure = sink.Contact(ctx, *ct); failure != nil {
				return false
			}
			read++
			if read%s.pageRows == 0 {
				sink.Progress(source.Progress{Done: int64(read), Unit: "items"})
			}
			return ctx.Err() == nil
		})
		if err == nil {
			err = failure
		}
		sink.Progress(source.Progress{Done: int64(read), Unit: "items"})
		if err != nil {
			return err
		}
		return ctx.Err()
	})
}

// contactFromRow decodes one table row, or nil when the row carries neither
// a name nor an address (a distribution list or an empty entry). The pager
// has already checked that the row has one value per column.
func contactFromRow(vals []any) *contacts.Contact {
	var r contactRow
	for i, col := range contactCols {
		if unset(vals[i]) {
			continue
		}
		if col.time {
			*col.dst(&r) = asExact(vals[i])
			continue
		}
		*col.dst(&r) = strings.TrimSpace(asString(vals[i]))
	}
	if r.entry == "" || r.name == "" && r.email1 == "" && r.email2 == "" && r.email3 == "" {
		return nil
	}
	phone := r.phone
	if phone == "" {
		phone = r.mobile
	}
	ct := contacts.Contact{EntryID: r.entry, DisplayName: r.name, Company: r.company,
		Department: r.department, JobTitle: r.title, LastModified: r.modified, Phone: phone}
	for _, a := range []string{r.email1, r.email2, r.email3} {
		if a != "" {
			ct.Emails = append(ct.Emails, a)
		}
	}
	return &ct
}

// pickedStores returns the selected stores of the last discovery.
func (c *Connector) pickedStores() []StoreInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]StoreInfo, 0, len(c.stores))
	for _, st := range c.stores {
		if st.Selected {
			out = append(out, st)
		}
	}
	return out
}

// openStore opens a store by id from the session's Stores collection.
func openStore(ns Object, storeID string) (Object, error) {
	v, err := ns.Get("Stores")
	if err != nil {
		return nil, err
	}
	stores := v.(Object)
	defer stores.Release()
	n, err := stores.Get("Count")
	if err != nil {
		return nil, err
	}
	for i := int64(1); i <= asInt(n).N; i++ {
		item, err := stores.Call("Item", i)
		if err != nil {
			return nil, err
		}
		st := item.(Object)
		id, err := st.Get("StoreID")
		if err != nil {
			st.Release()
			if unavailable(err) {
				return nil, err
			}
			continue
		}
		if asString(id) == storeID {
			return st, nil
		}
		st.Release()
	}
	return nil, fmt.Errorf("outlook: store %s not found", storeID)
}

// itemTypeContact is the OlItemType of a contact item, the test
// ContactCollections applies to a subfolder.
const itemTypeContact = 2
