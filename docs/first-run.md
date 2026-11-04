---
layout: default
title: First run
---

# First run

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <a href="ask.html">Ask</a>
  <a href="install.html">Install</a>
  <span class="doc-index-current" aria-current="page">First run</span>
  <a href="console.html">Console</a>
  <a href="mailboxes.html">Mailboxes</a>
  <a href="providers.html">Providers</a>
  <a href="learning.html">Learning</a>
  <a href="faq.html">FAQ</a>
  <a href="privacy.html">Privacy</a>
  <a href="security.html">Security</a>
  <a href="troubleshooting.html">Troubleshooting</a>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

The first start opens a short setup screen. It writes a marker when you finish,
so it does not open again. You can reopen it by resetting `ui.setup.done` in
Settings.

## Basic path

1. **Welcome.** Mailsense shows the data directory, how many Outlook stores it
   already found, the interface language, and the time zone it detected. Nothing
   is read from mail yet. Confirm or change the language and the zone; deadlines
   and "today" answers depend on the zone.
2. **Mail.** Choose Look for stores to list the stores in the classic Outlook
   profile on this Windows machine, then pick the store Mailsense may read.
   Nothing is read before you pick one. Choose Not now to finish without mail.
   On Linux there is no live source yet, so this step explains that and lets you
   finish.
3. **Model.** The local runtime is downloaded on first use: about 342 MB on
   Windows. The Linux source preview uses the same runtime and model, about
   328 MB. You can add a remote provider instead, or skip this step and add one
   later in Settings, Providers. Triage needs a decider and
   answers `422 provider_not_configured` without one; Search works without a
   decider, and the local classifier still learns from your answers.
4. **Finish.** Mailsense shows the default ceilings: 80% CPU, 85% memory and 90%
   disk. Keep them unless you know your machine well. The first sync and index
   run in the background. Screens that read a partial index say so. The learning
   gate stays closed until enough of your own answers certify it; the Learning
   screen shows what it is waiting for.

## Advanced path

The setup screen links to Settings, which has three tabs:

- **General.** Every declared setting, in its group, with its default, current
  value, layer and reset action.
- **Providers.** Provider records, the role each one fills, and the model list
  fetched from the provider.
- **Runtime.** The local runtime state, install and verification.

The Model step has an Open Providers button, so the advanced path starts from
the same place.

The advanced path is not a second configuration store: it writes the same
settings the General tab shows.

## What is ready before you start

- The time zone proposed by the operating system.
- The resource ceilings declared with the app.
- The default decision profile and taxonomy shipped with the app.
- The mailbox list from the discovery pass.
- The local runtime state, if it is already installed.

Mailsense does not pre-fetch a provider catalog or a model list: a provider is a
record you add, and its model list is fetched when you ask for it.

## If something fails

Each step fails on its own and can be retried without losing the others. The
error names the setting or the endpoint, not only an internal code. See
[troubleshooting](troubleshooting.md).

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
