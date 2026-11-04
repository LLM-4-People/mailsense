#!/usr/bin/env bash
# Decider benchmark: two or more decision providers decide the same
# stratified sample of primary-inbox messages of the legacy export (same
# taxonomy, composer and causal states), under one hard budget cap; then
# an export for an independent gold-label judge, and its scorer.
#
#   scripts/bench-deciders.sh run <provider> [provider…]
#   scripts/bench-deciders.sh score
#   scripts/bench-deciders.sh export
#
# run: imports the export into a temporary database, decides the sample,
# writes the aggregate report ($BENCH_REPORT; no message text, address or
# id) and, in $BENCH_DIR, taxonomy.json, gold-input.jsonl (the gold subset
# as the providers saw it, without history/examples or answers) and
# answers-<provider>.jsonl. score: scores every answers file against
# $BENCH_DIR/gold-labels.jsonl ({id, answers: {question: value}}) into
# $BENCH_GOLD_REPORT.
#
# export: no provider call ($0). A stratified sample of the whole chosen
# mailbox (the received owner-scope mail of every triaged folder; strata:
# dominant script × legacy category (BENCH_LEGACY, when present) × sender
# kind (internal / external / automated, from headers and the owner's
# identity) × year; legacy categories whose share is below
# BENCH_RARE_FLOOR are topped up to it) is written as taxonomy.json,
# gold-input.jsonl and strata.json (aggregates) into a NEW directory
# $BENCH_EXPORT_ROOT/sample-<seed>-<size>-<UTC time> (mode 700, files
# 600). Ids are the benchmark's (benchID): the earlier gold labels
# ($BENCH_KEEP_LABELS) are checked to still name messages.
#
# Keys: each record's api_key_secret (the app's engine.providers.file) is
# read from .env into the dev secret variable of the test process only;
# never printed.
#
# Parameters (environment; these defaults are the one place they live):
#   BENCH_DIR                 export directory (required; holds message text: never out/)
#   BENCH_SAMPLE=200          messages sampled (stratified: replied-within × year)
#   BENCH_SEED=20261014       sample, repeat and gold subsets
#   BENCH_REPEAT=30           messages decided twice (repeat agreement)
#   BENCH_CONCURRENCY=4       workers of the main phase
#   BENCH_REPEAT_CONCURRENCY=1 workers of the repeat phase
#   BENCH_ROUND=50            messages per provider per main-phase round
#   BENCH_MAX_USD=0.15        hard cap of the whole benchmark (all providers)
#   BENCH_MAX_CALLS=2000      call cap of the whole benchmark
#   BENCH_REPLY_WITHIN=168h   weak label: the owner replied within this
#   BENCH_GOLD=60             messages exported for the gold judge
#   BENCH_REPORT=out/bench-deciders.json, BENCH_GOLD_REPORT=out/bench-gold.json
#   BENCH_JSONL (default $FIXTURE_DIR/mailbox.jsonl), ENV_FILE (default .env), GO
#   export only: BENCH_EXPORT_SIZE=600, BENCH_RARE_FLOOR=15,
#   BENCH_EXPORT_ROOT=~/.local/share/mailsense/bench (outside the repo),
#   BENCH_LEGACY=$FIXTURE_DIR/classifications.jsonl.gz (used when present),
#   BENCH_KEEP_LABELS=$BENCH_EXPORT_ROOT/gold-labels.jsonl (checked when present)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

mode="${1:-}"
[[ "$mode" == run || "$mode" == score || "$mode" == export ]] || die "usage: bench-deciders.sh run <provider> [provider…] | score | export"
shift

if [[ "$mode" == export ]]; then
	export BENCH_JSONL="${BENCH_JSONL:-$FIXTURE_DIR/mailbox.jsonl}" BENCH_SEED="${BENCH_SEED:-20261014}"
	export BENCH_EXPORT_SIZE="${BENCH_EXPORT_SIZE:-600}" BENCH_RARE_FLOOR="${BENCH_RARE_FLOOR:-15}"
	root="${BENCH_EXPORT_ROOT:-$HOME/.local/share/mailsense/bench}"
	case "$(realpath -m "$root")" in "$ROOT"|"$ROOT"/*) die "BENCH_EXPORT_ROOT must be outside the repository" ;; esac
	[[ -f "$BENCH_JSONL" ]] || die "no $BENCH_JSONL"
	fixture_owner "$BENCH_JSONL"
	legacy="${BENCH_LEGACY-$FIXTURE_DIR/classifications.jsonl.gz}"
	[[ -z "$legacy" || -f "$legacy" ]] || legacy=""
	keep="${BENCH_KEEP_LABELS-$root/gold-labels.jsonl}"
	[[ -z "$keep" || -f "$keep" ]] || keep=""
	export BENCH_LEGACY="$legacy" BENCH_KEEP_LABELS="$keep"
	export BENCH_EXPORT_DIR="$root/sample-$BENCH_SEED-$BENCH_EXPORT_SIZE-$(date -u +%Y%m%dT%H%M%SZ)"
	[[ ! -e "$BENCH_EXPORT_DIR" ]] || die "$BENCH_EXPORT_DIR exists"
	(umask 077 && mkdir -p "$root" "$BENCH_EXPORT_DIR")
	chmod 700 "$root" "$BENCH_EXPORT_DIR"
	say "gold export: $BENCH_EXPORT_SIZE messages (seed $BENCH_SEED, rare floor $BENCH_RARE_FLOOR) into $BENCH_EXPORT_DIR"
	cd "$ROOT"
	set +e
	out="$("$GO" test -tags deciderbench -run '^TestDeciderBenchExport$' -count=1 -timeout 30m -v ./internal/triage 2>&1)"
	status=$?
	set -e
	grep -E 'BENCH|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +bench_live_test.go:[0-9]+: //'
	((status == 0)) || { printf '%s
' "$out" | grep -v BENCH | tail -30 >&2; die "export failed"; }
	say "export: $BENCH_EXPORT_DIR"
	exit 0
fi

export BENCH_DIR="${BENCH_DIR:-}"
[[ -n "$BENCH_DIR" ]] || die "set BENCH_DIR (the export directory; it holds message text, so not out/)"
case "$(cd "$ROOT" && realpath -m "$BENCH_DIR")" in "$ROOT"/out*) die "BENCH_DIR must not be under out/ (aggregates only there)" ;; esac
mkdir -p "$BENCH_DIR"
cd "$ROOT"

if [[ "$mode" == score ]]; then
	[[ -f "$BENCH_DIR/gold-labels.jsonl" ]] || die "no $BENCH_DIR/gold-labels.jsonl yet"
	export BENCH_GOLD_REPORT="${BENCH_GOLD_REPORT:-$ROOT/out/bench-gold.json}"
	say "gold scoring of $BENCH_DIR"
	set +e
	out="$("$GO" test -tags deciderbench -run '^TestDeciderBenchScore$' -count=1 -v ./internal/triage 2>&1)"
	status=$?
	set -e
	grep -E 'BENCH|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +bench_live_test.go:[0-9]+: //'
	((status == 0)) || { printf '%s\n' "$out" | tail -30 >&2; die "gold scoring failed"; }
	say "report: $BENCH_GOLD_REPORT"
	exit 0
fi

(($# >= 1)) || die "name at least one provider"
command -v jq >/dev/null || die "jq is required"
env_file="${ENV_FILE:-$ROOT/.env}"
providers="$(provider_file)"
[[ -f "$providers" ]] || die "no provider file at $providers (set PROVIDERS_FILE or configure the app)"
export BENCH_PROVIDERS_FILE="$providers"
export BENCH_JSONL="${BENCH_JSONL:-$FIXTURE_DIR/mailbox.jsonl}"
[[ -f "$env_file" && -f "$BENCH_JSONL" ]] || die "need $env_file and $BENCH_JSONL"
fixture_owner "$BENCH_JSONL"
export BENCH_PROVIDERS="$*" BENCH_SAMPLE="${BENCH_SAMPLE:-200}" BENCH_SEED="${BENCH_SEED:-20261014}" BENCH_REPEAT="${BENCH_REPEAT:-30}" \
	BENCH_CONCURRENCY="${BENCH_CONCURRENCY:-4}" BENCH_REPEAT_CONCURRENCY="${BENCH_REPEAT_CONCURRENCY:-1}" BENCH_ROUND="${BENCH_ROUND:-50}" \
	BENCH_MAX_USD="${BENCH_MAX_USD:-0.15}" BENCH_MAX_CALLS="${BENCH_MAX_CALLS:-2000}" BENCH_REPLY_WITHIN="${BENCH_REPLY_WITHIN:-168h}" \
	BENCH_GOLD="${BENCH_GOLD:-60}" BENCH_REPORT="${BENCH_REPORT:-$ROOT/out/bench-deciders.json}"

say "decider benchmark: $BENCH_PROVIDERS; $BENCH_SAMPLE messages (seed $BENCH_SEED), repeat $BENCH_REPEAT, cap \$$BENCH_MAX_USD"
set +e
out="$(
	# Keys are exported in this subshell only (export is a builtin: never in a process list).
	for p in "$@"; do
		secret="$(jq -r --arg id "$p" '.[$id].api_key_secret // empty' "$providers")"
		[[ -n "$secret" ]] || { echo "error: unknown provider record $p in $providers (or it has no api_key_secret)"; exit 3; }
		key="$(kv_get "$env_file" "$secret")"
		[[ -n "$key" ]] || { echo "error: $secret not found in $env_file"; exit 3; }
		export "MAILSENSE_SECRET_$secret=$key"
	done
	unset key
	exec "$GO" test -tags deciderbench -run '^TestDeciderBench$' -count=1 -timeout 60m -v ./internal/triage 2>&1
)"
status=$?
set -e
grep -E 'BENCH|^error:|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +bench_live_test.go:[0-9]+: //'
((status == 0)) || { printf '%s\n' "$out" | grep -v -E 'BENCH|^error:' | tail -30 >&2; die "benchmark failed"; }
say "report: $BENCH_REPORT; export: $BENCH_DIR"
