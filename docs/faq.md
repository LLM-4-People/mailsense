---
layout: default
title: FAQ
---

# FAQ

<!-- nav:start -->
[Documentation](index.md) · [Ask](ask.md) · [Install](install.md) · [First run](first-run.md) · [Console](console.md) · [Mailboxes](mailboxes.md) · [Providers](providers.md) · [Learning](learning.md) · **FAQ** · [Privacy](privacy.md) · [Security](security.md) · [Troubleshooting](troubleshooting.md) · [Contributing](contributing.md) · <a href="fr/faq.html" hreflang="fr" lang="fr" data-lang-switch="fr">Français</a>
<!-- nav:end -->

## Does Mailsense send my mail to a cloud service?

Not by default. The live source is the classic Outlook profile on this Windows
machine, and the store, the indexes and the classifier are local. A request
leaves the machine only when you configure a remote provider and a call runs, or
when the local runtime downloads its pinned artifacts. See
[privacy](privacy.md).

## Does it work with Gmail, IMAP or Microsoft 365 directly?

Not in this build. The only live source is classic Outlook on Windows through
COM. New Outlook, Outlook on the web, IMAP, POP, the Gmail API and Microsoft
Graph are not connected.

## Why is the inbox empty on Linux?

The Linux build is a preview. It opens the window, renders the screens and
explains the missing source, but it has no live mail connector. The inbox stays
empty by design.

## Do I need a model to use it?

Triage needs a decider, and answers `422 provider_not_configured` without one.
Search works without a decider. The local classifier still learns from your
answers because it runs in the app. Semantic search needs an embedder, which the
local runtime can provide.

## Where is my data?

The status bar shows the data directory. On Windows it is
`%LOCALAPPDATA%\Mailsense`; on Linux it is
`${XDG_DATA_HOME:-~/.local/share}/mailsense`. Set `MAILSENSE_DATA_DIR` before
launch to use another directory.

## How do I delete my data?

Close Mailsense and delete the data directory. That removes the mail store, the
indexes, the settings, the learned model and the logs. Uninstalling the app does
not delete it.

## How do I see the first-run screen again?

Set `ui.setup.done` to false in Settings, or reset that setting. Setup opens on
the next start when the marker is false and no mailbox is picked.

## Why does setup open again?

The setup screen opens when `ui.setup.done` is false and no mailbox is selected.
Finish the screen, pick a store in Sources, or set the marker back to true.

## Can it send, delete or move my messages?

No. The shipped connector reads the Outlook profile. It does not send, delete,
move messages, or change their read state.

## Does it work offline?

Local screens, full-text search, the local classifier and the local runtime work
without a network once the runtime is installed. The runtime download, provider
tests, model lists and remote provider calls need a network.

## Is it free?

The source is published under AGPL-3.0-or-later. This repository has no account,
no paid tier and no vendor backend. A remote provider you configure bills you
under its own terms.

<!-- footer:start -->
[Source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/) · [Privacy](privacy.md)
<!-- footer:end -->
