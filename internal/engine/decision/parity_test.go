package decision

import (
	"errors"
	"testing"
)

// TestStatusClassificationParity pins the classification every caller of
// the decider depends on: the public call's error class, the usage record's
// billing estimate and the retry/quota-finality hit count. The expected
// values are the ones the pre-consolidation adapter produced.
func TestStatusClassificationParity(t *testing.T) {
	qs := taxonomy17()[:1]
	ok := reply{200, systemoneAnswers(qs, 10)}
	const quotaBody = `{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"out of credits"}}`
	cases := []struct {
		name                string
		replies             []reply
		retryStatuses       []int
		quotaCodes          []string
		maxAttempts         int
		want                error
		wantDeclined        bool
		wantFailedEstimated bool
		wantHits            int
	}{
		{"200", []reply{ok}, nil, nil, 1, nil, true, false, 1},
		{"400", []reply{{400, `{"detail":"bad request"}`}}, nil, nil, 1, ErrRejected, true, false, 1},
		{"404", []reply{{404, `{"detail":"not found"}`}}, nil, nil, 1, ErrRejected, true, false, 1},
		{"408", []reply{{408, `{"detail":"timeout"}`}}, nil, nil, 1, ErrTransient, true, false, 1},
		{"422", []reply{{422, `{"detail":[{"msg":"invalid","type":"value_error"}]}`}}, nil, nil, 1, ErrRejected, true, false, 1},
		{"429", []reply{{429, `{"detail":"slow down"}`}}, nil, nil, 1, ErrTransient, true, false, 1},
		{"500", []reply{{500, `oops`}}, nil, nil, 1, ErrTransient, false, true, 1},
		{"503", []reply{{503, `oops`}}, nil, nil, 1, ErrTransient, true, false, 1},
		{"configured extra retryable then success", []reply{{529, `{"detail":{"error_type":"overloaded"}}`}, ok},
			[]int{429, 529}, nil, 0, nil, true, false, 2},
		{"quota 429 is final", []reply{{429, quotaBody}}, nil, []string{"credit_balance_exhausted"}, 0, ErrQuota, true, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, tc.replies...)
			p := provider(srv, "systemone")
			p.Retry.Statuses = tc.retryStatuses
			p.Retry.QuotaCodes = tc.quotaCodes
			if tc.maxAttempts > 0 {
				attempts := tc.maxAttempts
				p.Retry.MaxAttempts = &attempts
			}
			h := newAdapter(t, p)
			if got := p.Declined(tc.replies[0].status, h.ad.deps.Config); got != tc.wantDeclined {
				t.Fatalf("provider declined(%d) = %v, want %v", tc.replies[0].status, got, tc.wantDeclined)
			}
			_, err := h.ad.Decide(t.Context(), "state", qs)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if got := srv.hits(); got != tc.wantHits {
				t.Fatalf("%d attempts, want %d", got, tc.wantHits)
			}
			for _, rec := range h.sink.recs {
				if rec.Failed && rec.Estimated != tc.wantFailedEstimated {
					t.Fatalf("failed call estimated = %v, want %v (record %+v)", rec.Estimated, tc.wantFailedEstimated, rec)
				}
			}
		})
	}
}
