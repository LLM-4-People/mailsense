package threadintel

import (
	"hash/maphash"
	"sync"
)

// DefaultEvidenceEntries bounds the process-wide parsed-evidence cache. It
// covers a 65k-message mailbox, which is larger than the fixture and the
// documented mailbox target; a larger mailbox keeps the most recently parsed
// entries and re-parses the rest instead of growing without a cap.
const DefaultEvidenceEntries = 1 << 16

// parsedEvidence is the protocol-derived part of one message: whether it is
// machine-generated and whether it is a real thread move. It is derived only
// from MessageClass, Headers, Body, HasAttachments and Options.MinBodyRunes
// and holds no mail content. Every other field the resolver reads (workflow,
// verdict, role, sender, recipients, signal) stays on the live Message and is
// evaluated fresh on every pass, so a cache hit cannot freeze a decision that
// metadata changed.
type parsedEvidence struct {
	autoReply   bool
	substantive bool
}

// evidenceKey is the exact input of one parsed-evidence decision. Revision
// separates two writes of the same row; digest separates two different
// contents that happen to share a row id and revision (a deleted row whose
// id is reused by a later import, or a database replaced on disk). The
// digest is a guard, not the primary change marker: every write of Body,
// Headers or MessageClass goes through the message upsert that bumps
// messages.revision (store.messageUpsertSQL).
type evidenceKey struct {
	rowID    int64
	revision int64
	digest   uint64
	min      int64
	hasAtt   bool
}

// evidenceSeed makes the content digest process-local and unguessable: an
// untrusted body cannot be crafted to collide with another entry.
var evidenceSeed = maphash.MakeSeed()

// evidenceKeyOf digests the exact content the parse reads. RowID, revision,
// min and HasAttachments complete the key, so two calls can share an entry
// only when every input of the decision is identical.
func evidenceKeyOf(m *Message, min int) evidenceKey {
	var h maphash.Hash
	h.SetSeed(evidenceSeed)
	h.WriteString(m.MessageClass)
	h.WriteByte(0)
	h.WriteString(m.Headers)
	h.WriteByte(0)
	h.WriteString(m.Body)
	return evidenceKey{
		rowID:    m.RowID,
		revision: m.Revision,
		digest:   h.Sum64(),
		min:      int64(min),
		hasAtt:   m.HasAttachments,
	}
}

// CacheStats is the observable state of one evidence cache. Parses counts the
// messages whose Headers and Body this process had to parse: every miss is
// exactly one call into the parse path that invokes mail.ParseHeaders, so an
// unchanged second build with all entries present reports Parses == 0.
type CacheStats struct {
	Entries   int
	Hits      uint64
	Parses    uint64
	Evictions uint64
}

// Cache is a bounded, concurrency-safe cache of parsed message evidence. It
// keeps decisions, never bodies, subjects or addresses, and every lookup is
// keyed by the content that produced the decision, so an entry that outlives
// a store close can only answer for the same bytes. Closing a store therefore
// never serves content: the cache holds none and the next read of a different
// message misses on the digest.
type Cache struct {
	mu      sync.Mutex
	max     int
	entries map[evidenceKey]parsedEvidence
	ring    []evidenceKey
	next    int
	hits    uint64
	parses  uint64
	evict   uint64
}

// NewCache returns an isolated cache retaining at most max entries. A
// non-positive bound selects DefaultEvidenceEntries.
func NewCache(max int) *Cache {
	if max <= 0 {
		max = DefaultEvidenceEntries
	}
	return &Cache{max: max, entries: make(map[evidenceKey]parsedEvidence), ring: make([]evidenceKey, 0, max)}
}

// sharedEvidence is the one process-wide cache every store resolution pass
// uses, so two builds over unchanged mail reuse the same parsed evidence.
var sharedEvidence = NewCache(DefaultEvidenceEntries)

// SharedCache returns the process-wide parsed-evidence cache. It is bounded
// and safe for concurrent builds.
func SharedCache() *Cache { return sharedEvidence }

// Get returns the cached decision for key.
func (c *Cache) get(key evidenceKey) (parsedEvidence, bool) {
	c.mu.Lock()
	ev, ok := c.entries[key]
	if ok {
		c.hits++
	}
	c.mu.Unlock()
	return ev, ok
}

// put stores one decision, evicting the oldest entry when the bound is full.
func (c *Cache) put(key evidenceKey, ev parsedEvidence) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		c.entries[key] = ev
		return
	}
	if len(c.ring) < c.max {
		c.ring = append(c.ring, key)
	} else {
		delete(c.entries, c.ring[c.next])
		c.ring[c.next] = key
		c.next = (c.next + 1) % c.max
		c.evict++
	}
	c.entries[key] = ev
}

// recordParse accounts for one parsed message. It is called only on the miss
// path, immediately before the message's Headers and Body are parsed.
func (c *Cache) recordParse() {
	c.mu.Lock()
	c.parses++
	c.mu.Unlock()
}

// Stats reports the cache's current size and lifetime counters.
func (c *Cache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Entries: len(c.entries), Hits: c.hits, Parses: c.parses, Evictions: c.evict}
}

// Reset drops every entry and counter. Tests and benchmarks use it to start
// from a cold cache; production never needs it.
func (c *Cache) Reset() {
	c.mu.Lock()
	c.entries = make(map[evidenceKey]parsedEvidence, c.max)
	c.ring = c.ring[:0]
	c.next = 0
	c.hits, c.parses, c.evict = 0, 0, 0
	c.mu.Unlock()
}
