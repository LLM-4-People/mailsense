# Mailsense release plan

This file is the single owner of the release goal, the decisions already made,
the current state, and the next actions. `docs/PROGRESS.md` is the chronological
ledger; when the two disagree about what to do next, this file wins.

## Working agreement

- Work in `<repo root>`. The session must not move to another directory.
- Keep the public surface generated where a fact repeats. A hand-copied list,
  asset name, or landing page is a defect.
- Public copy is plain and familiar in English and French: no em dash, no en
  dash, no double hyphen as punctuation, and no stock marketing phrases. The
  rule is enforced by `internal/prose` and the tests that consume it.
- One owner per fact. Extend the existing owner instead of adding a second one.
- Every change records its regression test, a mutation that proves the test
  bites, the gate output, and residual risk.
- The repository is not published until the gate below is fully green.

## Goal

Publish Mailsense as an installable, bilingual, local-first desktop product
under `https://github.com/LLM-4-People/mailsense`, licensed AGPL-3.0-or-later,
with a generated public surface, an installer for Windows and Linux, a first-run
setup, and documentation that a new owner can follow without help.

## Decisions already made

1. **License:** GNU Affero General Public License, version 3 or later. The
   canonical FSF text is installed verbatim in `LICENSE`; `NOTICE` carries the
   copyright and the third-party notices.
2. **Legacy prototype:** removed. The Python prototype, its tests, the benchmark
   campaign documents, and the environment-specific relay tooling are gone.
3. **Environment-specific material:** removed. The module path is
   `github.com/LLM-4-People/mailsense`; employer names, real mailbox data,
   machine names, real client domains, and relay scripts are not part of the
   public tree.
4. **Publication target:** the `LLM-4-People` organization, repository
   `mailsense`, public.
5. **Voice:** plain, specific, non-promotional, in both languages. The
   mechanical guard is `internal/prose`.
6. **Installers:** Inno Setup for Windows (per-user, no admin) and a tarball
   with `packaging/linux/install.sh` for Linux.
7. **Documentation site:** Jekyll from `docs/`, English and French, deployed by
   `.github/workflows/pages.yml`.
8. **First run:** an overlay over the shell, not a router screen. It writes the
   ordinary `ui.setup.done` setting and reuses the mailbox, zone, language and
   provider routes. The predicate is "marker false and no selected mailbox",
   never "no discovered mailbox", because startup discovery pre-fetches stores
   for the wizard.

## State at a glance

### Done and verified

- `LICENSE` is the verbatim AGPL-3.0 text; `NOTICE`, both READMEs,
  `CHANGELOG.md` and this plan state the license.
- The module path is `github.com/LLM-4-People/mailsense`.
- Legacy Python, benchmark documents, relay scripts, environment live scripts,
  root binaries, caches and `.env` are removed.
- The PII substitution is complete: one invented business cast and invented
  people. Business stems, real person names, machine paths and internal vendor
  names return zero hits in the contract greps. Addresses and display labels
  were made consistent after the rename, and the test fixtures were realigned
  to the cast.
- `internal/prose` owns the public-copy rule; the docs test and the UI catalog
  test consume it.
- `docs/site.json` and `internal/site` are the single source and renderer for
  the landing pages, both READMEs and the four brand SVGs. `cmd/gensite` has
  `-write`, `-check` and `-pages`; `make site-check` passes.
- Ten bilingual guide pairs ship: install, first-run, mailboxes, providers,
  learning, privacy, security, troubleshooting, FAQ, contributing. The public
  page list is declared once in `internal/site` and the release tarball reads
  that list.
- The docs match the product: classic Outlook COM on Windows, no Microsoft
  sign-in, Linux preview with no live source, downloaded runtime with Windows
  and Linux sizes, no SQL console, no diagnostics export bundle, local training
  and remote requests stated plainly.
- First-run backend: `ui.setup.done` in the schema, `GET /api/setup`, and tests
  for a fresh install, a marker, an existing selected mailbox, and discovered
  but unselected stores.
- First-run UI: `internal/ui/js/05-setup.js` overlay with welcome (data
  directory, discovered stores, language, zone), mail, model and finish steps;
  a Not now path on the mail step; the chosen language applied before the next
  step paints; French and English catalogs; contract tests for the manifest,
  boot order, route reuse and catalog coverage.
- Windows Inno Setup, Linux tarball installer, CI, release, Pages, CodeQL,
  issue templates, PR template and dependabot exist and parse.
- `scripts/test-fast.sh` and `scripts/test.sh` are green on the final tree.

### Blocked or waiting on the owner

- GitHub organization write access and the final go-ahead to create the public
  repository and push.
- Rotation of the two provider keys that were deleted from `.env`. Deletion
  does not revoke them.
- A real Windows or WebView2 first paint cannot be verified from this Linux
  host.

## Publication gate

- [x] `LICENSE` is AGPL-3.0-or-later, installed from the canonical FSF text.
- [x] Root artifacts removed and `.gitignore` covers them.
- [x] Legacy Python prototype and environment-specific tooling removed.
- [x] Module path renamed to the publication path.
- [x] Ten bilingual guide pairs exist and are linked from the landing pages,
      the READMEs and the site header.
- [x] Installer assets, CI, release, Pages, CodeQL, dependabot and templates
      exist.
- [x] No real personal data or real client domains anywhere in the tree.
- [x] No secrets, tokens, `.env` values or private keys anywhere in the tree.
- [x] `internal/prose` guards public copy in both languages, and the guard's own
      mutation test fails when the rule is removed.
- [x] Generated brand assets, landing data and public page lists have one source
      and a check step.
- [x] `scripts/test-fast.sh` and `scripts/test.sh` are green on the final tree.
- [ ] The repository exists under `LLM-4-People/mailsense`, the first commit is
      pushed on a branch, and CI is green before the public announcement.
- [ ] Pages is enabled and the bilingual site is verified.

## Next actions, in order

1. `git init`, create the first commit on a feature branch, and never push
   directly to `main`.
2. Create `LLM-4-People/mailsense`, push the branch, open a pull request, and
   wait for every required check.
3. Enable Pages, verify the English and French pages and every navigation link,
   then announce.
4. The owner revokes the two deleted provider keys.
5. A Windows machine confirms the WebView2 first paint and the Inno installer.

## Workstreams

### A. Installer and packaging

- [x] `scripts/build.sh` produces version-stamped Windows and Linux binaries.
- [x] Windows Inno Setup script, per user, no admin, data preserved on
      uninstall.
- [x] Linux tarball installer with a desktop entry and XDG data directory.
- [x] The local runtime is fetched on first use, pinned by version and SHA-256,
      verified before every launch, with no offline bundle.
- [x] Unsigned Windows builds are labelled on the install page.
- [ ] Code signing, if the owner wants it.

### B. First-run setup

- [x] One overlay with a basic path: welcome (stores, language, zone), mailbox,
      model, finish (ceilings and background work).
- [x] The advanced path is Settings, with General, Providers and Runtime tabs;
      the model step links to Providers.
- [x] Defaults are shown, not asked: the detected zone, the resource ceilings,
      the shipped profile and taxonomy, and the discovery result.
- [x] The wizard does not pre-fetch a provider catalog or a model list, because
      the app ships no provider and a model list is fetched on request.
- [x] Setup is resumable and idempotent: the marker is the only completion fact,
      and every step is independent.
- [x] Every step has empty, loading, error and success states in both languages
      and uses native keyboard controls.
- [x] Discovered but unselected stores keep setup open.

### C. Console completeness

- [x] Every schema key is reachable from Settings, in its group, with its
      default, current value, layer and reset action.
- [x] Logs, jobs, triage, learning, providers, runtime and storage controls are
      reachable.
- [x] Groups filter and the search query persists.
- [x] An empty state links to the exact setup step that fixes it.
- [x] There is no SQL query console and no diagnostics export bundle; the docs
      say so instead of advertising them.

### D. Documentation and site

- [x] English and French landing pages plus ten guide pairs.
- [x] `docs/SELLING.md` for positioning, limits and objections.
- [x] The privacy page states local training, remote request shapes, automatic
      triage and the absence of telemetry.
- [ ] Screenshots from a clean first run, with no real mail.

### E. GitHub automation

- [x] CI runs gofmt, vet and the fast gate.
- [x] The race matrix runs on a schedule and by hand.
- [x] The Playwright suite runs on UI changes with artifacts on failure.
- [x] The release workflow builds both targets, publishes checksums and copies
      the public page list from `internal/site`.
- [x] Pages deploys `docs/`.
- [x] Issue templates, PR template, dependabot and CodeQL.

## Verification per round

Record the change, the regression test, the mutation that proves the test bites,
the gate output, and the residual risk. See `docs/PROGRESS.md`.

## Evidence log

Round 121 (release scaffold): `go build ./...` exit 0; every workflow YAML
parsed; `bash -n packaging/linux/install.sh` clean; the relative-link check over
the public pages found no broken link; root artifacts removed; `.env` deleted.

Round 122 (license and voice): the canonical AGPL-3.0 text was installed as
`LICENSE`; `NOTICE`, both READMEs, `CHANGELOG.md` and this plan were updated; the
public em dashes were fixed; `internal/prose` became the one owner of the
public-copy rules.

Round 123 (PII and generated surface): the invented cast replaced the remaining
client domains and people; the residue greps return zero for old business stems,
real names, machine paths and internal vendors. `docs/site.json`,
`internal/site`, `cmd/gensite` and `make site-check` became the generated public
surface. The two failing `internal/qa` suites from the rename were realigned to
the cast and pass.

Round 124 (first run and docs): `ui.setup.done`, `GET /api/setup`, the overlay
wizard and its catalog and contract tests landed. The predicate was corrected to
count selected, not discovered, mailboxes after the first-run discovery
regression was reproduced. The privacy, install, first-run and troubleshooting
pages were corrected to the real Outlook, Linux, runtime and training behavior,
and the missing bilingual guides were added.

Round 125 (final gate): `scripts/test-fast.sh` exit 0 and `scripts/test.sh` exit
0 on the final tree, after the language repaint fix. The setup predicate
regression, the new bilingual guides and the PII contract greps are recorded in
the newest `docs/PROGRESS.md` entry.

## Residual risks

- The deleted `.env` keys are still valid until the owner revokes them.
- Linux has no production mail connector and its secret store is read-only.
- The first-run overlay has no focus trap; it uses native controls and a
  background that is hidden from the accessibility tree, but a full trap is not
  implemented.
- Provider setup is fully manual; there are no presets.
- `internal/source/rfc5322` and `internal/source/archive` exist but are not
  registered in the shipped app.
- WebView2 first paint is unverified on this host.
- The deployed build predates the release work.
