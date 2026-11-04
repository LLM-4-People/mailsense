package httpx

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// Policy is the retry policy. Billing rule (PORTING §7): a request is
// retried only when it was never written to the connection, or when the
// server answered with a status in Statuses (it declined the work). Any
// failure after the request was written — read timeouts included — is
// terminal, because retrying could bill twice.
type Policy struct {
	MaxAttempts     int
	Statuses        []int
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	UnsentMaxDelay  time.Duration
	Jitter          float64
	HonorRetryAfter bool
	MaxRetryAfter   time.Duration
	// Final, when set, marks a response with a retryable status as final
	// from its body (a quota 429 means "out of credit", not "slow down").
	Final func(status int, body []byte) bool
	// Never are the statuses that are never retried, whatever Statuses
	// says (schema: http.retry.statuses not_in). WithPolicy cannot clear it.
	Never []int
}

// NeverRetried is the never-retried status list of the schema (the one
// source: providers validate their overrides against it too).
func NeverRetried(s *config.Snapshot) []int {
	st, ok := s.Schema().Lookup("http.retry.statuses")
	if !ok {
		return nil
	}
	out := make([]int, len(st.NotIn))
	for i, v := range st.NotIn {
		out[i] = int(v)
	}
	return out
}

// PolicyFromConfig reads the http.retry.* settings.
func PolicyFromConfig(s *config.Snapshot) Policy {
	return Policy{
		MaxAttempts:     s.Int("http.retry.max_attempts"),
		Statuses:        s.Ints("http.retry.statuses"),
		BaseDelay:       s.Duration("http.retry.base_delay"),
		MaxDelay:        s.Duration("http.retry.max_delay"),
		UnsentMaxDelay:  s.Duration("http.retry.unsent_max_delay"),
		Jitter:          s.Float("http.retry.jitter"),
		HonorRetryAfter: s.Bool("http.retry.honor_retry_after"),
		MaxRetryAfter:   s.Duration("http.retry.max_retry_after"),
		Never:           NeverRetried(s),
	}
}

// Decision is the outcome of one attempt.
type Decision struct {
	Retry  bool
	Delay  time.Duration
	Reason string
}

// Decide judges attempt (0-based) given whether the request was written and
// the response or error it produced. rnd is a uniform sample in [0,1).
func (p Policy) Decide(attempt int, wrote bool, resp *http.Response, err error, now time.Time, rnd float64) Decision {
	var d Decision
	switch {
	case err != nil && wrote:
		return Decision{Reason: "failed after the request was written (terminal: may be billed)"}
	case err != nil:
		d = Decision{Retry: true, Delay: p.backoff(attempt, p.UnsentMaxDelay, rnd), Reason: "not sent: " + err.Error()}
	case resp == nil:
		return Decision{Reason: "no response"}
	case slices.Contains(p.Never, resp.StatusCode):
		return Decision{Reason: fmt.Sprintf("status %d is never retried", resp.StatusCode)}
	case !slices.Contains(p.Statuses, resp.StatusCode):
		return Decision{Reason: fmt.Sprintf("status %d is final", resp.StatusCode)}
	default:
		d = Decision{Retry: true, Delay: p.backoff(attempt, p.MaxDelay, rnd), Reason: fmt.Sprintf("status %d is retryable", resp.StatusCode)}
		if wait, ok := retryAfter(resp.Header, now); ok && p.HonorRetryAfter {
			if wait > p.MaxRetryAfter {
				return Decision{Reason: fmt.Sprintf("server asks to wait %s, more than http.retry.max_retry_after %s", wait, p.MaxRetryAfter)}
			}
			d.Delay = wait
		}
	}
	if attempt+1 >= p.MaxAttempts {
		return Decision{Reason: fmt.Sprintf("%s; %d attempt(s) exhausted", d.Reason, p.MaxAttempts)}
	}
	return d
}

// backoff is mathx.Backoff with the jitter fraction removed at random.
func (p Policy) backoff(attempt int, limit time.Duration, rnd float64) time.Duration {
	d := mathx.Backoff(p.BaseDelay, limit, attempt)
	return d - time.Duration(float64(d)*p.Jitter*rnd)
}
