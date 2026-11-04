package outlook

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Default folders that give a folder its role, by OlDefaultFolders value.
// Roles come from GetDefaultFolder identity, never from (localized) names.
var roleFolders = []struct {
	id   int
	role mail.FolderRole
}{
	{olFolderInbox, mail.RoleInbox}, {olFolderSentMail, mail.RoleSent}, {olFolderDrafts, mail.RoleDrafts},
	{olFolderDeletedItems, mail.RoleDeleted}, {olFolderJunk, mail.RoleJunk},
}

// Discover lists the mail folders of every picked store
// (source.SettingSelected; nothing by default) with role and change
// state, and refreshes the store list (Stores, Mailboxes) with every store
// of the profile and the owner addresses of the picked ones. An unpicked
// store costs a few calls; its folders are counted once per connector
// (outlook.discover.count_unpicked; off: never, its counts stay -1). A
// store that cannot be read (offline) is listed with its error and left
// out; the others are returned.
func (c *Connector) Discover(ctx context.Context) ([]mail.Folder, error) {
	var out []mail.Folder
	err := c.op(ctx, func(ns Object, s settings) error {
		start, calls := c.clock.Now(), c.count.calls.Load()
		states := map[folderKey]folderState{}
		infos, folders, err := c.discoverStores(ctx, ns, s, states)
		if err != nil {
			return err
		}
		out = folders
		c.recordDiscovery(infos, states, len(out), c.clock.Now().Sub(start), c.count.calls.Load()-calls)
		return nil
	})
	return out, err
}

// discoverStores walks every store of the profile. A store that fails is
// listed with its error; only a lost session or cancellation stop the walk.
// The user's own addresses (the default store's owner) tell an online
// archive of the user's mailbox from someone else's mailbox.
func (c *Connector) discoverStores(ctx context.Context, ns Object, s settings, states map[folderKey]folderState) ([]StoreInfo, []mail.Folder, error) {
	defaultID := defaultStoreID(ns)
	v, err := ns.Get("Stores")
	if err != nil {
		return nil, nil, fmt.Errorf("outlook: list stores: %w", err)
	}
	stores := v.(Object)
	defer stores.Release()
	n, err := stores.Get("Count")
	if err != nil {
		return nil, nil, err
	}
	var infos []StoreInfo
	var out []mail.Folder
	for i := int64(1); i <= asInt(n).N; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		info, folders, err := c.discoverStore(ctx, ns, stores, i, defaultID, s, states)
		if unavailable(err) || ctx.Err() != nil {
			return nil, nil, err
		}
		if err != nil {
			info.Error = Code(err)
			c.log.Warn("store unreadable", "store", i, "key", info.Key, "selected", info.Selected, "code", Code(err), "err", err)
		}
		infos = append(infos, info)
		out = append(out, folders...)
	}
	markArchives(infos)
	for i := range infos {
		if !infos[i].Selected {
			infos[i].Addresses = nil // read for kinds; reported only for picked stores
		}
	}
	return infos, out, nil
}

// markArchives: a delegate-type store owned by the user (the default
// store's primary address) is the user's online archive — Outlook
// exposes no archive marker (probed: ExchangeStoreType, PR_MDB_PROVIDER,
// support mask), but no one else's mailbox can have the user as owner.
func markArchives(infos []StoreInfo) {
	var me string
	for _, in := range infos {
		if in.Default && len(in.Addresses) > 0 {
			me = mail.NormalizeAddress(in.Addresses[0])
		}
	}
	for i := range infos {
		in := &infos[i]
		if me != "" && !in.Default && in.Kind == mail.StoreDelegate && len(in.Addresses) > 0 && mail.NormalizeAddress(in.Addresses[0]) == me {
			in.Kind = mail.StoreArchive
		}
	}
}

// recordDiscovery keeps a Discover's results and publishes them.
func (c *Connector) recordDiscovery(infos []StoreInfo, states map[folderKey]folderState, folders int, d time.Duration, calls int64) {
	kinds := map[string]mail.StoreKind{}
	for _, st := range states {
		kinds[st.storeID] = st.kind
	}
	c.mu.Lock()
	c.folders, c.stores, c.kinds = states, infos, kinds
	c.mu.Unlock()
	errs := 0
	for _, in := range infos {
		if in.Error != "" {
			errs++
		}
		c.log.Info("store", "key", in.Key, "kind", in.Kind, "default", in.Default, "cached", in.Cached, "selected", in.Selected,
			"folders", in.Folders, "items", in.Items, "address_count", len(in.Addresses), "error", in.Error)
	}
	c.stats.add(func(st *Stats) {
		st.Discover.Runs++
		st.Discover.Stores += len(infos)
		st.Discover.StoreErrors += errs
		st.Discover.Folders += folders
		st.Discover.Time += d
	})
	c.publish(Phase{Phase: source.PhaseDiscover, Rows: folders, Calls: calls, Duration: d})
}

// defaultStoreID is the StoreID of the profile's default store ("" when
// Outlook does not say: then no store is marked default, nothing else is
// guessed).
func defaultStoreID(ns Object) string {
	v, err := ns.Get("DefaultStore")
	if err != nil || v == nil {
		return ""
	}
	st := v.(Object)
	defer st.Release()
	id, _ := st.Get("StoreID")
	return asString(id)
}

// discoverStore lists one store; an unpicked store is only counted, once.
func (c *Connector) discoverStore(ctx context.Context, ns, stores Object, i int64, defaultID string, s settings,
	states map[folderKey]folderState) (StoreInfo, []mail.Folder, error) {
	start, calls := c.clock.Now(), c.count.calls.Load()
	v, err := stores.Call("Item", i)
	if err != nil {
		return StoreInfo{}, nil, err
	}
	store := v.(Object)
	defer store.Release()
	info, err := c.storeInfo(store, defaultID, s)
	if err != nil {
		return info, nil, err
	}
	// The owner identity is reported for picked stores, and the default
	// store's is needed to tell the user's own online archive from someone
	// else's mailbox (markArchives). Every other unpicked store is skipped:
	// reading its identity calls into the store, and an offline delegate
	// mailbox could block there.
	if info.Selected || info.Default {
		owner, err := c.ownerAddresses(ns, store, info.StoreID, info.Default)
		if err != nil {
			return info, nil, err
		}
		info.Addresses, info.IdentityComplete = owner.addrs, len(owner.failed) == 0
		if !info.IdentityComplete {
			c.log.Warn("owner identity read incompletely; the recorded one is kept", "key", info.Key, "failed", owner.failed)
		}
	}
	if !info.Selected {
		c.mu.Lock()
		prev, ok := c.counted[info.Key]
		c.mu.Unlock()
		switch {
		case ok:
			info.Folders, info.Items, info.CountedAt = prev.Folders, prev.Items, prev.CountedAt
			return info, nil, nil
		case !s.countUnpicked: // listed without counts (Folders, Items -1)
			return info, nil, nil
		}
	}
	w := &walker{c: c, ctx: ctx, s: s, states: states, countOnly: !info.Selected,
		base: mail.Folder{SourceID: c.id, StoreID: info.StoreID, StoreName: info.Name, StoreKind: info.Kind}}
	if info.Selected {
		w.roles = defaultFolders(store)
	}
	if err := w.root(store, info.Name); err != nil {
		return info, nil, err
	}
	info.Folders, info.Items = w.folders, w.items
	if !info.Selected {
		info.CountedAt = c.clock.Now().UTC().Format(mail.TimeLayout)
		c.mu.Lock()
		c.counted[info.Key] = info
		c.mu.Unlock()
	}
	c.publish(Phase{Phase: source.PhaseDiscoverStore, Store: info.Name, StoreKey: info.Key, Kind: info.Kind, Rows: w.folders,
		Calls: c.count.calls.Load() - calls, Duration: c.clock.Now().Sub(start)})
	return info, w.out, nil
}

// storeInfo reads a store's identity, kind and state.
func (c *Connector) storeInfo(store Object, defaultID string, s settings) (StoreInfo, error) {
	typ, err := store.Get("ExchangeStoreType")
	if err != nil {
		return StoreInfo{}, err
	}
	name, err := store.Get("DisplayName")
	if err != nil {
		return StoreInfo{}, err
	}
	id, err := store.Get("StoreID")
	if err != nil {
		return StoreInfo{}, err
	}
	cached, _ := store.Get("IsCachedExchange")
	path, _ := store.Get("FilePath")
	sid := asString(id)
	info := StoreInfo{Mailbox: source.Mailbox{Key: c.keyOf(sid), SourceID: c.id, StoreID: sid, Name: asString(name),
		Default: defaultID != "" && sid == defaultID, Folders: -1, Items: -1}}
	info.Kind = storeKind(asInt(typ).N, asString(path))
	info.Cached, _ = asBool(cached)
	info.Selected = s.selected[info.Key]
	return info, nil
}

// storeKind maps Store.ExchangeStoreType (OlExchangeStoreType) to a kind;
// a non-Exchange store is a PST when its data file is one (the file
// format's extension), else another account's store (IMAP, POP…).
func storeKind(typ int64, filePath string) mail.StoreKind {
	switch typ {
	case olPrimaryExchangeMailbox:
		return mail.StorePrimary
	case olExchangeMailbox:
		return mail.StoreDelegate
	case olExchangePublicFolder:
		return mail.StorePublic
	case olAdditionalExchangeMailbox:
		return mail.StoreShared
	case olNotExchange:
		if strings.EqualFold(filepath.Ext(filePath), ".pst") {
			return mail.StorePST
		}
	}
	return mail.StoreOther
}

// defaultFolders maps the EntryIDs of a store's default folders to roles. A
// store without one of them (an archive, a PST) simply has no such role.
func defaultFolders(store Object) map[string]mail.FolderRole {
	roles := map[string]mail.FolderRole{}
	for _, rf := range roleFolders {
		v, err := store.Call("GetDefaultFolder", rf.id)
		if err != nil {
			continue
		}
		f := v.(Object)
		if eid, err := f.Get("EntryID"); err == nil {
			roles[asString(eid)] = rf.role
		}
		f.Release()
	}
	return roles
}

// FolderStatus tells the syncer what became of a stored folder the last
// Discover did not return: in a store that is not picked → not synced; in a
// store that was read without error → gone if Outlook no longer finds its
// EntryID (MAPI_E_NOT_FOUND), else not synced (hidden, excluded); unknown
// otherwise (store offline or no longer in the profile).
func (c *Connector) FolderStatus(ctx context.Context, f mail.Folder) (source.FolderStatus, error) {
	c.mu.Lock()
	var info *StoreInfo
	for i := range c.stores {
		if c.stores[i].StoreID == f.StoreID {
			info = &c.stores[i]
		}
	}
	c.mu.Unlock()
	switch {
	case info == nil || info.Error != "" || f.RemoteID == "":
		return source.FolderUnknown, nil
	case !info.Selected:
		return source.FolderNotSynced, nil
	}
	status := source.FolderUnknown
	err := c.op(ctx, func(ns Object, _ settings) error {
		v, err := ns.Call("GetFolderFromID", f.RemoteID, f.StoreID)
		if hr, _ := HResult(err); hr == hrNotFound {
			status = source.FolderGone
			return nil
		}
		if err != nil {
			return err
		}
		v.(Object).Release()
		status = source.FolderNotSynced
		return nil
	})
	return status, err
}
