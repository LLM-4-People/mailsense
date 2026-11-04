package outlook

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Sync brings one folder up to date. Unchanged since cursor (cursor.go:
// state equal and the last snapshot younger than outlook.sync.recheck_after):
// nothing is called. Else a
// three-column snapshot (EntryID, modification time, flags) goes to the sink, which
// answers which ids are new or changed; one full-column pass, sorted newest
// change first, then streams exactly those rows as Partial messages (the
// detail pass completes them). The cursor returned is the state Discover
// read before any row, so a change during the sync is seen next time.
func (c *Connector) Sync(ctx context.Context, folder mail.Folder, cursor source.Cursor, sink source.Sink) (source.Cursor, error) {
	c.mu.Lock()
	st, ok := c.folders[folderKey{folder.StoreID, folder.Path}]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("outlook: folder %q was not discovered in this session", folder.Path)
	}
	set, err := c.settings()
	if err != nil {
		return "", err
	}
	if st.unchanged(cursor, set.recheck) {
		c.stats.add(func(s *Stats) { s.Sync.Folders++; s.Sync.Unchanged++ })
		return cursor, nil
	}
	next := st.cursor()
	err = c.op(ctx, func(ns Object, s settings) error {
		v, err := ns.Call("GetFolderFromID", st.entryID, st.storeID)
		if err != nil {
			return err
		}
		f := v.(Object)
		defer f.Release()
		want, err := c.snapshot(ctx, f, folder, st, s, sink)
		if err != nil || len(want) == 0 {
			return err
		}
		return c.stream(ctx, ns, f, folder, s, want, sink)
	})
	c.stats.add(func(s *Stats) { s.Sync.Folders++ })
	if err != nil {
		return "", err
	}
	return next, nil
}

// snapshot reads every id and stamp of the folder and asks the sink which
// ones to deliver.
func (c *Connector) snapshot(ctx context.Context, f Object, folder mail.Folder, st folderState, s settings, sink source.Sink) (map[string]bool, error) {
	start, calls := c.clock.Now(), c.count.calls.Load()
	t, err := openTable(f, idColumns)
	if err != nil {
		return nil, err
	}
	defer t.Release()
	var versions []source.Version
	var bad int
	err = readRows(t, s.pageRows, len(idColumns), binaryEntryID, func(values []any) bool {
		r := decode(idColumns, values, s.times)
		if r.msg.ID == "" {
			bad++
			return true
		}
		versions = append(versions, source.Version{ID: r.msg.ID, Stamp: r.stamp()})
		return ctx.Err() == nil
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	complete := bad == 0 && int64(len(versions)) == st.count
	d := c.clock.Now().Sub(start)
	c.stats.add(func(x *Stats) {
		x.Sync.SnapshotRows += len(versions)
		x.Sync.SnapshotTime += d
		if !complete {
			x.Sync.Incomplete++
		}
	})
	c.publish(Phase{Phase: source.PhaseSnapshot, Store: folder.StoreName, StoreKey: c.keyOf(folder.StoreID), Folder: folder.Path, Rows: len(versions),
		Calls: c.count.calls.Load() - calls, Duration: d})
	return sink.Snapshot(ctx, versions, complete)
}

// stream reads the full columns, newest change first, and delivers the
// wanted rows; it stops as soon as every wanted row was delivered. When
// Outlook refuses the sort the whole table is read unsorted.
func (c *Connector) stream(ctx context.Context, ns, f Object, folder mail.Folder, s settings, want map[string]bool, sink source.Sink) error {
	start, calls := c.clock.Now(), c.count.calls.Load()
	cols := fullColumns(s.preview)
	t, err := openTable(f, cols)
	if err != nil {
		return err
	}
	defer t.Release()
	if _, err := t.Call("Sort", builtinLastModifiedSort, true); err != nil {
		if unavailable(err) {
			return err
		}
		c.stats.add(func(x *Stats) { x.Sync.SortFallbacks++ })
		c.log.Debug("table sort refused; reading unsorted", "folder", folder.Path, "code", Code(err))
	}
	rows, emitted := 0, 0
	var failure error
	err = readRows(t, s.pageRows, len(cols), binaryEntryID, func(values []any) bool {
		rows++
		r := decode(cols, values, s.times)
		if !want[r.msg.ID] {
			return true
		}
		m, err := c.message(ns, &r, folder)
		if err != nil {
			failure = err
			return false
		}
		if failure = sink.Message(ctx, m); failure != nil {
			return false
		}
		emitted++
		if rows%s.pageRows == 0 {
			sink.Progress(source.Progress{Done: int64(emitted), Total: int64(len(want)), Unit: "items"})
		}
		return emitted < len(want) && ctx.Err() == nil
	})
	if err == nil {
		err = failure
	}
	if err == nil {
		err = ctx.Err()
	}
	sink.Progress(source.Progress{Done: int64(emitted), Total: int64(len(want)), Unit: "items"})
	d := c.clock.Now().Sub(start)
	c.stats.add(func(x *Stats) { x.Sync.TableRows += rows; x.Sync.Emitted += emitted; x.Sync.TableTime += d })
	c.publish(Phase{Phase: source.PhaseTable, Store: folder.StoreName, StoreKey: c.keyOf(folder.StoreID), Folder: folder.Path, Rows: rows, Calls: c.count.calls.Load() - calls, Duration: d})
	return err
}

// message completes a decoded row into a Partial message.
func (c *Connector) message(ns Object, r *tableRow, folder mail.Folder) (mail.Message, error) {
	m := r.msg
	m.SourceID, m.StoreID, m.StoreName, m.FolderPath, m.FolderRole = c.id, folder.StoreID, folder.StoreName, folder.Path, folder.Role
	m.Origin, m.Partial, m.Stamp = Origin, true, r.stamp()
	m.AddressedToMe = r.addressedToMe()
	m.MessageFlags = r.flags
	m.IsRead = r.isRead()
	var err error
	if m.SenderAddress, err = c.dns.address(r.sender, entryOpener(ns, r.sender.entryID)); err != nil {
		return m, err
	}
	m.SentRepresentingAddress, err = c.dns.address(r.sentRepr, entryOpener(ns, r.sentRepr.entryID))
	return m, err
}

// entryOpener opens an address entry by its EntryID (nil when there is none).
func entryOpener(ns Object, entryID string) func() (Object, error) {
	if entryID == "" {
		return nil
	}
	return func() (Object, error) {
		v, err := ns.Call("GetAddressEntryFromID", entryID)
		if err != nil {
			return nil, err
		}
		return v.(Object), nil
	}
}
