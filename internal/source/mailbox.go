package source

import "github.com/LLM-4-People/mailsense/internal/mail"

// Mailbox is one mailbox store of a source as its last Discover saw it,
// for the picking screen and the learning status.
type Mailbox struct {
	Key      string         `json:"key"` // StoreKey(SourceID, StoreID)
	SourceID string         `json:"source_id"`
	StoreID  string         `json:"store_id"`
	Name     string         `json:"name"`
	Kind     mail.StoreKind `json:"kind"`
	// Default marks the profile's default store (Outlook: Namespace.DefaultStore);
	// shown to the user, never acted upon.
	Default bool `json:"default"`
	// Cached is true when the source reads a local copy (Outlook: Cached
	// Exchange Mode); otherwise every read goes to the server.
	Cached bool `json:"cached"`
	// Folders and Items count the store's mail folders and their items
	// (-1 = not counted yet); CountedAt is when an unpicked store was
	// counted (picked stores are counted at every Discover).
	Folders   int    `json:"folders"`
	Items     int64  `json:"items"`
	CountedAt string `json:"counted_at,omitempty"`
	// Addresses is the owner identity the source reports for a picked
	// store: the primary address first, then aliases (other SMTP proxy
	// addresses, the Exchange DN). Empty when the source reports none.
	Addresses []string `json:"addresses,omitempty"`
	// IdentityComplete is true when the source read the owner identity
	// completely (every read it needs succeeded; none at all is complete
	// for a store that names no owner). Only a complete read replaces the
	// recorded identity: an offline or partly read store keeps it.
	IdentityComplete bool `json:"identity_complete"`
	// Error is set when the store could not be read (offline, no rights):
	// a picked store in error is reported and waited on, never replaced.
	Error string `json:"error,omitempty"`
}

// Online reports whether the last Discover could read the store.
func (m Mailbox) Online() bool { return m.Error == "" }
