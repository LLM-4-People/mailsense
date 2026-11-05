---
layout: default
title: FAQ
---

# FAQ

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
  <span class="doc-index-current" aria-current="page">FAQ</span>
  <a href="privacy.html">Privacy</a>
  <a href="security.html">Security</a>
  <a href="troubleshooting.html">Troubleshooting</a>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
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

## Does it run on Linux?

The release ships a Windows installer; the source also builds on Linux as a
preview with an empty inbox, and that build is not a release download. The
preview opens the window and explains the missing live source, so the inbox
stays empty.

## Do I need a model to use it?

Triage needs a decider, and answers `422 provider_not_configured` without one.
Search works without a decider, and Ask can answer a plain count such as "How
many invoices?" without a model. The local classifier still learns from your
answers because it runs in the app. Semantic search needs an embedder, which the
local runtime can provide.

## Where is my data?

The status bar shows the data directory. On Windows it is
`%LOCALAPPDATA%\Mailsense`. Set `MAILSENSE_DATA_DIR` before launch to use
another directory.

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
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
