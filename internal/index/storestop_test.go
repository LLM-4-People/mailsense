package index

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestStoreStopIsOneClassification: a store write failure ends a run one of
// three ways. ConfirmEmbeddings and SaveEmbeddings used to spell the busy and
// cancelled checks themselves, with slightly different returns (the store
// error on one, the context cause on the other); storeStop is now the one
// reading.
func TestStoreStopIsOneClassification(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	cases := []struct {
		name        string
		ctx         context.Context
		err         error
		wantStopped string
		wantHandled bool
		wantErr     error // nil means the busy stop's nil return
	}{
		{name: "busy store", ctx: t.Context(), err: store.ErrStoreBusy, wantStopped: StoppedBusy, wantHandled: true},
		{name: "cancelled request", ctx: cancelled, err: errors.New("write refused"),
			wantStopped: StoppedCancelled, wantHandled: true, wantErr: context.Canceled},
		{name: "real failure", ctx: t.Context(), err: io.ErrShortWrite, wantHandled: false, wantErr: io.ErrShortWrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := &Report{}
			ret, handled := storeStop(tc.ctx, tc.err, rep)
			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tc.wantHandled)
			}
			if rep.StoppedBy != tc.wantStopped {
				t.Fatalf("StoppedBy = %q, want %q", rep.StoppedBy, tc.wantStopped)
			}
			if tc.wantErr == nil && ret != nil {
				t.Fatalf("returned %v, want nil", ret)
			}
			if tc.wantErr != nil && !errors.Is(ret, tc.wantErr) {
				t.Fatalf("returned %v, want %v", ret, tc.wantErr)
			}
			if tc.err != nil && errors.Is(tc.err, ErrStoreBusy) && rep.Error != tc.err.Error() {
				t.Fatalf("the stop does not carry the store's message: %q", rep.Error)
			}
			if rep.Committed() {
				t.Fatal("a stopped write counted committed mail")
			}
		})
	}

	// The cancellation cause, not the surface error, is what outcome reports.
	caused, cancelCause := context.WithCancelCause(t.Context())
	cancelCause(io.ErrUnexpectedEOF)
	rep := &Report{}
	ret, handled := storeStop(caused, errors.New("write refused"), rep)
	if !handled || !errors.Is(ret, io.ErrUnexpectedEOF) {
		t.Fatalf("cancellation cause lost: %v (handled %v)", ret, handled)
	}
	if rep.StoppedBy != StoppedCancelled {
		t.Fatalf("StoppedBy = %q", rep.StoppedBy)
	}
}
