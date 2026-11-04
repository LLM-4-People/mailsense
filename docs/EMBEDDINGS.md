# Local embeddings (semantic index and search)

The source of truth for how Mailsense embeds mail locally: the decision and
its evidence (research of 2026-10-14, moved here from the session brief),
the design as built in phase 2b, and every measurement. The pinned
artifacts themselves (URLs, sizes, SHA-256s) are data:
`internal/sidecar/runtimes.json` (runtime builds, model) and the app's
`engine.providers.file` (the embedder record: protocol, runtime, model,
dimensions, task templates, limits). This file explains them; it does not
repeat the hashes.

## Decision

**EmbeddingGemma 2 (Q8_0 GGUF, 768 dimensions) served by llama.cpp's
`llama-server` as a managed sidecar process. On Windows the product has
exactly one managed build, Vulkan; Linux dev/tests keep the cpu build.
There is no CPU or CUDA build on Windows, no variant picker and no CPU
fallback.**

- Model support: llama.cpp PR #30054 (merged 2026-10-06), first build b11454.
  Pinned **b11617**: it includes #30416 (attention on CPU fix) and #30445
  (memory fix). The v0.6.0 "stable" is too old.
- `/v1/embeddings` with a list input returns 768-d finite unit vectors.
  The `dimensions` parameter is unsupported (#25210): Matryoshka truncation
  (512/256/128) and re-normalisation happen client-side (`vector.Truncate`).
- Task prefixes (EmbeddingGemma model card) are provider data:
  `task: search result | query: {text}` for queries,
  `title: {title} | text: {text}` for documents (empty title: `none`).
  Measured (research): a French query scored cos 0.77 against French
  documents vs 0.63 against English ones; the prefixes work.
- Server flags (data, `runtimes.json`): `--embeddings --pooling mean -c 8192
  -b 2048 -ub 2048 -np 4 --no-webui --offline`, bound to 127.0.0.1 on a
  free port. A cpu build gets the tuned `--threads`; a GPU build without a
  cpu build passes `--threads -1` when `sidecar.threads` is 0 (the
  default) and lets llama.cpp choose. 4 slots × 2,048 tokens per input.
- Builds: Windows ships only the **vulkan** archive (32 MB) and
  `sidecar.variant = auto` resolves it. CUDA (153 MB + 423 MB runtime) is
  not in the product: on the target driver generation it crashed at start,
  and the product is one engine. The cpu build remains on Linux for
  dev/tests and in custom catalogs. `--fit-target 256` lets llama.cpp's fit
  logic offload what fits next to the desktop's VRAM use (its default
  margin is 1,024 MiB, which on the target's ≈1.3 GB free would offload
  nothing). Vulkan on the T400 risks fp16 accumulation overflow → NaN (the
  model card warns about fp16): the canary catches that.
- Canary: the model pins a reference embedding of the catalog's canary
  sentence (`canary_vector`, 768 values, used verbatim; its
  `canary_sentence_sha256`). A GPU build is checked against it at every
  start, so Windows downloads no cpu build. A platform that has a cpu
  build measures the reference there only when the model has no usable pin
  (a custom model, or a different `embed.dimensions`). With neither, the
  canary fails explicitly: no silent skip.
- Every build gets its own directory, so archives with colliding file
  names never overwrite each other.
- Alternatives rejected: ONNX Runtime via purego (needs a Gemma tokenizer in
  Go; the fp32 ONNX model is 1.08 GB, more than the free VRAM; fp16 NaN;
  DirectML is in sustained engineering and ORT-DML stopped at 1.24.4);
  Windows ML's self-contained C API (plausible, unverified from Go); pure-Go
  inference (nothing mature); Ollama (an extra install the user would manage;
  ARCHITECTURE's earlier default, replaced).
- Licensing: llama.cpp MIT (+ LICENSE-LLVM-OpenMP for libomp),
  EmbeddingGemma Apache-2.0 + the Gemma Prohibited Use Policy. No CUDA
  runtime is distributed.


## Design as built (phase 2b, review round 2b)

### Runtime (`internal/sidecar`)

- **Pins:** `runtimes.json` maps `GOOS/GOARCH` → `auto` (the GPU builds
  `sidecar.variant = auto` may try, in order) and `variants` → archives.
  Windows has exactly one variant, `vulkan`, and `auto: ["vulkan"]`; it has
  no cpu and no cuda entry. Linux dev/tests have `cpu` and an empty auto
  list. A custom catalog may keep several GPU builds. The model is a raw
  GGUF from a fixed Hugging Face commit and pins the canary reference
  (`canary_vector` + `canary_sentence_sha256`). The runtime declares its
  `slots` and `slot_context` once: the args get `{slots}`, `{slot_context}`
  and `{context}`, and the embedder's per-input context comes from there
  (the provider no longer repeats it). Users override a runtime or model in
  `sidecar.runtimes.file` (merged per runtime and per model; null
  removes). Every name that becomes a path (runtime, version, variant,
  artifact, model) must be a plain file name; every problem of the merged
  catalog is reported at once, and a bad catalog or a `sidecar.variant`
  the catalog does not have on this platform is a startup error.
- **Download on demand** (`sidecar.auto_install`, or the install action
  `Manager.Install`): a `jobs` job (`sidecar.install`, progress in bytes on
  `jobs.update`) per build, single-flight; and one fetch per file at a
  time, so two builds installing at once share the model download. Each
  artifact streams into `<name>.part`; an interrupted transfer resumes with
  an HTTP `Range`, and a `206` whose `Content-Range` does not start where
  the part ends starts the part over (up to `sidecar.download.attempts`,
  overall `sidecar.download.timeout`). The finished part is checked for
  size and SHA-256 and only then renamed into place (`fsx.Promote`).
- **Install:** archives are unpacked through an `os.Root` into
  `<build>.installing` (names must be local, link targets must stay inside
  by name, and the root refuses anything that would leave it through
  links — the chain `l1 → .`, `l1/l2 → ..`, `l1/l2/x`), the SHA-256 of every
  file goes into `.manifest.json` with the archives' pins, and the
  directory replaces the old one only once complete.
- **Verified before every launch:** the model against its pin, every file
  of the build against the manifest, no file that the manifest does not
  list (a planted DLL is refused), the manifest's pins against the
  current pins. A file whose size and modification time did not change
  since it last hashed right is not hashed again (≈360 MB per launch
  otherwise); a changed file is reinstalled (auto_install) or refused.
  Known limit: between this check and the exec, a process of the same
  user could swap a file — the same user can replace the app itself.
- **Launch:** a free loopback port; the manager's one random 32-byte key in
  `key_env` (registered once for redaction; never on the command line);
  the child's environment is the catalog's `env_allow` list of the app's
  variables, the variant's env and the key (no `MAILSENSE_*`, no stray
  `LLAMA_*`). After the first healthy answer the child must hold its port
  itself (Linux: the listening socket's inode among its descriptors;
  Windows: `GetExtendedTcpTable`): another local program that took the
  port between the check and the bind would get the key and the mail.
  Windows: started `CREATE_SUSPENDED | CREATE_NO_WINDOW`, put in a job
  object with `KILL_ON_JOB_CLOSE`, then resumed; the job handle is closed
  exactly once, also for a child that crashed. Linux: `Pdeathsig = SIGKILL`.
- **Supervision:** `/health` every `sidecar.health.interval`, or at once
  when a caller reports the runtime unreachable (`Locator.Failed`: the
  waiting callers get a new latch, and a failed check then restarts it
  without waiting for more misses); `sidecar.health.failures` misses, an
  exit, a changed `sidecar.variant` or a re-probe restart it with
  `mathx.Backoff`. A GPU build that stops `sidecar.health.failures` times
  within `sidecar.restart.max_delay` of its start is marked broken (kept in
  `state.json`); the next start uses cpu only when the platform has a cpu
  build. Windows has none: the Vulkan build stays stopped and the status
  says it is broken and a re-probe is required. States on `sidecar.state`:
  idle, installing, starting, tuning, canary, probing, ready, restarting,
  failed, stopped.
- **`auto` (the default):** the first start (or the first after a pin
  change or a re-probe) installs and starts each build of the platform's
  auto list. On a platform with a cpu build it also measures the cpu's
  tuned rate; a GPU build wins only when the canary passes **and** it
  reaches `sidecar.auto.min_speedup` (1.1) times the cpu rate. On Windows
  there is no cpu build: each auto candidate is installed, started and
  canary-checked against the pinned vector, and the first one that passes
  serves; no cpu rate is measured and `sidecar.auto.min_speedup` does not
  apply. The choice and every probe (cosine, tokens/s, speed-up where a cpu
  exists, or the reason it lost) are kept in `state.json`; later starts use
  the choice without probing. A named build (`vulkan` on Windows; `cpu` on
  Linux) is used as asked unless marked broken; a GPU build that fails its
  start or canary is marked broken, and cpu serves only when the platform
  has one (`Status.Fallback`). Windows returns the explicit error instead.
- **Canary:** the catalog's sentence on the GPU build against the model's
  pinned vector (used verbatim; matched to the current sentence hash and,
  when `embed.dimensions` > 0, its length), after the state cache. A
  platform with a cpu build measures the reference there and caches it per
  model, sentence and length only when the model has no usable pin. A pin
  that does not match and no cpu build is an explicit error naming the
  model, never a skipped check.
- **Threads:** `sidecar.threads = 0` measures the thread counts
  `sidecar.tune.cpu_percent` (25/50/75/100 % of the machine's logical CPUs:
  no fixed maximum) at the first cpu start and keeps the fastest per
  build, model and CPU count. A GPU build without a cpu build gets
  `-1` and llama.cpp chooses.
- **Actions for the UI:** `Builds` (every build of the platform: auto,
  installed, size, download size, pins, broken reason; on Windows that is
  the single Vulkan build), `Verify`, `Remove` (refused while in use),
  `Reprobe` (forgets the runtime's choice, broken marks, tuning and canary
  references, restarts it), `Learned` (tuning, choices with probes, broken
  builds), `Status` (every runtime/model of the catalog, idle when unused).

### Windows Vulkan preflight (round 25)

- **Why.** The b11617 Windows archive is audit-reported (the artifact was
  not available on the Linux dev box) to contain no `vulkan-1.dll` and to
  ship the CPU backend DLLs. Without a preflight, a machine with no usable
  Vulkan device could download the real catalog's **342,039,075 B**
  (309,855,456 B model + 32,183,619 B runtime archive), then fail — or
  silently use the bundled CPU backend. The preflight makes the machine
  fact explicit before any byte is fetched.
- **Data.** A variant declares `requires` (only `"vulkan"` is known;
  `""` needs nothing). The embedded Windows `vulkan` build declares it.
  Catalog validation rejects `cuda`, `Vulkan`, `" vulkan"` and any other
  value with `unknown requires`, for the embedded catalog and for
  `sidecar.runtimes.file` overlays (same `parseCatalog`).
- **Probe.** On Windows, `platformVulkanProbe` loads `vulkan-1.dll` from
  System32 (`windows.NewLazySystemDLL`, never the search path), calls
  `vkCreateInstance`, `vkEnumeratePhysicalDevices` and
  `vkDestroyInstance` through `golang.org/x/sys/windows` (no cgo), and
  destroys the instance. Missing DLL, `VK_ERROR_INCOMPATIBLE_DRIVER`,
  `VK_ERROR_INITIALIZATION_FAILED` and zero devices are `ErrNoVulkan`;
  access/bad-image, missing procedures and unexpected results are
  `ErrVulkanProbe`. On other platforms the probe explicitly returns
  `ErrVulkanProbe`; it never returns nil.
- **Boundaries.** `Manager.require` is the one owner and runs before
  `installJob`, before `layout.verify`, at the start of `install` (before
  the progress total is computed) and in `probeBuild` before the probing
  state. No probe runs in `New`. Only a confirmed device is cached (the
  existing `Reprobe` action forgets it); a missing device is re-probed on
  each status read, and the card's Refresh invalidates the cached
  `/api/sidecar` status before re-reading, so it sees a newly installed
  driver within the 15 s TTL. `ErrVulkanProbe` is never cached.
  `ErrNoVulkan` is permanent for the supervisor; `ErrVulkanProbe` may be
  retried.
- **Behavior.** `auto` on Windows (no cpu build) returns the typed
  `ErrNoVulkan`/`ErrVulkanProbe` wrapped, with zero installs and zero
  starts. A platform with a cpu build may continue to the cpu candidate.
  A named build returns the error before the canary, `markBroken` and any
  fallback. There is no CPU fallback, no second build and no new setting.
  `BuildInfo.Unavailable` is `vulkan` or `probe_failed`, and the API maps
  them to `422 vulkan_unavailable` / `503 vulkan_probe_failed`.
- **Measured (Linux, synthetic fake artifacts).** The round 25 control (a
  GPU variant with no `requires`) downloads the model and archive once,
  serves 10,776,987 B in this run (the fake archive is the test binary, so
  this varies with the build), starts one install job, starts the runtime
  and reaches canary/installing/ready/starting. The same catalog with
  `requires: "vulkan"` and a no-device probe serves **0 B**, makes **0**
  model and archive requests, creates **0** install jobs, starts nothing
  and publishes only `failed`. Two `Builds()` reads plus the failed start
  invoke the probe **3** times (a negative result is not cached, so a
  status Refresh re-probes); `Reprobe` makes the next read the **4th**. A
  present device is cached: start plus one `Builds()` is **1**. The real
  catalog total 342,039,075 B is asserted by a separate test; the synthetic
  bytes are not that number.
- **Not measured.** No Windows host was available in this round, so the
  live probe and the real 0-byte Windows install are blocked; `GOOS=windows
  go vet ./...` only compile-checks the probe and its struct-layout tests.
  The archive's DLL contents are audit-reported, not re-checked here.

### Embedder (`internal/engine/embed`)

- Protocol `openai_embeddings`; the record lives in the app's
  `engine.providers.file` (there is no embedded provider record). A managed
  record names its `runtime`: base URL, key, per-input context and the
  model file's revision come from the sidecar (`Locator.Model`); any other
  OpenAI-compatible server is a record with `base_url`, an optional
  `api_key_secret` and its own `context_tokens`.
- **First start.** When `embed.provider` is at its default (empty and not
  explicitly set by the file or environment), startup writes the pinned
  `local_embedder` as one ordinary record (`local-embedder`: protocol,
  path, model, runtime, `chars_per_token` and the model's complete
  embedding spec) to the owner's provider file and selects it. The model
  name, spec and estimate are data in `internal/sidecar/runtimes.json`;
  Go code never names the model. An explicit value (including an explicit
  empty), an existing equivalent record and an id collision are never
  overwritten. A custom `sidecar.runtimes.file` that replaces the pinned
  model must update or remove `local_embedder`: catalog validation
  requires the selected model to exist and to carry a complete embedding
  spec and a positive `chars_per_token`.
- **Space** = protocol/model/dimensions/template digest/record
  fingerprint/model revision (12 hex digits of the GGUF's SHA-256): a new
  model file, endpoint or template is a new space, while renaming the
  record (id or label) keeps its vectors. `embed.dimensions = 0` is the
  native length.
- **Input:** whitespace collapsed, title first, cut to
  `embed.max_input_tokens` × `chars_per_token` characters, inside the task
  template, and never longer than the context minus `reserved_tokens`
  UTF-8 bytes (a byte-fallback tokenizer makes at most one token per
  byte; 2,560 characters of one real mail made 2,089 tokens).
- **Trust boundary:** one item per input with a unique index, finite JSON
  numbers only, the native length, unit norm within `norm_tolerance`; then
  truncation and re-normalisation.
- **A dead runtime is not an unavailable embedder:** when a call cannot
  reach the managed runtime (refused, reset, cut), the adapter reports it
  (`Locator.Failed`), waits for the restart (`Locate`) and sends the batch
  once more. Bodies are read through `httpx.ReadBody`
  (`http.max_response_mb`), errors quote `http.error_text_chars`.
- **Retry overrides:** the adapter installs the record's `retry` policy
  (`max_attempts`, `statuses`, `quota_codes`) on every call through
  `httpx.WithPolicy`, just as the decision and generate adapters do;
  records without `retry` keep the `http.retry.*` defaults.
- `embed.Source` rebuilds the embedder only when the settings or the
  providers file changed (`Config.VersionKey`); `embed.Probe` (canary,
  tuning, auto) caches its adapter the same way and gets the endpoint per
  call.

### Index (`internal/index`)

- Eligible: active folders of the picked stores (`mailboxes.selected`) and
  `index.folder_roles`, newest first; `Options.Stores` narrows a run.
- Input: subject as title, `mail.CleanBody` as text. CleanBody's limits are
  data (`internal/mail/clean.json`); a bare rule (`====`, `____`) is a
  reply boundary only when a header or quoted line follows within
  `rule_lookahead_lines`, a header block not right under a heading line
  ending in ":", so an underlined title, a rule under "Notes:" and a
  "Status:" list are kept (English and French tests).
- Incremental by `messages.revision` and the input hash; one vector per
  message, int8 + scale, written with a write sequence kept in `meta`
  (`embedding_seq`) that is never reused.
- A failed batch whose error is message-scoped is embedded again message
  by message; a message's failed attempts are counted per revision
  (`index_failures`) and after `index.quarantine_after` it is left out
  until it changes (an input that crashes the runtime no longer blocks
  newest-first indexing). A remote provider's 5xx (including 500), rate
  limit or gateway answer is provider-wide instead: the run stops after one
  call without counting or quarantining a message, and retries the same
  mail later. Only a managed runtime's 500 (llama-server's oversize-input
  answer) keeps the per-message split. A vector write that answers
  SQLITE_BUSY/LOCKED is retried by the store's one busy
  policy (`index.store_busy_attempts` times, `index.store_busy_delay` apart,
  because another writer can hold the database past `store.busy_timeout`); a
  batch that stays busy stops the run with `stopped_by=store_busy` and is not
  counted or quarantined (the next run embeds it again, and vectors already
  stored are kept). The setting pair keeps its historical `index.*` prefix
  but governs every bounded store write: sync flushes, triage decision and
  failure writes, label writes (including the layer-1 judge) and usage
  records. An unavailable embedder or a cancellation stops the run; no
  failure removes a vector.
- Coverage in all and per store key and role (`primary/inbox`), with
  quarantined counts; `index.progress`, `index.finished`, `index_runs`.
- `index_runs` keeps the newest `index.runs.keep` runs (default 100, bounds
  1…1,000,000). A finished run prunes older reports immediately after its
  own row is written
  (`PruneIndexRuns` → `DELETE … WHERE id NOT IN (… ORDER BY id DESC LIMIT ?)`),
  so a stuck or unavailable embedder cannot grow the table forever (the
  unavailable wait below makes those retries rarer still). The run that
  just finished is the newest and is never removed; `Stage.Status` reads that
  row to report an unavailable-embedder blocker, so a `keep` of 1 keeps the
  reason visible. Retention deletes rows only: SQLite reuses the freed pages
  and the database file may not shrink; there is no `VACUUM`. The synthetic
  failed-row fixture measures 2,261 B of columns per run (1,904 B report,
  299 B error, 58 B metadata); the prepared audit store's 7 failed rows
  measure 2,326 B each (1,904 + 299 + 123). 720 runs/day is inferred from
  the 2-minute reconcile interval, not measured over 24 hours, and an
  unbounded history would add about 1.6–1.7 MB/day at those row sizes
  (inferred).
- A run that stopped with `stopped_by=unavailable` after committing no
  vector (`Report.Committed` false) arms a process-local wait
  (`index.retry_unavailable`, default 15m, bounds 2m…1h). While it is
  active, `Ensure` returns the stopped run's starting coverage and neither
  plans nor starts a run; the 2-minute reconcile makes no provider call and
  writes no row. The wait belongs to the exact work it was armed for
  (vector space, text version, mail generation, folder roles, quarantine
  threshold): a change of any of them, an effective settings change, the
  configured embedding provider reporting healthy, or the managed runtime
  becoming ready ends it. A run that committed vectors, a transient stop,
  store busy, cancellation and an every-batch failure do not arm it (they
  may have written vectors or failure records and must re-plan). An
  unavailable run that committed no vector does not re-plan when it
  finishes: the persisted `after` coverage is its starting coverage, so a
  plan cannot fold the run's own recorded failures into a coverage change
  the run never wrote. The latest unavailable
  run stays in `index_runs`, so `Stage.Status` keeps its `no_embedder`
  blocker and the exact error while the reconcile is quiet: nothing is
  hidden and no pending mail is dropped.
- `Indexer.AfterSync` starts a run after every successful sync run
  (`sync.finished`) while `index.after_sync` is on (wired in
  `cmd/mailsense`; phase 4 shows the runs).
- `Indexer.Ensure` is the startup/reconcile owner: it builds the configured
  embedder, computes the same plan and coverage, and — when actionable mail
  is pending or stale — claims the one run slot (`jobs.Single`) and starts
  exactly one background job through the same `startClaimed` path as
  `Start`. A run already in progress is left alone (the next reconcile owns
  the remainder), and a complete index, or one whose only remaining work is
  partial or quarantined mail, is a no-op. An unbuildable embedder is an
  explicit error; `Ensure` never makes a provider call itself. While the
  unavailable wait is active it returns the cached starting coverage with
  no error and no work.
- `cmd/mailsense`, after the API is announced, calls `Ensure` once and then
  every `indexReconcileInterval` (2 minutes), logging indexed/eligible,
  pending and stale coverage and throttling repeated embedder-unavailable
  warnings. This is the recovery path after a restart, a provider-space
  change, a failed or stalled run, or a sync event that never arrives. It
  never blocks API readiness. The local sidecar process is kept for the app
  lifetime; the periodic reconcile — not a sync — is what retries a failed
  run. The composition root wires the wait's reset points: any
  `config.changed`, a healthy `engine.provider` status for the configured
  embedder, and a `sidecar.state` that reaches `ready`.
- The first full index is not instant: at the live box's measured ≈10
  messages/s, 35k messages take about an hour (newest first). Runs are
  incremental thereafter (message revision + input hash). Coverage is
  disclosed throughout (`/api/learning`, index job progress and the search
  notice); partial and quarantined mail stays visible as pending until it
  can be indexed.

### Search (`internal/search`)

- One filter for both rankings (`store.SearchFilter`), triage filters
  through `Triager.Results`, RRF with `search.rrf_k`, snippets from the
  body, the coverage notice, full-text answers with the reason when the
  query cannot be embedded within the `search.embed_timeout` budget (150 ms by default); full text is already ranked.
- The vector copy merges the rows written since its last load; when the
  store then holds another number of vectors (a removal, also one a new
  vector replaced) it reloads all.
- Coverage: the eligible ids are cached per mail generation and settings
  (computed outside the lock); how many of them have a vector is counted
  from the copy at every search.
- Query vectors are cached in memory, bounded to 64 entries per process and
  keyed by `(space, folded text)`. Repeated and concurrent identical
  queries share one embedder call; failures and evictions are re-embedded.
  The omnibox's suggestions ask for `semantic=off`, so typing never starts
  the model; the Search page keeps the semantic ranking and reuses a
  suggestion's vector only when that suggestion was semantic too.
- `Searcher.Warm` runs once after the startup reconcile, in the background:
  it builds the embedder, loads the in-memory vector copy and embeds one
  fixed query text within a 60 s bound (`warmQueryTimeout`), so the first
  user search does not pay the runtime load. It checks no learning gate,
  carries no mail content and returns no result; it shares the vector copy's
  existing locks with concurrent searches.

## Storage choice: int8 (measured)

Local benchmark (`scripts/embed-local.sh 600`, real model on this dev box,
605 fixture messages, 76 queries — **historical quiet-box run**): int8 needs
772 bytes per vector instead of 3,072 (−75 %), and its top-10 agrees with
the exact float32 ranking at **recall@10 0.983–0.987, recall@50
0.981–0.982** (int8-vs-exact-float32 storage rank agreement, not retrieval
recall). At 34k messages that is 26 MB in memory and in SQLite instead of
104 MB. int8 is the only format. The current-host Round 42 baseline is
recall@10 **0.9803** / recall@50 **0.9824** (below).

## Measurements

| What | Result |
| --- | --- |
| Research (285H stand-in, WSL, under load), Q8_0, 8 threads | 850–900 tok/s; a ≈3,000-char mail ≈ 580 tokens; 12 threads under load collapsed to 265 tok/s (hence tuning); Qwen3-Embedding-0.6B 371 tok/s |
| Thread tuning, dev box (12 logical CPUs), first start | 4 → 819, 6 → 837, **8 → 977** tok/s (kept: 8) |
| Index, dev box, cpu, 605 fixture messages (cleaned, ≤ 512 tokens) — **historical quiet-box run** | **5.3–5.6 mails/s, 857–863 tok/s** (≈ 155 tokens per mail after cleaning); 19 truncated at the budget; 0 failures (after the byte-cap fix; 8 before). Not current: Round 42 measured **0.94 mails/s, 142 tok/s**, 18 truncated at load 10.6–67.4 |
| Index, current host, cpu, 605 fixture messages, Round 42 baseline (2026-10-29) | **0.94 mails/s, 142 tok/s**, 18 truncated at the budget, 0 failed/quarantined, 76 batches; recall@10 **0.9803** / recall@50 **0.9824** (int8-vs-exact-float32 storage rank agreement, not retrieval recall); load 10.6–67.4 |
| Hybrid vs full text, known item (own subject), dev box, real model | 76/76 hit@10 both (a small sample: every subject is distinctive) |
| Search latency, dev box, 605 vectors (query embedding on the CPU dominates) | p50 33 ms, p95 51 ms |
| Vector scan alone, 100,000 × 768 int8, 12 threads (`vector.BenchmarkScan100k`) | **6.1 ms** per query (top 200) |
| Fixture, 33,842 messages, lexical fake embedder | indexed in 10 s; full-text precision 10/10 on every term; known-item hit@10 hybrid 151/189, full text 153/189 (threads share subjects); filtered hits 0 violations; search p50 19 ms, p95 25 ms over 33,842 vectors; degraded path answers from full text |

Extrapolated full archive on CPU at the historical quiet-box rate
(5.3 mails/s): 34k mails ≈ 1.7 h (the research's 6.5 h assumed uncleaned
580-token mails). The current-host baseline is 0.94 mails/s, so this is not
a current-host extrapolation.

### Batch size A/B (Round 42, 2026-10-29)

The local harness (`scripts/embed-local.sh 600`) now reads the private
provider catalog (`EMBEDLOCAL_PROVIDERS`, record `EMBEDLOCAL_PROVIDER`,
default `local-embedder`) and accepts `EMBEDLOCAL_BATCH` to override
`embed.batch_size` for a benchmark; neither is read by the shipped app.
Before this repair the `embedlocal` test failed before indexing with
`no embedding provider is configured`.

| Batch | Runs | mails/s median | tok/s median | recall@10 | recall@50 | search p50 / p95 | failed/quarantined |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 8 (baseline, measured first) | 5 | 0.94 | 142 | 0.9803 | 0.9824 | 133.8 / 768.5 ms | 0 / 0 |
| 8 (A/B arm) | 5 | 1.54 | 234 | 0.9803 | 0.9824 | 91.2 / 276.4 ms | 0 / 0 |
| 16 | 5 | 0.94 | 143 | 0.9776 | 0.9839 | 89.9 / 349.3 ms | 0 / 0 |
| 32 | 5 | 2.48 | 377 | 0.9750 | 0.9842 | 66.4 / 112.6 ms | 0 / 0 |

605 messages, 76 queries, the pinned cpu runtime/model (runtime
`llama-server` SHA-256
`9e67814939a986bab80934e06a21579cbb2b4c2b47e574b24382827daa75759c`), 0
failed/quarantined in every run. The baseline's five runs ranged
0.56–2.29 mails/s (4.1×) at load 10.6–67.4; the A/B ran five order-balanced
runs per arm at load 12.0–89.6. The medians are therefore load-confounded.
Batch 32's median is **+61.0% mails/s / +61.1% tok/s** over the 8 arm (a
median win, load-confounded), but its recall@10 (int8-vs-exact-float32
storage rank agreement, not retrieval recall) fell **0.0053** (outside
±0.005 by 0.0003). Batch 16's recall@10 fell 0.0027 (inside), but its
median is **−39.0%** and its p95 +26%; its low-load runs (3.65/3.73) are
~20% above the 8 low-load runs (2.94/3.15). The Round 42 brief's median
gate is a **no-win**; the low-load 16 signal is a candidate controlled
re-test, not a default change. The ±0.005 recall tolerance and ≥3%
throughput gate were specified in the Round 42 brief before the A/B; the
post-A/B summary (`/tmp/mailsense-round42-measure-ab.txt`) is the first
file that records them. `ab-drive.sh` only defines the order-balanced
block order, and there is no independent pre-A/B gate file. The default
stays **8**; no config change was made. The prepared 33k store has **0
embeddings** and the 605-message sample is not its distribution, so this
is not a 33k indexing or populated-vector claim;
Windows/Vulkan/GPU/paid-provider measurements remain blocked: the host
exposes only a faux DRM device (`DRIVER=faux_driver`), has no Vulkan ICD
(`/usr/share/vulkan/icd.d` is empty) and no `vulkaninfo`.

### Pinned canary reference (2026-10-27)

The catalog's canary sentence was embedded by the cpu build on a Windows
machine for this exact model: **768 values, all finite, norm
1.000000**, stored verbatim as `canary_vector` in `runtimes.json` with
`canary_sentence_sha256` =
`f93987314012a9db8ab92ea790970d005a69e472a9483a603aa728dcf270bde3`.
On the same machine the Vulkan build measured **cosine 0.9998** against
it, above `sidecar.canary.min_cosine` (0.99). That pinned vector is now
the Windows reference: the canary no longer downloads or launches a cpu
build, and the vector is never logged.

### Windows measurements (2026-10-14)

These measurements were taken with the earlier catalog that also offered
Windows cpu and cuda builds; the product catalog now has neither (the cpu
measurements remain the evidence for the pinned vector and for the Linux
dev/test path).

i5-13500T (20 logical CPUs), NVIDIA T400 4 GB, driver 596.51. Every
artifact was downloaded on the machine by the sidecar, hash-checked and
installed in `%LOCALAPPDATA%\Mailsense\runtime`. Sample: the newest 300
complete messages of the primary Inbox (20,075 synced read-only in 336 s
into a temporary database, removed afterwards).

| Build | Served by | Canary (cos vs cpu) | Throughput, 300 mails (61,180 tokens) | GPU memory while loaded |
| --- | --- | --- | --- | --- |
| cpu | cpu, 6 threads (tuned: 4 → 794, **6 → 1,004**, 8 → 731 tok/s) | reference | 873 tok/s, 4.28 mails/s | — |
| **vulkan** | vulkan | **0.99981** | **1,126 tok/s, 5.52 mails/s** (first run); index job warm: **1,593 tok/s, 7.81 mails/s** | 2,797 used / 1,147 free MiB (desktop alone 2,191–2,633 used) |
| cuda 13.4 | **cpu (fallback)** | — | (cpu) 767 tok/s | the build crashed at start: `CUDA error: the provided PTX was compiled with an unsupported toolchain` (exit 0xC0000409): driver 596.51 cannot JIT this build's PTX |

- GPU memory before the run: 2,633 MiB used, **1,311 MiB free** (earlier
  probe 1,351); after: 1,753 free.
- Install (download + verify + unpack): cpu with the model 7.6 s, vulkan
  2.6 s, cuda 16.1 s (577 MB). Start incl. the first tuning: 15.7 s.
- Index job (vulkan): 300/300, 0 failures, 11 inputs cut to the budget.
- Search over the 300 (100 known-item queries, the messages' own subjects):
  **p50 37 ms, p95 41 ms**; hit@10 hybrid 99/100, full text alone 95/100.
- Job object: a helper "app" started the cpu runtime and exited without
  stopping it; the runtime (pid 53772) ended with it.
- Left on the machine, where the app keeps it: cpu build 50.0 MB, vulkan
  build 91.5 MB, model 309.9 MB, state.json 15 KB (451 MB). The cuda build
  was removed.

#### Review round 2b re-run (2026-10-15, `auto`)

A re-run with `sidecar.variant = auto` after a re-probe: the cpu build
tuned again (4 → 722, **6 → 894**, 8 → 859 tok/s), then the auto list's
vulkan build passed the canary (0.99981) at **1,787 tok/s = 2.00× cpu**
and was chosen; the choice and the probe are in `state.json`. First start
(tuning + probe) 22.0 s; the **second start took 2.3 s and did not
probe**. On the 300 newest Inbox mails: vulkan 1,651 tok/s / **7.84
mails/s**, cpu 599 tok/s / 2.85 mails/s (same sample, same machine load);
the index job 300/300 at 7.42 mails/s; search p50 38 ms, p95 44 ms,
semantic used on 100/100 queries, hit@10 99/100 (full text 95/100). GPU
memory: 1,607 MiB free before, 1,112 while vulkan was loaded, 1,645
after. The job object ended the runtime with its app. The sidecar test
suite also passes on Windows. Left in the runtime directory: cpu
47.6 MiB, vulkan 87.2 MiB, model 295.5 MiB, state.json 16 KB.

**Windows product: one build, Vulkan, chosen by `auto`.** These runs used
the earlier catalog that also offered cpu and cuda; Windows no longer has
either. `auto` (the default) installs, verifies, starts and
canary-checks Vulkan; at the measured rate the primary store's 34k mails
take ≈1.2–1.7 h on Vulkan, in the background, newest first. If Vulkan
cannot start or fails the canary, the runtime reports the error and stays
stopped: there is no CPU fallback and no hidden download.

## Running it

- Windows (the product): first use installs, verifies and starts the single
  Vulkan build (`sidecar.variant = auto`); the canary uses the model's
  pinned vector. There is no CPU/CUDA download and no manual variant step.
- `scripts/embed-local.sh [messages]` — dev box benchmark through the real
  sidecar path with the pinned Linux build (downloaded once into
  `~/.cache/mailsense-runtime`, hash-checked).
- Settings: `embed.*`, `sidecar.*`, `index.*`, `search.*` in
  `internal/config/schema.json`.
