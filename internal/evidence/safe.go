package evidence

import (
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// safely runs one item of a worker goroutine (a message, a sender's
// templates): a panic becomes that item's error, logged with its stack,
// instead of bringing the process down.
func safely(item string, fn func() error) error {
	err := jobs.Safe(fn)
	var p *jobs.PanicError
	if errors.As(err, &p) {
		logx.For("evidence").Error("evidence worker panicked", "item", item, "panic", fmt.Sprint(p.Value), "stack", p.Stack)
		return fmt.Errorf("evidence: %s: %w", item, err)
	}
	return err
}
