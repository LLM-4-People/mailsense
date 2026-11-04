package outlook

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestApartmentRunsEverythingOnOneGoroutine(t *testing.T) {
	inits := 0
	a, err := startApartment(func() error { inits++; return nil }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	order := []int{}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_ = a.do(context.Background(), func() error { mu.Lock(); order = append(order, i); mu.Unlock(); return nil })
		})
	}
	wg.Wait()
	if len(order) != 20 || inits != 1 {
		t.Fatalf("ran %d functions, %d inits", len(order), inits)
	}
	want := errors.New("boom")
	if err := a.do(context.Background(), func() error { return want }); err != want {
		t.Fatalf("error not returned: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.do(ctx, func() error { t.Error("must not run"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
	a.close()
	a.close() // idempotent
	if err := a.do(context.Background(), func() error { return nil }); !errors.Is(err, errApartmentClosed) {
		t.Fatalf("closed apartment: %v", err)
	}
}

func TestApartmentInitFailure(t *testing.T) {
	if _, err := startApartment(func() error { return errors.New("CoInitializeEx failed") }, func() {}); err == nil {
		t.Fatal("init failure must surface")
	}
}
