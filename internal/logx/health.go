package logx

import (
	"sync"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// Health is the state of the logging pipeline, for the UI.
type Health struct {
	FileErrors    uint64 `json:"file_errors"`     // failed file writes since Setup
	FileFailing   bool   `json:"file_failing"`    // the last file write failed
	LastFileError string `json:"last_file_error"` // redacted
	BusDropped    uint64 `json:"bus_dropped"`     // events dropped by full subscribers (whole bus)
}

// TopicHealth is published when file logging starts or stops failing.
var TopicHealth = events.NewTopic[Health]("log.health")

// health tracks file-write outcomes and publishes transitions.
type health struct {
	mu     sync.Mutex
	state  Health
	bus    *events.Bus
	redact func(string) string
}

func (h *health) fileResult(err error) {
	h.mu.Lock()
	changed := (err != nil) != h.state.FileFailing
	if err != nil {
		h.state.FileErrors++
		h.state.LastFileError = err.Error()
		if h.redact != nil {
			h.state.LastFileError = h.redact(h.state.LastFileError)
		}
	}
	h.state.FileFailing = err != nil
	snap := h.state
	h.mu.Unlock()
	if changed && h.bus != nil {
		snap.BusDropped = h.bus.Dropped()
		events.Publish(h.bus, TopicHealth, snap)
	}
}

func (h *health) snapshot() Health {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.state
	if h.bus != nil {
		s.BusDropped = h.bus.Dropped()
	}
	return s
}
