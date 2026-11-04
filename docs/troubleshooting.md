---
layout: default
title: Troubleshooting
---

# Troubleshooting

<!-- nav:start -->
[Documentation](index.md) · [Install](install.md) · [First run](first-run.md) · [Console](console.md) · [Mailboxes](mailboxes.md) · [Providers](providers.md) · [Learning](learning.md) · [FAQ](faq.md) · [Privacy](privacy.md) · [Security](security.md) · **Troubleshooting** · [Contributing](contributing.md) · [Français](fr/troubleshooting.md)
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

The status bar shows the data directory. It is
`%LOCALAPPDATA%\Mailsense` on Windows and
`${XDG_DATA_HOME:-~/.local/share}/mailsense` on Linux. Set
`MAILSENSE_DATA_DIR` before launch to use another directory.

<!-- footer:start -->
[Source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/) · [Privacy](privacy.md)
<!-- footer:end -->
