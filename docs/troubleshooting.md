---
layout: default
title: Troubleshooting
---

# Troubleshooting

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
  <a href="security.html">Security</a>
  <span class="doc-index-current" aria-current="page">Troubleshooting</span>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

## The inbox is empty

Open Sources. Run Discover, pick the store Mailsense may read, then start a sync.
The job strip shows how many messages have been scanned and how many remain. A
screen that depends on a partial index says so; it does not pretend the mailbox
is empty.

On Linux there is no live mail source yet, so the inbox stays empty by design.

## "Search index is warming"

The semantic part of the index is not complete. Full-text results are still
available. The notice on Search or Ask names the part that is missing. This is not
a no-match answer.

## The app pauses background work

The resource ceilings are doing their job. Open Settings, General, and raise the
CPU, memory or disk ceiling, or wait for the machine to settle. The Logs screen
records the pause with the resource and the limit that triggered it.

## A model call fails

- Check the provider record and the endpoint in Settings, Providers.
- Run the provider test; it reports the failing stage.
- A timeout is not a wrong answer. Retry, or reduce the request size.
- If no decider is configured, triage answers `422 provider_not_configured`.
  Add a provider and choose it as the decider. Search still works.

## Outlook is not found

Mailsense reads the classic Outlook profile that is already open on Windows. It
does not sign in to Microsoft. Check that classic Outlook is installed, running,
and has at least one store. On Linux, live mail is not available yet.

## Setup opens again

The setup marker is the `ui.setup.done` setting. If it is false and no mailbox is
selected, Mailsense opens setup on the next start. Set it back to true in
Settings, or pick a mailbox in Sources.

## Where the logs are

Open Logs for jobs, warnings and errors. The data directory also holds the log
files. There is no separate diagnostics export bundle.

## Where the data is

The status bar shows the data directory. On Windows it is
`%LOCALAPPDATA%\Mailsense`. Set `MAILSENSE_DATA_DIR` before launch to use
another directory.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
