# Documentation screenshots

This project captures the product screens shown on the site. It is separate
from `e2e/specs`, so the default test run never rewrites a committed image.

```sh
cd e2e
npx playwright test --config screenshots/playwright.config.ts
```

The capture boots the real Mailsense binary through the shared server fixture
and answers every content route with the invented mailbox in `data.ts`. Sender
names and addresses come from `senders.json`; `go test ./internal/site` fails
if the script names an address outside that reserved `.test` cast.

## What it writes

- `docs/assets/screenshots/<lang>/<screen>.png` for every screen listed in the
  `media` table in `docs/site.json`, in English and French.
- `docs/assets/social-preview.png`, rendered from the generated brand banner.

The screen list, the output directory and all four sizes live in
`docs/site.json`, so the capture script, the Go renderer and the truth test
read one table. Adding a screen there without a routine in `capture.spec.ts`
fails the capture.

## Reproducibility

The viewport is 1440 by 900, the zone is `Europe/Paris`, and the clock is fixed
at `2026-11-03T09:15:00Z`. The boot strip, the job strip, the connection
indicator and any toast are hidden before the frame. A loading skeleton or a
read error fails the capture instead of shipping.

Run `go run ./cmd/gensite -write` before the capture when the brand banner
changed: the social preview is rendered from the generated SVG.
