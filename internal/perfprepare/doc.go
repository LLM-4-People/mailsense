// Package perfprepare prepares the private fixture store for
// scripts/bench-perf.sh. It lives outside triage because it must compute the
// production route key (layer1), which triage cannot import (layer1 imports
// triage's finished topic). Test support only.
package perfprepare
