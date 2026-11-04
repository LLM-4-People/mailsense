package outlook

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/learning"
)

// The circuit breaker of the connection to Outlook. Connector.op is the
// only path that connects; after a lost connection (Outlook crashed,
// closed or restarted under a call: sessionLost — attaching included) it
// refuses to reconnect for a cooldown that grows with each loss, then
// until the running Outlook has been up outlook.breaker.min_uptime, and
// resumes at a pace reduced per loss (factor). Any other connect (the
// first one, or after the user's resume) to an Outlook up less than
// min_uptime is allowed at the pace reduced once more (easing) until it
// has been up that long: a young Outlook is never read at full pace.
// After outlook.breaker.max_losses losses within the window it stops
// until the user resumes it (Connector.Resume). The factor never goes
// below outlook.breaker.min_factor. Its state is kept by the connector and,
// with a keeper (Connector.Keep), in the store: an app restart forgets
// neither the losses nor a stop.

// breakerSettings are the outlook.breaker.* settings.
type breakerSettings struct {
	cooldown, cooldownMax time.Duration
	growth                float64
	minUptime, window     time.Duration
	maxLosses             int
	resumeFactor          float64
	minFactor             float64
}

func breakerFrom(snap *config.Snapshot) breakerSettings {
	return breakerSettings{
		cooldown: snap.Duration("outlook.breaker.cooldown"), cooldownMax: snap.Duration("outlook.breaker.cooldown_max"),
		growth: snap.Float("outlook.breaker.cooldown_growth"), minUptime: snap.Duration("outlook.breaker.min_uptime"),
		window: snap.Duration("outlook.breaker.window"), maxLosses: snap.Int("outlook.breaker.max_losses"),
		resumeFactor: snap.Float("outlook.breaker.resume_factor"), minFactor: snap.Float("outlook.breaker.min_factor"),
	}
}

// cooldownAfter is the cooldown after the n-th loss within the window
// (n ≥ 1): cooldown × growth^(n−1), at most cooldownMax.
func (s breakerSettings) cooldownAfter(n int) time.Duration {
	d := float64(s.cooldown) * math.Pow(s.growth, float64(n-1))
	if d >= float64(s.cooldownMax) {
		return s.cooldownMax
	}
	return time.Duration(d)
}

// Breaker states.
const (
	BreakerClosed  = "closed"  // connecting is allowed
	BreakerCooling = "cooling" // a loss happened; no reconnect before Until
	BreakerWarming = "warming" // the cooldown passed; Outlook has not been up long enough (Until)
	BreakerStopped = "stopped" // too many losses; waits for the user (Resume)
	BreakerEasing  = "easing"  // connected to an Outlook started recently: reduced pace (Factor) until Until
)

// BreakerStatus is the breaker's disclosed state (Connector.Breaker,
// TopicBreaker, Health).
type BreakerStatus struct {
	Source string            `json:"source"`
	State  string            `json:"state"`
	Losses int               `json:"losses"`          // lost connections within the window
	Last   time.Time         `json:"last,omitzero"`   // the latest one
	Until  time.Time         `json:"until,omitzero"`  // cooling or warming: no connect before this
	Factor float64           `json:"factor"`          // the pace multiplier now (1: full pace)
	Error  string            `json:"error,omitempty"` // the last loss's error
	Action []learning.Action `json:"actions,omitempty"`
}

// TopicBreaker carries every change of an Outlook connector's breaker.
var TopicBreaker = events.NewTopic[BreakerStatus]("outlook.breaker")

// breaker is the connector's breaker state (guarded by Connector.mu).
// Its exported fields are what Connector.Keep persists.
type breaker struct {
	Losses  []time.Time `json:"losses"` // within the window, oldest first
	LastErr string      `json:"last_error,omitempty"`
	Stopped bool        `json:"stopped,omitempty"`
	warming time.Time   // the Outlook start time last refused for its uptime (zero: none)
	young   time.Time   // connected to an Outlook younger than min_uptime: reduced pace until then
}

// kept is the part of the state that is persisted (Connector.Keep), a copy.
func (b *breaker) kept() breaker {
	return breaker{Losses: slices.Clone(b.Losses), LastErr: b.LastErr, Stopped: b.Stopped}
}

// prune forgets losses older than the window.
func (b *breaker) prune(now time.Time, s breakerSettings) {
	i := 0
	for i < len(b.Losses) && now.Sub(b.Losses[i]) >= s.window {
		i++
	}
	b.Losses = b.Losses[i:]
}

// allow returns nil when connecting is allowed now, else the named error
// (wrapping source.ErrUnavailable) with when it may be tried again.
// started is the running Outlook's start time (host.Started), read only
// once the cooldown passed.
func (b *breaker) allow(now time.Time, s breakerSettings, started func() (time.Time, error)) error {
	b.prune(now, s)
	if b.Stopped {
		return ErrStopped
	}
	if len(b.Losses) > 0 {
		if until := b.cooling(s); now.Before(until) {
			return fmt.Errorf("%w (until %s)", ErrCoolingDown, until.UTC().Format(time.RFC3339))
		}
	}
	at, err := started()
	if len(b.Losses) == 0 { // a first connect, or after a resume: never refused, eased while Outlook is young
		b.warming = time.Time{}
		if err != nil { // start time unknown (or Outlook starting under us): counted as started now
			at = now
		}
		b.young = at.Add(s.minUptime)
		return nil
	}
	if err != nil {
		return err
	}
	if up := now.Sub(at); up < s.minUptime {
		b.warming = at
		return fmt.Errorf("%w (Outlook is up %s; until %s)", ErrOutlookWarmingUp, up.Round(time.Second),
			at.Add(s.minUptime).UTC().Format(time.RFC3339))
	}
	b.warming, b.young = time.Time{}, time.Time{}
	return nil
}

// cooling is the end of the cooldown after the latest loss.
func (b *breaker) cooling(s breakerSettings) time.Time {
	return b.Losses[len(b.Losses)-1].Add(s.cooldownAfter(len(b.Losses)))
}

// lost records a lost connection; it stops the breaker at maxLosses.
func (b *breaker) lost(now time.Time, s breakerSettings, err error) {
	b.prune(now, s)
	b.Losses = append(b.Losses, now)
	b.LastErr = err.Error()
	b.Stopped = b.Stopped || len(b.Losses) >= s.maxLosses
}

// factor is the pace multiplier: resumeFactor per loss within the window,
// once more while Outlook is young, never below minFactor.
func (b *breaker) factor(now time.Time, s breakerSettings) float64 {
	b.prune(now, s)
	n := len(b.Losses)
	if now.Before(b.young) {
		n++
	}
	return max(s.minFactor, math.Pow(s.resumeFactor, float64(n)))
}

// resume is the user's action after a stop: the losses are forgotten;
// the next connect is a first one (eased while Outlook is young).
func (b *breaker) resume() { *b = breaker{} }

// status is the disclosed state now.
func (b *breaker) status(now time.Time, s breakerSettings) BreakerStatus {
	out := BreakerStatus{State: BreakerClosed, Losses: len(b.Losses), Factor: b.factor(now, s), Error: b.LastErr}
	if n := len(b.Losses); n > 0 {
		out.Last = b.Losses[n-1]
	}
	switch {
	case b.Stopped:
		out.State = BreakerStopped
		out.Action = []learning.Action{learning.Do(learning.ActionResumeSource), learning.Do(learning.ActionStartSource)}
	case len(b.Losses) > 0 && now.Before(b.cooling(s)):
		out.State, out.Until = BreakerCooling, b.cooling(s)
	case !b.warming.IsZero():
		out.State, out.Until = BreakerWarming, b.warming.Add(s.minUptime)
	case now.Before(b.young):
		out.State, out.Until = BreakerEasing, b.young
	}
	return out
}
