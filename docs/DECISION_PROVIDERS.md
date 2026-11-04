# Decision-model providers — wire contracts

Researched 2026-10-14 from vendor docs (URLs per section). Items marked
**[UNVERIFIED]** come from third parties or are undocumented; the adapter must
not depend on them without a live probe.

Provider records are the source of truth: `engine.providers.file` is a
strict JSON object keyed by immutable record id. There is no embedded
catalog and no preset overlay. A record names its `protocol`, endpoint
(`base_url`, `path`, `model`), `api_key_secret` (a secret name, never a
value), limits, prices, retry overrides and the capability fields its
adapter needs. This file keeps the vendor evidence behind the protocol
descriptors (kind, auth, required fields, probe, path validation) and the
adapter codecs.

Design consequence: one `engine/decision` adapter with two **protocols**:
`systemone` (one codec; vendors differ only in record data) and
`openai_decisions` (a structurally different codec). HTTP, auth, retry,
chunking, usage accounting and logging are shared (`httpx`).

## Shared rules

- Budget (phase 2b): before every call the adapter reserves its ceiling —
  `reserve_tokens_per_byte` (1) tokens per byte of the request at the
  input price, plus `reserve_output_tokens` at the output price (required
  when a record has one) — with the run's guard, and settles the reported
  usage after. An unpriced call in flight never switches the USD check off
  for the rest of the run. A provider that reports its charge (for example
  a metered `cost.request_cost_usd`) is accounted at that figure
  (`UsageRecord.Billed`); the plan and the reservation still use the
  record's price, its ceiling.

- Chunk question sets by the record's `max_questions`; merge `answers` and
  sum `usage.input_tokens` across chunks; the state is billed once per
  chunk.
- Never retry 400, 401, 402, 403, 404, 422 or 500 (the schema's `not_in` of
  `http.retry.statuses`; it binds record overrides too), nor quota 429s
  (`retry.quota_codes`: vendor-specific codes such as
  `credit_balance_exhausted`, `insufficient_quota`, `*_spend_limit_exceeded`
  and Cloudflare code 3036). Retried by default: 429, 502, 503, 504
  (`http.retry.statuses`); a record may add 408 and 529 (overloaded).
  Capped jittered backoff, honouring `Retry-After` / `retry-after-ms`.
  Connection-refused/DNS before send is free to retry; a failure after the
  request was written (read timeout) is terminal and accounted at the
  request's estimate, as is a 500 or any 5xx the policy does not retry (it
  may have been billed). A 500 is provider-wide (`ErrTransient`): the run
  stops after the first message and retries later instead of charging one
  failure per message.
- Parse every numeric answer at the trust boundary: finite, in range, never
  bool-as-number. A malformed answer is a contract error, not a default.
- Decode strictly: a 200 with no `answers` for a requested question is an
  error (an envelope bug once silently produced empty answers).
- Probabilities must be complete (every option or level), sum to 1, put the
  choice first, and give the score as their expected level, within the
  record's `probability_tolerance` (0.02 for the legacy evidence set).
- A failure in a later chunk keeps the earlier chunks' answers (paid for):
  triage stores them and asks only the missing questions next time.

## Protocol `systemone`

Request: `{"model", "state": string|object|array, "questions": {name: {type, instructions, criteria}}}`
- `noul`: optional `criteria: {"true": "...", "false": "..."}`
- `choice`: `criteria: {option: description|null}` (2–255)
- `score`: `criteria: [ordered level labels]` (2–10)

Response:
```json
{"model":"model-name","answers":{
  "escalate":{"type":"noul","noul":0.92},
  "queue":{"type":"choice","choice":"nurse_line","probabilities":{"nurse_line":1,"primary_care":0},"confidence":1},
  "acuity":{"type":"score","score":1.86,"legend":{"0":"Within a week","1":"Within a day","2":"Within an hour"},"probabilities":{"0":0,"1":0.13,"2":0.87},"confidence":0.79}},
 "usage":{"input_tokens":477,"output_tokens":0}}
```
`noul` answers carry no `confidence`. Output tokens are not billed.

Endpoint/evidence quirks that stay data or envelope handling:
- `max_questions` is the server's documented or probed cap (16 for a
  vendor whose docs leave it undocumented; 64 for the Clef family; 128 for
  SubQ Snap; 200 for OpenAI Decisions).
- A Cloudflare Worker AI endpoint wraps the answer in
  `{"result": …, "success": bool, "errors": […]}`: set `envelope:
  "cloudflare"`; `success: false` is an error, and text state is silently
  truncated when too long, so the adapter enforces the context budget
  itself.
- An OpenAI-Decisions-style endpoint that has no noul criteria uses
  `noul_fold` to fold the question's instructions and true/false criteria
  into `instructions`.
- A metered endpoint may add `id`, `object`, `created`, `truncated`,
  `energy` and `cost`; the adapter rejects `truncated: true` as a contract
  error and accounts a reported charge.
- Error shapes read by `DescribeError`: `{"detail":{error_type,message}}`,
  `{"detail":"..."}` (a string or FastAPI array), Cloudflare
  `{"errors":[{code,message}]}`, OpenAI
  `{"error":{type,code,message}}`; anything else is the trimmed body.

Vendor sources: provider.example.test/endpoints/decisions-api,
provider.example.test/api and /models, developers.cloudflare.com/workers-ai/models/clef/
(+ schema-input.json / schema-output.json),
blog.cloudflare.com/clef-decision-models/, a third-party report of the
Cloudflare envelope (github.com/looplj/axonhub/issues/2631).

## Protocol `openai_decisions`

`POST {base_url}{path}`, model data from the record (for example the public
beta decisions endpoint).
- Request: `input` (string; JSON-encode objects), `questions`: **array** of
  `{type: predicate|choice|score, name, instructions: string}`; choice
  `choices: [{value, description}]` (2–255); score `levels: [{label, description}]`.
  No noul criteria — fold into `instructions`. Up to 200 questions.
- Response: `answers` **array in question order**: predicate `probability`;
  choice `choice`, `probabilities: [{value, probability}]`, `confidence`; score
  `score`, `probabilities: [{value, label, probability}]`, `confidence` (no
  legend — rebuild from labels); any answer may be `{"type":"refusal"}`.
- Errors `{"error":{type, code, message}}`; headers `Retry-After`, `x-ratelimit-*`.

Source: developers.openai.com/api/docs/guides/decisions (+ reference, error-codes, rate-limits).

## Provider test

`POST /api/providers/{id}/test` dispatches on the record's kind:
- decider: the protocol descriptor's one-question `Probe` (state "Subject:
  connection test", a noul question `is_test`), through the real adapter;
- generator: one tiny system+user chat call (non-empty content; a JSON-mode
  record must return valid JSON);
- embedder: one short document embedding (one vector, native length, finite,
  unit norm within the record's `norm_tolerance`).

The key is checked before calling; the result carries `ok`, `latency_ms`,
`model` and a generic `detail`, never the key, mail or answer content.

## Live evidence (2026-10-14 → 2026-10-25)

Record-level probes of the systemone family established:
- probed caps: 17/64/128 one-call requests on SubQ Snap; 65 questions on a
  metered Clef-flash endpoint → HTTP 400 `{"detail": "…maximum of 64
  questions"}`; a 529 means overloaded and is retryable;
- usage is input-only (`output_tokens: 0`) on the decision endpoints;
- probabilities are consistent at four decimals (choice = argmax, score =
  expected level) on both codecs; outputs may still vary between requests,
  and the cache assumes nothing;
- Cloudflare's text state truncation is real and budget enforcement must
  happen before sending;
- a metered endpoint billed 3.4 µWh against a token-price ceiling of
  $0.000055 for a probe, so the record keeps the token price and the
  adapter accounts the reported charge.

No record secret is stored in this file; keys live in the app's secret
store under each record's `api_key_secret`.
