# Engineering log

This is the durable record of improvement cycles: what was found, what was
changed, what was tried, what failed, and what comes next. It is not a changelog
(the user-facing history lives in `CHANGELOG.md`) and not a plan (`docs/PLAN.md`
owns scope). Keep it current as work proceeds.

## Cycle: adversarial fix wave and resource budgets (2026-11-05)

### Adversarial findings on the merged round (validated, being fixed)

An adversarial review of PR #24 (`c781d3a`) found two defects and two risks.
Each was independently checked against source by the coordinator:

- The Brief relationship row (`internal/ui/js/21-brief.js:221-229`) still
  rendered the conversations rollup fields the new server producer stopped
  sending, so every row showed "0 conversations, 0 messages" and the new
  open-loop counts were invisible. The UI test fixture still pinned the stale
  wire shape, which is why the suite stayed green.
- The plain-count scope gate was skipped whenever a generator was configured
  (`internal/qa/qa.go:519`) and on the Local structured default branch
  (`internal/qa/route.go:173-175`): "Number of urgent messages from Bob?"
  answered a mailbox-wide total while `docs/API.md:852-857` promised the gate
  for every count cue. Reproduced with fake priority totals.
- A preview flip dropped `/api/priority`, `/api/triage` and
  `/api/conversations` but not `/api/brief`, whose snapshot is preview-keyed;
  a warm Brief repainted pre-flip data for up to the client TTL.
- The topic coverage line was suppressed when `scanned === 0`
  (`internal/ui/js/22-ask.js:848`): the exact silently-uncertified zero the
  feature exists to disclose.

### Shipped in this wave

- `fix-brief-row` (`b6dfe34`): the Brief relationship row renders the open-loop
  count, overdue and stored follow-up partitions the producer sends; the dead
  conversation-digest fields were deleted from `brief.Item`, the stale fixture
  was replaced with the real wire shape (the updated test fails on the old
  renderer), `/api/brief` joined the preview reads, and the topic coverage line
  renders at 0/0 with a distinct uncertified sentence.
- `fix-count-scope` (`c78821d`): the count decision is one scope check on
  wording, on every route and with or without a generator; an accepted count
  answers deterministically, a rejected one follows its route's freeform path.
  Closes the reproduced "urgent messages from Bob" mailbox-wide leak.
- `perf-viewcache-budget` (`1c1eb9d`): `Options.MaxBytes`/`Weigh` bound the
  shared read caches by bytes at their one owner, wired into every construction
  site except `internal/brief`.
- `chore-ci-hardening` (`4ebcf39`): the vulnerability job reads the Go version
  from `go.mod` (the action's `stable` default is suppressed), failure,
  Playwright and release artifacts are retained 7 days, and `.github/actions/**`
  edits re-run actionlint and the area label.
- `docs-ask-intelligence` (`1856154`): Ask's local topic counts, coverage
  honesty and scope rule documented EN/FR.
- `chore-node-version` (`490791b`, `6869e9d`): `.nvmrc` owns the Node version;
  the three workflows and the generated site/README text read it.
- `test-e2e-coverage` (`9fe8b04`): request-count pin for one changed-mail sync
  frame and a topic-answer rendering test.
- Local gate: `make fast` green on the integrated tree; the affected Playwright
  specs passed 54/54; the PII gate caught a literal internal host name in this
  log, which was neutralised.

### Finish round (verified and executed)

- `feat-site-dark-theme` (`b1d8ef8`): the public site has both palettes, a
  system default and a persisted three-state toggle; the generated head script
  carries the theme key, the header emits the localized buttons, and a
  contrast test pins WCAG AA for both palettes.
- `test-e2e-brief-topic` (`3358b79`): the Brief relationship row and the
  uncertified-zero coverage sentence are pinned end to end.
- `perf-derived-caches` (`dfae776`): the search coverage count is computed
  once per vector copy sequence and reused; the conversations facts memo
  remains queued.
- `perf-triage-core` (`0a66fcf`): the cache and the model share one
  mail-derived core, with a build counter proving one build per stamp.
- `intel-world-trend` (`015aef8`): a neutral cadence trend over two equal
  complete-month windows, carried on the company profile and its wire.
- `chore-ci-followup` (`075d8cd`): one log directory for CI failure logs, the
  stale sidecar artifact removed, scorecard/CodeQL/release-asset follow-ups,
  and `stop-windows.sh` now waits on the descriptor and treats refused/401 as
  not running (six exit paths exercised against a fake API).
- `feat-update-detect` (`249f169`): slice 1 of the update feature (semver
  comparison, GitHub release checker with ETag and rate-limit handling, atomic
  state, API, startup check, settings) and the privacy pages now list
  the update check as the fifth request kind.
- `fix(update-gate)`: the live Windows verification showed `GET /api/update`
  answering 423 on a fresh data dir because the four update routes were
  wrapped in the learning gate. The routes are now bearer-only and reachable
  before learning; the test asserts 200 with a closed gate, that the gate is
  never consulted, and that a missing bearer still answers 401.
- `test(e2e)`: the Companies cached-Back probe now waits for the fetched
  screen instead of the route skeleton that shares its class, removing a real
  race that failed once in CI; the probe passed 12 repeats under four workers
  and the full suite 193/193 locally.
- Promotion: `testing` merges to `main` through PR #25 with every required
  check green. The owner's explicit instruction authorizes the admin merge
  because the ruleset demands an extra approval for unattributed commits.

### Post-promotion fix (2026-11-05)

- The job strip's per-job cancel button showed a bare "Cancel". The action
  already built `Cancel <task>` for the accessible name and title but forced
  the visible span to `common.cancel`; the visible label now uses the same
  string at its one owner (`13-jobs.js:311`). The live Playwright case asserts
  the visible text and fails on the old bundle; no new catalog key or budget
  change.
- The dispatched exhaustive CI exposed a fixture race in
  `TestLauncherEnvironmentStripsMailsense`: the fake launcher's `env.txt` was
  polled while `env` was still writing it, so an empty read failed the
  GUI-variable check. The fake now writes a temporary file and renames it,
  publishing the dump atomically; 200 repeats pass.

### Update surface (2026-11-05)

- `42-updates.js` is the one update UI owner. `#/settings?tab=updates` shows
  the installed and latest versions, the last check, the release notes and
  the three schema settings the tab now owns; **Check now** posts
  `/api/update/check` and **Skip this version** posts `/api/update/skip`.
  A failed upstream answer stays the quiet status line the API contract
  promises, never an error toast.
- The first open that detects a release with unseen notes shows one What's
  New dialog. The release body renders as headings, paragraphs and bullets
  from text nodes only; closing by any path marks the notes seen. The tab's
  skip suppresses that version until `update.remind.after`. Setup and another
  dialog only defer the overlay: setup close and any dialog close retry it,
  and one version is offered at most once per session.
- A small Settings nav dot survives the overlay, so an available release is
  still discoverable; the panel's skipped and failed-check headlines are
  mutually exclusive with the available one, and Check now returns keyboard
  focus to the rebuilt control; the marker event it causes cannot start a
  second refresh over that control.
- `update.state` is subscribed in the shell; a mounted Updates tab re-reads,
  and a dropped stream re-bases the status with the other live reads.
- The first implementation used `state.setup` as the setup guard, but that
  field holds the setup view even when setup is not needed; the guard is the
  `#setup-root` node. Five Playwright cases cover the tab, both actions, the
  overlay and the three suppression states; the catalog budget moved by the
  exact 24 added lines in each language.
- Verification on the integrated tree: `make fast` green, the affected
  Playwright flows 56/56, the named regression suites for each feature green,
  the generated site current, and the PII gate clean.

### Background-action refresh scope (2026-11-05)

- PR #26's Playwright run failed with Logs routed but zero `.logs-screen`
  nodes. The discovery action settles its shell reads after its toast; the
  test navigated back to Logs in that window, and `afterBackgroundAction`
  unconditionally refreshed whichever screen was then active, replacing the
  cached Logs DOM. The owner now asks the invalidation graph which routes the
  write can change and rebuilds only a matching active screen; an unrelated
  cached route keeps its DOM, scroll and focus. The Sources case holds one
  shell read across the navigation and fails on the old unconditional
  refresh (reproduced: `Expected: 1, Received: 0`).

### Security (guidance mode) — results so far

- **Loopback API hunt:** four low candidates, each source-verified with a
  concrete fix: the auth gate reads the decoded path while the multiplexer
  matches the escaped path (`GET /api/%2e%2e/status` answers 404 without a
  token instead of 401); the sidecar sends its per-launch bearer on `/health`
  before the port-ownership check; the Windows browser fallback passes the
  token URL to `ShellExecuteW` as argv while the POSIX path deliberately uses
  a 0600 redirect page; the bench scripts put a live token in argv. Clean on
  CORS/CSRF, token travel, static/attachment traversal, CSP and body caps.
  All four were fixed on `security-hardening` (`afbb017`) with captured
  baselines: the gate now decides on the decoded and escaped path forms
  (`GET /api/%2e%2e/status` answers 401), the sidecar probes ownership before
  sending the per-launch key (a foreign listener no longer receives it; the
  unverifiable-platform stub is documented rather than claimed as mitigated),
  the Windows fallback opens a 0600 redirect page instead of passing the token
  URL to `ShellExecuteW`, and the bench scripts pass the bearer through a 0600
  curl config.
- **Secrets/data-at-rest hunt:** one medium — the only secret-classified
  setting (`http.proxy_url`) is persisted plaintext in `settings.json`; the
  owner decides between routing secret settings through the secret store or
  refusing credential-bearing values from the file layer. Two low: the POSIX
  browser launcher inherits `MAILSENSE_*` env into the browser process tree
  (fixed in `afbb017`: the launcher gets an allow-listed environment with every
  `MAILSENSE_*` stripped, reproduced as a leak before the fix);
  `http.log.body_max_bytes` writes mail text to logs by explicit opt-in
  (documented trade-off). Clean on DPAPI scope, redaction, sidecar artifact
  pinning, installer, env handling and tracked secrets.
- **Egress/render hunt:** one medium residual — indirect prompt injection can
  steer a model-written answer, and `groundAnswer` validates citation indices
  rather than entailment (`internal/qa/ask.go:1623-1678`); no state change,
  extra retrieval, model-directed fetch or exfiltration is reachable. Honest
  mitigations: the egress matrix, the embeddings default and the grounding
  residual are on the EN/FR security and privacy pages
  (`docs-security-egress`, `c00c358`; the writer corrected the old
  "computes embeddings on your machine" wording, which ignored the first-start
  local-embedder default and the remote case). A provenance note in the answer
  card and a test pinning the script-free mail-frame sandbox invariant remain
  pending. Clean on implicit egress, provider URL policy and SSRF, HTML
  sanitization, links, attachments and the sidecar trust chain.
- **Desktop/IPC hunt:** confirmed the same two defects (sidecar key before
  ownership, Windows token in browser argv) at exact fix sites, both now fixed,
  and reported informational residuals: the shell installs no navigation,
  new-window or permission policy with the pinned WebView2 wrapper (no trigger
  found today: every app link is a hash route and mail links are filtered),
  the WebView2 profile lives outside the data directory, the runtime child
  shares the data tree that holds the descriptor, `stop-windows.sh` trusts a
  stale descriptor PID, and `StartOutlook` launched a bare `OUTLOOK.EXE`. The
  Outlook launch was fixed in `security-outlook-path` (`8881994`: App Paths
  resolution under HKLM then HKCU, a typed error when unresolved, never a
  bare-name fallback); its fake user-profile path tripped the PII gate on CI
  and was neutralised in `9d0263a`.
- Promoting any of this to the full six-phase audit with artifacts still needs
  explicit authorization.

### Deployment verification (Windows test host)

- Built `0.3.0-dev+20261105040320` from `395083b`, uploaded it to the host's
  deploy directory and started it as the signed-in user with a fresh isolated
  `MAILSENSE_DATA_DIR`.
- Verified: the runtime descriptor reports that build; `GET /` answered 200 and
  the served bundle carries the new markers (`ask-topic-coverage`,
  `briefRelationshipRow`, `brief.relationships.open_loops`);
  `POST /api/ask "How many invoices?"` returned `423 learning_locked` with the
  expected mailbox and provider problem codes, which is the gate refusing Ask
  on an unmailed fresh directory rather than a fault; `POST /api/shutdown`
  answered `{"ok":true}`, the descriptor was removed and no process remained.
- The deployed binary predates the Outlook App Paths fix; the next build
  includes it.
- Process gap found: a merge made after the last `make fast` skipped the PII
  stage, and the new Outlook fixture path matched the deny list. Re-run the
  PII check (or the gate) after every merge, not only on the per-writer tree.

### Next queue

The owner asked for the interrupted wave to be verified and finished, then the
work to stop. Slice 1 of the update feature and the docs/site theme are now
shipped; remaining work, in priority order:

1. Evidence-date deadlines: the pure detector (`internal/datecue`) first, then
   the evidence producer, the revision/rules-scoped `message_due_cues`
   projection (migration 0040 after the priority-coverage migration), one
   extended `DueDeadlines` read, and the priority/commitments/agenda/Ask/UI
   consumers.
2. People profiles: `internal/people` merge, the store person context and
   exact-address lookup, `world.Person`, the API route, and the Companies/Ask
   surfaces.
3. Priority-count coverage: one `priority.Coverage` from the ranking scan,
   `RankPage`, persisted `qa_history.coverage`, and the shared Ask coverage
   line.
4. Update feature: slice 1 shipped (semver detection, GitHub checker, atomic
   state, API available before learning completes, startup check, settings).
   Remaining: the Settings tab and
   the first-open What's New overlay, then slice 2 (download and install: the
   `httpx` asset-host allowlist, a shared fetch package, the Windows
   relauncher helper). The privacy pages now list the update check as the
   fifth request kind.
5. Theme: the docs/site dark theme shipped. Remaining: the app theme
   (`ui.theme`, both palettes, mail-frame propagation, WebView2 background as
   an optional tail).
6. Resource work: one owner for the vector set, the priority second decision
   map, a byte budget for `qa.answers`, usage-write coalescing, and the
   conversations facts memo (the search coverage count shipped).
7. Security follow-ups: the `http.proxy_url` secret-at-rest decision (owner),
   the answer provenance note and the script-free mail-frame invariant test,
   WebView2 data path/background, and the remaining informational residuals.

## Cycle: three-writer intelligence round (2026-11-04/05)

### Shipped

- PR #24 merges three isolated writer branches into `testing`: sync
  invalidation coalescing (one pass per `sync.finished`, plus a preview flip
  dropping the ranked, triage and conversation reads), topic-scoped local Ask
  counts with classification coverage, and per-organization open loops from
  the one deduped commitments build. The superseded conversations
  organization rollup is deleted, so one obligation owner remains.
- The generated UI bundle was regenerated from the merged sources; two
  writers had touched `app.js` independently.

### Verification

- Each branch passed `make fast` in its own worktree; the integrated tree
  passed `make fast` again (gofmt, scripts, generated bundles, PII, vet, 91
  packages, sidecar, race).
- Focused suites on the merged tree: `internal/subject`, `internal/priority`,
  `internal/qa`, `internal/store`, `internal/ui`, `internal/conversations`,
  `internal/brief`, `internal/commitments`, `internal/world`, `internal/api`,
  `cmd/mailsense`.
- Playwright on the affected flows: 51 tests across `ask`, `mail`, `hub` and
  `completion-events` passed.
- Baseline reproduction: reverting the shell sources to the merge base made
  the new coalescing sandbox test fail; restoring the commit made it pass.

### Deployment

- Windows build `0.3.0-dev+20261105` on the isolated test host: PID matched
  the runtime descriptor, `/api/status` reported the same version, `/` and
  `/app.js` returned 200, and the bundle carried `syncFinishedTopics`, the
  topic sentence, the coverage line and the topic intent branch.
- The deployed binary answered `POST /api/ask "How many invoices?"` with
  `mode=structured`, `intent=topic`, `topic=billing_account`,
  `subtopic=invoice`, `count=0`, `classified=0`, `scanned=0`, no notice and no
  citations: the new local path ran end to end on Windows with no generator.
  The app then stopped through `POST /api/shutdown` (202) and its descriptor
  was removed.

### Next queue

1. Uncertified-dimension notices for the remaining structured counts (a
   priority category zero should disclose coverage like the topic count).
2. Evidence-date deadlines: date pressure found in cited text must flow into
   priority/commitments with its evidence.
3. People profiles: one person view assembled from messages, commitments and
   companies.
4. Rendering the open-loops rollup in the Companies screen and Brief rows
   (the server contract is in place; the UI still uses its local precedence).
5. A memory byte budget for the shared view caches and the priority/semantic
   path.

## Cycle: topic-scoped local Ask counts (2026-11-04)

### Shipped

- Ask answers a plain count of one catalogue topic locally, with no model and
  no second mailbox scan: "How many invoices?", "Combien de factures ?" and
  "How many support requests?" read the topic census the priority ranking
  already built. `internal/subject` owns the recognition table (whole topics
  plus their main subtopics, EN and FR, longest alias wins, validated against
  the catalogue) and `priority.TopicCensus` owns the counts.
- The provider-less Auto gate and the Local route share one decision owner
  (`providerlessCountIntent`), so the two cannot disagree about what a count
  answers. Priority category counts keep their precedence: "How many
  decisions?" and "How many urgent messages?" never become topic counts.
- A zero topic count discloses its classification coverage (classified of
  scanned): an empty topic is visibly different from a mailbox whose mail was
  never classified, and a failed census read is an error, never a zero.
- The answer is persisted with its path and coverage (migration 0038,
  `qa_history.topic`), so a reopened or history-loaded answer states exactly
  what it counted.
- Two relation prepositions ("about", "sur") joined the plain-count filler
  allowlist so "How many messages about invoices?" answers the topic count;
  every scope noun after them still fails the allowlist.

### Verification

- `go test ./internal/subject/... ./internal/priority/... ./internal/qa/... ./internal/store/... -count=1`
  and `go test ./internal/ui/... -count=1`.
- `make fast`: gofmt, script syntax, generated UI bundles, PII scan, vet,
  shards A/B/C, isolated sidecar suite, race shard.
- New tests: EN/FR accept (invoices, factures, billing, projet, support
  requests, about/sur), zero coverage with no citations, fail-closed controls
  (this week, today, person frame, organization, reported action, repeated
  alias, two categories, priority compound, unlisted scope, no count cue),
  priority precedence, resolver-error deferral, census failure with no history
  row, the alias boundary against priority and pinned scope words, the Local
  route, and the persisted topic round trip (including strict decode).

### Next queue

1. Uncertified-dimension notices for the remaining structured counts: a
   priority category zero should disclose its coverage like the topic count
   now does.
2. Evidence-date deadlines: date pressure found in cited text must flow into
   priority/commitments with its evidence.
3. Company open loops: unanswered owner obligations grouped by counterparty.
4. People profiles: one person view assembled from messages, commitments and
   companies.
5. Performance: one invalidation fan-out owner and a memory budget for the
   priority/semantic path.

## Cycle: public surface rebuild and intelligence hardening (2026-11-04/05)

### Shipped

- Public site rebuilt as a full-bleed ledger, then replaced by the petrol,
  bone, mint and copper product stage (PR #22, `3202d46`). Landing and
  documentation share one masthead, index, reading measure and rail. Every
  raster is regenerated from the one brand owner.
- Priority now reads open stored follow-up dates (`task_due_date` with a
  completed flag) and promotes an otherwise informational message to
  `follow_up` with `due_source=stored`, so the ranked queue, commitments,
  agenda and brief agree on the same fact.
- Cross-screen Ask handoff is a one-shot mount intent. The router consumes it
  on the cached Ask path; the fresh render consumes it with its own composer
  and leaves focus pending until that composer is mounted. "Ask about this
  message" and the company action therefore survive both a cold and a warm
  route. The cold-path test caught the first version focusing a detached
  field; the guard is now in the shared focus helper.
- Home's Important sections link to `#/mail?mode=important`, which makes the
  destination independent of the last Mail tab.
- Sync invalidation no longer kills paging on the mounted screen. Only cached,
  detached nodes lose their paging token; the active list keeps its Load more
  and compares `generated_at` before splicing a new snapshot.
- Release dispatches are tag-only: a branch ref fails before any build or
  publish. CI shard log artifacts carry the shard in the name, and the RACE
  shard is strict (`RACE=on`) instead of silently skipping when cgo is absent.

### Verification

- `make fast`: gofmt, script syntax, generated UI bundles, PII scan, vet, 91
  test packages, isolated sidecar suite, race shard.
- `go run ./cmd/gensite -check`: generated public surface current.
- Jekyll build of `docs/`: succeeds.
- Playwright: `mail.spec.ts` and `hub.spec.ts` pass, including the cached-Ask
  handoff and Important paging; browser audits at 320–3440 px in EN and FR with
  no overflow, console errors, broken images or external requests.
- Baseline reproduction: `Important pages the ranked snapshot` failed at
  `3202d46` with 50 rows after Load more. The cause was `forgetPagingTokens`
  clearing the live screen's token on the sync invalidation. The fix skips
  connected nodes; the same test now passes.
- Windows deployment (isolated data directory, no owner mailbox touched):
  build `0.2.0-dev+20261105b`, PID matched the runtime descriptor,
  `/api/status` reported the same version, `/` and `/app.js` returned 200,
  and the served bundle contained the prefill consumer, the cold-mount focus
  guard, the Important mode and the connected-node paging guard. The app
  stopped through `POST /api/shutdown` (202), the process exited, and the
  descriptor was removed. A fresh data directory reports `gate.allowed=false`
  until learning is opened, which is the expected first-run state.

### Merged and community state

- PR #23 merged to `main` as `d90a35c`. Main `ci`, `actionlint`, `codeql` and
  `scorecard` completed successfully; `pages` was not triggered because the
  change touched no docs or site sources. `testing` is synced to `main`.
- Three writer agents are working in isolated worktrees on non-overlapping
  slices: sync invalidation coalescing (UI shell), topic-scoped local Ask
  counts (subject/qa/store/Ask UI), and company open loops (commitments, brief,
  world, API wire). Each must pass `make fast` in its worktree and is reviewed
  and gated again during integration.

### Audits and their top findings

1. **Intelligence/correlation** — topic-scoped local counts, silent
   uncertified zero counts, evidence-date deadlines, a people layer, company
   open loops, meeting prep and evidence-based sender risk.
2. **Performance/resources** — post-sync invalidation fan-out, semantic
   refresh and coverage work per query, per-call usage/log writes, full-mailbox
   label and install scans, and missing memory budgets.
3. **Delivery** — release dispatch could publish a branch; matrix shard
   artifacts shared one name; RACE could disappear; stale action majors.
4. **SSOT/duplication** — provider field schema duplicated in the editor,
   parallel label lists, and several hard-coded limits.
5. **UX/entrypoints** — mobile Mail detail off-screen, cached Ask prefill,
   Home Important mode, mobile nav focus/scroll leak, Answers detail.

### Next queue

1. Topic-scoped local Ask counts: a count must name the topic it searched.
2. Uncertified-dimension notices: a zero count must say when the classifier
   never certified that dimension.
3. Evidence-date deadlines: date pressure found in cited text must flow into
   priority/commitments with its evidence.
4. Company open loops: unanswered owner obligations grouped by counterparty.
5. People profiles: one person view assembled from messages, commitments and
   companies.
6. Performance: one invalidation fan-out owner and a memory budget for the
   priority/semantic path.

### How to use this log

- One entry per cycle, newest first; keep shipped items tied to a commit or PR.
- Record failed experiments and the evidence that rejected them, not only the
  successes.
- Record the exact verification command and its result; a claim without an
  observable check is not done.
- Do not include mailbox content, credentials, tokens or personal data.
