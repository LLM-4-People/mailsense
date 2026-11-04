package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

type countSink struct{ n int }

func (c *countSink) AddUsage(context.Context, UsageRecord) error { c.n++; return nil }

type oneSecret struct{ secrets.Store }

func (oneSecret) Get(name string) (string, error) {
	if name == "K" {
		return "v", nil
	}
	return "", secrets.ErrNotFound
}

func TestMonitorPublishesHealthChanges(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	sub, _ := events.Subscribe(bus, TopicProviderHealth, 8)
	next := &countSink{}
	m := NewMonitor(bus, next, nil)
	for _, failed := range []bool{false, false, true, true, false} {
		_ = m.AddUsage(t.Context(), UsageRecord{Provider: "p", Failed: failed, Status: map[bool]int{true: 402, false: 200}[failed]})
	}
	var seen []bool
	for len(sub.C) > 0 {
		seen = append(seen, (<-sub.C).Healthy)
	}
	if len(seen) != 3 || !seen[0] || seen[1] || !seen[2] || next.n != 5 {
		t.Fatalf("published on first call and on each change: %v (forwarded %d)", seen, next.n)
	}
	st := m.Status(Provider{ID: "p", APIKeySecret: "K"}, oneSecret{})
	if !st.KeyPresent || st.Calls != 5 || st.Failures != 2 || !st.Healthy {
		t.Fatalf("status %+v", st)
	}
	if m.Status(Provider{ID: "q", APIKeySecret: "X"}, oneSecret{}).KeyPresent {
		t.Fatal("missing key")
	}
	if !m.Status(Provider{ID: "local"}, oneSecret{}).KeyPresent {
		t.Fatal("a record without a secret needs no key")
	}
}

type fixedDecider struct{ got []Question }

func (f *fixedDecider) Name() string                              { return "f" }
func (f *fixedDecider) Model() string                             { return "m" }
func (f *fixedDecider) Fingerprint() string                       { return "fp" }
func (f *fixedDecider) Plan(string, []Question) (Estimate, error) { return Estimate{}, nil }
func (f *fixedDecider) Decide(_ context.Context, s string, qs []Question) (Result, error) {
	f.got = qs
	return Result{Answers: map[string]Answer{qs[0].Name: {Type: Noul, Value: 0.9}}}, nil
}

func TestProviderTestUsesTheProtocolProbe(t *testing.T) {
	cfg := configtest.New(t, nil)
	cat, err := BuildCatalog(cfg, []Provider{{
		ID: "dec", Protocol: ProtocolSystemOne, BaseURL: "http://127.0.0.1:9/v1", Path: "/systemone", Model: "m",
		APIKeySecret: "K", MaxQuestions: 8, ContextTokens: 1000, CharsPerToken: 3, ReserveTokensPerByte: 1,
		ProbabilityTolerance: 0.02,
	}})
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	d := &fixedDecider{}
	reg.RegisterDecider(ProtocolSystemOne, func(Provider, Deps) (Decider, error) { return d, nil })
	deps := Deps{Config: cfg, Secrets: oneSecret{}}
	res, err := reg.TestProvider(t.Context(), cat, "dec", deps)
	if err != nil || !res.OK || res.LatencyMS < 0 || res.Model != "m" || res.Detail == "" || res.DetailCode != "decider_answered" {
		t.Fatalf("test result %+v %v", res, err)
	}
	if len(d.got) != 1 || d.got[0].Name != "is_test" {
		t.Fatalf("probe %+v", d.got)
	}
	if _, err := reg.TestProvider(t.Context(), cat, "missing", deps); err == nil {
		t.Fatal("an unknown record is an explicit error")
	}
}

func TestProtocolTableIsValid(t *testing.T) {
	if problems := protocolProblems(); len(problems) > 0 {
		t.Fatalf("protocol descriptors: %v", problems)
	}
	for _, proto := range Protocols() {
		if proto.Kind == KindDecider && proto.Probe == nil {
			t.Errorf("%s: a decider protocol needs a probe", proto.Name)
		}
	}
}

// A provider body is untrusted text that can echo a key, an address or a
// prompt fragment. The status reaches /api/providers and the SSE stream, so
// it stores the same redacted form the log pipeline applies.
func TestProviderStatusRedactsTheErrorText(t *testing.T) {
	m := NewMonitor(nil, nil, nil)
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	const address = "bob@example.test"
	_ = m.AddUsage(context.Background(), UsageRecord{Provider: "p", Failed: true,
		Error: "upstream said " + secret + " for " + address})
	st := m.Status(Provider{ID: "p"}, nil)
	if strings.Contains(st.LastError, secret) || strings.Contains(st.LastError, address) {
		t.Fatalf("status leaked the provider body: %q", st.LastError)
	}
	if !strings.Contains(st.LastError, config.SecretMask) {
		t.Fatalf("status did not mark the value: %q", st.LastError)
	}
}
