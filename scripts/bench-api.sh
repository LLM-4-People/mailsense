#!/usr/bin/env bash
# Measure loopback API latency and bytes on a prepared store.
#
#   scripts/bench-api.sh [--store DIR] [--binary PATH] [--out DIR] [--samples N]
#                        [--encoding identity|gzip|both] [--endpoint PATH]
#
# The app is started with GOMAXPROCS=4 and no display, then each endpoint is
# requested with the launch token read from runtime/api.json. The token is
# never printed. Output: one `endpoint enc=... p50_ms p95_ms bytes status`
# line per endpoint and encoding, plus a JSON file with every sample.
set -euo pipefail
cd "$(dirname "$0")/.."

store="${PERF_STORE_DIR:-}"
binary=""
out="out/bench/api"
samples=10
encoding="identity"
endpoints=()
while [[ $# -gt 0 ]]; do
	case "$1" in
	--store) store="$2"; shift 2 ;;
	--binary) binary="$2"; shift 2 ;;
	--out) out="$2"; shift 2 ;;
	--samples) samples="$2"; shift 2 ;;
	--encoding) encoding="$2"; shift 2 ;;
	--endpoint) endpoints+=("$2"); shift 2 ;;
	*) echo "error: unknown argument $1" >&2; exit 2 ;;
	esac
done
case "$encoding" in
identity | gzip | both) ;;
*) echo "error: --encoding must be identity, gzip or both" >&2; exit 2 ;;
esac
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

if [[ ${#endpoints[@]} -eq 0 ]]; then
	endpoints=(
		"/api/status"
		"/api/learning"
		"/api/hub"
		"/api/jobs"
		"/api/mailboxes"
		"/api/messages?limit=100&counts=1"
		"/api/priority?limit=50"
		"/api/conversations?limit=30"
		"/api/companies?limit=100"
		"/api/agenda?hours=3"
		"/api/triage?limit=50&offset=0"
		"/api/triage/summary"
		"/api/search?q=projet"
		"/app.js"
		"/app.css"
		"/i18n.en.js"
		"/i18n.fr.js"
		"/i18n.js"
		"/"
	)
fi

rm -f "$store/runtime/api.json"
before=0
log="$store/logs/mailsense.log"
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
for _ in $(seq 1 1200); do
	[[ -f "$store/runtime/api.json" ]] && break
	kill -0 "$pid" 2>/dev/null || { echo "error: the app exited before it was ready" >&2; tail -20 "$out/app.log" >&2; exit 1; }
	sleep 0.1
done
[[ -f "$store/runtime/api.json" ]] || { echo "error: the app never became ready" >&2; exit 1; }
ready_ms=$(( ($(date +%s%N) - started) / 1000000 ))

base="$(jq -r '.url' "$store/runtime/api.json" | sed 's:/*$::')"
token="$(jq -r '.token' "$store/runtime/api.json")"
[[ -n "$base" && -n "$token" && "$token" != "null" ]] || { echo "error: the API descriptor is incomplete" >&2; exit 1; }

printf 'ready_ms=%s\n' "$ready_ms"
python3 - "$base" "$token" "$samples" "$encoding" "$out/samples.json" "${endpoints[@]}" <<'PY'
import json, subprocess, sys, time
base, token, samples, encoding = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
out = sys.argv[5]
endpoints = sys.argv[6:]
encodings = ["identity", "gzip"] if encoding == "both" else [encoding]
rows = []
for path in endpoints:
    by_encoding = {}
    for enc in encodings:
        times, sizes, status = [], [], 0
        for _ in range(samples):
            t0 = time.perf_counter()
            args = ["curl", "-sS", "--max-time", "120", "-o", "/dev/null",
                    "-w", "%{http_code} %{size_download} %header{content-encoding}",
                    "-H", f"Authorization: Bearer {token}"]
            if enc == "gzip":
                args += ["-H", "Accept-Encoding: gzip"]
            p = subprocess.run(args + [base + path], capture_output=True, text=True)
            elapsed = (time.perf_counter() - t0) * 1000
            code, _, rest = p.stdout.partition(" ")
            size, _, got_enc = rest.partition(" ")
            got_enc = got_enc.strip()
            status = int(code) if code.isdigit() else 0
            times.append(elapsed)
            sizes.append(int(size) if size.isdigit() else 0)
        times.sort()
        if times:
            p50 = times[int(len(times) * 0.5) - 1] if len(times) > 1 else times[0]
            p95 = times[min(len(times) - 1, int(len(times) * 0.95))]
        else:
            p50 = p95 = 0
        row = {"path": path, "encoding": enc, "status": status,
               "p50_ms": round(p50, 1), "p95_ms": round(p95, 1),
               "bytes": sizes[-1] if sizes else 0, "samples": len(times)}
        rows.append(row)
        by_encoding[enc] = row
        print(f"{path} enc={enc} status={status} p50_ms={p50:.1f} p95_ms={p95:.1f} "
              f"bytes={sizes[-1] if sizes else 0} content_encoding={got_enc or 'none'}")
    if encoding == "both":
        plain = by_encoding["identity"]["bytes"]
        compressed = by_encoding["gzip"]["bytes"]
        saved = 100 * (1 - compressed / plain) if plain else 0
        print(f"{path} gzip_saved_bytes={plain - compressed} gzip_saved_pct={saved:.1f}")
json.dump(rows, open(out, "w"), indent=1)
PY

tail -n +"$((before + 1))" "$log" >"$out/startup.jsonl" || true
