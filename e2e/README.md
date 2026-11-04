# Mailsense browser E2E

The UI is a no-build, embedded shell. This suite runs it in Chromium against
the **real** Mailsense binary: real launch token, real Bearer auth, real CSP,
real static caching, real 401 handling and real SSE. Only content endpoints
that need mail are served from synthetic fixtures, so no personal mailbox data
is used and the suite can run on Linux.

## What it catches

- the token handshake and the reopen screen;
- the boot contract: the first route skeleton mounts before the status and
  learning reads, the boot strip reports real progress, hides at a clean
  ready, and stays as a dismissible warning when a ready warm has failed
  views;
- primary-route mounting, page errors and off-origin requests;
- Ask follow-ups appending to one transcript with the same conversation id,
  inline clarify options, pending turns and no-match notices;
- Mail list, viewer, filters and search;
- Companies list/profile/people and client-side filtering;
- live Learning progress painted in place from `index.progress`, and a forced
  refresh on `jobs.update`;
- the real log stream, level/component filters, pause/resume and clear.

It does **not** replace Windows verification: WebView2 pixels, DPI, the title
bar icon, Outlook COM and DPAPI remain Windows-only.

## Run

```sh
cd e2e
npm ci
npm run install:browsers   # once, installs the pinned Chromium
npx playwright test
```

Or from the repository root:

```sh
scripts/test-ui.sh
```

`scripts/test-ui.sh` installs Node dependencies on first use. Set
`MAILSENSE_E2E_INSTALL=1` to install the browser too.

## Design

- `global-setup.ts` builds `e2e/.cache/mailsense` once.
- `fixtures/server.ts` starts one real server per worker on a temporary data
  directory with `learning.test_mode` preview enabled and English forced. The
  descriptor is read from `<data>/runtime/api.json`; the token is never
  printed, never put in a URL and never stored in a trace.
- `fixtures/test.ts` seeds `sessionStorage["mailsense.token"]` and provides
  `goto`, `expectRoute` and `expectLive`.
- `fixtures/mock.ts` is one catch-all `page.route` router. Unlisted requests
  fall through to the real server.
- `fixtures/sse.ts` replaces only `window.EventSource`, for deterministic
  progress/event tests. Boot and Logs keep the real stream.
- `fixtures/data.ts` holds synthetic fixtures. They mirror the Go DTOs named
  in comments; update them with the server view.

Traces and video are disabled because the SSE URL carries the launch token.
Failure screenshots contain synthetic data only.
