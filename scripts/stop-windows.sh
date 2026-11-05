#!/usr/bin/env bash
# Ask Mailsense to stop through its own loopback API and wait for it.
#
#   scripts/stop-windows.sh [grace-seconds]     (default 30)
#
# It reads <data>/runtime/api.json, sends the bearer token to curl on stdin
# (never in argv, a process list or this script's output), POSTs /api/shutdown,
# then waits only for the runtime descriptor to disappear, for at most the
# grace period. It never kills a process and never prints the token.
#
# A dead descriptor PID proves the descriptor is stale, so that check runs
# before the request. A live PID proves nothing, because Windows reuses process
# numbers, so the script never polls the PID after the request: a refused
# connection (curl exit 7) or an HTTP 401 means the app is not running (there
# is nothing to stop), and a 202 (accepted) or 503 (already shutting down)
# means the app is stopping, with the descriptor's disappearance as the only
# completion signal.
#
# The script does not ask which process owns the descriptor's loopback port:
# that needs an owner lookup this script does not have. The Windows liveness
# probe and the app's own descriptor cleanup can only be confirmed on a real
# Windows run.
#
# Environment:
#   MAILSENSE_DATA_DIR     the data directory (default as config.ResolvePaths)
#   STOP_TIMEOUT seconds for one HTTP request (default 10)
#
# Exit codes: 0 stopped, or not running (no descriptor, stale PID, refused
# connection or HTTP 401); 1 invalid arguments or descriptor, or the shutdown
# request failed; 2 the descriptor was still present after the grace period.
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

# A dead PID proves the descriptor stale: the launch that wrote it is gone.
# A live PID proves nothing (Windows reuses process numbers), so the HTTP
# answer below, not this check, decides whether the app is still there. This
# is the only PID check: after the request the descriptor is the signal.
if ! runtime_pid_alive "$PID"; then
	printf 'Mailsense is not running (stale descriptor for PID %s); the next launch overwrites it.\n' "$PID"
	exit 0
fi

TOKEN="$(runtime_json_string "$DESCRIPTOR" token)"
[[ "$TOKEN" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo "error: the runtime descriptor has no valid launch token" >&2; exit 1; }

# The token goes through curl's config on stdin, never argv. The descriptor
# URL is validated above and curl bypasses every proxy, so this request cannot
# leave loopback. The body is captured but never printed; only the final status
# code is used. curl's stderr is captured too, so a refused connection is a
# quiet "not running" answer and is shown only for a real failure.
stderr_file="$(mktemp "${TMPDIR:-/tmp}/mailsense-stop.XXXXXX")"
trap 'rm -f "$stderr_file"' EXIT
set +e
response="$(
	printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
		curl -sS --noproxy '*' -K - --max-time "$REQUEST_TIMEOUT" -w '\n%{http_code}' \
			-X POST -H 'Content-Type: application/json' --data-binary '{}' "${URL}api/shutdown" \
			2>"$stderr_file"
)"
rc=$?
set -e
if ((rc == 7)); then
	# curl exit 7 is a refused connection: no process accepted the request,
	# so the descriptor's app is not running.
	printf 'Mailsense is not running (connection refused at %s).\n' "$URL"
	exit 0
fi
if ((rc != 0)); then
	printf 'error: cannot reach Mailsense at %s (curl exit %d)\n' "$URL" "$rc" >&2
	[[ ! -s "$stderr_file" ]] || cat "$stderr_file" >&2
	exit 1
fi
code="${response##*$'\n'}"
case "$code" in
202 | 503) ;; # accepted, or already shutting down
401)
	# The app does not accept this descriptor's token, so this descriptor no
	# longer describes the running app: there is nothing here to stop.
	printf 'Mailsense is not running (HTTP 401: the descriptor token was refused).\n'
	exit 0
	;;
*)
	printf 'error: Mailsense answered HTTP %s to the shutdown request\n' "$code" >&2
	exit 1
	;;
esac

# The PID is deliberately not polled again: a live PID proves nothing, and
# 202/503 means the app accepted the request. The descriptor is the app's own
# statement that it is running, so its disappearance is the only completion
# signal, bounded by the same grace period.
deadline=$((SECONDS + GRACE))
while [[ -f "$DESCRIPTOR" ]]; do
	if ((SECONDS >= deadline)); then
		printf 'error: Mailsense accepted the shutdown but the runtime descriptor is still present after %s s: %s\n' "$GRACE" "$DESCRIPTOR" >&2
		exit 2
	fi
	sleep 1
done
printf 'Mailsense stopped; the runtime descriptor is gone.\n'
exit 0
