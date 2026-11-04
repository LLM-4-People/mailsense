package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// stateBreaker is the name the breaker's state is kept under.
const stateBreaker = "breaker"

// Keep makes k keep the breaker's state (its losses, the stop and the last
// error) across app restarts, and restores what k holds: an app restarted
// after a crash of Outlook neither forgets the cooldown nor reconnects to a
// source the breaker had stopped. Call it before the first operation; the
// connector works without a keeper (the state then lives as long as it).
func (c *Connector) Keep(ctx context.Context, k source.StateKeeper) error {
	if k == nil {
		return fmt.Errorf("outlook: Keep needs a keeper")
	}
	raw, err := k.LoadState(ctx, c.id, stateBreaker)
	if err != nil {
		return err
	}
	var kept breaker
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &kept); err != nil {
			return fmt.Errorf("outlook: kept breaker state: %w", err)
		}
	}
	s, err := c.settings()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.keeper = k
	if len(raw) > 0 {
		c.brk = kept.kept()
	}
	c.disclose(s)
	st := c.brk.status(c.clock.Now(), s.breaker)
	c.mu.Unlock()
	c.log.Info("Outlook breaker state restored", "state", st.State, "losses", st.Losses, "until", st.Until)
	return nil
}

// breakerPersistTimeout bounds the best-effort breaker write: a hung keeper
// must not hold the caller that is already handling a connector failure.
const breakerPersistTimeout = 2 * time.Second

// persist writes the breaker's state to the keeper (none: nothing); a
// failed write is logged, never an operation's error.
func (c *Connector) persist(b breaker) {
	c.mu.Lock()
	k := c.keeper
	c.mu.Unlock()
	if k == nil {
		return
	}
	raw, err := json.Marshal(b)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), breakerPersistTimeout)
		defer cancel()
		err = k.SaveState(ctx, c.id, stateBreaker, raw)
	}
	if err != nil {
		c.log.Warn("cannot keep the Outlook breaker state; it lives until the app exits", "err", err)
	}
}
