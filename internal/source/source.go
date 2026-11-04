// Package source defines the Connector contract every mail source
// implements (Outlook COM, the legacy archive, future IMAP/Graph…) and the
// registry the app uses to find them.
package source

import (
	"context"
	"reflect"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Capabilities tell the layers above what a connector's data holds, so
// they ask instead of assuming one source (the store keeps them per
// source: store.SourceCapabilities). A layer that needs a capability a
// source lacks leaves that source's mail out of the rule and says so.
type Capabilities struct {
	Incremental   bool `json:"incremental"`     // cursors skip unchanged data
	Bodies        bool `json:"bodies"`          // full bodies (not only previews)
	Recipients    bool `json:"recipients"`      // structured To/Cc/Bcc
	Deletes       bool `json:"deletes"`         // reports deletions/moves
	LiveEvents    bool `json:"live_events"`     // pushes change notifications
	OwnerIdentity bool `json:"owner_identity"`  // reports each mailbox's owner addresses (Mailbox.Addresses)
	AddressedToMe bool `json:"addressed_to_me"` // per-message addressed-to-me flags (Message.AddressedToMe)
	FolderRoles   bool `json:"folder_roles"`    // folders carry their role from the source (inbox, sent…), never from names
	MessageClass  bool `json:"message_class"`   // item classes (meeting, report…: Message.MessageClass)
	Headers       bool `json:"headers"`         // transport headers (Message.Headers)
	MultiFolder   bool `json:"multi_folder"`    // one message can be in several folders (Message.Folders)
	Attachments   bool `json:"attachments"`     // attachment metadata (Message.Attachments)
	// The owner's actions the source records on each message.
	ReadState  bool `json:"read_state"` // read or unread (Message.IsRead)
	Flags      bool `json:"flags"`      // follow-up flags and their due dates (FlagStatus, TaskDueDate)
	Categories bool `json:"categories"` // the owner's categories (Message.Categories)
	LastVerb   bool `json:"last_verb"`  // the last reply/forward done on it (LastVerbExecuted)
	// The non-mail half of the world model.
	Contacts bool `json:"contacts"` // address books (source.ContactSource)
	Calendar bool `json:"calendar"` // calendar occurrences (source.CalendarSource)
}

// CapabilityList is the wire form of a source's capability set: the
// capability names (the JSON keys of Capabilities) in declaration order. It
// is the one vocabulary and the one order the layers above render, so no
// consumer keeps a second list to drift from the struct.
type CapabilityList []string

// List returns the capabilities c advertises, in the one stable order. A
// source with no capabilities answers an empty (never nil) list.
func (c Capabilities) List() CapabilityList {
	t := reflect.TypeFor[Capabilities]()
	v := reflect.ValueOf(c)
	out := make(CapabilityList, 0, t.NumField())
	for i := range t.NumField() {
		if v.Field(i).Bool() {
			out = append(out, t.Field(i).Tag.Get("json"))
		}
	}
	return out
}

// Cursor is an opaque, connector-defined sync position ("" = from scratch).
type Cursor string

// Progress is a connector's position within one folder sync.
type Progress struct {
	Done  int64  `json:"done"`
	Total int64  `json:"total"` // 0 when unknown
	Unit  string `json:"unit"`  // "items", "bytes"
}

// Version is one entry of a folder snapshot: a message id and its change
// stamp (stored form of the source's last modification time).
type Version struct {
	ID    string `json:"id"`
	Stamp string `json:"stamp"`
}

// Sink receives a folder sync. Message returning an error is an OUTPUT
// failure: the connector must stop and return it. Skip records an
// item-level failure (the item is counted, the sync goes on).
//
// Snapshot is optional for connectors: it hands the sink every message
// currently in the folder and returns the ids whose stored copy is missing
// or has another stamp (the ones to deliver). complete=true promises the
// list is the whole folder, so stored messages absent from it are removed
// at the end of the run (moves and deletes); pass false when the source
// changed while the snapshot was read.
type Sink interface {
	Message(ctx context.Context, m mail.Message) error
	Skip(ref string, err error)
	Progress(p Progress)
	Snapshot(ctx context.Context, items []Version, complete bool) (map[string]bool, error)
}

// ItemRef names one stored message of a source.
type ItemRef struct {
	StoreID string `json:"store_id"`
	ID      string `json:"id"`
}

// DetailReader is implemented by connectors whose Sync delivers Partial
// messages: the syncer then completes stored partial messages, newest
// first, one ReadDetails call each. An *ItemError means only that item
// failed (it stays partial and is retried next run); any other error stops
// the detail pass.
type DetailReader interface {
	ReadDetails(ctx context.Context, ref ItemRef) (mail.Details, error)
}

// ItemError marks a failure limited to one item. Quarantine: the item
// itself made the source fail as a whole (Outlook raised an exception
// reading it): it is set aside — never read again automatically — and Err
// may also be an ErrUnavailable that ends the run.
type ItemError struct {
	Code       string // short machine-readable cause (e.g. an HRESULT), for counting
	Err        error
	Quarantine bool
}

func (e *ItemError) Error() string { return e.Err.Error() }
func (e *ItemError) Unwrap() error { return e.Err }

// Connector is one configured mail source.
type Connector interface {
	ID() string
	Kind() string
	Capabilities() Capabilities
	// Discover lists the folders of the picked mailboxes (SettingSelected)
	// and refreshes the mailbox list (Mailboxes). It never reads a
	// message.
	Discover(ctx context.Context) ([]mail.Folder, error)
	// Mailboxes lists every mailbox store the last Discover saw, picked
	// or not, with its key, kind, counts and state, and for picked ones
	// the owner addresses the source reports.
	Mailboxes() []Mailbox
	// Sync streams the folder's messages changed since cursor into sink and
	// returns the cursor to store once the sync is known to be complete.
	Sync(ctx context.Context, folder mail.Folder, cursor Cursor, sink Sink) (Cursor, error)
}

// Starter is implemented by connectors whose application can be started at
// the owner's request. Background work never calls it.
type Starter interface {
	StartOutlook(ctx context.Context) error
}

// Resumer is implemented by connectors whose connection stopped after
// repeated losses and waits for the owner to resume it.
type Resumer interface {
	Resume() error
}

// OwnerAction names one action the owner can trigger on a source outside a
// sync. These are the action path segments and the names /api/status
// advertises, so a screen offers an action only when the source implements it.
type OwnerAction string

// Owner actions.
const (
	ActionStart        OwnerAction = "start"         // start the source's application (Starter)
	ActionResume       OwnerAction = "resume"        // resume a connection that gave up (Resumer)
	ActionRetryDetails OwnerAction = "retry-details" // retry messages whose detail read failed (DetailReader)
)

// ownerAction is one entry of the owner-action table: the wire name and the
// optional interface a connector must implement. The table is the one owner
// of both the order screens render and the support rule the dispatcher asks,
// so what /api/status advertises cannot drift from what a route can run.
type ownerAction struct {
	name       OwnerAction
	implements func(Connector) bool
}

var ownerActions = []ownerAction{
	{ActionStart, func(c Connector) bool { _, ok := c.(Starter); return ok }},
	{ActionResume, func(c Connector) bool { _, ok := c.(Resumer); return ok }},
	{ActionRetryDetails, func(c Connector) bool { _, ok := c.(DetailReader); return ok }},
}

// OwnerActions lists every owner action, in the one order screens render.
func OwnerActions() []OwnerAction {
	out := make([]OwnerAction, 0, len(ownerActions))
	for _, a := range ownerActions {
		out = append(out, a.name)
	}
	return out
}

// ConnectorActions lists the owner actions c implements, in OwnerActions
// order. A source with no optional interface answers an empty (never nil) list.
func ConnectorActions(c Connector) []OwnerAction {
	out := make([]OwnerAction, 0, len(ownerActions))
	for _, a := range ownerActions {
		if a.implements(c) {
			out = append(out, a.name)
		}
	}
	return out
}

// ImplementsAction reports whether c implements one owner action. An unknown
// action is false, the same answer as an action no connector implements.
func ImplementsAction(c Connector, action OwnerAction) bool {
	for _, a := range ownerActions {
		if a.name == action {
			return a.implements(c)
		}
	}
	return false
}

// FolderStatus is what a connector knows about a stored folder that its
// last Discover did not return.
type FolderStatus int

// Folder statuses.
const (
	FolderUnknown   FolderStatus = iota // cannot tell (store offline, no id): leave it alone
	FolderNotSynced                     // still exists but is not synced (store disabled, folder excluded)
	FolderGone                          // no longer exists at the source
)

// FolderProber is implemented by connectors that can check a folder that
// discovery no longer returned. The syncer removes a gone folder's
// messages (behind its removal safety valve) and marks a not-synced
// folder inactive: its mail is kept, outside scope and statistics.
type FolderProber interface {
	FolderStatus(ctx context.Context, f mail.Folder) (FolderStatus, error)
}

// Phase names a step of a sync: one vocabulary for connector measurements
// and syncer progress.
type Phase string

// Phases.
const (
	PhaseDiscover      Phase = "discover"       // the folder list of a source
	PhaseDiscoverStore Phase = "discover-store" // one store's part of it
	PhaseSnapshot      Phase = "snapshot"       // id/stamp list of one folder
	PhaseTable         Phase = "table"          // changed rows of one folder
	PhaseMessages      Phase = "messages"       // a folder's messages are written
	PhaseDetails       Phase = "details"        // partial messages are completed
)

// StateKeeper keeps a connector's own state across app restarts (the
// store implements it): a value per source and name, opaque to the keeper.
type StateKeeper interface {
	// LoadState returns the value kept for (sourceID, name), nil when none.
	LoadState(ctx context.Context, sourceID, name string) ([]byte, error)
	// SaveState replaces it.
	SaveState(ctx context.Context, sourceID, name string, value []byte) error
}
