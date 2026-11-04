package evidence

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// seen is one message as the shared context reads it.
type seen struct {
	item     templateItem
	threaded bool
	msg      threads.Message
	asked    bool // an owner message whose own text holds a question mark
}

// readMail streams the visible mail and builds each message's view on
// workers goroutines (threads.FromMail and mail.CleanBody are the costly
// part), in row order.
//
// It deliberately keeps its own pool instead of calling mapReduce: the store
// cursor is the producer, so only a bounded window of rows is ever resident.
// mapReduce works from a materialized slice (one batch of ids, the sender
// groups), which here would hold the whole mailbox in memory. The worker loop
// is the same shape everywhere.
func readMail(ctx context.Context, st *store.Store, inThreads map[int64]bool, set Settings) ([]seen, error) {
	rows := make(chan store.MessageRow, set.Workers*4)
	var mu sync.Mutex
	var out []seen
	var errs []error
	var wg sync.WaitGroup
	for range max(set.Workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []seen
			var failed error
			for r := range rows {
				if failed != nil {
					continue // drain: the run fails with the first error
				}
				failed = safely(fmt.Sprintf("message %d", r.RowID), func() error {
					local = append(local, view(&r, inThreads[r.RowID], set))
					return nil
				})
			}
			mu.Lock()
			out = append(out, local...)
			errs = append(errs, failed)
			mu.Unlock()
		}()
	}
	err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		select {
		case rows <- *r:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	})
	close(rows)
	wg.Wait()
	slices.SortFunc(out, func(a, b seen) int { return cmp.Compare(a.item.row, b.item.row) })
	return out, errors.Join(append(errs, err)...)
}

func view(r *store.MessageRow, threaded bool, set Settings) seen {
	m := &r.Message
	at, _ := m.Instant()
	s := seen{item: templateItem{row: r.RowID, sender: mail.NormalizeAddress(m.SenderAddress), at: at}, threaded: threaded}
	if !threaded {
		return s
	}
	s.msg = threads.FromMail(r.RowID, m, r.Reason == scope.ReasonSentByOwner, set.Threads.ExcerptChars, set.Domains)
	if s.msg.Role == mail.RoleSent {
		s.asked = mail.QuestionMarks(mail.CleanBody(m.Body)) > 0
	}
	return s
}
