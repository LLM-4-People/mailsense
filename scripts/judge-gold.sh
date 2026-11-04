#!/usr/bin/env bash
# Live gold validation of the judge (dev only; no production caller).
#
#   scripts/judge-gold.sh <record-id> [messages]     default: 60
#
# Reads the newest sealed gold sample's train and dev labels through
# internal/goldset (never sealed/), draws a deterministic sample, calls the
# judge record with the key its api_key_secret names in .env (only that key
# enters the test process) and prints per-question agreement, agreement
# overall and calls/tokens/USD.
# The run refuses before spending anything above JUDGE_GOLD_MAX_USD
# (default $0.50). Message content, addresses and label values are never
# printed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$ROOT"

record="${1:-}"
[[ -n "$record" ]] || die "usage: judge-gold.sh <record-id> [messages]"
messages="${2:-60}"
[[ "$messages" =~ ^[1-9][0-9]*$ ]] || die "messages must be a positive integer, got $messages"
cap="${JUDGE_GOLD_MAX_USD:-0.50}"
[[ "$cap" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "JUDGE_GOLD_MAX_USD must be a positive number, got $cap"
timeout="${JUDGE_GOLD_TIMEOUT:-30m}"

providers="$(provider_file)"
secret="$(provider_secret "$providers" "$record")"
key="$(kv_get "$ROOT/.env" "$secret")"
[[ -n "$key" ]] || die "$secret is not set in .env"

export MAILSENSE_ENGINE_PROVIDERS_FILE="$providers"
export JUDGE_GOLD_PROVIDER="$record"
export JUDGE_GOLD_MESSAGES="$messages"
export "MAILSENSE_SECRET_$secret=$key"

say "judge gold: record $record, $messages messages, hard cap \$$cap"
"$GO" test -tags judgegold -count=1 -timeout "$timeout" -run '^TestJudgeGold$' -v ./internal/judge
