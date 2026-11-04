---
layout: default
title: Privacy
---

# Privacy

Mailsense is local-first. The live source is the classic Outlook profile already
open on your Windows machine. Mailsense does not ask for Microsoft credentials,
does not create an account, and has no vendor backend. The local runtime
computes embeddings on your machine; classification and routing are local Go
code, not a cloud service.

## What stays on your machine

- The message store, attachments and indexes (SQLite).
- The learned profile, your corrections and the audit trail.
- Logs and run history, under your retention settings.
- The local runtime and its model, once downloaded.

## What leaves the machine

Mailsense makes four kinds of network request, and only for the reasons below:

1. **Local runtime download.** When you start the local runtime for the first
   time, it downloads its artifacts and model from the pinned URLs recorded in
   the app, and verifies each SHA-256 before use.
2. **A remote provider request.** Only when you configure a remote provider and
   a call runs. The request contains the message fields that call needs. The
   provider's terms apply. Settings, Providers names the endpoint and the model.
   Automatic triage can make this call after a sync without a screen asking; a
   provider test makes one small call when you press Test.
3. **A provider model list.** Only when you ask the Providers screen to fetch the
   models a provider offers.
4. **Nothing else.** No telemetry, no analytics, no update ping, no hidden
   request.

The local runtime serves embeddings on your machine. Classification, evidence
and routing run as local Go code. A remote provider does not keep a request
local; its terms govern what it does with what you send.

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
data directory; on Linux, removing the binary keeps it too.
