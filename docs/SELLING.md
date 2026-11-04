# Selling Mailsense

## One sentence

Mailsense turns the classic Outlook mailbox on a Windows machine into a ranked
queue of what needs its owner, with the evidence for every row stored on the same
machine.

## The promise

The first screen answers "what needs me now". Every row names the action and the
reason. The mailbox, the indexes, the learned profile and the evidence stay in a
SQLite directory the owner chooses. A remote model runs only when the owner adds
one.

## The ideal owner

- A consultant or small firm with high mail volume and client obligations, where
  a missed reply costs money.
- A team that cannot send mail to a third party but still needs triage and search.
- A person who already runs a local OpenAI-compatible endpoint and wants mail to
  use it.
- A Windows user who lives in classic Outlook and will not move the mailbox to a
  web app.

## What it does, mechanism by mechanism

1. **A decision is evidence.** The app stores answers to named questions, the
   confidence each answer carries where the question type has one, and the
   evidence reasons behind it. The message detail shows the fields the model
   read. A partial index is named on the screen.
2. **The queue is a fixed local policy.** A Go scorer ranks stored signals:
   suspicious +100, decision +45, reply +30, urgency +40/+30/+12/+3, deadline
   +45/+35/+25/+8, recency at most +12. The floor is 20, and a suspicious,
   decision, reply, urgent or deadline signal always enters. Bulk automated mail
   never enters. No model call is made to rank.
3. **Learning has a gate.** The local classifier trains one head per question on
   the owner's corrections. A question is certified only when the Wilson lower
   bound of its answered precision reaches 0.99 on a held-out split, with at
   least 200 training labels, 50 evaluation labels and 0.5 coverage. Below that
   threshold it abstains, and the Learning screen names the blocking stage.
4. **The boundary is stated.** Four kinds of network request exist: the pinned
   runtime and model download, a configured remote provider call, a provider
   model list on request, and one test request on request. The store, indexes,
   attachments, learned profile and ranking are local. There is no telemetry, no
   analytics, no update ping and no vendor account.

## Who it is not for

- A Gmail or Microsoft 365 user who wants a web app. The live source is classic
  Outlook on Windows through COM.
- A team that needs Mail to send, delete, move or mark messages read. The
  connector is read-only.
- A Linux desktop user who needs live mail today. The Linux build is a preview
  with an empty inbox.
- Someone who wants the app to decide without labels. Triage needs a decider, and
  the certified path needs the owner's own answers.

## Three objections, answered

### "Another assistant that reads my mail."

The store, the indexes and the classifier are local. A remote provider runs only
when the owner adds one and a call needs it. The provider page lists the fields a
call can carry: sender, subject, a truncated body preview, recipients, folder,
recent history and label examples. A remote provider's terms govern what it does
with a request. The connector does not ask for Microsoft credentials and does not
connect to a mail service.

### "It will be wrong."

Every answer carries evidence, and an answer below its certified threshold
abstains instead of guessing. The gate is per question: it opens on one certified
question and leaves the rest visible as warnings. The owner's corrections become
labels for the next training run. The app does not claim a certified answer when
the index is partial; it names the missing part.

### "It will eat my laptop."

CPU, memory and disk ceilings are settings, with defaults of 80% CPU, 85% memory
and 90% disk. Background work pauses at the ceiling and writes the resource and
the limit to Logs. Mail sync still respects the disk ceiling under CPU or memory
pressure. The local runtime is one pinned build and one pinned model, downloaded
once and verified before every launch.

## Compared with a cloud assistant

| Question | Mailsense | A typical cloud assistant |
| --- | --- | --- |
| Where the mailbox is stored | Local SQLite in a directory the owner chooses | The provider's service, under its terms |
| Account | None | Usually an account and a subscription |
| Model | A local runtime, or a provider record the owner adds | The vendor's model |
| Offline use | Local screens, full-text search, classifier and runtime after install | Usually requires a network |
| Mailbox actions | Read-only | Often sends, files and changes read state |
| Platform | Classic Outlook on Windows live; Linux preview | Usually web and mobile |
| Source and cost | AGPL-3.0-or-later, no paid tier; a remote provider bills under its terms | Subscription, with the vendor's terms |

The comparison is about the default shape of each product. A specific cloud
assistant may differ; read its terms before making a claim about it.

## Limits to state before a demo

- Only classic Outlook on Windows is a live source. New Outlook, Outlook on the
  web, IMAP, POP, the Gmail API, Microsoft Graph and file import are not
  connected.
- The Linux build opens and renders, but the inbox stays empty.
- No sending, deleting, moving or read-state changes.
- No SQL console and no diagnostics bundle.
- The database is not encrypted by the app; the operating-system account and
  disk encryption are the protection.
- The binaries are not code-signed. Windows may ask the user to confirm the
  publisher.

## Demo script: first run to first answer

1. Install for Windows and open the app. Welcome shows the data directory, the
   Outlook stores already found, the language and the time zone. Nothing has been
   read from mail.
2. Choose Look for stores, pick one store, and start the sync. Say plainly that
   nothing was read before this choice.
3. On Model, choose the local runtime. The download is about 342 MB on Windows
   and 328 MB on Linux, verified against a pinned SHA-256. Skip means no triage;
   Search still works.
4. Finish. Point at the resource ceilings and at the partial-index notice while
   the first sync runs.
5. Open Home or Brief. Take one ranked row and expand its evidence. Read the
   action, the reasons and the fields behind them.
6. Open Teach and answer one message. The answer becomes a stored label. This is
   the material the local classifier learns from.
7. Open Learning. Point at the first blocked stage and the action that unblocks
   it. Do not promise an open gate on a new mailbox.
8. Open Ask. Show a structured count first. If a provider is configured, show a
   written answer with citations. Then open Providers with no decider and show
   `422 provider_not_configured`.
9. Close on the data directory. Say that deleting it removes the store, indexes,
   settings, learned model and logs.

## Proof points to show, not claim

- A ranked row with its evidence expanded.
- A reply obligation that matches the last message in the thread.
- A partial-index notice that appears only when the semantic index is incomplete.
- A resource ceiling that pauses a job and writes the reason to Logs.
- The pinned runtime size and hash in the catalog.
- The Linux preview with an empty inbox and the explanation on Sources.
- The four network request kinds on the privacy page.

## Copy rules

- Calm and specific. No superlatives and no invented benchmarks.
- Sentence case headings. Plain sentences. One idea per sentence.
- Name the mechanism, not the feeling.
- Every number comes from the product, a public page or a test.
- French copy is written in French, not translated sentence by sentence.
- No em dash, no spaced en dash and no double hyphen as punctuation.
