package generate

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
)

// queuedReply is one scripted response of a parity server.
type queuedReply struct {
	status int
	body   string
}

// parityServer answers with the next queued reply; the last one repeats.
// It counts every attempt so the table can assert retry/quota finality.
type parityServer struct {
	*httptest.Server
	mu      sync.Mutex
	replies []queuedReply
	hits    int
}

func newParityServer(t *testing.T, replies ...queuedReply) *parityServer {
	t.Helper()
	s := &parityServer{replies: replies}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		i := min(s.hits, len(s.replies)-1)
		s.hits++
		rep := s.replies[i]
		s.mu.Unlock()
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *parityServer) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *sink) all() []engine.UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]engine.UsageRecord(nil), s.recs...)
}

// newParityAdapter builds a generator with a fake clock so retries and
// quota finality are observable without wall-clock delays.
func newParityAdapter(t *testing.T, p engine.Provider) harness {
	t.Helper()
	h := harness{sink: &sink{}}
	ad, err := New(p, engine.Deps{
		Config:  configtest.New(t, nil),
		Secrets: fakeSecrets{"GEN_KEY": "k"},
		Sink:    h.sink,
		Clock:   clocktest.New(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.ad = ad
	return h
}

// TestStatusClassificationParity pins the classification every caller of
// the generator depends on: the public call's error class, the usage
// record's billing estimate and the retry/quota-finality hit count. The
// expected values are the ones the pre-consolidation adapter produced.
func TestStatusClassificationParity(t *testing.T) {
	const quotaBody = `{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"out of credits"}}`
	cases := []struct {
		name                string
		replies             []queuedReply
		retryStatuses       []int
		quotaCodes          []string
		maxAttempts         int
		want                error
		wantDeclined        bool
		wantFailedEstimated bool
		wantHits            int
	}{
		{"200", []queuedReply{{200, goodBody}}, nil, nil, 1, nil, true, false, 1},
		{"400", []queuedReply{{400, `{"error":{"message":"bad request"}}`}}, nil, nil, 1, engine.ErrRejected, true, false, 1},
		{"404", []queuedReply{{404, `{"error":{"message":"not found"}}`}}, nil, nil, 1, engine.ErrRejected, true, false, 1},
		{"408", []queuedReply{{408, `{"error":{"message":"timeout"}}`}}, nil, nil, 1, engine.ErrTransient, true, false, 1},
		{"422", []queuedReply{{422, `{"error":{"message":"invalid"}}`}}, nil, nil, 1, engine.ErrRejected, true, false, 1},
		{"429", []queuedReply{{429, `{"error":{"message":"slow down"}}`}}, nil, nil, 1, engine.ErrTransient, true, false, 1},
		{"500", []queuedReply{{500, `{"error":{"message":"boom"}}`}}, nil, nil, 1, engine.ErrTransient, false, true, 1},
		{"503", []queuedReply{{503, `{"error":{"message":"unavailable"}}`}}, nil, nil, 1, engine.ErrTransient, true, false, 1},
		{"configured extra retryable then success", []queuedReply{{529, `{"error":{"message":"overloaded"}}`}, {200, goodBody}},
			[]int{429, 529}, nil, 0, nil, true, false, 2},
		{"quota 429 is final", []queuedReply{{429, quotaBody}}, nil, []string{"credit_balance_exhausted"}, 0, engine.ErrQuota, true, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newParityServer(t, tc.replies...)
			p := preset(t, &server{Server: srv.Server})
			p.Retry.Statuses = tc.retryStatuses
			p.Retry.QuotaCodes = tc.quotaCodes
			if tc.maxAttempts > 0 {
				attempts := tc.maxAttempts
				p.Retry.MaxAttempts = &attempts
			}
			h := newParityAdapter(t, p)
			if got := p.Declined(tc.replies[0].status, h.ad.deps.Config); got != tc.wantDeclined {
				t.Fatalf("provider declined(%d) = %v, want %v", tc.replies[0].status, got, tc.wantDeclined)
			}
			_, err := h.ad.Generate(t.Context(), "You label email.", "Message: invoice")
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if got := srv.attempts(); got != tc.wantHits {
				t.Fatalf("%d attempts, want %d", got, tc.wantHits)
			}
			for _, rec := range h.sink.all() {
				if rec.Failed && rec.Estimated != tc.wantFailedEstimated {
					t.Fatalf("failed call estimated = %v, want %v (record %+v)", rec.Estimated, tc.wantFailedEstimated, rec)
				}
			}
		})
	}
}
