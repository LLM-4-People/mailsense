package priority

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// fakeStore is the priority test double: it records every scan, can inject
// failures or rows mid-build, and serves bodies/heads without a database.
type fakeStore struct {
	mu           sync.Mutex
	rows         []store.PriorityRow
	bodies       map[int64]string
	calls        int
	fullScans    int
	preview      []bool
	questions    []store.QuestionKey
	err          error
	mailGen      uint64
	routeGen     uint64
	bumpOnReply  int
	headCalls    int
	headIDs      []int64
	headChars    int
	fullBodyIDs  []int64
	headErr      error
	headMissing  map[int64]bool
	headRevision map[int64]int64
	// panicEach injects a panic inside the one scan, so the service's
	// latch release is tested rather than the happy path.
	panicEach bool
}

func (f *fakeStore) MailGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mailGen
}

func (f *fakeStore) RouteGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routeGen
}

func (f *fakeStore) MessageBody(_ context.Context, messageID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullBodyIDs = append(f.fullBodyIDs, messageID)
	if body, ok := f.bodies[messageID]; ok {
		return body, nil
	}
	for _, r := range f.rows {
		if r.MessageID == messageID {
			return r.Body, nil
		}
	}
	return "", fmt.Errorf("message %d not found", messageID)
}

func (f *fakeStore) PriorityHeads(_ context.Context, ids []int64) (map[int64]store.PriorityBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	f.headIDs = append(f.headIDs, ids...)
	if f.headErr != nil {
		return nil, f.headErr
	}
	out, chars := testPriorityHeads(f.rows, f.bodies, ids, f.headMissing, f.headRevision)
	f.headChars += chars
	return out, nil
}

func (f *fakeStore) EachPriorityReply(ctx context.Context, fingerprint string, includePreview bool, questions []store.QuestionKey, fn func(store.PriorityRow) error) error {
	f.mu.Lock()
	f.questions = slices.Clone(questions)
	f.mu.Unlock()
	err := f.eachMessage(includePreview, false, true, fn)
	f.mu.Lock()
	if f.bumpOnReply > 0 {
		f.bumpOnReply--
		f.mailGen++
	}
	f.mu.Unlock()
	return err
}

func (f *fakeStore) EachPriorityMessage(_ context.Context, _ string, includePreview bool, questions []store.QuestionKey, fn func(store.PriorityRow) error) error {
	f.mu.Lock()
	f.fullScans++
	f.questions = slices.Clone(questions)
	f.mu.Unlock()
	return f.eachMessage(includePreview, true, true, fn)
}

func (f *fakeStore) eachMessage(includePreview, body, decode bool, fn func(store.PriorityRow) error) error {
	f.mu.Lock()
	f.calls++
	f.preview = append(f.preview, includePreview)
	rows := slices.Clone(f.rows)
	err := f.err
	panicEach := f.panicEach
	f.mu.Unlock()
	if panicEach {
		panic("fakeStore: injected scan panic")
	}
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !body {
			r.Body, r.BodyTruncated = "", false
		}
		if !decode {
			r.Route, r.Preview = nil, nil
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeStore) previewFlags() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.preview)
}

func (f *fakeStore) questionKeys() []store.QuestionKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.questions)
}

func (f *fakeStore) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeStore) setHeadErr(err error) {
	f.mu.Lock()
	f.headErr = err
	f.mu.Unlock()
}
