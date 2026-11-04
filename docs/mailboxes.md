---
layout: default
title: Mailboxes
---

# Mailboxes

Mailsense reads live mail from the classic Outlook profile that is already
running on a Windows machine. It does not ask for Microsoft credentials and it
does not connect to a mail service. The Linux build has no live source yet.

## What the shipped build can read

| Source | Status |
| --- | --- |
| Classic Outlook on Windows (COM) | Available |
| New Outlook, Outlook on the web | Not supported |
| IMAP, POP, Gmail API, Microsoft Graph | Not in this build |
| mbox, maildir, .eml files | Not exposed in this build |

The file readers and the archive importer exist in the source tree for tests and
future ports, but the application does not register them as sources.

## Discovery

Open Sources and choose Look for stores. Discovery lists the stores in the
Outlook profile and their folders. It does not read a message. A store shows a
message and folder count only after you pick it, because counting an unpicked
store would read it.

Discovery is also what makes a store pickable. A key that no discovery has seen
cannot be picked.

## Picking a store

Pick one or more stores. Nothing is read before that choice. The first sync runs
in the background and the job strip shows its progress.

- Selected means the store is in `mailboxes.selected` and its folders are visible
  to every screen.
- Discovered means the last discovery wrote a row for the store. A picked key
  with no row keeps its key and is marked as not discovered.
- Unpick keeps the stored mail but removes it from every screen and from the
  learning scope. It does not delete the database.

If the picked store has no owner address, learning stops with `no_owner_identity`
or `owner_never_reported` and offers Declare address. The address tells Mailsense
which messages you sent, which is what a reply obligation depends on.

## What a sync reads

- Folder inventory and message summaries.
- Full bodies, headers and recipients in the detail pass.
- Calendar occurrences and address books when those settings are on.

Outlook stores a summary row first and completes it later. The interface marks a
partial index instead of pretending the mailbox is empty. Semantic search skips a
message until its details are complete; full-text search can already find it.

## Removed mail

When a folder loses at least 100 messages or more than half of its messages,
Mailsense holds the removal instead of applying it. Confirm it in Sources, or let
it apply after 72 hours.

## Linux

There is no live mail source on Linux in this build. The window opens, the
screens render, and Sources explains that the inbox stays empty. The file source
is not exposed.
