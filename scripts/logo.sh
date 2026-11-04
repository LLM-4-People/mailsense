#!/usr/bin/env bash
# Regenerate the Windows icon resources from the one vector brand mark.
#
#   scripts/logo.sh
#
# The only source is internal/ui/favicon.svg (also the sidebar mark and the
# browser favicon). This script renders it at the sizes Windows asks for,
# writes cmd/mailsense/mailsense.ico, and embeds it in the Windows binaries
# through rsrc. It is a design-asset step, not part of the normal build.
#
# Requirements (dev only):
#   python3 with playwright and Pillow, and a Playwright Chromium build
#   rsrc: go install github.com/akavel/rsrc@latest
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v python3 >/dev/null 2>&1 || die "python3 not found"
python3 - <<'PY' || die "python3 needs playwright and Pillow (pip install playwright pillow; playwright install chromium)"
import playwright, PIL
PY

RSRC="${RSRC:-}"
if [[ -z "$RSRC" ]] && command -v rsrc >/dev/null 2>&1; then
	RSRC="$(command -v rsrc)"
fi
if [[ -z "$RSRC" ]] && [[ -x "$(go env GOPATH)/bin/rsrc" ]]; then
	RSRC="$(go env GOPATH)/bin/rsrc"
fi
[[ -n "$RSRC" ]] || die "rsrc not found (go install github.com/akavel/rsrc@latest)"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/mailsense-logo.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

say "rendering internal/ui/favicon.svg at 16, 24, 32, 48, 64, 128 and 256 px"
SVG="$ROOT/internal/ui/favicon.svg" TMPDIR_LOGO="$tmp" python3 - <<'PY'
import os, struct
from pathlib import Path
from playwright.sync_api import sync_playwright
from PIL import Image

root = Path(os.environ["TMPDIR_LOGO"])
svg = Path(os.environ["SVG"]).read_text()
sizes = (16, 24, 32, 48, 64, 128, 256)
with sync_playwright() as p:
    browser = p.chromium.launch()
    for n in sizes:
        page = browser.new_page(viewport={"width": n, "height": n}, device_scale_factor=1)
        page.set_content(
            "<!doctype html><style>html,body{margin:0;padding:0;background:transparent}"
            "svg{display:block;width:100vw;height:100vh}</style>" + svg
        )
        page.wait_for_timeout(60)
        page.screenshot(path=str(root / f"{n}.png"), omit_background=True)
        page.close()
    browser.close()

# ICO directory first, then the PNG-compressed entries (Vista+).
header = struct.pack("<HHH", 0, 1, len(sizes))
offset = 6 + 16 * len(sizes)
directory, blobs = [], []
for n in sizes:
    data = (root / f"{n}.png").read_bytes()
    directory.append(struct.pack("<BBBBHHII", 0 if n == 256 else n, 0 if n == 256 else n, 0, 0, 1, 32, len(data), offset))
    blobs.append(data)
    offset += len(data)
out = root / "mailsense.ico"
out.write_bytes(header + b"".join(directory) + b"".join(blobs))
with Image.open(out) as im:
    got = sorted(im.info.get("sizes", []))
if got != [(n, n) for n in sizes]:
    raise SystemExit(f"ICO sizes {got} do not match {sizes}")
print(f"    {out.stat().st_size} bytes, {len(sizes)} sizes")
PY

cp "$tmp/mailsense.ico" "$ROOT/cmd/mailsense/mailsense.ico"
for arch in amd64 arm64; do
	"$RSRC" -ico "$ROOT/cmd/mailsense/mailsense.ico" \
		-o "$ROOT/cmd/mailsense/rsrc_windows_${arch}.syso" -arch "$arch"
	say "embedded cmd/mailsense/rsrc_windows_${arch}.syso"
done
