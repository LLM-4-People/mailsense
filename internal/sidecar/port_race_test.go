package sidecar

import (
	"net"
	"runtime"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
)

// TestPortHeldDistinguishesAForeignListener pins the one probe the launch
// retry asks: a port still held once the child is gone belongs to another
// program, and a port nobody holds is the child's own failure. Without that
// distinction a lost race and a broken build would be indistinguishable.
func TestPortHeldDistinguishesAForeignListener(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skipf("portHeld is not implemented on %s", runtime.GOOS)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if held, err := portHeld(port); err != nil || !held {
		t.Fatalf("portHeld(%d) with a listener = %v, %v; want true", port, held, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if held, err := portHeld(port); err != nil || held {
		t.Fatalf("portHeld(%d) after the listener closed = %v, %v; want false", port, held, err)
	}
}

// TestLaunchRetriesALostPortRace: freePort closes its probe listener before
// the runtime binds, so another program can take the port in between. That
// used to mark a working GPU build broken, permanently, on a transient fact
// about the machine, and the supervisor could not recover it (a broken build
// is never tried again). The launch now asks for another port and starts.
//
// The GPU shape is the one that failed: a cpu build is merely restarted by the
// supervisor, which hid the same lost race.
func TestLaunchRetriesALostPortRace(t *testing.T) {
	w := newWorld(t, noCPUVars(), map[string]any{"sidecar.variant": "vulkan", "sidecar.threads": 1}, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	m := w.manager()

	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	blocked := taken.Addr().(*net.TCPAddr).Port

	restore := choosePort
	attempts := 0
	choosePort = func() (int, error) {
		attempts++
		if attempts == 1 {
			return blocked, nil
		}
		return restore()
	}
	t.Cleanup(func() { choosePort = restore })

	if _, err := w.locate(m); err != nil {
		t.Fatalf("a port taken by another process failed the launch: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the busy port %d was used %d times, want one retry (2 launches)", blocked, attempts)
	}
	st := m.Status()[0]
	if st.State != StateReady || st.PID == 0 || st.Variant != "vulkan" {
		t.Fatalf("the runtime is not ready on the requested build after the retry: %+v", st)
	}
	if st.Restarts != 0 {
		t.Fatalf("the runtime needed %d supervisor restarts; the launch retried nothing", st.Restarts)
	}
}

// TestLaunchDoesNotRetryAChildThatFailedOnItsOwn is the control: a build
// whose runtime refuses to start leaves its port free, so the retry must not
// fire and the failure must surface on the first attempt.
func TestLaunchDoesNotRetryAChildThatFailedOnItsOwn(t *testing.T) {
	failing := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_FAIL_START": "1"}}}
	w := newWorld(t, failing, map[string]any{"sidecar.threads": 1})
	m := w.manager()
	ctx := t.Context()
	b, err := m.build(rtName, modelName, VariantCPU)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ensureInstalled(ctx, b, func() {}); err != nil {
		t.Fatal(err)
	}

	restore := choosePort
	attempts := 0
	choosePort = func() (int, error) { attempts++; return restore() }
	t.Cleanup(func() { choosePort = restore })

	if _, err := m.launchReady(ctx, b, 1); err == nil {
		t.Fatal("a runtime that refuses to start was reported ready")
	}
	if attempts != 1 {
		t.Fatalf("a build that failed on its own was launched %d times, want 1", attempts)
	}
}
