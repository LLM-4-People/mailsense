package outlook

import (
	"errors"
	"strings"
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

// A configured profile is verified against the MAPI session the connector
// actually attached to. A running Outlook cannot be switched, so a mismatch
// is refused before any mail is read.
func TestConnectVerifiesTheConfiguredProfile(t *testing.T) {
	o := profile()
	o.profile = "A"
	c, _ := testConnector(t, o, map[string]any{"outlook.profile": "B"})
	_, err := c.Discover(t.Context())
	if !errors.Is(err, ErrWrongProfile) {
		t.Fatalf("mismatch: %v, want ErrWrongProfile", err)
	}
	if cause, _ := source.Cause(err); cause != "outlook_wrong_profile" {
		t.Fatalf("cause %q", cause)
	}

	o = profile()
	o.profile = "B"
	c, _ = testConnector(t, o, map[string]any{"outlook.profile": "B"})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("matching profile: %v", err)
	}

	// Empty means the profile already open, whatever it is.
	o = profile()
	o.profile = "A"
	c, _ = testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("empty profile: %v", err)
	}
	if h, err := c.Health(t.Context()); err != nil || h.Profile != "A" {
		t.Fatalf("health %+v %v", h, err)
	}
}

// The user's Start action opens the configured profile; a running Outlook is
// left alone, and a configured mismatch is named instead of silently ignored.
func TestStartOutlookUsesTheConfiguredProfile(t *testing.T) {
	o := profile()
	o.running = false
	c, _ := testConnector(t, o, map[string]any{"outlook.profile": "Work Mail"})
	if err := c.StartOutlook(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if o.startProfile != "Work Mail" {
		t.Fatalf("started profile %q, want Work Mail", o.startProfile)
	}
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("attach after start: %v", err)
	}

	o = profile()
	o.profile = "A"
	c, _ = testConnector(t, o, map[string]any{"outlook.profile": "B"})
	if err := c.StartOutlook(t.Context()); !errors.Is(err, ErrWrongProfile) {
		t.Fatalf("running mismatch: %v, want ErrWrongProfile", err)
	}
	if o.starts != 0 {
		t.Fatalf("a running Outlook must not be started (%d)", o.starts)
	}
}

// Probe turns the connector's named preconditions into the readiness a setup
// screen renders, without starting Outlook.
func TestProbeReportsReadiness(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	if got := c.Probe(t.Context()); !got.Available || got.Cause != "" {
		t.Fatalf("running: %+v", got)
	}

	o = profile()
	o.running = false
	c, _ = testConnector(t, o, nil)
	got := c.Probe(t.Context())
	if got.Available || got.Cause != "outlook_not_running" {
		t.Fatalf("not running: %+v", got)
	}
	if o.starts != 0 {
		t.Fatalf("probe started Outlook (%d)", o.starts)
	}

	o = profile()
	o.profile = "A"
	c, _ = testConnector(t, o, map[string]any{"outlook.profile": "B"})
	got = c.Probe(t.Context())
	if got.Available || got.Cause != "outlook_wrong_profile" || !strings.Contains(got.Detail, `"A"`) {
		t.Fatalf("wrong profile: %+v", got)
	}
}

func TestNewRequiresIDConfigAndHost(t *testing.T) {
	if _, err := newConnector("", nil, nil, nil, newFake()); err == nil {
		t.Fatal("empty id must be rejected")
	}
}
