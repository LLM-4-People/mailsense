#!/usr/bin/env bash
# Start Mailsense and wait until its loopback API is ready.
#
#   scripts/run-windows.sh
#
# Environment:
#   RUN_EXE           the binary (default dist/mailsense.exe)
#   MAILSENSE_DATA_DIR      the data directory (default as config.ResolvePaths)
#   RUN_TIMEOUT seconds to wait for the descriptor (default 60)
#
# The launch token is never printed: the app writes it to
# <data>/runtime/api.json (mode 0600) for scripts/stop-windows.sh and hands
# it to its own window. On Windows the GUI exe is started detached through
# cmd; elsewhere the script starts the named binary directly (the Linux dev
# build, for smoke tests).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/runtime-lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXE="${RUN_EXE:-$ROOT/dist/mailsense.exe}"
TIMEOUT="${RUN_TIMEOUT:-60}"
[[ "$TIMEOUT" =~ ^[1-9][0-9]*$ ]] || { echo "error: RUN_TIMEOUT must be a positive whole number of seconds" >&2; exit 1; }
[[ -f "$EXE" ]] || { echo "error: $EXE not found (run scripts/build.sh windows)" >&2; exit 1; }

DESCRIPTOR="$(runtime_descriptor)"
REPORT="$(runtime_error_report)"

running_url() {
	[[ -f "$DESCRIPTOR" ]] || return 1
	local url pid
	url="$(runtime_json_string "$DESCRIPTOR" url)"
	pid="$(runtime_json_uint "$DESCRIPTOR" pid)"
	runtime_valid_url "$url" || return 1
	runtime_pid_alive "$pid" || return 1
	printf '%s' "$url"
}

if url="$(running_url)"; then
	printf 'Mailsense is already running at %s\n' "$url"
	exit 0
fi

launch_pid=""
if runtime_is_windows; then
	exe_win="$(cygpath -w "$EXE")"
	launch_pid="$(powershell.exe -NoProfile -NonInteractive -Command \
		"(Start-Process -FilePath $(runtime_ps_quote "$exe_win") -PassThru).Id" | tr -d '\r')"
	[[ "$launch_pid" =~ ^[1-9][0-9]*$ ]] || { echo "error: cannot start $EXE" >&2; exit 1; }
else
	nohup "$EXE" >/dev/null 2>&1 &
	launch_pid=$!
fi

# Wait for the descriptor the app writes only after the listener is bound
# and the token is on disk. A failed start usually leaves startup-error.txt
# (or no process at all), so stop waiting as soon as the process is gone.
deadline=$((SECONDS + TIMEOUT))
while ((SECONDS < deadline)); do
	if url="$(running_url)"; then
		printf 'Mailsense is running at %s\n' "$url"
		printf 'The launch token is in %s; the window already holds it.\n' "$DESCRIPTOR"
		exit 0
	fi
	if ! runtime_pid_alive "$launch_pid"; then
		if [[ -f "$REPORT" ]]; then
			printf 'Mailsense failed to start; its report is %s\n' "$REPORT" >&2
		else
			printf 'Mailsense exited before its API was ready.\n' >&2
		fi
		exit 1
	fi
	sleep 1
done

printf 'Mailsense did not become ready within %s s.\n' "$TIMEOUT" >&2
if [[ -f "$REPORT" ]]; then
	printf 'Its report is %s\n' "$REPORT" >&2
fi
exit 1
