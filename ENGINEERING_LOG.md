# Engineering log

This is the durable record of improvement cycles: what was found, what was
changed, what was tried, what failed, and what comes next. It is not a changelog
(the user-facing history lives in `CHANGELOG.md`) and not a plan (`docs/PLAN.md`
owns scope). Keep it current as work proceeds.

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
