#!/usr/bin/env bash
# Fast quality gate: the checks of scripts/test.sh, split into parallel shards.
#
#   scripts/test-fast.sh [extra go test flags…]
#
# Extra arguments are passed to every `go test` invocation; package operands
# are refused because the gate owns the package partition.
#
# The exhaustive gate runs one `go test ./...` under -race. This gate keeps
# the same gofmt, script and vet checks, runs three ordinary test shards
# concurrently, runs the port-sensitive sidecar suite alone, and enables
# -race only on the concurrency surface the recent work changed (qa, api).
# New packages land in shard C automatically, so nothing can be skipped.
#
# Environment:
#   SHARD_P       packages built/tested concurrently per shard (default 4)
#   RACE          auto (default: -race on qa and api when cgo works) | on | off
#   SIDECAR       on (default) | off: the sidecar suite binds loopback ports
#                 and must run alone
#   TEST_TIMEOUT  per-test-binary timeout in seconds (default 300)
#   GO            Go toolchain to use
#
# Use scripts/test.sh for the exhaustive release gate.
set -euo pipefail
# shellcheck disable=SC1091 # lib.sh is resolved from this file's own directory
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1091 # test-lib.sh is resolved from this file's own directory
source "$(dirname "${BASH_SOURCE[0]}")/test-lib.sh"
cd "$ROOT"

TEST_TIMEOUT="${TEST_TIMEOUT:-300}"
SHARD_P="${SHARD_P:-4}"
SIDECAR="${SIDECAR:-on}"
[[ "$TEST_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die "TEST_TIMEOUT must be a positive whole number of seconds"
[[ "$SHARD_P" =~ ^[1-9][0-9]*$ ]] || die "SHARD_P must be a positive whole number"
case "$SIDECAR" in
on | off) ;;
*) die "SIDECAR must be on or off" ;;
esac
for arg in "$@"; do
	case "$arg" in
	./*) die "package operands are not supported (the gate assigns every package): $arg" ;;
	esac
done

gate_gofmt
gate_scripts
"$ROOT/scripts/check-generated.sh"
gate_vet
gate_race_flags

# The three test groups. A and B hold the packages whose tests are heavy or
# exercise the recently changed paths; C is every package not assigned here,
# so a new package is always tested without editing this list.
shard_a=(
	./cmd/mailsense
	./internal/qa
	./internal/api
	./internal/ui
	./internal/priority
	./internal/brief
	./internal/world
	./internal/agenda
	./internal/commitments
	./internal/conversations
)
shard_b=(
	./internal/layer1
	./internal/triage
	./internal/triage/taxonomy
	./internal/selective
	./internal/route
	./internal/engine/...
	./internal/evidence
	./internal/index
	./internal/search
	./internal/judge
	./internal/learning
	./internal/goldset
	./internal/goldexport
	./internal/feedback
)
sidecar_pkg=./internal/sidecar

all_text="$("$GO" list ./...)"
a_text="$("$GO" list "${shard_a[@]}")"
b_text="$("$GO" list "${shard_b[@]}")"
sidecar_text="$("$GO" list "$sidecar_pkg")"
mapfile -t all <<<"$all_text"
mapfile -t pkgs_a <<<"$a_text"
mapfile -t pkgs_b <<<"$b_text"
mapfile -t pkgs_sidecar <<<"$sidecar_text"

declare -A owner=()
assign() { # shard package…
	local shard="$1" pkg
	shift
	for pkg in "$@"; do
		[[ -z "${owner[$pkg]:-}" ]] || die "package $pkg is in both ${owner[$pkg]} and $shard"
		owner[$pkg]="$shard"
	done
}
assign A "${pkgs_a[@]}"
assign B "${pkgs_b[@]}"
assign sidecar "${pkgs_sidecar[@]}"

shard_c=()
for pkg in "${all[@]}"; do
	[[ -n "${owner[$pkg]:-}" ]] || shard_c+=("$pkg")
done
((${#shard_c[@]})) || die "no packages left for shard C"

say "test shards (${#all[@]} packages: A ${#pkgs_a[@]}, B ${#pkgs_b[@]}, C ${#shard_c[@]}, sidecar ${#pkgs_sidecar[@]})"

if [[ "$SIDECAR" == on ]]; then
	say "sidecar suite (isolated: it binds loopback ports)"
	start=$SECONDS
	run_stage "sidecar suite" "$GO" test -vet=off -count=1 -timeout "${TEST_TIMEOUT}s" "$@" "$sidecar_pkg"
	printf 'ok   %-7s %3ss  1 package\n' sidecar "$((SECONDS - start))"
else
	say "sidecar suite skipped (SIDECAR=off)"
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/mailsense-test-fast.XXXXXX")"
failed=0
cleanup() {
	if ((failed)); then
		say "shard logs kept in $tmp"
	else
		rm -rf "$tmp"
	fi
}
trap cleanup EXIT
declare -A pids=() logs=() starts=() cmds=()

start_shard() { # name go-test-args…
	local name="$1"
	shift
	cmds[$name]="$(printf '%q ' "$@")"
	logs[$name]="$tmp/$name.log"
	starts[$name]=$SECONDS
	("$@") >"${logs[$name]}" 2>&1 &
	pids[$name]=$!
}

wait_shard() { # name package-count
	local name="$1" count="$2" status=0
	wait "${pids[$name]}" || status=$?
	if ((status == 0)); then
		printf 'ok   %-7s %3ss  %d packages\n' "$name" "$((SECONDS - starts[$name]))" "$count"
	else
		printf 'FAIL %-7s %3ss  %d packages\n' "$name" "$((SECONDS - starts[$name]))" "$count"
		cat "${logs[$name]}"
		printf '     rerun this shard with:\n       %s\n' "${cmds[$name]}"
		return 1
	fi
}

if ((${#RACE_FLAGS[@]})); then
	say "race shard (qa, api)"
	start_shard RACE env CGO_ENABLED=1 "$GO" test "${RACE_FLAGS[@]}" -vet=off -count=1 -timeout "${TEST_TIMEOUT}s" "$@" ./internal/qa ./internal/api
else
	say "race check skipped (RACE=off, or no cgo/C compiler)"
fi
say "shard A (${#pkgs_a[@]} packages)"
start_shard A "$GO" test -vet=off -count=1 -timeout "${TEST_TIMEOUT}s" -p "$SHARD_P" "$@" "${pkgs_a[@]}"
say "shard B (${#pkgs_b[@]} packages)"
start_shard B "$GO" test -vet=off -count=1 -timeout "${TEST_TIMEOUT}s" -p "$SHARD_P" "$@" "${pkgs_b[@]}"
say "shard C (${#shard_c[@]} packages)"
start_shard C "$GO" test -vet=off -count=1 -timeout "${TEST_TIMEOUT}s" -p "$SHARD_P" "$@" "${shard_c[@]}"

wait_shard A "${#pkgs_a[@]}" || failed=1
wait_shard B "${#pkgs_b[@]}" || failed=1
wait_shard C "${#shard_c[@]}" || failed=1
if ((${#RACE_FLAGS[@]})); then
	wait_shard RACE 2 || failed=1
fi
((failed == 0)) || die "test shards failed; each FAIL above names the shard and the command that reruns it"

say "all fast checks passed (exhaustive gate: scripts/test.sh)"
