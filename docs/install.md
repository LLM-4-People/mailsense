---
layout: default
title: Install
---

# Install Mailsense

<!-- nav:start -->
[Documentation](index.md) · **Install** · [First run](first-run.md) · [Console](console.md) · [Mailboxes](mailboxes.md) · [Providers](providers.md) · [Learning](learning.md) · [FAQ](faq.md) · [Privacy](privacy.md) · [Security](security.md) · [Troubleshooting](troubleshooting.md) · [Contributing](contributing.md) · <a href="fr/install.html" hreflang="fr" lang="fr" data-lang-switch="fr">Français</a>
<!-- nav:end -->

Mailsense reads live mail from the classic Outlook profile that is already open
on a Windows machine. It does not ask for Microsoft credentials and it has no
cloud account. The Linux build is a preview: it starts, shows the interface, and
opens Settings, but it has no live mail source yet.

| Capability | Windows | Linux |
| --- | --- | --- |
| Live mail from classic Outlook | Yes | No, preview only |
| Local runtime | Downloaded on first use | Downloaded on first use |
| Installer | Per-user installer | Tarball with `install.sh` |

## Windows

1. Download the installer from the Releases page.
2. Run it. The install is per user and does not require administrator rights.
3. The database and settings live under `%LOCALAPPDATA%\Mailsense`. Uninstalling
   keeps them unless you delete that directory yourself.

Mailsense needs the Microsoft Edge WebView2 runtime. Windows 11 and current
Windows 10 installs already have it. When it is missing, the installer says so
and links to Microsoft's Evergreen runtime page; the app cannot open a window
until that runtime is installed. Builds published from this repository are not
code-signed yet, so Windows may ask you to confirm the publisher.

## Linux

1. Download the tarball for `amd64`.
2. Extract it and run `./mailsense`, or run `./install.sh` for a menu entry. The
   data directory is `${XDG_DATA_HOME:-~/.local/share}/mailsense`.
3. Live mail is not available on Linux yet. The build is a preview of the
   interface and the local data model.

The data directory is chosen at launch. Set `MAILSENSE_DATA_DIR` before starting
the app to use another directory; it is not a field in Settings.

## Requirements

- A 64-bit machine. 4 GB of RAM is a practical minimum; 8 GB is recommended
  if you run the local model.
- Free disk roughly three times the size of the mailbox you index: the mail,
  the index, and working space during a rebuild. This is guidance; the app
  enforces only its disk ceiling.
- The local runtime and its model add about 342 MB on Windows and 328 MB on
  Linux, downloaded once.

## Verify a download

Each release publishes the assets, a `SHA256SUMS` file with the SHA-256 of every
other asset, and one CycloneDX SBOM named
`mailsense-<version>-sbom.cdx.json`. The manifest does not list itself.

On Linux, from the directory that holds the downloaded files:

```bash
sha256sum -c SHA256SUMS --ignore-missing
```

`--ignore-missing` verifies only the files you downloaded. Omit it to require
every file in the manifest.

On Windows, compare a file with `Get-FileHash` in PowerShell.

Every artifact listed in `SHA256SUMS` also has a build provenance attestation
signed by GitHub Actions. With the GitHub CLI:

```bash
gh attestation verify mailsense-<version>-linux-amd64.tar.gz --repo LLM-4-People/mailsense
```

The attestation records the workflow, the commit and the tag that produced the
file. It is not a code signature: the binaries are not Authenticode-signed, so
Windows may still ask you to confirm the publisher.

## Uninstall

- Windows: Settings, Apps, Mailsense, Uninstall. Your data stays in
  `%LOCALAPPDATA%\Mailsense` until you delete it.
- Linux: remove the binary and the desktop entry. The data directory is
  `${XDG_DATA_HOME:-~/.local/share}/mailsense`.

<!-- footer:start -->
[Source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/) · [Privacy](privacy.md)
<!-- footer:end -->
