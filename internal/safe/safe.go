// Package safe turns panics in background and read-model code into errors.
// One owner keeps every recovery path honest: a build that panics must
// finalize its single-flight latch and release its waiters instead of
// leaving them blocked until a request deadline. It also owns the shape of a
// build that outlives its request: detached from the caller, bounded by a
// timeout, and cancelled by the service's own context (Detached).
package safe

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// PanicError is a recovered panic: its value and the stack.
type PanicError struct {
	Value any
	Stack string
}

func (p *PanicError) Error() string { return fmt.Sprintf("panic: %v", p.Value) }

// Run runs fn and returns its error, or a *PanicError.
func Run(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Value: r, Stack: string(debug.Stack())}
		}
	}()
	return fn()
}

// Value runs fn and returns its value, or a *PanicError. A cache builder
// uses it so a panic takes the ordinary error path (clearing the placeholder
// and closing readiness) instead of escaping with the latch installed.
func Value[T any](fn func() (T, error)) (v T, err error) {
	err = Run(func() error {
		var e error
		v, e = fn()
		return e
	})
	return v, err
}

// Detached runs one build that must outlive its caller: fn receives a context
// detached from parent (its cancellation and deadline do not reach the build;
// its values do, as context.WithoutCancel keeps them), bounded by timeout, and
// cancelled when base ends. A nil base means the owner has no service context to
// cancel through and joins the build on its own shutdown path instead. The
// returned error is fn's own, or the *PanicError a panic produces: the panic is
// already an ordinary error, and each caller keeps its own message text for it.
//
// This is the one owner of the detached, bounded, base-cancelled build context,
// so no caller re-spells context.WithoutCancel, the timeout or the base
// cancellation and gets one of them wrong. Whether the result may be installed
// stays with the caller: only it knows which snapshot may replace which.
func Detached[T any](parent, base context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	if base != nil {
		stop := context.AfterFunc(base, cancel)
		defer stop()
	}
	return Value(func() (T, error) { return fn(buildCtx) })
}
