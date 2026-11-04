---
layout: default
title: Learning
---

# Learning

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
  <span class="doc-index-current" aria-current="page">Learning</span>
  <a href="faq.html">FAQ</a>
  <a href="privacy.html">Privacy</a>
  <a href="security.html">Security</a>
  <a href="troubleshooting.html">Troubleshooting</a>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

Learning is the gate that decides when Mailsense may answer on its own. It is
not a progress bar for its own sake: every stage is a fact the app needs before
a certified answer exists.

## Stages

The Learning screen shows these stages in order:

1. **Mailboxes.** A store is picked and has an owner identity.
2. **Machine.** The time zone is confirmed.
3. **Sync.** The picked stores have been read, including message details.
4. **Evidence.** Each message has the evidence a decision needs.
5. **Embeddings.** The semantic index covers the picked mail.
6. **Labels.** Enough labelled examples exist for each question.
7. **Layer1.** The local classifier has a trained head for the question.
8. **Calibration.** The head has a threshold that holds on held-out data.
9. **Questions.** At least one question is certified.

A stage is pending, running, blocked or done. A blocked stage names the setting
or action that unblocks it.

## When the gate opens

The gate opens when the current selection is a subset of the last fully learned
scope. The learned scope is stored locally. Unpicking a store keeps the rest of
the gate open. Adding a store closes it until the larger selection is learned.

## What certified means

Certification is per question. The app trains on one split and tests on a
disjoint split, divided by conversation. A question is certified when the Wilson
lower bound of its answered precision reaches the target. The default target is
0.99, the default minimum coverage is 0.5, and the default requirements ask for
at least 200 training labels and 50 evaluation labels.

The gate opens as soon as one question is certified. The other questions stay
visible as warnings and abstain instead of guessing.

## Preview mode

`learning.test_mode` opens the screens before certification. It is a reversible
preview: every answer is labelled uncertified, and the real gate, the stages and
the stored labels do not change. Turn it off to return to the certified gate.

## What is trained and where

The local classifier trains small per-question heads on labels derived from your
mail. The parameters are stored in the local SQLite database. Training runs in
the app. Nothing about the training is uploaded to this project.

A remote provider can still receive requests when you configure one: the judge
proposes labels, a generator writes answers, or a remote embedder replaces the
local one. Those requests follow the provider's terms.

## What the screen shows

The Learning screen shows counts, question names, store keys, stage states,
blockers and diagnostics. It does not show message content. The Hub shows how
many questions are certified and links to the same screen.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
