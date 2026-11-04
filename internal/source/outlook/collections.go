package outlook

import "context"

// collectionKind is one walk over the picked stores' default collection
// folder: the OlDefaultFolders value it opens and the label its skip logs use.
type collectionKind struct {
	label  string
	folder int
}

// The collection walks, one per collection family the connector syncs.
var (
	calendarCollections = collectionKind{label: "calendar", folder: olFolderCalendar}
	contactCollections  = collectionKind{label: "contacts", folder: olFolderContacts}
)

// eachPickedDefaultFolder opens the default folder of kind in every picked
// store and hands it to fn. It owns both handles: the store is released as
// soon as its folder is obtained, and the folder exactly once after fn
// returns, so fn may read the folder freely but must not release it. A store
// or folder Outlook refuses is skipped with one log line; an unavailable
// Outlook or an ended context stops the walk and its error is returned as is.
// The error of fn is a folder-level failure: it skips that folder the same
// way and leaves the walk running.
func (c *Connector) eachPickedDefaultFolder(ctx context.Context, ns Object, kind collectionKind,
	fn func(st StoreInfo, folder Object) error) error {
	for _, st := range c.pickedStores() {
		store, err := openStore(ns, st.StoreID)
		if err != nil {
			if unavailable(err) || ctx.Err() != nil {
				return err
			}
			c.log.Warn(kind.label+": store unreadable", "store", st.Key, "code", Code(err))
			continue
		}
		fv, err := store.Call("GetDefaultFolder", kind.folder)
		store.Release()
		if err != nil {
			if unavailable(err) || ctx.Err() != nil {
				return err
			}
			c.log.Debug(kind.label+": default folder unavailable", "store", st.Key, "code", Code(err))
			continue
		}
		folder := fv.(Object)
		err = fn(st, folder)
		folder.Release()
		if err != nil && (unavailable(err) || ctx.Err() != nil) {
			return err
		}
	}
	return nil
}
