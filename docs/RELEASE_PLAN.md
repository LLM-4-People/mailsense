# Mailsense release plan

This file is the single owner of the release goal, the decisions already made,
the current state, and the next actions; when they disagree about what to do
next, this file wins.

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

## Current status, 2026-11-04

- Repository `LLM-4-People/mailsense` exists and is public. Pull request 1
  carries the whole tree from the initial `main` commit.
- Branch head is `dddda18`. The public voice and personal-data gates pass on
  1,784 tracked files.
- The 19 CodeQL `go/clear-text-logging` alerts are fixed at their logging
  sinks through `logx.RedactedError`, and the analysis now reports zero open
  alerts. All 19 review threads are resolved, so the ruleset blocker is gone.
- Every required check passes. The fast gate is 58 seconds on a warm cache:
  lint and vet 30 s, the four test shards 33 to 46 s each, the sidecar suite
  50 s and the windows cross-build 21 s. CodeQL is about 2 minutes and is the
  remaining pole.
- The fast gate briefly failed on a stale `docs/_config.yml`. The committed
  file came from a generator change that was still uncommitted, so the
  committed generator emitted a different `url`. Both are committed together
  now and `go run ./cmd/gensite -check` passes.
- Pages is enabled with `build_type=workflow`, but the workflow has never run
  from `main`, so the site is not live yet.
- There are no tags and no releases. The release workflow, installer, SBOM,
  checksum verification and action pinning are prepared but have never run.
- A Windows build from `7e733cc` was installed and launched on
  a Windows test machine. The WebView2 window renders the shipped UI. This closes the
  unverified first-paint item for that machine only.
- The repository now enforces secret scanning, push protection, Dependabot
  alerts, private vulnerability reporting, SHA-pinned actions, a `main`
  ruleset with required checks, and a release-tag ruleset.
- Uncommitted work on a security hardening pass, dependency updates and
  migration tests was discarded on the owner's instruction. Only the commits
  pushed to `release/publication` remain.

## Decisions already made

1. **License:** GNU Affero General Public License, version 3 or later. The
   canonical FSF text is installed verbatim in `LICENSE`; `NOTICE` carries the
   copyright and the third-party notices.
2. **Legacy prototype:** removed. The Python prototype, its tests, the benchmark
   campaign documents, and the environment-specific tooling are gone.
3. **Environment-specific material:** removed. The module path is
   `github.com/LLM-4-People/mailsense`; employer names, real mailbox data,
   machine names, real client domains, and environment-specific scripts are
   not part of the public tree.
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
- Legacy Python, benchmark documents, environment-specific scripts,
  environment live scripts, root binaries, caches and `.env` are removed.
- The PII substitution is complete: one invented business cast and invented
  people. Business stems, real person names, machine paths and internal vendor
  names return zero hits in the contract greps. Addresses and display labels
  were made consistent after the rename, and the test fixtures were realigned
  to the cast.
- `internal/prose` owns the public-copy rule; the docs test and the UI catalog
  test consume it.
- `docs/site.json` and `internal/site` are the single source and renderer for
  the landing pages, both READMEs and the four brand SVGs. One ordered page
  table derives the navigation, the release page list, the documentation links
  and every generated output, so a missing English or French page fails the
  check. `cmd/gensite` has `-write`, `-check` and `-pages`; `make
  site-check` passes.
- Every bilingual guide pair declared in `internal/site` ships, and the release
  tarball reads that same page list. The pages cover install, first run,
  mailboxes, providers, learning, privacy, security, troubleshooting, FAQ and
  contributing.
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

- Nothing blocks the merge any more. The ruleset requirement is satisfied:
  every required check passes and no review thread is open.
- Pages is enabled but the first deployment still waits for the merge.
- The Windows installer has not been compiled or installed. Only the portable
  binary was verified on a Windows test machine.
- The owner is revoking the two provider keys that were removed from `.env`.
  No copy of them remains in the tree.

## Publication gate

- [x] `LICENSE` is AGPL-3.0-or-later, installed from the canonical FSF text.
- [x] Root artifacts removed and `.gitignore` covers them.
- [x] Legacy Python prototype and environment-specific tooling removed.
- [x] Module path renamed to the publication path.
- [x] Every bilingual guide pair declared in `internal/site` exists and is
      linked from the landing pages, the READMEs and the site header.
- [x] Installer assets, CI, release, Pages, CodeQL, dependabot and templates
      exist.
- [x] No real personal data or real client domains anywhere in the tree.
- [x] No secrets, tokens, `.env` values or private keys anywhere in the tree.
- [x] `internal/prose` guards public copy in both languages, and the guard's own
      mutation test fails when the rule is removed.
- [x] Generated brand assets, landing data and public page lists have one source
      and a check step.
- [x] `scripts/test-fast.sh` and `scripts/test.sh` are green on the final tree.
- [x] The repository exists under `LLM-4-People/mailsense`, the first commit is
      pushed on a branch, and every required check passes.
- [x] The CodeQL alerts are fixed, every review thread is resolved and the
      analysis reports zero open alerts.
- [ ] Pull request 1 is merged.
- [ ] Pages is enabled and the bilingual site is verified.
- [ ] A tagged release publishes the installer, the tarball, the SBOM and the
      checksum manifest, and the installer is verified on Windows.

## Next actions, in order

1. Merge pull request 1 into `main` once the checks on the final commit are
   green.
2. Watch the Pages workflow on `main`, then verify the English and French
   landing pages, every guide page, both READMEs and every navigation link.
3. Create the first version tag and verify the release assets: Windows
   installer, Linux tarball, SBOM, checksums and provenance attestation.
4. Install the Windows package on a Windows test machine and run the first-run
   setup from a clean data directory.
5. Announce.
6. The owner revokes the two removed provider keys.

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

### C. Public release polish

- [x] Sweep every tracked file for personal data, real client names, machine
      names, mailbox addresses, internal vendor names and environment paths.
      The patterns live in `scripts/pii-patterns.tsv` and
      `scripts/check-pii.sh` runs them on every fast and exhaustive gate.
- [x] Remove all environment-specific references and documentation, including
      the private live harness and the three notes that only recorded private
      history.
- [x] Extend `internal/prose` to reject em dashes, spaced en dashes, double
      hyphen punctuation, stock phrases, title-case headings, rhetorical
      transitions and a closing restatement in every public English and French
      file. The console catalogs reuse the same owner.
- [x] Rewrite the landing pages, READMEs, install pages and selling speech in a
      plain, familiar voice in both languages. No literal translation.
- [x] Add screenshots from a clean first run with invented mail only.
- [x] Confirm the repository description, topics, homepage and landing links
      are set once and generated where possible.

### C2. Console scope

The console UX workstream is out of scope for this release. The owner confirmed
that the console direction belonged to another project. Do not spend rounds on
navigation, settings layout or new console screens. Only fix a console defect
that blocks the first-run setup, the installer or a documented public flow.

### D. Documentation and site

- [x] English and French landing pages plus ten guide pairs.
- [x] `docs/SELLING.md` for positioning, limits and objections.
- [x] The privacy page states local training, remote request shapes, automatic
      triage and the absence of telemetry.
- [x] Screenshots from a clean first run, with no real mail. Eight screens in
      both languages come from the Linux preview with a fixed clock, and a
      truth test fails when a listed image is missing or has the wrong size.
- [x] A 1200x630 social preview generated from the brand banner, wired through
      the theme's existing SEO include.

### E. GitHub automation

- [x] CI runs gofmt, vet and the fast gate.
- [x] The race matrix runs on a schedule and by hand.
- [x] The Playwright suite runs on UI changes with artifacts on failure.
- [x] The release workflow builds both targets, publishes checksums, attaches
      an SBOM, attests provenance and copies the public page list from
      `internal/site`.
- [ ] The release workflow has actually run on a tag and its assets are
      verified.
- [x] Pages is configured to deploy `docs/` from `main`.
- [ ] The first Pages deployment has run and the live site is verified.
- [x] Issue templates, PR template, dependabot, CodeQL, secret scanning, push
      protection, Dependabot alerts, private reporting, a `main` ruleset and
      SHA-pinned actions.
- [ ] The repository description, topics and social preview are set from one
      generated source.

## Verification per round

Record the change, the regression test, the mutation that proves the test bites,
the gate output, and the residual risk in the round's entry below.

## Recent rounds

Newest first. The older evidence log below keeps its original chronological
order.

Round 133 (CI speed): the fast gate went from six minutes twenty-five to
fifty-eight seconds on a warm cache. The gate is now the lint and vet job, four
test shard jobs, the sidecar job and a joining job; every job keeps its own Go
module and build cache through one composite action, because a GitHub cache is
immutable once written. Vet runs one host pass plus the packages that declare
each tag, instead of ten full module builds. The windows job cross-compiles the
shipped binary on Linux, which the tree allows because it has no cgo and no
assembly. CodeQL analyzes Go and JavaScript in parallel and stays at about two
minutes, so it is the remaining pole. The Companies cache test now owns the
event stream like its two siblings, which removes the intermittent failure.

Round 132 (publication ready): the nineteen clear-text-logging flows are
projected through `logx.RedactedError` at every sink, the analysis reports zero
open alerts and the review threads are resolved. The personal-data scan passes
on 1,784 tracked files with 105 deny patterns, and the voice guard covers the
new prohibitions. The installer has one version owner and a local verification
script; the release workflow calls `packaging/linux/package.sh`. Ten bounded
automation workflows are pinned and least-privilege. Eight documented screens
in both languages and a 1200x630 social preview ship with truth tests. The
fast gate failed once on a stale `docs/_config.yml` because the generator
change was not committed beside its output; both are committed together now.
Next: merge, watch Pages, verify every page and link, tag `v0.1.0`, verify the
release assets, install on Windows and announce.

Round 131 (publication state): pull request 1 is open from
`release/publication` at `7e733cc`. Fast gate, windows build, vulnerability
scan, dependency review, CodeQL analysis and Playwright pass. Merge is blocked
only by the ruleset requirement to resolve every review thread: 19 CodeQL
`go/clear-text-logging` threads remain open. Pages is enabled but has not run
from `main`. There are no tags or releases. A Windows build from `7e733cc` was
installed and verified on a Windows test machine, which closes the first-paint item for
that machine. Uncommitted exploratory work was discarded on the owner's
instruction, so the branch is the only source of truth. Next: fix the 19 alerts,
resolve the threads, merge, deploy Pages, tag a release and install the package.

Round 130 (API projection owners): settings read one snapshot and one key
guard; provider roles resolve once in `engine.RoleStatuses`; `InvalidProviderID`,
`requirePicked` and `startedJob` own their shapes; `mapError` owns the
unknown-company and commitment facet mappings, so keyed company routes answer
`404 unknown_company` through the shared mapper instead of a drifted 500.
Evidence at the committed tree: `go test ./internal/api ./internal/engine
-count=1` passes.

Round 129 (grouped navigation): the console sidebar is five collapsible native
groups, Answers is a first-class destination under Ask, and the small-screen
drawer uses the same destinations. `internal/ui/nav_contract_test.go` pins the
groups and destination order, and `e2e/specs/nav.spec.ts` drives the browser
behavior. Evidence at the committed tree: `go test ./internal/ui -run
TestNavContract -count=1` and `go test ./internal/ui -count=1` pass.

Round 128 (generated bundle gate): `scripts/check-generated.sh` regenerates the
committed UI bundle and fails on drift with the stale file and repair command;
`scripts/test-lib.sh` names every failed stage and its rerun command; CI runs the
site check, caches Playwright browsers, uploads failure artifacts and cancels
only superseded pull request runs. Evidence at the committed tree: `bash
scripts/check-generated.sh` reports that the bundles match, and `bash -n
scripts/*.sh` is clean.

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
regression, the new bilingual guides and the PII contract greps were verified
in that final gate.

Round 126 (CI round): the first hosted run exposed a first-paint regression in
the E2E harness, one reachable `x/net` advisory and a cluster of CodeQL
secret-name flows. The shared E2E server now sets the setup marker; the setup
spec starts its own server with the variable unset so the marker can persist.
`x/net` is `v0.62.0` and `go.mod` declares `go 1.27.3`; `govulncheck ./...`
reports no vulnerabilities. Provider error text no longer contains the secret
name, and the two narrowing conversions are bounded. The full Playwright suite
passes: 180 tests. The final fast and exhaustive gates were rerun after these
changes.

Round 127 (job strip): repeated source failures are one incident with one
action. The job payload carries a stable cause, and the console translates it
instead of printing the Go wrapper chain. The French action no longer reads as
"close Outlook". The manager and grouping contracts have tests; the fast gate
and the live/resilience Playwright specs pass.

## Residual risks

- `logx.Redact` is policy-based, not encryption: a secret that is neither
  registered nor matching a built-in pattern survives inside an error body.
  The nineteen flagged paths carried the secret-store name, never a value.
- `internal/bootstrap.Report` writes startup errors to a file and a native
  dialog without the redaction projection. That text contains a secret name
  only today, but it is the one startup surface that bypasses the projection.
- Pages has never deployed from `main`; the first run can still fail on a
  repository setting or a missing environment.
- The release workflow and the Windows installer have never run on a tag. The
  portable binary works, the package does not yet.
- The removed provider keys remain valid until the owner completes the
  revocation.
- Linux has no production mail connector and its secret store is read-only.
- The first-run overlay has no focus trap; it uses native controls and a
  background that is hidden from the accessibility tree, but a full trap is not
  implemented.
- Provider setup is fully manual; there are no presets.
- `internal/source/rfc5322` and `internal/source/archive` exist but are not
  registered in the shipped app.
- The public-copy guard covers the documentation and the catalogs, but not
  every generated report or commit message.
- WebView2 first paint is unverified on this host.
- The deployed build predates the release work.
- CodeQL keeps two design-intended findings visible: the generic filesystem
  choke points (`fsx`, `digest`) and the user-configured provider base URL.
  They are not dismissed silently; the reason is in the CI round entry.
