---
layout: default
title: "Mailsense"
lang: "en"
language_switch: true
description: "Ask your mail a question. It answers with a count, a list, or a written answer that cites the messages it used."
image:
  path: "/assets/social-preview.png"
  width: 1200
  height: 630
  alt: "Mailsense: Ask your mail. Every citation opens the message."
---

<header class="hero">
  <div class="shell hero-grid">
    <div class="hero-copy">
      <h1 class="promise" id="thesis-title">Ask your mail a question. It answers with a count, a list, or a written answer that cites the messages it used.</h1>
      <p class="lede">It reads the classic Outlook profile already open on your PC. Ask answers from the local index; a written answer comes only from a model you configured and the messages it can cite. The store, the classifier and the learned profile stay on your disk.</p>
      <p class="actions"><a class="button" href="install.html">Install</a><a class="quiet-link" href="first-run.html">Read the first-run guide</a></p>
      <p class="hero-note">Windows installer. No account, no telemetry.</p>
    </div>
    <div class="hero-stage">
      <figure class="app-window">
        <div class="app-bar">
          <img src="assets/mark.svg" width="20" height="20" alt="">
          <span class="app-name">Mailsense</span>
          <span class="app-badge">Preview</span>
        </div>
        <img class="app-shot" src="assets/screenshots/en/home.png" width="1440" height="900" loading="eager" decoding="async" alt="Mailsense: Home">
      </figure>
      <aside class="answer-slip" aria-labelledby="slip-label">
        <div class="slip-head">
          <p class="slip-label" id="slip-label">ASK / GROUNDED ANSWER</p>
          <span class="slip-meta">
            <span class="slip-sample">Sample answer</span>
            <span class="chip">Auto</span>
          </span>
        </div>
        <p class="slip-question">Who is waiting on the Northwind proposal?</p>
        <p class="slip-answer">Three threads still need you. Atelier Nord is holding the Thursday delivery window until you confirm the signed proposal. Harbor Logistics asked which pickup window works, and Studio Lumen sent an invoice whose payment link points somewhere unexpected. The first two are ordinary replies. The invoice is the one to check before you pay.</p>
        <ol class="citations">
          <li>
            <span class="citation-mark" aria-hidden="true">01</span>
            <span class="citation-main">
              <span class="citation-subject">Signed proposal for the Northwind launch</span>
              <span class="citation-meta">Atelier Nord · 3 Nov 2026</span>
            </span>
          </li>
          <li>
            <span class="citation-mark" aria-hidden="true">02</span>
            <span class="citation-main">
              <span class="citation-subject">Thursday pickup window</span>
              <span class="citation-meta">Harbor Logistics · 2 Nov 2026</span>
            </span>
          </li>
          <li>
            <span class="citation-mark" aria-hidden="true">03</span>
            <span class="citation-main">
              <span class="citation-subject">Invoice 1042 needs a second look</span>
              <span class="citation-meta">Studio Lumen · 3 Nov 2026</span>
            </span>
          </li>
        </ol>
        <p class="slip-caption">In the app, every citation opens the message it names.</p>
      </aside>
    </div>
  </div>
</header>

<section class="section section-ask" aria-labelledby="ask-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="ask-title">Ask first</h2>
      <p class="section-lead">Ask answers a question about your mail in plain language. The screen names which mechanism answered it: a typed query, a search, or a model you configured.</p>
    </div>
    <div class="ask-bento">
      <article class="ask-card ask-card-grounding">
        <div class="card-top">
          <span class="card-number">01</span>
          <span class="card-tag">Grounded</span>
        </div>
        <h3>A written answer cites every message it uses.</h3>
        <p>The model is asked to write at most 120 words and marks each claim with a message number. The server checks every marker against the retrieved set, refuses an answer whose marker points outside it, renumbers the markers to citation positions and keeps a citation only when it names a real message. When no citation survives, the matching messages stay on screen with a notice that names the cause.</p>
        <ul class="mini-cites">
          <li>
            <span class="mini-dot" aria-hidden="true"></span>
            <span class="mini-subject">Signed proposal for the Northwind launch</span>
            <span class="mini-sender">Atelier Nord</span>
          </li>
          <li>
            <span class="mini-dot" aria-hidden="true"></span>
            <span class="mini-subject">Thursday pickup window</span>
            <span class="mini-sender">Harbor Logistics</span>
          </li>
          <li>
            <span class="mini-dot" aria-hidden="true"></span>
            <span class="mini-subject">Invoice 1042 needs a second look</span>
            <span class="mini-sender">Studio Lumen</span>
          </li>
        </ul>
      </article>
      <article class="ask-card ask-card-local">
        <div class="card-top">
          <span class="card-number">02</span>
          <span class="card-tag">Local</span>
        </div>
        <h3>A count or a ranking is computed, not written.</h3>
        <p>A count, a ranking and a company profile are read from the local index and the stored facts. The number and the rows come from a typed query, never from model text. Local mode never calls a model: the same count still answers, and a free-form question returns the matching messages with a notice that says Local never calls a model.</p>
        <div class="mode-chips" aria-hidden="true">
          <span class="mode-chip">Auto</span>
          <span class="mode-chip is-on">Local</span>
          <span class="mode-chip">Remote</span>
        </div>
      </article>
      <article class="ask-card ask-card-trace">
        <div class="card-top">
          <span class="card-number">03</span>
          <span class="card-tag">Trace</span>
        </div>
        <h3>A citation opens the message, and the answer is kept.</h3>
        <p>Each citation row carries the subject, the sender and the date, and links to the message it names. Each answer is stored with its question, its route, its mode, its citations and, when a model was used, its provider and model. The history rail reopens it, and Clear history removes every stored answer.</p>
        <div class="mini-history">
          <span class="mini-history-dot" aria-hidden="true"></span>
          <span>Who is waiting on the Northwind proposal?</span>
        </div>
      </article>
    </div>
    <div class="ask-lower">
      <div class="mode-panel">
        <h3 id="modes-title">Three modes, one rule</h3>
        <table class="mode-table" aria-labelledby="modes-title">
          <thead><tr><th scope="col">Mode</th><th scope="col">What answers</th><th scope="col">When a model runs</th></tr></thead>
          <tbody>
            <tr>
              <th scope="row" data-label="Mode"><span class="mode-name">Auto</span></th>
              <td data-label="What answers">The local index and ranking when the question has an exact answer, otherwise the provider you configured or the matching messages.</td>
              <td data-label="When a model runs">Only for a question no exact intent covers.</td>
            </tr>
            <tr>
              <th scope="row" data-label="Mode"><span class="mode-name">Local</span></th>
              <td data-label="What answers">The local index, the ranking and the stored profile.</td>
              <td data-label="When a model runs">Never.</td>
            </tr>
            <tr>
              <th scope="row" data-label="Mode"><span class="mode-name">Remote</span></th>
              <td data-label="What answers">The provider you configured, grounded in the retrieved messages.</td>
              <td data-label="When a model runs">For every question with retrieved evidence. Offered only when a provider is usable.</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="guarantee-panel">
        <p class="pull">Every citation opens the message it names, and every answer is stored with its question so you can reopen it.</p>
        <p class="note">Ask is bounded by <code>qa.max_messages</code>, <code>qa.retrieval_timeout</code>, <code>qa.call_timeout</code> and the two budget settings. A warming index keeps the question and offers Try again.</p>
        <p class="chapter-link"><a href="ask.html">Read the Ask guide</a></p>
      </div>
    </div>
  </div>
</section>

<section class="section section-queue" aria-labelledby="queue-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="queue-title">Three things it does differently</h2>
      <p class="section-lead">The queue, the evidence and the gate. Each one is a mechanism you can inspect.</p>
    </div>
    <div class="queue-layout">
      <div class="queue-panel">
        <p class="queue-label">An invented queue. The score is local policy, not a model call.</p>
        <table class="queue-table">
          <thead><tr><th scope="col">Rank</th><th scope="col">Subject</th><th scope="col">Sender</th><th scope="col">Action</th></tr></thead>
          <tbody>
            <tr>
              <th scope="row" class="queue-rank">01</th>
              <td class="queue-subject">Signed proposal for the Northwind launch</td>
              <td class="queue-sender">Atelier Nord</td>
              <td class="queue-action"><span>Decide</span></td>
            </tr>
            <tr>
              <th scope="row" class="queue-rank">02</th>
              <td class="queue-subject">Invoice 1042 needs a second look</td>
              <td class="queue-sender">Studio Lumen</td>
              <td class="queue-action"><span>Verify</span></td>
            </tr>
            <tr>
              <th scope="row" class="queue-rank">03</th>
              <td class="queue-subject">Thursday pickup window</td>
              <td class="queue-sender">Harbor Logistics</td>
              <td class="queue-action"><span>Reply</span></td>
            </tr>
            <tr>
              <th scope="row" class="queue-rank">04</th>
              <td class="queue-subject">Workshop notes and the two open questions</td>
              <td class="queue-sender">Morgan Ellis</td>
              <td class="queue-action"><span>Read</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="queue-notes">
        <div class="queue-band">
          <article class="queue-item">
            <span class="band-number" aria-hidden="true">01</span>
            <p class="card-tag">Queue</p>
            <h3>The queue is scored by a fixed local policy.</h3>
            <p>A local Go scorer ranks each message from stored signals: a suspicious message adds 100, a decision 45, a reply 30, urgency and deadline levels add fixed weights, and recency adds at most 12. A score of 20 is the floor, and a suspicious, decision, reply, urgent or deadline signal always enters. Bulk automated mail never enters, however urgent its text.</p>
          </article>
          <article class="queue-item">
            <span class="band-number" aria-hidden="true">02</span>
            <p class="card-tag">Evidence</p>
            <h3>Every label names its source.</h3>
            <p>A decision is stored as answers to named questions, with the confidence each answer carries where the question type has one and the evidence behind it. The message detail shows the fields the model read. When an index is partial, the screen names the missing part.</p>
          </article>
          <article class="queue-item">
            <span class="band-number" aria-hidden="true">03</span>
            <p class="card-tag">Learning</p>
            <h3>It abstains until your own labels certify it.</h3>
            <p>The local classifier trains one head per question on your corrections. A question is certified only when the Wilson lower bound of its answered precision reaches 0.99 on a held-out split, with at least 200 training labels, 50 evaluation labels and 0.5 coverage. Below that threshold it abstains, and the Learning screen names the stage that blocks it.</p>
          </article>
        </div>
      </div>
    </div>
  </div>
</section>

<section class="section section-screens" aria-labelledby="screens-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="screens-title">The screens, starting with Ask</h2>
      <p class="section-lead">Captured from the Linux preview with an invented mailbox. No real mail appears here.</p>
    </div>
    <div class="screens-panel">
      <fieldset class="screen-index" aria-describedby="screen-hint">
        <legend class="visually-hidden">Screen index</legend>
        <div class="screen-index-head">
          <p class="screen-index-label" aria-hidden="true">Screen index</p>
          <p class="screen-hint" id="screen-hint"><span class="screen-hint-pointer">Choose a screen. Use the arrow keys to move between screens.</span><span class="screen-hint-touch">Choose a screen.</span></p>
        </div>
        <div class="screen-list">
          <label class="screen-tab" for="screen-ask">
            <input class="screen-radio" type="radio" name="screen" id="screen-ask" checked>
            <span class="screen-num" aria-hidden="true">01</span>
            <span class="screen-name">Ask with an answer</span>
          </label>
          <label class="screen-tab" for="screen-home">
            <input class="screen-radio" type="radio" name="screen" id="screen-home">
            <span class="screen-num" aria-hidden="true">02</span>
            <span class="screen-name">Home</span>
          </label>
          <label class="screen-tab" for="screen-mail">
            <input class="screen-radio" type="radio" name="screen" id="screen-mail">
            <span class="screen-num" aria-hidden="true">03</span>
            <span class="screen-name">Mail with a message open</span>
          </label>
          <label class="screen-tab" for="screen-teach">
            <input class="screen-radio" type="radio" name="screen" id="screen-teach">
            <span class="screen-num" aria-hidden="true">04</span>
            <span class="screen-name">Teach</span>
          </label>
          <label class="screen-tab" for="screen-learning">
            <input class="screen-radio" type="radio" name="screen" id="screen-learning">
            <span class="screen-num" aria-hidden="true">05</span>
            <span class="screen-name">Learning</span>
          </label>
          <label class="screen-tab" for="screen-sources">
            <input class="screen-radio" type="radio" name="screen" id="screen-sources">
            <span class="screen-num" aria-hidden="true">06</span>
            <span class="screen-name">Sources</span>
          </label>
          <label class="screen-tab" for="screen-settings">
            <input class="screen-radio" type="radio" name="screen" id="screen-settings">
            <span class="screen-num" aria-hidden="true">07</span>
            <span class="screen-name">Settings</span>
          </label>
          <label class="screen-tab" for="screen-first_run">
            <input class="screen-radio" type="radio" name="screen" id="screen-first_run">
            <span class="screen-num" aria-hidden="true">08</span>
            <span class="screen-name">First run</span>
          </label>
        </div>
        <div class="screen-stage">
          <figure class="shot" id="shot-ask">
            <div class="screen-frame">
              <img src="assets/screenshots/en/ask.png" width="1440" height="900" loading="eager" decoding="async" alt="Mailsense: Ask with an answer">
            </div>
            <a class="shot-full" href="assets/screenshots/en/ask.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Ask with an answer</strong> Ask leads: a question and its written answer, with every citation under the text opening the message it names.</figcaption>
          </figure>
          <figure class="shot" id="shot-home">
            <div class="screen-frame">
              <img src="assets/screenshots/en/home.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Home">
            </div>
            <a class="shot-full" href="assets/screenshots/en/home.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Home</strong> The day in three columns: what needs a reply, what needs a decision, and what is coming up.</figcaption>
          </figure>
          <figure class="shot" id="shot-mail">
            <div class="screen-frame">
              <img src="assets/screenshots/en/mail.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Mail with a message open">
            </div>
            <a class="shot-full" href="assets/screenshots/en/mail.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Mail with a message open</strong> The selected message shows its sender, its body and the fields the local model read.</figcaption>
          </figure>
          <figure class="shot" id="shot-teach">
            <div class="screen-frame">
              <img src="assets/screenshots/en/teach.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Teach">
            </div>
            <a class="shot-full" href="assets/screenshots/en/teach.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Teach</strong> One question at a time. The suggestion is shown, and your answer is the label that trains the local model.</figcaption>
          </figure>
          <figure class="shot" id="shot-learning">
            <div class="screen-frame">
              <img src="assets/screenshots/en/learning.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Learning">
            </div>
            <a class="shot-full" href="assets/screenshots/en/learning.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Learning</strong> Each stage reports its own progress, and the screen names the step that still blocks certification.</figcaption>
          </figure>
          <figure class="shot" id="shot-sources">
            <div class="screen-frame">
              <img src="assets/screenshots/en/sources.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Sources">
            </div>
            <a class="shot-full" href="assets/screenshots/en/sources.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Sources</strong> The Outlook store, the picked mailbox and the folders Mailsense can read.</figcaption>
          </figure>
          <figure class="shot" id="shot-settings">
            <div class="screen-frame">
              <img src="assets/screenshots/en/settings.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Settings">
            </div>
            <a class="shot-full" href="assets/screenshots/en/settings.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>Settings</strong> The everyday settings first. The full schema stays one click away.</figcaption>
          </figure>
          <figure class="shot" id="shot-first_run">
            <div class="screen-frame">
              <img src="assets/screenshots/en/first-run.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: First run">
            </div>
            <a class="shot-full" href="assets/screenshots/en/first-run.png" target="_blank" rel="noopener">View full size</a>
            <figcaption><strong>First run</strong> The first launch writes the setup marker, names the data directory and asks for a time zone before it reads any mail.</figcaption>
          </figure>
        </div>
      </fieldset>
    </div>
  </div>
</section>

<section class="section section-privacy" aria-labelledby="boundary-title">
  <div class="shell">
    <div class="privacy-panel">
    <div class="section-head section-head-dark">
      <h2 id="boundary-title">What leaves the machine</h2>
      <p class="section-lead">Mailsense makes five kinds of network request. Each one has a reason you can see.</p>
    </div>
      <div class="privacy-grid">
        <div class="boundary-col boundary-leaves">
          <h3>Leaves, only when it runs</h3>
          <ol>
            <li>The pinned local runtime and embedding model, downloaded once and checked against SHA-256 before use.</li>
            <li>A request to a remote provider you configured, for an Ask answer or automatic triage after a sync. It carries the question and the message fields that call needs.</li>
            <li>A provider model list when you press Fetch models.</li>
            <li>One small test request when you press Test. A test is billable and is not covered by a budget.</li>
            <li>The update check: at most once per update.check.interval (24 hours by default), one request to GitHub asks for the latest published release. It carries no mail, address or installation identifier, and can be turned off in Settings.</li>
          </ol>
        </div>
        <div class="boundary-col boundary-stays">
          <h3>Never leaves</h3>
          <ol>
            <li>The mail store, attachments, indexes and learned profile. They are SQLite files in the data directory you chose.</li>
            <li>The classification, evidence and ranking code. They run as local Go code, not as a cloud call.</li>
            <li>No telemetry, no analytics and no vendor account.</li>
          </ol>
        </div>
      </div>
      <p class="note">Mailsense reads the classic Outlook profile already open on Windows. It does not ask for Microsoft credentials. A remote provider&#39;s terms govern what it does with a request you send. See <a href="privacy.html">privacy</a>.</p>
    </div>
  </div>
</section>

<section class="section section-limits" aria-labelledby="limits-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="limits-title">What it cannot do yet</h2>
      <p class="section-lead">These limits are part of the current build.</p>
    </div>
    <div class="limits-panel">
      <ol class="limits-list">
        <li>
          <div class="limit-text">
            <h3>Only classic Outlook on Windows is a live source.</h3>
            <p>It reads the profile through COM. New Outlook, Outlook on the web, IMAP, POP, the Gmail API, Microsoft Graph and file import are not connected in this build.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>The Linux preview is built from source.</h3>
            <p>It opens, renders every screen and explains the missing source. The inbox stays empty because there is no live connector yet.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>It reads and ranks. It does not act on the mailbox.</h3>
            <p>No sending, deleting, moving or read-state changes. The connector is read-only.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>Triage needs a decider.</h3>
            <p>Without one, triage answers 422 provider_not_configured. Search and the local classifier still work.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>The database is not encrypted by the app.</h3>
            <p>Your operating-system account and disk encryption are the protection. Provider keys on Windows use DPAPI.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>There is no SQL console and no diagnostics bundle.</h3>
            <p>Logs are local and readable in the Logs screen. They redact secrets.</p>
          </div>
        </li>
      </ol>
    </div>
  </div>
</section>

<section class="section section-install" aria-labelledby="install-title">
  <div class="shell">
    <div class="install-panel">
    <div class="section-head section-head-dark">
      <h2 id="install-title">Install</h2>
      <p class="section-lead">The release ships a Windows installer; the source also builds on Linux as a preview with an empty inbox, and that build is not a release download.</p>
    </div>
      <div class="install-grid">
        <div class="install-file-block">
          <p class="install-label">Windows installer</p>
          <pre class="install-file"><code>mailsense-&lt;version&gt;-setup.exe</code></pre>
          <p class="note">The version comes from the release tag. Download the file from the <a href="https://github.com/LLM-4-People/mailsense/releases">releases page</a>.</p>
          <ul class="install-facts">
            <li>Installs per user.</li>
            <li>Needs no administrator rights.</li>
            <li>Keeps the data directory when you uninstall.</li>
          </ul>
        </div>
        <div class="install-action-block">
          <p class="install-caveat">Mailsense needs the WebView2 runtime, which Windows 11 and current Windows 10 already have. The build is not code-signed, so SmartScreen may ask you to confirm the publisher.</p>
          <p class="actions"><a class="button button-light" href="https://github.com/LLM-4-People/mailsense/releases">Download the installer</a></p>
          <p><a class="quiet-link quiet-link-light" href="first-run.html">Read the first-run guide</a></p>
          <p class="install-note">The first run writes the setup marker, lists the Outlook stores and shows the resource ceilings. Nothing is read from mail before you pick a store.</p>
        </div>
      </div>
    </div>
  </div>
</section>

<section class="status" aria-labelledby="status-title">
  <div class="shell status-line">
    <h2 id="status-title">Status</h2>
    <p>Mailsense is in active development. Interfaces and stored schemas may change between releases; the changelog and the release notes call out anything that requires a migration.</p>
  </div>
</section>
