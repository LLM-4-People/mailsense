---
layout: default
title: Providers
---

# Providers

<!-- nav:start -->
[Documentation](index.md) · [Install](install.md) · [First run](first-run.md) · [Console](console.md) · [Mailboxes](mailboxes.md) · **Providers** · [Learning](learning.md) · [FAQ](faq.md) · [Privacy](privacy.md) · [Security](security.md) · [Troubleshooting](troubleshooting.md) · [Contributing](contributing.md) · <a href="fr/providers.html" hreflang="fr" lang="fr" data-lang-switch="fr">Français</a>
<!-- nav:end -->

A provider is a record you add. The app ships no built-in provider and no
default key. The protocol decides which role a record can fill:

| Protocol | Role |
| --- | --- |
| `systemone`, `openai_decisions` | Decider |
| `openai_chat` | Generator |
| `openai_embeddings` | Embedder |

Four settings select the records:

- `triage.decider` for triage.
- `judge.provider` for the judge that proposes labels.
- `qa.provider` for written answers.
- `embed.provider` for semantic embeddings.

A record used by a role cannot be deleted or changed to another kind until the
role points elsewhere.

## Add a provider

Open Settings, Providers and choose Add. The form asks for a label, a protocol,
a base URL, a model and the fields that protocol needs. A decider also needs its
question and token budgets. A generator needs both prices and an output cap. An
embedder needs a complete embedding specification.

The record id is a lower-case slug and does not change after creation. The
provider file is `providers.json` in the data directory.

## Keys

If you leave the secret name empty, the form suggests
`PROVIDER_<ID>_API_KEY`. The value is written through the secrets route, never
through the provider record. On Windows the secret is encrypted with DPAPI. On
other platforms the secret store is read only, and the app reads
`MAILSENSE_SECRET_<NAME>` from the environment.

## Test and model list

Test makes one small real request: a one-question probe for a decider, a short
chat call for a generator, or one short embedding. The result reports the stage
that failed. A test is billable and is not covered by a budget.

Fetch models sends `GET {base_url}/models` with the key you entered. The list is
shown so you can apply a model; it is not stored. Missing limits and prices are
left empty instead of invented.

## When no decider is configured

Triage answers `422 provider_not_configured`. Automatic triage stays idle and
logs the reason. Search still works. The local classifier still learns from your
answers, because it runs in the app and does not use a provider.

A decider can be a remote service or a local OpenAI-compatible endpoint that you
run yourself.

## What leaves the machine

- Model discovery and the provider test, when you ask for them.
- Triage decisions, including the sender, subject, a truncated body preview,
  recipients, folder, recent history and label examples.
- Judge requests, which send message text to the configured generator.
- Written answers, which send the retrieved messages the answer cites.
- Embeddings, unless `embed.provider` points at the local runtime.

Nothing else is sent. See [privacy](privacy.md) for the full request list.

<!-- footer:start -->
[Source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/) · [Privacy](privacy.md)
<!-- footer:end -->
