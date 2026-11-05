# Developing Mailsense

How to build, test and extend the Go code. The contract is `ARCHITECTURE.md`;
the local HTTP surface is `API.md`; the open work is in `RELEASE_PLAN.md`.

## Prerequisites

- Go 1.27 (`~/.local/go/bin/go` on the dev box; the scripts find it on `PATH`
  or there, or use `GO=/path/to/go`).
- Bash ≥ 4.4 for the gate scripts (`mapfile`, associative arrays); macOS's
  `/bin/bash` 3.2 needs a newer bash on `PATH`.
- No C toolchain is needed: every build is `CGO_ENABLED=0`. If `gcc` is
  present, `scripts/test.sh` adds `-race` automatically.
- Network only for the first module download (`proxy.golang.org`).

## Scripts (`scripts/`)

| Script | What it does | Parameters |
| --- | --- | --- |
| `build.sh [linux\|windows\|all]` | Pure-Go builds into `dist/`: `mailsense` (Linux dev) and `mailsense.exe` (windows/amd64, `-s -w -H windowsgui`, no console). Both `-trimpath` and version-stamped. | `VERSION`, `OUT_DIR`, `GOARCH`, `GO` |
| `test.sh [go test args]` | Exhaustive gate: gofmt check → script self-test and `bash -n` → `go vet` (one host pass over every package, then a pass per tag: `-tags fixture` on the packages that declare it, `-tags triagelive`, `-tags deciderbench`, `-tags judgegold`, `-tags embedlocal`, and `GOOS=windows` with `outlooklive`, `evidencelive` and `embedlive`; the full windows pass belongs to the windows CI job) → taxonomy check (`gen-taxonomy.sh --check`) → the port-sensitive sidecar suite alone → `go test` on every other package (`-race` when cgo works). The gofmt/script/vet/taxonomy checks come from `test-lib.sh`, shared with `test-fast.sh`. | `RACE=auto\|on\|off`, `GO` |
| `test-fast.sh [go test flags]` | Fast gate for the inner loop: the same gofmt, script, vet and taxonomy checks, three test shards concurrently, the port-sensitive sidecar suite alone, and `-race` only on `qa` and `api`. Every package is assigned exactly once; a new package outside the named groups lands in shard C. Use `test.sh` before a release. `STAGES=lint` runs only the gofmt, script, generated, PII and vet half; `STAGES=tests` runs only the suites; `STAGES=sidecar` runs only the port-sensitive suite. `SHARDS` selects which shards this process runs, so CI gives each of A, B, C and RACE its own runner. | `STAGES=all\|lint\|tests\|sidecar`, `SHARDS=A,B,C,RACE`, `SHARD_P` (default 4), `RACE`, `SIDECAR=on\|off`, `TEST_TIMEOUT` (seconds, default 300), `GO` |
| `test-ui.sh [playwright args]` | Browser end-to-end gate: builds the real binary, starts one server per Playwright worker on an isolated temporary data directory, and runs `e2e/specs` in Chromium. Auth, CSP, static serving, 401 and SSE are real; mail content endpoints are synthetic fixtures. Covers the token handshake, primary routes, Ask conversation, Mail, Companies, live Learning progress and Logs. Windows-only fidelity stays in the manual/live sweeps. | `MAILSENSE_E2E_INSTALL=1` installs the pinned Chromium |
| `fixture-import.sh [file]` | Imports a legacy JSONL export (plain or gzip) into a fresh DB through archive → syncer → store → scope and prints counts, wall time, DB size, verdicts per rule and FTS checks; then indexes it with the lexical fake embedder and checks search (full-text precision, known-item retrieval, filters, coverage, degraded path, latency). The export names no owner: the fixture config (`fixtureconf`) picks its mailbox and declares the owner's addresses and time zone from `<export>.owner` (private, next to the export: one address per line and a `zone=` line). With the default `$FIXTURE_DIR/mailbox.jsonl` (`lib.sh`: `~/.local/share/mailsense/fixture`, mode 700/600 — the developer's personal fixture lives there, never in the repository nor `out/`) it asserts the ground truth (the one place that number lives) and uses its French check words; another export must set `FIXTURE_TERMS` (the pair check is skipped, with a notice, without `FIXTURE_FTS_PAIRS`). | `FIXTURE_EXPECT`, `FIXTURE_FTS_PAIRS`, `FIXTURE_TERMS`, `FIXTURE_OWNER`, `FIXTURE_ZONE`, `FIXTURE_DATA_DIR`, `GO` |
| `update-unicode-data.sh [<ucd-version> <cldr-tag>]` | Regenerates `internal/mail/scripts.json` (spaceless: CLDR scriptMetadata "LB letters"), `fold.json` letters (CLDR Latin-ASCII) and `punctuation.json` (UCD: "QUESTION MARK" + Terminal_Punctuation) from the pinned UCD/CLDR with `scripts/datagen`; hand-kept keys (`substring_extra` — Hangul and the 10 matra-based Indic scripts — and `strip_marks`) are preserved; records inputs and outputs (SHA-256) in `internal/mail/unicode-pin.json`; without arguments re-downloads the pin and verifies it. The UCD version must be Go's `unicode.Version` (a test checks). | `GO` |
| `update-locale-data.sh [<cldr-tag> <udhr-commit> <unicode-version>]` | Regenerates `internal/locale` (`langid.json`, `locales.json`, `currency.json`, `confusables.json`) from pinned CLDR (coverage levels, supplemental data, likely subtags, language aliases, the main files of `params.json`'s coverage levels, their regional locales (`regional_status`) and their parents — the list is `datagen locale-ids`; a regional locale CLDR ships no file for answers 404 and is skipped), the UDHR in XML project at one commit and UTS #39 confusables, with `scripts/datagen locale` and the hand-kept choices of `internal/locale/params.json`; records every input and output SHA-256 in `internal/locale/pin.json` (a test checks the outputs); without arguments re-downloads the pin and verifies it (`docs/EVIDENCE.md`). | `LOCALE_KEEP` (keep the downloaded sources in a new directory), `GO` |
| `evidence-fixture.sh [file]` | Imports the fixture, indexes it with the lexical fake embedder, runs the evidence engine twice (first and unchanged) and writes aggregate reports: `out/evidence-fixture.json` (import wall in the same process, runtime, database growth, the stage, coverage and distribution per signal) and `out/evidence-rules.json` (the rule table on a gold sample's TRAIN and DEV labels only; the test split is never read). | `EVIDENCE_GOLD_DIR` (default: the newest `~/.local/share/mailsense/bench/sample-*`; `none` skips), `EVIDENCE_REPORT_DIR`, `FIXTURE_OWNER`, `FIXTURE_ZONE`, `GO` |
| `update-prefixes.sh [oldid]` | Regenerates `internal/mail/subject_prefixes.json` from a pinned revision of Wikipedia's "List of email subject abbreviations" (+ the kept extras with provenance), pinned in `prefixes-pin.json`. | `GO` |
| `update-psl.sh [commit]` | Downloads the Public Suffix List at a pinned commit of github.com/publicsuffix/list into `internal/psl/public_suffix_list.dat` and records source, commit, date, size, SHA-256 and licence in `internal/psl/pin.json`; without an argument re-downloads the pinned commit and checks its hash (a test checks the shipped file against the pin). | `GO` |
| `gen-taxonomy.sh [--check]` | Regenerates `internal/triage/taxonomy/default_profile.json` from the legacy Python taxonomy (or checks it is unchanged; `test.sh` runs the check while the Python source exists). | `PYTHON` |
| `embed-local.sh [messages]` | Dev-box benchmark with the real model: the pinned Linux llama-server and model through the sidecar (downloaded once, hash-checked, into `~/.cache/mailsense-runtime`), a fixture sample indexed (throughput), int8 vs float32 recall, hybrid vs full-text known-item retrieval, search latency (`docs/EMBEDDINGS.md`). The `embedlocal` test reads the private provider catalog (`EMBEDLOCAL_PROVIDERS`, default the app data dir's `providers.json`, record `EMBEDLOCAL_PROVIDER`, default `local-embedder`) and accepts `EMBEDLOCAL_BATCH` to override `embed.batch_size` for an A/B; neither is read by the shipped app. | `FIXTURE_JSONL`, `EMBEDLOCAL_RUNTIME_DIR`, `EMBEDLOCAL_BATCH`, `EMBEDLOCAL_PROVIDERS`, `EMBEDLOCAL_PROVIDER`, `GO` |
| `triage-live.sh <record-id> [messages] [repeat]` | Small live check against one decider record: the protocol probe (a 402 stops it), then at most 30 fixture messages triaged in a temporary database and `repeat` of them decided again (agreement). The record's `api_key_secret` names the key read from `.env`; only that key enters the test process, never printed. Its run budget is set in the script (the one place). | `ENV_FILE`, `PROVIDERS_FILE`, `TRIAGE_JSONL`, `GO` |
| `judge-gold.sh <record-id> [messages]` | Live gold validation of one generator record (dev only): the newest sealed gold sample's DEV labels, a deterministic sample, the real judge calls, per-question agreement and calls/tokens/USD. The record's `api_key_secret` names the key read from `.env`; only that key enters the test process. | `JUDGE_GOLD_MAX_USD`, `PROVIDERS_FILE`, `GO` |
| `run-windows.sh` | Starts `dist/mailsense.exe` (or `RUN_EXE`), waits for the loopback API's `runtime/api.json`, and prints the base URL. Never prints the token. On a non-Windows host it starts the named Linux dev binary instead (smoke tests). | `RUN_EXE`, `RUN_TIMEOUT` (seconds, default 60), `MAILSENSE_DATA_DIR` |
| `stop-windows.sh [grace]` | Reads `runtime/api.json`, checks the PID, POSTs `/api/shutdown` with the token in curl's stdin config, and waits for the process and descriptor to disappear. Never kills and never prints the token. | `grace` (seconds, default 30), `STOP_TIMEOUT` (HTTP seconds, default 10), `MAILSENSE_DATA_DIR` |
| `runtime-lib.sh` | Shared helpers of the two runtime scripts: data directory and descriptor paths, descriptor field reads, loopback URL validation, PID liveness. Sourced, not run. | |
| `bench-deciders.sh run <provider>… \| score \| export` | Decider benchmark (below): the named providers decide the same stratified primary-inbox sample of the fixture under one hard cap; aggregate report in `out/bench-deciders.json`, gold-judge export in `BENCH_DIR`; `score` scores the answers against `BENCH_DIR/gold-labels.jsonl` into `out/bench-gold.json`; `export` (no provider call, $0) writes a stratified gold-judge sample of the whole chosen mailbox into a new private directory (below). Keys come from `.env` under each record's `api_key_secret`, never printed. | `BENCH_DIR` (required for run/score), `BENCH_SAMPLE`, `BENCH_SEED`, `BENCH_REPEAT`, `BENCH_CONCURRENCY`, `BENCH_REPEAT_CONCURRENCY`, `BENCH_ROUND`, `BENCH_MAX_USD`, `BENCH_MAX_CALLS`, `BENCH_REPLY_WITHIN`, `BENCH_GOLD`, `BENCH_REPORT`, `BENCH_GOLD_REPORT`, `BENCH_JSONL`, `ENV_FILE`, export: `BENCH_EXPORT_SIZE`, `BENCH_RARE_FLOOR`, `BENCH_EXPORT_ROOT`, `BENCH_LEGACY`, `BENCH_KEEP_LABELS`, `GO` (defaults in the script) |
| `bench-startup.sh [--store DIR] [--binary PATH] [--out DIR]` | Start-up phases on a prepared store with `GOMAXPROCS=4` and no display: `listen_ms` from the runtime descriptor (the listener is up and the window can open), `ready_ms` from the `mailsense ready` log after the background warm, the per-view warm durations, and a `/proc` sample at ready (CPU, VmRSS/VmHWM, FDs, threads). A build that writes the descriptor at readiness reports its old readiness as the listen proxy. Never prints mail content or the launch token. | `PERF_STORE_DIR` |
| `e2e/bench-first-click.mjs --binary PATH --store DIR [--runs N] [--out FILE]` | First-shell measurement with Playwright/Chromium: process start to `#app`, to the first route skeleton, to the first Mail click, and click-to-list. Records the first batch and its DOM, the all-rows time and long tasks, splitting each long task at the first-frame mark into first-frame and deferred maxima/totals. Waits for the descriptor, seeds the token in session storage, never prints it, and stops the app through `/api/shutdown`. JSON per run. | — |
| `e2e/bench-cold-load.mjs` | Cold HTTP-cache measurement of Home: builds and starts a real binary on a temporary store, launches a fresh Chromium context, clears the browser cache through CDP, then records cold and warm navigation, route-mount, request, byte and cache-hit counts with per-asset Resource Timing. Fails if any cold request was served from cache. Writes `out/bench/cold-load.json`; never prints the token or mail content. | — |
| `bench-perf.sh [--store DIR] [--out DIR] [--keep]` | The three user-facing performance paths on the routed fixture: start-up, `/api/status`, `/api/learning` and the label-stage count; the first `/api/triage` page and `/api/triage/summary`; FTS-first search with the embedder down. Imports the fixture into a temporary store (or reuses an already prepared `--store`, never modified) and writes synthetic route answers, builds the app, runs five samples per endpoint, prints aggregates only, and exits nonzero when a metric is more than 20% over its documented budget. Never prints mail content or the launch token. | `PERF_STORE_DIR`, `PERF_OUT`, `FIXTURE_JSONL`, `FIXTURE_OWNER`, `FIXTURE_ZONE`, `GO` |
| `gold-seal.sh [gold dir]` | Seals the TEST split of a gold sample (`internal/goldset.Seal`, run through `go run ./scripts/goldseal`): every line and value naming a test message, in every file of the directory, moves into `<dir>/sealed/` (mode 000); prints counts only. A second run seals only what was added since. | (default: the newest `~/.local/share/mailsense/bench/sample-*`), `GO` |
| `layer1-fixture.sh embed [phases] \| run [experiment] \| summary <run> \| compare <a> <b> \| stop [run\|embed]` | The layer-1 dev harness (`docs/LAYER1.md`): `embed` puts real EmbeddingGemma vectors (pinned Linux sidecar) into the private vector cache (`~/.cache/mailsense-layer1/vectors`; phases `sample`, `sample:<purpose>`, `full`; ≈5 messages/s: run it in the background); `run` trains layer 1 over the fixture with the gold dev and test messages excluded from every target and measures it on the gold DEV split only (`out/layer1-<experiment>.json`, a line of `out/layer1-dev-ledger.jsonl`; per-item results and error lists only under `~/.cache/mailsense-layer1/runs`); the legacy Ondelle teacher is the harness's own teachers override (the shipped teachers have no legacy source); `summary` prints a run's per-question dev table (accuracy, the honest dev certification at 0.99, the dev curve); `compare` the paired bootstrap of two stored runs (accuracy and coverage at 0.99); `stop` asks a running harness to stop (a stop file it polls). Never edit the script while a run uses it (bash reads it as it goes). | `FIXTURE_DIR`, `FIXTURE_JSONL`, `LAYER1_GOLD_DIR`, `LAYER1_LEGACY`, `LAYER1_CACHE_DIR`, `LAYER1_SAMPLE`, `LAYER1_REPORT_DIR`, `LAYER1_POPULATION`, `LAYER1_RUN`, `LAYER1_BASELINE`, `LAYER1_TIMEOUT`, `EMBEDLOCAL_RUNTIME_DIR`, `GO` |
| `lib.sh` | Shared helpers (repo root, Go/gofmt lookup, `kv_get` for key=value / .env files, `fixture_owner` for the export's private owner file, `live_limits` for `LIVE_DETAILS`/`LIVE_RATE`, `gold_dir` for the newest gold sample); sourced, not run. | |

Long runs (full gate, fixture import) are best started in the background.
Never stop processes with `kill`/`pkill`: `scripts/stop-windows.sh` asks the
app to stop through its own API and waits for it.

The repository does not ship a live Outlook harness, so the live checks
against a real mailbox cannot be run from a clone.

## Running the app

`cmd/mailsense` has no flags. It loads the config, starts logging, opens and
migrates the store, seeds the pinned local embedder into the owner's
provider file on first start (`embed.provider` still at its default), then
builds the provider catalog, the triage profile and the
decider (`triage.decider`), the semantic side (an embedder that is
unavailable or misconfigured on this machine is logged, not fatal: the
embeddings stage blocks learning, so the gate keeps search closed), the
evidence engine (started after every sync and every index run that
committed work), the learning machine
with every stage (the gate of every answer; its status is logged and
refreshed after every sync, discovery or selection change, index run and
settings change), the install-profile learners (after every sync), the
syncer's mailbox actions, triage and search, then serves the loopback API
(`internal/api`) and waits for `POST /api/shutdown` (or the OS process to be
closed). Every service is kept in one `app` value for the API. After the
listener's descriptor exists, `startWarm` runs one cancellable background
task (`cmd/mailsense/warm.go`): `warmDerivedTracked` launches the priority
ranking as its own goroutine, runs the independent early screens beside it,
joins the ranking, then builds the four dependent views and the Brief; the
semantic prepare runs after that, and `boot.ready()` plus `mailsense ready`
are emitted only after it returns. Every stage asks `warmPolicy` first: the
resource guard may refuse a stage, which is recorded `skipped` (never
ready, never failed) with its ceiling, and the route builds it on first use;
a skipped stage is not retried inside the same warm. Store-scanning stages
are bounded by `warmHeavyBuilds` (3) so the warm leaves a connection for the
owner's first click. First-open discovery and the semantic maintenance
worker wait on the same `warmDone`; shutdown cancels the task and joins it
for up to 15 s.
The Round 37 `runWarmAndPrepare` overlap (prepare beside the derived warm)
was implemented and measured, then removed because it raised ready and HWM;
do not reintroduce it without a fresh paired gate. After the
API is announced, a new install with no discovered mailbox and no selection
gets one background discovery; picking a mailbox starts its first sync.
The same post-ready phase starts one semantic maintenance worker:
`Indexer.Ensure` once, one `Searcher.Warm`, then `Indexer.Ensure` every
2 minutes (`indexReconcileInterval`). It logs coverage and throttled
failures, so a restart, a provider-space change or a failed/stalled run
heals without waiting for a sync; it never blocks API readiness. The local
sidecar stays up for the app lifetime. The first full index is not instant:
35k messages take about an hour at the measured ≈10 messages/s, newest
first; later runs are incremental, and coverage is reported until the index
is complete.
Any startup failure (no data directory, an invalid setting, the triage
profile or rules with every problem listed, logging or database failure)
exits 1. A malformed provider file or embedder configuration is logged and
the affected stage reports its blocker instead, so the owner can fix it in
the running app. It also writes
`startup-error.txt` in the data directory (or the temp directory when the
data directory is unknown) and shows the error: a native message box on
Windows, where the GUI exe has no console, and stderr elsewhere. A good start
removes a stale report.

```sh
MAILSENSE_DATA_DIR=/tmp/ms MAILSENSE_LOG_CONSOLE_ENABLED=true go run ./cmd/mailsense
```

The server binds `tcp4 127.0.0.1:0` only. Each launch generates a 256-bit
bearer token, writes it with the base URL and PID to
`<data>/runtime/api.json` (mode 0600), and removes that descriptor on a
graceful shutdown after checking it still names this launch. The token is
never logged; the API accepts it as `Authorization: Bearer` on every
`/api/*` path and as `?token=` only on `GET /api/events`. The static shell is
served without auth. A second instance with the same data directory exits 1
(`mailsense.lock` is held for the process lifetime).

Start and stop it without a console or `kill`:

```sh
scripts/run-windows.sh                  # waits for runtime/api.json, prints the URL
scripts/stop-windows.sh                 # graceful POST /api/shutdown, then waits
```

`run-windows.sh` never prints the token; `stop-windows.sh` reads it into
curl's stdin config and never puts it in argv. Both honour
`MAILSENSE_DATA_DIR` (and `RUN_EXE` for a Linux dev build).

Data directory: `MAILSENSE_DATA_DIR` (made absolute), else
`%LOCALAPPDATA%\Mailsense` on Windows, else `$XDG_DATA_HOME/mailsense` or
`~/.local/share/mailsense`. It holds `settings.json`, `mailsense.db` (+ WAL),
`logs/`, while running `runtime/api.json`, and on Windows
`secrets.dpapi`.

## Live progress and event freshness

**Failure that motivated this contract.** `/api/jobs` showed the running
index at `done=13696/34938` while `/api/learning` still returned
`embeddings {state:"running", progress:{done:0,total:34938}}`. There were
two independent causes. `index.Stage.Status` only knew the persisted
coverage from a full `plan`, so it reported the coverage before the run;
and `wireLearning` subscribed the learning machine to `index.finished`
only, so no progress event invalidated the status the API answered from.
The UI was already listening to `jobs.update` and forcing a read, but the
backend status it fetched was stale.

**Live run state.** `index.Indexer` keeps one `liveRun` (active, space,
eligible, selected total, done, embedded, unchanged, failed, startedAt),
written only by the run goroutine and read under a small mutex. It is
derived from the existing `Report`/`Progress`: `run` starts it after the
plan, `progress` copies each batch's counters into it, and `finish` clears
it before publishing `index.finished`. There is no second counter.

While a run is active in the embedder's current space,
`index.Stage.Status` returns `State=running` with the live done/total, the
run's embedded/unchanged/failed breakdown, and no coverage plan. The total
is the work selected for this run; when it is zero, the eligible count is
the denominator. When the run ends (success, failure or cancellation), the
stage falls back to the existing full coverage plan, so the final status
shows indexed/eligible, never the last live counter.

**Invalidation without a refresh storm.** `wireLearning` subscribes
`learning.RefreshOnProgress` to `index.TopicProgress` with
`learning.ProgressRefreshInterval` (one second), in addition to the
immediate `learning.RefreshOn` on `index.TopicFinished`. The progress
helper coalesces a burst into at most one rebuild per interval, never
invalidates while a rebuild is in flight, and leaves the final event to
the unthrottled finished subscription. `Machine.Invalidate` records a
rebuild requested during an in-flight compute (`again`), and
`refreshStatus` runs once more instead of losing that final change behind
an older revision. This is what guarantees the post-run status comes from
the coverage plan rather than the last progress event.

The 2-minute `Indexer.Ensure` reconcile is unchanged: it may start a
normal index job, and the job's events drive the same UI updates.

**kNN is bounded by the population.** kNN pools each answer with the
labelled population's nearest neighbours, and its scan is O(population)
per message and question. Above `knnMaxMessages` (2,000) `Train` sets
`plan.Config.KNN = nil` and logs why: at 34,763 messages the commit held
the SQLite writer for ~13 minutes (the run reached 17/17 and then blocked
every other write, including the Ask history save). Below the bound the
setting behaves as before. `store.busy_timeout` (30 s in the owner's
install) covers the short commit that remains.

**Structured, precise job progress.** `jobs.Progress` also carries
`stage` (a stable catalog key), `detail` (the current item, such as a
question name: data, never a sentence), `step_done`/`step_total` (the
position within the stage) and `eta`. The ETA has one owner:
`jobs.Manager.call` measures every report against the job's own average
rate since it started (`etaSince`), so no producer has to estimate one; a
producer that measures a better one sets `eta` itself. `layer1` reports
its pass through `Plan.Step`: design, gather, each question's λ path
(`fit`), its folds (`fold`), its measurement and certification, then
settle and answer. The answer phase counts every message's three passes
(decisions, route answers, preview) and gives the commit its own
denominator, so the job never sits at 100% while it writes; its ETA
starts from the new total. The runner keeps its latest `jobs.Progress`
behind a mutex, and `layer1.Stage.Status` answers from that live run before
reading the stored report or re-fingerprinting the store; `wireLearning`
subscribes `RefreshOnProgress` to `layer1.TopicProgress`.

**One progress model.** `layer1` has no progress type of its own: the run
callback, the live copy and `layer1.TopicProgress` are `jobs.Progress`, so
the job chip and the Learning timeline read the same fields. The producer
owns the unit (`unitForStage`: `questions` for every question phase,
`messages` for the answer phase), and `layer1.Stage.Status` copies `unit`,
`step_done` and `step_total` into `learning.Progress` instead of hardcoding
`questions`. The chip renders the unit beside the count and the timeline
renders the step, so the two can never disagree about what is being
counted or where the run is. A run that is active shows
running even when the stages it learns from are not done (a manual or
triggered run), because the run is the truth. Every stage key is
translated in both catalogs; `internal/ui` has a contract test that reads
the `layer1` stage constants and fails when one has no label.

The UI event map is in `docs/UI.md` ("Live updates and freshness"):
`index.progress` paints the mounted embeddings step in place without a
fetch; `jobs.update` still forces `/api/learning` and refreshes the job
list; `learning.status` remains the authoritative status. There is no
global poll.

Tests: `internal/index` covers live done/total before finish and the
learning machine seeing live progress plus the final coverage;
`internal/learning` covers burst coalescing and a final invalidation that
lands during an in-flight rebuild; `internal/ui` covers live
done/total/detail rendering, the direct `index.progress` paint, the
forced `jobs.update` read and the no-composer-reset rule.

## Module map

Dependencies point downwards only. The leaves are `clock`, `jsonx`,
`runstate`, `events` and `fsx`, and `config` sits on them. `events`,
`config`, `fsx` and the leaves never log, because `logx` depends on them.
They report problems through errors, counters and events instead.

| Package | Owns |
| --- | --- |
| `internal/clock` | The one time source (`Now`, context-aware `Sleep`); `clocktest.Fake` for tests. Production code never calls `time.Now`/`time.Sleep` directly (measuring the fixture's wall time is the exception). `Zone(name)` resolves `time.zone` ("" = not confirmed: `ErrZoneUnconfirmed`; "Local" refused — it names whatever the machine reports; the IANA database is embedded for Windows); `Detected` is the machine's zone as a `Proposal` {IANA name when the machine configures one (`$TZ`, the `/etc/localtime` link), abbreviation, offset} for the user to confirm. |
| `internal/mathx` | The one rounding rule (`Round`: half-up, never banker's, never truncation), the one median (average of the two middles), the one percentile rank (`Rank`: half-even, legacy's p90), the one exponential `Backoff`, `Allocate` (largest-remainder allocation), and the shared statistics: mean, population standard deviation, Pearson r, AUC (Mann-Whitney, ties ½), Brier score, Cohen's kappa. |
| `internal/jsonx` | Decode exactly one JSON value (plain or key-ordered `Object`); `DecodeStrict` (typed data files: unknown fields and trailing data are errors); `MergeObject` (per key, null removes); `PyDumps` (Python `json.dumps(sort_keys=True, ensure_ascii=True)` byte for byte, for legacy fingerprints); `Finite` and `Int` (the trust-boundary number rules: finite JSON numbers, integer literals; never a boolean or a string); JSON kind names for errors. |
| `internal/digest` | The one short hash: the first n hex digits of a SHA-256 (fingerprints, state hashes, store keys). |
| `internal/runstate` | The lifecycle states (running/succeeded/failed/cancelled) and the one rule `Of(ctx, err)`; shared by jobs, sync runs and the store. |
| `internal/events` | Typed pub/sub (`Topic[T]`, `Subscribe`, non-blocking `Publish`, per-subscriber bounded buffers, dropped counters); `On` (the one subscribe-and-loop: learning refresh, install-profile learners, the index run after a sync). |
| `internal/fsx` | Every file write: atomic replace (temp + fsync + rename, with retries on Windows sharing violations), `DirPerm`/`FilePerm`/`EnsureDir`, the size-rotating log writer (recovers from a failed rotation); `ReadOptional` for data files that may be absent. |
| `internal/config` | `schema.json` (every setting + cross-field constraints + named `vocabularies` shared by settings, e.g. `folder_role`), layering default → install profile (`install-profile.json`: learned values of `learnable` settings with sample size and provenance — basis as counts —, `Learn`, `Learned`, the user's `Reject`/`Unreject` of a learned value or item, kept across later learning) → `settings.json` → `MAILSENSE_*` env, strict validation, typed accessors, `Reload`/`Set`/`Reset` (re-read the file first, never overwrite one that does not parse), dry-run `Check`, `RestartPending`, `SecretMask`, `ParseEnviron`, `TopicChanged`. `configtest` builds configs for tests. |
| `internal/logx` | The logging pipeline: `logx.For(component)`, per-component levels, redaction (registered secrets, always-on built-in token/email patterns, additive `log.redact.patterns`, and the canonical content-key deny set), rotating JSON file, ring buffer, `Health()`, topics `log.entry`, `log.health`, `log.applied`, optional stderr. |
| `internal/httpx` | The one HTTP client: retry `Policy` (billing rule), Retry-After/retry-after-ms, logging transport (response bodies teed lazily at debug), `TopicRetry`, config-built transport. |
| `internal/secrets` | `Store` (Get/Set/Delete/List/Kind/ReadOnly); Windows DPAPI file store; env store (`MAILSENSE_SECRET_<NAME>`, read-only) elsewhere; the `observed` wrapper registers values for redaction and publishes `secrets.changed` (names only). |
| `internal/messages` | The Mail first-page read model: one immutable unfiltered all-mail page (`store.MessageListDefaultLimit`, `Counts: true`, no role/store/cursor) keyed by `store.MailGeneration`; at most one in-flight build per generation, waiters join and honor their own context, and the one build is detached from its starter (`context.WithoutCancel`) and bounded by `firstPageBuildTimeout` (2 min), so a cancelled leader never fails the joined callers; a failure is shared but never cached, every other page bypasses the cache, and callers receive independent copies. Because that build outlives its request, the service tracks it in a `join.Group`: `Close(ctx)` refuses a new build and joins the running one, so the store is never closed under it. After `Close` an already installed page is still served, and a generation that moved (`ErrClosed`) is refused rather than read. The startup warm and `messagesAPI.List` share one `wire` instance, so the first Mail click never rebuilds the warmed page. |
| `internal/join` | The one bounded join of detached background work: `Group.Track` admits a task and reports false once `Close` was called, `Done` retires it, `Closed` is the read side of the one closed flag, and `Close(ctx)` refuses new work and waits for the tracked tasks under the caller's context (`false` when it expires first). A cache or service that rebuilds off the request path owns its goroutines through this type, so the store is never closed under a reader. Used by `internal/viewcache` and `internal/messages`. |
| `internal/viewcache` | The one bounded, single-flight, stale-while-revalidate cache of the read services (`hub`, `world`, `agenda`, `conversations`, `commitments`, `brief`, `triage` pages and summaries): `TTL` bounds the staleness a generation cannot see, `Size` bounds the entry count and `MaxBytes` (with the required `Weigh` estimate, exposed through `Bytes()`) bounds the retained bytes of its values, evicting oldest-first but never the entry just installed, so one value larger than the budget stays alone. `Keep` decides whether a build may install and whether an installed value is still servable (a generation check), an expired entry is served immediately while one detached, timeout-bounded rebuild runs, and a rebuild that installs while another reader is inside `Keep` is recognised by the entry's install stamp instead of starting a second rebuild, and `Close(ctx) bool` refuses new builds, cancels the in-flight ones and joins them under the caller's context. After `Close` an installed entry is still served and a key with nothing installed returns `ErrClosed` instead of reading the store. |
| `internal/mail` | Source-neutral `Message`/`Folder`/`Recipient`, `OptInt`, `AddressedTo*` values, `TimestampFields`, address/DN/domain helpers (`NormalizeSenderQuery`: the one sender-filter validation and canonical form, `MaxSenderQueryRunes`), subject normalizer (`SubjectPrefixes`: the shipped `subject_prefixes.json`, pinned to a Wikipedia revision, plus the user's additions and learned ones), timestamp rules, `Validate` (also: every text field valid UTF-8), `Alteration` (the declared changes a connector made while reading: codes, `Message.Alter`), `CalendarMethod`, Python-compatible text helpers (`Strip`, `Words`, `WordCount`, `Head`, `Excerpt`: Unicode whitespace, code points; `Head`/`Excerpt` own a truncated result instead of retaining a larger body), `ThreadIDs` (Message-ID / In-Reply-To / References, from the transport headers when the fields are empty), `ParseHeaders` (a header block parsed once) / `HeaderValues`, `CompatView` (the one NFKC + default-ignorable fold of structural rules), `Fold`/`FoldMap` (search form in every script: NFKC of the whole string, Unicode case folding, default ignorables removed, nonspacing marks removed when the base character's script is in `fold.json` `strip_marks`, the CLDR Latin-ASCII letters of `fold.json`; an offset map for snippets), scripts (`ScriptOf`, `Scripts`, `Spaceless`, `NeedsSubstringSearch`: `scripts.json` spaceless from CLDR + the kept `substring_extra`: Hangul, whose words carry particles, and the 10 matra-based Indic scripts — Devanagari, Bengali, Gurmukhi, Gujarati, Oriya, Tamil, Telugu, Kannada, Malayalam, Sinhala — whose dependent vowel signs unicode61 treats as separators; they stay out of `Spaceless`, so word counts and triage facts are unchanged), `QuestionMarks` (`punctuation.json`, UCD), message kinds (`KindOf`: `kinds.json`; `Automated`: the machine-sent headers, `kinds.json` `automated`), `RegistrableDomain`/`Domains` (organisation key of a sender; shared domains group nobody), `Attachment`, `FolderRef`. The generated files are pinned in `unicode-pin.json` and `prefixes-pin.json` (tests check them). `mailtest` builds full messages, `Picked` (the selection of test stores) and a synthetic multilingual `Corpus` (language, script, IDN senders). |
| `internal/scope` | The ONE owner identity (`Identity` = `NewIdentity`: the one place owner addresses are normalized — lower case, the IDN domain in ASCII form, so Unicode, Punycode and full-width spellings are one address — and de-duplicated; RFC 5233 subaddresses, `Owns`; `Digest` names the persisted identity a verdict was judged against) and `Decide` (owner/shared/unattributed + reason, both with `Valid`): own output (sender or sent-on-behalf), Sent items of others, recipients, the addressed-to-me flag, Delivered-To/X-Original-To, the explicit "not addressed" flag, then List-Id. `EvidenceOf`/`AddHeaders` (headers parsed once). Pure; no I/O. |
| `internal/store` | SQLite (WAL, migrations in `migrations/`), messages/recipients/folders/sources (with capabilities), attachments and further folders, the one write path (`writer.go`: prepared statements, full-text entries written after the batch's other writes), the one bounded SQLITE_BUSY/LOCKED retry (`busy.go`: `BusyPolicy.WithBusyRetry`, built once at open from `index.store_busy_*` with the store clock, used by message upserts, embeddings, triage decisions/failures, labels, scope verdicts and usage; other errors return at once, cancellation returns the context error, an exhausted write returns `ErrStoreBusy` naming its owner), `visibleSQL` (THE predicate of every query over the user's mail: active folders of picked stores; `senderWhere` (the one exact-address/domain sender predicate shared by search and the All-mail page, compared with the `mailsense_lower` scalar over `mail.NormalizeAddress`); `folders.picked` follows `mailboxes.selected` before every read, `Generation` includes it), `rolesSQL` (the one role rule: over every folder of a message), mailboxes and identities (`SaveReported`, `Owners`: reported + declared joined once, `MailboxViews`: the picking screen's one query), the FTS write path (word index + trigram substring index, folded text incl. recipients, re-indexed at open in `sync.batch_size` transactions; `fts_text_version` names the folded form and rewrites both indexes, `fts_coverage_version` names the trigram coverage and scans the messages to insert only missing trigram rows, delete stale ones and leave the word index byte-identical, with `fts_version` as the downgrade mirror; coverage version 4 added the 10 matra-based Indic scripts — the text rewrite measured 17.97–18.35 s and +35.4 MB DB / 33.6 MB WAL on the prepared 33,842-message store, 96.96% of the DB growth free pages, while the coverage-only pass measured 0.753–1.536 s cold (0.686–0.728 s exact) and left the word index at 47,386,624 B; two-rune Indic terms are the disclosed scan; one query parser: `parseQuery` folds once, treats ASCII `"` spans as one phrase and bare words as ANDed terms, runs an unclosed quote to the end, turns control characters into separators and returns the typed `ErrEmptySearch` when nothing is left; `planQuery` term modes; the scan of short terms on folded text of the same fields: SQL function `mailsense_fold`; capped counts, id sets as one JSON parameter (`idSet`), incremental scope (`EachStaleScope`: bounded `m.id` keyset pages closed before its callback, so the syncer writes a page while no read cursor holds the connection; `WriteVerdicts`), `SyncProgress` (the same missing OR revision OR identity-digest stale rule as `EachStaleScope`, grouped by stored digest and summed per store), `MailGeneration`, cursors, runs (with alterations by code), meta, list APIs, `ValidateSQL` + `ReadOnly` runner, size/checkpoint, topics `store.migrated`, `store.run`. |
| `internal/source` | `Connector` (Discover, `Mailboxes`, Sync), `Mailbox` (incl. `IdentityComplete`: only a complete read replaces a recorded identity), `Sink`, `Capabilities` (incl. owner identity, addressed-to-me, folder roles, message class, headers, multi-folder, attachments, and the owner's actions: read state, flags, categories, last verb — layers ask instead of assuming Outlook, by name with `Has`/`CapabilityNames`; the store keeps them per source), `Registry` (`source.registry` events), `ErrUnavailable` / `Unavailable` (a source that cannot be read as a whole right now — its application gone, crashed or recovering: connectors wrap it, the syncer ends the run on it), `StoreKey` (the one per-store key: `store-<12 hex>` of source and store id), the explicit selection (`SettingSelected`, `SettingDeclared`, `Selected`, `Declared` (as given), `Pick` and `Declare` (keys must be discovered: `ErrUnknownMailbox`), `Unpick`, `Undeclare`, `Scope`). |
| `internal/source/archive` | Legacy JSONL importer (strict `from_record` rules, gzip by magic bytes, fingerprint cursor; one mailbox that names no owner); `EachLine` (the one plain/gzip JSONL line reader, also used by the gold export); `ImportLegacyAnswers` (the legacy client's own answers as triage decisions under their own decider key, checked like live ones, replaced as a whole; never a provider call). |
| `internal/goldexport` | The gold-judge sample of the whole chosen mailbox (strata: script × legacy category × sender kind × year, rare floor, seeded largest-remainder draw; legacy categories with unreadable lines counted; private output files); `triage.Triager.ExportGold` feeds it. |
| `internal/source/rfc5322` | Plain Internet mail files: mbox (RFC 4155: a separator only after a blank line and shaped like one; offsets recorded in one pass, messages read lazily by section; mboxrd unquoting), maildir (cur/new, Maildir++ subfolders, flags in the stamp), .eml files; ids: mbox = folder path + digest without the client status headers (`formats.json`) + occurrence, .eml = folder path + file name, maildir = its unique name (survives moves); `rfc5322.max_message_mb` skips (counted) a bigger message; layouts recognised by structure; RFC 5322/6532 headers, RFC 2047 words, raw 8-bit headers decoded with the message's charset or `rfc5322.headers.raw_charset` (declared), angle-bracket addresses of an unparsable list (declared), MIME (multipart, base64, quoted-printable; malformed parts kept as far as they decode, depth `rfc5322.mime.max_depth`), every WHATWG charset, HTML to text, attachment metadata, the calendar METHOD, Gmail labels (`X-Gmail-Labels`, RFC 2047-decoded) as further folders with the system labels' roles (`formats.json`; `rfc5322.folder_roles` wins), complete snapshots (removals). Every alteration is declared (`mail.Alteration`). |
| `internal/learning` | The app-wide learning gate: `Stage` (implemented by the module that learns: `syncer.MailboxesStage`, `syncer.SyncStage`, `evidence.Engine.Stage`, `index.Stage`, `MachineStage`, `layer1.Runner.Stage` and `QuestionsStage`; `NotBuilt` for labels and calibration), `Machine` (`Status`: stages, progress, ETA from the measured rate, machine-readable blockers: code, subject, `Actions` with params, diagnostic detail; `Allow`: the `Gate`, open while the selection is a subset of the last learned one, answering from a status cached until `Invalidate`, or always nil while the explicit `SetPreview` test preview is on; `Open`: the real gate answer that ignores preview; `gate`: the private real check), `StatusOf`/`Memo`/`withStatusMemo` (one per-build context memo keyed by stage type and id, or by fact type and name: a stage read through several paths answers once and a named fact is read once, status/value and error are preserved; a plain context falls back to `Status` or the fact's build; nothing survives the build), `TopicStatus` (`learning.status`), `RefreshOn` (immediate), `RefreshOnProgress` (coalesced, one rebuild per interval, with the `again` loop so a final event is not lost behind an in-flight rebuild), actions (`actions.go`: one vocabulary; `ConfirmZone`). `learningtest.Open` for tests of the gated features. |
| `internal/evidence` | Layer 0, the structural evidence engine (`docs/EVIDENCE.md`): the registry of signals (one `sig_*.go` each: name, version, type, needed capabilities, causal, context, summary, settings and pinned data read, unsure values and bound on a cut input), `Input` (cut-aware headers — the cut last line dropped —, own text, language, dates placed from the author's day, amounts; details pending for a partial message) and `Env` (the causal view: history and threads as known at each instant from `threads`, look-alike targets, template counts from the vectors, per-store context digests), runs (`Run`/`Start`, batches on workers with panics turned into the item's error, reuse by effective version and context digest, reasons interned, cancellable, `ErrBusy`, one at a time with `jobs.Single`), the stage `evidence` (fingerprint kept while the store's generation is unchanged), `AfterRuns` (after successful syncs, index runs that stored or confirmed a vector, and changes of declared settings; a failed or 0/0 index run starts none; a trigger during a run is never lost), pruning (`evidence.runs.keep`), queries (`Records`, `EachRecord`, `Coverage`, `SignalList`), features (`FeatureLayout` read once, `features.json` bins + `evidence.features.file`), facts (`FactsText`, `Facts`, `Version`), rules (`rules.json`, `evidence.rules.file`, checked against the triage taxonomy and each signal's type and vocabulary, `RuleSet.Measure` with Wilson bounds, `Judge`/`UseJudge`, `RuleTable`, `ErrNoJudge`); topics `evidence.progress`, `evidence.finished`; `docgen_test.go` keeps the reference of `docs/EVIDENCE.md` generated (`EVIDENCE_DOC_WRITE=1`). |
| `internal/layer1` | Layer 1, the local per-install classifier (`docs/LAYER1.md`): heads (`head_*.go`: multinomial, logistic, cumulative logit), the sharded deterministic objective with folded standardisation (`fit.go`), targets and teacher sources as data (`teachers.json` + `layer1.teachers.file`, `labelset.go`), requirements as data (`requirements.json` + `layer1.requirements.file`), grouped folds, the selection/certification split and the holdout (`holdout.go`), metrics (selective accuracy, AURC, ECE), calibration and kNN pooling (`combine.go`, `knn.go`), compact streamed teacher answers (`decisions.go`), the pass (`Plan`, `Train` over an `Inputs`: `StoreInputs` in the app; `Plan.Step` reports the design, the targets, each question's λ path, folds, measurement and certification), the runner (job `layer1`, `ErrBusy`; `settle.go`: promotion, retirement, re-scored thresholds, the staged answers and the run's report in one short `store.CommitLayer1` transaction; `Rescore` on evaluation-label changes; `AfterRuns`), verdicts from the model that answers (`verdict.go`), the learning stages `layer1` and `questions` (plain-language blockers with `learning.Measure`; their "after" stages are read through `learning.StatusOf`, and their layer-1 facts (plan, last run, last succeeded run, fingerprint, labels, route certifications) through `learning.Memo`, so one status build asks each stage once and reads each fact once; while a run is active the stage answers from the runner's live progress, before the stored report); topics `layer1.progress`, `layer1.finished` (no label digests). |
| `internal/selective` | The one selective/abstain module: the confidence of an answer per kind (`Decide`, `Confidence`), `Rank`, `Targets` (coverage at precision targets: diagnostics), `Certify` (threshold chosen on one part, tested once on a disjoint part), `InCertifyPart` (stable conversation split), `Needed`, `Answer`/`Abstained` (an abstained answer has no value, choice or confidence), `Probs`. Read by layer 1, `feedback.CalibrateEscalation` and `taxonomy.Profile.Uncertain`. Pure. |
| `internal/optim` | L-BFGS with Armijo backtracking: deterministic, cancellable, non-finite values refused. Pure. |
| `internal/goldset` | Dev-time gold access with the sealed test split: `Seal` (refuses a kept line naming a test id in any field), `Open` (train and dev labels only; runs Seal's planner read-only and refuses any file still naming a test message), `OpenTest` (once, `GOLD_UNSEAL` = the frozen model's digest, an exclusive lock and an exclusively created ledger). Test support. |
| `internal/locale` | Pinned language data and the pure functions over it (`docs/EVIDENCE.md`): `Language` (n-gram likelihood over UDHR-trained profiles with CLDR exemplar letters, among the languages of the text's scripts; Hiragana and Katakana are one writing system, so either identifies Japanese while the dominant script stays the text's own), `LocaleOf`/`Find`/`Locale.Resolve`/`Days` (CLDR names and date/time patterns of the language and its regions matched on tokens of the folded text — a numeric order the regions read differently is ambiguous —, resolved in code in the language's calendars: exact for the Gregorian, era-offset (Buddhist, ROC) and Persian arithmetic within its agreed years, else a disclosed reason; civil day numbers), `Amounts` (ISO 4217 codes and CLDR symbols, the separators of every locale from the data, space groups of three digits only where the language groups so, negatives, a code next to a plain integer of unknown value), `Skeleton` (UTS #39); `ngram` (the one n-gram rule of the generator and the detector); `pin.json`, `params.json`. |
| `internal/installprofile` | The learners of the install profile (config's learned layer), after every sync: shared/free-mail domains (many correspondents of a registrable domain who never share a thread) and reply prefixes (the leading word of replies the shipped list lacks), each with sample size and provenance (basis as counts); owners of the picked stores only (`store.MailboxViews`). |
| `internal/psl` | The Public Suffix List as shipped data (`public_suffix_list.dat`, pinned in `pin.json`, `scripts/update-psl.sh`), `Registrable(host)` (PSL algorithm with wildcards and exceptions; IP literals, bare or bracketed, are their own key; Unicode and Punycode spellings compare equal: RFC 3492 encoder), `ASCII(host)` (NFKC, ideographic full stops as dots), `Unusable` (rules that could not be read: a test requires none), `Unicode(host)` (RFC 3492 decoding: the form look-alikes are compared in). |
| `internal/source/outlook` | Classic Outlook over COM (`docs/OUTLOOK_SYNC.md`): one STA thread, attach only (`GetActiveObject`; `StartOutlook` is the user's action, through the shell), read-only guard over an explicit member list (`model.go`), the pace at that one call choke point (`pace.go`: busy share and call rate, slower while the user is active — `GetLastInputInfo`; `outlook.pace.*`), the connection breaker in `Connector.op` (`breaker.go`: cooldown growing per loss, Outlook's minimum uptime, reduced pace, the stop after repeated losses, `Resume`; `outlook.breaker.*`, `outlook.breaker` events), busy-call retries, DISPID cache per class, SAFEARRAY reading/building; Discover (stores, mail folders, roles, owners, change state), Sync (snapshot diff + one sorted table pass → partial messages), ReadDetails (body, headers, recipients, SMTP chain with DN cache, and the table-capped fields in full), FolderStatus (vanished-folder probe), Health, store list (`Stores`, `Recount`); `Stats` and `outlook.phase`/`outlook.stores` events. Pure Go except `*_windows.go`; unit-tested against a fake object model (`fake_test.go`). |
| `internal/jobs` | Cancellable background jobs with progress/status/errors, `TopicJob`, bounded history; `Progress` carries `done`/`total`, `stage` (a catalog key), `detail` (the current item), `step_done`/`step_total` and an `eta` the manager measures from the job's own rate (`etaSince`) so no producer estimates one; `Single` (one run of a kind at a time; a run wanted meanwhile follows it, requests coalesce: the evidence and index triggers); `Safe` (a worker's panic as that item's `*PanicError`). |
| `internal/engine` | Engine kinds (decider, embedder, generator), the typed `Question`/`Answer` contract (`Question.Validate` is the one question check), `Decider` (`Plan` errors when a request cannot be sent), `Usage`/`Estimate`, `TopicUsage` + `UsageSink`, provider records as data (the settings-owned `engine.providers.file` is the complete, strict definition; no built-in records), protocol descriptors (kind, Bearer auth, required fields, probe), `Catalog` (load, strict validation, atomic save), `Provider.Fingerprint`/`Key` for paid caches, `Registry` (protocol → adapter), `Monitor` (provider health: `engine.provider` events, `Status` incl. key present), `Registry.TestProvider` (protocol-aware probe), `ErrUnavailable`/`ErrNotConfigured`. |
| `internal/vector` | The one vector math: finite/norm checks, Matryoshka truncation + re-normalisation, cosine, the int8 storage form (`Quantize`, `DotInt8`, `NormInt8`, `CosineInt8`), and `Set`: the in-memory int8 copy of a vector space (incremental refresh from a `Source` — the store —, masked/filtered parallel top-k scan) that search and layer 1's kNN share. |
| `internal/engine/embed` | The embedder adapter (`openai_embeddings`): task templates, input budget with the byte cap, batching, httpx retries, strict vector validation, truncation, usage; `Source` (the configured record, rebuilt only when settings or the providers file change; provider health); the vector space names protocol, model, dimensions, template digest, record fingerprint and runtime revision (never the record id or label); `Probe` (the sidecar's canary/tuning prober); `embedtest` (the lexical fake server). |
| `internal/sidecar` | Managed local runtimes (`docs/EMBEDDINGS.md`): pins (`runtimes.json` + `sidecar.runtimes.file`, validated names; Windows has one Vulkan build, Linux dev/tests have cpu), the pinned `local_embedder` selection (validated: runtime and model exist, the model has a complete embedding spec and `chars_per_token > 0`; `LocalEmbedder(platform)` resolves its default build without starting it), resumable hashed downloads (one per file at a time), the `requires` preflight (Windows Vulkan: `vkCreateInstance` + `vkEnumeratePhysicalDevices`, 0 bytes before a missing device is reported; unknown requirements rejected), installs through an `os.Root` with a manifest, verification before every launch (stamp-cached, unlisted files refused), job object / parent-death signal, loopback port owned by the child (a port another program takes between the probe and the child's bind is retried with a fresh one, up to `launchAttempts`, instead of marking the build broken), one random key per manager, env allowlist, health supervision with backoff and immediate checks on reported failures, `auto` (pinned canary; speed-up against cpu only where a cpu build exists; persisted with every probe), broken-build marks, crash-loop handling (cpu only where it exists, else the re-probe error), thread tuning for cpu builds (`state.json`); the `engine.Locator` (`Locate`, `Failed`, `Model`), actions `Install`, `Builds`, `Verify`, `Remove`, `Reprobe`, `Learned`, `Status`, `Stop`; topic `sidecar.state`. |
| `internal/index` | The semantic index job: eligible mail of the picked stores and `index.folder_roles` (`Options.Stores`), the learning stage `Stage` (blocked without an embedder or with quarantined mail; while a run is active it answers from one live run state — done/total selected work, eligible denominator, embedded/unchanged/failed — instead of a coverage plan, and after success/failure/cancel it reads the full coverage), newest first, cleaned input, revision + input-hash incremental, int8 vectors per space, per-message retry of a failed batch, its vector writes use the store's one SQLITE_BUSY retry (`index.store_busy_*`) and map `ErrStoreBusy` to `stopped_by=store_busy` without quarantine, quarantine after `index.quarantine_after` failures, coverage in all and per store/role, `Eligible`, `AfterSync` (a run after every successful sync), `Ensure` (startup/reconcile: one background run when actionable mail is pending/stale, no duplicate while busy, explicit error without an embedder; a process-local wait after an unavailable no-vector stop — `index.retry_unavailable`, 15m default, reset by settings, provider health, runtime readiness, mail/space/text/scope changes — returns the stopped run's starting coverage without planning), `AfterChanges` (the reset subscription), `index.progress`/`index.finished`, persisted runs with retention (`index.runs.keep`: the newest N, pruned after each finished run, the latest run always kept, no `VACUUM`), no re-plan for an unavailable no-vector run; one run at a time (`ErrBusy`). |
| `internal/search` | Hybrid retrieval: FTS5 BM25 + in-memory int8 scan over the current space, one shared filter, triage filters, RRF, body snippets prepared once per hydrated page (word mode anchors whole FTS token sequences — unicode61 L*/N*/Co tokens, marks are separators — adjacent tokens for a punctuation-joined or quoted term; substring/scan mode anchors substrings and drops a lone one-rune scan term when a longer term exists (a matra-based Indic term keeps its dependent vowel sign: कि anchors कि, never the first की); a full-text hit with no body match has an empty snippet, a semantic-only hit keeps the cleaned-body preview; plain text under `search.snippet_chars`), coverage disclosure, term-mode disclosure (word, substring, scan) and capped match counts, vectors streamed into one copy and filtered by a bitset, the learning gate (`Deps.Gate`, required: `New` refuses a searcher without it; no answer before learning is done), the zero filter's ids cached per `MailGeneration` (selection included, label clicks not), full text first, semantic merged within the `search.embed_timeout` budget (150 ms default) or explicitly degraded, `Warm` (one fixed query embedding and vector refresh within 60 s after startup, no gate, safe beside searches); `SearchFilter.OwnerOnly` is the Q&A opt-in (both rankings use the same filter, `/api/search` keeps the default); topic `search.semantic`. |
| `internal/priority` | Pure deterministic important-mail ranking from `route.Answer` values and the optional preview, with the one `triage.ApplyPreview` substitution rule; the reply pass and the scoring pass read no body (route/preview answers and metadata only), then one bounded `PriorityHeads` query reads the first 2,000 characters, truncation flag and revision of the cached top 200 for their 180-rune snippets, with a rare per-item `MessageBody` fallback for a whitespace-heavy head; a bounded top-200 heap, per-filter totals, one cold single-flight and a cache per preview flag that keeps an unchanged snapshot for up to 5 minutes (a mail/route generation, route-key or preview change rebuilds immediately); the gated owner-only `/api/priority` page. `Close` cancels and waits up to 10 s for cold/refresh builds (a non-cooperative read is logged). No model call, no I/O in `Score`. |
| `internal/qa` | Gated Q&A: accent/case-folded EN+FR intent parser (structured intents read priority filters, `sender` uses owner-only search), free-form owner-only retrieval, one grounded generator call over numbered messages (`qa.provider`, `qa.max_messages`, `qa.max_answer_tokens`, `qa.call_timeout`, `qa.budget.*`), strict citation validation, explicit `retrieval` fallback with cause-specific notices (`qa_unavailable`, `qa_index_warming`, `qa_generation_failed`, `qa_no_grounded_answer`, `no_matches`), and the capped local history (migration 0016); `NewSource` is the provider factory. No content in logs. |
| `internal/livetest` | Test support of the live tests on a Windows machine: `Context` (ends before the deadline or on the stop file), `Pick` (the explicit pick of `-live.pick`) and `Limit` (`-live.details`, `-live.rate`: a short verification run through the user's settings `sync.details.max_per_run` and `outlook.pace.*calls_per_second`). |
| `internal/fixturetest` | Test support: imports the legacy export (or a sample) into a fresh store for fixture/benchmark tests; `fixtureconf`: the fixture's source configuration (its mailbox picked, its owner and time zone declared from `FIXTURE_OWNER`/`FIXTURE_ZONE`); `VectorCache` (real embedder vectors by space and exact input, append-only files in a private directory: a slow local model is paid once across runs and stores). |
| `internal/engine/decision` | The one decision-model adapter: codecs `systemone` (Ondelle, Zelvane, Aubelle; Cloudflare `result` envelope) and `openai_decisions` (arrays, predicate, refusals); chunking at `max_questions` (earlier chunks' answers kept when a later one fails), context budget before sending, strict and self-consistent decoding (complete probabilities summing to 1, choice = argmax, score = expected level), error classes (402/401/403/quota/422…), per-provider retry overrides through httpx, usage per call (event + counters; possibly billed failures at the estimate; a provider-reported charge, `cost.request_cost_usd`, is accounted as billed; `truncated: true` is a contract error). |
| `internal/threads` | Header-first threading over every owner folder of `CausalRoles` (every folder but drafts: the causal view of evidence and triage) or of `threads.folder_roles` (the install-profile learners) (direction from scope: sent by the owner), conversations grown in time order (`EarlierMembers`: the members known at a message's instant; `KnownReplies`/`KnownHistory`: replies paired at their own instant; `Exchange`: both-way standings), reply matching in both directions (sent items use `sent_on`; meeting responses, reports and automatic replies never reply or wait), senders grouped by organisation (`mail.Domains`), the behaviour profile per sender/domain in the owner's zone (`time.zone`), response buckets (`threads.response_buckets`), reply examples, `KnownHistory` (causal standings at any instant: only mail and replies before it, replies as paired at their own instant). Pure except `Load`. |
| `internal/feedback` | Labels (store table; `Labeler` Put/Delete publish `feedback.label`), causal few-shot selection and suspicious prior, evaluation (weak reply labels on the model's value, within `feedback.weak_reply.within`: accuracy/F1 at the cutoff, AUC, Brier), threshold and escalation calibration, correlations, insights and volume patterns as data. |
| `internal/triage/taxonomy` | The triage profile: embedded legacy taxonomy (`default_profile.json`, generated; the committed file is the reference once Python is gone) plus Mailsense's additions (`mailsense_profile.json`: escalation rules, insight vocabulary, `noul_default`, `sentiment_positive_min`), overrides (`triage.profile.file`, per key, null removes), all-errors validation (one role per question), the legacy fingerprint, typed roles and thresholds (`RoleCutoffs`, `Cutoff`, `Uncertain`), label parsing, `Value`. |
| `internal/triage` | Causal state composer (template, UTC facts, standings, reply and label examples from before the message; `rules.json` + `triage.rules.file` hold bands and phrases), cache of raw (possibly partial) answers, causal priors blended at read time, `Engines` (decider built per run, provider status and test), `Plan`/`Run`/`Start` (one run at a time: `ErrTriageBusy`, store lease; budget guard incl. unpriced providers; workers; failures; a paid decision or failure write retries the store's busy policy, and a decision that still stays busy is recorded as a failure so the message stays pending, never marked decided; invalidation counts), `Read`/`Result` (per message), `ExportGold`, `Analytics` (evaluation, calibration, correlations, insights), topics `triage.progress`, `triage.finished`. |
| `internal/syncer` | Connector → store runs: `Discover` (the mailbox list without reading mail; `sync.mailboxes` carries the `store.MailboxView`s), the picking actions `Pick`/`Unpick`/`Declare`/`Undeclare`/`Mailboxes` (validated against the discovered list, published), `StartPicked` (after a pick, one sync job per source with a picked mailbox; a busy source is skipped, the selection is never changed), only picked mailboxes' folders (`ErrNothingPicked`; picked but unreadable: `ErrMailboxUnreadable`, `Report.Waiting`), each picked mailbox's reported identity recorded from a complete read only (`Report.IdentityKept` otherwise), alterations counted per code (`RunTotals.Altered`), incremental scope (only new/changed mail or a changed identity; a store without identity blocked), the learning stages `MailboxesStage` and `SyncStage`; one run per source at a time (`ErrSourceBusy`), batching (message writes retry SQLITE_BUSY through the store's one policy, `index.store_busy_*`), validation, skip-ratio rule, reconciliation, per-folder cursors staged during the folder pass and committed in one transaction only after the end-of-run removal pass succeeds (a failed or incomplete folder snapshot leaves its whole store's removals and cursors untouched for the next run), snapshot diffs and end-of-run removals (moves vs deletes), the removal safety valve (`sync.removal.*`, held removals: `TopicHeld`, `ConfirmRemovals`), vanished/inactive folders, the detail pass for partial messages (newest first, `sync.details.*`: batch size, a cap per run, messages received moments ago deferred; never paced here — pacing is the connector's), per-store scope recompute (visible folders only), `RunFolders` (a selection), `TopicProgress`/`TopicFinished`. A failing folder is reported and the others still sync; output failures, an unavailable source (`source.ErrUnavailable`) and cancellation abort a run. |
| `internal/onboarding` | The first-run seed (`EnsureLocalEmbedder`): when `embed.provider` is at its default, writes the pinned sidecar `local_embedder` as one ordinary provider record (`local-embedder`) in `engine.providers.file` and selects it. Never overwrites an explicit value (including explicit empty), an existing equivalent record or an id collision; filesystem only (no runtime start, download or paid call); the provider file is written before the role so a partial failure retries next start. |
| `internal/oslocale` | The one OS interface-language probe, cached once per process: Windows `GetUserDefaultUILanguage`/`LCIDToLocaleName` with `GetUserDefaultLocaleName` fallback; Unix `LC_ALL` > `LC_MESSAGES` > `LANG` > `LANGUAGE`; `Parse` normalizes POSIX/BCP-47 names and maps `C`/`POSIX` to empty. Exposed as `system_language`/`system_locale` on `/api/status`. |
| `internal/ui` | The embedded desktop shell: `index.html`, the 24 ordered modules under `js/`, the 10 stylesheets under `css/`, `mail.css`, `favicon.svg`, the bilingual `i18n.en.js`/`i18n.fr.js` catalogs and the `i18n.js` runtime. `app.js` and `app.css` are generated from the two manifests, never edited; `bundle.go`/`gen` own the generation and `structure_test.go` fails on a stale bundle, an unlisted module, an oversized module/function, a duplicate body or a new monolith. Unhashed assets are served with `no-cache` + ETag and the document with `no-store` (there is no content-hashed asset set). Owns `ui.language` client behavior, `Intl` formatting and the taxonomy's English-only presentation; the boot strip stays visible at `ready` with `failed > 0`, names the failed views and offers Dismiss. |
| `internal/bootstrap` | Startup error report (file + native message box). |
| `internal/buildinfo` | Link-time version stamp. |
| `cmd/mailsense` | Wiring only: the first-run local embedder seed before the engines; the listener and its descriptor before the derived warm; the desktop window immediately after the warm starts; one `bootState` (the twelve ordered warm views) behind `GET /api/boot`; the derived warm (`warm.go`) as one cancellable background task that logs `mailsense ready` when it finishes: the priority ranking as its own goroutine, the independent early screens (Mail first page, learning status, triage summary and page, hub) beside it, the four dependent views and then the Brief after the ranking joins, and the semantic prepare last. Every stage asks `warmPolicy` first: the resource guard may refuse it, and a refused stage is recorded `skipped` with its ceiling (never ready, never failed) so the route builds it on first use; store-scanning stages are bounded by `warmHeavyBuilds` (3). First-open discovery (only when no mailbox has ever been listed and no selection exists) and one semantic maintenance worker (Ensure once, one query warm-up, then a 2-minute reconcile) wait for ready or shutdown. `startFirstOpenDiscovery` returns a stop that cancels and waits without a timeout, and `main` defers it after `st.Close` so LIFO joins the discovery before the store closes; production connectors honor the context, and a non-cooperative connector is a residual. The semantic worker is joined too. Priority `Close` cancels and waits up to 10 s for its builds (production store reads honor the context; a non-cooperative build is logged and remains the one bounded residual). Shutdown cancels the warm and joins it for up to 15 s (a cooperative view honors the context; a non-cooperative view is logged after that bound) before the API, priority builds, jobs and runtime stop. `readModelCloserList` (beside the adapters in `readmodels.go`) is the one list of the detached read models joined before `store.Close`: hub, companies, conversations, commitments, brief, triage, messages, agenda and the learning machine. Every read-model adapter's `Close` is nil-safe, so a partially wired app still shuts down. |

## UI source modules and generated bundles

`internal/ui/js/` holds the ordered source modules and `internal/ui/css/` the
ordered stylesheets; `js/manifest.txt` and `css/manifest.txt` own the order.
The served `app.js` and `app.css` are generated output:

```
go generate ./internal/ui
```

Never edit `app.js` or `app.css`. `internal/ui/structure_test.go` runs in
every gate and fails when a bundle is stale or hand-edited, an embedded module
is missing from its manifest, a JS module exceeds 1,300 lines (600 for CSS),
a function exceeds its reviewed budget, two functions share a body, or a new
source file above 1,500 lines has no reviewed budget. Move code into the
owning module, add it to the manifest, then run the generator.

Shell keyboard shortcuts have one owner, `handleGlobalShortcut` in
`js/33-search.js`. The top document and every sandboxed message frame attach
it, because a key event inside a frame never reaches the parent document.
Do not add a second keydown shortcut handler for `/` or Ctrl/Cmd+K.

## Configuration: adding a setting

1. Add an entry to `internal/config/schema.json`:
   `key` (dotted lower_snake, e.g. `sync.batch_size`), `type` (`bool`, `int`,
   `float`, `string`, `duration`, `enum`, `list`), `default`, `description`,
   `group`, optionally `min`/`max` (numbers and durations, given as `"5s"`;
   for `list` of `int` they bound each item), `enum`, `item_type` (`string`
   or `int`, lists only), `pattern` (regex for strings and string items),
   `format` (`regexp`, `http_url`, `time_zone`, `pair:<key>` = `name=value` with value valid
   for `<key>`, `one_of:<vocabulary>`, `keyed:<vocabulary>` = `<name>=<value of the vocabulary>`;
   a value list several settings share is a named `vocabularies` entry of the schema, written once), `secret` (shown as `config.SecretMask` in the UI and in
   errors, redacted in logs; applies to list items too), `restart_required`.
   A relation between two settings goes in `constraints`
   (`{"key": a, "op": "<" | "<=", "other": b}`, int/float/duration of one type).
   Constraints are checked on the effective values after layering.
2. Read it where it is used: `cfg.Int("sync.batch_size")` (or on a
   `Snapshot` for several reads that must agree). Never copy a default into Go.
3. `go test ./internal/config` validates the schema (types, defaults within
   bounds, env-name collisions, formats, constraints). It also fails when a
   setting is not read by any production Go code or by the embedded shell
   (`internal/ui/js/` and `internal/ui/index.html`, for client-consumed settings such as
   `ui.language`): no inert settings. An accessor with an unknown key or
   wrong type panics in the first test that runs it.

A setting marked `"learnable": true` (never secret or restart-required)
can also take a value learned on this install: `Config.Learn(key, value,
sample, provenance)` writes it to `install-profile.json`; it applies above
the default and below `settings.json`/env, and `Config.Learned()` lists
every entry with its sample size and provenance (an entry for a setting
that is unknown or no longer learnable is listed with the reason, never
applied). `Config.Reject(key, value)` is the user's action against a
learned value (an item of a list setting): applied at once, stored with
the entry and kept by every later `Learn`; `Unreject` withdraws it.

Values: JSON in `settings.json` (flat object of dotted keys; `null` = default;
durations as strings); env `MAILSENSE_<KEY>` with dots → underscores,
upper-cased (`MAILSENSE_LOG_FILE_MAX_SIZE_MB=20`; booleans `true`/`false`;
lists as JSON arrays). `MAILSENSE_DATA_DIR` and `MAILSENSE_SECRET_*` are
reserved; any other unknown `MAILSENSE_*` variable is an error.

### Learning test preview

`learning.test_mode` is the explicit, reversible uncertified preview used
to exercise the automatic answer screens while the learning gate is
closed. It is ordinary live configuration, set through `PUT
/api/settings/learning.test_mode` (the Hub action uses the same path), and
it is never stored as a learned value or a certification source.
`cmd/mailsense.wireLearning` installs it with `Machine.SetPreview(func()
bool { return cfg.Bool("learning.test_mode") })`.

- `Machine.Allow` returns nil while the preview is on; every gated
  handler and service keeps using that one method and has no bypass.
- `Machine.Open` is the real gate answer and ignores the preview.
  `cmd/mailsense.learningAPI.Status` uses `Open`, never `Allow`, so
  `/api/learning` (and `/api/status` and the hub gate) keep `open: false`;
  `LearningView.preview` mirrors the setting.
- `Status`, the latch, the stages, the label requirements and
  certification never change. Accepting judge suggestions as manual
  truth, lowering a target or reporting the preview as open/certified is
  not a valid test.
- The automatic answer screens show the layer-1 model's threshold-free
  best guess (`selective.Given`) where the route abstained because it is
  uncertified. The layer-1 commit writes those answers to the separate
  `route_preview_answers` table (`Layer1Commit.Preview`, migration 0015)
  in the same transaction as the route release; a regular run and a
  re-score both refresh it. `Machine.Previewing` is the display gate, and
  the triage read substitutes only an answer whose route reason is
  `not_certified`; a certified, below-target, no-answer or refused answer
  is never replaced, a missing preview row keeps the abstention, and a
  malformed preview row fails the read. The route, the gate,
  certification, the judge and the report never read the preview table.
- Use only synthetic data in tests. The focused checks are
  `internal/learning` (Allow vs Open and reversibility),
  `internal/store` and `internal/layer1` (preview write/read, replacement,
  pruning and rollback), `internal/triage` (substitution rules and
  unchanged coverage/order), `internal/api` (preview flag, open false,
  gated read), `internal/config` (live round-trip), `internal/ui` (banner,
  separate pill, Hub action) and `cmd/mailsense` (configuration wiring).

## Q&A provider, privacy and tests

`qa.enabled` controls the Ask endpoints; `qa.provider` names a provider
record of protocol kind `generator` (empty = not configured; its key comes
from the record's `api_key_secret` through the ordinary secrets store). `qa.max_messages`, `qa.max_answer_tokens`,
`qa.call_timeout`, `qa.answer_language.min_letters` and `qa.budget.*` bound one
free-form question. `qa.answer_language.min_letters` (default 30) is the
fewest letters a grounded written answer must contain before its language is
compared with the requested `en`/`fr`; quoted reply lines are stripped first.
The one check lives in `internal/qa/language.go` (`finalizeGeneratedAnswer`)
and runs at the planner JSON answer, the grounded prose fallback and the
entity summary. The check is notice-only: the answer, citations, counts,
mode and provider/model stay as written, nothing retries or falls back to
retrieval, and the answer is saved to history but never cached, so a re-ask
calls the provider again. A view that already carries a notice keeps it.

The provider-less count fallback is split across the existing owners:
`aggregateStructuredIntent` (`internal/qa/intent.go`) recognizes only a
plain, single-category count that uses the declared count cues (`count`/
`counts`/`nombre`/`total`/`combien`/`amount`/`quantity`/`quantité`, the
phrases `how many`/`how much`/`number of`/`nombre de`/`amount of`/
`quantity of`/`quantité de`, and bare `number` immediately before a
category or mailbox noun). After removing the count cue and the one
matched category word, every residual token must be in a small explicit
filler/question/message allowlist; any unlisted scope, topic, status, date,
person or organization word keeps the question free-form, as do list
requests, content or action questions and multiple categories. The allowlist
has no third-person pronouns or `an`, and a `does`/`did` subject is
rejected unless it is first/second person or a mailbox noun; a category
noun in the subject position is a person frame, so "How many deadlines does
Deadline have?" and "How many urgent messages does Urgent have?" stay
free-form while "do I/we/you have" remain plain counts. A repeated cue or
category noun before `has`/`have` is also a person frame ("Amount of
deadlines Amount has?"); "deadlines have I got" keeps its first-person
plain path. A bare repeated category noun without an auxiliary ("How many
deadlines Deadline?") is kept free-form too. The gate runs in `qa.Ask` before
the intent switch for every request with a recognized count cue, including
"Number of…", "Total…", "Amount of…", "Quantity of…", "Quantité…" and
French "Combien…", not only "how many" wording,
so a recognized count cannot parse directly to a category and bypass it.
The fail-closed scope and person checks apply to every recognized cue. The
fallback is conservative and does not claim complete name or pronoun
coverage.
`Service.generatorAvailable` and `Service.providerlessCountScopeAllowed`
(`internal/qa/ask.go`) keep the computed count when a generator exists and
keep organization/person scopes on the free-form path; a failed
company-book or person-candidate read also keeps the question free-form.
The strict person read resolves the plain-count wording through
`aggregateCountEntityMentions`, which removes the count-cue terms before the
bounded candidate query: a cue word is not a name, so "Amount of deadlines"
is not captured by a sender named "Mont Amount" by substring and "Count of
deadlines" is not captured by "Accounting". A category noun in the
person-subject position is added back as a term, so a contact actually named
Deadline or Urgent still scopes the freeform follow-up; the same noun in the
object position stays out of the person query. The freeform resolver uses the
same filtered mentions for a recognized count, so "Amount of deadlines does
Bob have?" considers Bob and not Mont Amount. Explicit emails/domains remain
terms, so a real address still scopes the count.
`askStructured` and `priorityCounts` produce the view from the existing
structured priority counts. The fallback covers only security, decision,
deadline, urgent, reply and workload: there is no priority filter, so a
"priority" count is not one of its categories. A count request that would
otherwise parse directly as `priority` (for example "Combien de messages
importants ?") is also kept free-form instead of answering the unfiltered
total. It is not notice-only, and a matched plain count does not fall back to
retrieval.
`TestProviderlessAggregateCountUsesThePriorityAnswer`,
`TestProviderlessAggregateCountZeroIsReal`,
`TestProviderlessAggregateCountControls`,
`TestProviderlessAggregateCountRejectsUnlistedScopes`,
`TestProviderlessAggregateCountDoesNotAnswerPriority`,
`TestProviderlessAggregateCountRejectsPersonFrames`,
`TestProviderlessAggregateCountPreSwitchGate`,
`TestProviderlessAggregateCountLeavesScopedNamesToTheResolver`,
`TestProviderlessCountScopeDefersOnResolverErrors`,
`TestProviderlessAggregateCountAvailabilityAndFailures`,
`TestProviderlessAggregateCountRejectsCountSynonymScopes`,
`TestProviderlessAggregateCountAcceptsPlainCountSynonyms`,
`TestProviderlessAggregateCountRejectsCategoryNamedPerson`,
`TestProviderlessAggregateCountRejectsBareCategoryNamedPerson`,
`TestProviderlessAggregateCountRejectsRepeatedCueName`,
`TestProviderlessAggregateCountKeepsPlainAmountWithMontAmountSender`,
`TestProviderlessAggregateCountScopesFreeformToBobNotMontAmount`,
`TestAggregateRepeatedCategoryWordNormalizesPlurals` and
`TestAggregateCountSynonymsUseTheCanonicalCueOwner` pin the fallback,
its real-zero answer, its negative controls, the fail-closed scope and
person-frame gates, the removed priority category, the scoped-name and
resolver-error residuals (including the strict `peopleErr`/`listErr` gate
logs), and the ranking/save/availability errors;
`e2e/specs/ask.spec.ts` checks the structured sentence and citation render.
No setting, schema or API field was added. The
factory (`internal/qa/source.go`) builds the catalog and the generator
registry, sets the output cap on a copy of the provider instance (the answer
budget plus the reasoning effort's allowance within the record's declared
output reserve) and returns `qa.ErrUnavailable` for a missing key or an
unusable provider; the service turns that into an explicit `retrieval`
answer with `qa_unavailable`. Conversation summaries call the generator with
`engine.GenerateOptions{Prose: true}`, so a provider record configured for
the planner's strict JSON mode does not force a summary into JSON. No new
provider key, dependency or local generator was added.

The service builds one owner-only evidence bundle per question: direction,
sender, date, recipients, unread/importance, attachments, the resolved
thread state, learned labels (manual shadows judge) and priority signals,
plus person evidence for a named person and profile facts for a named
organization. Plans and generated answers are cached under the planner
provider's identity and `plannerVersion`, so changing the provider or the
planner contract never reuses an earlier decision. A zero-row plan falls
through to fresh retrieval; a semantic search that did not run is retried
once and then reported as `qa_index_warming`. A query that embedded
successfully but scanned an empty/partial vector set (`Used`, `Eligible > 0`,
`Covered == 0`) is also warming rather than a no-match; only a genuinely
empty mailbox (`Eligible == 0`) is `no_matches`. Neither a no-match nor a
degraded answer is cached. See `docs/API.md` for the route modes.

The privacy boundary is part of the service contract:

- Priority and every Q&A retrieval are owner-only (`SearchFilter.OwnerOnly`
  for search, the scope join for the priority stream). `/api/search` keeps
  its shared-inclusive default.
- A free-form prompt contains the numbered owner messages (subject,
  sender, date, a 600-character plain snippet and priority signals) and is
  sent to the configured external provider. Structured answers make no
  provider call, and a retrieval fallback makes no further call; a missing
  provider returns the retrieved sources with `qa_unavailable`, while a
  failed or unusable call returns `qa_generation_failed` or
  `qa_no_grounded_answer` — never a fabricated answer.
- Questions, answers, subjects, snippets, addresses and citations never go
  to a log. The Q&A service logs only a generic fallback event with the
  notice code, the provider/model when known, and an error class, plus the
  `answer_language` event carrying the requested and detected language tags,
  the provider and model, and never the answer text. History is local; it stores only
  citation message ids and reasons and rehydrates visible owner mail on
  read.

Tests inject a fake `qa.Generator`; no test builds the real provider or
makes a network call. The focused checks are `internal/priority` (weights,
filters, rare signals, stable tie-break, preview substitution, cache),
`internal/qa` (intent parsing, modes, retrieval fallback, citation
validation, history rehydration, no content logs), `internal/store`
(owner-only stream, strict answers, history prune), `internal/search`
(owner-only on both rankings), `internal/api` (contracts, gating,
validation) and `internal/config`/`internal/ui` (settings and French
catalog).

## Interface language (i18n)

The desktop shell ships English and French UI wording; the model-facing
taxonomy stays English. The one preference is `ui.language` in
`internal/config/schema.json` (`auto` by default, or `en`/`fr`; environment
`MAILSENSE_UI_LANGUAGE`). It applies live through the ordinary settings API
and never writes a value on first run.

- `internal/ui/i18n.en.js` and `i18n.fr.js` are flat JSON-object literals
  assigned to `window.MailsenseI18nCatalogs`. Plural entries are
  `{one, other}` objects. `i18n.js` owns `t(key, params, fallback)`,
  plural selection with `Intl.PluralRules`, interpolation and the `Intl`
  wrappers. `index.html` loads the catalogs and runtime before `app.js`;
  `ui.go` embeds all three with the same `no-cache`/`ETag` treatment as
  every other asset.
- `t` resolves selected catalog → English catalog → caller fallback → key.
  Every value is inserted with `textContent` semantics; the runtime never
  returns markup. Numeric params are formatted with the active format
  locale, not concatenated.
- Shared keys exist in both catalogs. The only French-only namespaces are
  `api.*` (server code fallback), `settings.group.*` and
  `settings.<key>.description` (schema fallback). Adding a French key
  elsewhere without the English key fails `go test ./internal/ui`.
- No user-facing prose belongs in the UI modules or `index.html`. The tests scan
  UI sinks for literals and allow only listed technical values (the example
  address, the example IANA zone, `#` and `v` prefixes). Put wording in both
  catalogs instead.

Resolution precedence: explicit setting → detected system language → first
supported `navigator.languages` entry only when detection returned nothing →
English. A known but unsupported system language (`de-DE`) is English plus
the Settings offer; the browser locale never silently selects French. The
offer writes `fr` only when the user accepts it. An explicit choice also
keeps a compatible regional format locale (`fr-CA`); otherwise `en-US` or
`fr-FR` is the formatting fallback.

`internal/oslocale` detects once per process:

| Platform | Source |
| --- | --- |
| Windows | `GetUserDefaultUILanguage` → `LCIDToLocaleName` (display language, catalog), fallback `GetUserDefaultLocaleName` (regional format). Missing procedures and failures are empty, never a panic. |
| Linux/Unix | `LC_ALL` > `LC_MESSAGES` > `LANG` > the `LANGUAGE` colon list. Strip encoding/modifier, keep the first region. `C`, `POSIX`, empty and malformed values are "not detected" and do not fall through a set higher-precedence variable. |
| Elsewhere | The client's `navigator.languages` is the fallback; macOS display locale detection is not implemented beyond that. |

The effective owner zone for dates is `time.zone`; an unconfirmed zone shows
the machine zone with a disclosure on Learning. The shell auto-confirms the
browser's IANA zone on the first run when the machine stage carries
`time_zone_unconfirmed`; the manual dialog remains the fallback. Log
timestamps always use the machine zone. French output follows `Intl`: 24-hour clock, decimal comma,
no-break spaces in grouped numbers and before `%`, base-1000 byte units
(`o`, `ko`, `Mo`, `Go`, `To`).

**Do not translate the taxonomy.** The 17 question names, instructions,
criteria, option names, score levels and other text in
`internal/triage/taxonomy/default_profile.json` and
`mailsense_profile.json` are model input and the profile fingerprint's
source. They are displayed unchanged (as English data) in both languages.
Do not edit those files, `internal/judge/prompt.json`, `triage.rules.json`
or the taxonomy fingerprint for an interface-language change.

Adding a string:

1. Add the key and value to both catalogs (or only the French-only
   namespaces when falling back to schema/server data).
2. Use `t("key", params)` in `app.js`; use `tOr` only when a data fallback
   is intended.
3. Add a `data-i18n`/`data-i18n-aria` attribute for static shell text.
4. Run `go test ./internal/ui`; when Node is installed it also executes
   `testdata/i18n_behavior_test.js`, which checks resolution, plurals,
   numbers, dates and zone fallback against the real runtime.

French glossary (keep one term per concept):

| English | French |
| --- | --- |
| learning, gate | apprentissage, verrou |
| stage, blocker | étape, blocage |
| facts, evidence | faits, preuves |
| labels, review | étiquettes, révision |
| mailbox, pick | boîte aux lettres, sélectionner |
| triage, answer | triage, réponse |
| scope, coverage | périmètre, couverture |
| owner, time zone | propriétaire, fuseau horaire |
| run, job | exécution, tâche |
| provider, local runtime | fournisseur, moteur local |
| settings, data directory | paramètres, répertoire de données |
| restart, secret | redémarrage, secret |

## Logging and events

- `var log = logx.For("component")`, never `fmt.Print`/`log.Print`. Dotted
  components inherit levels (`source.archive` → `source`), configured with
  `log.components`.
- Redaction is automatic for messages and every attribute (non-primitive
  values are stringified first, so they cannot bypass it). Register runtime
  secrets with `logx.RegisterSecret`; `secrets` and secret settings do this.
  The built-in patterns are always compiled and cannot be replaced:
  bearer/basic credentials, `sk-` keys, `key=value` / `"key": "value"` pairs
  whose key mentions token, secret, password, api key or authorization, and
  email addresses (including internationalized letters and dotless internal
  hosts; a lone `@` or a bare word such as `noreply` is not an address).
  `log.redact.patterns` adds custom expressions; its schema default is empty,
  so a user list never drops the built-ins.
- Attribute names are deny-by-default for owner content: one `contentKeys`
  set in `internal/logx/content.go` (body/subject, sender/recipient/address/
  email, snippet/content, question/answer, mailbox/store names, series
  subjects, headers). A match replaces the value with
  `config.SecretMask` whatever its type. The whole key is normalized
  (`.`/`-`/space to `_`, lower case) and checked with its dot segments and
  adjacent pairs, so `display.name`, `mailbox.name`, `store.name`,
  `mail-subject`, a group prefix and case variants cannot bypass it. Do not
  log a content value under a new name: log a stable id, class or count
  instead (a store key, `address_count`, a series digest, an alteration code
  and detail size). Triage taxonomy logs `question_key`, never `question`. `http.log.body_max_bytes=0` keeps
  request/response bodies off; if an operator explicitly enables that bounded
  diagnostic switch, its `request_body`/`response_body` attrs are the one
  disclosed exception and still pass the secret/pattern redactor.
- An expected HTTP 503 from the sidecar health probe is logged at debug
  (`httpx.WithQuietUnavailable`, used only by that client), while every other
  4xx/5xx, every transport error and the supervisor's restart/failure warning
  keep their warn level.
- Topics: `config.changed`, `log.entry`, `log.health`, `log.applied`,
  `jobs.update`, `sync.progress`, `sync.finished`, `store.migrated`,
  `store.run`, `secrets.changed`, `http.retry`, `source.registry`,
  `outlook.phase`, `sync.mailboxes`, `learning.status`, `evidence.progress`, `evidence.finished`, `sync.removals_held`, `engine.usage`,
  `engine.provider`, `feedback.label`, `triage.progress`, `triage.finished`,
  `index.progress`, `index.finished`, `sidecar.state`, `search.semantic`, `layer1.progress`, `layer1.finished`. Declare a
  topic once in the owning package with `events.NewTopic[T]`. The bus's
  dropped-event counter is pulled (`Bus.Dropped`, included in
  `logx.Health`), not published, because a full bus cannot report on itself.

## Store

- Migrations: `internal/store/migrations/NNNN_name.sql`, applied in order in
  a transaction each, tracked in `PRAGMA user_version`. Once a migration has
  shipped to a user it is never edited; add the next number. (0001 is unreleased.)
- `messages` columns after `folder_id` mirror the json tags of `mail.Message`
  (`store/columns.go`, reflection). To add a message field: add it to
  `mail.Message` with a json tag, add the column to a new migration, and
  `TestColumnsMatchSchema` keeps both in sync.
- `EachMessage` is the one stream read of stored messages: one
  visibility/owner predicate (`MessageFilter.sql`) and one scan. A caller
  projects the `mail.Message` columns it reads with `MessageFilter.Fields`
  (`MessageFieldsOf`): the zero value reads every mapped column plus the
  recipients and attachment/folder subqueries, and a non-zero projection is
  resolved through `messageColumns` (never a second column list). Omitted
  fields stay zero and `itemsSQL` runs only when `attachments` or `folders`
  is asked for. The consumer owns its set next to its reader; threads
  declares `threadFields` beside `FromMail` (sender, subject, conversation,
  body, recipients, class, headers, calendar method, times and message-id
  headers), so its load never reads body HTML, attachment/folder JSON or an
  unrelated column.
- Folder metadata comes from `Connector.Discover` (`UpsertFolder`). The
  message write path only makes sure the folder row exists and never
  overwrites what discovery recorded.
- The FTS word index (`messages_fts`, contentless, `unicode61 remove_diacritics 2`)
  is written only by `writeFTS` (the batch write path and the full open
  reindex).
- Phase 2a tables (migration 0002): `engine_usage` (counters per UTC day,
  provider, model), `triage_decisions` (the cache: raw answers per
  message, fingerprint and decider, with the state hash), `triage_failures`
  (last failure; never removes a decision), `triage_runs` (with the JSON
  report), `labels` (sender and subject come from the message, never
  copied) and `leases` (one triage run at a time across processes).
  `EachMessage` streams messages with recipients and verdicts.
  `Generation` counts the writes derived views depend on (messages,
  folders, scope, labels) so triage reuses its analysis while unchanged.
- Phase 2b (migration 0003): `messages.revision` (incremented by every
  write of a message, the exact change marker), `embeddings` (one int8
  vector per message, its space, input hash, revision checked and write
  sequence — the counter `embedding_seq` in `meta`, never reused),
  `index_failures` (failed attempts per message and revision: the
  quarantine) and `index_runs` (retained to the newest `index.runs.keep`
  rows, pruned by the indexer after each finished run). `SearchFilter` is the one filter of both
  search rankings (`SearchText`, `FilterIDs`); `instantSQL` is
  `mail.Instant` in SQL (kept equal by a test).
- Phase L0 (migrations 0004–0006): `sources.capabilities`; `folders.store_owner`
  dropped and store kinds remapped; `mailboxes` (every discovered store);
  `owner_identities` reported/declared in order; `scope_verdicts.revision`
  and `.identity` (incremental scope); `messages_fts_tri` (trigram index of
  substring-searched scripts — the data-driven set: CLDR spaceless plus the
  kept `substring_extra` of Hangul and the 10 matra-based Indic scripts —
  written by `writeFTS` for message writes and by `reindexTri` for the
  coverage-only pass; `meta` `fts_text_version` (the folded form,
  a change rewrites both indexes) and `fts_coverage_version` (which
  messages have a trigram row, a change scans and rewrites only the
  trigram rows) trigger the Go-side re-index at open; `fts_version`
  remains the downgrade mirror and the fallback when the split keys are
  absent);
  `attachments` and `message_folders`. `meta` `learned_scope` is the
  learning latch (the last learned selection; a subset of it stays open).
- Review round L0 (migration 0007): `messages.calendar_method` and
  `.alterations` (JSON array of `mail.Alteration`; slice fields of
  `mail.Message` are JSON columns), `sync_runs.altered` (counts by code),
  `folders.store_key` (Go function `mailsense_store_key`) and `.picked`
  (follows the selection: `visibleSQL`), `mailboxes.identity_complete`;
  declared rows of `owner_identities` deleted (declared addresses live in
  the settings only).
- Layer 0 (migration 0008): `evidence_schemas` (one row per ordered list
  of `[signal, effective version]`), `evidence` (one row per message: the
  revision it was computed at, the schema, the values as a JSON array
  aligned with it), `evidence_runs` (with the fingerprint of their
  inputs: `EvidenceInputs`). `EachMessage` now streams attachments and
  further folders too, and `MessageRow.Revision`; `MessageIDs` lists a
  filter's ids without reading the mail.
- Review of the Outlook crash-safety round (migration 0010):
  `source_state` (a connector's own state across app restarts,
  `source.StateKeeper`: the Outlook breaker's losses and stop),
  `detail_quarantine` (messages set aside at their revision after their
  detail read made the source fail; `quarantinedSQL` is the one predicate,
  read by `pendingSQL` and `SyncProgress`), `triage_decisions.message_revision`
  (the revision a paid decision was made at: the policy above; earlier rows
  take the message's current revision, never re-billed by the migration),
  `embeddings.text_version` (the cleaning data's digest a vector's text was
  made with: a new one rechecks every vector, re-embedding only changed
  texts).
- Layer 1 (migration 0011): `layer1_models` (one head per question and
  version: float32 parameters, meta and out-of-fold metrics as JSON,
  state active/candidate/retired, at most one active per question by a
  partial unique index) and `layer1_runs` (inputs fingerprint, report).
  Layer-1 answers are `triage_decisions` of decider `layer1`
  (`ReplaceDecisions`: one transaction); `SaveDecisions` batches many
  decisions (`SaveDecision` is its one-row case); `DecisionSummary` is a
  key's count and last decision time.
- Display HTML (migration 0014): `messages.body_html` (raw, sanitized on
  every API read), `.body_html_known` and `.body_html_truncated`. The
  fields are deliberately `json:"-"` in `mail.Message` and are the one
  explicit exception to the reflected column map: `bodyHTMLColumns` in
  `columns.go` declares them once, the upsert preserves a known body when
  an incoming write does not know HTML, and only `GetMessage` /
  `EachMessage{WithBodyHTML:true}` select them. `ApplyBodyHTML` is the
  backfill's HTML-only update: it never bumps `revision` or `updated_at`
  and never touches the full-text index. Plain `Body` remains the one
  learning, search and model input.
- Q&A history (migration 0016): `qa_history` (`STRICT`) stores question,
  answer, mode, intent, counts and citations as JSON, provider/model,
  language, preview and creation time, newest 100 only (`SaveQA` prunes in
  the same transaction). Citations persist only `{message_id, reason}`;
  history reads rehydrate subject/sender/date from visible owner mail
  (`SummariesOwner`). `SearchFilter.OwnerOnly` adds the scope-owner
  predicate to the same filter used by FTS and the vector scan; the
  priority stream joins `route_answers`, `route_preview_answers` and
  `scope_verdicts` and decodes both answer maps strictly.
- The message write path (`writer.go`) prepares every statement once per
  transaction and writes the full-text entries after the batch's other
  writes: an FTS insert followed by another statement of the transaction
  opens a statement journal whose savepoint makes FTS5 flush one-message
  segments and merge them.
- Agent SQL goes through `ReadOnly.Query`. `ValidateSQL` first: one
  SELECT/WITH, comments become a space, quote-aware, no bind parameters
  (`? : @ $ #` outside literals), mutation scan. The statement then runs on a
  `mode=ro` + `query_only` pool with `query.max_rows` and `query.timeout`.

## Adding a connector

1. Implement `source.Connector` in `internal/source/<kind>/`:
   - `Discover` returns folders with `Role` set (`mail.Roles()`; `other`
     when the source has no roles — never derived from a name); it is the
     authority for folder metadata. The syncer keeps only the folders of
     picked mailboxes.
   - `Mailboxes` lists every store the last Discover saw (`source.StoreKey`,
     a valid `mail.StoreKind`, counts, `Error` when unreadable) and, for
     picked stores, the owner addresses the source reports, as reported
     (none is fine: the user declares them), with `IdentityComplete` true
     only when every read of the identity succeeded: an incomplete read
     never replaces the recorded identity.
   - `Capabilities` says what the data holds; layers ask it (scope ignores
     an addressed-to-me value from a source without the flag).
   - `Sync(ctx, folder, cursor, sink)` streams the folder's messages changed
     since `cursor`. Every message carries the connector's `ID()` as
     `SourceID`, a stable `ID` unique within `(SourceID, StoreID)`, and the
     folder's path/role. Item-level read failures go to `sink.Skip(ref, err)`.
     If `sink.Message` returns an error (output failure), stop and return it.
     Return the next cursor only when the folder was read completely, and take
     the cursor position *before* reading so stale data is never marked fresh.
     The syncer stages it and commits it only after the run's removal pass
     succeeds, and only for a store whose every folder delivered a complete
     snapshot or was verified unchanged.
   - Do not validate messages: the syncer validates once and counts invalid
     ones as skips (the store re-checks at its write choke point). Text
     must be valid UTF-8: decode what you read, and declare every decoding
     you had to choose, every part kept only in part and every value taken
     from another field (`m.Alter(code, detail)`, `mail.Alteration`); the
     syncer counts and logs them. Never substitute data silently.
   - Optional, for sources where listing is cheap and reading is not:
     call `sink.Snapshot(ctx, versions, complete)` with every id and change
     stamp of the folder; it answers which ids to deliver, and with
     `complete=true` the syncer removes stored messages absent from the
     list after the run (a vanished message whose Internet Message-ID is
     still stored in the same store counts as moved). Deliver `Partial`
     messages and implement `source.DetailReader`: the syncer then calls
     `ReadDetails` for each partial message, newest first, and stores the
     `mail.Details`. A partial write never erases details already stored.
   - Check `ctx` often; return `ctx.Err()` when cancelled.
2. Register an instance in a `source.Registry` and run it with
   `syncer.Run` or `syncer.Start` (job). The syncer owns batching,
   validation, de-duplication, the skip-ratio rule, reconciliation, cursors,
   scope recompute and events. Connectors do none of that.
3. Test with a fake sink (see `source/archive/archive_test.go`) and through
   the syncer with a fake connector (see `syncer/syncer_test.go`).

## Engines: adding a decision provider

1. If it speaks an existing protocol (`systemone`, `openai_decisions`), it is
   data only: add a complete record to `engine.providers.file` in the data
   directory (there are no built-in records; a missing or empty file means
   no providers). The top-level object is keyed by immutable record id
   (`[a-z0-9][a-z0-9_-]*`); each record names `label` (display only),
   `protocol`, `base_url` (absolute, may contain `{model}`; empty only for a
   managed runtime embedder), `path` (starts `/`, may contain `{model}`),
   `model`, `api_key_secret` (secret-store name; dev:
   `MAILSENSE_SECRET_<NAME>`), `max_questions` (the server cap),
   `context_tokens` (enforced before sending), `chars_per_token`,
   `reserve_tokens_per_byte` (the worst case a run's budget reserves before
   each call: tokens per request byte, 1 for byte-level tokenizers),
   `reserve_output_tokens`, `probability_tolerance`, `envelope`
   (`cloudflare` or empty), `noul_fold` (protocols without noul criteria),
   `price_input_per_mtok`/`price_output_per_mtok` (null = unknown: the
   budget then limits calls only), `retry` overrides (`max_attempts`,
   `statuses`, `quota_codes`; 400/401/402/403/404/422/500 can never be
   retried) and, for an embedder, `embedding`. Records are strict: unknown
   fields, a bad protocol, a missing required field or a negative number is
   rejected with every problem reported. `POST /api/providers` (create),
   `PUT /api/providers/{id}` and `DELETE /api/providers/{id}` write the
   whole set atomically; the UI and scripts are clients of that file.
2. A new wire format is a protocol descriptor (`internal/engine/protocol.go`)
   plus a codec in `internal/engine/decision`: implement `encode`/`decode`
   (strict: every requested question answered, numbers through
   `bounded`/`probability`), add it to `codecs`, and test it with an
   `httptest` fake including junk payloads (see `openai_test.go`). The
   descriptor owns the kind, auth scheme, required fields, probe and path
   validation; endpoints, models, keys, limits and prices stay data.
3. Select it with `triage.decider` (a record id; empty = not configured).
   `cmd/mailsense` starts even with no provider configured: the action and
   the learning stages report the missing record explicitly.

## Engines: adding an embedder

An OpenAI-compatible `/v1/embeddings` server is data: a record with
`protocol: openai_embeddings`, `base_url` + `path`, an optional
`api_key_secret`, `context_tokens` (per-input limit; left out for a
managed runtime, whose catalog's `slot_context` is it), `chars_per_token`
and an `embedding` object (`dimensions`, `matryoshka`, `query_template`,
`document_template`, `empty_title`, `norm_tolerance`, `max_batch`,
`reserved_tokens`). A model served by the managed sidecar sets `runtime`
instead of `base_url`/`api_key_secret` and gets a `models` entry in
`internal/sidecar/runtimes.json` (or `sidecar.runtimes.file`). The pinned
model selected by the top-level `local_embedder` is written to the owner's
provider file on first start as `local-embedder` (see
`docs/EMBEDDINGS.md`); a custom catalog that replaces that model must update
or remove `local_embedder`, because catalog validation requires the
selection's model to carry a complete embedding spec and a positive
`chars_per_token`. Select an embedder with `embed.provider` (record id;
empty = not configured); a different
model, endpoint, length or template is a new vector space that the next
index run fills, while renaming the record keeps its vectors. A new runtime
build is a variant entry with its archives' URL, size and SHA-256 (GitHub's
release digest); a GPU variant must pass the canary against the model's
pinned reference vector (or the platform's cpu build when it has one), and
the platform's `auto` list names the GPU builds `auto` may try.

## Triage: adding a question or role

- A question is profile data. Users add it in `triage.profile.file`
  (`{"questions": {"vip": {"type": "noul", "instructions": "…",
  "criteria": {"true": "…", "false": "…"}}}}`); built-in questions come from
  the legacy source through `scripts/gen-taxonomy.sh` (never edit
  `default_profile.json` by hand while the Python source exists). Any
  question change changes the fingerprint and therefore re-decides every
  message (the plan shows the cost first). Question order is chunk order.
- The one policy of paid decisions (`triage.classify`, lead decision
  2026-10-15): a decision stays valid for its message until the message's
  own revision changes (or the question set or decider: another key). The
  paid key is the profile fingerprint plus the decider; route and preview
  rows use the separate route key (`layer1.Plan.RouteKey`, `rk1-…`), which
  also covers teachers, requirements, evidence rules, algorithm/feature
  versions and the route settings, so a route configuration change never
  invalidates or re-bills a paid decision. A
  change of derived data — the cleaning rules, evidence facts, thread
  views — only makes it stale: the plan counts `stale` with
  `stale_estimate` (the re-decision cost), results are served flagged
  `stale`, and only `Options.Redecide` (the user's action) decides them
  again.
- A role is code meaning: add it to `roleTypes` in
  `internal/triage/taxonomy/roles.go` with its required type, plus a role
  constant when code reads it, then point it at a question in the profile
  (`roles`). Thresholds a role needs go in the `*Thresholds` tables there.
- Mailsense-specific profile data (escalation rules per role, the category
  options that count as suspicious, the relationships whose domains are
  known, extra thresholds) lives in `mailsense_profile.json`, merged over
  the generated file; users override it like the rest.
- `triage.facts.evidence` (off by default) adds the message's evidence
  facts (`evidence.Engine.FactsText`: causal signals, bucketed) to its
  observed facts; turning it on changes every state (the plan shows the
  cost). On without a wired evidence engine is an error.
- State wording (facts, history, examples) and the bands the history
  quotes live in `internal/triage/rules.json` (users: `triage.rules.file`).
  Changing them changes every state (the plan's `invalidated` shows the
  cost). States are causal: `causal_test.go` proves new mail, replies,
  labels and `time.zone` leave existing states unchanged.

## Live triage check

`scripts/triage-live.sh <record-id> 30 10` runs the protocol probe against
that decider record and stops on HTTP 402. Otherwise it imports the fixture into a temporary
database, triages the newest 30 owner messages and decides 10 of them
again, printing calls, tokens, estimated dollars, latency p50/p95, value
distributions and repeat-agreement (aggregates only). The run budget set
in the script refuses anything bigger; a full-archive run needs the
owner's approval and a raised `triage.budget.*`.

## Decider benchmark

`BENCH_DIR=<dir> scripts/bench-deciders.sh run decider-a decider-b`
compares any decider records of the provider file on equal terms. The harness
is test code in `internal/triage` (`bench_*_test.go`; live entry
`bench_live_test.go`, tag `deciderbench`), so it drives the real pipeline:
- the fixture is imported into a temporary database (`fixturetest`); triage
  is restricted to the primary inbox (`triage.folder_roles` = inbox);
- the sample is stratified (owner replied within `BENCH_REPLY_WITHIN` ×
  year received, largest-remainder allocation) and drawn with `BENCH_SEED`,
  which also draws the repeat and gold subsets;
- every provider decides the same messages through `Triager.Run`: same
  profile, composer and causal states (the report's `state_mismatch`
  proves it), the main phase in rounds of `BENCH_ROUND` messages, provider
  after provider, so a budget stop leaves every provider with the same
  messages; then the repeat subset again (forced) at
  `BENCH_REPEAT_CONCURRENCY`;
- the cap is the triage budget guard: each run gets what is left of
  `BENCH_MAX_USD`/`BENCH_MAX_CALLS`, its plan must fit, and every call's
  ceiling is reserved against it. A provider that reports its charge is
  accounted at it (`usd`); `usd_tokens_x_price` is tokens × the list price;
- measured per provider and phase: calls, tokens, dollars, call and message
  latency p50/p95/p99, decide span and messages/s, errors by class, failed
  calls by status, retries (`http.retry`), decoding rejections; repeat and
  cross-provider agreement per question (choice: exact + Cohen's kappa;
  noul: same side of the profile cutoff + mean |Δp|; score: same rounded
  level + mean |Δ|); weak-label evaluation (`feedback.Evaluate` on the weak
  reply labels with `feedback.weak_reply.within`: accuracy, F1 at the
  profile cutoff, AUC, Brier). There is no language split: the repository
  has no structural language detector.

`BENCH_DIR` receives `taxonomy.json`, `gold-input.jsonl` (`{id, text}`: the
gold subset as the providers saw it, without the history and label-example
blocks and without answers) and `answers-<provider>.jsonl`. Once a judge has
written `gold-labels.jsonl` (`{id, answers: {question: value}}`, values as
for labels: option name, true/false or probability, level),
`BENCH_DIR=<dir> scripts/bench-deciders.sh score` runs `feedback.Evaluate`
per provider and question (choice accuracy; noul accuracy/F1 at the
profile cutoff, 0.5; score MAE and `within_half_level` = |Δ| ≤ 0.5). Unknown
ids or questions, invalid values, an answers file that does not fit the
taxonomy, or another taxonomy are refused, all listed at once.

### Gold export (no provider call)

`scripts/bench-deciders.sh export` draws a sample for the gold judge from
the whole chosen mailbox — the received owner-scope mail of every triaged
folder — without calling any provider ($0). Strata: dominant script of the
message's own text × legacy category (`BENCH_LEGACY`, the legacy
classifications, when present) × sender kind (internal: the owner's
organisation by registrable domain; automated: a delivery report, receipt
or automatic reply, or List-Id / List-Unsubscribe / Auto-Submitted /
Precedence headers; external otherwise) × year. Legacy categories whose
proportional share is below `BENCH_RARE_FLOOR` (15) are first topped up to
it, then the rest of `BENCH_EXPORT_SIZE` is allocated by largest remainder;
`BENCH_SEED` fixes every draw. It writes `taxonomy.json`,
`gold-input.jsonl` (the model view, ids = the benchmark's `benchID`, so
earlier gold labels stay valid) and `strata.json` (aggregates) into a NEW
directory `$BENCH_EXPORT_ROOT/sample-<seed>-<size>-<UTC time>` (default
root `~/.local/share/mailsense/bench`, outside the repository; mode 700,
files 600), and checks that every id of `BENCH_KEEP_LABELS` (the earlier
`gold-labels.jsonl`) still names a message.

## Testing conventions

- `scripts/test-fast.sh` is the inner-loop gate: the same gofmt, script,
  vet and taxonomy checks, three test shards in parallel, the sidecar suite
  alone, and `-race` on `qa` and `api` (about 50–60 s on the dev box). It
  derives every package from `go list ./...`; a new package outside the
  named groups lands in shard C. Run `scripts/test.sh` before a release: it
  races every package. The sidecar suite binds real loopback ports; give it
  the machine (or set `SIDECAR=off` while another suite owns the ports).
- Browser behavior is covered by `scripts/test-ui.sh` (Playwright,
  `e2e/`): a real server per worker, real token/auth/CSP/SSE, and synthetic
  content endpoints only. It is not part of `test-fast.sh` because it needs
  Node and Chromium; run it before shipping a UI change. The suite only
  asserts DOM, focus, requests and live events; Windows-only behavior
  (WebView2, DPI, icon, Outlook COM, DPAPI) stays in the Windows live
  checks. The All-mail first paint uses one row builder
  (`allMailRowsFragment`/`appendAllMailRows`): 24 real rows paint first, the
  rest append after a double `requestAnimationFrame`, and
  `pendingAllMailRows` blocks a concurrent Load more. The Playwright tests
  hold `/api/boot`, observe the first `.all-mail-row` batch, deep-link a row
  in the deferred batch, and cover empty/error without fake rows;
  `TestAllMailProgressiveFirstPaint` guards the source contract, the
  prefetch guard, the `morePromise` wait/clear and the one
  `allMailRowNode(row, {` call site. The progressive test also installs the
  fake SSE seam and drives the two race probes: `config.changed` plus a
  hover/focus proves no bare prefetch on a filtered list, and a gated cursor
  plus `dropped` proves the refresh waits and lands 150 rows with one
  `.mail-list`. Explicit Load more clicks use `clickLoadMore`, which focuses
  without scrolling and dispatches the real handler; Playwright's
  scroll-into-view would otherwise let the observer replace the button
  mid-click. Do not weaken the 24/100/150, ID, one-list, sentinel or
  first-click assertions. The All-mail sender filter is covered at
  every layer: `TestNormalizeSenderQuery` (`internal/mail`),
  `TestMessageListSenderFilterAndCounts`,
  `TestMessageListCountsCacheSeparatesSenders`,
  `TestSearchSenderRuleIsUnicodeAndOneOwner` and the cursor/validation and
  sender keyset-plan tests (`internal/store`),
  `TestMessageListSenderParameterAndValidation` (`internal/api`), the sender
  row in the service cache-bypass table, and the two extended All-mail
  Playwright tests (exact filter, clear, sender empty copy, 100 → 150 sender
  page). The All-mail unread filter is covered by
  `TestMessageListUnreadFilterAndCounts`,
  `TestMessageListCountsCacheSeparatesUnread`, the cursor/validation and
  unread keyset-plan assertions (`internal/store`),
  `TestMessageListUnreadParameterAndValidation` (`internal/api`), the unread
  row in the service cache-bypass table, the static markers in
  `TestAllMailProgressiveFirstPaint`, and the extended Playwright tests
  (Unread toggle/URL/`aria-pressed`/row dot, the two group names
  "Folder filter" and "Read state", keyboard Space activation, the
  discriminating unfiltered/filtered counts, the "Clear unread filter"
  action, the unread empty copy, and the 100 → 150 unread sender page with
  `unread=true` on the cursor request). No rows or counts are filtered in
  the client. Round 41
  exercised a 40 + 36
  one-batch-per-frame variant (frames 24/64/100) and reverted it when the
  first-visible metrics regressed; the suite still guards the single
  deferred append. `bench-first-click.mjs` now splits long tasks at the
  first-frame mark so the deferred work is attributed separately.
- Deterministic and offline: `httptest` for HTTP, fake connectors and ciphers,
  temp dirs via `configtest`, `clocktest.Fake` for time. Tests wait on
  channels and events (`log.applied`, a connector's `entered`), never on
  sleeps or polling. The real deadlines left in tests (SQLite's
  `busy_timeout`, `query.timeout` on an endless query, net/http's
  `ResponseHeaderTimeout` against a handler that never answers) are timers
  inside SQLite and net/http. Each guards an operation that can never
  succeed, so the outcome does not depend on scheduling.
- The start-up contract is guarded in layers, with no production test hook:
  `TestMainStartsTheListenerBeforeTheWarm` parses `main.go` and requires
  `srv.Start` before `startWarm` before the window, and no blocking
  `warmDerived`; `TestWarmDerivedStartsMailBesidePriority` blocks each side
  in turn and proves neither Mail nor priority waits for the other;
  `TestWireSharesTheOneMailFirstPageService` proves the warm and the API use
  one cache; `TestSearchWhilePrepareRuns` keeps the text-first answer during
  a held vector refresh; the `bootState` tests cover every view, a failed
  view, and a never-run view at `ready()`; the API tests cover `/api/boot`,
  auth and the wrong method; `TestBootContractAssets` checks the shell,
  script and both catalogs; and the two Playwright boot tests prove the
  skeleton mounts before the delayed reads and the strip reports real
  progress and hides.
- Every misuse path (bad config, junk input, locked/corrupt DB, cancelled
  jobs, injection attempts) sits next to its happy path; every fixed bug has
  a regression test that failed before the fix.
- `go test -fuzz=FuzzValidateSQLNeverPanics ./internal/store` fuzzes the SQL guard.
- The fixture test (`-tags fixture`) is excluded from `go test ./...`. Run it
  with `scripts/fixture-import.sh`.
- Windows-only code (`secrets/dpapi_windows.go`, `fsx/sharing_windows.go`,
  `bootstrap/notify_windows.go`, `source/outlook/*_windows.go`,
  `sidecar/proc_windows.go`, `sidecar/vulkan_windows.go`) is compiled by
  `GOOS=windows go vet` in `test.sh`; it runs only on Windows (Outlook, the
  job object and the Vulkan probe). The
  non-Windows `sidecar/vulkan_other.go` returns `ErrVulkanProbe`, never nil.
- The Vulkan preflight is injected (`Options.VulkanProbe`) in every manager
  test, so the Linux fake-artifact suites can prove the no-device path
  without a GPU. `sidecar/vulkan_windows_test.go` pins the C struct sizes
  and offsets, and its live test is opt-in:
  `MAILSENSE_VULKAN_LIVE=1 MAILSENSE_VULKAN_EXPECT=present|none go test
  ./internal/sidecar -run TestVulkanLiveProbe -count=1 -v` on the Windows
  machine. A run of that test is a real measurement; a `GOOS=windows go
  vet` pass is only a compile check.
- The sidecar tests run real child processes: the test binary doubles as a
  fake llama-server (`sidecar/fake_test.go`: the fake runtime archive
  holds the test binary itself, and a variant whose env sets
  `MAILSENSE_FAKE_LLAMA=1` makes it serve), from a test artifact server,
  so download, verification, install, launch, key, port ownership,
  health, restart, crash recovery, pinned canary, no-cpu fallback rules,
  auto, tuning and the parent-death signal / job object run on Linux. They
  poll real processes with short timeouts (the one place tests wait on wall
  time).
