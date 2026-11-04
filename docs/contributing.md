---
layout: default
title: Contributing
---

# Contributing

Mailsense is Go for the application and plain JavaScript for the interface. It
has no framework on either side. The repository is the source of truth for the
build, the tests and the public pages.

## Requirements

- Go 1.27 or newer.
- Node.js for the interface behavior tests. Those tests skip when Node is
  missing; the rest still run.
- A C toolchain for the race detector. `scripts/test.sh` turns race on when cgo
  works.

## Build and test

```bash
go build ./...
scripts/test-fast.sh    # inner loop: format, vet, scripts, unit and contract tests
scripts/test.sh         # exhaustive release gate
```

`scripts/test-fast.sh` is the normal gate before a commit. `scripts/test.sh` is
the one to run before a release. Both take their gofmt, script and vet checks
from one shared library.

After changing any file under `internal/ui/js` or `internal/ui/css`, regenerate
the served bundles:

```bash
go generate ./internal/ui
```

The bundle tests fail when the generated `app.js` or `app.css` is stale.

## Public pages

`docs/site.json` is the one source for the landing pages, both READMEs and the
brand SVGs. Do not edit a generated file by hand.

```bash
make site           # regenerate the public surface
make site-check     # fail when it is stale
```

A documentation page comes in a pair: `docs/name.md` and `docs/fr/name.md`. Add
the key to `internal/site/render.go`, `internal/site/site.go`, the `nav` array in
`docs/site.json` and `docs/_config.yml`. The release packaging reads the public
page list from `internal/site`, so a new page ships without a second copy of the
list.

## Public copy

`internal/prose` owns the style rule for text that ships to readers. It rejects
an em dash, a spaced en dash, a punctuation double hyphen and a list of stock
phrases. Fenced code and inline code are skipped. The docs test and the
interface catalog test both use that one owner.

Write in plain English and plain French. Prefer a short sentence and a concrete
fact over a claim. Do not describe a feature that the code does not implement.

## Interface changes

- A screen module goes in `internal/ui/js/manifest.txt` and a stylesheet in
  `internal/ui/css/manifest.txt`. Both manifests are complete or the tests fail.
- Every user-visible string comes from `i18n.en.js` and `i18n.fr.js`. The French
  catalog must cover every setting and every API error code.
- Keep domain logic out of the interface. Call the existing route; do not
  duplicate a rule in JavaScript.

## Report a bug

Include the version, the operating system, the screen or route, what you
expected, what happened, and the relevant Logs entry. Remove mail content,
addresses and provider keys before you paste a log.

Security reports go through the private channel on the [security
page](security.md).

## License

The project is licensed under AGPL-3.0-or-later. By contributing, you agree that
your contribution is distributed under the same license. There is no separate
contributor license agreement.
