package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// lostSource is what a connector returns once the source is gone.
var lostSource = source.Unavailable("fake: the connection was lost")

// Review of the crash-safety round, #4: one item that made Outlook raise
// an exception (RPC_E_SERVERFAULT) ended the run as a lost connection and
// stayed partial — newest first, it was read first on every run: three
// runs, three losses, the source stopped with Outlook alive (failed first:
// run 2 read s4 again). It is set aside (persisted, at its revision), the
// run still ends, later runs skip it, the count is disclosed with an
// action, and the user's retry (or a new revision) reads it again.
func TestAnItemThatFaultsTheSourceIsSetAside(t *testing.T) {
	s, st, _, cfg := setup(t, nil)
	c := newSnapConn()
	c.failItem["s4"] = &source.ItemError{Code: "0x80010105", Err: fmt.Errorf("fake: Body: %w", lostSource), Quarantine: true}
	rep, err := s.Run(t.Context(), c, nil)
	if !errors.Is(err, source.ErrUnavailable) || strings.Join(c.read, ",") != "s4" {
		t.Fatalf("run 1 must end on the fault: %v read %v", err, c.read)
	}
	if rep.Details == nil || rep.Details.Quarantined != 1 || rep.Details.Failures["0x80010105"] != 1 {
		t.Fatalf("run 1 report %+v", rep.Details)
	}
	c.read = nil
	rep, err = s.Run(t.Context(), c, nil)
	if err != nil || strings.Join(c.read, ",") != "i3,i2,i1" || rep.Details.Quarantined != 1 || rep.Details.Pending != 1 {
		t.Fatalf("run 2 must skip the set-aside item: %v read %v %+v", err, c.read, rep.Details)
	}
	status, err := SyncStage(cfg, st).Status(t.Context())
	if err != nil || status.State != learning.Running || len(status.Blockers) != 1 || status.Blockers[0].Code != BlockDetailsQuarantined ||
		len(status.Blockers[0].Actions) == 0 || status.Blockers[0].Actions[0].ID != learning.ActionRetryDetails {
		t.Fatalf("the set-aside item is a disclosed blocker with an action: %v %+v", err, status)
	}
	// The user's retry: read again (it succeeds now).
	delete(c.failItem, "s4")
	n, err := s.RetryDetails(t.Context(), "fake")
	if err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	c.read = nil
	if rep, err = s.Run(t.Context(), c, nil); err != nil || strings.Join(c.read, ",") != "s4" || rep.Details.Quarantined != 0 || rep.Details.Pending != 0 {
		t.Fatalf("after the retry: %v read %v %+v", err, c.read, rep.Details)
	}
	// Misuse: an unknown source retries nothing.
	if n, err := s.RetryDetails(t.Context(), "nope"); err != nil || n != 0 {
		t.Fatalf("unknown source: %d %v", n, err)
	}
}

// A set-aside item that changes (a new revision) is read again by itself.
func TestASetAsideItemThatChangesIsReadAgain(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := newSnapConn()
	c.failItem["s4"] = &source.ItemError{Code: "0x80010105", Err: lostSource, Quarantine: true}
	_, _ = s.Run(t.Context(), c, nil)
	delete(c.failItem, "s4")
	c.stamps["s4"] = "2026-10-05T00:00:00Z" // the message changed in the source
	c.read = nil
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || !strings.HasPrefix(strings.Join(c.read, ","), "s4") || rep.Details.Quarantined != 0 {
		t.Fatalf("a new revision is read again: %v read %v %+v", err, c.read, rep.Details)
	}
}

// The detail reads of a batch done before the source became unavailable
// were thrown away with the batch (failed first: s4 and i3 stayed partial
// and were read again — more load on a source that just failed).
func TestDetailsReadBeforeAnUnavailableSourceAreKept(t *testing.T) {
	s, st, _, _ := setup(t, map[string]any{"sync.details.batch_size": 4})
	c := newSnapConn()
	c.failItem["i2"] = lostSource
	if _, err := s.Run(t.Context(), c, nil); !errors.Is(err, source.ErrUnavailable) {
		t.Fatal(err)
	}
	for id, partial := range map[string]bool{"s4": false, "i3": false, "i2": true, "i1": true} {
		m, err := st.GetMessage(t.Context(), "fake", "store-1", id)
		if err != nil || m.Partial != partial {
			t.Errorf("%s: partial %v, want %v (%v)", id, m.Partial, partial, err)
		}
	}
}

// sync.details.enabled off: metadata only, no detail read at all (a live
// test's detail budget spent, or a user sparing the source).
func TestDetailsCanBeTurnedOff(t *testing.T) {
	s, _, _, _ := setup(t, map[string]any{"sync.details.enabled": false})
	c := newSnapConn()
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || len(c.read) != 0 || rep.Details == nil || !rep.Details.Disabled || rep.Details.Pending != 4 {
		t.Fatalf("%v read %v %+v", err, c.read, rep.Details)
	}
}

// probeConn has folders that stop being discovered and a prober.
type probeConn struct {
	*snapConn
	hideSent bool
	probeErr error
}

func (c *probeConn) Discover(ctx context.Context) ([]mail.Folder, error) {
	all, err := c.snapConn.Discover(ctx)
	if !c.hideSent {
		return all, err
	}
	var out []mail.Folder
	for _, f := range all {
		if f.Path != "Sent" {
			out = append(out, f)
		}
	}
	return out, err
}

func (c *probeConn) FolderStatus(context.Context, mail.Folder) (source.FolderStatus, error) {
	return source.FolderGone, c.probeErr
}

// Lower finding: the check of folders no longer discovered logged an
// unavailable source and went on (failed first: the run succeeded). It
// ends the run, and nothing is removed.
func TestAnUnavailableSourceEndsTheVanishedFolderCheck(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := &probeConn{snapConn: newSnapConn()}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	c.hideSent, c.probeErr = true, lostSource
	if _, err := s.Run(t.Context(), c, nil); !errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("an unavailable prober must end the run: %v", err)
	}
	if m, err := st.GetMessage(t.Context(), "fake", "store-1", "s4"); err != nil || m.ID != "s4" {
		t.Fatalf("nothing is removed on an aborted check: %v", err)
	}
}
