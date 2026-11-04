package main

import (
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

// unavailableResetter is the indexer's process-local backoff reset. The
// interface keeps internal/index free of a sidecar import.
type unavailableResetter interface{ ResetUnavailable() }

// watchRuntimeReady ends the index's unavailable-embedder wait when the
// managed runtime becomes ready: a runtime that just started can serve the
// next reconcile even though the last run failed. Other states (starting,
// installing, failed) change nothing.
func watchRuntimeReady(r unavailableResetter, bus *events.Bus, buffer int) (stop func(), err error) {
	return events.On(bus, sidecar.TopicState, buffer, func(st sidecar.Status) {
		if st.State == sidecar.StateReady {
			r.ResetUnavailable()
		}
	})
}
