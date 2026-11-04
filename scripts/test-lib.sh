#!/usr/bin/env bash
# Shared checks of the test gates (sourced, not executed).
#
# One owner for the gofmt and vet matrix so scripts/test.sh (exhaustive) and
# scripts/test-fast.sh (parallel shards) can never drift apart.
set -euo pipefail
# shellcheck disable=SC1091 # lib.sh is resolved from this file's own directory
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# gate_failed <stage> <rerun command>: report the stage that failed and the
# command that reruns only that stage. Every check reports through here, so a
# failure never ends as a bare exit status.
gate_failed() {
	printf 'error: %s failed\n' "$1" >&2
	printf '       rerun just this stage with:\n         %s\n' "$2" >&2
	exit 1
}

# run_stage <stage> <command…>: run one stage and, on failure, name it and
# print the exact command that reruns it.
run_stage() {
	local stage="$1" rendered
	shift
	if ! "$@"; then
		rendered="$(printf '%q ' "$@")"
		gate_failed "$stage" "${rendered% }"
	fi
}

# gate_gofmt: every Go source under cmd/, internal/ and scripts/ is formatted.
gate_gofmt() {
	say "gofmt"
	local unformatted
	unformatted="$("$GOFMT" -l cmd internal scripts)"
	if [[ -n "$unformatted" ]]; then
		printf 'not gofmt-formatted:\n%s\n' "$unformatted" >&2
		gate_failed "gofmt" "$GOFMT -l cmd internal scripts"
	fi
}

# gate_scripts: a syntax check of every script and the one data-directory
# owner: runtime_data_dir.
gate_scripts() {
	say "script syntax check"
	local f
	for f in "$ROOT"/scripts/*.sh; do
		bash -n "$f" || gate_failed "script syntax check ($f)" "bash -n $f"
	done
	local dup
	# shellcheck disable=SC2016 # the pattern matches the literal text, not $HOME
	dup="$(grep -l 'XDG_DATA_HOME:-$HOME/.local/share}/mailsense' "$ROOT"/scripts/*.sh 2>/dev/null |
		grep -v '/test-lib.sh$' || true)"
	if [[ -n "$dup" ]]; then
		printf 'the data directory is spelled outside runtime_data_dir:\n%s\n' "$dup" >&2
		gate_failed "script data-directory check" "grep -n 'XDG_DATA_HOME' scripts/*.sh"
	fi
}

# gate_vet: go vet on the host and every tagged configuration the shipped
# code uses. One full-module pass covers every package, and each tag only owns
# the packages that declare it, so a tag pass rebuilds that bounded set instead
# of the whole module. The full windows pass belongs to the windows job, which
# vets the tree natively; a GOOS build here would duplicate it and, on a
# 4-core runner, three full module builds at once were the slowest stage of the
# gate by a wide margin. The host pass runs alone for the same reason; the
# small tag passes then run together.
gate_vet() {
	say "go vet (host pass, then the tagged packages in parallel)"
	local tmp
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/mailsense-vet.XXXXXX")"
	trap 'rm -rf "$tmp"' RETURN
	if ! "$GO" vet ./... >"$tmp/host.log" 2>&1; then
		cat "$tmp/host.log" >&2
		gate_failed "go vet (host)" "$GO vet ./..."
	fi
	local -a labels=() pids=() cmds=()
	local i=0
	vet_bg() { # label command…
		local label="$1"
		shift
		labels+=("$label")
		cmds+=("$(printf '%q ' "$@")")
		("$@") >"$tmp/$i.log" 2>&1 &
		pids+=($!)
		i=$((i + 1))
	}
	vet_bg "windows, outlooklive tag" env GOOS=windows "$GO" vet -tags outlooklive ./internal/source/outlook
	vet_bg "windows, evidencelive tag" env GOOS=windows "$GO" vet -tags evidencelive ./internal/evidence
	vet_bg "windows, embedlive tag" env GOOS=windows "$GO" vet -tags embedlive ./internal/search
	# A fixture-only file lives in exactly these packages; the tag pass follows
	# the files rather than the whole module.
	vet_bg "fixture tag" "$GO" vet -tags fixture \
		./internal/conversations ./internal/evidence ./internal/layer1 \
		./internal/perfprepare ./internal/priority ./internal/search \
		./internal/store ./internal/syncer ./internal/threads
	vet_bg "triagelive tag" "$GO" vet -tags triagelive ./internal/triage
	vet_bg "deciderbench tag" "$GO" vet -tags deciderbench ./internal/triage
	vet_bg "judgegold tag" "$GO" vet -tags judgegold ./internal/judge
	vet_bg "embedlocal tag" "$GO" vet -tags embedlocal ./internal/search ./internal/layer1
	local status=0 rerun=""
	for i in "${!pids[@]}"; do
		if ! wait "${pids[$i]}"; then
			printf 'FAIL go vet (%s)\n' "${labels[$i]}"
			cat "$tmp/$i.log"
			[[ -n "$rerun" ]] || rerun="${cmds[$i]}"
			status=1
		fi
	done
	((status == 0)) || gate_failed "go vet (tagged)" "${rerun% }"
}

# gate_race_flags: fills RACE_FLAGS with (-race) when RACE allows it.
# RACE=auto (default) enables it when cgo and a C compiler work, on enables
# it unconditionally, off disables it.
# shellcheck disable=SC2034 # RACE_FLAGS is read by the gate that sources this file
gate_race_flags() {
	RACE_FLAGS=()
	case "${RACE:-auto}" in
	on) RACE_FLAGS=(-race) ;;
	off) ;;
	auto)
		if command -v "${CC:-gcc}" >/dev/null 2>&1 && [[ "$(CGO_ENABLED=1 "$GO" env CGO_ENABLED)" == "1" ]]; then
			RACE_FLAGS=(-race)
		fi
		;;
	*) die "RACE must be auto, on or off" ;;
	esac
}
