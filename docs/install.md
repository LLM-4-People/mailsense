---
layout: default
title: Install
---

# Install Mailsense

<!-- nav:start -->
[Documentation](index.md) · [Ask](ask.md) · **Install** · [First run](first-run.md) · [Console](console.md) · [Mailboxes](mailboxes.md) · [Providers](providers.md) · [Learning](learning.md) · [FAQ](faq.md) · [Privacy](privacy.md) · [Security](security.md) · [Troubleshooting](troubleshooting.md) · [Contributing](contributing.md) · <a href="fr/install.html" hreflang="fr" lang="fr" data-lang-switch="fr">Français</a>
<!-- nav:end -->

The release ships a Windows installer; the source also builds on Linux as a
preview with an empty inbox, and that build is not a release download. Mailsense
reads live mail from the classic Outlook profile that is already open on a
Windows machine, and it does not ask for Microsoft credentials.

| Capability | Windows |
| --- | --- |
| Live mail from classic Outlook | Yes |
| Local runtime | Downloaded on first use |
| Installer | Per-user installer, no administrator |

## Windows

1. Download `mailsense-<version>-setup.exe` from the Releases page. The version
   comes from the release tag.
2. Run it. The install is per user and does not require administrator rights.
3. The database and settings live under `%LOCALAPPDATA%\Mailsense`. Uninstalling
   keeps them unless you delete that directory yourself.

Mailsense needs the Microsoft Edge WebView2 runtime. Windows 11 and current
Windows 10 installs already have it. When it is missing, the installer says so
and links to Microsoft's Evergreen runtime page; the app cannot open a window
until that runtime is installed. Builds published from this repository are not
code-signed yet, so SmartScreen may ask you to confirm the publisher.

## Requirements

- A 64-bit machine. 4 GB of RAM is a practical minimum; 8 GB is recommended
  if you run the local model.
- Free disk roughly three times the size of the mailbox you index: the mail,
  the index, and working space during a rebuild. This is guidance; the app
  enforces only its disk ceiling.
- The local runtime and its model add about 342 MB on Windows. The Linux source
  preview uses the same pinned runtime and model, about 328 MB, downloaded once.

## Verify a download

Each release publishes the installer, a `SHA256SUMS` file with the SHA-256 of
every other asset, and one CycloneDX SBOM named
`mailsense-<version>-sbom.cdx.json`. The manifest does not list itself.

On Windows, compare the installer with its `SHA256SUMS` entry by running
`Get-FileHash .\mailsense-<version>-setup.exe` in PowerShell.

Every asset listed in `SHA256SUMS` also has a build provenance attestation
signed by GitHub Actions. With the GitHub CLI:

```bash
gh attestation verify mailsense-<version>-setup.exe --repo LLM-4-People/mailsense
```

The attestation records the workflow, the commit and the tag that produced the
file. It is not a code signature: the installer is not Authenticode-signed, so
Windows may still ask you to confirm the publisher.

## Uninstall

In Windows, open Settings, Apps, Mailsense and choose Uninstall. Your data stays
in `%LOCALAPPDATA%\Mailsense` until you delete it.

<!-- footer:start -->
[Source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/) · [Privacy](privacy.md)
<!-- footer:end -->
