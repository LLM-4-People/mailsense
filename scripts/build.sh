#!/usr/bin/env bash
# Build Mailsense binaries.
#
#   scripts/build.sh [linux|windows|all]     (default: all)
#
# Environment:
#   VERSION   version stamp (default: 0.1.0-dev+<UTC timestamp>)
#   OUT_DIR   output directory (default: <repo>/dist)
#   GOARCH    target architecture (default: amd64)
#   GO        Go toolchain to use
#
# Both targets are pure Go (CGO_ENABLED=0, -trimpath). The Windows exe is
# stripped and linked as a GUI program (-H windowsgui: no console window).
#
# This script is the one version owner. It stamps VERSION into both binaries,
# writes the same value to $OUT_DIR/VERSION for the installers and the tarball
# builder, and therefore names every artifact from one place.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

target="${1:-all}"
VERSION="${VERSION:-0.1.0-dev+$(date -u +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"
ARCH="${GOARCH:-amd64}"
stamp="-X github.com/LLM-4-People/mailsense/internal/buildinfo.Version=$VERSION"

# The version becomes an Inno Setup AppVersion, a file name and a link-time
# string. Reject anything that could break one of those surfaces.
[[ "$VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]] ||
	die "VERSION must start with a letter or digit and contain only letters, digits, dot, underscore, plus or hyphen: $VERSION"

build() { # os output ldflags
	say "building $1/$ARCH → $2 ($VERSION)"
	go_cross "$1" build -ldflags "$3" -o "$2" ./cmd/mailsense
	ls -l "$2" | awk '{printf "    %s bytes\n", $5}'
}

case "$target" in
linux | windows | all) ;;
*) die "unknown target '$target' (linux|windows|all)" ;;
esac
mkdir -p "$OUT_DIR"
# A failed build must not leave a previous version behind for the packaging
# steps to read.
rm -f "$OUT_DIR/VERSION" "$OUT_DIR/VERSION.tmp"
printf '%s\n' "$VERSION" >"$OUT_DIR/VERSION.tmp"
trap 'rm -f "$OUT_DIR/VERSION.tmp"' EXIT
if [[ "$target" == linux || "$target" == all ]]; then
	build linux "$OUT_DIR/mailsense" "$stamp"
fi
if [[ "$target" == windows || "$target" == all ]]; then
	build windows "$OUT_DIR/mailsense.exe" "-s -w -H windowsgui $stamp"
fi
mv "$OUT_DIR/VERSION.tmp" "$OUT_DIR/VERSION"
trap - EXIT
say "version $VERSION written to $OUT_DIR/VERSION"
