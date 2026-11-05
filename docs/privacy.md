---
layout: default
title: Privacy
---

# Privacy

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
  <span class="doc-index-current" aria-current="page">Privacy</span>
  <a href="security.html">Security</a>
  <a href="troubleshooting.html">Troubleshooting</a>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

Mailsense is local-first. The live source is the classic Outlook profile already
open on your Windows machine. Mailsense does not ask for Microsoft credentials,
does not create an account, and has no vendor backend. The store, the index, the
learned classifier and the routing run on your machine; the section below lists
the only requests that can leave it.

## What stays on your machine

- The message store, attachments and indexes (SQLite).
- The learned profile, your corrections and the audit trail.
- Logs and run history, under your retention settings.
- The local runtime and its model, once downloaded.

## What leaves the machine

Mailsense makes five kinds of network request, and only for the reasons below:

1. **Local runtime download.** When you start the local runtime for the first
   time, it downloads its artifacts and model from the pinned URLs recorded in
   the app, and verifies each SHA-256 before use.
2. **A provider request.** Only through a role you select. The app ships no
   provider record: a role with no record makes no call and reports what is
   missing. Settings, Providers names the four roles, the record each one uses
   and the fields each call carries; [Ask](ask.md) lists what a written answer
   sends. Automatic triage and the index pass that follows a sync can start a
   call without a screen asking when their role is configured. The provider's
   terms apply.
3. **A provider model list.** Only when you ask the Providers screen to fetch the
   models a provider offers.
4. **A provider test.** Only when you press Test. It makes one small call, which
   is billable and is not covered by a budget.
5. **An update check.** After the startup warm, and at most once per
   `update.check.interval` (24 hours by default), the app asks GitHub for the
   latest published Mailsense release. It is one unauthenticated `GET` to
   `api.github.com/repos/LLM-4-People/mailsense/releases/latest`, with an
   `If-None-Match` header after the first answer. The request carries no mail,
   no address, no account and no installation identifier; GitHub sees the
   request's IP address and the standard HTTP headers. The release notes in
   the answer are cached locally as plain text. When a newer release exists,
   one more request lists the recent release tags so the app can count how far
   behind it is. The check is on by default (`update.check.enabled`) and can
   be turned off in Settings; a manual check runs only when you ask for it.
6. **Nothing else.** No telemetry, no analytics, no hidden request.

Embeddings have a default of their own. On first start the app selects the
pinned local embedder, and the local runtime computes embeddings in a process on
your machine: the subject and body of each message it indexes, and the text of
each question or search it embeds, stay on the machine. If you point
`embed.provider` at a remote service instead, those fields are sent to it, once
per newly indexed message and once per search. The index pass follows a sync
that stored new mail, by default (`index.after_sync`), so a remote embedder is
called without a screen asking.

The learned classifier, the evidence it reads and the routing run in the app. A
remote provider does not keep a request local; its terms govern what it does
with what you send.

## Learning

The local classifier trains small per-question heads on labels derived from
your mail, and it stores them in the local database. Training happens on your
disk. Nothing about that training is uploaded to this project. If you configure
a remote provider, that provider's terms govern the requests you send it.

## Audit it yourself

Every stored decision carries its source, the message fields it read, and a
confidence. The message detail shows that evidence; the Logs screen shows the
jobs, warnings and errors behind a screen. There is no hidden second record.

## Deleting your data

The data directory is shown in the app's status bar. Delete it to remove the
store, indexes, settings and logs. On Windows, uninstalling the app keeps the
data directory until you delete it.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
