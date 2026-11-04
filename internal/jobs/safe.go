package jobs

import "github.com/LLM-4-People/mailsense/internal/safe"

// PanicError is a panic recovered by Safe. It is owned by package safe, so a
// job worker and a read-model build share one recovery contract.
type PanicError = safe.PanicError

// Safe runs fn and turns a panic into a *PanicError: worker goroutines call
// it per item, so one bad item fails as that item's error instead of bringing
// the whole process down.
func Safe(fn func() error) error { return safe.Run(fn) }
