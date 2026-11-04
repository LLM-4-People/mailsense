---
layout: default
title: "Mailsense"
lang: "en"
description: "Mailsense turns your Outlook mailbox into a ranked list of what needs you, with the reason on every row."
image: "assets/banner-en.svg"
---

<header class="hero">
  <div class="hero-copy">
    <p class="eyebrow">Local mail triage for Windows</p>
    <h1>Mailsense</h1>
    <p class="promise">Mailsense turns your Outlook mailbox into a ranked list of what needs you, with the reason on every row.</p>
    <p class="lede">It reads the classic Outlook profile already open on your PC. The message store, the classifier and the learned profile stay on your disk. A remote model runs only when you add one.</p>
    <p class="hero-actions"><a class="button" href="install.html">Install</a><a class="text-link" href="first-run.html">Read the first-run guide</a></p>
    <p class="hero-note">Windows installer, or the Linux preview. No account, no telemetry.</p>
  </div>
  <figure class="hero-art">
    <img src="assets/queue-en.svg" width="640" height="480" alt="An invented ranked queue with three rows: verify an invoice, reply to a kickoff, read workshop notes.">
    <figcaption>An invented queue. The action and the reason sit on every row.</figcaption>
  </figure>
</header>

[Install](install.md) · [First run](first-run.md) · [Console](console.md) · [Mailboxes](mailboxes.md) · [Providers](providers.md) · [Learning](learning.md) · [FAQ](faq.md) · [Privacy](privacy.md) · [Security](security.md) · [Troubleshooting](troubleshooting.md) · [Contributing](contributing.md) · [Français](fr/)

<section class="differences" aria-labelledby="differences-title">
  <h2 id="differences-title">Three things it does differently</h2>
  <p class="section-lead">Each one is a mechanism you can inspect.</p>
  <div class="cards">
    <article class="card">
      <p class="card-tag">01 / Evidence</p>
      <h3>Every label names its source.</h3>
      <p>A decision is stored as answers to named questions, with the confidence each answer carries where the question type has one and the evidence behind it. The message detail shows the fields the model read. When an index is partial, the screen names the missing part.</p>
    </article>
    <article class="card">
      <p class="card-tag">02 / Queue</p>
      <h3>The queue is scored by a fixed local policy.</h3>
      <p>A local Go scorer ranks each message from stored signals: a suspicious message adds 100, a decision 45, a reply 30, urgency and deadline levels add fixed weights, and recency adds at most 12. A score of 20 is the floor, and a suspicious, decision, reply, urgent or deadline signal always enters. Bulk automated mail never enters, however urgent its text.</p>
    </article>
    <article class="card">
      <p class="card-tag">03 / Learning</p>
      <h3>It abstains until your own labels certify it.</h3>
      <p>The local classifier trains one head per question on your corrections. A question is certified only when the Wilson lower bound of its answered precision reaches 0.99 on a held-out split, with at least 200 training labels, 50 evaluation labels and 0.5 coverage. Below that threshold it abstains, and the Learning screen names the stage that blocks it.</p>
    </article>
  </div>
</section>

<section class="boundary" aria-labelledby="boundary-title">
  <h2 id="boundary-title">What leaves the machine</h2>
  <p class="section-lead">Mailsense makes four kinds of network request. Each one has a reason you can see.</p>
  <div class="boundary-grid">
    <div class="boundary-col leaves"><h3>Leaves, only when it runs</h3><ul>
      <li>The pinned local runtime and embedding model, downloaded once and checked against SHA-256 before use.</li>
      <li>A request to a remote provider you configured, including automatic triage after a sync. It carries the message fields that call needs.</li>
      <li>A provider model list when you press Fetch models.</li>
      <li>One small test request when you press Test. A test is billable and is not covered by a budget.</li>
    </ul></div>
    <div class="boundary-col stays"><h3>Never leaves</h3><ul>
      <li>The mail store, attachments, indexes and learned profile. They are SQLite files in the data directory you chose.</li>
      <li>The classification, evidence and ranking code. They run as local Go code, not as a cloud call.</li>
      <li>No telemetry, no analytics, no update ping and no vendor account.</li>
    </ul></div>
  </div>
  <p class="note">Mailsense reads the classic Outlook profile already open on Windows. It does not ask for Microsoft credentials. A remote provider&#39;s terms govern what it does with a request you send. See <a href="privacy.html">privacy</a>.</p>
</section>

<section class="limits" aria-labelledby="limits-title">
  <h2 id="limits-title">What it cannot do yet</h2>
  <p class="section-lead">These limits are part of the current build.</p>
  <ol class="limit-list">
    <li>
      <h3>Only classic Outlook on Windows is a live source.</h3>
      <p>It reads the profile through COM. New Outlook, Outlook on the web, IMAP, POP, the Gmail API, Microsoft Graph and file import are not connected in this build.</p>
    </li>
    <li>
      <h3>The Linux build is a preview.</h3>
      <p>It opens, renders every screen and explains the missing source. The inbox stays empty because there is no live connector yet.</p>
    </li>
    <li>
      <h3>It reads and ranks. It does not act on the mailbox.</h3>
      <p>No sending, deleting, moving or read-state changes. The connector is read-only.</p>
    </li>
    <li>
      <h3>Triage needs a decider.</h3>
      <p>Without one, triage answers 422 provider_not_configured. Search and the local classifier still work.</p>
    </li>
    <li>
      <h3>The database is not encrypted by the app.</h3>
      <p>Your operating-system account and disk encryption are the protection. Provider keys on Windows use DPAPI.</p>
    </li>
    <li>
      <h3>There is no SQL console and no diagnostics bundle.</h3>
      <p>Logs are local and readable in the Logs screen. They redact secrets.</p>
    </li>
  </ol>
</section>

<section class="install" id="install" aria-labelledby="install-title">
  <div class="install-copy">
    <h2 id="install-title">Install</h2>
    <p class="section-lead">Windows is the supported live build. Linux is a preview.</p>
    <h3>Windows</h3><p>Download the per-user installer from <a href="https://github.com/LLM-4-People/mailsense/releases">Releases</a>. It needs the Microsoft Edge WebView2 runtime, which Windows 11 and current Windows 10 already have. The build is not code-signed, so Windows may ask you to confirm the publisher.</p>
    <h3>Linux</h3><p>Download the amd64 tarball, extract it and run install.sh for a menu entry, or run the binary directly.</p>
  </div>
  <div class="install-panel">
    <p class="panel-label">Linux preview</p>
    <pre><code>tar -xzf mailsense-*-linux-amd64.tar.gz &amp;&amp; cd mailsense-*-linux-amd64 &amp;&amp; ./install.sh</code></pre>
    <p class="note">One command for the Linux preview, from the directory that holds the downloaded tarball.</p>
    <a class="button button-light" href="first-run.html">Read the first-run guide</a>
  </div>
</section>

<p class="install-note">The first run writes the setup marker, lists the Outlook stores and shows the resource ceilings. Nothing is read from mail before you pick a store.</p>

<section class="status" aria-labelledby="status-title">
  <h2 id="status-title">Status</h2>
  <p>Mailsense is in active development. Interfaces and stored schemas may change between releases; the changelog and the release notes call out anything that requires a migration.</p>
</section>

<footer class="page-footer">
  <p><strong>Mailsense</strong> · <a href="https://github.com/LLM-4-People/mailsense">Source</a> · <a href="https://github.com/LLM-4-People/mailsense/LICENSE">AGPL-3.0-or-later</a> · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a></p>
  <p><a href="privacy.html">Privacy</a> · Your mailbox, ranked. Every row explains itself.</p>
</footer>
