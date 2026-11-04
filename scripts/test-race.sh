#!/usr/bin/env bash
# Run the exhaustive gate with the race detector on every package.
#
#   scripts/test-race.sh [extra go test args…]
#
# This is scripts/test.sh with RACE=on: same gofmt, vet matrix, taxonomy and
# sidecar isolation, but every test binary is built with -race. It is the
# release check for concurrency, not the inner loop (the fast gate already runs
# -race on qa and api on every round).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$ROOT"
RACE=on exec "$ROOT/scripts/test.sh" "$@"
