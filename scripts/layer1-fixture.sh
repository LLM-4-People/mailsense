#!/usr/bin/env bash
# Layer-1 dev harness over the legacy fixture (docs/LAYER1.md).
#
#   scripts/layer1-fixture.sh embed [phases]      real EmbeddingGemma vectors into the private
#                                                   vector cache (≈5 messages/s on the dev CPU: run
#                                                   it in the background; cached vectors are reused);
#                                                   phases: sample, sample:<purpose>, full (comma list)
#   scripts/layer1-fixture.sh run [experiment]      train and evaluate layer 1 on the gold DEV split
#                                                   (the test split is sealed and never read)
#   scripts/layer1-fixture.sh compare <a> <b>       the paired bootstrap of two stored runs (b − a)
#   scripts/layer1-fixture.sh summary <run>         the per-question dev table of out/layer1-<run>.json
#                                                   (accuracy, the honest dev certification at 0.99, the dev curve)
#   scripts/layer1-fixture.sh stop [run|embed]      ask a running harness to stop (a stop file it
#                                                   polls: a graceful stop, never a kill; default run)
#
# Reports: out/layer1-*.json (aggregates only), every dev evaluation
# appended to out/layer1-dev-ledger.jsonl; per-item results and error
# lists (gold ids) only under ~/.cache/mailsense-layer1/runs (private).
# The fixture (export, owner file, legacy answers) is personal data: it
# lives in $FIXTURE_DIR (lib.sh: ~/.local/share/mailsense/fixture, 700/600).
#
# Environment:
#   FIXTURE_JSONL           the export (default $FIXTURE_DIR/mailbox.jsonl, lib.sh); FIXTURE_OWNER/FIXTURE_ZONE as for fixture-import.sh
#   LAYER1_GOLD_DIR         the sealed gold sample (default: the newest ~/.local/share/mailsense/bench/sample-*)
#   LAYER1_LEGACY           the legacy Ondelle answers (default $FIXTURE_DIR/classifications.jsonl.gz)
#   LAYER1_CACHE_DIR        the private vector cache (default ~/.cache/mailsense-layer1/vectors, mode 700)
#   LAYER1_SAMPLE           the experiment sample's draw besides the gold (default 8000)
#   EMBEDLOCAL_RUNTIME_DIR  the pinned runtime (default ~/.cache/mailsense-runtime)
#   LAYER1_REPORT_DIR       where reports go (default out/)
#   LAYER1_POPULATION       sample (the experiment sample) or all (every owner message with a vector);
#                           default: the experiment's own (E1: sample)
#   LAYER1_RUN              the run's name (default: the experiment's; the report and the per-item results)
#   LAYER1_BASELINE         a run name (an earlier experiment): its paired comparison with this one
#   LAYER1_TIMEOUT          the test timeout (default 12h; the harness stops itself: never killed)
#   GO
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

mode="${1:-}"
export FIXTURE_JSONL="$(realpath "${FIXTURE_JSONL:-$FIXTURE_DIR/mailbox.jsonl}")"
[[ -f "$FIXTURE_JSONL" ]] || die "no such file: $FIXTURE_JSONL"
fixture_owner "$FIXTURE_JSONL"
export LAYER1_GOLD_DIR="${LAYER1_GOLD_DIR:-$(gold_dir)}"
[[ -n "$LAYER1_GOLD_DIR" && -d "$LAYER1_GOLD_DIR/sealed" ]] || die "no sealed gold sample at '$LAYER1_GOLD_DIR' (scripts/gold-seal.sh)"
export LAYER1_LEGACY="${LAYER1_LEGACY:-$FIXTURE_DIR/classifications.jsonl.gz}"
[[ -f "$LAYER1_LEGACY" ]] || die "no legacy answers at $LAYER1_LEGACY"
cache_root="${XDG_CACHE_HOME:-$HOME/.cache}"
export LAYER1_CACHE_DIR="${LAYER1_CACHE_DIR:-$cache_root/mailsense-layer1/vectors}"
mkdir -p "$LAYER1_CACHE_DIR" && chmod 700 "$(dirname "$LAYER1_CACHE_DIR")" "$LAYER1_CACHE_DIR"
export LAYER1_SAMPLE="${LAYER1_SAMPLE:-8000}"
export EMBEDLOCAL_RUNTIME_DIR="${EMBEDLOCAL_RUNTIME_DIR:-$cache_root/mailsense-runtime}"
# The harness builds a temporary data directory, so it must be told which
# provider catalog to read; the app's own file is the default (embed-local.sh
# has the same two variables).
export EMBEDLOCAL_PROVIDERS="${EMBEDLOCAL_PROVIDERS:-$(provider_file)}"
export EMBEDLOCAL_PROVIDER="${EMBEDLOCAL_PROVIDER:-local-embedder}"
export LAYER1_REPORT_DIR="${LAYER1_REPORT_DIR:-$ROOT/out}"
mkdir -p "$LAYER1_REPORT_DIR"
cd "$ROOT"

# run_test <tags> <test regexp>: the harness test, its LAYER1/FIXTURE lines shown.
run_test() {
	local status
	set +e
	"$GO" test -tags "$1" -run "$2" -count=1 -timeout "${LAYER1_TIMEOUT:-12h}" -v ./internal/layer1 2>&1 |
		grep --line-buffered -E 'LAYER1|FIXTURE|^(--- )?(PASS|FAIL|SKIP|ok)|panic|FAIL|_test.go:' | sed -u -E 's/^ +[a-z_0-9]+_test.go:[0-9]+: //'
	status=${PIPESTATUS[0]}
	set -e
	((status == 0)) || die "layer-1 harness failed"
}

case "$mode" in
compare)
	export LAYER1_COMPARE_A="${2:?run a}" LAYER1_COMPARE_B="${3:?run b}"
	"$GO" test -run '^TestLayer1Compare$' -count=1 -v ./internal/layer1 2>&1 | grep -E 'LAYER1|FAIL|ok'
	;;
summary)
	f="$LAYER1_REPORT_DIR/layer1-${2:?run name}.json"
	[[ -f "$f" ]] || die "no report $f"
	jq -r '"run \(.run // .experiment) (\(.population // "?")): mean dev accuracy layer1 \(.mean_accuracy.layer1*1000|round/1000) ondelle \(.mean_accuracy.ondelle*1000|round/1000); train \(.wall_ms.train/1000|round) s; model \(.model_bytes) bytes",
		(.compare // empty | "vs \(.baseline): \(.mean_diff*1000|round/1000) [\(.ci95[0]*1000|round/1000), \(.ci95[1]*1000|round/1000)]"),
		"question           n    layer1 ondelle   ceil  cert@.99 l1/ondelle/stack  curve@.99 l1/ondelle/stack  meets  lambda   eval",
		(.questions[] | (.selective.certified) as $c |
			"\(.question|.+"                 "|.[0:17]) \(.n|tostring|.+"   "|.[0:4]) \(.layer1.accuracy*1000|round/1000|tostring|.+"      "|.[0:6]) \(.ondelle.accuracy*1000|round/1000|tostring|.+"      "|.[0:6]) \(.ceiling|tostring|.+"     "|.[0:5]) \([$c.layer1, $c.ondelle, $c.stack] | map(map(select(.target == 0.99))[0].coverage*100|round/100|tostring) | join("/")|.+"                      "|.[0:22]) \([.selective.layer1, .selective.ondelle, .selective.stack] | map(map(select(.target == 0.99))[0].coverage*100|round/100|tostring) | join("/")|.+"                      "|.[0:22]) \(.meets_requirement|tostring|.+"      "|.[0:6]) \(.train.lambda|tostring|.+"        "|.[0:8]) \(.train.eval)")' "$f"
	;;
stop)
	which="${2:-run}"
	[[ "$which" == run || "$which" == embed ]] || die "stop run|embed"
	touch "$(dirname "$LAYER1_CACHE_DIR")/stop-$which"
	say "stop requested (the running harness ends within seconds; the file is removed by the next run)"
	;;
embed)
	export LAYER1_EMBED_PHASES="${2:-sample,full}"
	say "embedding ($LAYER1_EMBED_PHASES) into $LAYER1_CACHE_DIR"
	run_test embedlocal '^TestLayer1Embed$'
	;;
run)
	export LAYER1_EXPERIMENT="${2:-default}"
	say "layer 1 on the fixture, experiment $LAYER1_EXPERIMENT (dev split only)"
	run_test fixture '^TestLayer1Fixture$'
	;;
*) die "usage: scripts/layer1-fixture.sh embed [phases] | run [experiment] | compare <a> <b> | summary <run> | stop [run|embed]" ;;
esac
