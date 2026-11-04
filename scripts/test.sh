#!/usr/bin/env bash
# Run the exhaustive quality gate: gofmt, go vet (host and Windows tags),
# and every package's tests in parallel.
# The inner loop is scripts/test-fast.sh; this gate is the release check, and
# scripts/test-race.sh is the exhaustive race check.
#
#   scripts/test.sh [extra go test args…]     e.g. scripts/test.sh -run Store -v
#
# Environment:
#   PARALLEL   test binaries run at once (default 8; the host has 12 cores)
#   RACE       off (default: the exhaustive correctness gate) | on | auto
#              scripts/test-race.sh is the same gate with RACE=on
#   GO         Go toolchain to use
set -euo pipefail
# shellcheck disable=SC1091 # lib.sh is resolved from this file's own directory
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1091 # test-lib.sh is resolved from this file's own directory
source "$(dirname "${BASH_SOURCE[0]}")/test-lib.sh"
cd "$ROOT"

RACE="${RACE:-off}"
PARALLEL="${PARALLEL:-8}"
[[ "$PARALLEL" =~ ^[1-9][0-9]*$ ]] || die "PARALLEL must be a positive whole number"

gate_gofmt
gate_scripts
gate_vet
gate_race_flags

# The sidecar suite starts real child processes that bind loopback ports and
# checks the child owns the port. Running it beside every other package makes
# that check race unrelated process startup, so it runs alone first.
mapfile -t pkgs < <("$GO" list ./... | grep -v '^github\.com/LLM-4-People/mailsense/internal/sidecar$')
((${#pkgs[@]})) || die "no packages to test"

say "sidecar suite (isolated: it binds loopback ports)"
if ((${#RACE_FLAGS[@]})); then
	run_stage "sidecar suite" env CGO_ENABLED=1 "$GO" test "${RACE_FLAGS[@]}" -vet=off -count=1 "$@" ./internal/sidecar
else
	run_stage "sidecar suite" "$GO" test -vet=off -count=1 "$@" ./internal/sidecar
fi

say "go test ${RACE_FLAGS[*]:-} -p $PARALLEL (${#pkgs[@]} packages, sidecar isolated)"
if ((${#RACE_FLAGS[@]})); then
	run_stage "go test (race, -p $PARALLEL)" env CGO_ENABLED=1 "$GO" test "${RACE_FLAGS[@]}" -vet=off -count=1 -p "$PARALLEL" "$@" "${pkgs[@]}"
else
	run_stage "go test (-p $PARALLEL)" "$GO" test -vet=off -count=1 -p "$PARALLEL" "$@" "${pkgs[@]}"
fi
say "all checks passed"
