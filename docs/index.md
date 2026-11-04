---
layout: default
title: Mailsense
lang: en
---

# Mailsense

**Your mailbox, sorted. Every decision explained. Your mail is stored on your
machine; the local analysis runs there; a remote model is used only when you
configure one.**

Mailsense reads the mail already in your classic Outlook profile on Windows,
tells you what each message is, what it asks for, and what deserves your
attention first. It shows the reason for every verdict, learns from your
corrections, and keeps the whole record in a local database you can inspect. The
Linux build is a preview without a live mail source.

[Français](fr/) · [Install](install.md) · [First run](first-run.md) · [Mailboxes](mailboxes.md) · [Providers](providers.md) · [Learning](learning.md) · [FAQ](faq.md) · [Privacy](privacy.md) · [Security](security.md) · [Troubleshooting](troubleshooting.md) · [Contributing](contributing.md)

![Mailsense](assets/banner-en.svg)

## What it does

- **Triage you can audit.** Category, topic, the ask, reply obligation, urgency,
  deadline, sentiment, phishing suspicion, and the evidence behind each one.
- **A ranked inbox.** Reply, decide, pay, schedule, or read, ordered by what the
  message actually asks of you, with the reason on the row.
- **A mailbox that gets better.** Your corrections become examples; reply
  behaviour becomes priors.
- **Thread truth.** Who spoke last, whether the owner owes a reply, and which
  thread is still open.
- **Companies and people.** Declared contacts merged with what mail observed.
- **Ask in plain language.** Structured counts and lists first; a model-written
  summary only when you configured one, always with citations.
- **Local by default.** SQLite on disk, local embeddings downloaded once,
  configurable CPU, memory and disk ceilings, and background work that pauses at
  the ceiling instead of fighting the machine.

## Why it is different

Most assistants hide the reasoning. Mailsense treats every label as a claim with
a source: the profile that produced it, the message fields it read, and the
confidence it carries. When the mailbox has not finished indexing, it says so
instead of pretending the answer is complete.

## What runs where

| Part | Default | Optional |
| --- | --- | --- |
| Live mail source | Classic Outlook on Windows | None on Linux yet |
| Mail store and indexes | Local SQLite | A chosen data directory |
| Semantic embeddings | Downloaded once on first use (about 342 MB on Windows, 328 MB on Linux) | A remote embedding provider you configure |
| Message classification | Local Go classifier; triage needs a decider | The decider you choose, including a local OpenAI-compatible endpoint |
| Written answers | Off unless configured | The writing provider you choose |

Mailsense reads the classic Outlook profile already open on Windows; it does not
ask for Microsoft credentials. A remote provider receives the message fields a
request needs, and only when that request runs. See [privacy](privacy.md).

## Get started

1. [Install Mailsense](install.md) for Windows. The Linux build is a preview
   without a live mail source.
2. Follow the [first run](first-run.md): a short basic path, and an advanced
   path when you want to control providers, storage and resources.
3. Read the [privacy model](privacy.md) in one page.

## Status

Mailsense is in active development. Interfaces and stored schemas may change
between releases; the changelog and the release notes call out anything that
requires a migration.

[Source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later
