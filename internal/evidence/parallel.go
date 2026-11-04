package evidence

import (
	"context"
	"sync"
)

// mapReduce is the one worker pool of the evidence engine. It offers every
// item to at most workers goroutines, guards each call with safely (so a panic
// becomes that item's error instead of the process's death) and hands each
// successful result to merge.
//
// A cancelled context stops the producer and abandons every item not yet
// started: evidence.batch_size can exceed the whole mailbox, and template
// counting covers the whole mailbox, so draining the work would delay shutdown
// for minutes. The cancellation is not reported as an item error; the caller
// reads context.Cause after the call, because only the caller knows whether the
// abandoned work is a cancelled run or a stopped pass.
//
// The call returns only after every worker has exited, so no goroutine outlives
// it. merge runs on a worker goroutine under the pool's one mutex and must be a
// plain assignment into the caller's result array or map.
func mapReduce[T, R any](ctx context.Context, workers int, items []T, label func(T) string,
	fn func(T) (R, error), merge func(index int, result R)) error {
	if len(items) == 0 {
		return nil
	}
	errs := make([]error, len(items))
	var mu sync.Mutex
	next := make(chan int)
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if ctx.Err() != nil {
					continue // a cancelled pool abandons the rest at once
				}
				var result R
				err := safely(label(items[i]), func() (err error) {
					result, err = fn(items[i])
					return err
				})
				if err != nil {
					errs[i] = err
					continue
				}
				mu.Lock()
				merge(i, result)
				mu.Unlock()
			}
		}()
	}
send:
	for i := range items {
		// Check cancellation before offering the next item: a select with both
		// a ready worker and a cancelled context picks at random, so the
		// pre-check keeps a large batch from being drained anyway.
		select {
		case <-ctx.Done():
			break send
		default:
		}
		select {
		case next <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(next)
	wg.Wait()
	return firstError(errs)
}

// firstError returns the failure of the lowest item index, so a run with
// several broken messages always reports the same one.
func firstError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
