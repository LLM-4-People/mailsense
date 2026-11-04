package outlook

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
)

// apartment runs functions one at a time on one goroutine locked to one OS
// thread: Outlook's object model is a single-threaded apartment (STA), so
// every COM call of the connector happens on this thread (init/uninit are
// CoInitializeEx/CoUninitialize on Windows, nothing in tests).
type apartment struct {
	work chan func()
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

var errApartmentClosed = errors.New("outlook: connector closed")

// startApartment starts the thread and runs init on it.
func startApartment(init func() error, uninit func()) (*apartment, error) {
	a := &apartment{work: make(chan func()), quit: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(a.done)
		if err := init(); err != nil {
			ready <- err
			return
		}
		defer uninit()
		ready <- nil
		for {
			select {
			case fn := <-a.work:
				fn()
			case <-a.quit:
				return
			}
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return a, nil
}

// do runs fn on the apartment thread and waits for it. A call already
// running is never interrupted (COM calls cannot be): fn checks ctx itself.
func (a *apartment) do(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil { // select alone may still pick the work
		return err
	}
	result := make(chan error, 1)
	select {
	case a.work <- func() { result <- protect(fn) }:
		return <-result
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		return errApartmentClosed
	}
}

// close stops the thread after the running function returns; safe to call twice.
func (a *apartment) close() {
	a.once.Do(func() { close(a.quit) })
	<-a.done
}

// protect runs fn and turns a panic into ErrInternal, so one bad value from
// Outlook cannot take the COM thread (and the app) down.
func protect(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrInternal, p)
		}
	}()
	return fn()
}
