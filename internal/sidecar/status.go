package sidecar

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// State is the lifecycle state of a managed runtime.
type State string

// States.
const (
	StateIdle       State = "idle"       // never started in this process
	StateInstalling State = "installing" // downloading or unpacking (see the install job)
	StateStarting   State = "starting"   // launched, waiting for /health
	StateTuning     State = "tuning"     // measuring thread counts (first start)
	StateCanary     State = "canary"     // checking a GPU build against the reference vector
	StateProbing    State = "probing"    // auto: measuring a GPU build
	StateReady      State = "ready"
	StateRestarting State = "restarting" // died or stopped answering: waiting to start again
	StateFailed     State = "failed"     // gave up (the reason says why; the next use tries again)
	StateStopped    State = "stopped"
)

// Status is what the UI shows about a runtime.
type Status struct {
	Runtime   string `json:"runtime"`
	Model     string `json:"model"`
	Requested string `json:"requested"` // sidecar.variant (auto or a build)
	Variant   string `json:"variant"`   // the build running (cpu only where the platform has one)
	State     State  `json:"state"`
	Detail    string `json:"detail,omitempty"`
	// Fallback says why the build running is not the one asked for (a
	// broken GPU build, auto's choice of cpu); Manager.Learned has every
	// probe.
	Fallback string `json:"fallback,omitempty"`
	// Canary is the cosine of the GPU build's canary vector with the
	// reference (0 for cpu builds).
	Canary   float64   `json:"canary,omitempty"`
	PID      int       `json:"pid,omitempty"`
	Threads  int       `json:"threads,omitempty"`
	Restarts int       `json:"restarts"`
	Since    time.Time `json:"since"`
}

// TopicState carries every state change of a managed runtime.
var TopicState = events.NewTopic[Status]("sidecar.state")
