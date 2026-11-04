package mail

import "fmt"

// FolderRole is what a folder is for, independent of its localized name.
type FolderRole string

// Folder roles.
const (
	RoleInbox   FolderRole = "inbox"
	RoleSent    FolderRole = "sent"
	RoleDrafts  FolderRole = "drafts"
	RoleDeleted FolderRole = "deleted"
	RoleJunk    FolderRole = "junk"
	RoleArchive FolderRole = "archive"
	RoleOther   FolderRole = "other"
)

// Roles lists every valid role (a fresh slice each call).
func Roles() []FolderRole {
	return []FolderRole{RoleInbox, RoleSent, RoleDrafts, RoleDeleted, RoleJunk, RoleArchive, RoleOther}
}

// ParseRole validates a role name.
func ParseRole(s string) (FolderRole, error) {
	for _, r := range Roles() {
		if string(r) == s {
			return r, nil
		}
	}
	return "", fmt.Errorf("mail: unknown folder role %q", s)
}

// StoreKind is the kind of mailbox a store is, as its connector reports
// it. It is shown when the user picks mailboxes and never selects or
// substitutes anything by itself: nothing syncs until a store is picked.
type StoreKind string

// Store kinds.
const (
	StorePrimary  StoreKind = "primary"  // a mailbox of the profile's own account(s)
	StoreDelegate StoreKind = "delegate" // someone else's mailbox opened by the user
	StoreShared   StoreKind = "shared"   // an additional (shared) mailbox
	StoreArchive  StoreKind = "archive"  // an online archive of one of the user's mailboxes
	StorePublic   StoreKind = "public"   // public folders
	StorePST      StoreKind = "pst"      // a personal data file
	StoreOther    StoreKind = "other"    // anything else (a non-Exchange account, a file import)
)

// StoreKinds lists every valid kind (a fresh slice each call).
func StoreKinds() []StoreKind {
	return []StoreKind{StorePrimary, StoreDelegate, StoreShared, StoreArchive, StorePublic, StorePST, StoreOther}
}

// Valid reports whether k is one of StoreKinds.
func (k StoreKind) Valid() bool {
	for _, v := range StoreKinds() {
		if v == k {
			return true
		}
	}
	return false
}

// OwnerContent reports whether a store of this kind can contribute
// owner-facing content. A shared, delegate or public store may change thread
// state, but its subjects, bodies, snippets and participants are not the
// owner's content.
func (k StoreKind) OwnerContent() bool {
	switch k {
	case StoreShared, StoreDelegate, StorePublic:
		return false
	}
	return true
}

// Folder is one mail folder of a source.
type Folder struct {
	SourceID  string     `json:"source_id"`
	StoreID   string     `json:"store_id"`
	StoreName string     `json:"store_name"`
	StoreKind StoreKind  `json:"store_kind"`
	Path      string     `json:"path"`
	Role      FolderRole `json:"role"`
	ItemCount int64      `json:"item_count"` // -1 when unknown
	// RemoteID is the source's own id of the folder (Outlook: its EntryID),
	// so a folder that is no longer discovered can be checked for existence.
	RemoteID string `json:"remote_id"`
}

// FolderOf returns the folder a message claims to live in.
func FolderOf(m *Message) Folder {
	return Folder{SourceID: m.SourceID, StoreID: m.StoreID, StoreName: m.StoreName, StoreKind: StoreOther, Path: m.FolderPath, Role: m.FolderRole, ItemCount: -1}
}
