package store

import (
	"context"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/safe"
)

// buildSlotTimeout is the one bound of a shared cache build. A stuck read must
// not keep every waiter blocked forever, and every generation-keyed cache in
// this package fails the same way.
const buildSlotTimeout = 2 * time.Minute

// buildSlot is the one owner of "at most one build per (generation,
// configuration), shared by every concurrent reader" for a cache in this
// package. A reader of the same pair joins the in-flight build; a reader of a
// newer pair waits for it and then starts the next one. The build runs
// detached from the caller that happened to start it and bounded by
// buildSlotTimeout, so one request ending early neither fails the waiters nor
// repeats the scan for them.
//
// The build is installed under the pair it sampled even when the mail or the
// configuration moved on meanwhile: it is still a coherent snapshot, and the
// next read sees the newer pair and builds behind it. Waiting on a build is
// the only case where a reader blocks; a completed value is always served
// from the snapshot it was built for.
type buildSlot[T any] struct {
	mu        sync.Mutex
	installed bool
	value     T
	gen       uint64
	snap      *config.Snapshot
	building  *buildJoin
	builds    uint64 // builds started; the tests pin one per generation
}

// buildJoin is one in-flight build. err is written before done is closed; the
// close is the happens-before edge for every waiter.
type buildJoin struct {
	done chan struct{}
	gen  uint64
	snap *config.Snapshot
	err  error
}

// get returns the value installed for (gen, snap), joins the one in-flight
// build, or starts it. build receives the sampled snapshot and, when the
// configuration is unchanged, the installed value as an incremental base
// (valid says whether it is one).
//
// snapshot asks for the last coherent value while a newer build runs; the
// profile-style reads use it so a refresh never makes them wait. The exact
// reads pass false and wait for their own pair.
func (b *buildSlot[T]) get(ctx context.Context, gen uint64, snap *config.Snapshot,
	snapshot bool, build func(context.Context, *config.Snapshot, T, bool) (T, error)) (T, error) {
	var zero T
	for {
		b.mu.Lock()
		if b.installed && b.gen == gen && b.snap == snap {
			value := b.value
			b.mu.Unlock()
			return value, nil
		}
		if join := b.building; join != nil {
			installed, value := b.installed, b.value
			b.mu.Unlock()
			if snapshot && installed {
				return value, nil
			}
			select {
			case <-join.done:
				// A build that sampled exactly this pair owns its error:
				// return it instead of retrying the same scan. One for an
				// older pair only installed its baseline; this reader still
				// needs the next value.
				if join.err != nil && join.gen == gen && join.snap == snap {
					return zero, join.err
				}
				continue
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
		previous, valid := b.value, b.installed
		if valid && b.snap != snap {
			// A different configuration can change the shared domains, the
			// declared owner or the selection, so the installed value is not
			// an incremental base.
			previous, valid = zero, false
		}
		join := &buildJoin{done: make(chan struct{}), gen: gen, snap: snap}
		b.building = join
		b.builds++
		b.mu.Unlock()

		value, err := safe.Detached(ctx, nil, buildSlotTimeout,
			func(buildCtx context.Context) (T, error) {
				return build(buildCtx, snap, previous, valid)
			})

		b.mu.Lock()
		// Install under the pair this build sampled. A newer pair replaces it
		// at the next read, but the completed scan stays the newest coherent
		// value until then instead of being dropped.
		if err == nil {
			b.value, b.gen, b.snap, b.installed = value, join.gen, join.snap, true
		}
		join.err = err
		b.building = nil
		close(join.done)
		b.mu.Unlock()
		return value, err
	}
}

// reset drops the installed value and the in-flight join. It is the shutdown
// path: no reader may answer from a cache after Close.
func (b *buildSlot[T]) reset() {
	var zero T
	b.mu.Lock()
	b.value, b.installed, b.gen, b.snap, b.building = zero, false, 0, nil, nil
	b.mu.Unlock()
}

// installedValue reports the currently installed value and the generation it
// was built for (cache diagnostics and tests).
func (b *buildSlot[T]) installedValue() (T, uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value, b.gen, b.installed
}

// buildCount reports how many builds were started (cache diagnostics and
// tests).
func (b *buildSlot[T]) buildCount() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builds
}
