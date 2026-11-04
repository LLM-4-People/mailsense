# Contributing

## Before you start

- Read `docs/ARCHITECTURE.md` for the layering and ownership rules, and
  `docs/DEVELOPMENT.md` for the build, test and script workflow.

## Ground rules

- One owner per fact. If a rule is stated twice, fix it at the lowest choke point
  and delete the copy.
- Every bug fix ships with a test that fails before the fix.
- Every UI string exists in French and English.
- No new dependency without a documented reason and a maintenance check.

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

## Commit messages

`area: imperative summary`, then a body that says what was wrong, what the fix
is, and how it was verified.
