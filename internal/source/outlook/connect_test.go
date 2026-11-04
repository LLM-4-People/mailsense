package outlook

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/source"
)

func TestPreconditionsAreNamedErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeOutlook)
		want  error
	}{
		{"elevated", func(o *fakeOutlook) { o.elevated = true }, ErrElevated},
		{"classic outlook missing", func(o *fakeOutlook) { o.unregistered = true }, ErrNoClassicOutlook},
		{"new outlook and not running", func(o *fakeOutlook) { o.running, o.newOutlook = false, true }, ErrNewOutlook},
		{"integrity mismatch on attach", func(o *fakeOutlook) { o.attachErr = hrServerExecFailure }, ErrIntegrity},
		{"not running", func(o *fakeOutlook) { o.running = false }, ErrNotRunning},
		{"attach fails otherwise", func(o *fakeOutlook) { o.attachErr = 0x80004005 }, ErrNotRunning},
	}
	for _, c := range cases {
		o := profile()
		c.setup(o)
		conn, _ := testConnector(t, o, nil)
		if _, err := conn.Discover(t.Context()); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

// Incident 2026-10-15 (Outlook crashed twice under sync load): background
// work only ever attaches. A connector whose Outlook is not running fails
// with ErrNotRunning (a source.ErrUnavailable) on every operation and never
// starts Outlook; only the user's action StartOutlook does, through the
// shell (its own window), and leaves a running Outlook alone.
func TestBackgroundWorkNeverStartsOutlook(t *testing.T) {
	o := profile()
	o.running = false
	c, _ := testConnector(t, o, nil)
	ops := map[string]func() error{
		"discover": func() error { _, err := c.Discover(t.Context()); return err },
		"details": func() error {
			_, err := c.ReadDetails(t.Context(), source.ItemRef{StoreID: "S-PRIMARY", ID: "x"})
			return err
		},
		"health": func() error { _, err := c.Health(t.Context()); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrNotRunning) || !errors.Is(err, source.ErrUnavailable) {
			t.Errorf("%s: %v, want ErrNotRunning", name, err)
		}
	}
	if o.starts != 0 || len(o.calls) != 0 {
		t.Fatalf("background work started Outlook (%d) or called it (%v)", o.starts, o.calls)
	}
	if err := c.StartOutlook(t.Context()); err != nil || o.starts != 1 {
		t.Fatalf("the user's start: %v, starts %d", err, o.starts)
	}
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("attach after the user's start: %v", err)
	}
	if err := c.StartOutlook(t.Context()); err != nil || o.starts != 1 {
		t.Fatalf("a running Outlook must be left alone: %v, starts %d", err, o.starts)
	}
	if _, ok := model[classNamespace]["Logon"]; ok {
		t.Fatal("Namespace.Logon must not be callable: the connector never logs on to a profile")
	}
}

func TestStartOutlookMisuse(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeOutlook)
		want  error
	}{
		{"classic outlook missing", func(o *fakeOutlook) { o.unregistered = true }, ErrNoClassicOutlook},
		{"new outlook", func(o *fakeOutlook) { o.newOutlook = true }, ErrNewOutlook},
		{"shell refuses", func(o *fakeOutlook) { o.startErr = errors.New("ERROR_FILE_NOT_FOUND") }, nil},
	}
	for _, c := range cases {
		o := profile()
		o.running = false
		c.setup(o)
		conn, _ := testConnector(t, o, nil)
		err := conn.StartOutlook(t.Context())
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	o := profile()
	conn, _ := testConnector(t, o, nil)
	conn.Close()
	if err := conn.StartOutlook(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestNewRequiresIDConfigAndHost(t *testing.T) {
	if _, err := newConnector("", nil, nil, nil, newFake()); err == nil {
		t.Fatal("empty id must be rejected")
	}
}
