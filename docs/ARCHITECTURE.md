# Mailsense — architecture (single source of truth)

Native Windows desktop app (one `.exe`, no CLI) that syncs mail from any
connected source, triages every message, indexes it for exact + semantic
search, and answers any question about the mailbox with cited evidence.

This document is the contract every contributor (human or agent) builds
against. When code and this file disagree, fix one of them in the same change.

## Goals

| Goal | How it is met |
| --- | --- |
| Fast | Pure-Go SQLite (FTS5) local index; incremental sync; in-memory vector scan; background jobs; cache keyed by content hash |
| Lightweight | Single static `.exe` (`CGO_ENABLED=0`), WebView2 window (ships with Windows 11), nothing to install: the local embedding runtime and model are pinned artifacts downloaded on demand (SHA-256 checked) and run as a managed child process |
| Accurate | Decision-model triage with calibrated probabilities + learned priors + user labels; grounded Q&A that searches, reads and cites (deterministic intents, one cited written answer) |
| Answers many questions | Deterministic EN/FR intents for common mail questions plus a free-form answer grounded in numbered owner messages; an explicit retrieval-only mode when no written answer is available |
| Agnostic | `source.Connector` interface — Outlook (local COM) first, others plug in; engines behind provider interfaces |
| Expandable / dynamic | Registries for connectors, engines, tools, triage questions; every knob in layered config, editable from the UI |

## Stack (decided, with evidence)

- **Go 1.27**, `CGO_ENABLED=0`, cross-compiled from WSL (`GOOS=windows`). Spike
  2026-10-14: `modernc.org/sqlite` FTS5 (`unicode61 remove_diacritics 2`) +
  `go-ole` + `go-webview2` → 6.6 MB exe, ran on Windows 11 24H2.
- **Dependencies (each justified):** `modernc.org/sqlite` (storage+FTS, pure Go),
  `github.com/go-ole/go-ole` (Outlook COM), `github.com/jchv/go-webview2`
  (native window), `github.com/anthropics/anthropic-sdk-go` (Claude, official
  SDK), `golang.org/x/sys/windows` (DPAPI secret storage),
  `golang.org/x/text` (L0, the Go team's: Unicode normalization tables —
  NFKC/NFD and Unicode case folding for search folding, subject prefixes
  and body rules in every script, which the standard library lacks — and the WHATWG charset
  decoders the RFC 5322 connector needs for GB18030, Big5, Shift_JIS,
  ISO-2022-JP, EUC-KR, windows-125x, KOI8…; writing either ourselves would
  mean shipping and maintaining the same Unicode/WHATWG tables). Nothing else without
  a written reason here. (`go.mod` also lists modernc.org/sqlite's own transitive
  modules — libc, mathutil, memory, go-humanize, uuid, go-isatty, go-strftime,
  bigfft — as `// indirect`; none is imported by Mailsense code.)
- **UI:** vanilla HTML/CSS/JS embedded with `go:embed`. No build step.

## Engines (per task, each selected in settings)

| Task | Engine kind | Default | Notes |
| --- | --- | --- | --- |
| Triage | `Decider` (state + typed questions → probabilities) | record `triage.decider` (empty = not configured) | One adapter, two protocols (`systemone`, `openai_decisions`); provider records are the settings-owned `engine.providers.file` (no built-in records, strict validation, atomic API writes). Selecting a record by id; a missing record is an explicit blocker. `scripts/bench-deciders.sh` compares records on the same states |
| Semantic search | `Embedder` | record `embed.provider` (empty = not configured, then seeded from the pinned local model on first start) | Any OpenAI-compatible `/v1/embeddings` server is a record; a managed llama.cpp `llama-server` record names its `runtime` (Windows: the pinned Vulkan build; Linux dev/tests: cpu). The canary uses the model's pinned reference vector. No embedder blocks learning (embeddings stage), so the gate keeps search closed; once learned, an unreachable embedder degrades search to full text, disclosed with every answer (`docs/EMBEDDINGS.md`) |
| Q&A | `Generator` | record `qa.provider` (empty = not configured) | Deterministic intents answer common questions locally; a free-form question makes one grounded call over numbered owner messages and must cite them. A missing record/key returns retrieval-only sources with `qa_unavailable`; a failed or unusable call returns them with `qa_generation_failed` or `qa_no_grounded_answer`. Retrieval waits for a complete semantic ranking (`qa.retrieval_timeout`) and lets an incomplete index catch up, so `qa_index_warming` is reserved for a genuinely incomplete index and `qa_retrieval_timeout` for a retrieval that missed its bound. No answer is invented. No local/private generator and no streaming. |

Provider records are the one source of truth for endpoints, models, key
names, limits and prices: `engine.providers.file` is a strict JSON object
keyed by immutable record id, and there is no embedded catalog. The four
role settings (`triage.decider`, `judge.provider`, `qa.provider`,
`embed.provider`) select records by id; empty means not configured, and a
deleted record fails closed with an explicit message. Creating/updating a
record through the API validates the whole set and writes it atomically; a
record selected by a role cannot be deleted (or change kind) until that
role is cleared. Paid caches key on `Provider.Fingerprint()` (protocol,
endpoint, path, model, embedding spec), so a model/endpoint change
invalidates plans, answers, decisions and vectors while a rename does not.

Low-confidence triage answers can be escalated to the Generator (setting).

## Layout

```
cmd/mailsense/        entry point: wire modules, start API, open the desktop surface
internal/
  config/             layered config: embedded schema.json (type, default, bounds, description of every
                      setting) → user settings.json → env MAILSENSE_*; validation (all errors at once);
                      change notifications
  logx/               central slog logger: rotating file, in-memory ring for the UI, secret + content-key + email-address redaction,
                      component attribute; EVERY module logs through it
  events/             in-process pub/sub (sync/triage/index progress, log lines, settings changed); On = the one
                      subscribe-and-loop
  httpx/              the one HTTP client: logging RoundTripper, timeouts, retry policy (billing-aware)
  secrets/            API keys: DPAPI on Windows, env fallback in dev
  clock/              the one time source (tests use clocktest.Fake; no sleeps in tests); time.zone resolver
  mathx/              the one rounding rule (half-up) and shared statistics
  digest/             the one short hash (SHA-256 hex prefix)
  jsonx/              one-JSON-value decoding shared by every trust boundary
  runstate/           lifecycle states of jobs and runs + the one outcome rule
  fsx/                every file write: atomic replace (Windows lock retries), rotating log writer
  mail/               source-neutral message model, address/subject/timestamp helpers
  render/             display-only mail body: MailCanvas allowlist + per-message CSS compiler, inline-image
                      identity resolution, escaped and linkified plain text; raw stored HTML never reaches a client
  store/              SQLite schema + migrations, messages, recipients, content-addressed inline blobs,
                      FTS5 (contentless, one write path), embeddings, decisions, labels, sync cursors;
                      read-only SQL runner (validated)
  source/             Connector interface + registry; ErrUnavailable (the source as a whole cannot be read now: the
                      syncer ends the run)
    outlook/          local Outlook COM connector — all stores, all mail folders; attach only, paced at the one
                      call choke point, a breaker after lost connections (docs/OUTLOOK_SYNC.md); pure Go over
                      an Object interface, COM only in *_windows.go; fake object model in its tests
    archive/          importer for the legacy JSONL export (dev fixture + migration)
    rfc5322/          mail files: mbox (offsets, lazy reads), maildir, .eml (MIME, charsets, attachments,
                      Gmail labels and system-label roles); every decoding it must choose is declared
  syncer/             connector → store orchestration, incremental cursors, progress events
  scope/              THE owner identity (reported by the source + declared by the user, never inferred)
                      + per-message verdict (owner/shared/unattributed)
  learning/           THE app-wide learning gate: stages, status (progress, ETA, machine-readable blockers
                      with actions), latch of the learned selection
  installprofile/     learners of the install profile (shared mail domains, reply prefixes)
  psl/                Public Suffix List (shipped, pinned data) → registrable domains (and their Unicode form)
  locale/             pinned CLDR / UDHR / UTS #39 data: language identification, date/time patterns and names,
                      currency codes and symbols, confusables skeleton (docs/EVIDENCE.md)
  evidence/           layer 0: registry of structural signals per message (causal, versioned, capability-aware),
                      stored and incremental; rules (data, measured), feature view, observed facts; the "evidence" stage
  layer1/             the local per-install classifier: per-question heads on embeddings + causal layer-0 cells,
                      teachers and requirements as data, cross-fitted, calibrated, certified on held-out labels;
                      the "layer1" and "questions" stages (docs/LAYER1.md)
  selective/          the one selective/abstain module: confidence per answer kind, coverage at precision,
                      split certification, the abstained answer form (layer 1, feedback, triage uncertainty)
  route/              the certified answer route (pure): the escalation cascade layer-0 key → layer 1 →
                      SubQ, each layer answering only above its own gate chosen on the selection labels;
                      the cascade's decisions are tested once on the disjoint certification labels
                      (built and wired as the `calibration` stage)
  optim/              L-BFGS (the one optimiser)
  goldset/            dev-time gold access with the sealed test split (test support)
  threads/            header-first threading, reply matching both ways, behaviour profile (owner's zone)
  feedback/           labels, few-shot, suspicious priors, evaluation, calibration, correlations + insights as data
  goldexport/         the gold-judge sample of the whole chosen mailbox (strata, seeded draw, files)
  engine/             provider kinds, typed question/answer contract, providers as data, registry, usage
    decision/         the decision-model adapter: systemone + openai_decisions codecs, chunking, budget, retries
    embed/            OpenAI-compatible embeddings adapter (llama-server sidecar or any server)
    claude/           Generator via anthropic-sdk-go
  vector/             the one vector math: checks, Matryoshka truncation, cosine, int8 storage form, the
                      in-memory int8 set and scan shared by search and layer 1
  sidecar/            managed local runtimes: pinned downloads, verified installs, job object, health, canary, tuning
  index/              semantic index job: cleaned mail newest first, incremental, int8 vectors per space
  triage/             causal state composer, cache of raw answers, causal priors, engines, pipeline job, analytics
    taxonomy/         the profile: embedded legacy taxonomy (generated), overrides, validation, fingerprint
  search/             hybrid retrieval: FTS5 BM25 + in-memory int8 vector scan, one shared filter (owner-only
                      for Q&A), RRF, coverage
  priority/           pure deterministic ranking of owner mail from route + preview values, metadata-only
                      scoring, one bounded 2,000-char head batch for the top-200 snippets, a bounded cache and
                      the /api/priority page; also the topic/subtopic census and the decision/action reads
  subject/            the one topic → subtopic hierarchy: 11 topics, 49 closed subtopics, deterministic rule
                      precedence, evidence basis, and the FromAnswers adapter both priority and triage read
  action/             the one primary owner action, waiting direction and due source; precedence table,
                      automated-mail veto and the shared OwnerAction predicate
  threadintel/        structural conversation truth from visible messages: last mover, owner-last closing,
                      external answer, directed ask, automated/CC/attachment/OOF rules; no body text out
  commitments/        the Brief's open-loop read model: due, reply, decision and concrete action, one row per
                      conversation with the strongest kind and carried evidence
  brief/              pure composition of commitments, conversation facts and the priority census
  messages/           the Mail first-page read model: one immutable unfiltered all-mail page (default limit,
                      counts) keyed by store.MailGeneration, at most one in-flight build per generation,
                      detached from its starter (context.WithoutCancel) and bounded by a 2 min build
                      timeout so a cancelled leader still serves its waiters, shared failures not cached,
                      independent copies; every filtered page (role, store, sender, unread, cursor, other
                      limit, no counts) bypasses it. store.MessageList owns the keyset page, its
                      sender/role/store/read-state predicates and the (generation, store, sender, unread)
                      counts cache
  qa/                 gated Q&A: deterministic intents from priority, owner-only retrieval, one grounded
                      generator call with validated citations, and the local history
  jobs/               cancellable background jobs with progress + events
  api/                localhost HTTP API (+ SSE) with per-launch token; serves the embedded shell; the UI talks only to this
  ui/                 embedded shell (HTML/CSS/JS, no build step or network): screens, token handshake, SSE state (docs/UI.md)
  desktop/            the one bootstrap-URL validator; WebView2 window on Windows, non-blocking default browser elsewhere
  bootstrap/          startup error report: file in the data dir + native message box (Windows)
  buildinfo/          version stamped at link time
scripts/              build.sh, test.sh, fixture-import.sh (phase 1a); gen-taxonomy.sh, triage-live.sh (2a); bench-deciders.sh (decider benchmark); embed-local.sh, embed-live.sh (2b);
                      update-psl.sh, update-unicode-data.sh, update-prefixes.sh, update-locale-data.sh + datagen/ (pinned data, L0);
                      evidence-fixture.sh (layer 0 over the fixture); gold-seal.sh, layer1-fixture.sh (layer 1);
                      outlook-live.sh and relay/ (Windows
                      machine through the device relay: exec, transfer, detached jobs, probes; phase 1b);
                      run-windows.sh, stop-windows.sh
                      (graceful, via API) come with the API in phase 4
docs/                 ARCHITECTURE.md (this), DEVELOPMENT.md (how-to), PROGRESS.md (log + issues),
                      PORTING.md (legacy lessons), EMBEDDINGS.md (local embeddings: decision, design, measurements),
                      EVIDENCE.md (layer 0: every signal and rule), LAYER1.md (layer 1)
```

## Rules of the codebase

- **One owner per fact.** A constant, default, formula or policy lives in exactly
  one place. Every setting (type, default, bounds, description) lives in
  `internal/config/schema.json`, never in Go code.
- **Lowest choke point.** Logging in `logx`, HTTP policy in `httpx`, SQL safety in
  `store`, window predicates in one helper, cost/chunking in `engine/decision`.
- **One message read.** `store.EachMessage` owns the one visibility/owner
  predicate and the one scan of stored messages. A consumer projects the
  `mail.Message` columns it reads with `MessageFilter.Fields`
  (`MessageFieldsOf`), resolved through `messageColumns`, and declares its
  set next to its reader (threads: `threadFields` beside `FromMail`). The
  zero projection is the full read; an omitted field stays zero and the
  attachment/folder subquery runs only when asked.
- **UI render owner.** `renderRouteNow` is the one screen mount; a route
  builds its node (and may cache it) before that append. The All mail screen
  has one row builder (`allMailRowsFragment`/`appendAllMailRows`): it inserts
  the first 24 real rows before the `.mail-more` sentinel, then the deferred
  rest after a double `requestAnimationFrame`; `pendingAllMailRows` (a
  `WeakSet` keyed by the list) keeps **Load more** from racing. A detached
  cached list may receive the rest harmlessly; selection is resynced only
  when the captured list is connected and Mail is the current route. Round
  41 measured a 40 + 36 one-batch-per-frame variant of that deferred append
  (frames 24/64/100; deferred long-task max 53 → 0 ms) and reverted it
  because first-visible messages→list rose 86 → 97 ms (paired median +5)
  and click-to-list 94 → 102 ms (+13); the single deferred append remains
  the owner. The All-Mail model carries one transient `morePromise` that
  `allMailSnapshot` never stores: `loadMoreAllMail` settles it in a single
  `finally`, and `renderAllMail` waits for it before a same-filter background
  refresh rebuilds, so the refreshed screen is built from the settled rows.
  `prefetchRoute` refuses the default Mail All destination while any All-Mail
  route is mounted, so the idle warm cannot replace `state.allMail` or
  disconnect the list's one observer; a hover after leaving All retries
  through the existing once-only marker. The Hub→Mail first-click prefetch is
  unchanged. The filtered-All→unfiltered-Mail nav click no longer prefetches;
  it is cold only when the bare `#/mail` route-cache entry is absent, evicted
  or invalidated (the test forces that with `config.changed`), and is a warm
  cache hit when the entry survives.
- **Everything observable.** Every module with runtime behaviour emits events
  and logs (config, logx, jobs, syncer, store, secrets, httpx, source registry);
  pure modules (mail, scope, jsonx, runstate) have nothing to report. The UI
  exposes settings, signals and actions for each.
- **Logs carry no owner content by default.** `logx` is the one redaction owner: registered
  secrets and the built-in token/email patterns (bearer/basic, `sk-`,
  `key=value`, email) are always applied to every message and attribute, and
  `log.redact.patterns` only adds custom expressions (its schema default is
  empty; a custom list cannot replace the built-ins). One canonical content-key
  set (body, subject, sender/recipient/address/email, snippet, content,
  question/answer, mailbox/store names, series subjects, headers) is denied
  whatever the value's type; the whole key is normalized (`.`/`-`/space to `_`,
  lower case) and checked together with its dot segments and adjacent pairs, so
  `display.name`, `mailbox.name`, `store.name` and `mail-subject` cannot split a
  compound. Call sites log stable ids, classes and counts instead (store key,
  `address_count`, series digest, alteration code and detail size); the triage
  taxonomy logs `question_key`. The Logs UI shows the stored `[REDACTED]`
  marker and does not re-redact. `http.log.body_max_bytes=0` keeps request/
  response bodies off; if an operator explicitly enables that bounded
  diagnostic switch, its `request_body`/`response_body` attrs are the one
  disclosed exception and still pass the secret/pattern redactor.
- **No silent degradation.** When something is missing (classification, embeddings,
  a source), answers and UI say so.
- **Nothing changed or dropped silently.** A connector that must choose a
  decoding, keep part of a malformed body or take a value from another
  field declares it (`mail.Alteration`): stored with the message, counted
  per code in the sync run, logged. No fallback ever substitutes data.
- **Never destroy good data on a failed run.** A job that produced nothing keeps
  prior results.
- **Tests:** table-driven, deterministic, no network; fakes for COM, HTTP and
  engines; misuse cases (bad config, junk API payloads, cancelled jobs, locked DB)
  tested alongside the happy path. Every bug fixed gets a regression test.
- Files stay small and single-purpose; no package-level mutable globals except
  registries.

## Mailbox selection, identity and the learning gate (L0)

- Nothing syncs until the user picks mailboxes (`mailboxes.selected`, empty
  by default). Discovery lists every store (key, kind, default, cached,
  counts, online); none is ever preselected. On a new install only — no
  discovered mailbox and no selection — one best-effort discovery lists the
  sources' stores in the background after the API is announced; it never
  reads mail, picks one, or repeats on a normal restart. Picking starts one
  sync job per source with a picked mailbox: the job records that store's
  reported owner identity before it reads mail, so mail and owner addresses
  arrive without a second owner action. A picked store that cannot be
  read is reported and waited on, never replaced. The selection applies
  at the store's choke point at once (`store.visibleSQL`: active folders
  of picked stores): an unpicked store's mail leaves search, vectors,
  scope, labels, triage and the learners immediately and is kept for a
  re-pick.
- The All-mail page (`store.MessageList`) takes one optional sender filter:
  one exact address or one domain, plus one optional read-state filter
  (`Unread=true` adds `m.is_read = 0` to the page and its role counts;
  false/absent is every read state). `mail.NormalizeSenderQuery` owns the
  validation and the canonical form (trim, Unicode lower case, at most 320
  runes, any control character (checked after trimming), not a lone `@`); `store.senderWhere`
  is its only predicate and `/api/search` uses it too, so there is no second
  sender rule. The SQL compares the `mailsense_lower` scalar
  (`mail.NormalizeAddress`) rather than SQLite's ASCII-only `lower()`, so a
  non-ASCII local part is compared in the same lower-case form Go produces. The page and its role counts carry the same condition;
  the counts cache key is `(mailbox generation, store, sender, unread)`; the
  keyset cursor carries the normalized sender and the read state and is
  rejected under another role, store, sender or read state. A filtered page
  follows the `messages_instant` keyset order and filters each row: there
  is no `is_read` or sender expression index yet, and a p95 above 150 ms
  would justify one as a follow-up migration. The one-unread page on the
  33,842-message prepared store measured p50 30.99 / p95 37.46 ms after the
  change (five fresh-copy runs), under that bound.
- The local embedder is not a hidden preset or fallback. The pinned runtime
  and model data (including the complete embedding spec and
  `chars_per_token`) live in `internal/sidecar/runtimes.json`; on first
  start the app writes one ordinary provider record (`local-embedder`) to
  the owner's `engine.providers.file` and selects it only while
  `embed.provider` is at its default. An explicit owner value (including an
  explicit empty), an existing equivalent record or an id collision is
  never overwritten.
- A store's owner is what its source reports for the picked mailbox plus
  what the user declares (`mailboxes.declared_addresses`, read from the
  settings only and joined in `store.Owners`). No inference: without
  either, its mail gets no verdict and learning is blocked. A reported
  identity is recorded only from a complete read of an online store: an
  offline or partly read store keeps its recorded identity and verdicts.
- One picking/identity query (`store.MailboxView`) and validated actions
  (`syncer.Pick`, `Unpick`, `Declare`, `Undeclare`), `learning.ConfirmZone`,
  `config.Reject`/`Unreject` for learned values; blockers name their
  actions (`learning.Action`), never prose.
- ONE gate for the whole app (`internal/learning`): no triage answer, no
  search, no Q&A until the picked mailboxes are fully learned — every stage
  done (mailboxes, machine facts, sync, evidence, embeddings, labels,
  layer 1, calibration, questions); stages not built yet (labels,
  calibration) are pending, a
  stage that cannot finish reports a blocker with reason and remedy; the
  gate never relaxes itself. Once a selection is learned it stays open
  while learning continues; a strict subset of it stays open (unpicking
  keeps the rest learned); any store added closes the gate until the new
  selection is learned as a whole. The gate answers from a cached status
  refreshed by every sync, discovery, index run and settings change. The
  explicit, reversible `learning.test_mode` preview is not a second gate:
  `Allow` unlocks the automatic answer screens for testing, while `Open`
  and `Status` keep reporting the real closed gate and nothing is accepted
  or certified.
- ONE status build reads each stage and each named fact once
  (`learning.StatusOf`, `learning.Memo`): the machine's build installs one
  per-build context memo, and every stage (including a stage another stage
  reads as its "after" dependency) asks through it. The stage key is the
  stage type plus its id — the machine already refuses duplicate ids among
  its top-level stages, and the route's layer-1 stage is the layer-1 stage
  — so the index coverage is planned once instead of once per path. The
  same memo shares the facts a stage reads, keyed by fact type and name
  (the layer-1 plan, last run, last succeeded run, fingerprint, labels and
  route certifications), so the second stage that needs one reads it from
  the build instead of the store. The memo never outlives the build: there
  is no cross-build cache, a fact written mid-build is seen by the next
  build, and a direct stage read outside the machine falls back to the
  stage's own `Status`.
- Learned per install (the install profile, a config layer between the
  shipped defaults and the user's overrides): shared mail domains, reply
  prefixes — each with sample size and provenance, queryable.
- Agnostic foundations: registrable domains from the Public Suffix List
  (shipped data, `scripts/update-psl.sh`; IP literals are their own key);
  scripts (the CLDR spaceless set and the hand-kept `substring_extra`), fold
  letters and question marks are generated from pinned UCD/CLDR
  (`scripts/update-unicode-data.sh`), subject prefixes from a pinned
  Wikipedia revision (`scripts/update-prefixes.sh`), each with a pin test;
  message kinds and RFC 5322 format data are data files; folding is NFKC +
  case folding of the whole string with marks stripped by the base
  character's script; search has a substring index for scripts searched by
  substring — the spaceless scripts plus Hangul and the matra-based Indic
  scripts, whose dependent vowel signs unicode61 treats as separators — and
  short terms are scanned on the folded text of the same fields; folder
  roles are one schema vocabulary and a message's role rule reads every
  folder it is in; connectors declare capabilities that the layers read.

## Priority and Q&A

Both read surfaces are gated by the one learning machine and both see owner
mail only; `/api/search` keeps its shared-inclusive default.

Search snippets are cut from the `messages.body` already hydrated with the
page, never from FTS `snippet()` and never with another SQL query. A
full-text hit anchors on the first body match: word mode compares whole
folded FTS tokens (a punctuation-joined or quoted multi-token term must be
adjacent), substring/scan mode keeps substring matching, and a one-rune
scan term is dropped when a longer term exists. A hit that matched only
the subject, sender or recipients has an empty snippet; a semantic-only hit
keeps a cleaned-body preview, which is a preview rather than a matched
excerpt. Snippets are plain text (no markup) and use the one
`search.snippet_chars` budget.

**Priority** (`internal/priority`) is a pure scorer over typed values. The
store streams visible owner messages that have route answers under the
current route key (the full configuration identity: profile, teachers,
requirements, decider, rules, algorithm and settings, not only the questions
fingerprint), with a left join on the same message's preview
row (`internal/store/priority.go`). Two passes build one ranking: the reply
pass reads the route/preview answers without any body (`EachPriorityReply`),
and the scoring pass reads metadata only (`EachPriorityItem`). The ranking
reads message metadata, `map[string]route.Answer`, the optional
`map[string]engine.Answer` preview, the received instant and the clock; its
scoring does no body I/O and no body text reaches `Score`. After the top-200
order is known, one `PriorityHeads` query reads the first 2,000 characters,
the truncation flag and the revision of those 200 ids through the same
`visibleSQL`/`ownerContentWhere`/`threadContentRoleWhere` boundary; a
missing head or a revision mismatch leaves that snippet empty. A truncated
head that cannot fill the 180-rune snippet (a body beginning with more
whitespace than the head) reads that one full body through `MessageBody`.
The cache holds only the snippets, never the bodies. `triage.ApplyPreview`
is the one substitution rule: a route answer is replaced only when it
abstained because the route is uncertified and a valid preview answer
exists. Certified, `below_target`, `no_answer`, refused and absent answers
are never overridden, and a malformed preview fails the read. The score
table (security, decision, deadline, urgency, reply/question/action,
category, effort/request count and recency) is documented at the package;
`docs/API.md` documents the exposed signals, filters and entry rule. It is
one policy, not a setting per weight. A
bounded heap keeps the best 200 while the whole stream is counted for the
per-filter totals; if a row appears between the two passes, the build
replays the union and restarts the heap, topic rollups, scan total and
filter counts together, so every row is counted once. The service caches
that view for 15 s per preview flag.
The priority page exposes thresholded signals (`urgency`, `reply_needed`,
`suspicious`, `decision_needed`, `deadline`, `effort`, `request_count`,
`is_question`, `call_to_action`, `info_sufficiency`, `category`, `asks_for`,
`stance`, `relationship`), the derived `subject` path, the `action`
classification, reasons and a 180-character plain snippet, never a body or
an HTML field. The same full stream also feeds the whole-mailbox topic
census the Brief reads: every owner message with a usable topic answer is
counted once, including mail that never ranks, with a nested subtopic
breakdown and evidence.

`internal/subject` is the one subject hierarchy. The model's `topic` stays
the parent; the child is derived deterministically from the message's other
answers (`asks_for`, then `decision_needed`, then `category`, then
`reply_needed`, then the topic default). `subject.FromAnswers` reads the raw
route answers with the profile's own yes/no cutoff, and both the priority
ranking and the triage detail call it, so a configured threshold cannot
make the two screens disagree. A topic added by a user profile keeps its
label and has no child; the catalogue is closed for the built-in topics.

`internal/action` is the one semantic consequence. It reads the thresholded
signals plus the structural thread assessment and returns the primary owner
action (`reply`, `decide`, `pay`, `schedule`, `send`, `do`, `verify`,
`follow_up`, `wait`, `read`), the waiting direction (`owner`, `other`,
`unknown`, `none`) and the due source (`stored` or the classifier level).
Automated bulk mail is read-only whatever its text asks. A thread that needs
no reply does not erase a concrete payment or decision ask: the message
rules still run, and the waiting direction follows the concrete action. The
priority Reply/Decision filters and the commitments decision/action sources
read this result; no screen or service re-derives it.

`internal/threadintel` owns the structural conversation truth. `is_question`
is deliberately undirected, so it never creates an owner obligation by
itself; `reply_needed` and a dominant `answer_question` ask are the directed
inbound evidence. An owner-last message that asks nothing is a closing note,
not `awaiting_other`; an inbound message after the owner's own request is
`answered_by_other`; automated mail, a CC-only copy, an attachment-only move
and an Outlook out-of-office item have explicit rules. Shared-mailbox rows
still affect the state without exposing content, and a partial-visibility
thread stays `unknown` instead of claiming a reply.

**Q&A** (`internal/qa`) has four collaborators: the priority ranking, the
owner-only search, the local history store and the configured generator.
The intent parser folds case and accents and recognizes EN+FR keywords for
reply, urgent, deadline, security, decision, workload, priority,
summary_today, sender and freeform. Structured intents read the priority
filters (counts plus the top five citations) and never call a provider.
An explicit aggregate request ("Which emails did I approve?", "Combien
d'échéances ai-je approuvées ?") keeps that deterministic answer, while a
question about the act recorded in a message ("Which deadlines did I
approve?") is freeform and reaches the planner. When `qa.provider` is
empty, a plain count whose wording uses the declared count vocabulary
(`count`/`counts`/`nombre`/`total`/`combien`/`amount`/`quantity`/`quantité`,
the phrases `how many`/`how much`/`number of`/`nombre de`/`amount of`/
`quantity of`/`quantité de`, and bare `number` immediately before a
category or mailbox noun) and contains exactly one fallback
category (security, decision, deadline, urgent, reply or workload) falls
back to the same structured priority totals instead of the unavailable
planner ("How many deadlines do I have?"). The scope gate is fail-closed:
after the count cue and the one category word are removed, every residual
token must be in a small explicit filler/question/mailbox-noun allowlist,
so any unlisted scope, topic, status or date word keeps the question
free-form. The gate runs before the intent switch for every request with a
recognized count cue, not only "how many" wording, so "Number of…",
"Total…", "Amount of…", "Quantity of…", "Quantité…" and French "Combien…"
cannot parse directly to a category and bypass it. The fail-closed scope and
person checks apply to every recognized cue. The allowlist has no
third-person pronouns or `an`, and a `does`/`did` subject is
rejected unless it is first/second person or a mailbox noun; a category
noun in the subject position is a person frame, so "How many deadlines does
Deadline have?" and "How many urgent messages does Urgent have?" stay
free-form. A repeated cue or category noun before `has`/`have` is also a
person frame ("Amount of deadlines Amount has?"), while "do I/we/you have"
keep their plain-count path. A bare repeated category noun without an
auxiliary ("How many deadlines Deadline?") is kept free-form too.
For a recognized cue, a count that names an organization, or a person the
contact book resolves, keeps the existing free-form/company path; a failed
company or person read also keeps it free-form, never a mailbox-wide total.
The strict person read ignores the count cues themselves, so a cue word
cannot match a sender name by substring; a category noun in the
person-subject position remains a mention so a contact named Deadline or
Urgent can still scope the freeform follow-up, and explicit addresses remain
terms. The freeform resolver for a recognized count uses the same filtered
mentions as the strict gate. The fallback is
conservative and does not claim complete name or pronoun coverage: any
unrecognized scope or person token in a recognized count stays free-form.
Company-scoped counts
still need a generator when the profile cannot answer them. There is no
priority fallback: `priority` is not a priority filter, so a "priority" count is not
answered from the unfiltered total; a count parsed directly as `priority`
(for example "Combien de messages importants ?") is kept free-form too. A
yes/no necessity question
("Do I need to decide?", "Faut-il approuver cette facture ?") has no wh-word
and keeps its deterministic decision intent; advice ("Should I pay this
invoice?") is freeform because the planner must read the message. `sender:`
uses a waiting
owner-only search: an incomplete index or an expired retrieval bound returns
`retrieval` with `qa_index_warming` or `qa_retrieval_timeout`, never a false
"no message from this sender". A free-form question resolves the person or
organization it names before planning: one bounded read over visible owner
mail participants (received sender, sent To/Cc) plus the picked address books
returns exact normalized addresses, and the organization book's labels and
domains return every distinct company match. The read uses the same owner,
shared/delegate, draft/deleted/junk and owner-identity boundary as the other
owner-facing reads and costs no provider call. A single candidate becomes a
`Resolved entity` block in the prompt and an exact `contact_addresses` scope
on any plan that names the person; a first name is never executed as a
substring, and a question term matches one whole name or address token rather
than an inner fragment (`liste` is not `listening@…`). Several exact
candidates in a singular question return `mode: clarify` with localized,
complete follow-up questions whose visible label carries the exact address
or domain, so clicking one resolves that candidate instead of asking again;
the response also carries structured candidates (name, address/domain,
message count, follow-up) so the screen shows the identity rather than
repeating the prompt. A plural role noun ("clients", "fournisseurs") is a
set and never clarifies. Same-name addresses merge only through one
address-book entry or one registrable domain; unrelated same-name people stay
separate. A plural people list filtered by a subject is a computed topic
ranking: one owner-only retrieval over the receiving folders, grouped by exact
sender, ranked by matching message count, with name, address, count and one
representative citation per contact; the retrieved hit set is never cached as
a replayable plan, and company rankings keep the lexical company executor. A free-form
question then retrieves `qa.max_messages` owner messages (merging the three
best priority items
when one or two hits are thin, never when search has no hits), numbers them
with subject, sender, date, a 600-character plain snippet and the priority
signals, and makes one call through the configured `qa.provider`. A
question that names one known organization is scoped to that
organization's domains and also carries the earliest message of its five
most recent conversations' bounded page, so a specific fact is answered
from the mail that the mention scopes rather than from the organization's
profile. The
profile answers only when the question is about the organization itself:
its closed dimension vocabulary accepts message/people/conversation
counts, waiting states, last contact, commitments and events; any other
content word sends the question to retrieval. The system prompt requires an
answer only from those messages, a `[n]` citation for every claim, an
explicit "not enough information" answer, the requested language and strict
JSON `{"answer":"…","used":[1,2]}`; `used` indices are validated. A
summarization request that names one resolved entity bypasses the planner:
the store finds the newest owner-visible conversation by the person's exact
addresses (or the company profile's newest recent conversation), reads at
most 20 messages through `ThreadMessages`, fetches their owner-safe bodies and
details, and calls the same generator with a dedicated summary prompt (only
those messages, at most 120 words, subject/who said what/decisions/open
items/next action, `[n]` on every claim). The result is `mode: generated`
with the thread messages as citations, never a computed count. A
summarization request without a resolved entity may still use the planner,
but a returned `messages` plan is rejected and falls back to retrieval. The
call is bounded by `qa.call_timeout` and `qa.budget.*`, and
its usage goes through the ordinary engine sink. A missing key or an
unusable provider returns mode `retrieval` with `qa_unavailable`; a refused
budget, a timeout, a failed call or an unusable answer returns the same mode
with `qa_generation_failed`; an answer that loses its grounding returns
`qa_no_grounded_answer`. A computed plan returned for a content-action
question ("Which deadlines did I approve?") is rejected the same way, because
a list cannot state the act. Retrieval is complete before the generator is
called: a waiting search (`search.Query.Wait`) finishes the semantic leg,
merges the vectors an index catch-up writes and waits for coverage inside
`qa.retrieval_timeout`, so a warm mailbox is never reported as warming because
one 150 ms interactive budget expired. `qa_index_warming` now means the index
is genuinely incomplete after that wait; `qa_retrieval_timeout` means the
retrieval bound itself expired. Neither is persisted to history, because a
failed retrieval is not an answer. The sender intent uses the same waiting
search and the same two notices on a zero-hit degraded retrieval, so it never
shows "no message from this sender" when the mailbox was not fully searched.
A grounded written answer then passes one post-grounding language check
(`internal/qa/language.go`, `finalizeGeneratedAnswer`), called at the three
generated-prose exits: the planner's JSON answer, the grounded prose fallback
and the entity summary. Quoted reply lines are stripped with
`mail.CleanBody`, an answer with fewer than `qa.answer_language.min_letters`
letters stays unjudged, and a confident language other than the requested
`en`/`fr` sets `answer_language`. The check is notice-only: it never changes
the answer, citations, counts, mode or provider/model, never retries,
rewrites or falls back to retrieval, and never overwrites an existing notice.
A noticed answer is saved to history but is not cached in memory.
A degraded retrieval carries the retrieved citations when there are any. A
zero-hit free-form question returns `no_matches` with no citations and no
priority substitution; a healthy zero-hit sender search stays `structured`
with no citations, because the sender list itself is the answer. An answer is
never invented.

Every successful answer (all modes) is persisted in `qa_history`
(migration 0016) with the newest 100 kept. The history stores only the
citation's message id and reason; subject, sender and received instant are
rehydrated from visible owner mail, so shared, unpicked, removed or missing
mail cannot resurface through an old answer. Questions, answers, snippets
and addresses never enter the log metadata. HTTP body logging is off by
default (it can capture a prompt and its mail content when enabled for a
bounded diagnosis). The Q&A generator is built by `qa.NewSource` from the
same catalog/registry as the judge, with `qa.max_answer_tokens` as its
answer budget; a reasoning provider's effort allowance is added within the
record's declared output reserve, so the call cannot spend its whole cap
thinking and return no answer. A conversation summary asks for prose
(`engine.GenerateOptions{Prose: true}`) instead of the planner's strict
JSON mode; no new provider key or dependency exists.

## Data location

`%LOCALAPPDATA%\Mailsense\` (overridable): `mailsense.db`, `logs\`, `settings.json`,
`install-profile.json` (learned values),
`runtime\` (the downloaded local runtime builds, the model and `state.json`: thread tuning, canary reference).

## Phases

1. Foundation: config, logx, events, httpx, secrets, mail, store, source (archive +
   outlook), syncer, scope, scripts, docs.
2. Engines + triage: engine registry, decision adapter, embedder, triage pipeline,
   threads/behavior priors, indexer.
3. Search + agent: hybrid search, agent tools, Claude generator, streaming answers.
4. Desktop: API, SSE, UI (Ask, Triage, Search, Insights, Sources, Settings, Logs), window.
5. Hardening: end-to-end on the 33,842-message fixture, Windows smoke test, review
   rounds, parity check with the Python app, then removal of the Python code.

## Decision changes

- 2026-10-14 (phase 1a): `defaults.json` became `schema.json`. Keeping a
  setting's default apart from its type, bounds and description would split
  one fact across two files. Owner identity moved from `mail/` to `scope/`,
  so the codebase has one identity function (PORTING §3). FTS5 is
  contentless (`contentless_delete=1`) because a content copy cost 124 MB of
  435 MB on the fixture. Search snippets will be built from `messages.body`
  instead of FTS `snippet()`.
- 2026-10-14 (review round 1a): one injectable `clock` replaces direct
  `time.Now`/`time.Sleep` in production code, so tests use a fake clock
  instead of sleeping. The rotating log writer moved from `logx` to `fsx`
  (the file-write choke point). `log.redact.replacement` was removed: one
  `config.SecretMask` marks every hidden value. The `mail.subject.*` settings
  were removed because nothing reads them before phase 2 threading, and that
  phase brings them back with its consumer. `SubjectNormalizer` itself stays
  as the phase-1a-specified pure helper. A source syncs one run at a time.
- 2026-10-14 (phase 1b): Outlook sync is snapshot-driven, not DASL-driven.
  A three-column id/stamp table per changed folder is diffed against the store
  (the sink's `Snapshot`), so incremental needs no locale-sensitive date
  filter, and the same list gives moves and deletes. Messages arrive in two
  steps: a table pass writes `Partial` messages (metadata searchable in
  minutes), and the syncer's detail pass completes them newest first through
  `source.DetailReader`. A partial write never erases stored details. Scope
  runs per mailbox store, and `owner_identities` is keyed by store. A
  delegate store's owner is the mailbox Outlook reports, and the user's
  identity is never learned from it. A folder that fails no longer aborts a
  run. Only output failures (the database cannot write) and cancellation do,
  because one offline delegate mailbox must not stop the others.
- 2026-10-14 (review round 1b): change stamps are opaque and full precision
  (`Message.Stamp`: Outlook = modification time to the millisecond + message
  flags). The snapshot backstop (`outlook.sync.recheck_after`) re-snapshots
  every folder that often, because no folder property reveals two read flips
  or a change replicated with an older time. Removals pass a safety valve
  (`sync.removal.*`): a mass removal is held and visible
  (`sync.removals_held`, `Store.HeldRemovals`). It is applied once it has
  persisted, or when confirmed (`Syncer.ConfirmRemovals`). Folders that are
  no longer discovered are probed (`source.FolderProber`): gone ones are
  removed through the valve, still-existing ones (disabled store, excluded
  folder) become inactive. Their mail is kept outside scope and statistics,
  and enabling them again resumes incrementally. One phase vocabulary
  (`source.Phase`) serves connector and syncer events, and events carry ids
  (store key, folder id), never localized names alone.
- 2026-11-03 (sync cursor atomicity): folder cursors are staged during the
  folder pass and committed in one transaction only after the end-of-run
  removal pass succeeds. A run that fails or is cancelled before
  that pass commits none of them, and a removal failure leaves every
  previous cursor in place, so the next run re-reads the folders and retries.
  Per store, a failed folder or an incomplete snapshot makes the view
  partial: that store's message removals and all its cursors are skipped (an
  id absent from one folder may have moved into the folder the run could not
  read), while other stores proceed. A folder discovery no longer returns is
  still probed, and its message removal obeys the same per-store gate: with a
  failed or incomplete sibling it keeps its rows until a complete run, while
  a store whose folders are all gone (no partial view) is removed. A crash
  before the cursor transaction
  loses only staged cursors and re-upserts idempotently; a crash after it is
  safe because the removals already ran.
- 2026-10-14 (phase 2a): triage states quote a PINNED, banded behaviour
  snapshot (versioned, with hysteresis) — superseded the same day by causal
  states (review round 2a, below), never live counts: the legacy states embedded live totals, so any new
  mail re-billed the whole archive. Priors still use live statistics,
  blended at read time (free). Few-shot labels are live: a label is a
  deliberate change. A message's own reply and own label are never in its
  state (legacy leaked both). The cache keys on fingerprint AND decider
  (record id/model/fingerprint), so answers of another model or endpoint are never mixed in. The
  taxonomy is data (`triage/taxonomy/default_profile.json`) generated from
  the legacy source; its fingerprint stays `9c9956169b0c`. One rounding
  rule (`mathx.Round`, half-up) replaces Python's banker's `round()` and
  `int()` truncation (p90 index, score levels). Trust-boundary numbers are
  JSON numbers only (legacy accepted numeric strings). Observed facts give
  the arrival in the owner's zone (legacy: UTC). Triage covers owner mail of
  `triage.stores.enabled` (default primary; replaced in L0 by the explicit pick) and `triage.folder_roles`
  (drafts, deleted and junk out). Low-confidence answers only flag a result
  for the future Generator. `StoreKey` moved from `source/outlook` to
  `source` (one key for every per-store switch). The exe now links
  net/http and the embedded IANA zone database (11.3 MB).
- 2026-10-14 (review round 2a): the pinned behaviour snapshot is gone;
  triage states are CAUSAL instead: every context block (standings, reply
  examples, label examples) is computed from data dated strictly before
  the message's own instant (`mail.Instant`), bucketed, without the message
  itself, and the arrival fact is UTC. Later mail, replies, labels and
  `time.zone` can therefore never change an existing state (proved by
  tests), and the message's own outcome never leaks into its state or its
  priors. Few-shot labels come only from the same sender or domain (the
  global subject overlap is gone). Partial answers are cached and
  completed. One triage run at a time (in-process lock + store lease). The
  decider is built per run, so `triage.decider` and the providers file
  apply without a restart. The never-retried statuses live once, in the
  schema (`not_in` of `http.retry.statuses`), enforced by `httpx.Policy`
  and provider validation. Escalation is role-aware profile data
  (`mailsense_profile.json`), calibrated on the legacy answers.
- 2026-10-14 (phase 2b): local embeddings run on llama.cpp's
  `llama-server` as a managed sidecar instead of a user-installed Ollama
  (evidence: `docs/EMBEDDINGS.md`). The runtime builds and the model are
  pinned data (`internal/sidecar/runtimes.json`: URL, size, SHA-256),
  downloaded on demand, verified before every launch, bound to loopback
  with a per-launch key, and tied to the app by a job object. At that time
  cpu was the default build; GPU builds served only after the canary.
  Vectors are int8 (recall@10 0.98 vs float32, a quarter of the size), one
  per message per space, scanned exactly in memory; hybrid search fuses
  BM25 and similarity with RRF and always discloses coverage. Every message
  write increments `messages.revision`, the exact change marker for
  derived data. The triage budget reserves each call's ceiling before
  sending it (`engine.Reserver`, `reserve_tokens_per_byte`), so parallel
  workers can never overshoot `triage.budget.*`.
- 2026-10-15 (review round 2b): `sidecar.variant` defaults to `auto`: the
  platform's auto list (catalog data) was probed once; on a platform with a
  cpu build a GPU build was used only when it passed the canary AND beat
  cpu by `sidecar.auto.min_speedup`, while a platform without one kept the
  first candidate that passed. The choice and every probe are persisted, as
  are broken builds (a failed canary or start, a crash loop). The runtime
  catalog owns the per-input context and the model revision (part of the
  vector space). Children get an environment allowlist and must own their
  port. A message that keeps failing is quarantined until it changes; an
  index run follows every successful sync (`index.after_sync`). One median
  (average of the two middles) and one percentile rank (legacy's half-even
  p90) in `mathx`, used everywhere; the urgency prior is legacy's again
  (inclusive at 0.1 minute).
- 2026-10-15 (phase L0): mailbox selection is explicit (`mailboxes.selected`,
  nothing by default; the `primary` key, `outlook.stores.enabled`,
  `triage.stores.enabled`, `index.stores.enabled` and `scope.owner_address`
  are gone; keys hash source and store id). The owner identity is reported
  by the connector (Outlook: primary SMTP, SMTP/X500 proxies, DN; accounts
  delivering into the store) or declared; Sent-folder inference and every
  fallback are gone; scope is incremental. One learning gate guards every
  answer (search, triage reads; Q&A in phase 3). The install profile is a
  config layer. Folding, scripts, kinds, prefixes and facts are Unicode-
  and data-driven; a trigram FTS index serves substring-searched scripts
  (the spaceless CLDR set plus the kept `substring_extra`); facts are
  measured on the cleaned body and dated in the sender's offset; an empty
  `time.zone` is unconfirmed (no silent UTC). The RFC 5322 connector adds
  mail files, attachments and multi-folder membership. A missing embedder
  is not a startup error (it blocks learning instead).
- 2026-10-15 (review round L0): the store is the choke point of the
  selection (`folders.picked` follows `mailboxes.selected` before every
  read; `Generation` includes it), the gate latches the learned selection
  as a set (subset rule above), identities are replaced only from complete
  reads, declared addresses have one home (the settings), blockers are
  machine-readable actions, and connectors declare every alteration
  (`mail.Alteration`, migration 0007). Full-text writes are batched after
  the batch's other writes (an FTS insert followed by another statement
  made SQLite flush one-message FTS5 segments: a quarter of the import's
  CPU). Fold is NFKC + Unicode case folding of the whole string with marks
  stripped by the base character's script (`ftsTextVersion` 3, formerly
  the single `ftsVersion`: re-index at open). `golang.org/x/text/cases`
  (same module) gives the case folding.
- 2026-10-15 (layer 0): the evidence engine (`internal/evidence`,
  `docs/EVIDENCE.md`) is the learning stage `evidence`. Structure is
  measured, meaning is judged: 72 signals, each a small registered
  extractor declaring its version, type, the source capabilities it
  needs (missing → unavailable, never guessed), whether it is causal
  (only causal signals feed features, facts and rules; the owner's later
  actions are not) and the settings it reads (part of its effective
  version). Values on a cut input are partial. Locale knowledge is pinned
  data generated by `scripts/update-locale-data.sh` (`internal/locale`):
  language profiles trained on UDHR translations plus CLDR exemplar
  letters, CLDR date/time patterns and names (dates resolved in code
  against the message's day in the owner's zone), currency codes and
  symbols, UTS #39 confusables; no word list anywhere. Rules are data
  and act as answer keys only when their precision, measured on the
  install's own judge labels, has a Wilson lower bound ≥ a setting (no
  judge yet: evidence only). Observed facts in the triage state are
  behind `triage.facts.evidence`, off (they would change every state).
  Connectors declare four more capabilities (read state, flags,
  categories, last verb); Outlook's and the legacy exporter's header
  cuts are disclosed per message (`headers_truncated`); Gmail's Unread
  label is the read state.
- 2026-10-15 (review round layer 0): causal evidence reads the **causal
  view** of the mailbox (`threads.CausalRoles`: owner-scope mail of every
  folder but drafts — filing is the owner's later action), and
  conversations grow in time order (each message keeps the members and
  reply pairs known at its own instant), once in `threads`; triage keeps
  its filed view until the migration of paid answers is decided
  (`triage.causal_threads`, off). A stored message-only value is reused
  only under the same revision and context digest (verdict, owner,
  capabilities, organisations); signals declare the settings and pinned
  data they read (their digests version them); values of a message whose
  details are pending are unavailable, never zeros; what a cut input can
  overstate is declared per signal. One run of a kind at a time without
  losing a trigger (`jobs.Single`), worker panics as item errors
  (`jobs.Safe`). Locale data joins each language's regional locales and
  calendars (CLDR): an ambiguous numeric order or a calendar without an
  exact conversion is disclosed, never a wrong date; separators come from
  the data. `importance` is the sender's (transport headers); the
  property is the owner's (`importance_mark`).
- 2026-10-15 (Outlook safety and the lead's decisions on the layer-0
  review): after Outlook crashed twice under sync load, the Outlook
  connector only attaches (starting Outlook is the user's action), paces
  every call at its one choke point (a share of Outlook's UI-thread time
  and a call rate, lower while the user is active) and keeps a breaker in
  `Connector.op` (growing cooldown, Outlook's minimum uptime, reduced pace,
  a stop after repeated losses that the user resumes); a lost connection
  is a `source.ErrUnavailable`, which ends the whole sync run; the detail
  pass defers mail received moments ago and can be capped per run; the
  live harness refuses a young Outlook and reports a crash, never
  reruns. Triage reads the causal threads view only
  (`triage.causal_threads` and the filed-view history removed; no paid
  answer existed). A year-less all-numeric date is a resolved date only
  if no region of its language writes numeric dates in the other order.
  `mail.CleanBody` cuts attribution lines before a tab-indented quote
  (Outlook's rendering of quoted HTML), structurally.
- 2026-10-15 (review of the Outlook crash-safety round): every call into
  Outlook is paced at the guard's one call path, the final `Release` of
  every object included, and no wait exceeds `outlook.pace.max_wait`
  (computed in float); the breaker's factor has a floor
  (`outlook.breaker.min_factor`); any connect to an Outlook younger than
  `min_uptime` (the first one, after a resume) runs at the reduced pace
  until it is old enough, never refused; a lost connection while
  attaching is a loss; a call still busy after its retries ends the run
  (`ErrOutlookBusy`, no loss); an item whose read made Outlook raise an
  exception is set aside (persisted, disclosed, `Syncer.RetryDetails`)
  so it is not read first again; the breaker's state is kept in the
  store (`source.StateKeeper`); state reads and actions never wait for an
  operation (two mutexes). Every live test starts through `livetest.Open`
  (limits before the first call, unpicked stores not walked, a per-test
  detail budget); `embed-live.sh` reads Outlook inside the crash guard; a
  failed event-log query fails the guard. **Paid decisions:** a decision
  stays valid for its message until the message's own revision changes
  (or the question set or decider, another key); a change of derived data
  (cleaning, evidence, thread views) only makes it stale — disclosed with
  its re-decision cost, decided again on the user's action
  (`Options.Redecide`); data digests are of the files' meaning
  (`digest.Content`); vectors carry the version of their text and are
  rechecked (free) when the cleaning changes. `mail.CleanBody` cuts at an
  attribution only when its quote runs to the end, never on a quoted
  line, and an attribution is never own text. Copies of a message (same
  store, same Message-ID) count once in threads and histories; the
  threading rules version their evidence signals (`threads.Version`).
- 2026-10-15 (layer 1): the local classifier (`internal/layer1`,
  `docs/LAYER1.md`) is the learning stages `layer1` and `questions`.
  Per question a linear head (multinomial, logistic, cumulative logit)
  over the message's embedding and its causal layer-0 cells, fitted by
  one hand-written L-BFGS (`internal/optim`; gonum would be a module to
  justify for ~200 tested lines) on soft teacher targets whose sources,
  precedence and weights are data (`teachers.json`); λ chosen and the
  temperature fitted on the install's evaluation labels (manual and
  judge), grouped by conversation; requirements are data
  (`requirements.json`), the gate never relaxes. Layer-1 answers are
  triage decisions of decider `layer1`, local and free: recomputed
  whenever the active models change (lead decision); SubQ/Gemma states
  stay immutable. The legacy SubQ answers are a teacher under their own
  decider key (`legacy:subq-snap-preview`), never mixed with live ones,
  never a paid call. The in-memory int8 vector set moved from `search`
  to `vector.Set` (one implementation for search and kNN). The gold test
  split is sealed (`internal/goldset`): read once, on the lead's call.
- 2026-10-15 (layer 1 review round, lead decisions): a threshold is
  certified only by labels it was not chosen on and that never trained
  the model that answers (split by conversation, `layer1.certify.fraction`;
  verdicts say "agreement with" the certifying sources); one
  selective/abstain module (`internal/selective`) for layer 1, feedback
  and the triage uncertainty, an abstained answer carrying no usable
  value; promotion and answers in one transaction; the legacy teacher
  lives in the dev harness only; every data file's edit is a change of
  its setting (`config.WatchDataFiles`); the personal dev fixture lives
  in `~/.local/share/mailsense/fixture`, never `out/`.
- 2026-10-25 (route foundation): `selective.Certify` gained an explicit
  coverage denominator (`CertifyEligible`: answered certification
  messages over every eligible one; `Certify` is the same call with
  `len(cert)`), and the answer shape moved to `selective.Given` so layer
  1, feedback and the route share it. `selective.NoThreshold` moved from
  2.0 to 4.0. `internal/route` is the pure certified route.
- 2026-10-25 (route completed): the first implementation cut every layer
  with one global score (`band + confidence`), which starved SubQ: layer 1
  offers on nearly every message, so SubQ's gate had almost no rows and
  the route measured exactly like layer 1 alone (25.54 % vs 25.56 % dev
  coverage at 0.99). Replaced by the lead's escalation CASCADE: each
  layer's gate is chosen on all of that layer's labelled selection
  offers; at answering time the first member at or above its gate
  answers, and a higher member below its gate falls through to the next.
  The cascade's decided answers are certified with
  `selective.CertifyFixed` — no cut is chosen on the certification labels
  at all — so `Certification.Threshold` is 0 for a route and every
  consumer reads `Certified`. On dev at 0.99 the route now covers
  69.82 % (layer 1 25.56 %, SubQ 45.00 %) with SubQ giving 914 of 2,085
  answers; honest certification is still 0/17 at 0.99 because the
  certification part has 117 labels and 381 error-free answers are
  needed. The route is stored in `route_answers` (migration 0012, one
  row per message and route key, abstentions included) in the same
  transaction as layer 1's answers and promotions; the `calibration`
  stage requires a routed pass, and the `questions` stage now certifies
  the route, not layer 1 alone. `triage.UseRoute` makes the read path
  serve the route only (never the paid cache); priors and search filters
  ignore abstentions. The paid decider remains a teacher and a route
  participant, never a source of the displayed answer.
- 2026-10-26 (judge label recovery): a numeric label (noul, score) may be
  a JSON number or a string holding exactly a JSON number literal after
  trimming whitespace (`"3"`, `" 3.0 "`). Gemma quotes some numeric
  answers, and rejecting an unambiguous value lost real labels. Units,
  words, NaN/Inf, other float syntax and out-of-range values stay contract
  errors; a choice option is still a name, never a number. `jsonx.Finite`
  is unchanged, so every other trust boundary still takes JSON numbers
  only.
- 2026-10-27 (one Windows engine): the product catalog has a single
  Windows build, `vulkan` (`auto: ["vulkan"]`); the cpu and cuda builds and
  the CUDA runtime are gone. It is downloaded, verified, installed and
  loaded automatically, with no variant picker and no CPU fallback: a
  broken or crash-looping Vulkan build is marked broken and the next start
  reports the re-probe error. The model pins the canary reference vector
  (`canary_vector` + `canary_sentence_sha256`), so the canary never needs a
  cpu build; Linux dev/tests keep `cpu` for tuning and as the reference
  measurement fallback. `Model()` and auto's empty-list case resolve the
  platform's default build (cpu when present, else the first auto entry,
  else the only variant), and a GPU build without a cpu build passes
  `--threads -1` when `sidecar.threads` is 0.
- 2026-10-28 (Windows Vulkan preflight): a variant may declare `requires`;
  the Windows `vulkan` build declares `"requires": "vulkan"`. Before any
  download, `Manager.require` creates a Vulkan instance and enumerates a
  physical device through `golang.org/x/sys/windows` (System32-only
  loader, no cgo). No driver or no device is `ErrNoVulkan` (422
  `vulkan_unavailable`, permanent for the supervisor); a check that itself
  fails is `ErrVulkanProbe` (503 `vulkan_probe_failed`, retryable and never
  cached). The preflight does not add a CPU fallback, a second build or a
  setting: on Windows the 342,039,075 B install is refused before its first
  byte, `Builds` reports the build as `unavailable`, and the runtime card
  hides the install action. Only a confirmed device is cached; `Reprobe`
  forgets it. A missing device is re-probed on each status read, and the
  card's Refresh invalidates the cached `/api/sidecar` status before
  re-reading; a probe failure is never cached. Unknown `requires` values
  are rejected by catalog validation.
- 2026-10-28 (round 30, Indic substring routing): the ten matra-based Indic
  scripts of the locale data (Devanagari, Bengali, Gurmukhi, Gujarati,
  Oriya, Tamil, Telugu, Kannada, Malayalam, Sinhala) join `substring_extra`
  in `mail/scripts.json`; Hangul stays. They are not spaceless, so word
  counts and triage facts are unchanged. unicode61 treats their dependent
  vowel signs as separators (कि, की, कु, कू word-index to क; कमल and कमला
  share the token कमल), so the word index alone returns 4/4 documents for
  any of the four; the trigram index and the disclosed two-rune scan keep
  the sign and return 1/4. `ftsVersion` is now `4-indic-substring`: an
  existing store rewrites both indexes once at open. Measured on a
  disposable copy of the 33,842-message prepared store (Linux, GOMAXPROCS
  default): cold open **17.97–18.35 s** (audit 17.95 s), warm open
  **5.8–7.7 ms**, DB **384,184,320 → 419,586,048 B** (**+35.4 MB, +9.21%**;
  **96.96%** of the growth is free pages, which the app never VACUUMs), WAL
  **33.6 MB** at completion and absent after close, trigram rows **93 → 94**.
  The synthetic two-rune scan is **4.5 ms** at 1,040 messages and
  **49.1 ms** at 10,040; the prepared mailbox has only 94 trigram rows, so
  its scan is structurally far smaller (not separately measured). Windows and a live mailbox were not measured.
  `mail.DataDigest` also includes `scripts.json`: 37 evidence signal keys
  moved (0 values changed) and a disposable-copy `evidence.Run` recomputed
  **33,842** messages in **4.328 s**, wrote 33,842 rows, kept the DB at
  419,586,048 B and wrote a **4.3 MB** WAL. The semantic text version is
  the same digest prefix; the prepared copy has 0 embeddings (no runtime),
  so no recheck cost was measurable here. In a populated store the cleaned
  text is unchanged (scripts do not enter `Fold`/`CleanBody`), so every
  vector would be confirmed without a provider call; that recheck was not
  timed.
- 2026-10-28 (round 31, split FTS versions): the single `ftsVersion` is
  split into `fts_text_version` (the folded form both indexes hold) and
  `fts_coverage_version` (which messages have a trigram row), with
  `fts_version` kept as a downgrade mirror. A missing split key falls back
  through the legacy value: `4-indic-substring` adopts text 3 / coverage 4
  with no index work, `3-nfkc-casefold` selects the trigram-only pass, and
  a missing, older or unknown value rebuilds both. A folded-text (or fold)
  change still rewrites both indexes; a coverage-only change scans every
  message for the same coverage predicate and inserts only the missing
  trigram rows, deletes stale ones and leaves the word index untouched
  (no word statement is even prepared). On the 33,842-message prepared
  store the legacy-3 tri-only open measured **0.753–1.536 s** cold
  (**0.686–0.728 s** exact scan+insert) against **17.880–18.043 s** for the
  full rewrite; the word
  index stayed byte-identical (`47,386,624 B`, same rowid checksum), the
  trigram index went **93 → 94** rows (**+8,192 B**), the DB grew **2 pages
  (+8,192 B)** after checkpoint and WAL was **53,592 B**. The full control reproduced
  the Round 30 shape: **17.880–18.043 s**, DB **+35,401,728 B**, WAL
  **33,648,072 B**, freelist **8,380 pages**. App ready was **3.439 /
  3.639 / 3.946 s** on fresh legacy-3 copies (full control **20.324 s**) and
  **2.832–3.239 s** warm. A Round 30 store (legacy 4) adopted the split keys in
  **255.9–397.5 ms** with no row or byte change; a store with no missing
  coverage scanned 33,842 messages in **0.693–0.707 s** with 0 inserts, and the
  10,040-message Hindi store in **0.160–0.168 s**. No migration, setting, table,
  index or column was added; `writeFTS` remains the incremental write
  path and the coverage pass shares its fold/insert helper.
- 2026-10-28 (round 32, startup warm order): the first learning status is
  no longer built inside `wireLearning` before the derived warm. A stage
  that cannot report is a startup-warm warning surfaced by `/api/learning`,
  never a fatal startup error. `warmDerived` starts one early `learning`
  goroutine beside the priority `Rank`, joins it after priority, then runs
  the seven priority-dependent views and the Brief. Priority deliberately
  stays out of the concurrent group: it is the base of every dependent view
  and has no cold single-flight, so moving it would duplicate the ranking
  build. The learning machine's `computeMu` serializes first builds, so a
  priority gate that needs the status waits on the same build. Every view
  is still warm before the API port; the warm log carries `offset_ms`. On
  the prepared store learning ran at offset **0 ms** for
  **352–372 ms** and ready improved **2.935 → 2.734 s** in the fully warm
  series (matched first series **3.027 → 2.827 s**); priority paid
  **+99 ms** of contention, so the net gain was **0.200 s**, below the
  0.25 s win threshold. Peak RSS **539 MB**, CPU **4.56 s**, FDs **16**,
  goroutines **69**, one priority build per run. Windows, WebView2 and the
  live 35 s start were not measured.
- 2026-10-28 (round 33, one learning build reads each stage once): the
  status fan-out asked the embeddings stage three times per build (the
  index directly, as layer 1's "after" stage, and through the route's
  layer-1 stage), so one build planned the 33,842-row coverage three times
  when evidence was complete. `learning.StatusOf` now reads every stage
  through one per-build context memo (`withStatusMemo`), keyed by stage
  type and id; `computeStatus` installs it once, and the layer-1, route and
  labels stages read their "after" stages through it. A failed stage
  returns the same status and error within the build. The memo is dropped
  when the build returns — no cross-build cache, no invalidation surface,
  and a plain context still calls the stage directly. Measured on a
  disposable copy with evidence complete: plans inside the learning build
  **3 → 1**, learning **666 → 313 ms** (median of 3+3); on the Round 32
  resource store (evidence pending, so the 3-plan path was not active)
  learning **361 → 308 ms**. Ready **2935 → 2833 ms**, priority
  **1630 → 1653 ms** (noise), CPU **4.60 → 4.62 s**, peak RSS
  **564 → 550 MB**, FDs 16, goroutines 69, threads 8; idle 60 s used
  **0.00 CPU-s**, and the 600-read p50s and DB/WAL bytes were unchanged.
  Learning ≤200 ms was missed (**313 ms**: sync 56 ms + evidence status
  119 ms + the one 146 ms plan); a coverage-only plan is not small, so it
  stays a residual. Windows, Defender, WebView2, a live mailbox, a
  populated vector store, pprof heap and a cold page cache were not
  measured.

- 2026-10-28 (round 34, listener and window before the warm): the desktop
  surface no longer waits for the derived views. `main` builds the API,
  `srv.Start` binds loopback and writes `runtime/api.json`, then the warm
  runs as one cancellable background task; the window opens immediately and
  first-open discovery plus the semantic maintenance worker wait for the
  warm's done channel. `mailsense ready` moved to the end of that task.
  One `bootState` owns the twelve ordered stages (`mail`, `learning`,
  `priority`, `agenda`, `triage_summary`, `triage_page`, `home`,
  `conversations`, `commitments`, `companies`, `brief`, `semantic_search`);
  the Mail first page is warmed beside learning/priority (Round 35 added the
  view; Round 37 moved it from the serial prelude into the early group), and
  `GET /api/boot`
  reports pending/running/done/failed and is never gated. The priority
  service gained one cold single-flight beside its stale-while-refresh path:
  concurrent first readers share one build detached from their request
  contexts, a failure is shared but not cached, and `Close` cancels and
  waits up to 10 s for both cold and refresh builds before the store closes.
  Production store reads honor the context; a non-cooperative build is
  logged and remains the one bounded residual, not an absolute join. The
  shell mounts the route skeleton and opens the event stream before its
  first awaits, polls the boot contract, keeps a warning strip at `ready`
  when `failed > 0` and starts the core prefetch once ready (or on a 404
  from an older server). The launch token is still only in the bootstrap
  fragment and session storage; no window or API descriptor wait applies to
  it. Shutdown cancels the warm and joins it for up to 15 s before the API and
  store stop: a cooperative view honors the context, while a non-cooperative
  one is logged after that bound. It also cancels and waits for first-open
  discovery and joins the semantic worker. The first-open wait has no timeout;
  production connectors honor the context, and a connector that ignores
  cancellation can delay shutdown as a residual, not as a reason to close the
  store underneath the pass. The API refuses new handlers once shutdown starts
  (`503 shutting_down`) and joins every admitted handler; a handler that
  outlives the deadline is `api.ErrHandlersUnjoined`. Jobs are cancelled
  immediately after the signal so a handler waiting on one can return before
  the API join expires; `jobs.Shutdown` reports `jobs.ErrUnjoined` when a job
  outlives its bound. `priority.Close` reports whether its builds joined. The
  one shutdown decision lives in `main`: when any bounded join reports
  unjoined, the store and the instance lock stay open and the process exits 1
  (SQLite replays the WAL on the next start); otherwise the store closes
  normally. The remaining unbounded joins (first-open discovery, sidecar,
  connector close) can delay shutdown, but they never cause a store close
  underneath a live goroutine.

- 2026-10-28 (round 35, first-click Mail): `renderMail` no longer awaits
  `/api/learning` before content. It starts that read for the chrome, asks
  the new authenticated `/api/gate` (one `Learning.Allow`: the open/latched
  path reads the learned-scope latch and short-circuits, while a locked path
  builds and returns the full status) and paints the all-mail list;
  a direct `#/mail` mount also skips the learning await in the shell's
  first-load sequence, while every other route keeps the old order. The
  new `internal/messages` service owns one immutable unfiltered first page
  (`limit=100`, `counts=1`, no role/store/cursor) keyed by
  `store.MailGeneration`, with at most one in-flight build per generation,
  shared-but-uncached failures and independent copies; role, store, cursor, other-limit and
  no-counts pages bypass it. `warmDerivedTracked` starts `mail` and
  `learning` beside priority; `warmViewOrder` is the one twelve-view
  list the boot snapshot and the UI catalog derive from. The semantic
  prepare stays after the derived warm because overlapping it measured no
  ready win and added contention. Measured on the complete Round 33 store (one-minute load samples; the click and startup
  series were 40–44): click-to-list **1,346 → 213 ms**
  median (5 interleaved pairs, −84.2%; absolute ≤150 ms still open),
  messages **159 → 12 ms**, first-page API warm p50 **9 ms**, **open-gate**
  warm p50 **5 ms** (the locked gate builds the full status and was not
  measured), ready **9,735 → 9,531 ms** (paired, no ≤+0.5 s regression),
  boot **ready 10/12 failed 2**. Startup CPU **+0.39 s** and ready RSS
  **+56 MiB** missed their bounds; warm HWM was **−9.1 MiB**. The 600-read
  pass was **44.612 → 69.720 ms (+56.3%), a regression, not a win**:
  `after-1` timed out one request after 318/600 and is excluded; the after
  median is inside the 71.06 ms guard but the broad endpoints also rose
  (hub, priority, search, companies), so no resource win is claimed.
  Windows/WebView2/COM, a cold page cache and live mail remain unmeasured.

- 2026-10-28 (round 37, Mail beside priority; prepare-overlap rejected):
  `warmDerivedTracked` starts the Mail first page and the first learning
  status in the early group beside the priority ranking (`early.Add(2)`),
  then joins both before the seven dependent views and the Brief. The first
  Mail click still shares `wire.messagesOnce`/`messages.Service`, so it joins
  the warmed build instead of starting a second one; a pathologically slow
  Mail build can hold the dependent group at `early.Wait` but cannot delay
  priority, which runs on the warm's own goroutine. The 12-view order is
  unchanged (`mail` remains first in the snapshot). The semantic prepare
  stays serial after the derived warm: the round implemented the overlapping
  `runWarmAndPrepare` variant, but its five paired runs measured ready
  **3,782 → 4,063 ms** (+281), priority/companies at the 5% edge, semantic
  prepare 47 → 387 ms and HWM +77.7 MiB, so the overlap was removed. The final
  tree keeps only the Mail overlap: five paired runs measured ready
  **3,222 → 3,260 ms** (+38, no win), priority **+0.9%**, companies
  **+1.1%**, click-to-list **90 → 87 ms**, boot `ready 10/12 failed 2`,
  `semantic_search` done. The absolute ready ≤2.75 s bound is not
  assessable at load 6–7 (the Round 33 series was 1.05) and remains open.

- 2026-11-03 (attachment completeness): a detail read returns a message's
  complete attachment list or fails. Outlook's `attachments()` no longer
  breaks on a failed or null `Attachments.Item` and returns the items read
  before it; it returns the error with the message id and the attachment
  index, so a transient COM failure leaves the message `partial` and the
  next run retries. The store's authoritative detail write (`ApplyDetails`
  → `replaceAttachments`) is unchanged and now states its contract: it is
  handed a list the reader asserts is complete, so the rows and
  content-addressed inline bytes of a part that could not be read are
  neither deleted nor merged with the stored ones. Only an item class
  without an attachment collection is an empty list. Outlook is the only
  `source.DetailReader`; the RFC 5322 reader parses one in-memory MIME tree
  (a malformed tail is a recorded alteration, not a transient read) and the
  legacy archive carries attachment metadata only.

- 2026-11-03 (layer-1 commit leaves the write lock during answers): a
  layer-1 run computes one decision, route answer and preview per message,
  ~15 s on a large mailbox, and `CommitLayer1` used to stream that phase
  inside its one `BEGIN IMMEDIATE`: every other writer (sync, triage, Ask
  history, labels, usage) waited out `store.busy_timeout` and aborted. The
  production one-pass `Layer1Answers` form is now staged on one dedicated
  connection (`store.commitLayer1`): a TEMP table shaped exactly like
  `Layer1Answer` (message_id, revision, run_id, state_hash, decision, route,
  preview) is cleared and filled from `Answers.Each` in bounded batches
  outside any main-database transaction, so the inference phase and the TEMP
  writes take no main lock. The same connection then runs the one short
  `BEGIN IMMEDIATE` (`inTxOn`, the store's transaction policy reused, not
  forked): promote/retire/meta, replay the staged rows in `message_id` order
  through the existing `streamLayer1Answers`/`writeAnswerRow` seen, prune and
  upsert logic, write the report and mark the run succeeded. A staging
  failure returns before the transaction, leaving decisions, routes,
  previews, promotions and the run row untouched; a failure inside the short
  transaction rolls them all back together. The TEMP table is dropped and
  the connection returned to the pool; the post-commit route generation bump
  and checkpoint are unchanged. The legacy separate `Decisions`/`Routes`/
  `Preview` streams have no inference phase to stage and keep their single
  transaction. Measured with 200 answers and 3 ms of simulated inference
  each, the transaction spanned 659 ms (the whole answer phase) before and
  5–9 ms after; on a quiet 35k-answer fixture the pure-write transaction was
  about a second, since the replay costs one TEMP read per answer while the
  inference itself no longer holds the lock. Concurrent
  `UpsertMessages` and `PutLabel` now complete while the answer stream is
  blocked (they returned SQLITE_BUSY before). Residual: a very large commit
  still holds the write lock for the staged replay of every answer through
  `writeAnswerRow`, bounded by the write cost rather than inference.

- 2026-11-03 (one busy policy for every bounded write): three SQLITE_BUSY
  policies existed — the indexer retried, the syncer aborted its run,
  triage aborted and dropped the paid answer, labels/scope/usage failed at
  once. `internal/store/busy.go` now owns `BusyPolicy.WithBusyRetry`, built
  once at open from the existing `index.store_busy_attempts` /
  `index.store_busy_delay` pair (historical keys, now group **Storage**). It
  runs one idempotent write, returns other errors immediately, retries only
  `IsBusy` while attempts remain and the context is alive, waits through the
  store clock, and finally returns `ErrStoreBusy` naming the write and
  wrapping the cause without SQL text or values. Message upserts, embedding
  writes, triage decisions/failures, label writes (the layer-1 judge included),
  scope verdicts and usage records use it; reads and `CommitLayer1` do not.
  The indexer's own loop is gone: `index.ErrStoreBusy` is the store sentinel
  and `stopped_by=store_busy` keeps its no-count/no-quarantine meaning. A
  triage decision whose write exhausts the budget is recorded through
  `SaveFailure` and stays pending, never decided; the provider is called
  once. Usage stays best-effort and logs one warning (provider and model)
  when its record is lost. Residual: a lock held past the whole attempt
  budget still fails, and usage has no durable outbox.
