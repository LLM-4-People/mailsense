# Contributing

## Before you start

Read `docs/ARCHITECTURE.md` for the layering and ownership rules and
`docs/DEVELOPMENT.md` for the build, test and script workflow. Participation is
covered by the [code of conduct](CODE_OF_CONDUCT.md).

## Ground rules

- One owner per fact. If a rule is stated twice, fix it at the lowest choke point
  and delete the copy.
- Every bug fix ships with a test that fails before the fix.
- Every UI string exists in French and English.
- No new dependency without a documented reason and a maintenance check.

## Requirements

- Go 1.27 or newer.
- Node.js for the interface behavior tests. Those tests skip when Node is
  missing; the rest still run.
- A C toolchain for the race detector. `scripts/test.sh` turns race on when cgo
  works.

## Checks before a pull request

Run these from the repository root, in this order:

1. `scripts/test-fast.sh` runs gofmt, the script syntax check, the generated UI
   bundle check, the vet matrix, and the fast test shards.
2. `scripts/check-generated.sh` regenerates `internal/ui/app.js` and
   `internal/ui/app.css` and fails when either committed file is stale. The fast
   gate runs it too.
3. `make site-check` fails when a page or brand asset generated from
   `docs/site.json` is stale.
4. `scripts/test-ui.sh` runs the Playwright suite for any change under
   `internal/ui` or `e2e`.

`scripts/test.sh` and `scripts/test-race.sh` are the exhaustive release gates.
Run them before a release, not on every commit.

When a stage fails, the gate prints `error: <stage> failed` and the exact command
that reruns only that stage.

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
- After changing any file under `internal/ui/js` or `internal/ui/css`, regenerate
  the served bundles with `go generate ./internal/ui`. The bundle tests fail when
  the generated `app.js` or `app.css` is stale.

## Commit messages

`type(scope): imperative summary`, then a body that says what was wrong, what
the fix is, and how it was verified. The checked types are `feat`, `fix`,
`docs`, `test`, `refactor`, `release`, `build` and `ci`, with an optional scope
and an optional `!` for a breaking change. The authoritative list is the
`ALLOWED_TYPES` value in `.github/workflows/pr-title.yml`.

## Developer certificate of origin

Every commit must carry a `Signed-off-by` line. This repository requires web
commit signoff, and the GitHub web editor adds the line for you. For a local
commit, run:

```bash
git commit -s
```

The sign-off certifies the Developer Certificate of Origin 1.1: you wrote the
change, or you have the right to submit it, under AGPL-3.0-or-later. There is no
separate contributor license agreement.

## Report a bug

Include the version, the operating system, the screen or route, what you
expected, what happened, and the relevant Logs entry. Remove mail content,
addresses and provider keys before you paste a log. Security reports go through
the private channel in [SECURITY.md](SECURITY.md).

## License

The project is licensed under AGPL-3.0-or-later. By contributing, you agree that
your contribution is distributed under the same license.
