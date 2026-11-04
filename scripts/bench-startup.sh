#!/usr/bin/env bash
# Measure start-up phases on a prepared store (scripts/bench-startup.sh).
#
#   scripts/bench-startup.sh [--store DIR] [--binary PATH] [--out DIR]
#
# The store must already be imported (scripts/fixture-import.sh) and, for a
# ranking measurement, prepared with TestFixturePerfPrepare. The app is
# started with GOMAXPROCS=4 and no display. Two milestones are read:
# listen_ms is the runtime descriptor (the listener is accepting and the
# desktop window can open); ready_ms is the "mailsense ready" log line after
# the background warm. A build that writes both at once reports the old
# readiness as its proxy for listen. The process is sampled once at ready and
# then stopped. Only aggregates are printed: no subject, address, body or
# token.
set -euo pipefail
cd "$(dirname "$0")/.."

store="${PERF_STORE_DIR:-}"
binary=""
out="out/bench/startup"
while [[ $# -gt 0 ]]; do
	case "$1" in
	--store) [[ $# -ge 2 ]] || { echo "error: --store needs a directory" >&2; exit 2; }; store="$2"; shift 2 ;;
	--binary) [[ $# -ge 2 ]] || { echo "error: --binary needs a path" >&2; exit 2; }; binary="$2"; shift 2 ;;
	--out) [[ $# -ge 2 ]] || { echo "error: --out needs a directory" >&2; exit 2; }; out="$2"; shift 2 ;;
	*) echo "error: unknown argument $1" >&2; exit 2 ;;
	esac
done
[[ -n "$store" ]] || { echo "error: --store or PERF_STORE_DIR is required" >&2; exit 2; }
store="$(cd "$store" && pwd)"
[[ -f "$store/mailsense.db" ]] || { echo "error: $store has no mailsense.db" >&2; exit 2; }
mkdir -p "$out"
out="$(cd "$out" && pwd)"

if [[ -z "$binary" ]]; then
	binary="$out/mailsense"
	go build -o "$binary" ./cmd/mailsense
fi
binary="$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")"

rm -f "$store/runtime/api.json"
log="$store/logs/mailsense.log"
before=0
[[ -f "$log" ]] && before="$(wc -l <"$log")"

pid=""
cleanup() {
	if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
		kill -TERM "$pid" 2>/dev/null || true
		for _ in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
		kill -KILL "$pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT

started="$(date +%s%N)"
MAILSENSE_DATA_DIR="$store" GOMAXPROCS=4 DISPLAY= "$binary" >"$out/app.log" 2>&1 &
pid=$!
listen_ms=""
for _ in $(seq 1 6000); do
	if [[ -f "$store/runtime/api.json" ]]; then
		listen_ms=$(( ($(date +%s%N) - started) / 1000000 ))
		break
	fi
	kill -0 "$pid" 2>/dev/null || { echo "error: the app exited before it was ready" >&2; tail -20 "$out/app.log" >&2; exit 1; }
	sleep 0.01
done
[[ -n "$listen_ms" ]] || { echo "error: the app never published the descriptor" >&2; exit 1; }

ready_ms=""
for _ in $(seq 1 12000); do
	# Re-read the appended range every time. Tracking a byte offset races
	# the writer: a line appended between the read and the stat would be
	# skipped forever. This range is small and the ready line never moves.
	if [[ -f "$log" ]] && tail -n +"$((before + 1))" "$log" 2>/dev/null | grep -q '"msg":"mailsense ready"'; then
		ready_ms=$(( ($(date +%s%N) - started) / 1000000 ))
		break
	fi
	kill -0 "$pid" 2>/dev/null || { echo "error: the app exited before ready" >&2; tail -20 "$out/app.log" >&2; exit 1; }
	sleep 0.01
done
[[ -n "$ready_ms" ]] || { echo "error: the app never logged mailsense ready" >&2; exit 1; }

# Sample the process at ready. The 200 ms settle keeps the sampler out of the
# last seconds of the warm, matching the earlier resource series.
sleep 0.2
stat="$(cat /proc/$pid/stat)"
status="$(cat /proc/$pid/status 2>/dev/null || true)"
read -r _ _ _ _ _ _ _ _ _ _ _ _ _ utime stime _ < <(printf '%s\n' "$stat")
rss="$(awk '/^VmRSS:/ {print $2}' <<<"$status")"
hwm="$(awk '/^VmHWM:/ {print $2}' <<<"$status")"
threads="$(awk '/^Threads:/ {print $2}' <<<"$status")"
fds="$(find /proc/$pid/fd -maxdepth 1 -type l 2>/dev/null | wc -l)"
{
	printf 'utime_ticks=%s\nstime_ticks=%s\n' "$utime" "$stime"
	printf 'vmrss_kb=%s\nvmhwm_kb=%s\nthreads=%s\nfds=%s\n' "$rss" "$hwm" "$threads" "$fds"
} >"$out/sample.txt"

tail -n +"$((before + 1))" "$log" >"$out/startup.jsonl"
python3 - "$out/startup.jsonl" "$listen_ms" "$ready_ms" "$out/sample.txt" <<'PY'
import json, sys
path, listen, ready, sample_path = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
views = {}
ready_log = None
prepared = None
for line in open(path):
    try:
        e = json.loads(line)
    except Exception:
        continue
    msg, a = e.get("msg"), e.get("attrs") or {}
    if msg == "startup view warm":
        views[a.get("view")] = a.get("duration_ms")
    elif msg == "mailsense ready":
        ready_log = e.get("time")
    elif msg == "semantic index prepared":
        prepared = (a.get("covered"), a.get("eligible"), a.get("vectors"))
print(f"listen_ms={listen}")
print(f"ready_ms={ready}")
print(f"semantic_prepared={prepared}")
for name in ("priority", "companies", "conversations", "agenda", "commitments",
             "home", "learning", "brief", "triage_summary", "triage_page",
             "semantic_search"):
    if name in views:
        print(f"warm_{name.replace(' ', '_')}_ms={views[name]}")
sample = {}
for line in open(sample_path):
    key, _, value = line.strip().partition("=")
    sample[key] = value
cpu = (int(sample.get("utime_ticks", 0)) + int(sample.get("stime_ticks", 0))) / 100.0
print(f"cpu_s={cpu:.2f}")
print(f"vmrss_mb={int(sample.get('vmrss_kb', 0)) / 1024:.1f}")
print(f"vmhwm_mb={int(sample.get('vmhwm_kb', 0)) / 1024:.1f}")
print(f"fds={sample.get('fds', '')}")
print(f"threads={sample.get('threads', '')}")
PY
