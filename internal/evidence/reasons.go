package evidence

import (
	"context"
	"fmt"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// reasonBook interns the reasons of not-applicable and unavailable values
// (store: evidence_reasons): a stored value names its reason by id. Ids
// are never reused, so a cached one stays right.
type reasonBook struct {
	st    *store.Store
	mu    sync.Mutex
	ids   map[string]int64
	texts map[int64]string
}

func newReasonBook(st *store.Store) *reasonBook {
	return &reasonBook{st: st, ids: map[string]int64{}, texts: map[int64]string{}}
}

// id is a reason's id, recorded the first time.
func (b *reasonBook) id(ctx context.Context, reason string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.ids[reason]; ok {
		return id, nil
	}
	got, err := b.st.EvidenceReasonIDs(ctx, []string{reason})
	if err != nil {
		return 0, fmt.Errorf("evidence: intern reason %q: %w", reason, err)
	}
	b.ids[reason], b.texts[got[reason]] = got[reason], reason
	return got[reason], nil
}

// text is the reason of an id (the table read again for an unknown one).
func (b *reasonBook) text(ctx context.Context, id int64) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.texts[id]; ok {
		return t, nil
	}
	all, err := b.st.EvidenceReasons(ctx)
	if err != nil {
		return "", err
	}
	for i, t := range all {
		b.texts[i], b.ids[t] = t, i
	}
	t, ok := b.texts[id]
	if !ok {
		return "", fmt.Errorf("evidence: no reason %d", id)
	}
	return t, nil
}
