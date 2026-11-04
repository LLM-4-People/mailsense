package outlook

import (
	"github.com/LLM-4-People/mailsense/internal/source"
)

// StoreInfo is one store of the profile as the last Discover saw it,
// picked or not.
type StoreInfo struct {
	source.Mailbox
	Selected bool `json:"selected"`
}

// Recount makes the next Discover count the unpicked stores again (their
// counts are otherwise kept for the connector's lifetime: walking an online
// archive costs tens of seconds).
func (c *Connector) Recount() {
	c.mu.Lock()
	c.counted = map[string]StoreInfo{}
	c.mu.Unlock()
}

// keyOf is the stable key of a store id. Callers hold no lock: it is pure.
func (c *Connector) keyOf(storeID string) string { return source.StoreKey(c.id, storeID) }

// Stores returns the store list of the last Discover.
func (c *Connector) Stores() []StoreInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]StoreInfo(nil), c.stores...)
}

// Mailboxes returns the mailboxes of the last Discover (source.Connector).
func (c *Connector) Mailboxes() []source.Mailbox {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]source.Mailbox, 0, len(c.stores))
	for _, in := range c.stores {
		out = append(out, in.Mailbox)
	}
	return out
}
