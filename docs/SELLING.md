# Selling Mailsense

## One sentence

Mailsense answers a plain question about the classic Outlook mailbox on a Windows
machine, with a count, a list or a written answer whose citations open the
message, and it still ranks the queue of what needs its owner.

## The promise

Ask is the first screen. You type a question; the answer is computed from the
local index, read from the ranked queue, or written by a model the owner
configured. Every citation opens the message it names, and every answer is
stored with its question. The queue is the second proof: each row names the
action and the reason. The mailbox, the indexes, the learned profile and the
answer history stay in a SQLite directory the owner chooses.

## The ideal owner

- A consultant or small firm with high mail volume and client obligations, where
  a missed reply costs money.
- A team that cannot send mail to a third party but still needs answers, triage
  and search.
- A person who already runs a local OpenAI-compatible endpoint and wants mail to
  use it, either for a written answer or for nothing at all.
- A Windows user who lives in classic Outlook and will not move the mailbox to a
  web app.

## What it does, mechanism by mechanism

1. **Ask answers in three shapes.** A count or a ranking over mail metadata is a
   typed query against the local index and the fixed ranking. A list of matching
   messages comes from the same local search. A written answer comes only
   through a configured provider. The screen names which mechanism answered.
2. **A written answer is grounded, not trusted.** The model sees numbered
   messages and marks every claim with a number. The server checks each marker
   against the retrieved set, refuses the whole answer when a marker points
   outside it, renumbers the markers to citation positions and keeps only
   citations that name a real message. When no citation survives, the matching
   messages stay on screen with a notice that names the cause.
3. **The queue is a fixed local policy.** A Go scorer ranks stored signals:
   suspicious +100, decision +45, reply +30, urgency +40/+30/+12/+3, deadline
   +45/+35/+25/+8, recency at most +12. The floor is 20, and a suspicious,
   decision, reply, urgent or deadline signal always enters. Bulk automated mail
   never enters. No model call is made to rank.
4. **Learning has a gate.** The local classifier trains one head per question on
   the owner's corrections. A question is certified only when the Wilson lower
   bound of its answered precision reaches 0.99 on a held-out split, with at
   least 200 training labels, 50 evaluation labels and 0.5 coverage. Below that
   threshold it abstains, and the Learning screen names the blocking stage.
5. **The boundary is stated.** Four kinds of network request exist: the pinned
   runtime and model download, a configured remote provider call, a provider
   model list on request, and one test request on request. The store, indexes,
   attachments, learned profile, queue and answer history are local. There is no
   telemetry, no analytics, no update ping and no vendor account.

## Who it is not for

- A Gmail or Microsoft 365 user who wants a web app. The live source is classic
  Outlook on Windows through COM.
- A team that needs Mail to send, delete, move or mark messages read. The
  connector is read-only, and Ask cannot act on the mailbox either.
- A Linux desktop user who needs live mail today. The source builds on Linux as
  a preview with an empty inbox, and that build is not a release download.
- Someone who wants the app to decide without labels. Triage needs a decider, and
  the certified path needs the owner's own answers.
- Someone who wants Ask to search the web. It answers from the local mail and
  nothing else.

## Three objections, answered

### "Another assistant that reads my mail."

Ask in Local mode never calls a model, which is the answer for a team that
cannot send mail to a third party. When the owner selects Auto with a provider
configured, or selects Remote, the request carries the question, the requested
language, today's date, the time zone, the numbered messages the local search
retrieved, the thread states, the owner line and the prior turns of the same
conversation. Each numbered message carries its subject, direction, sender name
and address, received date, unread and importance state, attachment flag, To and
Cc lines, learned labels, priority signals and a body excerpt of at most 600
characters. Nothing else leaves the machine for that call. The provider's terms
govern what it does with the request. The connector does not ask for Microsoft
credentials and does not connect to a mail service.

### "It will be wrong."

A written answer carries a citation on every claim, and an answer whose marker
points at a message outside the retrieved set is refused whole. When no citation
survives, the screen keeps the matching messages and says why. A count or a
ranking is computed from the local query, so the model never states the number.
Every classification answer carries evidence, and an answer below its certified
threshold abstains instead of guessing. The app does not claim a complete index
when the index is partial; it names the missing part.

### "It will eat my laptop."

CPU, memory and disk ceilings are settings, with defaults of 80% CPU, 85% memory
and 90% disk. Background work pauses at the ceiling and writes the resource and
the limit to Logs. Mail sync still respects the disk ceiling under CPU or memory
pressure. The local runtime is one pinned build and one pinned model, downloaded
once and verified before every launch. Ask is bounded by `qa.max_messages`,
`qa.retrieval_timeout`, `qa.call_timeout` and the two budget settings, so one
question cannot hold a worker or a provider call without a limit.

## Compared with a cloud assistant

| Question | Mailsense | A typical cloud assistant |
| --- | --- | --- |
| Where the mailbox is stored | Local SQLite in a directory the owner chooses | The provider's service, under its terms |
| Account | None | Usually an account and a subscription |
| Model | A local runtime, or a provider record the owner adds | The vendor's model |
| A question about the mail | Ask returns a count, a list or a written answer, and every citation opens the message | A chat answer, often with no link to the source message |
| Offline use | Local screens, full-text search, classifier and runtime after install | Usually requires a network |
| Mailbox actions | Read-only | Often sends, files and changes read state |
| Platform | Classic Outlook on Windows live; Linux source preview | Usually web and mobile |
| Source and cost | AGPL-3.0-or-later, no paid tier; a remote provider bills under its terms | Subscription, with the vendor's terms |

The comparison is about the default shape of each product. A specific cloud
assistant may differ; read its terms before making a claim about it.

## Limits to state before a demo

- Only classic Outlook on Windows is a live source. New Outlook, Outlook on the
  web, IMAP, POP, the Gmail API, Microsoft Graph and file import are not
  connected.
- The Linux source preview opens and renders, but the inbox stays empty, and it
  is not a release download.
- No sending, deleting, moving or read-state changes, and Ask does not search the
  web.
- Ask needs no provider in Local mode, but a written answer needs one. Without a
  provider, a free-form question returns the matching messages.
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
3. On Model, choose the local runtime. The download is about 342 MB on Windows,
   verified against a pinned SHA-256. Skip means no triage; Search still works.
4. Finish. Point at the resource ceilings and at the partial-index notice while
   the first sync runs.
5. Open Ask. Switch the selector to Local and ask "How many messages are
   urgent?". The count comes from the local ranking, and the footer says no model
   was used.
6. With a provider configured, switch to Auto and ask "Summarize the thread
   about the delivery window." Read the written answer, then open each citation
   under it. The citation opens the message it names.
7. Open the history rail. The question and the answer are stored together.
8. Open Home or Brief. Take one ranked row and expand its evidence. Read the
   action, the reasons and the fields behind them. This is the second proof.
9. Open Teach and answer one message. The answer becomes a stored label. This is
   the material the local classifier learns from.
10. Open Learning. Point at the first blocked stage and the action that unblocks
    it. Do not promise an open gate on a new mailbox.
11. Open Providers with no decider and show `422 provider_not_configured`.
12. Close on the data directory. Say that deleting it removes the store, indexes,
    settings, learned model, answer history and logs.

## Proof points to show, not claim

- A count answered in Local mode, with no model call and the footer that says so.
- A written answer with a citation on every claim, and each citation opening its
  message.
- A stored answer that reopens from the history rail with its question.
- A grounded answer refused when it cannot cite the retrieved mail, with the
  matching messages still on screen.
- A ranked row with its evidence expanded.
- A reply obligation that matches the last message in the thread.
- A partial-index notice that appears only when the semantic index is incomplete.
- A resource ceiling that pauses a job and writes the reason to Logs.
- The pinned runtime size and hash in the catalog.
- The Linux source preview with an empty inbox and the explanation on Sources.
- The four network request kinds on the privacy page.

## Copy rules

- Calm and specific. No superlatives and no invented benchmarks.
- Sentence case headings. Plain sentences. One idea per sentence.
- Name the mechanism, not the feeling.
- Every number comes from the product, a public page or a test.
- French copy is written in French, not translated sentence by sentence.
- No em dash, no spaced en dash and no double hyphen as punctuation.
