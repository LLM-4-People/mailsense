//go:build windows && outlooklive

package outlook

import (
	"context"
	"flag"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/livetest"
)

// livePick is the live tests' explicit pick: "default" (the profile's
// default store, as the user would pick it) or a comma list of store
// keys. Nothing is ever picked by the app itself.
var livePick = flag.String("live.pick", "default", `mailboxes to pick: "default" or comma-separated store keys`)

// openLive is the one start of every live test of the connector
// (livetest.Open): the limits first, one Discover with unpicked stores
// listed but not walked, the -live.pick stores picked. It returns the
// test's detail budget.
func openLive(t *testing.T, ctx context.Context, c *Connector, cfg *config.Config) *livetest.Budget {
	t.Helper()
	return livetest.Open(t, ctx, cfg, c, *livePick)
}
