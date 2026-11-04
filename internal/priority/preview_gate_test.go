package priority

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// settingGate is the fake closed real gate: Allow passes and preview answers
// are allowed exactly while its callback says so. It mirrors the one rule the
// learning machine owns without reproducing the machine's latch in every
// priority test. allowGate (no PreviewAnswers method) remains a fake that
// predates the rule and is preview off.
type settingGate struct{ preview func() bool }

func (settingGate) Allow(context.Context) error { return nil }

func (g settingGate) PreviewAnswers(context.Context) bool {
	return g.preview != nil && g.preview()
}

// TestRankingUsesGatePreviewAnswers pins the rule at the ranking boundary:
// with the setting on, a gate that says PreviewAnswers=false keeps the route
// answer (and its abstention) and reports no preview; flipping the gate alone
// substitutes the preview row, marks the item and the page, and rebuilds the
// snapshot because the flag is part of its identity.
func TestRankingUsesGatePreviewAnswers(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{
			"reply_needed": notCertified(engine.Noul),
			"urgency":      score("urgency", 4),
		},
		Preview: map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}}
	cfg := configtest.New(t, map[string]any{"learning.test_mode": true})
	var gateOn atomic.Bool
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: gateOn.Load},
		Config: cfg, Clock: &fakeClock{now: testNow}, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.RankPage(t.Context(), FilterAll, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.preview || page.Preview {
		t.Fatalf("a gate that denies preview answers reported preview: snapshot=%v page=%v",
			first.preview, page.Preview)
	}
	if len(page.Items) != 1 || page.Items[0].Preview || page.Items[0].Signals.ReplyNeeded != 0 {
		t.Fatalf("the route abstention was substituted or relabelled: %+v", page.Items)
	}

	gateOn.Store(true)
	second, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("flipping PreviewAnswers reused the previous snapshot")
	}
	page, err = svc.RankPage(t.Context(), FilterReply, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !second.preview || !page.Preview {
		t.Fatalf("the allowed preview was not reported: snapshot=%v page=%v", second.preview, page.Preview)
	}
	if len(page.Items) != 1 || !page.Items[0].Preview || page.Items[0].Signals.ReplyNeeded != 1 {
		t.Fatalf("the preview answer was not used: %+v", page.Items)
	}
	if flags := fs.previewFlags(); !slices.Equal(flags, []bool{false, true}) {
		t.Fatalf("store preview flags %v, want one build per flag", flags)
	}
}

// TestPriorityPreviewMatchesTriage pins the shared rule across the (setting,
// gate) matrix. Triage's previewing is the same optional-gate read, and both
// packages ask the learning machine, so comparing priority with
// Machine.PreviewAnswers (the one rule triage consumes) makes Important and
// Triage agree: with the real gate open the setting alone is never preview.
func TestPriorityPreviewMatchesTriage(t *testing.T) {
	cases := []struct {
		setting bool
		open    bool
		want    bool
	}{
		{setting: false, open: false, want: false},
		{setting: false, open: true, want: false},
		{setting: true, open: false, want: true},
		{setting: true, open: true, want: false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("setting=%t/gate_open=%t", tc.setting, tc.open), func(t *testing.T) {
			cfg := configtest.New(t, map[string]any{"learning.test_mode": tc.setting})
			scope, stage := "", learning.Blocked
			if tc.open {
				scope, stage = "mailbox", learning.Done
			}
			machine, err := learning.New(func() string { return "mailbox" },
				&previewLatch{scope: scope}, nil, nil, previewStage{state: stage})
			if err != nil {
				t.Fatal(err)
			}
			machine.SetPreview(cfg.Preview)
			svc, err := New(cfg, Deps{Store: &fakeStore{}, Gate: machine, Config: cfg, RouteKey: testRouteKey})
			if err != nil {
				t.Fatal(err)
			}

			got := svc.previewing(t.Context())
			if want := machine.PreviewAnswers(t.Context()); got != want {
				t.Fatalf("priority preview %v, the learning rule says %v", got, want)
			}
			if got != tc.want {
				t.Fatalf("priority preview %v, want %v", got, tc.want)
			}

			// Allow stays the screen gate (the setting opens the screens even
			// while the real gate is closed). When it passes, the built
			// snapshot must carry the same flag; this is what Important,
			// Triage and the store query actually read.
			allowed := machine.Allow(t.Context()) == nil
			if allowed != (tc.setting || tc.open) {
				t.Fatalf("Allow passed=%v, want %v", allowed, tc.setting || tc.open)
			}
			if !allowed {
				return
			}
			c, err := svc.ranking(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if c.preview != tc.want {
				t.Fatalf("the ranking built with preview=%v, want %v", c.preview, tc.want)
			}
		})
	}
}

func TestPreviewOffWhenGateHasNoPreviewRule(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"learning.test_mode": true})
	svc, err := New(cfg, Deps{Store: &fakeStore{}, Gate: allowGate{}, Config: cfg, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	if svc.previewing(t.Context()) {
		t.Fatal("a gate without PreviewAnswers must not fall back to learning.test_mode")
	}
}

type previewLatch struct{ scope string }

func (l *previewLatch) LearnedScope(context.Context) (string, error) { return l.scope, nil }
func (l *previewLatch) SetLearnedScope(_ context.Context, scope string) error {
	l.scope = scope
	return nil
}

type previewStage struct{ state learning.State }

func (previewStage) ID() string { return "preview-gate-test" }

func (s previewStage) Status(context.Context) (learning.StageStatus, error) {
	return learning.StageStatus{State: s.state}, nil
}
