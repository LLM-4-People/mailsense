package decision

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

func TestHTTPFailuresAreClassifiedAndBillingAware(t *testing.T) {
	qs := taxonomy17()[:1]
	ok := reply{200, systemoneAnswers(qs, 10)}
	cases := []struct {
		name    string
		replies []reply
		quota   []string
		want    error
		hits    int
		message string
	}{
		{"402 no credits, never retried", []reply{{402, `{"detail":{"error_type":"insufficient_credits","message":"Add credits"}}`}}, nil, ErrInsufficientCredits, 1, "Add credits"},
		{"401 rejected key", []reply{{401, `{"detail":"bad key"}`}}, nil, ErrAuth, 1, "bad key"},
		{"403 model access", []reply{{403, `{}`}}, nil, ErrAuth, 1, "{}"},
		{"422 Zelvane array detail", []reply{{422, `{"detail":[{"loc":["body"],"msg":"must NOT have more than 16 properties","type":"value_error"}]}`}}, nil, ErrRejected, 1, "16 properties"},
		{"400 rejected", []reply{{400, `not json`}}, nil, ErrRejected, 1, "not json"},
		{"500 is provider-wide: never retried", []reply{{500, `oops`}}, nil, ErrTransient, 1, "oops"},
		{"503 then success is retried", []reply{{503, ``}, ok}, nil, nil, 2, ""},
		{"quota 429 is final", []reply{{429, `{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"out"}}`}},
			[]string{"credit_balance_exhausted"}, ErrQuota, 1, "out"},
		{"rate-limit 429 is retried", []reply{{429, `{"error":{"code":"rate_limit_exceeded"}}`}, ok}, []string{"credit_balance_exhausted"}, nil, 2, ""},
		{"Cloudflare 3036 is final", []reply{{429, `{"success":false,"errors":[{"code":3036,"message":"daily free allocation exceeded"}]}`}},
			[]string{"3036"}, ErrQuota, 1, "allocation"},
	}
	for _, c := range cases {
		srv := newServer(t, c.replies...)
		p := provider(srv, "systemone")
		p.Retry.QuotaCodes = c.quota
		h := newAdapter(t, p)
		_, err := h.ad.Decide(t.Context(), "state", qs)
		if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
		if srv.hits() != c.hits {
			t.Errorf("%s: %d hits, want %d", c.name, srv.hits(), c.hits)
		}
		var apiErr *APIError
		if c.want != nil && (!errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, c.message)) {
			t.Errorf("%s: message %q lacks %q", c.name, err, c.message)
		}
		if c.want != nil && (len(h.sink.recs) != 1 || !h.sink.recs[0].Failed) {
			t.Errorf("%s: a failed call is still accounted: %+v", c.name, h.sink.recs)
		}
	}
}

func TestProviderRetryOverrides(t *testing.T) {
	qs := taxonomy17()[:1]
	srv := newServer(t, reply{529, `{"detail":{"error_type":"overloaded"}}`}, reply{200, systemoneAnswers(qs, 1)})
	p := provider(srv, "systemone")
	p.Retry.Statuses = []int{429, 529}
	h := newAdapter(t, p)
	if _, err := h.ad.Decide(t.Context(), "state", qs); err != nil || srv.hits() != 2 || len(h.clk.Sleeps()) != 1 {
		t.Fatalf("Zelvane's 529 is retried by its preset: %v hits %d", err, srv.hits())
	}
	srv = newServer(t, reply{503, ``})
	p = provider(srv, "systemone")
	one := 1
	p.Retry.MaxAttempts = &one
	h = newAdapter(t, p)
	if _, err := h.ad.Decide(t.Context(), "state", qs); !errors.Is(err, ErrTransient) || srv.hits() != 1 {
		t.Fatalf("max_attempts override: %v hits %d", err, srv.hits())
	}
}

func TestContextBudgetIsEnforcedBeforeSending(t *testing.T) {
	qs := taxonomy17()
	srv := newServer(t, chunkReplies(qs, 16)...)
	p := provider(srv, "systemone")
	p.ContextTokens = 2000
	h := newAdapter(t, p)
	_, err := h.ad.Decide(t.Context(), strings.Repeat("long state ", 600), qs)
	if !errors.Is(err, ErrContextBudget) || srv.hits() != 0 {
		t.Fatalf("an oversized request is refused before any call (Aubelle would truncate silently): %v hits %d", err, srv.hits())
	}
	// The budget covers every chunk: the small second chunk fits, the first does not, nothing is sent.
	first, _ := systemone{}.encode(&p, "short", qs[:16])
	p.ContextTokens = int(p.Tokens(first)) - 1
	h = newAdapter(t, p)
	if _, err := h.ad.Decide(t.Context(), "short", qs); !errors.Is(err, ErrContextBudget) || srv.hits() != 0 {
		t.Fatalf("partial chunks must not be sent: %v hits %d", err, srv.hits())
	}
}

func TestMisuseIsRefusedWithoutCalls(t *testing.T) {
	srv := newServer(t, reply{200, `{}`})
	h := newAdapter(t, provider(srv, "systemone"))
	good := taxonomy17()[:2]
	dup := append(append([]engine.Question(nil), good...), good[0])
	bad := []engine.Question{{Name: "x", Type: engine.Choice, Instructions: "i", Options: []engine.Option{{Name: "only"}}}}
	for name, c := range map[string]struct {
		state string
		qs    []engine.Question
	}{"empty state": {"  \n", good}, "no questions": {"s", nil}, "duplicate names": {"s", dup}, "invalid question": {"s", bad},
		"unknown type": {"s", []engine.Question{{Name: "x", Type: "yesno", Instructions: "i"}}}} {
		if _, err := h.ad.Decide(t.Context(), c.state, c.qs); !errors.Is(err, ErrInput) {
			t.Errorf("%s: want ErrInput, got %v", name, err)
		}
	}
	p := provider(srv, "systemone")
	p.APIKeySecret = "MISSING_KEY"
	h = newAdapter(t, p)
	if _, err := h.ad.Decide(t.Context(), "s", good); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	if srv.hits() != 0 {
		t.Fatalf("misuse must not reach the provider (%d hits)", srv.hits())
	}
	if _, err := New(engine.Provider{Protocol: "nope"}, engine.Deps{}); err == nil {
		t.Fatal("unknown protocol")
	}
}

func TestCancellationStopsBetweenChunks(t *testing.T) {
	qs := taxonomy17()
	srv := newServer(t, chunkReplies(qs, 16)...)
	h := newAdapter(t, provider(srv, "systemone"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err := h.ad.Decide(ctx, "state", qs)
	if !errors.Is(err, context.Canceled) || srv.hits() != 0 || len(res.Answers) != 0 || res.Usage.Calls != 0 {
		t.Fatalf("a cancelled decide sends nothing: %v hits %d", err, srv.hits())
	}
}

func TestPlanEstimatesCallsAndCost(t *testing.T) {
	srv := newServer(t, reply{200, `{}`})
	h := newAdapter(t, provider(srv, "systemone"))
	est, err := h.ad.Plan("state", taxonomy17())
	if err != nil || est.Calls != 2 || est.InputTokens <= 0 || !est.PriceKnown || est.EstimatedUSD != float64(est.InputTokens)*0.05/1e6 {
		t.Fatalf("plan %+v", est)
	}
	p := provider(srv, "systemone")
	p.PriceInput = nil
	h = newAdapter(t, p)
	if est, _ := h.ad.Plan("state", taxonomy17()); est.PriceKnown || est.EstimatedUSD != 0 || srv.hits() != 0 {
		t.Fatalf("unknown price: %+v", est)
	}
	if est, err := h.ad.Plan("", nil); est.Calls != 0 || !errors.Is(err, ErrInput) {
		t.Fatalf("invalid input plans nothing and says why: %+v %v", est, err)
	}
}
