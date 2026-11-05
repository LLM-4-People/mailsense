#!/usr/bin/env bash
# Measure the three user-facing performance paths on the private fixture.
# Aggregates only: no subject, address, body or token is printed or kept.
#
#   scripts/bench-perf.sh [--store DIR] [--out DIR] [--keep]
#
# It imports the fixture into a temporary data directory and writes
# synthetic route answers; --store / PERF_STORE_DIR reuses an already
# prepared bench store and never modifies it. It builds the app, then
# measures:
#
#   1. start-up, /api/status, /api/learning and the label-stage count
#   2. the first /api/triage page and /api/triage/summary
#   3. FTS-first search with the embedder down
#
# Thresholds (33,842-message fixture, GOMAXPROCS=4, warm page cache):
#   ready_ms            3,000   /api/status_ms      1,300
#   /api/learning_ms    1,300   labels_stage_ms       100
#   triage_page_ms        100   triage_page_bytes   800,000
#   summary_ms          2,000   search_degraded_ms    250
#   search_common_ms      250   peak_rss_mb           700
#
# A metric more than 20% over its threshold fails the run (nonzero exit);
# so does any non-200 response. The thresholds are the current contract
# from /tmp/mailsense-perf-plan.md, not a recorded run: absolute times are
# noisy on a shared host, the shape (page vs whole table, budget vs 3 s)
# is the gate.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

store="${PERF_STORE_DIR:-}"
out="${PERF_OUT:-$ROOT/out/perf}"
keep=0
while (($#)); do
	case "$1" in
	--store) [[ $# -ge 2 ]] || die "--store needs a directory"; store="$2"; shift 2 ;;
	--out) [[ $# -ge 2 ]] || die "--out needs a directory"; out="$2"; shift 2 ;;
	--keep) keep=1; shift ;;
	-h | --help) sed -n '2,35p' "$0"; exit 0 ;;
	*) die "unknown argument '$1' (try --help)" ;;
	esac
done

input="${FIXTURE_JSONL:-$FIXTURE_DIR/mailbox.jsonl}"
[[ -f "$input" ]] || die "no fixture at $input (set FIXTURE_JSONL or FIXTURE_DIR)"
fixture_owner "$input"
export FIXTURE_JSONL="$input"
mkdir -p "$out"

work=""
prepare=0
if [[ -z "$store" ]]; then
	work="$(mktemp -d "${TMPDIR:-/tmp}/mailsense-bench.XXXXXX")"
	store="$work/data"
	prepare=1
fi
abs_store="$(realpath -m "$store")"
mkdir -p "$abs_store"
if ((prepare == 0)); then
	[[ -f "$abs_store/mailsense.db" ]] || die "--store has no mailsense.db: pass an already prepared bench store"
fi

pid=""
tmp_files=()
cleanup() {
	if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
		kill -TERM "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	for f in "${tmp_files[@]}"; do rm -f "$f"; done
	if ((keep == 0)) && [[ -n "$work" ]]; then rm -rf "$work"; fi
}
trap cleanup EXIT

if ((prepare == 1)); then
	say "preparing the fixture store (aggregates only)"
	set +e
	prepare_out="$(PERF_STORE_DIR="$abs_store" "$GO" test -tags fixture -run '^TestFixturePerfPrepare$' -count=1 -timeout 30m -v ./internal/perfprepare 2>&1)"
	prepare_status=$?
	set -e
	grep -E 'PERF prepared|^(ok|FAIL)' <<<"$prepare_out" || true
	((prepare_status == 0)) || { printf '%s\n' "$prepare_out" | tail -20 >&2; die "the fixture store could not be prepared"; }
else
	say "using the prepared store (not modified)"
fi

bin="${work:-$out}/mailsense"
say "building $bin"
(cd "$ROOT" && GOMAXPROCS=4 CGO_ENABLED=0 "$GO" build -trimpath -o "$bin" ./cmd/mailsense)

rm -f "$abs_store/runtime/api.json"
start_ns="$(date +%s%N)"
MAILSENSE_DATA_DIR="$abs_store" GOMAXPROCS=4 DISPLAY= "$bin" >"$out/app.log" 2>&1 &
pid=$!
desc="$abs_store/runtime/api.json"
ready_ms=""
cfg=""
for _ in $(seq 1 1200); do
	if ! kill -0 "$pid" 2>/dev/null; then die "the app exited before it was ready (see $out/app.log)"; fi
	if [[ -f "$desc" ]]; then
		read -r base token < <(python3 - "$desc" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print(d["url"].rstrip("/"), d["token"])
PY
)
		# The bearer header travels in a 0600 curl config, never an argv:
		# the token is a secret and a process list must not carry it.
		if [[ -z "$cfg" ]]; then
			cfg="$work/curl.cfg"
			if [[ -z "$work" ]]; then cfg="$(mktemp)"; tmp_files+=("$cfg"); fi
			printf 'header = "Authorization: Bearer %s"\n' "$token" >"$cfg"
			chmod 600 "$cfg"
		fi
		if curl -sS --config "$cfg" --max-time 2 -o /dev/null "$base/api/status" 2>/dev/null; then
			ready_ms=$((( $(date +%s%N) - start_ns ) / 1000000))
			break
		fi
	fi
	sleep 0.05
done
[[ -n "$ready_ms" ]] || die "the app did not become ready (see $out/app.log)"
[[ "$base" =~ ^http://127\.0\.0\.1:[0-9]+$ ]] || die "unexpected API URL"

tsv="$work/metrics.tsv"
if [[ -z "$work" ]]; then tsv="$(mktemp)"; tmp_files+=("$tsv"); fi
: >"$tsv"

request_code() { # path
	curl -sS --config "$cfg" --max-time 120 -o /dev/null -w '%{http_code}' "$base$1" 2>/dev/null || true
}

warm() { # path
	local code
	code="$(request_code "$1")"
	[[ "$code" == 200 ]] || die "warm-up $1 answered $code"
}

measure() { # name path iterations
	local name="$1" path="$2" iterations="${3:-5}" i code seconds bytes out
	for i in $(seq 1 "$iterations"); do
		out="$(curl -sS --config "$cfg" --max-time 120 -o /dev/null \
			-w '%{http_code} %{time_total} %{size_download}' "$base$path")" || die "$name: curl failed"
		read -r code seconds bytes <<<"$out"
		[[ "$code" == 200 ]] || die "$name answered $code"
		printf '%s\t%s\t%s\n' "$name" "$(awk -v s="$seconds" 'BEGIN { printf "%.1f", s * 1000 }')" "$bytes" >>"$tsv"
	done
}

say "warming the endpoints"
warm "/api/status"
warm "/api/status"
warm "/api/learning"
# The prepared route must be visible: a wrong storage key measures an empty
# page and reports a meaningless budget. Ask once and require rows; the body
# reaches python on stdin, so the token stays in the curl config alone.
curl -sS --config "$cfg" --max-time 30 "$base/api/triage?limit=50&offset=0" |
	python3 -c '
import json, sys
body = json.load(sys.stdin)
if not body.get("total"):
    raise SystemExit("the prepared triage page is empty: the fixture route key does not match the app")
print("triage rows visible:", body.get("total"))
'
warm "/api/triage?limit=50&offset=0"
warm "/api/triage/summary"
warm "/api/search?q=facture"

say "measuring"
measure status "/api/status" 5
measure learning "/api/learning" 5
measure triage_page "/api/triage?limit=50&offset=0" 5
measure triage_summary "/api/triage/summary" 5
measure search_degraded "/api/search?q=facture" 5
measure search_common "/api/search?q=de" 5

rss_kb="$(awk '/VmHWM/ { print $2 }' "/proc/$pid/status" 2>/dev/null || echo 0)"
peak_rss_mb=$((rss_kb / 1024))

bench_out="$out/labels-bench.txt"
"$GO" test -run '^$' -bench '^BenchmarkLabelsStageStatus$' -benchtime 5x -count=1 ./internal/layer1 >"$bench_out" 2>&1
labels_ns="$(awk '/BenchmarkLabelsStageStatus/ { print $3; exit }' "$bench_out")"
[[ "$labels_ns" =~ ^[0-9]+$ ]] || die "the labels benchmark produced no ns/op (see $bench_out)"
labels_ms=$((labels_ns / 1000000))

say "checking the search budgets and the warm merge"
"$GO" test ./internal/search -run 'TestHybridSearchFusesBothRankings|TestSlowEmbedderTimesOut|TestSearchDegrades' -count=1 >"$out/search-gate.txt" 2>&1 ||
	die "the search budget gate failed (see $out/search-gate.txt)"

set +e
python3 - "$tsv" "$out/metrics.json" "$ready_ms" "$labels_ms" "$peak_rss_mb" <<'PY' | tee "$out/summary.txt"
import json, math, sys
path, out, ready, labels, rss = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5])
rows = {}
for line in open(path):
    name, ms, size = line.rstrip("\n").split("\t")
    rows.setdefault(name, []).append((float(ms), int(size)))

def percentile(values, p):
    values = sorted(values)
    if not values:
        return 0.0
    rank = min(len(values) - 1, max(0, math.ceil(p * len(values)) - 1))
    return values[rank]

# budget, 1.2x host-noise allowance, unit
budgets = {
    "ready": (3000, 1.2, "ms"),
    "status": (1300, 1.2, "ms"),
    "learning": (1300, 1.2, "ms"),
    "labels": (100, 1.2, "ms"),
    "triage_page": (100, 1.2, "ms"),
    "triage_page_bytes": (800000, 1.2, "B"),
    "triage_summary": (2000, 1.2, "ms"),
    "search_degraded": (250, 1.2, "ms"),
    "search_common": (250, 1.2, "ms"),
    "peak_rss": (700, 1.2, "MB"),
}
metrics = {}
for name, samples in rows.items():
    metrics[name] = {
        "n": len(samples),
        "median_ms": round(percentile([s[0] for s in samples], 0.5), 1),
        "p95_ms": round(percentile([s[0] for s in samples], 0.95), 1),
        "bytes": max(s[1] for s in samples),
    }
# The label count is in-process (BenchmarkLabelsStageStatus), the summary
# page has no separate byte budget (it is bounded by the profile).
metrics.setdefault("triage_page", {})["bytes"] = metrics.get("triage_page", {}).get("bytes", 0)
report = {
    "ready_ms": ready,
    "labels_stage_ms": labels,
    "peak_rss_mb": rss,
    "endpoints": metrics,
}
json.dump(report, open(out, "w"), indent=2, sort_keys=True)

checks = [
    ("ready", ready, budgets["ready"]),
    ("labels_stage", labels, budgets["labels"]),
    ("status", metrics.get("status", {}).get("median_ms", 0), budgets["status"]),
    ("learning", metrics.get("learning", {}).get("median_ms", 0), budgets["learning"]),
    ("triage_page", metrics.get("triage_page", {}).get("median_ms", 0), budgets["triage_page"]),
    ("triage_page_bytes", metrics.get("triage_page", {}).get("bytes", 0), budgets["triage_page_bytes"]),
    ("triage_summary", metrics.get("triage_summary", {}).get("median_ms", 0), budgets["triage_summary"]),
    ("search_degraded", metrics.get("search_degraded", {}).get("median_ms", 0), budgets["search_degraded"]),
    ("search_common", metrics.get("search_common", {}).get("median_ms", 0), budgets["search_common"]),
    ("peak_rss", rss, budgets["peak_rss"]),
]
print(f"{'metric':<20} {'value':>12} {'budget':>10} {'limit (1.2x)':>12}  result")
failed = False
for name, value, (budget, factor, unit) in checks:
    limit = budget * factor
    ok = value <= limit
    failed = failed or not ok
    print(f"{name:<20} {value:>10.1f} {budget:>10.0f} {limit:>12.0f}  {'ok' if ok else 'FAIL'}")
for name in ("status", "learning", "triage_page", "triage_summary", "search_degraded", "search_common"):
    m = metrics.get(name)
    if not m:
        print(f"missing metric {name}")
        failed = True
        continue
    print(f"{name + ' p95':<20} {m['p95_ms']:>10.1f} ms")
sys.exit(1 if failed else 0)
PY
status=${PIPESTATUS[0]}
set -e
if ((status != 0)); then
	say "PERF REGRESSION: see $out/metrics.json"
	exit 1
fi
say "perf budget met; aggregates in $out/summary.txt"
