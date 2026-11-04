#!/usr/bin/env bash
# Ask Mailsense to stop through its own loopback API and wait for it.
#
#   scripts/stop-windows.sh [grace-seconds]     (default 30)
#
# It reads <data>/runtime/api.json, checks that the PID is alive before
# using the descriptor, sends the bearer token to curl on stdin (never in
# argv, a process list or this script's output), POSTs /api/shutdown, then
# waits for the process to exit and the descriptor to disappear. It never
# kills a process and never prints the token.
#
# Environment:
#   MAILSENSE_DATA_DIR     the data directory (default as config.ResolvePaths)
#   STOP_TIMEOUT seconds for one HTTP request (default 10)
#
# Exit codes: 0 stopped (or not running); 1 the request failed; 2 still
# running after the grace period, or the process exited but left the
# descriptor (its PID is printed for a human to decide).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/runtime-lib.sh"

GRACE="${1:-30}"
[[ "$GRACE" =~ ^[1-9][0-9]*$ ]] || { echo "usage: stop-windows.sh [grace-seconds]" >&2; exit 1; }
REQUEST_TIMEOUT="${STOP_TIMEOUT:-10}"
[[ "$REQUEST_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || { echo "error: STOP_TIMEOUT must be a positive whole number of seconds" >&2; exit 1; }

DESCRIPTOR="$(runtime_descriptor)"
if [[ ! -f "$DESCRIPTOR" ]]; then
	printf 'Mailsense is not running (no runtime descriptor at %s).\n' "$DESCRIPTOR"
	exit 0
fi

URL="$(runtime_json_string "$DESCRIPTOR" url)"
PID="$(runtime_json_uint "$DESCRIPTOR" pid)"
runtime_valid_url "$URL" || { echo "error: the runtime descriptor has no loopback URL: $DESCRIPTOR" >&2; exit 1; }
[[ "$PID" =~ ^[1-9][0-9]*$ ]] || { echo "error: the runtime descriptor has no PID: $DESCRIPTOR" >&2; exit 1; }

if ! runtime_pid_alive "$PID"; then
	printf 'Mailsense is not running (stale descriptor for PID %s); the next launch overwrites it.\n' "$PID"
	exit 0
fi

TOKEN="$(runtime_json_string "$DESCRIPTOR" token)"
[[ "$TOKEN" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo "error: the runtime descriptor has no valid launch token" >&2; exit 1; }

# The token goes through curl's config on stdin, never argv. The descriptor
# URL is validated above, so this request cannot leave loopback. The body is
# captured but never printed; only the final status code is used.
set +e
response="$(
	printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
		curl -sS -K - --max-time "$REQUEST_TIMEOUT" -w '\n%{http_code}' \
			-X POST -H 'Content-Type: application/json' --data-binary '{}' "${URL}api/shutdown"
)"
rc=$?
set -e
if ((rc != 0)); then
	printf 'error: cannot reach Mailsense at %s (curl exit %d)\n' "$URL" "$rc" >&2
	exit 1
fi
code="${response##*$'\n'}"
case "$code" in
202 | 503) ;; # accepted, or already shutting down
401)
	printf 'error: Mailsense refused the launch token (stale descriptor?)\n' >&2
	exit 1
	;;
*)
	printf 'error: Mailsense answered HTTP %s to the shutdown request\n' "$code" >&2
	exit 1
	;;
esac

deadline=$((SECONDS + GRACE))
while runtime_pid_alive "$PID"; do
	if ((SECONDS >= deadline)); then
		printf 'Mailsense (PID %s) is still running after %s s; not killed.\n' "$PID" "$GRACE" >&2
		exit 2
	fi
	sleep 1
done

for _ in 1 2 3 4 5; do
	if [[ ! -f "$DESCRIPTOR" ]]; then
		printf 'Mailsense stopped (PID %s); the runtime descriptor is gone.\n' "$PID"
		exit 0
	fi
	sleep 1
done
printf 'Mailsense (PID %s) stopped, but the descriptor remains: %s\n' "$PID" "$DESCRIPTOR" >&2
exit 2
