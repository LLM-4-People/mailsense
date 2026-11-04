#!/usr/bin/env bash
# Run the browser end-to-end suite (Playwright + Chromium).
#
#   scripts/test-ui.sh [playwright args…]     e.g. scripts/test-ui.sh ask.spec.ts
#
# The suite starts the real Mailsense binary on an isolated temporary data
# directory per worker and mocks only content endpoints with synthetic data.
# Windows-only behavior (WebView2, Outlook COM, DPAPI) is still verified on
# the Windows host; this gate covers the browser shell and contracts.
#
# Environment:
#   MAILSENSE_E2E_INSTALL=1   install the pinned Chromium before running
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$ROOT/e2e"

if [[ ! -d node_modules ]]; then
	npm ci --no-audit --no-fund
fi
if [[ "${MAILSENSE_E2E_INSTALL:-0}" == "1" ]]; then
	npx playwright install chromium
fi
exec npx playwright test "$@"
