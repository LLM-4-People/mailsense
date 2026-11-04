package learning

import (
	"context"
	"reflect"
	"sync"
)

// A status build reads the same stage through several paths: the machine
// asks the embeddings stage directly, the layer-1 stage asks it again as
// its "after" stage, and the route's layer-1 stage asks it once more. Each
// answer plans the whole mailbox coverage, so one build must ask each
// stage once. The memo lives in the context for exactly one build: it is
// created by computeStatus, shared by every stage it calls (including
// nested ones), and dropped when the build returns. Nothing is cached
// across builds or invalidated: a new build reads every stage again.
//
// The same memo also shares the facts a stage reads (Memo): a build reads
// each named (type, name) fact once too. A fact is therefore shared for
// the whole build even if a write lands mid-build. The build answers from
// one internally consistent snapshot of each fact; the next build reads
// it again. Neither memo outlives the build.

// memoKind keeps a stage key and a fact key apart even when a stage's
// dynamic type and a fact's type happen to match.
type memoKind uint8

const (
	stageMemo memoKind = iota
	factMemo
)

// statusMemoKey is a stage's or a fact's identity within one build. A
// stage declares its identity with ID; the machine already refuses
// duplicate ids among its top-level stages, and a nested stage repeats the
// id of the stage it mirrors (the route's layer-1 stage is the layer-1
// stage). The type keeps two different implementations that happen to
// share an id apart, and the kind keeps a stage and a fact apart. Neither
// a non-comparable stage value nor a stage holding a slice can panic here:
// the key never compares stage values.
type statusMemoKey struct {
	kind memoKind
	typ  reflect.Type
	id   string
}

type statusAnswer struct {
	status StageStatus
	err    error
}

// statusEntry answers a key once; builds may read the same key
// concurrently.
type statusEntry struct {
	once   sync.Once
	answer statusAnswer
}

// factEntry answers one typed fact once.
type factEntry[T any] struct {
	once  sync.Once
	value T
	err   error
}

type statusMemo struct {
	mu      sync.Mutex
	entries map[statusMemoKey]any // *statusEntry or *factEntry[T]
}

type statusMemoContextKey struct{}

// withStatusMemo installs one build's memo. It is idempotent: a build that
// already carries a memo keeps it.
func withStatusMemo(ctx context.Context) context.Context {
	if statusMemoFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, statusMemoContextKey{}, &statusMemo{entries: map[statusMemoKey]any{}})
}

func statusMemoFrom(ctx context.Context) *statusMemo {
	memo, _ := ctx.Value(statusMemoContextKey{}).(*statusMemo)
	return memo
}

// StatusOf is the one reader of a stage inside a status build. Within a
// build it returns the stage's first answer (status and error) to every
// caller; without a build memo it is exactly s.Status(ctx), so direct
// callers outside the machine keep their behavior.
func StatusOf(ctx context.Context, s Stage) (StageStatus, error) {
	memo := statusMemoFrom(ctx)
	if memo == nil {
		return s.Status(ctx)
	}
	key := statusMemoKey{kind: stageMemo, typ: reflect.TypeOf(s), id: s.ID()}
	memo.mu.Lock()
	entry, ok := memo.entries[key].(*statusEntry)
	if !ok {
		entry = &statusEntry{}
		memo.entries[key] = entry
	}
	memo.mu.Unlock()
	entry.once.Do(func() { entry.answer.status, entry.answer.err = s.Status(ctx) })
	return entry.answer.status, entry.answer.err
}

// Memo runs build once per status build and returns the same value and
// error to every caller that names the same fact. Without a build memo it
// is exactly build(ctx). The key is (T, name), so two facts that share a
// name but not a type never collide.
//
// The memo never outlives the build: a fact is shared even if a write
// lands while the build runs, so the build answers from one internally
// consistent snapshot of that fact; the next build reads it again.
func Memo[T any](ctx context.Context, name string, build func(context.Context) (T, error)) (T, error) {
	memo := statusMemoFrom(ctx)
	if memo == nil {
		return build(ctx)
	}
	key := statusMemoKey{kind: factMemo, typ: reflect.TypeFor[T](), id: name}
	memo.mu.Lock()
	entry, ok := memo.entries[key].(*factEntry[T])
	if !ok {
		entry = &factEntry[T]{}
		memo.entries[key] = entry
	}
	memo.mu.Unlock()
	entry.once.Do(func() { entry.value, entry.err = build(ctx) })
	return entry.value, entry.err
}
