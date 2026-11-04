package outlook

import (
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
)

// fakeOutlook is an in-memory Outlook object model behind the host
// interface: stores, folders, tables, items, recipients and an address
// book, with failure injection. Only the members the connector may call
// exist; any other call is an error and is recorded.
type fakeOutlook struct {
	elevated, unregistered, newOutlook, running bool
	attachErr                                   uint32 // HRESULT on Attach
	starts                                      int    // Start calls (the user's action)
	startErr                                    error  // Start answers this
	startedAt                                   time.Time
	idle                                        time.Duration // the user's idle time (UserIdle)
	idleErr                                     error

	stores      []*fakeStore
	currentUser string // address-book key of the profile's user
	book        map[string]fakeEntry
	accounts    []fakeAccount

	busy       int    // the next n calls answer RPC_E_CALL_REJECTED
	lost       bool   // every call answers RPC_E_DISCONNECTED
	sortErr    uint32 // Table.Sort answers this HRESULT
	refuseCols map[string]bool
	transpose  bool // GetArray returns columns × rows (layout regression)

	clock      *clocktest.Fake  // when set, every call takes 1 ms (timing stats are measured)
	onCall     func(key string) // test hook, run before each call (e.g. cancel a context)
	shortProps bool             // GetProperties answers one value too few
	calls      []string         // every member called, "Class.Member"
	opened     []*fakeItem      // items opened by GetItemFromID, in order
	open       int              // live objects (Release decrements)
	released   int
	misuse     []string // calls the model does not have, double releases
}

type fakeEntry struct {
	smtp     string   // ExchangeUser.PrimarySmtpAddress ("" = not an Exchange user)
	propSMTP string   // PR_SMTP_ADDRESS of the entry
	dn       string   // ExchangeUser.Address (legacy DN)
	proxies  []string // PR_EMS_AB_PROXY_ADDRESSES ("SMTP:…", "smtp:…", "X500:…", "SIP:…")
	failUser uint32   // GetExchangeUser answers this HRESULT (the address book offline)
}

// fakeAccount is one account of the profile (Namespace.Accounts).
type fakeAccount struct {
	smtp, storeID string // SmtpAddress; StoreID of its DeliveryStore ("" = none)
}

type fakeStore struct {
	name, id  string
	typ       int64
	ownerEID  string // hex; "" = no PR_MAILBOX_OWNER_ENTRYID
	filePath  string // Store.FilePath
	offline   bool
	cached    bool
	isDefault bool // Namespace.DefaultStore
	root      *fakeFolder
	defaults  map[int]*fakeFolder
}

type fakeFolder struct {
	name, eid, class   string
	hidden, search     bool
	unread             int64
	failProps          uint32    // the folder's PropertyAccessor answers this (no rights)
	failEID            uint32    // Folder.EntryID answers this (a folder-level read failure)
	commitMax          time.Time // zero = property absent
	children           []*fakeFolder
	items              []*fakeItem
	itemType           int64
	itemIter           int
	includeRecurrences bool
}

type fakeItem struct {
	props        map[string]any // by schema name (and built-in names)
	body         string
	htmlBody     string
	recips       []fakeRecip
	atts         []fakeAttachment
	failOpen     uint32 // GetItemFromID answers this
	failBody     uint32 // Body answers this
	failHTMLBody uint32 // HTMLBody answers this
	class        string // "post": an item class without Recipients
	tableCap     int    // >0: tables return strings cut to this many characters (Outlook: 255)
	nothing      bool   // GetItemFromID answers Nothing instead of an item
	// failOccurrence makes a recurring master's GetOccurrence answer this
	// HRESULT for every date (0 answers normally).
	failOccurrence uint32
	// exceptions are the instances of the series that Outlook keeps as
	// exceptions: moved, canceled or deleted occurrences.
	exceptions []fakeException
}

// fakeException is one exception of a recurring series. original is the
// pattern slot the instance belongs to; item is what GetOccurrence returns
// for a moved or canceled instance (nil for a deleted one).
type fakeException struct {
	original time.Time
	item     *fakeItem
	deleted  bool
	nothing  bool // answer Nothing instead of E_FAIL for a deleted instance
}

type fakeRecip struct {
	typ                         int64
	name, email, addrType, smtp string
	entry                       string // address-book key for AddressEntry
}

// fakeAttachment is one Attachment object: the visible name/size plus the
// hidden MIME properties read through its PropertyAccessor.
type fakeAttachment struct {
	name  string
	size  int64
	props map[string]any
	// failRead makes Attachments.Item answer this HRESULT instead of the
	// object; nothing makes it answer Nothing (a failed read, not an absent
	// attachment).
	failRead uint32
	nothing  bool
}

// newFake is a running Outlook started long ago, the user away.
func newFake() *fakeOutlook {
	return &fakeOutlook{running: true, startedAt: t0.Add(-24 * time.Hour), idle: time.Hour, book: map[string]fakeEntry{},
		refuseCols: map[string]bool{propLocalCommitTime: true}}
}

// host interface.

func (o *fakeOutlook) Init() error      { return nil }
func (o *fakeOutlook) Uninit()          {}
func (o *fakeOutlook) NewOutlook() bool { return o.newOutlook }
func (o *fakeOutlook) Elevated() (bool, error) {
	return o.elevated, nil
}
func (o *fakeOutlook) Registered() error {
	if o.unregistered {
		return &ComError{Code: hrClassString, Member: "CLSIDFromProgID"}
	}
	return nil
}
func (o *fakeOutlook) Attach() (Object, error) {
	if o.attachErr != 0 {
		return nil, &ComError{Code: o.attachErr, Member: "GetActiveObject"}
	}
	if !o.running {
		return nil, &ComError{Code: hrUnavailable, Member: "GetActiveObject"}
	}
	return o.obj("Application", nil), nil
}
func (o *fakeOutlook) Start() error {
	o.starts++
	if o.startErr != nil {
		return o.startErr
	}
	o.running, o.lost = true, false
	o.startedAt = o.now()
	return nil
}
func (o *fakeOutlook) Started() (time.Time, error) {
	if !o.running {
		return time.Time{}, ErrNotRunning
	}
	return o.startedAt, nil
}
func (o *fakeOutlook) UserIdle() (time.Duration, error) { return o.idle, o.idleErr }

// now is the fake clock's time (tNow without one).
func (o *fakeOutlook) now() time.Time {
	if o.clock == nil {
		return tNow
	}
	return o.clock.Now()
}

// crash is Outlook dying: every call answers RPC_E_DISCONNECTED until it
// is restarted (restart, at the fake clock's time).
func (o *fakeOutlook) crash() { o.lost, o.running = true, false }

func (o *fakeOutlook) restart() {
	o.lost, o.running = false, true
	o.startedAt = o.now()
}

// fakeObj is one object of the fake model.
type fakeObj struct {
	o        *fakeOutlook
	kind     string
	target   any
	released bool
}

func (o *fakeOutlook) obj(kind string, target any) *fakeObj {
	o.open++
	return &fakeObj{o: o, kind: kind, target: target}
}

func (f *fakeObj) Release() {
	if f.released {
		f.o.misuse = append(f.o.misuse, "double release of "+f.kind)
		return
	}
	f.released = true
	f.o.open--
	f.o.released++
}

func (f *fakeObj) Get(name string, args ...any) (any, error)  { return f.invoke(name, false, args) }
func (f *fakeObj) Call(name string, args ...any) (any, error) { return f.invoke(name, true, args) }

func (f *fakeObj) Put(name string, value any) error {
	f.o.calls = append(f.o.calls, f.kind+".Put:"+name)
	switch t := f.target.(type) {
	case *fakeFolder:
		if name == "IncludeRecurrences" {
			t.includeRecurrences, _ = value.(bool)
			return nil
		}
	}
	return fail(hrUnknownName, name)
}

func fail(code uint32, member string) error { return &ComError{Code: code, Member: member} }

func (f *fakeObj) invoke(name string, call bool, args []any) (any, error) {
	o := f.o
	key := f.kind + "." + name
	o.calls = append(o.calls, key)
	if o.onCall != nil {
		o.onCall(key)
	}
	if o.clock != nil {
		o.clock.Advance(time.Millisecond)
	}
	if f.released {
		o.misuse = append(o.misuse, "call on released "+key)
	}
	if o.lost {
		return nil, fail(hrDisconnected, name)
	}
	if o.busy > 0 {
		o.busy--
		return nil, fail(hrCallRejected, name)
	}
	v, err := f.member(name, args)
	if err == errNoMember {
		o.misuse = append(o.misuse, "no such member "+key)
		return nil, fail(hrUnknownName, name)
	}
	return v, err
}

var errNoMember = fmt.Errorf("no member")

func (f *fakeObj) member(name string, args []any) (any, error) {
	switch f.kind {
	case "Application", "Namespace", "Stores", "Accounts":
		return f.session(name, args)
	}
	switch t := f.target.(type) {
	case *fakeStore:
		return f.store(t, name, args)
	case fakeFolderRef:
		return f.folder(t, name, args)
	case *fakeFolders:
		return f.folders(t, name)
	case *fakeFolder: // Items
		return f.items(t, name, args)
	case *fakeTable:
		return f.table(t, name, args)
	case *fakeColumns:
		return f.columns(t, name, args)
	case *fakeAccessor:
		return f.accessor(t, name, args)
	case *fakeItem:
		return f.item(t, name)
	case *fakeAttachments:
		return f.attachments(t, name, args)
	case *fakeAttachment:
		return f.attachment(t, name)
	case *fakeRecipients:
		return f.recipients(t, name, args)
	case fakeRecip:
		return f.recipient(t, name)
	case *fakeRecurrence:
		return f.recurrence(t, name, args)
	case string:
		return f.entry(t, name, args)
	case fakeAccount:
		return f.account(t, name)
	}
	return nil, errNoMember
}

// session serves the Application, Namespace and Stores objects.
func (f *fakeObj) session(name string, args []any) (any, error) {
	o := f.o
	switch f.kind + "." + name {
	case "Application.GetNamespace":
		return o.obj("Namespace", nil), nil
	case "Application.Version":
		return "16.0.0.20430", nil
	case "Namespace.Stores":
		return o.obj("Stores", nil), nil
	case "Namespace.DefaultStore":
		for _, st := range o.stores {
			if st.isDefault {
				return o.obj("Store", st), nil
			}
		}
		return nil, nil
	case "Namespace.CurrentUser":
		return o.obj("Recipient", fakeRecip{entry: o.currentUser}), nil
	case "Namespace.GetFolderFromID":
		return o.folderFromID(args[0].(string), args[1].(string))
	case "Namespace.GetItemFromID":
		return o.itemFromID(args[0].(string), args[1].(string))
	case "Namespace.GetAddressEntryFromID":
		if _, ok := o.book[args[0].(string)]; !ok {
			return nil, fail(hrNotFound, name)
		}
		return o.obj("AddressEntry", args[0].(string)), nil
	case "Namespace.Accounts":
		return o.obj("Accounts", nil), nil
	case "Accounts.Count":
		return int64(len(o.accounts)), nil
	case "Accounts.Item":
		i := argInt(args[0])
		if i < 1 || i > len(o.accounts) {
			return nil, fail(hrNotFound, name)
		}
		return o.obj("Account", o.accounts[i-1]), nil
	case "Stores.Count":
		return int64(len(o.stores)), nil
	case "Stores.Item":
		i := argInt(args[0])
		if i < 1 || i > len(o.stores) {
			return nil, fail(hrNotFound, name)
		}
		return o.obj("Store", o.stores[i-1]), nil
	}
	return nil, errNoMember
}

func (o *fakeOutlook) folderFromID(eid, sid string) (any, error) {
	for _, s := range o.stores {
		if s.id == sid {
			if fl := s.root.find(eid); fl != nil {
				return o.obj("Folder", fakeFolderRef{s, fl}), nil
			}
		}
	}
	return nil, fail(hrNotFound, "GetFolderFromID")
}

func (o *fakeOutlook) itemFromID(id, sid string) (any, error) {
	it := o.item(sid, id)
	switch {
	case it == nil:
		return nil, fail(hrNotFound, "GetItemFromID")
	case it.nothing:
		return nil, nil // Outlook answered Nothing
	case it.failOpen != 0:
		return nil, fail(it.failOpen, "GetItemFromID")
	}
	o.opened = append(o.opened, it)
	return o.obj("Item", it), nil
}

func (f *fakeObj) account(a fakeAccount, name string) (any, error) {
	switch name {
	case "SmtpAddress":
		return a.smtp, nil
	case "DeliveryStore":
		for _, st := range f.o.stores {
			if st.id == a.storeID {
				return f.o.obj("Store", st), nil
			}
		}
		return nil, nil
	}
	return nil, errNoMember
}
