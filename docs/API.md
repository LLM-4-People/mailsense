# Local API

`cmd/mailsense` serves the desktop backend on `tcp4 127.0.0.1:0` (a fresh
ephemeral port per launch). There is no TLS, no fixed port, no CORS and no
cookie: the only client is the embedded same-origin shell.

## Launch and authentication

- Every launch generates a 256-bit token (`base64.RawURLEncoding`, 43
  characters) and a non-secret 8-byte launch id.
- The base URL, PID, token, launch id, start time and build are written to
  `<data>/runtime/api.json` (mode 0600) once the listener is bound. The file
  is removed on a graceful shutdown when it still names this launch.
- `Authorization: Bearer <token>` is required on every `/api/*` path.
  `?token=<token>` is accepted **only** on `GET /api/events` (the
  `EventSource` fallback); if both are present they must match.
- Host must be exactly `127.0.0.1:<bound port>`; an Origin, when present,
  must be `http://127.0.0.1:<bound port>`, and `Sec-Fetch-Site: cross-site`
  is refused. Both checks answer `403` JSON before token comparison.
- The static shell (`GET /`, `GET /index.html` → `308 /`) is unauthenticated
  and contains no data.
- A second process with the same data directory exits before binding: the
  single-instance lock is `<data>/mailsense.lock`.

`scripts/run-windows.sh` starts the app and waits for the descriptor;
`scripts/stop-windows.sh` reads it, posts `/api/shutdown` with the token in
curl's stdin config, and waits for the process and descriptor to disappear.
Neither prints the token.

## Errors

Every API error is one JSON object:

```json
{"error":{"code":"invalid_limit","message":"limit must be between 1 and 200",
  "params":{"name":"limit","min":"1","max":"200"}}}
```

`code` and `params` are the stable machine contract. `message` is always an
English diagnostic/fallback; the server does not localize. The client looks
up `api.<code>` in its catalog, interpolates `params`, and shows the English
`message` only under a diagnostics disclosure. `problems` is present on
`422` validation errors and on `423 learning_locked` (each `{code, stage,
subject, message, params, actions, measure}`); a validation problem carries
`params.key` and `params.source`. A client that does not know a code shows
the server message or a generic localized message, never a blank.
Panics become `500 internal` with no panic value, stack or path; a panic
after the response started aborts the connection. Request bodies are capped
at 1 MiB (`413 payload_too_large`) and ordinary handlers have a 30 s context
deadline (long reads 5 min); every write frame has its own deadline.
Wrong methods are JSON `405 method_not_allowed` with an `Allow` header.
Triage paging misuse is `400 invalid_limit` (outside 1…200) or `400
invalid_offset` (negative). Mail-list misuse is `400 invalid_role`,
`invalid_store`, `invalid_cursor`, `invalid_limit` (outside 1…200),
`invalid_counts` or `invalid_unread` (both boolean filters carry
`params.name`). Search misuse is `400 invalid_scope`, `invalid_limit`
(outside 1…50), `invalid_offset` (outside 0…1000), `invalid_store`,
`invalid_role`, `invalid_sender` or `invalid_attach`. Priority misuse is
`400 invalid_filter` or `400 invalid_limit` (outside 1…50); Q&A misuse is
`400 invalid_question`/`invalid_language` (history limits outside 1…100
are `400 invalid_limit`) and `503 qa_disabled`.
A Windows GPU build's device preflight has two codes of its own:
`422 vulkan_unavailable` (no usable Vulkan device: install a GPU driver with
Vulkan support, or use another machine — permanent until the machine
changes) and `503 vulkan_probe_failed` (the check itself failed and may be
retried). Both are mapped before the generic runtime-unavailable codes, so
a caller can tell an environment fact from a broken install. The same
errors reach prompt/index job paths through the one error mapper.

## Endpoints

`G` means the global statuses: `401` missing/invalid token, `403`
Host/Origin refused, `405` wrong method, `500` internal.

| Method and path | Request | Success | Notes |
| --- | --- | --- | --- |
| `GET /api/status` | — | `200` `StatusView` | version, launch, PID, URL, data/store paths, sources, running jobs, SSE clients, pending restarts, live resources, `system_language`, `system_locale`, `time_zone`; learning status is `/api/learning`, not part of this view |
| `GET /api/boot` | — | `200` `BootView` | start-up state; `state` is `warming` or `ready`; `done`, `total`, `failed`, `current` and the ordered `views` (`name`, `state`, `duration_ms`); not gated, no store read |
| `GET /api/learning` | — | `200` `LearningView` | `{status, open, preview}`; `open` is the real gate latch, never the test preview; `preview` is the effective preview rule (`learning.test_mode` on and the real gate closed). `status.stages[].certifications[]` carries each question's route certification report when the questions stage has one; its `class_recall` (`{class, eligible, answered, correct}`) and `class_collapsed` name a class the pooled rule never answered and do not yet reject certification |
| `GET /api/gate` | — | `200` `GateView` | the Mail gate read: `{allowed, preview, status?}`; one `GateState` reading, so `allowed`, `open` and `preview` all come from one evaluation of the gate rule. It reads no messages and builds no Mail page. On the open/latched path `Allow` reads the learned-scope latch (a store meta read) and short-circuits before the full status; on a locked path it builds and returns the full status, which is carried in `status`. A locked answer is `200 allowed=false`, not `423`; authenticated and never learning-gated. The measured 5 ms warm / 11 ms cold are open-gate only; the locked cost is unmeasured |
| `POST /api/learning/zone` | `{"zone":"Europe/Paris"}` | `200` `LearningView` | confirms the owner time zone |
| `GET /api/jobs` | — | `200` `{"jobs":[…]}` | in-memory history |
| `GET /api/jobs/{id}` | — | `200` `jobs.Info` | `404` when pruned/unknown |
| `POST /api/jobs/{id}/cancel` | empty or `{}` | `202` `jobs.Info` | cancellation is asynchronous |
| `GET /api/mailboxes` | — | `200` `{"mailboxes":[…]}` | |
| `POST /api/mailboxes/discover` | `{"source":"outlook"}` | `200` `{"mailboxes":[…]}` | long timeout |
| `POST /api/mailboxes/pick` | `{"keys":["store-…"]}` | `200` mailboxes | 1…256 keys (`400 invalid_request` outside it); commits the selection, then starts one sync job per source with a picked mailbox (same job as `/api/actions/sync`; a source already syncing is skipped) |
| `POST /api/mailboxes/unpick` | `{"keys":[…]}` | `200` mailboxes | 1…256 keys (`400 invalid_request` outside it) |
| `POST /api/mailboxes/declare` | `{"key":"store-…","address":"…"}` | `200` mailboxes | owner identity |
| `POST /api/mailboxes/undeclare` | same | `200` mailboxes | |
| `POST /api/actions/sync` | `{"source":"outlook"}` | `202` `{"job":…}` | `422 no_mailbox_picked`, `409 busy` |
| `POST /api/actions/run_evidence` | empty or `{}` | `202` `{"job":…}` | evidence rebuild job |
| `POST /api/actions/index` | `{"stores":["store-…"]}` | `202` `{"job":…}` | empty stores = picked |
| `POST /api/actions/run_layer1` | empty or `{}` | `202` `{"job":…}` | layer1 learning job |
| `POST /api/actions/run_judge` | `{"limit":1,"questions":[…],"budget":{…}}` | `202` `{"job":…}` | judge job |
| `POST /api/actions/backfill_html` | `{"source":"outlook"}` | `202` `{"job":…}` | display and inline-image re-read, see below |

An action has one spelling: a name the dispatcher does not know answers `404 not_found`. The former aliases (`evidence`, `layer1`, `judge`, `label`, `label_messages`, `backfill_bodies`) were removed. `label_messages` is still a learning blocker id, but the UI handles it by opening the Labels screen; the queue itself is `GET /api/labels/queue`.

| `GET /api/settings` | optional `keys=a,b` | `200` `{"settings":[…],"total":250,"restart_pending":[…]}` | `?keys=` projects the list de-duplicated in schema order; absent or empty returns all; `total` is always the full schema count; unknown key → `400 invalid_keys` with `params.key`; secret values masked |
| `GET /api/settings/{key}` | — | `200` `SettingView` | `404 unknown_setting` |
| `PUT /api/settings/{key}` | `{"value":…}` | `200` `SettingView` | mailbox selection keys → `409 use_mailbox_endpoint`; `ui.language` and `learning.test_mode` apply live, no restart |
| `DELETE /api/settings/{key}` | — | `200` `SettingView` | reset to default |
| `POST /api/settings/reload` | empty or `{}` | `200` `{"restart_pending":[…]}` | |
| `GET /api/settings/learned` | — | `200` `{"learned":[…]}` | |
| `POST /api/settings/learned/{key}/reject` | `{"value":…}` | `200` learned | |
| `POST /api/settings/learned/{key}/unreject` | `{"value":…}` | `200` learned | |
| `GET /api/secrets` | — | `200` `SecretsView` | names and expected names only |
| `PUT /api/secrets/{name}` | `{"value":"…"}` | `204` | read-only store → `403 read_only` with the env remedy |
| `DELETE /api/secrets/{name}` | — | `204` | |
| `GET /api/providers` | — | `200` `{"providers":[…],"roles":[…]}` | provider records + health; role selectors with their configured/available state (a record can be configured but unavailable when its key is missing) |
| `POST /api/providers` | complete record (`id`, `protocol`, `base_url`, `path`, `model`, `api_key_secret`, limits/prices, `retry`/`embedding` when needed) | `201` `ProviderInfo` | strict whole-set validation, atomic write; an omitted `api_key_secret` on a keyed record is suggested as `PROVIDER_<UPPER_ID>_API_KEY` |
| `PUT /api/providers/{id}` | complete record (body `id` must be absent or equal) | `200` `ProviderInfo` | selected record's kind cannot change until the role is cleared |
| `DELETE /api/providers/{id}` | — | `204` | refused (`409 provider_in_use`) while a role selects the record |
| `POST /api/providers/{id}/test` | empty or `{}` | `200` `{"ok":true,"latency_ms":…,"model":…,"detail":…,"detail_code":…,"detail_params":…}` | protocol-aware: decider probe, one tiny chat call, or one short embedding; checks key presence first and never returns the key; long timeout. `detail_code` is `decider_answered`, `generator_answered` or `embedder_answered` (the embedder also carries `detail_params.dimensions`); `detail` stays the English fallback |
| `POST /api/providers/discover` | `{"base_url":"https://provider.example.test/v1","protocol":"openai_chat","api_key":"…","api_key_secret":"…"}` | `200` `{"models":[…]}` | **long timeout**; fetches the provider's official `GET {base_url}/models`. `base_url` is required, absolute http(s), trailing slash trimmed; `protocol` defaults to `openai_chat`; `api_key` takes precedence over `api_key_secret`; no key sends no `Authorization`; see [Model discovery](#model-discovery) |
| `GET /api/sidecar` | — | `200` `SidecarStatus` | builds and runtime states; on Windows exactly one build (`vulkan`), installed and chosen automatically (no cpu/cuda rows). A build this machine cannot run carries `unavailable` (`vulkan`: no usable device; `probe_failed`: the check itself failed); reading the status never downloads, installs or starts it |
| `POST /api/sidecar/install` | `{"runtime":…,"model":…,"variant":…}` | `202` `{"job":…}` | a build of the catalog for this platform; on Windows that is `vulkan` only (the automatic path needs no manual call). The capability preflight runs before the job: no device is `422 vulkan_unavailable`, a failed check is `503 vulkan_probe_failed`; neither creates a job or downloads a byte |
| `POST /api/sidecar/verify` | same | `200` `{"ok":true}` | long timeout |
| `GET /api/logs` | `?since=<seq>&limit=<n>&attrs=on\|off` | `200` `LogSnapshot` | `limit` 1…5000 (default 500; independent of `log.ring.size`); entry messages and `attrs` are already redacted by `logx` (registered secrets; built-in token/email patterns always applied; `log.redact.patterns` only adds custom expressions; content keys, including split compounds, → `[REDACTED]`), the API never re-redacts attributes. `attrs` defaults to `on` (the unchanged entry shape); `attrs=off` removes every entry's `attrs` and adds `has_attrs` (true when the stored entry had attributes), so one expanded row can read `GET /api/logs/{seq}`; invalid `400 invalid_attrs` |
| `GET /api/logs/{seq}` | — | `200` log entry | exactly one retained entry **with** its `attrs`; one bounded ring lookup; a malformed or non-positive sequence is `400 invalid_id` (the shape every path id uses), and `404 log_not_found` means the well-formed sequence rotated out or never existed |
| `GET /api/events` | `?since=<seq>` or `Last-Event-ID` | `200` SSE | see below |
| `GET /api/hub` | — | `200` `Hub` | pre-gate; gate, label counts, top blockers, mailbox counts and newest 6 manual answers; no content |
| `GET /api/answers` | `?question=…&source=…&message=…&cursor=…&limit=…` | `200` `AnswerPage` | pre-gate; current manual/judge labels newest first, see below |
| `GET /api/labels/summary` | — | `200` `LabelSummary` | pre-gate; the same per-question label counts as the hub, no mailbox counts |
| `GET /api/labels/queue` | `?question=…&limit=<n>&review=<bool>` | `200` `LabelQueue` | pre-gate; `review=true`/`1` returns judge suggestions, see below |
| `POST /api/labels` | `{"items":[{…}]}` | `200` `PutResult` | manual batch, pre-gate |
| `GET /api/messages` | `?role=<name>&store=<key>&sender=<address\|domain>&unread=true\|false&before=<cursor>&limit=<n>&counts=1` | `200` `MessageListView` | **gated**, owner-only all-mail page; `role` a mail folder role or `all` (default, excludes nothing), `store` one mailbox key, `sender` one exact address or one domain (`@example.org` or `example.org`; never free text or a display name), `unread=true`/`1` keeps only unread mail (`false`/`0` and an absent value both mean every read state, never a read-only page), opaque `before` cursor bound to the role/store/sender/read state, `limit` 1…200 (default 100), `counts=1` adds the per-role totals; newest `instant` first; invalid `400 invalid_role`/`invalid_store`/`invalid_sender`/`invalid_cursor`/`invalid_limit`/`invalid_counts`/`invalid_unread` |
| `GET /api/messages/{id}` | — | `200` `MessageView` | **gated**; adds the `sender` identity contract |
| `GET /api/messages/{id}/attachments/{position}` | `?v=<sha256>` | `200` image bytes | **gated**; serves one stored inline image (`image/png`, `image/jpeg`, `image/gif` or `image/webp`), `Content-Disposition: inline`, immutable private cache. `404 asset_unavailable` when the part has metadata but no bytes, `404 not_found` for an unknown row, position or stale `v`, `415 unsupported_asset` for any other type, `400 invalid_attachment`. The shell fetches it with the bearer header and turns the bytes into a `blob:` URL inside the message iframe; the token never appears in an image URL |
| `GET /api/triage` | `?limit=<n>&offset=<n>` | `200` `triage.Page` | **gated**; newest first; `limit` 1…200 (default 50), `offset` ≥ 0; `total` is the whole decided population; `preview` is the effective preview rule |
| `GET /api/triage/summary` | — | `200` `triage.Summary` | **gated**; `key`, `coverage`, `total` and per-question value counts; `preview` is the effective preview rule; no message content |
| `GET /api/triage/{id}` | — | `200` `triage.Result` | **gated**; `preview` is true only when a preview answer is shown |
| `GET /api/search` | `?q=…&scope=owner\|all&limit=<n>&offset=<n>&store=<key>&role=<name>&sender=<address\|domain>&attach=any\|yes\|no&semantic=on\|off` | `200` `search.Result` | **gated**; full text first, semantic merged within `search.embed_timeout` (150 ms default) or disclosed as degraded; `scope=all` default, `limit` 1…50, `offset` 0…1000, one store/role/sender/attachment filter, `semantic=off` for text-only suggestions; invalid `400 invalid_scope`/`invalid_limit`/`invalid_offset`/`invalid_store`/`invalid_role`/`invalid_sender`/`invalid_attach`/`invalid_semantic` |
| `GET /api/priority` | `?limit=<n>&filter=<name>` | `200` `PriorityView` | **gated**, owner-only; `limit` 1…50 (default 8); `filter` `all`, `reply`, `urgent`, `security`, `decision`, `deadline`, `workload`; unknown filter `400 invalid_filter` |
| `GET /api/agenda` | `?hours=<n>` | `200` `AgendaWire` | **gated**, owner-only; `hours` 1…168 (default `agenda.default_hours`); a malformed, non-finite or out-of-range value is the same `400 invalid_hours` with `params.name`/`min`/`max` |
| `GET /api/companies` | `?limit=<n>&q=<text>` | `200` `CompaniesView` | **gated**, owner-only; ranked external organizations; `limit` 1…100 (default 25); `q` up to 120 bytes filters label, domain or address over the whole ranking; `total`/`has_more` describe the whole matching population |
| `GET /api/companies/profiles` | repeated `key=<key>`, `?months=<n>&event_days=<n>` | `200` `{"generated_at":…, "profiles":{"<key>":CompanyProfileWire}}` | **gated**, owner-only; 1…16 deduplicated keys for the visible-row prefetch; same validation as `{key}`, `404 unknown_company` if one key does not resolve |
| `GET /api/companies/{key}` | `?months=<n>&event_days=<n>` | `200` `CompanyProfileWire` | **gated**, owner-only; key `c:<company>` or `d:<domain>`; `months` 1…60, `event_days` 1…365; `404 unknown_company` |
| `GET /api/threads/{conversation_id}` | `?limit=<n>&before=<id>&at=<id>` | `200` `ThreadView` | **gated**, owner-only; `limit` 1…100 (default 50); `before` pages older exclusively, `at` ends the page at the opened message inclusively (mutually exclusive); `404 unknown_thread` |
| `GET /api/conversations` | `?limit=<n>&before=<cursor>&state=<name>&q=<text>` | `200` `conversations.Page` | **gated**, owner-only; one digest row per conversation; `limit` 1…100 (default 30), opaque `before` cursor, six thread states, folded search over subject/sender; invalid `400 invalid_limit`/`invalid_cursor`/`invalid_state`/`invalid_query` |
| `GET /api/commitments` | `?kind=<name>&direction=<name>&status=<name>&limit=<n>` | `200` `commitments.Result` | **gated**, owner-only; `kind` `due`, `reply`, `decision`, `action`; `direction` `owner`, `other`; `status` `open`, `overdue`, `done`, `unknown`; `limit` 1…500 (default 100); invalid filter `400 invalid_kind`/`invalid_direction`/`invalid_status`/`invalid_limit` |
| `GET /api/brief` | `?since=<timestamp>` | `200` `brief.Result` | **gated**, owner-only; `since` optional (default seven days, RFC3339 or the stored timestamp form, never in the future); invalid `400 invalid_since_time` with `params.name = "since"`; every section and items array is always present |
| `POST /api/ask` | `{"question":"…","language":"en"\|"fr","conversation_id":"…","route":"auto\|local\|remote"}` | `200` `AskView` | **gated**, long timeout; question 3…500 characters; optional `conversation_id` is 1…64 ASCII letters, digits, `_` or `-`; an omitted id starts a thread and returns a generated 16-hex id; optional `route` defaults to `auto` and is returned on the view; `400 invalid_question`/`invalid_language`/`invalid_conversation_id`/`invalid_route`; `503 qa_disabled` |
| `GET /api/ask/history` | `?limit=<n>` | `200` `{"items":[AskView]}` | **gated**; `limit` 1…100 (default 20), newest first |
| `DELETE /api/ask/history` | — | `204` | **gated**; removes the local history |
| `POST /api/shutdown` | empty or `{}` | `202` `{"ok":true}` | second call `503 shutting_down` |
| `GET /` | — | `200` HTML | embedded desktop shell |
| `GET /index.html` | — | `308` `/` | |

`GET /api/messages` has one read-state switch: `unread=true` (or `1`) keeps
only unread mail and adds the same predicate to the page and its role
counts; `unread=false` (or `0`) and an absent value both mean every read
state. It is a filter switch, not a read-only mode. The opaque `before`
cursor carries the switch and is rejected with `400 invalid_cursor` when the
next request asks for the other state. A value that is neither `true`/`1`
nor `false`/`0`, including a present-but-empty or blank value, is
`400 invalid_unread` with `params.name = "unread"`.

All successes are JSON with `Content-Type: application/json;
charset=utf-8` except the static shell, SSE and `204`. Validation failures
are `400 invalid_*`, `415 unsupported_media_type`, `422
invalid_value`/invalid domain values, owner `404`s, and `409 busy` for a
second run of the same kind. Every numeric path id (`{id}`, `{folder}`,
`{seq}`) that is not a positive integer is the same `400 invalid_id`, so a
malformed URL never reads as "the resource is gone".

`StatusView.system_language` is the detected display-language base (`"fr"`)
and `system_locale` its regional BCP-47 tag (`"fr-CA"`); both are empty when
detection found nothing. `time_zone` is the configured owner zone
(`time.zone`), empty until the user confirms one. These are projections of
the OS and of one setting, not additional preferences: the only language
preference is `GET`/`PUT /api/settings/ui.language` (`auto`, `en`, `fr`).
The shell shows confirmed dates in `time_zone` and discloses the machine
zone when it is empty; the server never formats user-facing dates.

### Mailbox selection and first-open discovery

`POST /api/mailboxes/pick` commits the selection and then starts one sync
job per source that has a picked mailbox. Starting those jobs never reads
mail on the request path: the response returns the updated mailbox list,
and each job's own discovery records the picked mailbox's reported owner
identity before it reads mail. A source already syncing (the action's
`409 busy`) is skipped, so a pick never creates a second job. If a job
cannot start, the selection stays committed and the failure is logged; the
pick response is still successful.

On a new install only — the store has listed no mailbox and
`mailboxes.selected` is empty — startup launches one best-effort discovery
in the background after the warm finishes (`/api/boot` is `ready`). It lists
stores only, never picks one, never reads mail, never retries in a loop, and
is skipped when a selection or any discovered mailbox already exists. A
discovery already running (`ErrSourceBusy`) is benign. Shutdown cancels and
joins the discovery before the store closes. Picking remains the explicit
act that starts sync.

### Start-up, boot and the cold paths

The listener, its descriptor (`runtime/api.json`) and the desktop window come
before the derived warm. `GET /api/boot` is the one contract for that window:
it reports `warming` with the views that are done and the one currently
running, then `ready`. The ordered views are `mail`, `learning`, `priority`,
`agenda`, `triage_summary`, `triage_page`, `home`, `conversations`,
`commitments`, `companies`, `brief` and `semantic_search`. `mail` is first
in the view order and is warmed beside the learning/priority group: it
builds the exact first page the Mail click asks for (`limit=100`, `counts=1`,
no role/store/cursor), so the click joins one cache instead of starting a
second store scan. A view that fails stays in the list as `failed`; a view
that never ran is marked `failed` when the boot becomes ready. `ready` means the warm finished, not that every view
succeeded: `failed` and the per-view states are the honest result, and the
shell keeps a warning strip for a non-zero `failed`. The endpoint is
authenticated like every other path and is never learning-gated. It reads no
mail and no learning status.

The first `GET /api/priority` while the ranking is cold waits for the one
shared build instead of starting a second mailbox scan; concurrent callers
join it, and a caller that disconnects does not cancel it. `Close` at
shutdown cancels it and waits up to 10 s; production store reads honor the
context, and a non-cooperative build is logged rather than blocking
shutdown forever. If the learning gate is closed during warm, the endpoint
answers the ordinary `423 learning_locked`; it does not serve a partial
ranking.

`GET /api/search` is available from the listener onward. It still checks the
learning gate and can answer `423` during warm. The semantic prepare
(vector copy, coverage, filter sets) runs after the derived warm and before
`ready`; a search that arrives while it is still running serves the full-text
ranking first and the existing warming notice for the semantic leg. If the
prepare fails (for example no embedder), the failure is reported as a boot
view warning in the log and search keeps its existing text-first degradation;
no fake result or empty success is returned.

First-open discovery and the semantic maintenance worker (one Ensure, one
query warm-up, then the two-minute reconcile) both wait for `ready` or for
shutdown, so neither competes with the views the boot strip is counting.

### Hub, answers and label summary

All three reads work while the learning gate is closed: they are not
gated, return no subject, sender, recipient, body or snippet, and never
expose hidden (unpicked) or shared (non-owner) mail. A label value is the
raw JSON the owner or the judge stored; the profile question `type`
(`noul`, `choice` or `score`) is added by the API, empty when the current
profile no longer names the question.

`GET /api/hub`:

```json
{
  "generated_at": "2026-10-26T09:20:00Z",
  "gate": {
    "open": false,
    "state": "blocked",
    "labels": {"manual": 10, "judge": 40, "needed": 30, "review": 30},
    "questions": [
      {"question": "reply_needed", "manual": 10, "judge": 40, "needed": 30, "review": 30}
    ],
    "blockers": [
      {"code": "layer1_too_few_eval_labels", "subject": "reply_needed",
       "detail": "This question has 10 of the 30 evaluation labels it needs; 20 more are needed.",
       "measure": {"have": 10, "need": 30, "unit": "evaluation labels"}}
    ]
  },
  "mailbox": {"messages": 1200, "stores": 1, "synced": true},
  "recent_answers": [
    {"id": 91, "message_id": 123, "question": "reply_needed", "source": "manual",
     "type": "noul", "value": true, "at": "2026-10-26T09:12:00Z"}
  ]
}
```

- `gate.open` is the gate answer (`/api/learning`'s `open` latch);
  `gate.state` is the overall learning state, so the two can differ while
  new mail is learned incrementally.
- `labels.manual`, `judge`, `needed` and `review` sum the questions.
  `needed` comes from each labels-stage
  `layer1_too_few_eval_labels` blocker's `measure.need`; `review` counts
  judge labels whose message and question have no manual label. Both the
  labels stage and the questions stage follow the question's **route**
  certification (`routes[].certification`); the layer-1 verdict
  certification is the training-stage diagnostic. A question whose route
  certification precision is below its target has no labels-stage blocker
  (the questions stage owns that blocker), so it adds nothing to `needed`:
  more labels cannot raise its precision. Questions
  with labels or a labels-stage blocker are returned, ordered by remaining
  `max(needed-manual, 0)` descending, then `review` descending, then
  question name; no question is invented from the profile.
- `blockers` is at most 3: the labels-stage blockers, or the first
  blockers of the other stages when the labels stage has none. Codes,
  subjects, details and measures are the learning stage's own.
- `mailbox` counts visible owner messages and picked stores with visible
  mail; `synced` is true only when the sync stage is done. No hidden mail
  is counted.
- `recent_answers` is the newest 6 **manual** labels: the owner's own
  trail. Gemma's suggestions stay on `GET /api/answers`, so the hub rail
  never implies a suggestion is the owner's answer.

`GET /api/answers?question=&source=&message=&cursor=&limit=`:

- `question` is optional. `source` is `manual` or `judge`; empty means
  both, anything else is `400 invalid_source`.
- `message` and `cursor` are positive row ids; invalid or non-positive
  values are `400 invalid_message`/`400 invalid_cursor`. `limit` is 1…100
  (default 50); outside that is `400 invalid_limit`. Unknown query
  parameters are ignored.
- Newest first by the labels table's `rowid` descending. `cursor` is the
  last returned id; the response is
  `{"items":[…],"next_cursor":91,"has_more":true}`. Items carry
  `id`, `message_id`, `question`, `source`, `type`, raw `value` and `at`.
- This is the **current label state**, not an append-only event log: an
  edit replaces the stored value and keeps the row's cursor position and
  `created_at`. Corrections remain `POST /api/labels`; this read adds no
  write path.

`GET /api/labels/summary` returns the label totals at the top level plus
the same ordered questions and top 3 blockers as the hub, without
`mailbox`, `recent_answers` or `generated_at`:

```json
{"manual": 10, "judge": 40, "needed": 30, "review": 30,
 "questions": [{"question": "reply_needed", "manual": 10, "judge": 40, "needed": 30, "review": 30}],
 "blockers": [{"code": "layer1_too_few_eval_labels", "subject": "reply_needed",
               "detail": "…", "measure": {"have": 10, "need": 30, "unit": "evaluation labels"}}]}
```

### Label queue

`LabelQueue` is `{"question","strategy","tasks","total","has_more"}`, and every task is
`{"message_id","question","reason"}`. It carries no message content.
Without `review` the queue offers messages with no evaluation label for
the question; `strategy` is `uncertainty` (the default; abstained first,
then the route answer closest to its decision boundary) or `missing`
(by id), and `reason` is `uncertain` or `missing`.

`review=true` (or `1`) instead offers the visible members that carry a
**judge** label for the question and no manual label, so the owner can
accept or correct the teacher's suggestion. Each task adds
`"reason":"review"`, `"suggestion":…` and `"suggestion_source":"judge"`.
The suggestion is the decoded judge value in the form the answer
controls apply: the option name (choice), `true`/`false` (yes/no), or a
level index (score, rounded half-up). A stored judge value that does not
decode under the current profile is skipped; it never fails the queue.
`review=false`, `0`, empty or absent is the normal mode; any other value
is `400 invalid_review`. Normal mode never reads or returns a label value;
review mode reads the stored judge value intentionally and still returns
no message content. A manual label that decodes under the profile removes
its message from review, and the labels stage counts the manual label first
(judge labels remain teacher targets).

### Triage answers and the preview

`triage.Page` and `triage.Summary` carry `preview`: the effective preview
rule — the explicit `learning.test_mode` is on and the real gate is closed.
It is never the real gate — `GET /api/learning` keeps `open` as the latch —
and grants nothing.

`triage.Result.values` maps each question name to its judged answer:
`{"type","refused","choice","number","confidence","abstain"}`. `type` is
`choice`, `score` or `noul`; `refused` says the model declined the
question; `choice` and `number` carry the answer; `confidence` is the
stated confidence of a choice or score answer; `abstain` names why an
abstained answer must not be used on its own. The distribution and its
diagnostics — `probabilities`, the pre-blend `model` value and the learned
`prior` — are internal and are not exposed on `/api/triage` or
`/api/triage/{id}`.

`triage.Result.preview` is true only when at least one of that message's
answers is a preview answer.

While the preview is on and the route is uncertified, a question whose
stored route answer abstained with `not_certified` is served as the
layer-1 model's threshold-free best guess (`selective.Given`: choice,
value and confidence, no abstention). A certified route answer,
`below_target`, `no_answer`, a refused answer and a question absent from
the route record are never replaced; a message with no preview row keeps
its abstention. Preview answers live in their own table
(`route_preview_answers`, migration 0015), written by the same layer-1
commit as the route. The route, the gate, certification, the judge and the
report never read them. The summary's value counts and every coverage
counter stay the stored route's: the preview is display data, not an
answer source.

### Priority mail

`GET /api/priority` is the owner-only important-mail page. It is gated and
reads only visible owner mail (`scope_verdicts.verdict = 'owner'`, active
folders of picked stores). It never reads shared or unattributed mail and
never exposes a body: `snippet` is plain text collapsed to at most 180
characters.

```json
{"items":[{"message_id":1,"conversation_id":"…","subject":"…","sender_name":"…","sender_address":"…",
  "received_at":"2026-10-25T10:00:00Z","folder_role":"inbox","has_attachments":false,
  "snippet":"…","score":87.5,"reasons":["security","urgent"],"preview":true,
  "signals":{"urgency":3,"reply_needed":1,"suspicious":1,"decision_needed":0,
  "deadline":4,"effort":2,"request_count":2,"is_question":0,"call_to_action":0,
  "info_sufficiency":1,"category":"security","asks_for":"approval_decision",
  "stance":"neutral","relationship":"client",
  "subject":{"topic":"meeting_scheduling","subtopic":"confirmation",
    "path":"meeting_scheduling/confirmation","basis":"ask:approval_decision"}},
  "action":{"primary":"decide","waiting":"owner","reasons":["decision"]}}],
 "total":42,"available":42,"offset":0,"has_more":false,
 "generated_at":"2026-10-26T12:00:00Z","preview":true}
```

`items` is always an array (never null). `limit` is 1…50 (default 8) and
`offset` is 0 or more (default 0); a negative or non-integer offset is
`400 invalid_offset` before the service is called. `available` is the ranked
depth of the current snapshot and is at most 200 (`priority.MaxCached`);
`offset` echoes the page start actually served, and `has_more` says whether
another page follows inside that snapshot. `total` counts every candidate
that passes the filter, not the mailbox, and may be larger than `available`:
candidates outside the top 200 are not pageable until the ranking rebuilds.
`generated_at` is when the ranking
snapshot was built, not when this request was served: a
stale-while-revalidate page keeps its build time. `preview` is the effective preview rule
(`learning.test_mode` on and the real gate closed);
`items[].preview` is true only when at least one answer of that message is
the layer-1 threshold-free preview answer substituted for an uncertified
route abstention. The values come from `route_answers` and the separate
`route_preview_answers` table; a certified or `below_target`/`no_answer`
route answer is never overridden. A malformed stored answer fails the read
instead of becoming a value.

Signals are plain values: a yes/no answer at the profile's own cutoff
(`thresholds.reply`, `thresholds.suspicious`, … else `thresholds.noul_default`)
becomes 0/1, score answers their nearest level, and
`category`/`topic`/`asks_for`/`stance`/`relationship` the chosen option
(empty when absent). No probabilities are exposed. `subject` is the
deterministic topic → subtopic hierarchy derived from those answers; its
`basis` names the one evidence code (`ask:<option>`, `decision`,
`category:<option>`, `reply`, `default`). `action` is the one
owner-facing classification: `primary` is `reply`, `decide`, `pay`, `schedule`, `send`, `do`, `verify`, `follow_up`, `wait`, `read` or
`none`; `waiting` is `owner`, `other`, `unknown` or `none`; `due_source`
is `stored` or `classifier` (a classifier deadline is a level, never a
date). Automated bulk mail (`spam`, `marketing`, `notifications`, or an
`automated_service` relationship) is read-only whatever its text asks and
never enters the ranking. The deterministic score is owned by
`internal/priority` (see `docs/ARCHITECTURE.md`), not by a setting per
weight. Reasons are a subset of `security`, `decision`, `reply`, `urgent`,
`deadline`, `action`, `question`, `workload`, `recent`. Ties order by
score, then received instant, then message id, all descending. The service
caches the top 200 for 15 s per preview flag; the client may cache the
page too.

Filters apply after scoring: `reply` includes a live owner reply
(`action.primary = reply` and `action.waiting = owner`), so an answered
thread, a history row, automated mail and a payment are excluded;
`urgent` includes urgency level ≥ 3 or deadline ≥ 3; `security` includes
only the suspicious judgment or spam (the security category is a topic, not
a threat); `decision` includes a live owner decision
(`action.primary = decide` and `action.waiting = owner`);
`deadline` (level ≥ 3) and `workload` (effort or request count ≥ 2) match
their signals; `all` is unfiltered. A message enters the ranking only with
a substantive positive signal and a score of at least 20; a suspicious,
decision, live reply, urgent, deadline or concrete-ask signal always
enters. Automated bulk mail never enters, however urgent its text. Rare
high-signal mail therefore surfaces without claiming the preview's answers
are certified.

### Agenda

`GET /api/agenda?hours=3` is the calendar-linked "next X" view. It joins
three kinds of stored evidence in one ordered list:

- calendar occurrences of the picked mailboxes inside the window (canceled,
  declined, free and duplicate occurrences are dropped);
- conversations where the thread resolver says the owner still owes a
  reply — a colleague's reply in a visible shared Sent folder suppresses
  the obligation instead of flagging it;
- owner mail with a follow-up due date inside the window (recent overdue
  ones stay visible; a follow-up marked complete is not due and is never
  listed).

A reply whose thread shares a participant with an upcoming meeting is
scheduled before that meeting and carries `linked_event_title`. Every item
has `kind` (`event`, `reply`, `deadline`), `at`, `title` and `reasons`, plus
the evidence the row renders: `last_at` (reply), `detail`, `message_id`
(mail), `location`, `all_day` and `linked_event_title`. `events`,
`obligations`, `backlog`, `deadlines` and the applied `horizon_hours` are
counted server-side. `obligations` counts the replies included in the window;
`backlog` counts the waiting threads left out because they are older than
a week or beyond the five most recent unlinked ones. A due date more than
a day overdue is left in Mail instead of flooding a three-hour agenda. An unknown horizon
is rejected; a longer-than-maximum request is clamped by the service and
reported in `horizon_hours`.

When a shared or delegate mailbox exists but no folder of it is picked,
the thread resolver reports `unknown`/`visibility_gap` rather than
claiming the owner owes a reply: the colleague's answer may be outside the
visible evidence.

```json
{
  "horizon_hours": 3,
  "items": [
    {"kind": "reply", "at": "2026-11-03T09:00:00Z", "last_at": "2026-11-02T16:00:00Z",
     "title": "Plan review", "message_id": 42, "linked_event_title": "Kickoff",
     "reasons": ["waiting_since"]},
    {"kind": "event", "at": "2026-11-03T10:30:00Z",
     "title": "Kickoff", "location": "Room A", "detail": "Project kickoff",
     "reasons": ["meeting"]}
  ],
  "events": 1, "obligations": 1, "deadlines": 0, "backlog": 0
}
```

`items` and each item's `reasons` are always arrays. A reply item with
`linked_event_title` must be handled before that event. The wire carries
only the rendered view: the build bounds (`now`, `until`), the next-event
shortcut, `slack_seconds`, `busy_seconds`, `truncated`, conversation and
event ids, `end` and the participant count stay in the service value.

### Companies

`GET /api/companies` ranks external organizations, never people. `q`
filters the whole ranking before the page limit, so a company beyond the
first 100 rows is reachable; `total` is the matching population and
`has_more` says the returned `companies` are its prefix. A row carries
`key`, `label`, `domains`, `people`, `messages`, `sent`, `received` and
`last_at`; `coverage` carries `contacts`, `contacts_with_company` and
`declared_orgs`. An address
belongs to the company its visible address-book entry declares; otherwise
to the dominant declared company of its non-shared registrable domain;
otherwise to that domain. A shared/free-mail domain with no declaration is
not an organization and is skipped. A domain naming several unrelated
declared companies (at least four over at least five declared addresses) is
treated as shared even when the learned shared-domain setting has not
caught it; exact declarations still resolve, but its dominant company never
labels every undeclared address. Owner domains and companies are internal
and never ranked. Messages are counted once per organization, so one sent
mail addressed to three people at one company counts once. The Companies
explorer ranks organizations with at least one message in either direction:
a company that only ever wrote to the owner remains visible, and its profile
states that direction instead of hiding it. `metric=exchanges` (the Ask
company intent) still counts only two-way organizations.

A domain organization with no declared company is labeled from local
evidence: the business words and person names the mailbox already shows
(`climatisation-exemple.test` becomes `Climatisation Exemple`), never
the raw domain. An unrecognized brand stays one title-cased word
(`messagerie-exemple.test` becomes `Messagerie Exemple`); no web lookup is made.

`GET /api/companies/profiles?key=c%3Aacme&key=d%3Aacme.ca&months=12&event_days=30`
returns up to 16 profiles in one store batch. It is the Companies explorer's
visible-row prefetch path, so one mailbox scan warms every profile on screen.
Keys are repeated `key` parameters (a company key may itself contain a comma),
deduplicated and validated like `{key}`; a key that does not resolve fails the
batch instead of disappearing. The response is
`{"generated_at": "...", "profiles": {"<key>": <CompanyProfileWire>}}`.

`GET /api/companies/{key}` returns the organization's declared directory
entry, its people (messages, sent, received, last exchange, the
address-book phone when the source reports one, and a locally extracted
signature name, phone or title when the address book has none), totals,
zero-filled monthly history, recent owner-visible messages, open
conversations with the same thread truth as priority/agenda, active
calendar events and due commitments. `key` is `c:<folded company>` for a
declared company (merged across its domains) or `d:<registrable domain>`,
URL-encoded. A `d:` key with no contact and no observed mail is
`404 unknown_company`; a contact-only domain still returns its identity
with zero mail.

The profile also carries the evidence the Companies screen states:

- `generated_at` is when the view was built.
- `last_inbound_at` and `last_outbound_at` are the newest received and sent
  messages; either is omitted when that direction has no mail.
- `last_90_days` is `{messages, threads, active_days}` over the 90 days
  ending at the profile's build instant. Totals (`messages`, `sent`,
  `received`, `threads`) are all time.
- `recent_total`, `events_total` and `deadlines_total` are the full list
  sizes before the display caps. The arrays are capped at 100 people, 20
  recent messages, 20 open threads, 10 events and 20 deadlines, so a
  consumer must state truncation instead of implying the list is complete.
- `thread_states` always carries all six states. `open_threads` includes
  `reason`, the resolver's evidence, `messages`, the conversation's visible
  message count, and `actor` only when the other side moved last; an
  `awaiting_other` thread never presents the owner as the counterpart. The
  row links through its owner-visible `message_id`; the conversation id
  stays service-side.
- `events` contains only active occurrences (not canceled, declined or
  free) whose organizer or attendee resolves to this organization, with
  `attendee_count` instead of the attendee array. An address that resolves
  to another declared company never matches by domain. `event_days` bounds
  the forward window (1..365, default 30).
- `deadlines` carries `message_id`, `subject`, `sender_name` and `due`.
- Book-wide address-book coverage is not repeated in the profile; the list
  endpoint carries it once.

Open-thread `message_id` is always an owner-visible message: a
shared/delegate reply can set the state but is never a link target. Arrays
are always arrays; bodies and HTML are never returned. A profile may read
the bodies of at most twelve of its newest received messages locally to
fill an empty name, phone or title by sender; the evidence block is the
signature when one was found, otherwise the last few non-blank lines, and a
minimal `De:` + `Envoyé:` quote is cut before extraction. It never returns
the evidence text.

The response is a wire projection, not the world service value: `key`,
`addresses`, `addresses_total`, `open_threads_total`, conversation ids,
deadline participants and the calendar resolution metadata (`source_id`,
`store_key`, `entry_id`, global ids, busy/meeting status, series fields,
`body_preview`, `last_modified`) are omitted. The service keeps them for
Q&A and the thread resolver. A future consumer that needs one of those
fields must extend the wire projection rather than read around it.

### Conversations

`GET /api/threads/{conversation_id}` returns one conversation as the owner
may see it: metadata and a bounded plain-text snippet per message, never a
body or HTML. Drafts, deleted, junk and shared/delegate messages are
excluded from `messages` and `participants`; a shared reply affects only
`state` and its `visibility`, never the content. `state.actor` names that
actor only when the move is owner-facing: it is empty when the last move
came from shared/delegate mail, like the conversation list.

`messages` is chronological (oldest first). The default page ends at the
latest message. `before=<id>` returns the page older than that message
(exclusive). `at=<id>` ends the page at that message inclusively, so
opening an old message in a long conversation shows its own earlier
context rather than newer mail. Both are row ids but the filter follows
the mail instant (a source may sync newest first, giving older mail larger
ids). `truncated` reports whether older messages remain. `count`,
`first_at` and `last_at` cover the whole conversation, not the page.
`newest_message_id` is the owner-visible newest message of the whole
conversation, even on an anchored page; `newer` counts messages after an
inclusive anchor (zero for a latest page or an exclusive older page).
`state` is the same resolved thread truth priority and agenda use, and
carries the last substantive move's `topic`/`subtopic` (the newest message
with a classifier subject when a shared reply has none). The body is never
included.

### Conversation digests

`GET /api/conversations` is the thread-first mailbox: one row per
conversation, not one per message. The latest row is chosen by mail
instant (not row id), and the conversation's owner-visible messages are
counted once. Drafts, deleted, junk and shared/delegate content never
enter a row; a shared reply can still set the resolved state. Each row
carries the latest subject/sender, message/sent/received/unread counts,
unread and attachment flags, an open due date, a bounded snippet, up to
three external participants with the total count, the resolved
organization, and the same thread state priority and agenda show. The
state also carries the conversation's `topic`/`subtopic`, so the row can
show the subject path without a second read.

`counts` covers the whole search-matched set for all six states before the
state filter. `total` is the state-filtered count. `next_before` is an
opaque cursor that binds the query and state; a mismatched cursor is
`400 invalid_cursor`. `coverage` reports owner-visible messages,
conversations and unthreaded messages (which are counted but not listed).
Arrays are always arrays. Pages are enriched only for the returned rows,
with one participant, one snippet and one CompanyBook read.

### Commitments

`GET /api/commitments` is the deterministic open-loop read model behind the
Brief: due follow-ups (`due`), conversations still owing the owner a reply
(`reply`), mail the ranker flags as asking for a decision (`decision`) and
other concrete owner actions (`action`: pay, send, schedule, do, verify). It merges stored evidence only and never extracts a promise from
message text. Items dedupe to one row per conversation (one message when
no conversation exists); the strongest kind wins (`decision` > `action` >
`reply` > `due`) and the other evidence, the earliest due date and the
subject/action dimensions are carried over. Order is overdue first, then
by due/last instant, kind and id. `counts` covers the whole deduped set
before filtering, with every kind, direction and status key present.

```json
{"items":[{"id":"…","kind":"action","direction":"owner","status":"open","title":"…","subject":"…","due":"2026-11-03T09:00:00Z","conversation_id":"…","message_id":42,"organization_key":"c:…","organization_label":"…","person":"…","evidence":[42],"action":"pay","waiting":"owner","due_source":"classifier","due_level":4,"topic":"billing_account","subtopic":"payment"}],"counts":{"due":1,"reply":0,"decision":0,"action":1,"owner":2,"other":0,"open":1,"overdue":1,"done":0,"unknown":0},"generated_at":"2026-10-27T08:00:00Z"}
```

`kind` is `due`, `reply`, `decision` or `action`; `direction` is `owner` or
`other`;
`status` is `open`, `overdue`, `done` or `unknown`. `due` and
`conversation_id` are omitted when the evidence has none. `items` is always
an array and `counts` always an object. `limit` is 1…500 (default 100); an
unknown filter value is `400 invalid_kind`, `invalid_direction` or
`invalid_status`, and an out-of-range or non-numeric page size is
`400 invalid_limit`.

### Brief

`GET /api/brief?since=` composes the deterministic Home Brief from read
models the app already built:

- `must` and `waiting` read `GET /api/commitments`' owner-other split. They
  keep the commitment service's order (overdue first, then its due/last
  instant, kind and id), omit completed items, and cap the rows at 12 and
  8. `count` is the direction total before that display cap.
- `changed` reads the cached conversation digest: conversations whose
  latest message is strictly newer than `since`. `count` is every matching
  conversation before the 12-row display cap; `since` echoes the effective
  boundary. Undated conversations are excluded while a boundary is set and
  sort last without one. Each item carries the resolved
  `state`/`state_reason` and the latest message id as its evidence.
- `relationships` reads the same digest and the one CompanyBook resolver.
  It counts conversations, messages and open threads per external
  organization, tracks the latest received counterparty even when the
  owner replied last, and keeps only organizations with at least one open
  conversation. It returns at most 8 rows; `count` is every open
  organization before the cap. Internal colleagues and unresolved
  shared-domain senders are never organizations.
- `topics` is a whole-mailbox census of the classifier's `topic` answer,
  not a sample of the ranked list. `count` is every owner-facing message
  carrying a topic; `total` is the owner-facing message count of the
  mailbox, so the two disclose classification coverage. `items` is the
  top 12 topics by count, each with up to three recent message ids as
  evidence and a nested `subtopics` breakdown (identifier, count, up to
  three evidence ids; most frequent first). `title` is the profile's
  topic option (the default profile covers project work, support, sales,
  billing/account, meetings, technical integration, hiring, partnership,
  internal operations, news and other); the UI localizes it. If the
  priority service is unavailable the section is empty and the rest of
  the Brief still renders.
- `must` and `waiting` rows carry the commitment's `action`, `waiting`,
  `due_source`, `due_level`, `topic` and `subtopic` beside the existing
  title, organization, person, due, status and evidence fields, so the
  screen never re-derives the action.

There is no language model, no new mailbox scan and no invented fact: every
row is a stored or already-computed observation, and every evidence array
lists the message ids behind it. `since` defaults to the start of the
current UTC day minus seven days, so the default keeps one stable cache
key; an unparseable or future value is `400 invalid_since`. A fresh Brief
is served directly, and an expired one is served immediately while one
background composition refreshes it.

```json
{"generated_at":"2026-10-27T12:00:00Z",
 "must":{"count":2,"items":[{"id":"due-1","kind":"due","title":"Invoice",
   "subject":"Invoice","organization":{"key":"c:atelier nord","label":"Atelier Nord"},
   "person":"alice@atelier-nord.test","due":"2026-11-01T09:00:00Z","status":"overdue",
   "direction":"owner","conversation_id":"C1","message_id":11,"evidence":[11]}],
   "suggested":false},
 "waiting":{"count":1,"items":[{"id":"reply-1","kind":"reply","title":"Budget",
   "subject":"Budget","person":"bob@example.test","status":"open","direction":"other",
   "conversation_id":"C2","message_id":12,"evidence":[12],
   "last_at":"2026-10-27T07:00:00Z"}],"suggested":false},
 "changed":{"count":1,"items":[{"id":"activity:C3","kind":"activity","title":"Latest",
   "subject":"Latest","conversation_id":"C3","message_id":13,"evidence":[13],
   "last_at":"2026-10-27T10:00:00Z","state":"awaiting_owner",
   "state_reason":"inbound_last"}],"since":"2026-10-20T12:00:00Z","suggested":false},
 "relationships":{"count":1,"items":[{"id":"organization:c:atelier nord",
   "kind":"organization","title":"Atelier Nord","subject":"Latest",
   "organization":{"key":"c:atelier nord","label":"Atelier Nord"},"message_id":13,
   "evidence":[13],"last_at":"2026-10-27T10:00:00Z","conversations":3,
   "messages":12,"awaiting_owner":2,"awaiting_other":1,"count":3}],
   "suggested":false},
 "topics":{"count":87,"total":120,"items":[{"id":"topic:project_work","kind":"topic",
   "title":"project_work","evidence":[13,12,11],"count":54,"suggested":true,
   "subtopics":[{"id":"subtopic:project_work/status_update","kind":"subtopic",
     "title":"status_update","topic":"project_work","subtopic":"status_update",
     "evidence":[13,12],"count":40,"suggested":true},
     {"id":"subtopic:project_work/approval","kind":"subtopic","title":"approval",
     "topic":"project_work","subtopic":"approval","evidence":[11],"count":14,
     "suggested":true}]},
   {"id":"topic:billing_account","kind":"topic","title":"billing_account",
   "evidence":[9],"count":33,"suggested":true,"subtopics":[
     {"id":"subtopic:billing_account/payment","kind":"subtopic","title":"payment",
      "topic":"billing_account","subtopic":"payment","evidence":[9],"count":33,
      "suggested":true}]}],"suggested":true,
   "scope":"owner_mailbox"}}
```

This is an honest working set, not a mailbox census: the changed window is
bounded by `since`, relationships show only the top organizations, and the
commitment sections read the service's 500-item scan. The topics section is
the exception: it counts every owner-facing message that carries a topic,
and `topics.total` discloses the whole owner-facing mailbox so the UI can
show coverage. `items` is always an array, every section is always present,
and `topics.count` exceeds `len(topics.items)` when the display cap
applies. A message without a usable topic answer (unjudged, missing or
abstained) is not assigned to a topic.

### Ask and history

`POST /api/ask` answers one natural-language question. It is gated,
owner-only for every retrieval, and persists every successful answer
locally before returning it. The handler is a long route (a written answer
may use up to `qa.call_timeout`).

```json
{"question":"Qu'est-ce qui est urgent ?","language":"fr"}
```

`language` is `en` or `fr`; empty means `fr`. The question is trimmed and
must be 3…500 characters. A missing question/language is
`400 invalid_question`/`400 invalid_language`; `qa.enabled: false` is
`503 qa_disabled`.

```json
{"id":7,"question":"…","answer":"…","mode":"structured","intent":"urgent",
 "counts":{"reply":11246,"urgent":1916,"security":373,"decision":6,
 "deadline":10,"workload":56,"total":20211},
 "citations":[{"message_id":1,"subject":"…","sender_name":"…",
 "received_at":"2026-10-25T10:00:00Z","reason":"urgent"}],
 "provider":"","model":"","took_ms":4,"preview":true,
 "created_at":"2026-10-26T12:00:00Z"}
```

Modes are explicit:

- `structured`: a deterministic intent answered from the priority ranking
  or owner-only search. `answer` is empty; the UI composes the localized
  sentence from `intent` and `counts`. No provider call and no cost.
  Intents: `reply`, `urgent`, `deadline`, `security`, `decision`,
  `workload`, `priority`, `summary_today`, and `sender` (the text after an
  explicit sender/from/expéditeur prefix; owner-only search, not the
  generator). A sender search waits for complete retrieval
  (`qa.retrieval_timeout`); when the index is warming or the bound expires
  with no hit, the result is `retrieval` with `qa_index_warming` or
  `qa_retrieval_timeout`, never a false "no message from this sender".
  `counts` carries the priority filter totals plus `total`. With
  `qa.provider` empty, a plain count using the declared count vocabulary
  (`count`/`counts`/`nombre`/`total`/`combien`/`amount`/`quantity`/
  `quantité`, the phrases `how many`/`how much`/`number of`/`nombre de`/
  `amount of`/`quantity of`/`quantité de`, and bare `number` immediately
  before a category or mailbox noun) with exactly one fallback category
  (`security`, `decision`, `deadline`, `urgent`, `reply` or `workload`) and
  no scoped wording ("How many deadlines do I have?") is answered here from
  the priority totals (a zero total is a real zero with no citation). The
  scope gate is fail-closed: after removing the count cue and the one
  category word, every remaining token must be a known filler, question or
  mailbox noun; any unlisted scope, topic, status or date word keeps the
  question free-form. The gate runs before the intent switch for every
  request with a recognized count cue, not only "how many" wording, so
  "Number of…", "Total…", "Amount of…", "Quantity of…", "Quantité…",
  bare "Number deadlines" and French "Combien…" cannot parse directly to a
  category and bypass it. The fail-closed scope and person guarantees apply
  to every recognized cue. For a recognized cue, a company- or
  person-scoped count also stays free-form and needs a generator when the
  profile cannot answer it; a failed person or company lookup stays
  free-form rather than widening to the mailbox total. For recognized cues,
  third-person and person-frame counts ("does she have", "does An have", "do
  they have") stay free-form: the allowlist has no third-person pronouns or
  `an`, and a `does`/`did` subject is rejected unless it is first/second
  person or a mailbox noun. A category noun in the subject position is a
  person frame, not a category: "How many deadlines does Deadline have?" and
  "How many urgent messages does Urgent have?" stay free-form. A repeated
  cue or category noun before `has`/`have` is also a person frame ("Amount of
  deadlines Amount has?"), while first/second-person forms keep the plain
  path. A bare repeated category noun without an auxiliary ("How many
  deadlines Deadline?") is kept free-form too. The freeform resolver for a
  recognized count uses the same filtered
  mentions, so a cue word cannot select a sender by substring; a category
  noun in the person-subject position remains a mention, so a contact named
  Deadline or Urgent can still scope the follow-up. The fallback is
  deliberately conservative and does not claim complete name or pronoun
  coverage: any unrecognized scope or person token in a recognized count
  stays free-form.
  There is no priority fallback: a "priority" count is not a real filter and
  is not
  answered from `total`; a count whose wording parses directly as `priority`
  (for example "Combien de messages importants ?") is kept free-form for the
  same reason.
- `generated`: a written answer for a free-form question. A question about
  the act recorded inside a message — "Which deadlines did I approve?",
  "Quelles échéances ai-je signées ?" — is free-form even when it contains a
  word such as `deadline` or `decision`: a count or list of subjects cannot
  state the act. An explicit aggregate request ("Which emails did I approve?",
  "Combien d'échéances ai-je approuvées ?") keeps its deterministic intent.
  Advice questions such as "Should I pay this invoice?" are free-form; a
  necessity question such as "Must I approve this invoice?" keeps the decision
  intent. Before the planner runs, the service resolves the person or organization
  the question names from visible owner mail participants and the picked
  address books, plus the organization book's labels/domains. This is
  deterministic and local: one bounded candidate query, no provider call. A
  term matches one whole name or address token, never an inner fragment, so a
  question word such as "liste" cannot select `listening@…`. Zero candidates
  leave the general path unchanged; one candidate becomes a `Resolved entity`
  block in the prompt (exact addresses for a person, label/domains for a
  company); several exact candidates in a singular question return `clarify`
  instead (below). A plural role noun ("clients", "contacts", "fournisseurs")
  is a set: it is never reduced to one person, and the planner answers it with
  a topic ranking. The service then
  retrieves at most `qa.max_messages` owner messages, numbers them, and
  sends one evidence bundle to `qa.provider`: direction, subject, sender,
  date, unread state, importance, attachments, To/Cc/Bcc, the conversation's
  resolved thread state (state, actor, message count, visibility), learned
  labels with their `manual`/`judge` provenance, priority signals and a
  600-character plain body. One or two search hits are topped up with the
  three best priority items, deduplicated; zero hits never are. A question
  that names one known person also carries the newest received messages from
  every address that writes under that display name, so their other
  organizations are evidence rather than a missing address. A question that
  names one known organization is scoped to that organization's domains and
  also carries its profile facts (volume, people, open thread states, events
  and deadlines) and the bounded first/last page of its five most recent
  conversations. The model must cite `[n]` for every claim and return strict
  JSON `{"answer":"…","used":[1,2]}`. Cited indices are validated. A
  plain-text answer that does not look like JSON is shown only when it can
  be grounded in the numbered messages; malformed JSON fails the contract
  and falls back to retrieval. `provider` and `model` name the generator
  used. After grounding, the answer's language is checked once against the
  requested language: quoted reply lines are stripped first, and an answer
  with fewer than `qa.answer_language.min_letters` letters stays unjudged. A
  confident mismatch keeps the answer, citations and provider/model exactly
  as written and sets `notice: "answer_language"`; the check never retries,
  rewrites, translates or falls back to retrieval. A noticed answer is saved
  to history but not cached, so asking the same question again calls the
  provider again.
- **Entity resolution and clarify.** A substantial name token, explicit
  email address or explicit domain is resolved against the same owner-facing
  scope as every other read (owner identities, shared/delegate stores,
  drafts, deleted and junk excluded). A person candidate is a display name
  with one or more exact normalized addresses, message count, last activity
  and any declared company; same-name addresses merge only when an
  address-book entry or one registrable domain proves the identity, so two
  people who share a first name (or a full name on unrelated domains) stay
  separate. A company candidate is a distinct organization-book key with its
  label and domains. Two or more distinct candidates in a **singular**
  question return `mode: "clarify"` with
  `clarify: {"question":"…","options":["…"],"candidates":[…]}` and no
  provider call, plan or cached answer; `candidates` is optional. Each
  candidate carries `kind` (`person`, `company` or `all`), `label`, `address`
  or `domain`, `messages`, `last_at` and `question`; `question` is always one
  of `options` and is what a click submits, so the screen shows the identity
  and count instead of repeating the prompt. A plural question does not
  clarify: it is answered as a set. The question is localized
  (`Which client do you mean?` / `De quel client parlez-vous ?`,
  `Which person…` / `De quelle personne…`) and options are complete
  follow-up questions built from the original question plus a stable visible
  label: `… — Atelier Nord (atelier-nord.test)` or
  `… — Nadia Tremblay <nadia@atelier-nord.test>`. At most six options are shown,
  ordered by match quality then activity. Clicking one sends an ordinary
  question containing the exact email address or domain, which selects that
  candidate instead of asking again. No opaque candidate id exists in the
  contract.
- **Topic ranking.** A list or ranking of several people filtered by a
  subject ("clients who mentioned AI", "fournisseurs qui ont parlé de X")
  resolves to `kind: "contacts"`, `action: "rank"` and `text` set to the
  topic. One owner-only retrieval runs over the receiving folders, bounded by
  `search.max_hits`; the hits are grouped by exact sender address and ranked
  by matching message count, then recency. The result is `mode: "computed"`
  with one row per contact (`label`, `address`, `messages`, `last_at`) and one
  representative citation per row. `result.total` is the matching message
  count; `result.capped` says the retrieval pool was bounded. The model never
  writes the list, and the hit set is not stored in the plan cache. A
  sent-only request stays on the lexical executor; company rankings keep the
  company executor, which owns domain aggregation.
- **Latest-exchange summary.** A question that asks to summarize/résumer/
  synthèse/récap a latest exchange, conversation or thread and resolves to
  exactly one entity takes a deterministic summary path. For a person the
  newest owner-visible conversation containing one of the exact addresses is
  selected; for a company the profile's newest recent conversation is used.
  At most 20 thread messages are read through `ThreadMessages` and their
  owner-safe bodies/details are numbered for a dedicated summary prompt
  (only those messages, at most 120 words, subject/who said what/decisions/
  open items/next action, `[n]` on every claim, never a subject list). The
  result is `mode: "generated"` with the newest thread messages as citations,
  never `computed` and never the 902-message count. A missing generator keeps
  the exact-thread citations with `qa_unavailable`; a failed or ungrounded
  call uses `qa_generation_failed` / `qa_no_grounded_answer`; no conversation
  returns `no_matches` with no citations. When no entity resolves, the
  general planner may answer, but a returned `messages` plan for a summary
  request is rejected and falls back to retrieval, so "N messages
  correspondants" is never shown as a summary.
- **Plans and caching.** A free-form question may resolve to a typed plan
  (`computed`), a grounded answer, or one clarification. When a person is
  resolved, a plan that names that person (or whose question is about that
  person) is rewritten before execution: `contact` is replaced by exact
  normalized `contact_addresses` (`IN (…)` over sender and To/Cc), so a
  first-name substring can never match unrelated mail. `contact_addresses`
  holds at most 20 addresses of at most 320 characters each, validated at
  the plan boundary; an empty list means no exact scope, and a malformed
  address is rejected. Plans and answers are cached under the selected
  record's fingerprint and a planner version, so changing the model/endpoint
  or the planner contract never reuses an old decision; the rewritten
  `contact_addresses` are part of the cached plan, and the existing question
  key remains the only invalidation. A plan whose message list matches
  nothing falls through to fresh retrieval instead of freezing a wrong
  no-match. A semantic search that could not run the whole mailbox is
  reported as `qa_index_warming` when the index is genuinely incomplete or
  `qa_retrieval_timeout` when the bound expired, never as an empty mailbox or
  a provider problem; neither a no-match nor a degraded answer is cached. A
  latest-message question about a person resolves to the newest message that
  person sent (direction `received`), never to the owner's later reply; when
  that message is in the numbered evidence the answer is written from it,
  otherwise the computed list names it.
- `clarify`: the request names two or more distinct people or organizations
  and the answer depends on which one. `answer` repeats the localized
  clarification question, `clarify.question` holds it and
  `clarify.options` holds 2…6 complete follow-up questions. An optional
  `clarify.candidates` array carries the same choices as structured data:
  `{"kind":"person"|"company"|"all","label":"…","address":"…","domain":"…",
  "messages":N,"last_at":"…","question":"…"}`, where `address` is a person's
  exact normalized email, `domain` is an organization's domain, and
  `question` is exactly one of `options` — the string the UI submits when
  that candidate is clicked. The visible card shows only the label, the
  address or domain and the message count. No provider call is made, no plan
  is executed or cached, and the ambiguous turn is not stored as an answer
  cache entry.
- `computed`: a typed plan, the agenda, or one organization's profile.
  `plan` and `result` (or `plan` and `agenda`) carry the executed query;
  the `agenda` block is the same wire projection as `GET /api/agenda`, so
  both screens read one contract. The company answer is composed by the
  server from the profile's stored counts. No number or name is
  model-authored.
- **Company questions.** A free-form question that names exactly one known
  organization **and asks about the organization itself** — its message or
  people volume, its conversations, what it is waiting on, its last
  contact, its commitments or events — is answered with `mode: "computed"`
  and `intent: "company"` from the cached profile, deterministically and
  instantly, with no search and no provider call. A question that merely
  names the organization as the scope of a specific fact ("who is the
  laptop for?") is not a profile question: it goes to `generated` (or
  `retrieval`) scoped to that organization's mail. The guard is a closed
  vocabulary of the profile's own dimensions plus the question frame; any
  unexplained content word sends the question to retrieval, never to a
  generic profile answer.
- `retrieval`: no written answer. `answer` is empty, `notice` names the
  cause, and the matching sources stay cited unless the search itself found
  nothing. `provider` and `model` are set only when a configured provider
  was selected for the answer, even if the call was refused or failed. No
  answer is invented in any case.

  | `notice` | When it occurs | `provider` / `model` | Citations |
  | --- | --- | --- | --- |
  | `qa_index_warming` | The semantic index is not ready and full-text found no hit. Transient: the answer is not cached, so the next ask retries. | empty | none |
  | `qa_retrieval_timeout` | The waiting retrieval missed `qa.retrieval_timeout`, or a search returned a deadline. Transient: the answer is not cached or persisted. | empty | none |
  | `qa_generation_failed` | A configured provider was selected, but the call was refused or failed (budget, timeout, provider error) or its output did not satisfy the planner contract. | the selected provider and model when known | the retrieved sources |
  | `qa_no_grounded_answer` | A provider response was produced, but no usable answer or citation survived grounding in the numbered messages. | the provider and model that answered | the retrieved sources |
  | `qa_unavailable` | No answer generator is configured: `qa.provider` is empty, its record or key is missing/unusable, or `Generator()` returns `ErrUnavailable`. This is the only notice that offers the provider-settings action. | empty | the retrieved sources |
  | `no_matches` | The mailbox has no evidence for the question; no priority item is substituted. | empty | none |
  | `company_profile_unavailable` | The named organization's profile could not be read (computed mode); the shortcut never substitutes unrelated mail. | empty | none |
  | `answer_language` | A grounded generated answer is confidently in another language than the requested `en`/`fr`. Quoted reply lines are stripped before detection; fewer than `qa.answer_language.min_letters` letters stays unjudged. The answer, citations and provider/model are unchanged, and it is saved to history but not cached. | the provider and model that wrote it | the grounded citations |

  `answer_language` is the only notice that accompanies a written answer; it
  never retries, rewrites, translates or substitutes retrieved sources. The
  other notices belong to a retrieval fallback.

  When there are no retrieved messages, a missing provider still returns
  `no_matches` rather than `qa_unavailable`: the mailbox, not the provider,
  is the missing evidence. A free-form question with zero owner search hits
  merges no priority items; when a configured provider is asked with no
  evidence and returns no citation, the result is also `no_matches`.

`preview` is the effective preview rule: `learning.test_mode` is on and the
real gate is closed. It describes the classifier's trust state, not the
company, retrieval or generated answer: those read stored mail or the
organization profile and disclose their citations directly.

Free-form questions never search shared or unattributed mail: the Q&A
retrieval sets the owner-only search filter internally (`/api/search`
keeps its shared-inclusive default). The prompt and the messages are sent
to the configured external provider, so a written answer leaves the
machine; structured answers never call it, and a retrieval fallback makes
no further call even when the provider it replaced did. Questions,
answers, snippets and addresses are never logged. The call is bounded by
`qa.call_timeout` (30 s default) and a per-question budget of
`qa.budget.max_calls` (1 default) and `qa.budget.max_usd` (0.05 default);
usage is recorded through the ordinary provider counters.

`GET /api/ask/history?limit=20` returns the newest persisted answers
(`1…100`, default 20) as `{"items":[AskView]}`, also gated. The history
stores only question, answer, mode, intent, counts, provider/model,
language, preview, conversation id, notice and each citation's message id
and reason; subject, sender and received instant are rehydrated from
visible owner mail on read. A cited message that is missing, shared,
unpicked or removed keeps its id with empty subject/sender/date. A
retrieval row with no stored notice and no citations is read with
`notice: "no_matches"`; one with citations and no provider keeps
`qa_unavailable`; one with citations and a provider/model is read as
`qa_generation_failed`. Rows written before the notice split that stored
`qa_unavailable` are read as `qa_generation_failed` when they carry a
provider/model, as `qa_index_warming` when they carry neither a provider
nor citations, and as `qa_unavailable` otherwise. The newest 100 answers
are kept; `DELETE /api/ask/history` empties the table (`204`). Both history
reads and the delete are gated and never touch the provider.

Each `AskView` carries the `conversation_id` of its turn. A follow-up
sends the same id: the planner receives the newest six turns of that
conversation as chat context, so a short follow-up resolves against the
previous question. The prior turns are context only; every factual claim
must still be grounded in and cite the numbered owner messages, and a
turn from another conversation is never read. The first turn of a new
thread has no prior context, so it may reuse the context-free single-turn
plan/answer cache; every cacheable continuation is cached under its
conversation id and newest prior turn id, so a cached answer never crosses
threads. An `answer_language` notice, a retrieval fallback and the other
degraded answers are not cached.

### Mail list

`GET /api/messages` is the gated owner all-mail page. It reads every picked,
active store through the same owner-content boundary as the conversation
and priority views: shared, delegate and public stores are excluded, and a
message must carry the owner scope verdict. Visibility is not re-implemented
in the handler.

The service caches exactly one shape: the unfiltered all-mail first page
(`role` absent or `all`, no `store`, no `sender`, no `before`, `limit` absent
or 100, and `counts` on). The startup warm builds that page once; a concurrent
first request joins the same generation's build, and later requests read it
while `store.MailGeneration` is unchanged. There is at most one in-flight
build per generation: a mail write that advances the generation may start a
new build while the previous one is still finishing, and the older result is
not cached under the new generation. A label click does not invalidate it; a
mail, folder, selection or scope write does. A failed build is shared with
the callers waiting on it but not cached, so the next request retries. Every
other page (role, store, sender, cursor, another limit, no counts) bypasses
the cache and reads the store. The handler and the API view are unchanged;
the cache returns independent copies and callers cannot mutate it.

| Query | Meaning |
| --- | --- |
| `role` | One primary folder role (`inbox`, `sent`, `drafts`, `deleted`, `junk`, `archive`, `other`) or `all`/absent. A role page matches the message's primary folder only; its further folders (Gmail labels) are not counted in role pages for v1. `all` includes drafts, deleted and junk, and a role page shows them when that role is selected. |
| `store` | One mailbox store key; absent means every picked store. |
| `sender` | One exact address (`demo@example.test`) or one domain (`@example.org`, or `example.org` without an `@`). Matching is case-insensitive by Unicode lower-casing (so a non-ASCII local part matches its own case) and never a substring or a display name: a domain matches every address under that one domain, not its subdomains. An empty or all-space value, a lone `@`, any control character (checked after trimming) or more than 320 runes is `400 invalid_sender`. The value is trimmed and lower-cased before it is compared and carried in the cursor. |
| `before` | Opaque keyset cursor from the previous response. It is bound to the normalized role, store and sender it was issued for; reusing it under another role/store/sender is `400 invalid_cursor`. |
| `limit` | 1…200, default 100. |
| `counts` | `1` (or `true`) adds `counts`; `0`/`false`/absent omits it. |

Order is `instant DESC, id DESC`, where `instant` is `mail.Instant`: a sent
item when it left (`sent_on` when present, else `received_at`), any other
message when it arrived. A sent item's `received_at` and `sent_on` stay
visible separately. The row id breaks equal instants, so a page boundary is
total and stable. The store fetches one extra row to set `has_more` and only
then returns `next_before`; there is no offset.

`counts` groups the same owner-visible, store- and sender-filtered
population by primary role and carries every role, zero-filled. It ignores
`role` and the cursor on purpose: the tabs show the whole (filtered)
mailbox, not the current page. `total` is a separate count under the role,
store, sender and visibility predicate (without the cursor). Both are live at
query time: keyset paging is not a transaction snapshot, so mail inserted
above a cursor does not appear in older pages. The counts are memoized per
mailbox generation, store and sender; a mail write bumps the generation and
forces the next read to recompute.

Rows carry `row_id`, `subject`, `sender_name`, `sender_address`,
`sent_representing_address`, `instant`, `received_at`, `sent_on`, `role`,
`folder_path`, `store_key`, `store_name`, `is_read`, `has_attachments`, a
bounded collapsed plain-text `snippet` (at most 200 runes). They never
carry `recipients`, `body`, `body_html`, transport headers or attachments;
the single-message endpoint owns the recipient list. `messages` is always
an array (never null).

```json
GET /api/messages?role=inbox&limit=2&counts=1
{"messages":[{"row_id":42,"subject":"…","sender_name":"…","sender_address":"…",
  "sent_representing_address":"…","instant":"2026-10-12T15:48:34Z",
  "received_at":"2026-10-12T15:48:34Z","sent_on":"2026-10-12T15:48:25Z",
  "role":"inbox","folder_path":"Inbox","store_key":"store-…","store_name":"…",
  "is_read":true,"has_attachments":false,"snippet":"…"}],
 "total":1,"counts":{"inbox":1,"sent":0,"drafts":0,"deleted":0,"junk":0,
 "archive":0,"other":0},"next_before":"","has_more":false}
```

### Search

`GET /api/search` is gated hybrid retrieval. `q` is required (trimmed-empty
is `400 empty_query`). The query is read in its folded form (case,
diacritics and compatibility forms ignored). ASCII double quotes delimit a
phrase: `"bonjour merci"` matches the two words adjacent and in that order,
while unquoted `bonjour merci` ANDs the two words. A quote that is never
closed runs to the end as a phrase; a query with nothing searchable after
folding and parsing (quotes only, NUL or another control character only, a
zero-width space only) is `400 empty_query`, never a server error. Operators
inside or outside a phrase are text, not FTS syntax. A phrase in a
substring-searched script runs on the trigram index; that set is the
spaceless scripts plus Hangul and the matra-based Indic scripts
(Devanagari, Bengali, Gurmukhi, Gujarati, Oriya, Tamil, Telugu, Kannada,
Malayalam, Sinhala). A quoted phrase on the trigram index shorter than
three runes is matched by the disclosed scan, while a short Latin phrase
stays a word-index phrase (`"ab"` is `word`, not `scan`). A two-rune Indic
term such as `कि` is therefore `scan`, not `substring`: it is an exact
substring of the folded text, not a trigram. A term of three runes or more
is `substring`; `कमल` remains a substring of `कमला` (a documented
residual). A mixed query with any substring-searched term moves every term
to the trigram index (the whole query is planned on that table): a Latin
term of three or more runes can therefore match as a substring — `com`
matches `communication` — and a shorter term is `scan`.
The additive filters map to the one `store.SearchFilter` both rankings share:

- `scope=all` (default) keeps today's shared-inclusive search;
  `scope=owner` keeps owner-facing content only (owner verdict, a store kind
  other than shared/delegate/public, and no drafts/deleted/junk).
- `limit` 1…50 (default `search.limit`), `offset` 0…1000. Offset is applied
  after reciprocal-rank fusion and triage filtering, before hydration;
  `limit` applies after the offset.
- `store` one mailbox store key, `role` one valid mail folder role, `sender`
  an address or a domain (the one exact-address / domain-suffix rule the
  All-mail page shares: `mail.NormalizeSenderQuery` owns the validation and
  normalization, `store.senderWhere` the predicate, and the comparison uses
  the `mailsense_lower` scalar so Unicode lower-casing is Go's, not SQLite's
  ASCII-only `lower()`), `attach=any|yes|no`. An empty or all-space value, a
  lone `@`, any control character (checked after trimming) or more than 320 runes is `400
  invalid_sender`.
- `semantic=on` (default) ranks both ways; `semantic=off` skips the embedder
  entirely and returns the same full-text ranking with `semantic.used=false`
  and a notice saying the semantic ranking was not requested. The omnibox
  uses it for suggestions; the Search page keeps the default. Any other
  value is `400 invalid_semantic`.
- An omitted filter is unchanged from the old q-only request.

`search.Result` keeps every old field and adds `total` and `capped`. `total`
counts every fused, triage-filtered candidate before `offset`/`limit`; it is
a lower bound when `capped` is true. `capped` is true when a ranking returned
`search.max_hits` rows or the full-text count reached `search.count_cap`.

`snippet` is a plain-text excerpt of `search.snippet_chars` code points
(plus up to two ellipses), with whitespace collapsed and no highlight
markup. For a full-text hit (`text_rank` ≥ 1) it is cut around the first
body match: word-mode terms match whole unicode61 tokens (letters, numbers,
private-use; marks are separators), so a quoted phrase, `mailer-daemon` or
`com_value` must match adjacent tokens; substring/scan terms match as
substrings. A full-text hit whose match is only in the subject, sender or
recipients has an empty `snippet`; the subject/sender fields still carry the
match. A semantic-only hit (`text_rank` absent or 0) has no matched excerpt:
its `snippet` is the cleaned beginning of the body, a preview. The excerpt
is never synthesized from the subject or sender and never contains HTML.

The semantic side never blocks past `search.embed_timeout`. A cold vector
refresh that outlives the budget is cancelled; the request answers from full
text with `semantic.used: false`, an explicit `semantic.reason` naming the
budget and the usual full-text notice. A semantic result that arrives within
the budget is merged exactly as before.

`internal/search` keeps one bounded query-vector cache (64 entries per
process), keyed by the vector space plus the folded query text. Repeated
searches of the same text share one embedder call, concurrent identical
searches are single-flight, and an evicted or failed embedding is retried on
its next use. The cache is memory-only; vectors are never written to disk.

### Message display

`GET /api/messages/{id}` returns a `MessageView` that keeps the stored
message and adds the display body:

| Field | Meaning |
| --- | --- |
| `message.body` | The canonical plain text, unchanged. Learning, search and models read only this field. |
| `body_format` | `html` when the prepared fragment is enriched mail, `text` when it is the escaped plain body. |
| `body_html` | The compiled display fragment. Empty when there is no displayable body. |
| `body_css` | The compiled presentation sheet for that fragment, omitted when none applies. The shell links it as a `blob:` stylesheet after `/mail.css`; the fragment itself never contains a `style` attribute or a `<style>` element. |
| `body_html_truncated` | The stored rich body was cut, or the renderer refused it and fell back to plain text. |
| `body_remote_images` | Remote image references in the compiled HTML; zero when the body fell back to plain text. |
| `body_blocked_images` | Remote images replaced by a placeholder; also zero on the plain-text fallback. |
| `body_inline_images` | Inline images resolved to a stored asset, emitted as a `data-inline` span for authenticated hydration. |
| `body_missing_inline` | Inline references whose bytes are not stored (old mail, over budget); they keep an "Inline image unavailable" placeholder. |

The same view carries a top-level `sender` object. It is additive: the
stored `message` and its `recipients` are unchanged, so the full recipient
list is not stored or sent twice.

| Sender field | Meaning |
| --- | --- |
| `from_address` | The address the message claims to come from: `sent_representing_address` when the source reports one, else `sender_address`. |
| `sender_address` | The address that actually sent the message. |
| `on_behalf_of` | True only when both addresses exist, differ after normalization, and the message is received. A sent item and a draft stay false even when the two addresses differ. |
| `reply_to` | First `Reply-To` header value, or `""` when absent. It is never silently assumed equal to From. |
| `return_path` | First `Return-Path` header value, or `""` when absent. |
| `authentication.reported` | True when the topmost `Authentication-Results` header is present and non-empty. |
| `authentication.source` | The receiving domain's authserv-id from that header (RFC 8601). |
| `authentication.spf` / `dkim` / `dmarc` | The receiving domain's first reported result per method (`pass`, `fail`, `softfail`, …), empty when the header or method is absent. |

The authentication parser is the one evidence uses for its `auth_*`
signals. When no header exists there is no pass/fail claim; the API never
calls a display name, a lookalike domain or an absent result "verified".
`Message.Recipients` remains the only recipient storage.

The raw stored HTML never leaves the process. `mail.Message` and
`mail.Details` exclude their HTML fields from JSON, list, triage and search
reads do not load them, and the view is rebuilt by `internal/render` on
every single-message read. A stored body is parsed again and re-emitted
from a strict allowlist, never returned as stored:

- Allowed: paragraphs, headings, lists, definition lists, quotes, pre/code,
  basic emphasis, tables, rules, links and neutral section containers.
- Removed with their subtree: scripts, document metadata,
  iframes/frames/objects/embeds, SVG/MathML, forms and controls, media,
  canvas, maps and templates. Comments and doctypes are dropped.
- Presentation is compiled, not copied. A sender `<style>` block is rebuilt
  rule by rule into `body_css`; a sender `style` attribute becomes a
  `data-ms` hook selected by that sheet. The compiler keeps typography,
  colour, spacing, borders, widths, backgrounds and table layout, and drops
  `url()`, `@import`, `@font-face`, `@keyframes`, `position`, `transform`,
  custom properties and every other fetching or escaping construct.
- HTML attributes are limited to safe presentation: table/cell `width`,
  `height`, `align`, `valign`, `bgcolor`, `border`, `cellpadding`,
  `cellspacing`, bounded `colspan`/`rowspan`, `class`/`id`/`role` for the
  compiled selectors, and `dir`/`lang`/`title`. Event handlers never survive.
- Links must be absolute `http`, `https` or `mailto` URLs without
  credentials; they gain `target="_blank"` and
  `rel="noopener noreferrer nofollow"`. Every other scheme, relative URL or
  protocol-relative URL keeps its text but loses the link.
- Remote images are never fetched. They become sized "Remote image blocked"
  placeholders and are counted. A `cid:` or `Content-Location` reference that
  matches a stored inline image becomes a `data-inline` span; the shell
  fetches the bytes over the bearer API and swaps in an ephemeral `blob:`
  image. An inline reference without bytes keeps an "Inline image
  unavailable" placeholder. Safe PNG/JPEG/GIF/WebP `data:` images are kept;
  SVG and every other data type are refused.
- Caps: 4 MiB of raw HTML, 1 MiB of fragment, 50,000 nodes and 256 levels
  of nesting, 256 KiB of compiled CSS and a bounded set of declaration
  lengths. A body over a cap, or one the parser refuses, falls back to the
  escaped plain text and reports `body_html_truncated: true`.

Plain text is escaped, never parsed as markup, rendered in one `pre` that
keeps blank lines, indentation and quote prefixes, and only `http(s)` and
`mailto` addresses become links. An empty or unknown HTML state uses this
path without reporting truncation. The pre-gate
`GET /api/messages/{id}?for=label&question=…&limit=…&review=true` returns
the same view and still carries no triage or route answer. The queue
parameters are mandatory while the gate is closed: the server accepts the
read only when that exact label-queue page offers the message, so the flag
alone unlocks nothing. The Answers screen has a separate surface:
`GET /api/messages/{id}?for=answer` returns the same view only when the
message carries a stored answer. The answer is the binding; an unanswered
message gets the ordinary gate error.

### Display backfill

Messages stored before this build may have `body_html_known = false`, or an
HTML body that references `cid:` inline images whose bytes were never
captured. The owner starts the one-time re-read with:

```json
POST /api/actions/backfill_html
{"source":"outlook"}
```

The body is optional; an empty `source` is accepted only when one connector
is registered. Success is `202 {"job":…}`. The job kind is `messages.html`,
so progress and cancellation use the ordinary `/api/jobs` endpoints and the
Learning screen shows it. One backfill runs at a time across sources
(`409 busy`); a sync may run beside it and neither blocks the API, and the
backfill never takes the sync claim.

Only display state is written: `body_html`, `body_html_known`,
`body_html_truncated`, attachment identity, the content-addressed inline
blobs, and the internal `inline_assets_checked` completion marker. The plain
body, `revision`, `updated_at`, `last_seen_run`, the search index and every
triage, evidence or learning row are left alone. A message the source refuses
is counted as skipped and the job continues; so is a row removed or filled by
a concurrent sync after it was listed. A source-level failure ends the job
with an error and a later run resumes the remaining rows.

A connector without `source.DetailReader` (the archive and RFC 5322
connectors today) cannot re-read a message: every candidate is reported
skipped and the job finishes successfully. New RFC 5322 mail nevertheless
stores its inline bytes at ingest; only mail captured by an older build stays
unavailable there. Outlook re-reads the attachment collection and repairs old
messages, and its completion marker prevents a message the source cannot
satisfy from being re-read on every pass. Decoded image bytes held by one
backfill transaction are capped at 64 MiB.

`422 nothing_to_backfill` means no visible message needs its display state,
including inline images; `422 no_mailbox_picked` means no mailbox is selected;
`422 source_required`/`422 unknown_source` and `503 no_source` describe source
selection. The action is a data capture, not a message read, so it is not
behind the learning gate.

## SSE

`GET /api/events` writes `retry: 3000`, then `event: <topic>`,
`data: <JSON>` frames and `: heartbeat` comments. Log growth is a reference,
not a payload: `log.wake` carries `{"seq":<last>,"count":<n>}` and an `id:`
of the newest ring sequence, with no message text or attributes. The Logs
screen reads the entries from `/api/logs?since=` after a wake; every other
screen ignores the frame. `Last-Event-ID` and `since` replay a wake for the
retained entries. `dropped` is a synthetic event with a reason and per-topic
counts: `buffer_full` (slow client queue), `bus_dropped` (a topic forwarder's
bus subscription), `ring_rotated` (log entries overwritten before replay) and
`seq_reset` (a cursor from an older launch). `log.entry` bus events are only
wake-ups and lost wake-ups are recovered on the next tail or heartbeat. At
most 8 clients (`503 too_many_streams` + `Retry-After: 5`); closing the hub
ends every stream on shutdown.

## Provider records

`engine.providers.file` (default `providers.json` under the data directory)
is a strict JSON object keyed by immutable record id
(`[a-z0-9][a-z0-9_-]*`); a missing or empty file means no providers are
configured. It is the one source of truth: there is no embedded catalog and
no second provider list in `settings.json`.

```json
{
  "decider-a": {
    "label": "Decider A",
    "protocol": "systemone",
    "base_url": "https://endpoint.example/v1",
    "path": "/systemone",
    "model": "model-1",
    "api_key_secret": "PROVIDER_DECIDER_A_API_KEY",
    "max_questions": 16,
    "context_tokens": 100000,
    "chars_per_token": 3,
    "reserve_tokens_per_byte": 1,
    "probability_tolerance": 0.02,
    "price_input_per_mtok": 0.05,
    "price_output_per_mtok": 0,
    "retry": {"statuses": [429, 502, 503, 504]}
  }
}
```

Protocols are `systemone`, `openai_decisions` (deciders), `openai_chat`
(generators) and `openai_embeddings` (embedders); the kind derives from the
protocol. `api_key_secret` is a secret name (the value is written through
`PUT /api/secrets/{name}`), never a key. A managed-runtime embedder sets
`runtime` instead of `base_url`/`api_key_secret`. Unknown fields, a bad
protocol, missing required fields, non-absolute URLs, bad retry bounds and
incomplete embedding specs are all reported; writes are atomic and replace
the whole record set. `GET /api/providers` returns each record's
`id`, `label`, `protocol`, derived `kind`, `base_url`, `path`, `model`,
`api_key_secret`, `key_present` and live `status`, plus the four role
selectors (`triage.decider`, `judge.provider`, `qa.provider`,
`embed.provider`) and whether each is configured. The provider test
result's `detail_code` is the stable translation key (`decider_answered`,
`generator_answered`, `embedder_answered`); `detail_params.dimensions` is
present for an embedder and `detail` remains the English fallback.

## Model discovery

`POST /api/providers/discover` reads the provider's official model-list
API, never a paid inference call and never embedded vendor data:

```json
{"base_url":"https://provider.example.test/v1","protocol":"openai_chat",
 "api_key":"plaintext-form-value","api_key_secret":"STORED_SECRET_NAME"}
```

The key precedence is `api_key` (when non-empty), then `api_key_secret`
read from the app secret store, then no authentication. A named secret
that is missing or empty is `422 missing_key`; the key is never echoed,
logged or returned, and upstream errors carry only the host and HTTP
status. The request is `GET {base_url}/models` with
`Accept: application/json` through the shared HTTP client (proxies,
retries and timeouts included), and its body is capped at 2 MiB. Unknown
response fields are ignored.

The normalized answer is sorted by model id; entries without an id are
skipped:

```json
{"models":[{
  "id":"provider.example.test/deepseek-v4.1-flash",
  "name":"DeepSeek V4.1 Flash Standard",
  "type":"chat",
  "context_tokens":1048576,
  "max_output_tokens":393216,
  "price_input_per_mtok":0.19,
  "price_output_per_mtok":0.6,
  "structured_output":true,
  "reasoning":true,
  "tools":true,
  "streaming":true,
  "endpoints":["chat/completions"],
  "path":"/chat/completions"
}]}
```

Missing values stay zero or omitted: the engine never invents context,
output caps, prices or capabilities. `context_tokens` comes from the first
positive `context_length`/`context_tokens`/`context_window`;
`max_output_tokens` from the first positive
`max_output_tokens`/`max_completion_tokens`. `pricing.input`/`pricing.output`
accept JSON numbers or numeric strings. `pricing.unit` is
`per_1m_tokens` (default), `per_1k_tokens` or `per_token`; values are
converted to per-million tokens, and an unknown unit omits both prices.
False capabilities are omitted. `path` is the canonical
`/chat/completions` or `/embeddings` (never the base URL); it follows the
reported `endpoints`, otherwise the protocol default. `type` is the
reported type, or `chat`/`embedding` inferred from the endpoints (then the
protocol) when absent.

Discovery errors are `502` JSON errors: `provider_discovery_auth` when the
provider answers `401`/`403`, `provider_discovery_not_found` for `404`,
`provider_discovery_failed` for any other non-2xx (including `429` and
`5xx`), network failure or an oversized body, `provider_discovery_invalid`
for malformed JSON, and `provider_discovery_empty` when no usable model is
returned. A missing base URL or non-absolute/non-http(s) URL is
`400 invalid_request`; a missing named secret is `422 missing_key`.

## Gate

`learning.Machine.Allow` is checked before `/api/triage*`, `/api/search`,
`/api/priority`, `/api/agenda`, `/api/companies`, `/api/companies/profiles`,
`/api/companies/{key}`, `/api/threads/{conversation_id}`, `/api/commitments`,
`/api/ask`, `/api/ask/history` (read and delete),
`/api/messages` and `/api/messages/{id}`. Refusal is `423 learning_locked` with the stage
blockers. `GET /api/gate` is the one query in front of that set: it calls
`Learning.Allow` once and answers `200`, because the Mail screen needs the
answer (and, while locked, the existing status) without first building
`/api/learning` itself. It reads no messages and builds no Mail page; the
open/latched path reads the learned-scope latch (store meta) and
short-circuits before the status, while the locked path builds and returns
the full status. It never returns `423` itself. The one exception is `GET /api/messages/{id}?for=label&question=…&limit=…&review=true`:
labelling is what opens the gate, so the label screen must be able to
show the message it asks the user to label, but only that message: the
server checks its membership in the same queue page the screen holds and
answers the ordinary gate error otherwise. That read returns no triage
or route answer. A second explicit surface is
`GET /api/messages/{id}?for=answer`: the Answers screen may read a message
that has a stored answer while the gate is closed, and an unanswered
message gets the same `423`. Everything that opens or configures learning (status,
learning, settings, secrets, mailboxes, jobs, learning actions, the
display backfill, logs, providers, sidecar, labels, events, shutdown)
works while the gate is closed. The service remains authoritative: a
service that returns `*LockedError` is answered `423` even if the
middleware's check just passed.

`learning.test_mode` is the explicit, reversible uncertified preview.
While it is on, `Allow` lets those gated reads through, but
`Machine.Open` — the source of `open` in `GET /api/learning`, `GET
/api/status` and the hub gate — keeps reporting the real gate, and
`Status`, the latch, the stages and the label/certification requirements
are unchanged. The preview is only configuration: `PUT
/api/settings/learning.test_mode` stores it through the ordinary settings
API and it has no effect on stored labels or certification. Turning it
off restores the locked responses immediately.

## Deviations from the phase-4 contract

- API tuning values have no `api.*` settings: `internal/api/options.go`
  owns the operational constants and reuses `events.buffer_size` and
  `log.ring.size`. They are restart-scoped, as the contract's
  `restart_required` settings would have been.
- A read-only secret store answers `PUT`/`DELETE` with `403 read_only`
  and the `MAILSENSE_SECRET_<NAME>` remedy, per the product brief. The
  contract reserved `403` for Host/Origin and proposed `409`; `GET
  /api/secrets` still reports `read_only: true`.
- Action paths follow the UI vocabulary (`sync`, `evidence`, `index`,
  `layer1`, `judge`, `label`) and additionally accept the learning
  blocker ids `run_evidence`, `run_layer1`, `run_judge` and
  `label_messages`. The label action returns a queue; manual labels use
  `POST /api/labels`.
- Q&A is a deterministic intent layer plus a single grounded generator
  call, not an agent loop: there is no tool iteration, no streaming and no
  second provider. A common question is answered locally from the priority
  ranking; a free-form question receives one written, cited answer or an
  explicit retrieval-only response. `GET /` serves the embedded desktop
  shell documented in `docs/UI.md`; the source registry is empty outside
  Windows.
