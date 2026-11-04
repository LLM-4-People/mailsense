package outlook

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Lookups and builders.

func (fl *fakeFolder) find(eid string) *fakeFolder {
	if fl.eid == eid {
		return fl
	}
	for _, c := range fl.children {
		if x := c.find(eid); x != nil {
			return x
		}
	}
	return nil
}

func (fl *fakeFolder) each(fn func(*fakeFolder)) {
	fn(fl)
	for _, c := range fl.children {
		c.each(fn)
	}
}

func (o *fakeOutlook) item(storeID, id string) *fakeItem {
	for _, s := range o.stores {
		if s.id != storeID {
			continue
		}
		var hit *fakeItem
		s.root.each(func(fl *fakeFolder) {
			for _, it := range fl.items {
				if it.id() == id {
					hit = it
				}
			}
		})
		return hit
	}
	return nil
}

func (it *fakeItem) id() string { return asHex(it.props[propLongTermEntryID]) }

// newItem builds a mail item: n numbers its ids, received is its delivery
// time (UTC); every other summary property gets a plausible value.
func newItem(n int, received time.Time) *fakeItem {
	lt := []byte(fmt.Sprintf("LT-%04d", n))
	return &fakeItem{
		props: map[string]any{
			propLongTermEntryID: lt, propLastModified: DateOf(received.Add(time.Minute)), builtinEntryID: fmt.Sprintf("EID%04d", n),
			propSearchKey: []byte(fmt.Sprintf("SK%d", n)), propMessageClass: "IPM.Note", propSubject: fmt.Sprintf("Sujet %d", n),
			propSenderName: "Expéditeur", propSenderSMTP: fmt.Sprintf("sender%d@example.com", n%3),
			propDeliveryTime: DateOf(received), propSubmitTime: DateOf(received.Add(-time.Minute)),
			propCreationTime: DateOf(received), propMessageFlags: int64(msgFlagRead), propHasAttach: false,
			propToMe: true, propCcMe: false, propRecipientMe: true, propInternetMessageID: fmt.Sprintf("<m%d@example.com>", n),
			propTransportHeaders: "Received: by example", propReferences: "<root@example.com>",
			propLocalCommitTime: DateOf(received.Add(2 * time.Minute)), propBody: "aperçu",
		},
		body:     fmt.Sprintf("Corps du message %d — déjà vu", n),
		htmlBody: fmt.Sprintf("<p>Corps du message <strong>%d</strong> — déjà vu</p>", n),
		recips:   []fakeRecip{{typ: 1, name: "Amélie", email: "amelie.durand@example.com", addrType: "SMTP", smtp: "amelie.durand@example.com"}},
	}
}

// mailbox builds a store with Inbox and Sent (localized names, roles by
// default-folder identity), a hidden folder, a search folder and a
// calendar, each with the given items.
func mailbox(name, id string, typ int64, inbox, sent []*fakeItem) *fakeStore {
	in := &fakeFolder{name: "Boîte de réception", eid: hexID(id + "-in"), class: "IPF.Note", items: inbox}
	out := &fakeFolder{name: "Éléments envoyés", eid: hexID(id + "-sent"), class: "IPF.Note", items: sent}
	sub := &fakeFolder{name: "Projets", eid: hexID(id + "-proj"), class: "IPF.Note"}
	in.children = []*fakeFolder{sub}
	root := &fakeFolder{name: "Top", eid: hexID(id + "-root"), children: []*fakeFolder{
		in, out,
		{name: "Paramètres", eid: hexID(id + "-hid"), class: "IPF.Configuration", hidden: true},
		{name: "Recherche", eid: hexID(id + "-srch"), class: "IPF.Note", search: true},
		{name: "Calendrier", eid: hexID(id + "-cal"), class: "IPF.Appointment"},
	}}
	return &fakeStore{name: name, id: id, typ: typ, root: root, defaults: map[int]*fakeFolder{6: in, 5: out}}
}

func hexID(s string) string { return strings.ToUpper(hex.EncodeToString([]byte(s))) }

// argInt accepts the integer kinds the connector passes (int, int64).
func argInt(a any) int {
	switch v := a.(type) {
	case int:
		return v
	case int64:
		return int(v)
	}
	panic(fmt.Sprintf("not an integer argument: %T", a))
}
