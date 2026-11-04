package source

import "errors"

// ErrUnavailable marks a source that cannot be read as a whole right now:
// its application is not running, crashed or restarted, or its connector
// paused reconnecting after lost connections. Connectors wrap it in their
// own named errors (Unavailable); the syncer ends the whole run on it
// instead of trying the next folder or item, which would only hammer a
// source that is gone or recovering.
var ErrUnavailable = errors.New("source: the source is unavailable")

// Unavailable returns a named error that matches ErrUnavailable
// (errors.Is) and reads as msg. It has no stable cause code: a screen shows
// the generic unavailability.
func Unavailable(msg string) error { return &unavailable{msg: msg} }

// UnavailableCause is Unavailable with a stable cause code. The code is the
// translatable identity of a precondition ("outlook_not_running"), and msg is
// the owner-readable English diagnostic. Screens translate the code and show
// msg only as a diagnostic, so one cause reads the same in every language.
func UnavailableCause(code, msg string) error { return &unavailable{code: code, msg: msg} }

type unavailable struct {
	code string
	msg  string
}

func (e *unavailable) Error() string        { return e.msg }
func (e *unavailable) Is(target error) bool { return target == ErrUnavailable }

// Cause exposes the stable code and diagnostic to a caller that only knows
// the small structural interface, such as the job manager. The package-level
// Cause function remains the helper for an error chain.
func (e *unavailable) Cause() (string, string) { return e.code, e.msg }

// Cause returns the stable code and owner-readable message of a source
// unavailability ("" when err is not one). The message never carries mail
// content; the code is what a bilingual screen translates.
func Cause(err error) (code, msg string) {
	var u *unavailable
	if errors.As(err, &u) {
		return u.code, u.msg
	}
	return "", ""
}
