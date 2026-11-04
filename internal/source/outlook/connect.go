package outlook

import (
	"context"
	"fmt"
	"time"
)

// host is the operating-system side of the connector: the Windows COM
// implementation (host_windows.go) or the fake object model of the tests
// (fake_test.go).
type host interface {
	// Init and Uninit run on the apartment thread (CoInitializeEx / CoUninitialize).
	Init() error
	Uninit()
	// Elevated reports whether this process runs elevated.
	Elevated() (bool, error)
	// Registered fails when classic Outlook's ProgID is not registered.
	Registered() error
	// NewOutlook reports whether the profile is switched to the new Outlook.
	NewOutlook() bool
	// Attach returns the running Outlook.Application (MK_E_UNAVAILABLE when
	// none). It is the ONLY way the connector obtains Outlook: background
	// work never starts it.
	Attach() (Object, error)
	// Start starts classic Outlook as the user would (its own window, the
	// default profile); only the user's explicit action calls it
	// (Connector.StartOutlook).
	Start() error
	// Started is when the running Outlook process of this session started
	// (ErrNotRunning when none runs).
	Started() (time.Time, error)
	// UserIdle is how long the interactive user has given no input.
	UserIdle() (time.Duration, error)
}

// session is a live connection: the Application and its MAPI Namespace,
// both unguarded (views wrap them per operation and never release them).
type session struct {
	app, ns Object
}

func (s *session) close() {
	s.ns.Release()
	s.app.Release()
}

// connect checks the preconditions in order and attaches to the running
// Outlook. It never starts Outlook: a hidden Outlook started by a
// background job (no window, possibly racing Windows' own restart after a
// crash) is exactly what must not happen. Each failure is a named error
// the user can act on (errors.go).
func connect(ctx context.Context, h host, root func(context.Context, Object, string) *guard) (*session, error) {
	elevated, err := h.Elevated()
	if err != nil {
		return nil, fmt.Errorf("outlook: cannot read this process's elevation: %w", err)
	}
	if elevated {
		return nil, ErrElevated
	}
	if err := h.Registered(); err != nil {
		return nil, fmt.Errorf("%w (%w)", ErrNoClassicOutlook, err)
	}
	app, err := h.Attach()
	if err != nil {
		hr, _ := HResult(err)
		switch {
		case sessionLost(hr): // Outlook died or is stopping while being attached: a lost connection
			return nil, fmt.Errorf("%w: attaching: %w", ErrSessionLost, err)
		case hr == hrServerExecFailure:
			return nil, fmt.Errorf("%w (%w)", ErrIntegrity, err)
		case hr != hrUnavailable:
			return nil, fmt.Errorf("%w: attaching failed: %w", ErrNotRunning, err)
		case h.NewOutlook():
			return nil, ErrNewOutlook
		}
		return nil, ErrNotRunning
	}
	v, err := root(ctx, app, classApplication).Call("GetNamespace", "MAPI")
	if err != nil {
		app.Release()
		if unavailable(err) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: GetNamespace: %w", ErrNotRunning, err)
	}
	return &session{app: app, ns: v.(*guard).inner}, nil
}
