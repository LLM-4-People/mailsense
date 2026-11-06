# Engineering log

This is the durable record of improvement cycles: what was found, what was
changed, what was tried, what failed, and what comes next. It is not a changelog
(the user-facing history lives in `CHANGELOG.md`) and not a plan (`docs/PLAN.md`
owns scope). Keep it current as work proceeds.

## Cycle: automatic semantic releases (2026-11-06)

### Why the main merge produced no release

Release Drafter only updates a draft release; a maintainer had to open the
draft and publish it, and publishing is what creates the tag that starts
`release.yml`. The merge to `main` therefore left a new draft and no release.
The repository also carried several old drafts and two version owners (the
draft's label resolver and the tag's version), which is the handling defect to
remove, not a missing manual step.

The first automatic run then failed before it started: the repository Actions
policy allows GitHub-owned actions plus a short allowlist of named third
parties, and `cycjimmy/semantic-release-action` is not on it. A blocked action
produces `startup_failure` with no jobs and no logs, which is exactly what run
`42221171098` recorded. A release tool is a CLI, not an action: the workflow
now installs the pinned packages with npm and runs the binary directly, so
every action it uses is GitHub-owned.

### Shipped

- `.github/workflows/auto-release.yml` runs after the whole `ci` workflow
  succeeds on a push to `main` (the fast gate and, on main, the exhaustive
  gate). It checks out the exact commit CI tested, refuses a commit that is
  not on `main`, and runs semantic-release with pinned `25.0.9` and pinned
  default plugins.
- semantic-release computes the next version from the conventional commits
  and creates the tag and GitHub release. The npm plugin is deliberately
  absent: Mailsense is not an npm package; the tag is the product.
- The release step snapshots the remote tags before and after the run and
  emits the new tag as a step output. The tag refs are the fact, not a parsed
  release name; the log only decides whether a missing tag is worth waiting
  for, and a run that released nothing is a successful no-op.
- `@semantic-release/github` targets the new tag at the branch name, so a
  merge that lands while CI runs could put the tag on a commit this gate never
  tested. The workflow moves the just-created tag to the checked-out HEAD and
  verifies the ref before it dispatches the build.
- A tag created through `GITHUB_TOKEN` does not trigger a `push` workflow.
  The workflow therefore dispatches `release.yml` at the new tag, after
  checking the tag is visible. `release.yml` still refuses a branch ref and
  verifies the tag is an ancestor of `main`, so a manual dispatch cannot
  publish a branch.
- `release-drafter` and its config are removed, and `.github/labeler.yml` no
  longer names it. There is one version owner: the commit history. The
  existing `release.yml` remains the one builder and publisher of the
  installer, SBOM and checksums.
- `CHANGELOG.md` stays curated; the generated release notes are the commit
  summary on the GitHub release. The release body is not overwritten by the
  publish job.

### Verification

- `semantic-release --dry-run` on `835f5d7` analyzed 81 commits and resolved
  **0.2.0** (a minor release from the `feat` commits), with generated notes.
- The first `auto-release` run (`42221171098`, `b44d481`) was a
  `startup_failure` with no jobs; `gh api
  repos/.../actions/permissions/selected-actions` showed the allowlist did not
  contain the action. After the rewrite, the workflow uses only
  `actions/checkout` and `actions/setup-node`.
- `actionlint` on the workflow; the tag-diff and tag-resolution logic
  exercised locally against a scratch remote; `scripts/test-fast.sh
  STAGES=lint`; PR CI.
- The merged workflow's own `auto-release` run (`42224808758`, `3ea10cc`)
  completed in 67 seconds: it analyzed 85 commits, resolved **0.2.0**, created
  the tag at the tested commit, and dispatched `release.yml`
  (`42224955299`). That build completed successfully. `SHA256SUMS` verifies
  the 9,072,600-byte installer and the SBOM, and both `main` and the tag
  point at `3ea10cc`.

### Next queue

- If a release build fails after the tag exists, re-dispatch `release.yml` at
  the tag; the workflow is idempotent and uploads the assets to the existing
  release. A future reconciliation job can do that automatically.
- The old draft releases are left untouched; they are unpublished and do not
  affect the tag history.

## Cycle: measured rules and the definitional gate (2026-11-05)

### What the gold dev split measured

The rule table was measured on the sealed gold sample's dev split (240
messages; the test split was never read) and on train+dev. The dev table is
the check a shipped answer rule must survive, and it rejected most of the
table:

| rule | dev fired | dev precision |
| --- | ---: | ---: |
| `question_mark` | 92 | 0.565 |
| `request_one` | 43 | 0.651 |
| `request_two` | 20 | 0.200 |
| `request_three` | 18 | 0.111 |
| `signature_none` | 191 | 0.414 |
| `deadline_weekday` | 12 | 0.250 |
| `deadline_any` | 22 | 0.364 |
| `urgency_today` | 11 | 0.091 |
| `info_missing_attachment` | 5 | 0.000 |
| `decision_meeting_request` | 1 | 0.000 |

The rules that survived are definitional facts: `not_susp_known_pass`
(119/119), `not_susp_internal` (27/27), `reply_not_bulk` (5/5),
`signature_found` (10/10), `reply_not_cc` (11/12), `rel_internal` (25/27),
plus the zero- or near-zero-coverage facts that the taxonomy definition and
the signal semantics guarantee (`asks_meeting_request`,
`cat_report_notifications`, `cta_meeting_request`, `rel_directory`,
`rel_precedence`, `topic_calendar`, `topic_meeting_kind`). Nine candidate
rules whose answer was not definitional were rejected and left out of the
shipped table: the urgency/decision/stance families and
`info_missing_attachment`.

The zero-label experiment (`e4-rules-unmeasured`, now a true zero-teaching
install: the label sources stay defined, no label rows are stored) trained
layer 1 from the rules alone. It reached **0.292 mean dev accuracy** against
**0.663 for the legacy decider**; the per-question majority aggregate was
0.443, but several questions had no training target, so the aggregate is
only indicative. The model beat the majority on `is_question` (0.738 vs
0.688), `relationship` (0.183 vs 0.108), `has_signature` (0.429 vs 0.388)
and `category` (0.087 vs 0.067), and lost badly on `urgency` (0.000 vs
0.787), `decision_needed` (0.000 vs 0.967) and `request_count` (0.250 vs
0.362). Unmeasured rules are not a teacher: `unmeasured_precision` stays
`null`, so a rule with no measured target teaches nothing.

### Shipped

- `evidence.Engine.RuleAnswers` now applies only `definitional` rules. An
  estimate (a question mark, a named date, a question count) remains
  evidence and a teacher only; it answers only after its precision clears
  `evidence.rules.min_precision` on this install.
- The rule table has no wordlist: `attachment_mention` (27 hand-written cue
  phrases across six languages, consumed by no rule) was deleted. Signals
  carry `Lexical` when their extractor's value is decided by a fixed
  vocabulary (for example `is_reply`'s shipped subject prefixes, kept for
  threading); `LoadRules` and `TestNoRuleUsesALexicalSignal` both refuse a
  rule that reads one. The generated reference prints both `Lexical` per
  signal and `Definitional` per rule.
- The measurement harness now composes experiments over the **shipped**
  teacher document and writes an explicit null for every shipped source an
  experiment removes. Before this, a null for a shipped source was lost in
  the harness's own merge and the "zero-label" run silently trained on 119
  gold labels. `TestRulesOnlyExperimentIsAZeroLabelInstall` pins the
  composition and the empty label store.
- A non-finite `PathNLL` (a single-class question, no scored selection
  label) no longer reaches the report: it is omitted and the λ choice falls
  back to `DefaultLambdas`. An infinite float cannot be stored as JSON and
  failed a real run's commit. `TestPathNLLOmitsUnscoredLambdas` is the
  regression; the golden model pins the intended weight change.
- The sidecar downloader can follow a pinned artifact's cross-origin CDN
  redirect (GET/HEAD, same scheme, SHA-256 and size still verified). Without
  it a fresh install failed at
  `httpx: cross-origin redirect refused` on the Hugging Face CDN, so the
  model could not be installed on another machine.

### Verification

- `scripts/evidence-fixture.sh`: 33 s, dev rule table written to
  `out/evidence-rules.json`.
- `scripts/layer1-fixture.sh run e4-rules-unmeasured`: true zero-label run,
  mean dev accuracy 0.2919 (legacy 0.6627).
- `go test ./internal/evidence ./internal/triage ./internal/layer1
  ./internal/ui`; `go test ./internal/httpx`; `make fast`.

### Zero-shot taxonomy prototypes (measured, rejected as a general classifier)

`e8-definitions` embedded each question's own instructions and class
descriptions with the local EmbeddingGemma and classified dev messages by
the nearest description — no rule, no wordlist, no labels. Mean dev
accuracy was **0.4265 against 0.4319 for the per-question majority**. It
beat the majority on `topic` (0.342 vs 0.129), `asks_for` (0.254 vs 0.025),
`has_signature` (0.358 vs 0.054), `info_sufficiency` (0.417 vs 0.358) and
`call_to_action` (0.487 vs 0.433), but lost on `reply_needed` (0.775 vs
0.988), `is_question` (0.633 vs 0.804), `effort` (0.404 vs 0.642) and
`stance` (0.346 vs 0.512). It is not accurate enough to be an answer or a
teacher. The experiment stays as the reproducible baseline for a
generative judge.

### Next queue

- The remaining semantic questions (`urgency`, `decision_needed`,
  `sentiment`, `stance`, `effort`, `info_sufficiency`, `request_count`)
  need an automatic teacher. The local embedding-only path is measured and
  insufficient; the next candidate is a small local chat model as the
  `judge` provider (automatic labels, no owner teaching), measured against
  the same gold dev split before any production wiring.
- The sidecar manages embedder runtimes only; a local chat runtime and its
  model catalog are a separate feature.

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

### Job strip names (2026-11-05)

- The owner's strip said `Outlook · En cours…` because the job name is the
  connector id and the sync never reported a phase. A job now composes its
  translated kind and subject (`Mail sync · Outlook`) and a running sync
  reports a stable stage (`folders`, `details`); the UI translates it and
  suppresses the generic `Working…` when the phase is already named. The
  producer's English message stays a Logs diagnostic.
- The Sources backfill cancel names its source (`Stop the backfill · Outlook`)
  instead of the generic **Cancel**. The job tests cover both stages, the
  composed name and the cancel label.

### No-teaching answer layer (2026-11-05)

- First run had a real coupling defect: `NewPlan` selected `triage.decider`
  unconditionally, so a missing provider failed training, status and teacher
  choice even though none of them calls a provider. `NewPlan` now reads the
  route key's unconfigured decider identity and selects strictly only when an
  id is configured. `TestPlanWithoutDeciderKeepsLocalTrainingUsable` pins the
  empty-decider plan, its route key and a local teacher choice.
- The 17-question detail was reading route abstentions only: the route cannot
  certify a question without labels, so every value said **No usable
  answer**. `evidence.Engine.RuleAnswers` now applies the shipped rules to
  stored evidence at read time. It fills only `not_certified`; a conflict, a
  score range or two exact levels answers nothing. The answer carries its rule
  id, is never stored, never counted as coverage and never opens the gate.
  The detail and list name it **automatic rule · uncertified** and count it
  apart from certified answers (`TestRuleAnswersFillOnlyUncertifiedAbstentions`,
  `TestRuleAnswersPageCountsSeparately`, plus the triage browser cases).
- Two dimensions gained deterministic coverage without a model: `request_count`
  from `question_marks` (0, 1, 2, 3+ as separate rules) and `call_to_action`
  from a meeting request (true) or machine-written mail (false). Existing
  evidence already carries sender relation, reply rate, auth results, thread
  gap and deadline cues; the remaining semantic questions still need a model,
  and the UI says so instead of printing a bare zero.

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
