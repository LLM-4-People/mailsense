// Package runstate is the one lifecycle vocabulary of background work
// (jobs, sync runs) and the one rule that turns an outcome into a state.
package runstate

import "context"

// State of a job or run.
type State string

// States.
const (
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Of classifies a finished unit of work: no error is success, an error
// while ctx is done is a cancellation, any other error is a failure.
func Of(ctx context.Context, err error) State {
	switch {
	case err == nil:
		return Succeeded
	case ctx.Err() != nil:
		return Cancelled
	default:
		return Failed
	}
}
