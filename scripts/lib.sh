#!/usr/bin/env bash
# Shared helpers for the scripts in this directory (sourced, not executed).
set -euo pipefail

# Repository root (the directory holding go.mod).
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export ROOT

# The runtime helpers own the data directory (MAILSENSE_DATA_DIR, LOCALAPPDATA
# or XDG); this file reads it instead of spelling the path again.
source "$ROOT/scripts/runtime-lib.sh"

# GO: the Go toolchain; override with GO=/path/to/go.
if [[ -z "${GO:-}" ]]; then
	if command -v go >/dev/null 2>&1; then
		GO="$(command -v go)"
	elif [[ -x "$HOME/.local/go/bin/go" ]]; then
		GO="$HOME/.local/go/bin/go"
	else
		echo "error: Go toolchain not found (set GO=/path/to/go)" >&2
		exit 1
	fi
fi
export GO
# GOFMT: the gofmt shipped with that toolchain.
GOFMT="$(dirname "$GO")/gofmt"
[[ -x "$GOFMT" ]] || GOFMT="$(command -v gofmt)" || { echo "error: gofmt not found" >&2; exit 1; }
export GOFMT

say() { printf '==> %s\n' "$*"; }

# FIXTURE_DIR: the private dev fixture (the legacy export mailbox.jsonl,
# its mailbox.owner file, the legacy answers classifications.jsonl.gz):
# personal data, mode 700/600, never in the repository nor in out/ (out/
# holds aggregates only).
FIXTURE_DIR="${FIXTURE_DIR:-$(runtime_data_dir)/fixture}"
export FIXTURE_DIR

# go_cross <goos> <go subcommand> [args…]: a pure-Go, -trimpath
# cross-compilation step from the repo root (GOARCH, default amd64).
go_cross() {
	local goos="$1" sub="$2"
	shift 2
	(cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="${GOARCH:-amd64}" "$GO" "$sub" -trimpath "$@")
}
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# kv_get <file> <key>: the value of the first "key=value" line of a config
# or .env file, split at the FIRST '=' only (base64 tokens end in '='),
# spaces around key and value, a trailing CR and one pair of surrounding
# quotes removed. Prints nothing when absent. Never echo secrets read with it.
kv_get() {
	local file="$1" want="$2" line key value
	[[ -f "$file" ]] || return 0
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ "$line" == *=* ]] || continue
		key="${line%%=*}" value="${line#*=}"
		key="${key//[[:space:]]/}"
		[[ "$key" == "$want" ]] || continue
		value="${value%$'\r'}"
		value="${value#"${value%%[![:space:]]*}"}"
		value="${value%"${value##*[![:space:]]}"}"
		if [[ "$value" =~ ^\"(.*)\"$ || "$value" =~ ^\'(.*)\'$ ]]; then value="${BASH_REMATCH[1]}"; fi
		printf '%s' "$value"
		return 0
	done <"$file"
}

# fixture_owner <export>: exports FIXTURE_OWNER (the legacy export's owner
# addresses, comma-separated) and FIXTURE_ZONE (the owner's IANA time zone)
# unless already set, from <export>.owner (one address per line, a
# "zone=<IANA name>" line, # comments; private, next to the export, never
# in the repository). The legacy format names neither; neither is guessed.
fixture_owner() {
	local file="${1%.gz}"
	file="${file%.jsonl}.owner"
	if [[ -z "${FIXTURE_OWNER:-}" || -z "${FIXTURE_ZONE:-}" ]]; then
		[[ -f "$file" ]] || die "no FIXTURE_OWNER/FIXTURE_ZONE and no $file: declare the export's owner addresses and zone"
		local lines
		lines="$(grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$file")"
		: "${FIXTURE_OWNER:=$(grep -v '^zone=' <<<"$lines" | paste -sd, -)}"
		: "${FIXTURE_ZONE:=$(grep '^zone=' <<<"$lines" | head -1 | cut -d= -f2-)}"
	fi
	export FIXTURE_OWNER FIXTURE_ZONE
}

# live_limits: the limits of a live script's run on the Outlook machine,
# validated: live_details (LIVE_DETAILS, default 1000: detail reads in the
# whole test, 0 = every message, a full sync — ask the user first) and
# live_rate (LIVE_RATE, default 100: Outlook calls per second, 0 = the
# shipped pace). Passed to the test binary as -live.details / -live.rate
# (livetest.Limit).
live_limits() {
	live_details="${LIVE_DETAILS:-1000}" live_rate="${LIVE_RATE:-100}"
	[[ "$live_details" =~ ^[0-9]+$ ]] || die "LIVE_DETAILS must be a whole number (0: every message)"
	[[ "$live_rate" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "LIVE_RATE must be a number of calls per second (0: the shipped pace)"
}

# gold_dir: the gold sample directory of the dev-time harnesses: the newest
# ~/.local/share/mailsense/bench/sample-* (private, outside the
# repository); prints nothing when there is none.
gold_dir() {
	ls -d "$HOME"/.local/share/mailsense/bench/sample-* 2>/dev/null | sort | tail -1 || true
}

# provider_file: the provider record file the app uses (engine.providers.file,
# resolved under the data directory like the app does). PROVIDERS_FILE
# overrides it. The file is data: the scripts never copy its records.
provider_file() {
	local dir
	dir="$(runtime_data_dir)"
	local rel="providers.json"
	if [[ -f "$dir/settings.json" ]]; then
		command -v jq >/dev/null 2>&1 || die "jq is required to read engine.providers.file"
		rel="$(jq -r '.["engine.providers.file"] // "providers.json"' "$dir/settings.json")"
	fi
	if [[ -n "${PROVIDERS_FILE:-}" ]]; then
		printf '%s\n' "$PROVIDERS_FILE"
	elif [[ "$rel" == /* ]]; then
		printf '%s\n' "$rel"
	else
		printf '%s/%s\n' "$dir" "$rel"
	fi
}

# provider_secret <provider-file> <record-id>: the record's api_key_secret
# (a name, never a value); dies when the record does not exist.
provider_secret() {
	local file="$1" id="$2" secret
	command -v jq >/dev/null 2>&1 || die "jq is required to read $file"
	[[ -f "$file" ]] || die "no provider file at $file (set PROVIDERS_FILE or configure the app)"
	jq -e --arg id "$id" 'has($id)' "$file" >/dev/null 2>&1 || die "unknown provider record $id in $file"
	secret="$(jq -r --arg id "$id" '.[$id].api_key_secret // empty' "$file")"
	[[ -n "$secret" ]] || die "provider record $id has no api_key_secret"
	printf '%s\n' "$secret"
}
