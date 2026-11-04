package triage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// fakeDecider answers every question deterministically and counts calls.
type fakeDecider struct {
	mu      sync.Mutex
	calls   int
	asked   []int // questions asked per Decide
	states  []string
	fail    func(state string) error
	partial func(state string) bool // answer only the first chunk, then fail
	tooBig  func(state string) bool // over the context budget
	extra   float64                 // usage charged beyond the estimate (USD)
	under   bool                    // the ceiling it reserves does not cover extra (a provider billing past it)
	block   chan struct{}           // when set, Decide waits for it (cancellation tests)
	entered chan struct{}           // when set, each Decide sends on it before waiting on block
	value   float64                 // noul probability and confidence
	name    string
}

// fakeSource hands out the fake decider and counts how often it is built.
type fakeSource struct {
	d      *fakeDecider
	builds int
	mu     sync.Mutex
}

func (f *fakeSource) Decider(*config.Config) (engine.Decider, *engine.Catalog, error) {
	f.mu.Lock()
	f.builds++
	f.mu.Unlock()
	return f.d, nil, nil
}

func (f *fakeDecider) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}
func (f *fakeDecider) Model() string       { return "m1" }
func (f *fakeDecider) Fingerprint() string { return "fake-fingerprint" }

func (f *fakeDecider) Plan(state string, qs []engine.Question) (engine.Estimate, error) {
	calls := (len(qs) + 15) / 16
	tokens := int64(len(state)/3+1) * int64(calls)
	est := engine.Estimate{Calls: calls, InputTokens: tokens, EstimatedUSD: float64(tokens) * 0.05 / 1e6, PriceKnown: true}
	if f.tooBig != nil && f.tooBig(state) {
		return engine.Estimate{PriceKnown: true}, fmt.Errorf("%w: too big", decision.ErrContextBudget)
	}
	return est, nil
}

func (f *fakeDecider) Decide(ctx context.Context, state string, qs []engine.Question) (engine.Result, error) {
	if f.block != nil {
		if f.entered != nil {
			f.entered <- struct{}{}
		}
		select {
		case <-f.block:
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		}
	}
	est, err := f.Plan(state, qs)
	if err != nil {
		return engine.Result{}, err
	}
	usage := engine.Usage{Calls: est.Calls, InputTokens: est.InputTokens, EstimatedUSD: est.EstimatedUSD + f.extra}
	ceiling := est
	if !f.under {
		ceiling.EstimatedUSD += f.extra
	}
	if guard := engine.ReserverOf(ctx); guard != nil {
		if !guard.Reserve(ceiling) {
			return engine.Result{}, engine.ErrBudget
		}
		defer guard.Settle(ceiling, usage)
	}
	f.mu.Lock()
	f.calls += est.Calls
	f.asked = append(f.asked, len(qs))
	f.states = append(f.states, state)
	f.mu.Unlock()
	if f.fail != nil {
		if err := f.fail(state); err != nil {
			return engine.Result{Usage: usage}, err
		}
	}
	partial := f.partial != nil && f.partial(state) && len(qs) > 16
	v := f.value
	if v == 0 {
		v = 0.8
	}
	res := engine.Result{Model: "m1", Answers: map[string]engine.Answer{}, Usage: usage}
	if partial {
		qs = qs[:16]
	}
	for _, q := range qs {
		a := engine.Answer{Type: q.Type}
		switch q.Type {
		case engine.Noul:
			a.Value = v
		case engine.Choice:
			c := v
			a.Choice, a.Probabilities, a.Confidence = q.Options[0].Name, map[string]float64{q.Options[0].Name: v}, &c
		case engine.Score:
			c := v
			a.Value, a.Probabilities, a.Confidence = 1, map[string]float64{"1": 1}, &c
		}
		res.Answers[q.Name] = a
	}
	if partial {
		return res, fmt.Errorf("%w: chunk 2 failed", decision.ErrHTTP)
	}
	return res, nil
}

func (f *fakeDecider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// world is a store with owner-scope mail.
type world struct {
	t   testing.TB
	cfg *config.Config
	st  *store.Store
	bus *events.Bus
	dec *fakeDecider
	src *fakeSource
	tr  *Triager
	n   int
}

func newWorld(t testing.TB, settings map[string]any) *world {
	t.Helper()
	return newWorldClock(t, settings, nil)
}

// newWorldClock is newWorld with an injected store clock (the store's busy
// retry waits on it).
func newWorldClock(t testing.TB, settings map[string]any, clk clock.Clock) *world {
	t.Helper()
	if settings == nil {
		settings = map[string]any{}
	}
	if _, ok := settings["time.zone"]; !ok {
		settings["time.zone"] = "America/New_York" // never the machine's zone: deterministic facts
	}
	if _, ok := settings[source.SettingSelected]; !ok {
		settings[source.SettingSelected] = []any{sourceKeyOf("store-1")}
	}
	cfg := configtest.New(t, settings)
	st, err := store.Open(t.Context(), cfg, nil, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, cfg: cfg, st: st, bus: events.New(), dec: &fakeDecider{}}
	w.src = &fakeSource{d: w.dec}
	t.Cleanup(w.bus.Close)
	if w.tr, err = New(cfg, st, w.bus, w.src, learningtest.Open{}, nil); err != nil {
		t.Fatal(err)
	}
	return w
}

// testRouteKey is deliberately distinct from the profile fingerprint: a
// reader that falls back to the paid identity must fail these fixtures.
const testRouteKey = "rk1-0123456789abcdef0123456789abcdef"

// routeKeyer returns the fixture's route key.
func (w *world) routeKeyer(t testing.TB) RouteKeyer {
	t.Helper()
	return RouteKeyFunc(func() (string, error) { return testRouteKey, nil })
}

// useRoute wires the store reader with that key.
func (w *world) useRoute(t testing.TB, r RouteReader) {
	t.Helper()
	w.tr.UseRoute(r, w.routeKeyer(t))
}

var base = time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)

const me = "me@x.com"

// mail adds an inbox message from sender (conversation conv) at offset;
// replyAfter > 0 adds the owner's reply that much later.
func (w *world) mail(sender, conv, subject string, at time.Duration, replyAfter time.Duration) int64 {
	w.t.Helper()
	w.n++
	in := w.message(fmt.Sprintf("in-%d", w.n), mail.RoleInbox, "Inbox", sender, conv, subject, base.Add(at), me)
	in.Body = "Bonjour, pouvez-vous confirmer la RÉUNION de mardi? Voir https://x.y/z\nMerci."
	msgs := []mail.Message{in}
	if replyAfter > 0 {
		out := w.message(fmt.Sprintf("out-%d", w.n), mail.RoleSent, "Sent", me, conv, "RE: "+subject, base.Add(at+replyAfter), sender)
		out.Body = "Oui, c'est confirmé.\nAmelie"
		msgs = append(msgs, out)
	}
	w.write(msgs...)
	return w.rowOf(in.ID)
}

func (w *world) message(id string, role mail.FolderRole, folder, sender, conv, subject string, at time.Time, to string) mail.Message {
	ts := at.UTC().Format(mail.TimeLayout)
	return mail.Message{SourceID: "src", StoreID: "store-1", StoreName: "Me", FolderPath: folder, FolderRole: role, ID: id,
		Subject: subject, SenderName: strings.Split(sender, "@")[0], SenderAddress: sender, ConversationID: conv,
		Recipients: []mail.Recipient{{Kind: mail.To, Address: to}}, ReceivedAt: ts, SentOn: ts, AddressedToMe: mail.AddressedTo}
}

func (w *world) write(msgs ...mail.Message) {
	w.t.Helper()
	if err := w.st.UpsertMessages(w.t.Context(), 0, msgs); err != nil {
		w.t.Fatal(err)
	}
	w.ownAll()
}

// ownAll marks every message owner scope (scope itself is tested elsewhere).
func (w *world) ownAll() {
	var rows []store.VerdictRow
	_ = w.st.EachMessage(w.t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		reason := scope.ReasonAddressedFlag
		if r.Message.FolderRole == mail.RoleSent {
			reason = scope.ReasonSentByOwner
		}
		rows = append(rows, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: reason})
		return nil
	})
	if err := w.st.WriteVerdicts(w.t.Context(), "test", rows); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) rowOf(msgID string) int64 {
	var id int64
	_ = w.st.EachMessage(w.t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == msgID {
			id = r.RowID
		}
		return nil
	})
	return id
}

// states composes the current state of every eligible message.
func (w *world) states() map[int64]string {
	w.t.Helper()
	s, err := w.tr.prepare(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[int64]string{}
	if err := s.each(w.t.Context(), w.st, nil, nil, func(r *store.MessageRow, state string) error { out[r.RowID] = state; return nil }); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) run(o Options) Report {
	w.t.Helper()
	rep, err := w.tr.Run(w.t.Context(), o, nil)
	if err != nil && !errors.Is(err, ErrBudget) {
		w.t.Logf("run error: %v", err)
	}
	return rep
}

// withSettings returns a triager over the same store with other settings.
func (w *world) withSettings(settings map[string]any) *world {
	w.t.Helper()
	for k, v := range settings {
		if err := w.cfg.Set(k, v); err != nil {
			w.t.Fatal(err)
		}
	}
	return w
}

func sourceKeyOf(storeID string) string { return source.StoreKey("src", storeID) }
