package store

import (
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Migrated is published for every migration applied at Open.
type Migrated struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
}

// RunChanged is published when a sync run starts and when it finishes.
type RunChanged struct {
	RunID    int64          `json:"run_id"`
	SourceID string         `json:"source_id"`
	State    runstate.State `json:"state"`
	Totals   RunTotals      `json:"totals"`
	Error    string         `json:"error,omitempty"`
}

// Topics published by the store.
var (
	TopicMigrated   = events.NewTopic[Migrated]("store.migrated")
	TopicRunChanged = events.NewTopic[RunChanged]("store.run")
)

func publish[T any](s *Store, topic events.Topic[T], v T) {
	if s.bus != nil {
		events.Publish(s.bus, topic, v)
	}
}
