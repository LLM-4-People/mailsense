---
layout: default
title: Ask
---

# Ask

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <span class="doc-index-current" aria-current="page">Ask</span>
  <a href="install.html">Install</a>
  <a href="first-run.html">First run</a>
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

Ask answers a question about the mail on this machine. You type a question and
the answer comes back as a computed count, a ranked list, a list of matching
messages, or a written answer with citations.

## What it answers

- A count or a ranking over mail metadata: how many messages are urgent, which
  senders wrote the most, which month was busiest.
- A count of one recognized topic, such as invoices or support requests, with
  the topic named and the classification coverage behind the number.
- A list of matching messages: which messages talk about the invoice.
- A fact from the mail: what the signed proposal asks you to confirm.
- A summary of one thread or one day.

A question is between 3 and 500 characters. The composer offers a few suggested
questions, and a new conversation starts a fresh thread.

## The three modes

The model selector sits beside the question field.

| Mode | What answers | When a model is called |
| --- | --- | --- |
| Auto | The local index and ranking when the question has an exact answer, otherwise the configured provider or the matching messages | Only for a question no exact intent covers |
| Local | The local index, the ranking and the stored profile | Never |
| Remote | The configured provider, grounded in the retrieved messages | For every question with retrieved evidence |

Remote is offered only when a provider record is configured and usable. When it
is not, the selector says why and links to provider settings, and Auto and Local
stay available.

## Counts from the local index

Ask can answer a plain count of one recognized topic or priority category
from the local index without calling a model. "How many invoices?", "How many
support requests?" and "Combien de factures ?" count one topic; "How many
urgent messages?" counts one priority category. Local answers these counts
directly, and Auto answers them the same way when no model is configured.

The topic answer names the topic or subtopic it counted and reports the
classification coverage behind it, for example "2 messages are classified as
Billing or account › Invoice" and "9 of 12 messages have a topic". The
coverage line states how many of the mailbox's messages carry a topic at all,
so a low or zero count can be read against what was classified. A zero count
says that no message is classified as that topic and cites nothing.

The local count applies only to a plain mailbox-wide wording. When the
wording names a person, a company, a date or another scope, the local count
does not answer it: "How many invoices this week?" and "How many invoices
does Bob have?" ask about a smaller set.

## How a written answer is grounded

A written answer is generated only through a configured provider. The model sees
numbered messages retrieved from the local index and marks each claim with a
message number, for example `[1]`. The server checks every marker against the
retrieved set.

An answer that cites a number outside that set is refused whole. The server then
renumbers the markers to their citation positions, adds a source the model named
in its used list but forgot in the text, and keeps only citations that point at a
real message. The prompt asks for at most 120 words.

### When grounding fails

When no answer or citation survives, the screen keeps the matching messages and
shows a notice that names the cause: no model configured, a failed call, or an
answer that could not be grounded. The app never shows a written claim without a
source under it.

## Citations and history

Every citation row carries the subject, the sender and the date, and opens the
message it names. The same citation appears as a numbered chip in the answer
text.

Each answer is stored with its question, its route, its mode, its citations and,
when a model was used, its provider and model. The history rail reopens a stored
answer, and Clear history removes every stored answer.

## When there is no model

Local never calls a model. A count of a recognized topic or priority category,
a ranking and a company profile still answer. A free-form question returns the
matching messages and a notice that says Local never calls a model, with the
offer to switch to Auto or Remote.

A failed provider call and an answer that could not be grounded keep the
matching messages on screen. A warming index or a retrieval timeout keeps the
question and offers Try again. A question with no match suggests different words,
a shorter question or a broader topic.

Triage needs a configured decider. Ask in Local mode does not.

## What a provider request carries

When a provider is used, the request carries the question, the requested
language, today's date, your time zone, the numbered messages the local search
retrieved, the thread states, the owner line, which names you and your addresses
when the mailbox resolves them, and the prior turns of the same conversation, up
to six.

Each numbered message carries its subject, direction, sender name and address,
received date, unread and importance state when known, attachment flag, To and
Cc lines, learned labels, priority signals, and a body excerpt of at most 600
characters. Nothing else leaves the machine for that call. A provider's terms
govern what it does with the request. See [privacy](privacy.md).

## What it cannot do

- It does not search the web.
- It does not send, delete, move or mark mail read. The connector is read-only.
- It does not use a vendor account.
- It does not state a fact that is absent from the retrieved messages.
- It is bounded by `qa.max_messages`, `qa.retrieval_timeout`, `qa.call_timeout`
  and the two budget settings.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
