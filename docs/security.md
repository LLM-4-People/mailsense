---
layout: default
title: Security
---

# Security

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

The release is not code-signed yet. Windows may ask you to confirm the
publisher. Integrity comes from the pinned hashes, not from a signature.

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
