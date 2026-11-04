package outlook

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func guarded(o *fakeOutlook, obj Object, class string, attempts int) (*guard, *clocktest.Fake) {
	clk := clocktest.New(t0)
	return &guard{inner: obj, class: class, ctx: context.Background(), clock: clk, count: &counters{}, pace: unpaced(clk),
		policy: retryPolicy{attempts: attempts, base: 10 * time.Millisecond, max: 40 * time.Millisecond}}, clk
}

func TestGuardRetriesBusyCallsWithBackoff(t *testing.T) {
	o := profile()
	app, _ := o.Attach()
	defer app.Release()
	g, clk := guarded(o, app, classApplication, 5)
	o.busy = 4
	v, err := g.Call("GetNamespace", "MAPI")
	if err != nil {
		t.Fatal(err)
	}
	v.(Object).Release() // a paced, counted call too (the 6th)
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}
	if got := clk.Sleeps(); len(got) != 4 || got[0] != want[0] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("backoff %v, want %v", got, want)
	}
	if g.count.retries.Load() != 4 || g.count.calls.Load() != 6 || g.count.snapshot()["Namespace.Release"].Calls != 1 {
		t.Fatalf("calls %d retries %d", g.count.calls.Load(), g.count.retries.Load())
	}
	if m := g.count.snapshot()["Application.GetNamespace"]; m.Calls != 5 {
		t.Fatalf("member cost %+v", m)
	}
	o.busy = 5
	if _, err := g.Call("GetNamespace", "MAPI"); err == nil {
		t.Fatal("a call still busy after every attempt must fail")
	} else if hr, _ := HResult(err); hr != hrCallRejected {
		t.Fatalf("the HRESULT must survive: %v", err)
	}
}

func TestGuardRetryWaitHonoursCancellation(t *testing.T) {
	o := profile()
	app, _ := o.Attach()
	defer app.Release()
	g, _ := guarded(o, app, classApplication, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.ctx = ctx
	o.busy = 1
	if _, err := g.Call("GetNamespace", "MAPI"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
}

// The connector is read-only by construction: anything not in model.go is
// refused before it reaches Outlook.
func TestGuardRefusesEverythingOffTheList(t *testing.T) {
	o := profile()
	app, _ := o.Attach()
	defer app.Release()
	g, _ := guarded(o, app, classApplication, 1)
	v, err := g.Call("GetNamespace", "MAPI")
	if err != nil {
		t.Fatal(err)
	}
	ns := v.(*guard)
	defer ns.Release()
	for _, bad := range []struct {
		obj  *guard
		name string
		call bool
	}{
		{ns, "GetDefaultFolder", true}, {ns, "Stores", true}, {g, "Quit", true}, {ns, "Logoff", true},
	} {
		var err error
		if bad.call {
			_, err = bad.obj.Call(bad.name)
		} else {
			_, err = bad.obj.Get(bad.name)
		}
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s.%s: %v", bad.obj.class, bad.name, err)
		}
	}
	for class, members := range model {
		for name := range members {
			for _, banned := range []string{"Save", "Delete", "Move", "Send", "MarkAsTask", "Remove", "Copy", "Display", "Close", "UnRead"} {
				if name == banned {
					t.Errorf("%s.%s must never be callable", class, name)
				}
			}
		}
	}
	if callsTo(o, "Namespace.GetDefaultFolder") != 0 || callsTo(o, "Application.Quit") != 0 {
		t.Fatal("refused calls must not reach Outlook")
	}
}

func TestGuardRejectsAnObjectWhereAValueIsExpected(t *testing.T) {
	o := profile()
	app, _ := o.Attach()
	defer app.Release()
	g, _ := guarded(o, app, classApplication, 1)
	g.inner = &objectReturner{o: o}
	if _, err := g.Get("Version"); err == nil {
		t.Fatal("an unexpected object must be refused (and released)")
	}
}

type objectReturner struct{ o *fakeOutlook }

func (r *objectReturner) Get(string, ...any) (any, error)  { return r.o.obj("Surprise", nil), nil }
func (r *objectReturner) Call(string, ...any) (any, error) { return nil, nil }
func (r *objectReturner) Put(string, any) error            { return nil }
func (r *objectReturner) Release()                         {}

func TestErrorClassification(t *testing.T) {
	for _, c := range []struct {
		code uint32
		want error
	}{
		{hrDisconnected, ErrSessionLost}, {hrServerUnavailable, ErrSessionLost}, {hrObjNotConnected, ErrSessionLost},
		{hrConnTerminated, ErrSessionLost}, {hrServerDied, ErrSessionLost}, {hrServerDiedDNE, ErrSessionLost},
		{hrServerFault, ErrSessionLost}, {hrCallFailed, ErrSessionLost}, {hrCallFailedDNE, ErrSessionLost},
		{hrServerStopping, ErrSessionLost},
		{hrNetworkError, ErrStoreOffline}, {hrFailOneProvider, ErrStoreOffline},
	} {
		err := classify(&ComError{Code: c.code, Member: "X"})
		if !errors.Is(err, c.want) || errors.Is(err, source.ErrUnavailable) != (c.want == ErrSessionLost) {
			t.Errorf("0x%08X: %v", c.code, err)
		}
		if hr, _ := HResult(err); hr != c.code || Code(err) == "other" {
			t.Errorf("0x%08X lost its HRESULT: %v", c.code, err)
		}
	}
	if err := classify(errors.New("plain")); Code(err) != "other" {
		t.Fatal("plain errors pass through")
	}
}
