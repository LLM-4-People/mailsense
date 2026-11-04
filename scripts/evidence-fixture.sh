#!/usr/bin/env bash
# Run the evidence engine over the whole legacy fixture (imported into a
# temporary database and indexed with the lexical fake embedder) and write
# aggregate reports: out/evidence-fixture.json (coverage and distribution
# per signal, runtime of a first and an unchanged second run, database
# growth, the stage) and, with a gold sample, out/evidence-rules.json (the
# rule table: precision, coverage and Wilson bounds on its TRAIN and DEV
# labels only — the test split is never read). No text, no address.
#
#   scripts/evidence-fixture.sh [path/to/mailbox.jsonl[.gz]]   (default: $FIXTURE_DIR/mailbox.jsonl, lib.sh)
#
# Environment:
#   EVIDENCE_GOLD_DIR    gold sample directory (splits.json, gold-labels.jsonl);
#                        default: the newest ~/.local/share/mailsense/bench/sample-*;
#                        "none" skips the rule table
#   EVIDENCE_REPORT_DIR  where the reports go (default: out/)
#   FIXTURE_OWNER, FIXTURE_ZONE, GO   as for fixture-import.sh
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

input="$(realpath "${1:-$FIXTURE_DIR/mailbox.jsonl}")"
[[ -f "$input" ]] || die "no such file: $input"
fixture_owner "$input"
if [[ -z "${EVIDENCE_GOLD_DIR:-}" ]]; then
	EVIDENCE_GOLD_DIR="$(gold_dir)"
fi
[[ "${EVIDENCE_GOLD_DIR:-}" == none ]] && EVIDENCE_GOLD_DIR=""
if [[ -n "$EVIDENCE_GOLD_DIR" ]]; then
	[[ -f "$EVIDENCE_GOLD_DIR/splits.json" && -f "$EVIDENCE_GOLD_DIR/gold-labels.jsonl" ]] || die "$EVIDENCE_GOLD_DIR lacks splits.json or gold-labels.jsonl"
fi
export FIXTURE_JSONL="$input" EVIDENCE_GOLD_DIR EVIDENCE_REPORT_DIR="${EVIDENCE_REPORT_DIR:-$ROOT/out}"
mkdir -p "$EVIDENCE_REPORT_DIR"
say "evidence over $input"
cd "$ROOT"
set +e
out="$("$GO" test -tags fixture -run '^TestFixtureEvidence$' -count=1 -timeout 30m -v ./internal/evidence 2>&1)"
status=$?
set -e
grep -E 'FIXTURE|^(--- )?(PASS|FAIL|ok)' <<<"$out" | sed -E 's/^ +fixture_test.go:[0-9]+: //'
((status == 0)) || { printf '%s\n' "$out" | tail -40 >&2; die "evidence fixture run failed"; }
