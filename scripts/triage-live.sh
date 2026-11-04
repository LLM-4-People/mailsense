#!/usr/bin/env bash
# Small live check of triage against the real decision provider record:
# one probe call (a 402 = no credits stops here), then the newest N owner
# messages of the legacy export are triaged in a temporary database, and
# R of them are decided again to measure repeat-agreement.
#
#   scripts/triage-live.sh <record-id> [messages] [repeat]     (default: 30 10)
#
# The record's api_key_secret names the key read from .env; only that key
# enters the test process, and it is never printed. Never use this for a
# full archive: the run budget below (the one place it is set) refuses it.
#
# Environment:
#   ENV_FILE        key file (default: <repo>/.env)
#   PROVIDERS_FILE  provider record file (default: the app's engine.providers.file)
#   TRIAGE_JSONL    legacy export (default: $FIXTURE_DIR/mailbox.jsonl)
#   GO              Go toolchain to use
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

record="${1:-}"
[[ -n "$record" ]] || die "usage: triage-live.sh <record-id> [messages] [repeat]"
messages="${2:-30}"
repeat="${3:-10}"
[[ "$messages" =~ ^[0-9]+$ && "$repeat" =~ ^[0-9]+$ ]] || die "messages and repeat must be numbers"
((messages <= 30 && repeat <= messages)) || die "this is a small check: at most 30 messages, repeat ≤ messages"
env_file="${ENV_FILE:-$ROOT/.env}"
jsonl="${TRIAGE_JSONL:-$FIXTURE_DIR/mailbox.jsonl}"
[[ -f "$env_file" && -f "$jsonl" ]] || die "need $env_file and $jsonl"
fixture_owner "$jsonl"

providers="$(provider_file)"
secret="$(provider_secret "$providers" "$record")"
key="$(kv_get "$env_file" "$secret")"
[[ -n "$key" ]] || die "$secret not found in $env_file"

say "live triage check: record $record, probe, then $messages messages, $repeat repeated"
cd "$ROOT"
set +e
budget_calls=150 budget_usd=0.25 # the live check's whole budget
out="$(env "MAILSENSE_SECRET_$secret=$key" TRIAGE_LIVE_PROVIDER="$record" TRIAGE_LIVE_PROVIDERS_FILE="$providers" \
	TRIAGE_LIVE_JSONL="$jsonl" TRIAGE_LIVE_MESSAGES="$messages" TRIAGE_LIVE_REPEAT="$repeat" \
	TRIAGE_LIVE_MAX_CALLS="$budget_calls" TRIAGE_LIVE_MAX_USD="$budget_usd" \
	"$GO" test -tags triagelive -run '^TestTriageLive$' -count=1 -timeout 20m -v ./internal/triage 2>&1)"
status=$?
set -e
unset key
grep -E 'LIVE|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +live_test.go:[0-9]+: //'
((status == 0)) || { printf '%s\n' "$out" | tail -30 >&2; die "live check failed"; }
