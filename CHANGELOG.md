# Changelog

All notable changes to Mailsense are recorded here. The format follows Keep a
Changelog, and the project uses semantic versioning once a first stable release
exists.

## Unreleased

### Added
- The project is released under the GNU Affero General Public License, version
  3 or later.
- First-run setup with a basic and an advanced path, resumable and idempotent.
- Installer assets: a per-user Windows Inno Setup script and a Linux
  `install.sh` with a desktop entry.
- Bilingual documentation site (English and French) deployed from `docs/`.
- GitHub automation: CI, race matrix, UI suite, CodeQL, releases with checksums,
  Pages, dependabot, issue and pull-request templates.

### Changed
- CI now verifies that the committed generated UI bundles match their sources,
  and a failed gate stage names the command that reruns only that stage.
- One diagnostic disclosure component for every screen.
- One retrieval-degradation classifier, so a warming index is reported as
  warming on the topic lane too.
- One role-mailbox vocabulary for people and company contacts.

### Fixed
- Undated messages are excluded from a search date window and counted in a
  ranked mail window according to the declared policy.
- The company book reads the address book once per rebuild instead of twice.
