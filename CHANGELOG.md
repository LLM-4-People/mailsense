# Changelog

All notable changes to Mailsense are recorded here. The format follows Keep a
Changelog, and the project uses semantic versioning once a first stable release
exists.

## Unreleased

### Added
- Setup probes the mail source before offering it. A source that cannot run
  (Outlook not open, a COM failure, or a configured profile that does not
  match the open one) is disabled with its cause, and a Start Outlook / Check
  again path lets the owner fix it.
- The source label comes from the configured `outlook.display_name`; the new
  `outlook.profile` setting selects the classic Outlook profile to verify.
  Classic Outlook uses one profile per running process, so the app reports a
  mismatch and starts the configured profile only after Outlook is closed.
- The job strip names the source and phase of a sync (`Mail sync · Outlook`,
  `Reading folders`), and its cancel action names the task it stops. The
  Sources backfill cancel says which source it stops too.
- Mailsense answers from **definitional** evidence rules (structure or
  protocol, never a wordlist or an unmeasured estimate) when the learned
  route has not certified a question. The answer names its rule, says it is
  uncertified, and is never written as a label, counted as coverage or used
  to open the gate. A first-run install also trains, reports status and
  chooses a teacher without a configured paid decider.
- A Settings → Updates tab shows the installed and latest versions, the last
  check, the release notes, **Check now** and **Skip this version**; the
  first open that detects a release opens one What's New dialog and marks the
  notes seen, and a small dot on Settings keeps the offer discoverable.
- Ask leads the public surface: both landings, both READMEs and the selling
  guide open with a question answered from local mail, the screen table leads
  with the Ask capture, and a new bilingual guide covers the three modes,
  grounding, citations, the provider request and the limits.
- The project is released under the GNU Affero General Public License, version
  3 or later.
- First-run setup with a basic and an advanced path, resumable and idempotent.
  It pre-fetches the data directory, database size, zone, language, sources,
  stores, providers, runtime, learning stage and held removals, and each read
  fails independently with its own retry.
- Installer assets: a per-user Windows Inno Setup script. One version owner
  stamps the binary, the installer name and the About surface.
- A generated bilingual site with a restrained palette, system fonts only,
  accessible brand art, and a ranked-queue illustration built from invented
  senders.
- Bilingual documentation site (English and French) deployed from `docs/`.
- GitHub automation: CI, race matrix, UI suite, CodeQL, releases with checksums,
  Pages, dependabot, issue and pull-request templates.
- Ask answers a count of one topic from local classified mail, names the topic
  and subtopic it counted, and discloses the classification coverage behind a
  zero.

### Changed
- The release ships the Windows installer only. The source still builds on
  Linux as a preview with an empty inbox, and that build is not a release
  download.
- CI now verifies that the committed generated UI bundles match their sources,
  and a failed gate stage names the command that reruns only that stage.
- A merge to `main` releases automatically: semantic-release reads the
  conventional commits, creates the next semantic-version tag and GitHub
  release only after the fast and exhaustive gates pass, then dispatches the
  release build that attaches the Windows installer, SBOM and checksums.
  Release Drafter is retired, so commits are the single version owner.
- GitHub automation now covers actionlint, a generated-site link crawl, pull
  request titles and sizes, path labels, automatic semantic releases, OSSF
  Scorecard, Go dependency submission, first-contribution guidance and a stale
  policy that never closes a pull request.
- CI runs the lint and vet half, the test half and the sidecar suite on
  separate runners, cross-builds the Windows binary on Linux, and analyzes Go
  and JavaScript in parallel. The fast gate went from about six minutes to
  about two.
- Public voice and personal-data rules are mechanical. The gate rejects em
  dashes, spaced en dashes, double hyphen punctuation, stock phrases,
  title-case headings, rhetorical transitions and environment, client, machine
  or vendor residue.
- One diagnostic disclosure component for every screen.
- One retrieval-degradation classifier, so a warming index is reported as
  warming on the topic lane too.
- One role-mailbox vocabulary for people and company contacts.

### Removed
- Internal design notes that carried private environment and vendor details are
  not part of the public tree. The facts a contributor needs remain in code
  comments, tests and the public guides.
- Real vendor and environment names are replaced with the invented cast across
  code, comments, fixtures and the remaining internal notes.

### Fixed
- Undated messages are excluded from a search date window and counted in a
  ranked mail window according to the declared policy.
- The company book reads the address book once per rebuild instead of twice.
