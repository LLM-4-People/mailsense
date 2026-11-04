# Changelog

All notable changes to Mailsense are recorded here. The format follows Keep a
Changelog, and the project uses semantic versioning once a first stable release
exists.

## Unreleased

### Added
- The project is released under the GNU Affero General Public License, version
  3 or later.
- First-run setup with a basic and an advanced path, resumable and idempotent.
  It pre-fetches the data directory, database size, zone, language, sources,
  stores, providers, runtime, learning stage and held removals, and each read
  fails independently with its own retry.
- Installer assets: a per-user Windows Inno Setup script and a Linux
  `install.sh` with a desktop entry. One version owner stamps the binaries,
  the installer name, the tarball name and the About surface.
- A generated bilingual site with a restrained palette, system fonts only,
  accessible brand art, and a ranked-queue illustration built from invented
  senders.
- Bilingual documentation site (English and French) deployed from `docs/`.
- GitHub automation: CI, race matrix, UI suite, CodeQL, releases with checksums,
  Pages, dependabot, issue and pull-request templates.

### Changed
- CI now verifies that the committed generated UI bundles match their sources,
  and a failed gate stage names the command that reruns only that stage.
- The Linux tarball is built by `packaging/linux/package.sh`, ships its own
  checksum and the generated brand assets, and the release workflow calls it
  instead of rebuilding the payload inline.
- GitHub automation now covers actionlint, a generated-site link crawl, pull
  request titles and sizes, path labels, release drafting, OSSF Scorecard, Go
  dependency submission, first-contribution guidance and a stale policy that
  never closes a pull request.
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
