#!/usr/bin/env bash
# Compile the Windows installer from a built dist/mailsense.exe.
#
#   scripts/build.sh windows
#   packaging/windows/compile.sh
#
# The version comes from dist/VERSION, which scripts/build.sh owns, so the
# installer name and its version stamp cannot disagree with the binary. The
# validated path is appended to GITHUB_OUTPUT when GitHub Actions sets it, so
# the release workflow derives the asset name instead of restating it. ISCC
# must be installed (Inno Setup 6); set ISCC to a different path when needed.
#
# Git Bash rewrites a leading slash into a drive path. Both variables switch
# that off, or ISCC reads /DAppVersion and /DSourceExe as script filenames.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

version_file="dist/VERSION"
[[ -s "$version_file" ]] || {
	printf 'error: %s is missing; run scripts/build.sh windows first\n' "$version_file" >&2
	exit 1
}
version="$(tr -d '\r\n' <"$version_file")"
[[ -n "$version" ]] || {
	printf 'error: %s is empty\n' "$version_file" >&2
	exit 1
}
[[ -s dist/mailsense.exe ]] || {
	printf 'error: dist/mailsense.exe is missing; run scripts/build.sh windows first\n' >&2
	exit 1
}

iscc="${ISCC:-/c/Program Files (x86)/Inno Setup 6/ISCC.exe}"
[[ -x "$iscc" ]] || {
	printf 'error: ISCC not found at %s; install Inno Setup 6 or set ISCC\n' "$iscc" >&2
	exit 1
}

source_exe="$(cd dist && pwd -W)/mailsense.exe"
export MSYS2_ARG_CONV_EXCL='*'
export MSYS_NO_PATHCONV=1

printf 'iscc /DAppVersion=%s /DSourceExe=%s\n' "$version" "$source_exe"
"$iscc" "/DAppVersion=$version" "/DSourceExe=$source_exe" packaging/windows/mailsense.iss

setup="dist/mailsense-${version}-setup.exe"
[[ -s "$setup" ]] || {
	printf 'error: %s was not produced\n' "$setup" >&2
	exit 1
}
# CI captures the validated path here, so the release workflow consumes this
# one owner instead of restating the name pattern.
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
	printf 'setup=%s\n' "$setup" >>"$GITHUB_OUTPUT"
fi
printf 'ok   %s\n' "$setup"
