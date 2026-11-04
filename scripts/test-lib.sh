#!/usr/bin/env bash
# Shared checks of the test gates (sourced, not executed).
#
# One owner for the gofmt and vet matrix so scripts/test.sh (exhaustive) and
# scripts/test-fast.sh (parallel shards) can never drift apart.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# gate_gofmt: every Go source under cmd/, internal/ and scripts/ is formatted.
gate_gofmt() {
	say "gofmt"
	local unformatted
	unformatted="$("$GOFMT" -l cmd internal scripts)"
	[[ -z "$unformatted" ]] || die "not gofmt-formatted:"$'\n'"$unformatted"
}

# gate_scripts: a syntax check of every script and the one data-directory
# owner: runtime_data_dir.
gate_scripts() {
	say "script syntax check"
	local f
	for f in "$ROOT"/scripts/*.sh; do bash -n "$f"; done
	local dup
	dup="$(grep -l 'XDG_DATA_HOME:-$HOME/.local/share}/mailsense' "$ROOT"/scripts/*.sh 2>/dev/null |
		grep -v '/test-lib.sh$' || true)"
	[[ -z "$dup" ]] || die "the data directory is spelled outside runtime_data_dir:"$'\n'"$dup"
}

# gate_vet: go vet on the host and every cross/tagged configuration the
# shipped code uses. The configurations are independent builds, so they run
# concurrently; each output is replayed only when it fails.
gate_vet() {
	say "go vet (host, windows and tagged configurations; parallel)"
	local tmp
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/mailsense-vet.XXXXXX")"
	trap 'rm -rf "$tmp"' RETURN
	local -a labels=() pids=()
	local i=0
	vet_bg() { # label command…
		local label="$1"
		shift
		labels+=("$label")
		("$@") >"$tmp/$i.log" 2>&1 &
		pids+=($!)
		i=$((i + 1))
	}
	vet_bg "host" "$GO" vet ./...
	vet_bg "windows" env GOOS=windows "$GO" vet ./...
	vet_bg "windows, outlooklive tag" env GOOS=windows "$GO" vet -tags outlooklive ./internal/source/outlook
	vet_bg "windows, evidencelive tag" env GOOS=windows "$GO" vet -tags evidencelive ./internal/evidence
	vet_bg "fixture tag" "$GO" vet -tags fixture ./...
	vet_bg "triagelive tag" "$GO" vet -tags triagelive ./internal/triage
	vet_bg "deciderbench tag" "$GO" vet -tags deciderbench ./internal/triage
	vet_bg "judgegold tag" "$GO" vet -tags judgegold ./internal/judge
	vet_bg "windows, embedlive tag" env GOOS=windows "$GO" vet -tags embedlive ./internal/search
	vet_bg "embedlocal tag" "$GO" vet -tags embedlocal ./internal/search ./internal/layer1
	local status=0
	for i in "${!pids[@]}"; do
		if ! wait "${pids[$i]}"; then
			printf 'FAIL go vet (%s)\n' "${labels[$i]}"
			cat "$tmp/$i.log"
			status=1
		fi
	done
	((status == 0)) || die "go vet failed"
}

# gate_race_flags: fills RACE_FLAGS with (-race) when RACE allows it.
# RACE=auto (default) enables it when cgo and a C compiler work, on enables
# it unconditionally, off disables it.
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
