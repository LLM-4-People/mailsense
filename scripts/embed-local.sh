#!/usr/bin/env bash
# Local benchmark with the real embedding model (Linux dev box): the pinned
# Linux llama-server and model through the sidecar (downloaded on first
# use into EMBEDLOCAL_RUNTIME_DIR, SHA-256 checked), a sample of the
# fixture indexed for throughput, int8 vs float32 recall, hybrid vs
# full-text known-item retrieval and search latency (aggregates only).
#
#   scripts/embed-local.sh [messages]     (default 600; historical quiet-box ≈5.3–5.6 mails/s; current host ≈0.94 mails/s at the Round 42 baseline)
#
# Benchmark-only knobs (never read by the shipped app): EMBEDLOCAL_BATCH
# overrides embed.batch_size for this run; EMBEDLOCAL_PROVIDERS is the
# private provider catalog that records the runtime and model, and
# EMBEDLOCAL_PROVIDER is its record id.
#
# Environment: FIXTURE_JSONL (default $FIXTURE_DIR/mailbox.jsonl), EMBEDLOCAL_RUNTIME_DIR
# (default ~/.cache/mailsense-runtime: kept between runs), EMBEDLOCAL_BATCH,
# EMBEDLOCAL_PROVIDERS (default the app data dir's providers.json),
# EMBEDLOCAL_PROVIDER (default local-embedder), GO.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
export FIXTURE_JSONL="${FIXTURE_JSONL:-$FIXTURE_DIR/mailbox.jsonl}"
export EMBEDLOCAL_MESSAGES="${1:-600}"
export EMBEDLOCAL_RUNTIME_DIR="${EMBEDLOCAL_RUNTIME_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/mailsense-runtime}"
export EMBEDLOCAL_PROVIDERS="${EMBEDLOCAL_PROVIDERS:-$(provider_file)}"
export EMBEDLOCAL_PROVIDER="${EMBEDLOCAL_PROVIDER:-local-embedder}"
[[ -f "$FIXTURE_JSONL" ]] || die "no such file: $FIXTURE_JSONL"
[[ -f "$EMBEDLOCAL_PROVIDERS" ]] || die "no such provider catalog: $EMBEDLOCAL_PROVIDERS (set EMBEDLOCAL_PROVIDERS)"
fixture_owner "$FIXTURE_JSONL"
mkdir -p "$EMBEDLOCAL_RUNTIME_DIR"
say "benchmarking $EMBEDLOCAL_MESSAGES messages (runtime in $EMBEDLOCAL_RUNTIME_DIR)"
cd "$ROOT"
set +e
out="$("$GO" test -tags embedlocal -run '^TestLocalRealModel$' -count=1 -timeout 4h -v ./internal/search 2>&1)"
status=$?
set -e
grep -E 'LOCAL|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +local_test.go:[0-9]+: //'
((status == 0)) || { printf '%s\n' "$out" | tail -40 >&2; die "local benchmark failed"; }
