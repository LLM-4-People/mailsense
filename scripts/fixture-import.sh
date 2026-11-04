#!/usr/bin/env bash
# Import a legacy JSONL export into a fresh database and report counts,
# timings, DB size and scope verdicts, then index it with the lexical fake
# embedder and check search precision, known-item retrieval, filters,
# coverage and the degraded path with latencies (Go tests behind the
# `fixture` tag: syncer TestFixtureImport, search TestFixtureSearch).
#
#   scripts/fixture-import.sh [path/to/mailbox.jsonl[.gz]]   (default: $FIXTURE_DIR/mailbox.jsonl, lib.sh)
#
# Environment:
#   FIXTURE_EXPECT     comma list of key=value checks (messages, inbox, sent,
#                      owner, shared, unattributed, skipped). Defaults to the
#                      legacy ground truth when the default fixture is used.
#   FIXTURE_FTS_PAIRS  accented/plain word pairs that must match equally
#                      (default export only: réunion/reunion,ébauche/ebauche,
#                      déjà/deja; for another export the check is skipped
#                      unless set)
#   FIXTURE_TERMS      words whose full-text hits must all contain them
#                      (default export only: réunion,facture,contrat,
#                      soumission — it is French; required for another export:
#                      words of its own language)
#   FIXTURE_DATA_DIR   keep the database in this directory (default: temp, removed)
#   FIXTURE_OWNER      the export's owner addresses, comma-separated (default:
#                      read from <export>.owner next to the export — one
#                      address per line, # comments; private, never in the
#                      repository). The legacy format names no owner, and the
#                      owner is never guessed: without it the import fails.
#   GO                 Go toolchain to use
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

default="$FIXTURE_DIR/mailbox.jsonl"
input="$(realpath "${1:-$default}")"
[[ -f "$input" ]] || die "no such file: $input"
if [[ "$input" == "$default" ]]; then
	[[ -n "${FIXTURE_EXPECT+x}" ]] || FIXTURE_EXPECT="messages=33842,inbox=19999,sent=13843,owner=33581,shared=152,unattributed=109,skipped=0"
	: "${FIXTURE_FTS_PAIRS=réunion/reunion,ébauche/ebauche,déjà/deja}"
	: "${FIXTURE_TERMS:=réunion,facture,contrat,soumission}"
fi
[[ -n "${FIXTURE_TERMS:-}" ]] || die "FIXTURE_TERMS is required for $input: words of its own language (the defaults are the French words of the default export)"
[[ -n "${FIXTURE_FTS_PAIRS:-}" ]] || say "FIXTURE_FTS_PAIRS not set: the accented/plain full-text check is skipped"
fixture_owner "$input"
export FIXTURE_JSONL="$input" FIXTURE_EXPECT="${FIXTURE_EXPECT:-}"
export FIXTURE_FTS_PAIRS="${FIXTURE_FTS_PAIRS:-}" FIXTURE_TERMS
export FIXTURE_DATA_DIR="${FIXTURE_DATA_DIR:-}"
[[ -z "$FIXTURE_DATA_DIR" ]] || mkdir -p "$FIXTURE_DATA_DIR"

say "importing $input"
cd "$ROOT"
set +e
out="$("$GO" test -tags fixture -run '^TestFixture(Import|Search|Medians)$' -count=1 -timeout 30m -v ./internal/syncer ./internal/search ./internal/threads 2>&1)"
status=$?
set -e
grep -E 'FIXTURE|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +fixture_test.go:[0-9]+: //'
((status == 0)) || { printf '%s\n' "$out" | tail -40 >&2; die "fixture import failed"; }
