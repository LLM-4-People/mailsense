package api

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// Operational constants of the API surface. There is no api.* group in
// the settings schema (phase 4 did not add one); these are the contract's
// defaults, owned here and restart-scoped like a restart_required setting.
const (
	descriptorFile     = "runtime/api.json"
	bindAddr           = "127.0.0.1:0"
	tokenBytes         = 32
	launchBytes        = 8
	descriptorVersion  = 1
	descriptorAttempts = 5
	descriptorDelay    = 100 * time.Millisecond

	defaultMaxBytes        int64 = 1 << 20
	defaultRequestTimeout        = 30 * time.Second
	defaultLongTimeout           = 5 * time.Minute
	defaultSSEHeartbeat          = 15 * time.Second
	defaultSSEWriteTimeout       = 10 * time.Second
	defaultSSEBuffer             = 512
	defaultSSEMaxClients         = 8
	defaultLogsLimit             = 500
	// maxLogsPage is the API page ceiling, independent of log.ring.size:
	// the ring may retain 100,000 entries, but one response never carries
	// more than this many. A larger limit is rejected, not clamped.
	maxLogsPage            = 5000
	defaultShutdownTimeout = 30 * time.Second
	maxHeaderBytes         = 64 << 10
	sseRetryMillis         = 3000
)

// Options is the one mapping from configuration to the server. New
// refuses zero values instead of inventing a hidden default; production
// uses OptionsFromConfig.
type Options struct {
	Token           string // tests only; "" = generate a per-launch token
	DescriptorPath  string // "" = no descriptor (tests)
	Replace         fsx.Replace
	MaxBytes        int64
	RequestTimeout  time.Duration
	LongTimeout     time.Duration
	SSEHeartbeat    time.Duration
	SSEWriteTimeout time.Duration
	SSEBuffer       int
	SSEMaxClients   int
	EventsBuffer    int
	ShutdownTimeout time.Duration
	Clock           clock.Clock
}

// OptionsFromConfig is the only production source of the API options.
// events.buffer_size is the existing owner of the one value the contract
// would otherwise duplicate as an api.* setting. The logs page bound is an
// API constant: log.ring.size owns retention, not response size.
func OptionsFromConfig(cfg *config.Config) Options {
	return Options{
		DescriptorPath:  cfg.Paths().Resolve(descriptorFile),
		Replace:         fsx.Replace{Attempts: descriptorAttempts, Delay: descriptorDelay},
		MaxBytes:        defaultMaxBytes,
		RequestTimeout:  defaultRequestTimeout,
		LongTimeout:     defaultLongTimeout,
		SSEHeartbeat:    defaultSSEHeartbeat,
		SSEWriteTimeout: defaultSSEWriteTimeout,
		SSEBuffer:       defaultSSEBuffer,
		SSEMaxClients:   defaultSSEMaxClients,
		EventsBuffer:    cfg.EventBuffer(),
		ShutdownTimeout: defaultShutdownTimeout,
	}
}

func (o Options) validate() error {
	switch {
	case o.MaxBytes <= 0:
		return errOption("max bytes")
	case o.RequestTimeout <= 0:
		return errOption("request timeout")
	case o.LongTimeout <= 0:
		return errOption("long request timeout")
	case o.SSEHeartbeat <= 0:
		return errOption("SSE heartbeat")
	case o.SSEWriteTimeout <= 0:
		return errOption("SSE write timeout")
	case o.SSEBuffer <= 0:
		return errOption("SSE buffer")
	case o.SSEMaxClients <= 0:
		return errOption("SSE client limit")
	case o.EventsBuffer <= 0:
		return errOption("event buffer")
	case o.ShutdownTimeout <= 0:
		return errOption("shutdown timeout")
	}
	return nil
}

type optionError string

func (e optionError) Error() string { return "api: invalid options: " + string(e) }

func errOption(what string) error { return optionError(what + " must be positive") }
