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

## Workflow

1. `scripts/test-fast.sh` before every commit.
2. `scripts/test-ui.sh` for any UI change.
3. `scripts/test.sh` and `scripts/test-race.sh` before a release.
4. Keep the change focused; unrelated cleanup goes in its own change.

## Commit messages

`area: imperative summary`, then a body that says what was wrong, what the fix
is, and how it was verified.
