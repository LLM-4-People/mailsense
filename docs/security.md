---
layout: default
title: Security
---

# Security

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <a href="ask.html">Ask</a>
  <a href="install.html">Install</a>
  <a href="first-run.html">First run</a>
  <a href="console.html">Console</a>
  <a href="mailboxes.html">Mailboxes</a>
  <a href="providers.html">Providers</a>
  <a href="learning.html">Learning</a>
  <a href="faq.html">FAQ</a>
  <a href="privacy.html">Privacy</a>
  <span class="doc-index-current" aria-current="page">Security</span>
  <a href="troubleshooting.html">Troubleshooting</a>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

Mailsense is a local desktop app. It reads a mail profile on the same machine,
serves its own interface on loopback, and keeps its data in a directory you
control. This page states what that design does and does not protect.

## The local server

The API listens on `tcp4 127.0.0.1` with an ephemeral port. The listener refuses
to start if the bound address is not IPv4 loopback, so another machine on the
network cannot reach it.

The desktop window receives a launch token in the URL fragment. The script
copies the token to `sessionStorage` and removes it from the address before the
first API call. The token is not written to the DOM, the console or an error
message. Every API request carries it. A local process that can read your
session storage or the process memory is outside this boundary.

## Data at rest

- The mail store, indexes and learned model are a local SQLite database. They are
  not encrypted by the app. Your operating-system account and disk encryption
  are the protection.
- Provider keys on Windows are encrypted with DPAPI. On other platforms the
  secret store is read only and reads `MAILSENSE_SECRET_<NAME>` from the
  environment.
- Logs are local. They redact secrets, and the log owner applies the retention
  you set.

## Runtime integrity

The local runtime is pinned to a fixed release and a fixed model. Downloads
resume by range request and are checked for size and SHA-256. Unpacking writes a
manifest with the hash of every file and promotes the directory atomically.
Before every launch the app verifies the model, the manifest, the executable and
every listed file, and it refuses an unlisted file or link.

## Release integrity

A release asset is built by the release workflow from the tagged commit on
`main`. GitHub stores a signed build provenance attestation for every artifact
listed in `SHA256SUMS`, and
`gh attestation verify <file> --repo LLM-4-People/mailsense` checks it. The
release also publishes one CycloneDX SBOM.

The binaries are not code-signed. Windows may ask you to confirm the publisher.
The attestation proves which workflow built the file; it is not a publisher
certificate.

## Network requests

The app makes these requests, and no others:

1. The pinned runtime and model download.
2. A remote provider call you configured, including automatic triage after a
   sync.
3. A provider model list, when you ask for it.
4. A provider test, when you press Test.

There is no telemetry, no analytics, no update ping and no vendor account.
[Privacy](privacy.md) lists the fields each request can carry.

## Report a vulnerability

Open a private security advisory from the Security tab of the repository. Include
the version, the operating system, the affected route or screen, and a minimal
reproduction. If a private advisory is unavailable, open a public issue that
describes the impact without exploit details, and ask for a private channel.

Supported versions are the latest published release and the current `main`
branch. Please do not include real mail, provider keys or personal data in a
report.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
