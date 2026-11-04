package source

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// RegistryChange is published when a connector is added or removed.
type RegistryChange struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Action string `json:"action"` // "add" or "remove"
}

// TopicRegistry carries registry changes.
var TopicRegistry = events.NewTopic[RegistryChange]("source.registry")

// Registry holds the configured connectors by id.
type Registry struct {
	mu        sync.RWMutex
	byID      map[string]Connector
	bus       *events.Bus
	closeOnce sync.Once
}

// NewRegistry returns an empty registry; bus may be nil.
func NewRegistry(bus *events.Bus) *Registry { return &Registry{byID: map[string]Connector{}, bus: bus} }

// Close closes every registered connector that owns resources. It is
// idempotent; the app calls it once on shutdown, after background jobs
// have stopped and before the store closes.
func (r *Registry) Close() {
	r.closeOnce.Do(func() {
		for _, c := range r.List() {
			if closer, ok := c.(interface{ Close() }); ok {
				closer.Close()
			}
		}
	})
}

// Add registers a connector; ids are unique and non-empty.
func (r *Registry) Add(c Connector) error {
	if c == nil || c.ID() == "" || c.Kind() == "" {
		return fmt.Errorf("source: connector needs an id and a kind")
	}
	r.mu.Lock()
	if _, dup := r.byID[c.ID()]; dup {
		r.mu.Unlock()
		return fmt.Errorf("source: connector %q already registered", c.ID())
	}
	r.byID[c.ID()] = c
	r.mu.Unlock()
	r.publish(c, "add")
	return nil
}

// Remove unregisters a connector; it reports whether it existed.
func (r *Registry) Remove(id string) bool {
	r.mu.Lock()
	c, ok := r.byID[id]
	delete(r.byID, id)
	r.mu.Unlock()
	if ok {
		r.publish(c, "remove")
	}
	return ok
}

func (r *Registry) publish(c Connector, action string) {
	if r.bus != nil {
		events.Publish(r.bus, TopicRegistry, RegistryChange{ID: c.ID(), Kind: c.Kind(), Action: action})
	}
}

// Get returns a connector by id.
func (r *Registry) Get(id string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[id]
	return c, ok
}

// List returns the connectors sorted by id.
func (r *Registry) List() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Connector, 0, len(r.byID))
	for _, id := range slices.Sorted(maps.Keys(r.byID)) {
		out = append(out, r.byID[id])
	}
	return out
}
