package evidence

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// contextChars is the length of a message's context digest.
const contextChars = 16

// contexts are the parts of a run's message context digests: what a
// message-only value reads besides the message itself (its source's
// capabilities, its store's owner, the owners' organisations).
type contexts struct {
	caps   map[string]string // source id → its capabilities (JSON)
	owners map[string]string // store key → its owner's addresses (JSON)
	orgs   string            // the owners' organisations (JSON, sorted)
}

func newContexts(env *Env) *contexts {
	c := &contexts{caps: map[string]string{}, owners: map[string]string{}}
	for id, caps := range env.caps {
		raw, _ := json.Marshal(caps)
		c.caps[id] = string(raw)
	}
	for key, id := range env.owners {
		raw, _ := json.Marshal(id.Addresses)
		c.owners[key] = string(raw)
	}
	raw, _ := json.Marshal(slices.Sorted(maps.Keys(env.orgs)))
	c.orgs = string(raw)
	return c
}

// of is the context digest of one message: its source's capabilities,
// its store's owner identity, the owners' organisations, its scope
// verdict and reason. It is stored with the values; a stored
// message-only value is reused only under the same digest.
func (c *contexts) of(r *store.MessageRow) string {
	key := source.StoreKey(r.Message.SourceID, r.Message.StoreID)
	parts := []string{c.caps[r.Message.SourceID], c.owners[key], c.orgs, string(r.Verdict), string(r.Reason)}
	raw, _ := json.Marshal(parts)
	return digest.Hex(raw, contextChars)
}
