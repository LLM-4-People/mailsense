package store

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/safe"
)

// TestCompanyCounterpartsBuildPanicIsContained pins the panic path the shared
// detached build now owns for this cache. A build that panics must reach the
// waiters as an ordinary error and clear the one in-flight latch: a latch left
// closed would make every later read block until the build timeout instead of
// retrying. The test hook runs inside the build, so it stands in for a panic
// anywhere in the scan.
func TestCompanyCounterpartsBuildPanicIsContained(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	ctx := t.Context()

	s.counterpartsBuildHook = func() { panic("counterparts build panicked") }
	_, err := s.CompanyCounterparts(ctx)
	if err == nil {
		t.Fatal("a panicking build returned no error")
	}
	var panicErr *safe.PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("a panicking build returned %v, want a *safe.PanicError", err)
	}

	// The latch is clear: the next read builds again instead of waiting on a
	// build that will never finish.
	s.counterpartsBuildHook = nil
	cp, err := s.CompanyCounterparts(ctx)
	if err != nil {
		t.Fatalf("the read after the recovered panic failed: %v", err)
	}
	if cp == nil {
		t.Fatal("the read after the recovered panic returned no counterparts")
	}
}
