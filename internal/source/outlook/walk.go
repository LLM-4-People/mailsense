package outlook

import (
	"context"
	"errors"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// folderProps are everything Discover needs of a folder, read in ONE
// GetProperties call (each COM call to an online mailbox is a server round
// trip): identity, name, kind, visibility, children and change state.
var folderProps = []string{propEntryID, propDisplayName, propContainerClass, propSubfolders, propFolderType,
	propAttrHidden, propContentCount, propCommitTimeMax, propContentUnread}

// walker enumerates the folder tree of one store.
type walker struct {
	c      *Connector
	ctx    context.Context
	s      settings
	states map[folderKey]folderState
	roles  map[string]mail.FolderRole
	base   mail.Folder
	out    []mail.Folder
	// countOnly: a disabled store is only counted (no change state, no
	// folders returned).
	countOnly bool
	folders   int
	items     int64
}

// root walks a store from its root folder; paths take Outlook's
// FolderPath form \\Store\Folder\Subfolder.
func (w *walker) root(store Object, storeName string) error {
	v, err := store.Call("GetRootFolder")
	if err != nil {
		return err
	}
	root := v.(Object)
	defer root.Release()
	return w.children(root, `\\`+storeName)
}

// children visits every subfolder of parent (whose path is path), depth
// first. GetFirst/GetNext walks the hierarchy cursor: measured 3–7× cheaper
// than Folders.Item(i) on online mailboxes, and it needs no Count.
func (w *walker) children(parent Object, path string) error {
	v, err := parent.Get("Folders")
	if err != nil {
		return err
	}
	folders := v.(Object)
	defer folders.Release()
	next := "GetFirst"
	for {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		v, err := folders.Call(next)
		if err != nil || v == nil {
			return err
		}
		next = "GetNext"
		f := v.(Object)
		err = w.visit(f, path)
		f.Release()
		if err != nil && storeFatal(err) {
			return err
		}
		if err != nil {
			w.skip("unreadable")
			w.c.log.Warn("folder unreadable; skipped, the rest of the store is still synced", "parent", path, "code", Code(err))
		}
	}
}

// storeFatal: errors that end the walk of a store (the session or the
// store itself is gone, or the operation was cancelled); any other error is
// one folder's (e.g. no rights on a delegate subfolder).
func storeFatal(err error) bool {
	return unavailable(err) || errors.Is(err, ErrStoreOffline) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// visit records f when it is a visible mail folder and descends into its
// subfolders (hidden and search folders are skipped with their subtree).
func (w *walker) visit(f Object, parent string) error {
	v, err := f.Get("PropertyAccessor")
	if err != nil {
		return err
	}
	pa := v.(Object)
	raw, err := pa.Call("GetProperties", folderProps)
	pa.Release()
	if err != nil {
		return err
	}
	vals, err := valuesOf(raw, folderProps)
	if err != nil {
		return err
	}
	hidden, _ := asBool(vals[5])
	switch {
	case asInt(vals[4]).N == folderTypeSearch:
		w.skip("search")
		return nil
	case hidden:
		w.skip("hidden")
		return nil
	}
	path := parent + `\` + asString(vals[1])
	switch {
	case !isMailClass(asString(vals[2])):
		w.skip("not_mail")
	case w.excluded(path):
		w.skip("excluded")
	default:
		w.record(f, path, vals)
	}
	if sub, _ := asBool(vals[3]); sub {
		return w.children(f, path)
	}
	return nil
}

// nonMailClasses are the folder classes whose DefaultItemType is not
// olMailItem (measured on the target profile: every other class, including
// none, IPF, IPF.Note…, IPF.Imap, IPF.SkypeTeams.Message, is mail).
var nonMailClasses = []string{"IPF.Appointment", "IPF.Contact", "IPF.Task", "IPF.Journal", "IPF.StickyNote"}

// isMailClass reports whether a folder of this container class holds mail
// (DefaultItemType = olMailItem) without asking for DefaultItemType.
func isMailClass(class string) bool {
	for _, c := range nonMailClasses {
		if class == c || strings.HasPrefix(class, c+".") {
			return false
		}
	}
	return true
}

func (w *walker) skip(reason string) { w.c.stats.add(func(s *Stats) { s.Discover.Skipped[reason]++ }) }

func (w *walker) excluded(path string) bool {
	for _, re := range w.s.excludeFolders {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

func (w *walker) record(f Object, path string, vals []any) {
	w.folders++
	w.items += asInt(vals[6]).N
	if w.countOnly {
		return
	}
	eid := asHex(vals[0])
	role, ok := w.roles[eid]
	if !ok {
		role = mail.RoleOther
	}
	st := folderState{storeID: w.base.StoreID, kind: w.base.StoreKind, entryID: eid, count: asInt(vals[6]).N,
		commitMax: asExact(vals[7]), unread: asInt(vals[8]).N, readAt: w.c.clock.Now()}
	if st.commitMax == "" && st.count > 0 {
		st.newest = w.newest(f)
	}
	folder := w.base
	folder.Path, folder.Role, folder.ItemCount, folder.RemoteID = path, role, st.count, eid
	w.states[folderKey{folder.StoreID, folder.Path}] = st
	w.out = append(w.out, folder)
}

// newest reads the newest LastModificationTime through a one-column table
// sorted by it ("" when Outlook refuses: the folder then always syncs).
func (w *walker) newest(f Object) string {
	stamp, err := newestStamp(f)
	w.c.stats.add(func(s *Stats) {
		s.Discover.NewestReads++
		if err != nil {
			s.Discover.NewestFails++
		}
	})
	if err != nil {
		w.c.log.Debug("newest modification time unreadable; folder will be read", "code", Code(err))
	}
	return stamp
}

func newestStamp(f Object) (string, error) {
	t, err := openTable(f, []column{stampColumn})
	if err != nil {
		return "", err
	}
	defer t.Release()
	if _, err := t.Call("Sort", builtinLastModifiedSort, true); err != nil {
		return "", err
	}
	v, err := t.Call("GetArray", 1)
	if err != nil {
		return "", err
	}
	rows, _ := v.([][]any)
	if len(rows) == 0 || len(rows[0]) != 1 {
		return "", errLayout
	}
	return asExact(rows[0][0]), nil
}
