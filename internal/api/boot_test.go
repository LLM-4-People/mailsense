package api

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// fakeBoot is a scripted boot source: each call returns the next view, and
// the last one repeats.
type fakeBoot struct {
	mu    sync.Mutex
	views []BootView
	calls int
	err   error
}

func (f *fakeBoot) Boot(context.Context) (BootView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return BootView{}, f.err
	}
	if len(f.views) == 0 {
		return BootView{State: "ready"}, nil
	}
	index := min(f.calls-1, len(f.views)-1)
	return f.views[index], nil
}

func (f *fakeBoot) callsMade() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestBootEndpointReportsWarmingAndReady(t *testing.T) {
	w := newWorld()
	views := []BootStageView{
		{Name: "learning", State: "done", DurationMS: 12},
		{Name: "priority", State: "running"},
		{Name: "agenda", State: "pending"},
	}
	w.boot = &fakeBoot{views: []BootView{
		{State: "warming", Done: 1, Total: 3, Current: "priority", Views: views},
		{State: "ready", Done: 3, Total: 3, Views: []BootStageView{
			{Name: "learning", State: "done", DurationMS: 12},
			{Name: "priority", State: "done", DurationMS: 30},
			{Name: "agenda", State: "done", DurationMS: 4},
		}},
	}}
	w.services.Boot = w.boot
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	status, header, data := do(t, ts, srv, request{method: "GET", path: "/api/boot"})
	mustStatus(t, status, 200, data)
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache %q, want no-store", got)
	}
	var warming BootView
	if err := json.Unmarshal(data, &warming); err != nil {
		t.Fatal(err)
	}
	if warming.State != "warming" || warming.Done != 1 || warming.Total != 3 || warming.Current != "priority" {
		t.Fatalf("warming view %+v", warming)
	}
	if len(warming.Views) != len(views) {
		t.Fatalf("%d views, want %d", len(warming.Views), len(views))
	}
	for i, want := range views {
		if warming.Views[i] != want {
			t.Fatalf("view %d = %+v, want %+v", i, warming.Views[i], want)
		}
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/boot"})
	mustStatus(t, status, 200, data)
	var ready BootView
	if err := json.Unmarshal(data, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.State != "ready" || ready.Done != 3 || ready.Total != 3 {
		t.Fatalf("ready view %+v", ready)
	}
	if got := w.boot.callsMade(); got != 2 {
		t.Fatalf("boot read %d times, want 2", got)
	}
}

func TestBootEndpointRequiresAuthAndGet(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/boot", skipAuth: true})
	mustStatus(t, status, 401, data)

	status, header, data := do(t, ts, srv, request{method: "POST", path: "/api/boot"})
	mustStatus(t, status, 405, data)
	if header.Get("Allow") != "GET" {
		t.Fatalf("Allow %q, want GET", header.Get("Allow"))
	}
	if code := errorCode(t, data); code != "method_not_allowed" {
		t.Fatalf("code %q", code)
	}
	if _, ok := header["Content-Type"]; !ok {
		t.Fatal("a 405 must be JSON")
	}
}
