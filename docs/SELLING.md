# Positioning and copy

## One sentence

Mailsense turns a Windows Outlook mailbox into a ranked, auditable work queue
that runs on your own machine.

## The problem, in the owner's words

"I have 40,000 messages, three clients waiting, and no idea which one I answered
last."

A summary tells you what a message says. It does not tell you what is owed, to
whom, by when, or why the app believes it. It also sends the mailbox to a server.

## The claim

1. **Every verdict carries its evidence.** Category, topic, the ask, reply
   obligation, urgency, deadline and confidence, each with the fields it read.
2. **The queue is the product.** The first screen is what needs you, ordered by
   what the message asks, with the reason on the row.
3. **Local by default.** SQLite on disk, a local embedder, and a local classifier
   that learns from your corrections. A remote provider is a record you add, not
   a condition of use.
4. **It states its own limits.** A partial index, a warming model, a missing
   decider or an unread store is named on the screen. The app does not turn a
   missing fact into an empty answer.

## Who it is for

- Consultants and small firms with high mail volume and client obligations.
- People who already run a local model and want their mail to use it.
- Teams that cannot send mail to a third party but still need triage.

## What it does not do

These limits are part of the offer, not a footnote.

- No Microsoft sign-in, no Graph, no IMAP, no Gmail API. The live source is the
  classic Outlook profile already open on Windows.
- No file import in the shipped build.
- No sending, deleting, moving or read-state changes in the mailbox. The
  connector reads.
- No Linux mail source yet. The Linux build is a preview of the interface and the
  local data model.
- No SQL console and no diagnostics export bundle.
- No cloud account, no telemetry and no vendor backend.

## Proof points to show, not claim

- A ranked row with its evidence expanded, including the message fields used.
- A reply obligation that matches the last message in the thread.
- A warming notice that appears only when the semantic index really is partial.
- A configurable resource ceiling that pauses a job and writes the reason to
  Logs.
- A runtime download checked against a pinned SHA-256 before every launch.

## Objections

| Objection | Answer |
| --- | --- |
| "Another assistant that reads my mail." | The store, index and classifier are local. A remote provider runs only when you add one and a call needs it. The request shape is listed in Providers. |
| "It will be wrong." | It shows confidence and evidence, and it abstains below the certified threshold. Your corrections become labels. |
| "I already have folders and rules." | Mailsense does not ask you to move mail. It ranks what is already there. |
| "It will eat my laptop." | CPU, memory and disk ceilings are settings. Background work pauses at the ceiling and logs why. Mail sync still respects the disk ceiling under CPU or memory pressure. |
| "Lock-in." | SQLite in a data directory you choose. Delete the directory and the app is gone. |
| "Which model do I need?" | Triage needs a decider. Search works without one. A decider can be remote or a local OpenAI-compatible endpoint. |
| "Does it work on Linux?" | The preview opens and renders, but the inbox stays empty because there is no live source yet. |

## Voice

Calm and specific. No superlatives. Short sentences. Name the mechanism, not the
feeling. French copy mirrors the English structure without literal translation.

## A short walkthrough

1. Open Brief and expand the evidence on one row. The fields are the fields the
   model read.
2. Open Learning and read the stage that blocks the gate. The screen names the
   setting or action that unblocks it.
3. Open Providers with no decider and show the typed error. Add a record and run
   its test; the result names the failing stage.
4. Open Settings, General and change a resource ceiling. Run a job and show the
   pause in Logs.

## Calls to action

- Install for Windows and read the first-run page.
- Read the privacy page in one sitting.
- If the Linux preview is all you need today, say so on the page rather than
  promising a source that does not exist.
