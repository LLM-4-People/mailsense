package engine_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// usageRecorder is a UsageSink that keeps every record.
type usageRecorder struct {
	mu      sync.Mutex
	records []engine.UsageRecord
}

func (r *usageRecorder) AddUsage(_ context.Context, rec engine.UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

func (r *usageRecorder) all() []engine.UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.UsageRecord(nil), r.records...)
}

// roundTripFunc lets one test answer (or fail) every request without a server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func answer(status int, body string) roundTripFunc {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
}

// TestPaidCallAccountsEveryFailureKind pins the one accounting rule of a paid
// call: a transport failure after the write and a status the provider did not
// decline are estimates; a declined status is not; every failure is reported
// once with status 0 for a transport failure. The decider and the generator
// share this path, so a drift here would under-report spend for both.
func TestPaidCallAccountsEveryFailureKind(t *testing.T) {
	cfg := configtest.New(t, nil)
	clk := clocktest.New(time.Unix(100, 0))
	p := engine.Provider{ID: "prov", Model: "m", Protocol: engine.ProtocolOpenAIChat,
		CharsPerToken: 4, PriceInput: price(2), PriceOutput: price(4),
		Retry: engine.Retry{QuotaCodes: []string{"quota"}}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	body := []byte(`{"model":"m"}`)

	cases := []struct {
		name          string
		response      roundTripFunc
		status        int
		estimated     bool
		wantStatus    int
		wantInputFrom int64 // -1: whatever the estimate is
	}{
		{name: "transport failure", response: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		}, wantStatus: 0},
		{name: "transport failure after the write", response: func(*http.Request) (*http.Response, error) {
			return nil, httpx.ErrAfterWrite
		}, wantStatus: 0, estimated: true},
		{name: "declined status", response: answer(http.StatusBadRequest, `{"error":{"message":"bad"}}`),
			status: 400, wantStatus: 400},
		{name: "insufficient credits", response: answer(http.StatusPaymentRequired, `{"error":{"message":"pay"}}`),
			status: 402, wantStatus: 402},
		{name: "quota code", response: answer(http.StatusTooManyRequests, `{"error":{"code":"quota"}}`),
			status: 429, wantStatus: 429},
		{name: "server failure", response: answer(http.StatusInternalServerError, `{"error":{"message":"boom"}}`),
			status: 500, wantStatus: 500, estimated: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &usageRecorder{}
			call := engine.PaidCall{Deps: engine.Deps{Config: cfg, Sink: sink, Clock: clk},
				Provider: &p, Clock: clk, Log: quiet, Label: "test call"}
			client := &http.Client{Transport: tc.response}
			status, _, usage, err, ok := call.Post(t.Context(), client,
				engine.PostSpec{URL: "http://provider.invalid/v1", Key: "k", Body: body},
				engine.SentinelPolicy(p.ID, engine.ErrInput, engine.ErrHTTP), body)
			if ok || err == nil {
				t.Fatalf("a failed call reported ok=%v err=%v", ok, err)
			}
			if status != tc.status {
				t.Fatalf("status %d, want %d", status, tc.status)
			}
			records := sink.all()
			if len(records) != 1 {
				t.Fatalf("%d records for one call", len(records))
			}
			rec := records[0]
			if !rec.Failed || rec.Status != tc.wantStatus || rec.Estimated != tc.estimated {
				t.Fatalf("record %+v, want failed status=%d estimated=%v", rec, tc.wantStatus, tc.estimated)
			}
			if rec.Error == "" || rec.Provider != p.ID || rec.Model != p.Model {
				t.Fatalf("record identity %+v", rec)
			}
			// An estimated failure is accounted at the request estimate; a
			// status the provider explicitly declined cost nothing, so it
			// carries no invented tokens. Either way the call is reported.
			var wantTokens int64
			if tc.estimated {
				wantTokens = p.Tokens(body)
			}
			if rec.InputTokens != wantTokens || rec.InputTokens != usage.InputTokens {
				t.Fatalf("input tokens %d/%d, want %d", rec.InputTokens, usage.InputTokens, wantTokens)
			}
			if want := p.Cost(wantTokens, 0); rec.EstimatedUSD != want {
				t.Fatalf("usd %v, want %v", rec.EstimatedUSD, want)
			}
			if rec.Usage.Calls != 1 || rec.Usage.Duration != 0 {
				t.Fatalf("base usage %+v", rec.Usage)
			}
		})
	}
}

// TestPaidCallLeavesSuccessToTheAdapter: a 200 is not reported by Post. The
// adapter decodes it first (the token counts and the billed flag come from the
// response), then reports once.
func TestPaidCallLeavesSuccessToTheAdapter(t *testing.T) {
	cfg := configtest.New(t, nil)
	clk := clocktest.New(time.Unix(100, 0))
	p := engine.Provider{ID: "prov", Model: "m", Protocol: engine.ProtocolOpenAIChat, CharsPerToken: 4, PriceInput: price(2)}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := &usageRecorder{}
	call := engine.PaidCall{Deps: engine.Deps{Config: cfg, Sink: sink, Clock: clk},
		Provider: &p, Clock: clk, Log: quiet, Label: "test call"}
	client := &http.Client{Transport: answer(http.StatusOK, `{"ok":true}`)}

	status, raw, usage, err, ok := call.Post(t.Context(), client,
		engine.PostSpec{URL: "http://provider.invalid/v1", Body: []byte("{}")},
		engine.SentinelPolicy(p.ID, engine.ErrInput, engine.ErrHTTP), []byte("{}"))
	if !ok || err != nil || status != http.StatusOK || len(raw) == 0 {
		t.Fatalf("success reported ok=%v status=%d err=%v raw=%q", ok, status, err, raw)
	}
	if records := sink.all(); len(records) != 0 {
		t.Fatalf("a 200 was reported before the adapter decoded it: %+v", records)
	}
	if usage.Calls != 1 || usage.EstimatedUSD != 0 {
		t.Fatalf("base usage %+v", usage)
	}

	call.Report(t.Context(), usage, status, nil, false, false)
	records := sink.all()
	if len(records) != 1 || records[0].Failed || records[0].Status != http.StatusOK {
		t.Fatalf("reported %+v", records)
	}
}
