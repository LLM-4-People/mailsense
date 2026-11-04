package rfc5322

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// capture is a sink that keeps what a sync delivers.
type capture struct {
	msgs     []mail.Message
	skips    map[string]error
	versions []source.Version
}

func (c *capture) Message(_ context.Context, m mail.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}
func (c *capture) Skip(ref string, err error) { c.skips[ref] = err }
func (c *capture) Progress(source.Progress)   {}
func (c *capture) Snapshot(_ context.Context, items []source.Version, _ bool) (map[string]bool, error) {
	c.versions = append(c.versions, items...)
	want := map[string]bool{}
	for _, it := range items {
		want[it.ID] = true
	}
	return want, nil
}

// syncAll discovers root and syncs every folder into a capture.
func syncAll(t *testing.T, root string, settings map[string]any) *capture {
	t.Helper()
	c, err := New("files", root, configtest.New(t, settings))
	must(t, err)
	folders, err := c.Discover(t.Context())
	must(t, err)
	out := &capture{skips: map[string]error{}}
	for _, f := range folders {
		if _, err := c.Sync(t.Context(), f, "", out); err != nil {
			t.Fatalf("sync %s: %v", f.Path, err)
		}
	}
	return out
}

// one syncs a single .eml message and returns it.
func one(t *testing.T, raw string) mail.Message { return oneWith(t, raw, nil) }

// oneWith syncs a single .eml message with the given settings.
func oneWith(t *testing.T, raw string, settings map[string]any) mail.Message {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.eml")
	write(t, p, raw)
	got := syncAll(t, p, settings)
	if len(got.msgs) != 1 {
		t.Fatalf("one message expected: %d (skips %v)", len(got.msgs), got.skips)
	}
	return got.msgs[0]
}

func altered(m mail.Message, code string) bool {
	return slices.ContainsFunc(m.Alterations, func(a mail.Alteration) bool { return a.Code == code })
}
