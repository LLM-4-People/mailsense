package outlook

import (
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// User-facing preconditions and failures. Each names what to do.
var (
	ErrUnsupported = errors.New("outlook: the Outlook connector runs on Windows only")
	ErrElevated    = source.UnavailableCause("outlook_elevated",
		"outlook: Mailsense is running as administrator, and classic Outlook refuses COM calls "+
			"from an elevated process. Start Mailsense normally (not \"Run as administrator\")")
	ErrNoClassicOutlook = source.UnavailableCause("outlook_not_installed",
		"outlook: classic Outlook for Windows is not installed (Outlook.Application is not registered). "+
			"The new Outlook has no COM interface; install or switch back to classic Outlook")
	// ErrOutlookExeNotFound: classic Outlook is installed, but its App
	// Paths registration (the absolute path of OUTLOOK.EXE, apppaths.go)
	// resolved nowhere, so it cannot be started safely.
	ErrOutlookExeNotFound = source.UnavailableCause("outlook_exe_not_found",
		"outlook: classic Outlook's executable could not be located (no App Paths registration for OUTLOOK.EXE under HKLM or HKCU). "+
			"Repair or reinstall classic Outlook")
	ErrNewOutlook = source.UnavailableCause("outlook_new_outlook",
		"outlook: classic Outlook is not running and this profile is switched to the new Outlook "+
			"(UseNewOutlook=1), which has no COM interface. Start classic Outlook, or turn off \"New Outlook\"")
	ErrIntegrity = source.UnavailableCause("outlook_integrity",
		"outlook: Outlook and Mailsense run at different privilege levels (one of them as administrator). "+
			"Run both normally")
	ErrWrongProfile = source.UnavailableCause("outlook_wrong_profile",
		"outlook: Outlook is running with a different mail profile than outlook.profile. "+
			"Close Outlook, then use Start Outlook to open the configured profile")
	// ErrNotRunning: Mailsense only attaches to a running Outlook; it starts
	// Outlook only when the user asks (Connector.StartOutlook).
	ErrNotRunning = source.UnavailableCause("outlook_not_running",
		"outlook: classic Outlook is not running. Start Outlook (or use \"Start Outlook\"); "+
			"Mailsense never starts it on its own")
	// ErrSessionLost: Outlook died, closed or restarted under a call; the
	// breaker (breaker.go) decides when to reconnect.
	ErrSessionLost = source.UnavailableCause("outlook_session_lost",
		"outlook: the connection to Outlook was lost (Outlook closed, crashed or restarted)")
	// ErrCoolingDown, ErrOutlookWarmingUp and ErrStopped: the breaker
	// refuses to reconnect after lost connections (breaker.go).
	ErrCoolingDown = source.UnavailableCause("outlook_cooling_down",
		"outlook: the connection to Outlook was lost; Mailsense waits before reconnecting")
	ErrOutlookWarmingUp = source.UnavailableCause("outlook_warming_up",
		"outlook: Outlook restarted recently; Mailsense waits until it has run for a while "+
			"before reading again")
	ErrStopped = source.UnavailableCause("outlook_stopped",
		"outlook: the connection to Outlook was lost too often; syncing Outlook is stopped "+
			"until you resume it (\"Resume Outlook sync\")")
	// ErrOutlookBusy: Outlook declined a call through every retry
	// (outlook.com.retry.*: a dialog is open, or it is working hard).
	ErrOutlookBusy = source.UnavailableCause("outlook_busy",
		"outlook: Outlook stayed busy through every retry (a dialog may be open); "+
			"Mailsense stops reading for now and tries again on the next run")
	ErrStoreOffline = errors.New("outlook: the mailbox is offline or its server cannot be reached")
	ErrNotAllowed   = errors.New("outlook: refused a call that is not on the connector's read-only list")
	ErrNoObject     = errors.New("outlook: Outlook returned no object where one is required")
	ErrInternal     = errors.New("outlook: internal error in the Outlook connector")
)

// classify turns a failed call into the named error a user can act on,
// keeping the HRESULT in the chain.
func classify(err error) error {
	hr, ok := HResult(err)
	switch {
	case !ok:
		return err
	case sessionLost(hr):
		return fmt.Errorf("%w: %w", ErrSessionLost, err)
	case retryable(hr): // the guard retried it outlook.com.retry.attempts times
		return fmt.Errorf("%w: %w", ErrOutlookBusy, err)
	case offline(hr):
		return fmt.Errorf("%w: %w", ErrStoreOffline, err)
	}
	return err
}

// unavailable: Outlook as a whole cannot be read now (the session was
// lost, Outlook stays busy, the breaker refuses): every walk and pass
// stops, and the syncer ends the run.
func unavailable(err error) bool { return errors.Is(err, source.ErrUnavailable) }

// faulted: Outlook raised an exception inside the call (RPC_E_SERVERFAULT):
// a lost session too, but one the item being read may cause again.
func faulted(err error) bool {
	hr, ok := HResult(err)
	return ok && hr == hrServerFault
}
