package syncer

import (
	"errors"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// outputError marks a failure to write the database: unlike a connector or
// folder failure it aborts the whole run (PORTING §10).
type outputError struct{ err error }

func (e *outputError) Error() string { return e.err.Error() }
func (e *outputError) Unwrap() error { return e.err }

// output marks err as an output failure (nil stays nil).
func output(err error) error {
	if err == nil || isOutput(err) {
		return err
	}
	return &outputError{err}
}

func isOutput(err error) bool {
	var o *outputError
	return errors.As(err, &o)
}

// aborts reports whether err ends the whole run: an output failure, or a
// source that is unavailable as a whole (source.ErrUnavailable).
func aborts(err error) bool { return isOutput(err) || errors.Is(err, source.ErrUnavailable) }
