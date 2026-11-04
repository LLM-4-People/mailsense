#!/usr/bin/env bash
# Shared helpers for the app runtime scripts (sourced, not executed):
# locating the data directory and the runtime descriptor, reading the
# descriptor, and checking a PID. They never read or print the token: only
# stop-windows.sh reads it, into a variable that goes to curl on stdin.
#
#   scripts/run-windows.sh   start the app and wait for its descriptor
#   scripts/stop-windows.sh  ask the app to stop through its own API

# runtime_data_dir: MAILSENSE_DATA_DIR, else %LOCALAPPDATA%\Mailsense on
# Windows, else $XDG_DATA_HOME/mailsense or ~/.local/share/mailsense. This
# mirrors config.ResolvePaths; the app, not the script, is the owner.
runtime_data_dir() {
	if [[ -n "${MAILSENSE_DATA_DIR:-}" ]]; then
		printf '%s' "$MAILSENSE_DATA_DIR"
	elif [[ -n "${LOCALAPPDATA:-}" ]]; then
		printf '%s' "$LOCALAPPDATA/Mailsense"
	elif [[ -n "${XDG_DATA_HOME:-}" ]]; then
		printf '%s' "$XDG_DATA_HOME/mailsense"
	else
		printf '%s' "$HOME/.local/share/mailsense"
	fi
}

# runtime_shell_path <path>: the path as this shell's file functions see it
# (Git Bash receives Windows paths from LOCALAPPDATA).
runtime_shell_path() {
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -u "$1"
	else
		printf '%s' "$1"
	fi
}

runtime_descriptor() {
	printf '%s/runtime/api.json' "$(runtime_shell_path "$(runtime_data_dir)")"
}

runtime_error_report() {
	printf '%s/startup-error.txt' "$(runtime_shell_path "$(runtime_data_dir)")"
}

runtime_is_windows() {
	case "$(uname -s 2>/dev/null)" in
	MINGW* | MSYS* | CYGWIN*) return 0 ;;
	*) return 1 ;;
	esac
}

# runtime_pid_alive <pid>: a liveness probe only, never a signal.
runtime_pid_alive() {
	[[ "$1" =~ ^[1-9][0-9]*$ ]] || return 1
	if runtime_is_windows; then
		powershell.exe -NoProfile -NonInteractive -Command \
			"if (Get-Process -Id $1 -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }" >/dev/null 2>&1
	else
		ps -p "$1" >/dev/null 2>&1
	fi
}

# runtime_json_string <file> <key>: one top-level string field of the
# compact descriptor. The values the script reads (url, token) contain no
# JSON escapes; a malformed file simply yields nothing.
runtime_json_string() {
	sed -n 's/.*"'"$2"'":"\([^"\\]*\)".*/\1/p' "$1" | head -1
}

runtime_json_uint() {
	sed -n 's/.*"'"$2"'":\([0-9][0-9]*\).*/\1/p' "$1" | head -1
}

# runtime_valid_url <url>: exactly the loopback base URL the API writes.
runtime_valid_url() {
	[[ "$1" =~ ^http://127\.0\.0\.1:[1-9][0-9]*/$ ]]
}

# runtime_ps_quote <text>: a PowerShell single-quoted string literal.
runtime_ps_quote() {
	printf "'%s'" "${1//\'/\'\'}"
}
