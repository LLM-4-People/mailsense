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
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

target="${1:-all}"
VERSION="${VERSION:-0.1.0-dev+$(date -u +%Y%m%d%H%M%S)}"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"
ARCH="${GOARCH:-amd64}"
stamp="-X github.com/LLM-4-People/mailsense/internal/buildinfo.Version=$VERSION"

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
if [[ "$target" == linux || "$target" == all ]]; then
	build linux "$OUT_DIR/mailsense" "$stamp"
fi
if [[ "$target" == windows || "$target" == all ]]; then
	build windows "$OUT_DIR/mailsense.exe" "-s -w -H windowsgui $stamp"
fi
