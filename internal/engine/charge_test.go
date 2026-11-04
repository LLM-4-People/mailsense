package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// fakeReserver records the reserve/settle protocol of a paid call.
type fakeReserver struct {
	refuse   bool
	reserved int
	settled  int
	ceilings []engine.Estimate
	used     []engine.Usage
}

func (f *fakeReserver) Reserve(ceiling engine.Estimate) bool {
	f.reserved++
	f.ceilings = append(f.ceilings, ceiling)
	return !f.refuse
}

func (f *fakeReserver) Settle(ceiling engine.Estimate, used engine.Usage) {
	f.settled++
	f.used = append(f.used, used)
}

// TestChargeReservesAndSettlesOnce pins the one reserve/settle protocol: a
// call is reserved before it runs, settled with its usage after, and settled
// even when it failed (a failed call may still be billed). A refused
// reservation never runs the call.
func TestChargeReservesAndSettlesOnce(t *testing.T) {
	ceiling := engine.Estimate{Calls: 1, EstimatedUSD: 0.5, PriceKnown: true}
	used := engine.Usage{Calls: 1, InputTokens: 10}
	boom := errors.New("boom")

	cases := []struct {
		name       string
		refuse     bool
		callErr    error
		wantRan    bool
		wantErr    error
		wantSettle int
	}{
		{name: "budget covers the call", wantRan: true, wantSettle: 1},
		{name: "failed call is still settled", callErr: boom, wantRan: true, wantErr: boom, wantSettle: 1},
		{name: "refused reservation costs nothing", refuse: true, wantErr: engine.ErrBudget, wantSettle: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := &fakeReserver{refuse: tc.refuse}
			ctx := engine.WithReserver(t.Context(), guard)
			ran := false
			got, usage, err := engine.Charge(ctx, ceiling, func() (int, engine.Usage, error) {
				ran = true
				return 7, used, tc.callErr
			})
			if guard.reserved != 1 {
				t.Fatalf("reserved %d times, want 1", guard.reserved)
			}
			if guard.settled != tc.wantSettle {
				t.Fatalf("settled %d times, want %d", guard.settled, tc.wantSettle)
			}
			if ran != tc.wantRan {
				t.Fatalf("call ran = %v, want %v", ran, tc.wantRan)
			}
			// The call's value survives its error: a failed adapter call still
			// reports the usage it spent.
			if ran && got != 7 {
				t.Fatalf("value %d, want the call's own value", got)
			}
			if (err != nil) != (tc.wantErr != nil) {
				t.Fatalf("value %d err %v, want err %v", got, err, tc.wantErr)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) && !errors.Is(err, engine.ErrBudget) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if tc.wantSettle == 1 && (usage.Calls != used.Calls || guard.used[0].InputTokens != used.InputTokens) {
				t.Fatalf("settled with %+v, want %+v", guard.used, used)
			}
		})
	}
}

// TestProviderAPIKeyIsOneRefusal: every adapter reads its key through the
// record, so a missing, empty or unreadable secret is the same ErrNoKey error
// with the same message whichever adapter asked.
func TestProviderAPIKeyIsOneRefusal(t *testing.T) {
	p := engine.Provider{ID: "prov", APIKeySecret: "KEY"}
	if key, err := p.APIKey(fakeSecrets{"KEY": "sk-1"}); err != nil || key != "sk-1" {
		t.Fatalf("key %q err %v", key, err)
	}
	for name, secrets := range map[string]fakeSecrets{
		"missing": {},
		"empty":   {"KEY": ""},
	} {
		t.Run(name, func(t *testing.T) {
			key, err := p.APIKey(secrets)
			if key != "" || !errors.Is(err, engine.ErrNoKey) {
				t.Fatalf("key %q err %v", key, err)
			}
			if !strings.Contains(err.Error(), "secret KEY for provider prov") {
				t.Fatalf("message %q", err)
			}
		})
	}
}
