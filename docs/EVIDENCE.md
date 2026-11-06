# Evidence (layer 0) — the structural evidence engine

Single source of truth for the signals, their values and the rules
(`internal/evidence`, data in `internal/evidence/*.json`, locale data in
`internal/locale`). `TestTheDocListsEverySignalAndRule` keeps the tables
below equal to the registry and the shipped rules.

**Structure is measured, meaning is judged.** Every signal is a
deterministic, language-neutral measurement of one message and its
context: protocol facts (header names, MAPI values, MIME types,
authentication results) are protocol constants; locale knowledge (month
and weekday names, date and time patterns, separators, currency symbols,
exemplar letters, language profiles, confusables) is pinned data from
CLDR, the UDHR in XML project and the Unicode security data. The rule
layer adds no hand-written word list: a signal whose value is decided by a
fixed vocabulary is marked **lexical** (the threading signal `is_reply`),
and no rule may read one.

## The three roles

| Role | What | Where | State |
| --- | --- | --- | --- |
| (a) rules | data-driven rules mapping signal conditions to an answer of a question; a **definitional** rule may answer a question the route has not certified, while every other rule acts as an answer key only when its precision, measured on THIS install's judge labels, has a Wilson lower bound ≥ `evidence.rules.min_precision` | `rules.json`, `evidence.rules.file`, `RuleSet.Measure`, `Engine.RuleTable` | built; no judge yet (`ErrNoJudge`): estimates are evidence only, definitional rules answer uncertified questions |
| (b) observed facts | the causal signals, bucketed, as lines of the triage state | `Engine.FactsText`, `triage.facts.evidence` | built, OFF by default (it would change every state hash; paid decisions then only become stale — never re-billed on their own, the plan shows the re-decision cost) |
| (c) features | a stable one-hot/bucketed/hashed vector of the causal signals with partial and missing flags, for layer 1 | `Engine.FeatureSchema`, `Engine.Features` | built |

## Signals

A signal (`Signal`, one file `sig_*.go` each, registered in `init`)
declares its name, version, value type (bool, int, float, enum with its
vocabulary, opaque category, set, list of ints), the source capabilities
it needs (`Needs`: all; `AnyOf`: one), whether it is **causal** (uses
only data at or before the message's instant: only causal signals feed
features, facts and rules), whether it depends on other messages
(**context**: history, threads, vectors — computed again every run),
whether it reads only a message's summary fields (`Summary`: it has a
value while the details are pending), the settings and the pinned data
it reads (`Settings`, `Data`: `locale`, `mail`, `psl` and `threads` —
their values and digests are part of its effective version, so a changed
setting or regenerated data recomputes exactly the signals that read it;
a data digest is of the files' meaning, `digest.Content`: a `_doc` edit or
a reformatting changes nothing; `threads` is the version of the threading
rules, `threads.Version`), and what
a value read on a cut input can overstate (`Unsure` values such as
none/absent/false; `Bound` of a number: `at_least` for a count,
`at_most` for a nearest distance). An extractor returning an enum value
outside its vocabulary fails the run (a defect, never stored).

**Causality.** A causal signal reads only what existed at the message's
instant. The history and the conversations are the **causal view**
(`threads.CausalRoles`): the owner-scope mail of every folder of the
picked stores but drafts, wherever the owner has filed it since (filing
is the owner's later action). Conversations grow in time order: each
message sees the members its conversation held at its own instant
(`threads.Analysis.EarlierMembers`), and the owner's replies are paired
at their own instant (`KnownReplies`, `KnownHistory`), so a later
message bridging two conversations never changes an earlier message's
values (`TestALaterBridgeNeverChangesEarlierThreads`,
`TestOwnerFilingNeverChangesCausalHistory`,
`TestLaterMailNeverChangesCausalEvidence`). Junk and deleted mail stay in
the causal view (they arrived): only a PURGE (the owner empties Deleted
Items or Junk) removes messages from the history and so shifts later
values — `history_overall` first — which is free local recomputation;
paid triage decisions are never re-billed by it (they only turn stale:
the decision policy, `docs/DEVELOPMENT.md`). A message filed twice (same
store, same Message-ID) is one message: only its first copy in time order
waits, answers and counts; the copies share its replies
(`threads.TestCopiesOfAMessageCountOnce`).

Every signal's values over a small multilingual corpus are pinned with its
effective version (`testdata/signal-pins.json`,
`TestSignalValuesChangeOnlyWithTheirVersion`): values that change while
the version, settings and data digests stay the same fail the build
(review 3: `date_unresolved` had changed at v1); `EVIDENCE_PIN_WRITE=1`
rewrites the pins after a bump.

A value is OK, **not applicable** (`na`: no previous message, nothing to
measure) or **unavailable** (the input is missing: a capability the
source lacks — `capability:<name>`, every one that would do named —, a
sent item without transport headers, a message without a vector, a
message whose details its source has not delivered yet —
`details_pending`): never guessed. A value read on a cut input (the
header block or body cut by its source: `mail.AltHeadersTruncated`,
`BodyTruncated`) is **partial** — OK or not applicable alike: a count is
a lower bound, and an unsure value means "not in the part read"; the last
line of a cut header block is not read at all (`mail.ParseCutHeaders`).
Rules never let a partial value settle what it cannot (`Cond.holds`,
`Signal.Unsure`, `Signal.Bound`).

Dates are counted from the message's day in the owner's zone
(`time.zone`; the evidence stage waits for it to be confirmed) and placed
from the author's day (the Date header's offset: the weekday or the
year-less date the author meant). The language of the own text
(`locale.Language`) selects the CLDR date and time patterns of the
language **and its regional locales** (en_US and en_GB, es and es_MX…;
plus root's ISO 8601 dates with a year in every language), with UTS #35
widths (MM and dd are exactly two digits, M and d one or two), matched
on tokens of the folded text (`locale.Locale.Find`; a letter next to a
digit is a boundary for dates and times — a Hebrew prefix letter, the
ISO "T" —, and a literal written right before a month name is part of
its word). A numeric date the regions read in different orders
(03/04/2026) is **ambiguous** unless a field above 12 decides: a date,
but never a confident wrong one. `Locale.Resolve` puts a year-less date
in the nearest year, a two-digit year in the nearest century, a lone
weekday at its next occurrence, and reads a written year in the
calendar of the language's likely territory (CLDR calendarPreferenceData)
that brings it nearest: converted when that is exact (Gregorian; a
calendar of Gregorian months counting another era — Buddhist from 2484
BE, ROC; the Persian arithmetic over 1300–1498 AP, where it agrees with
the official calendar), else not resolved with its reason
(`calendar:islamic` — which Islamic variant a writer used is not in a
date —, `ambiguous_order`): `date_unresolved` counts them. A year-less all-numeric form ("10-12",
"03/11") is a resolved date only if no pattern of the language — of any
of its regions — writes numeric dates in the other day/month order
(`locale.disputeOrders`; a year-first ISO pattern claims no order, and a
pattern whose literals name the fields, "M月d日", is not all-numeric):
English and French, whose regions write both MM-dd and dd/MM, read such a
form as `ambiguous_order`, German ("20.10.") as a date; ten shipped
languages have disputed forms. Distances are civil day numbers (any
year). The nearest full date strictly before the message's day is
`date_past_days` (positive: overdue; 0 when a full date is named but none
is before it; not applicable when it names none): the deadline rule
`deadline_overdue` reads it. A written year 0000 (a form's placeholder) is
no date. The own
text is `mail.CleanBody`'s: since 2026-10-15 it also cuts at an
attribution line (ending in a colon, naming an address, holding a digit;
never a quoted line) whose quote runs to the end — every following
non-blank line, up to the end or a separator, header block or signature
delimiter, is quoted or tab-indented: Outlook's plain-text rendering of a
quoted HTML message — whose date was the mail's own day in 28 of the 38
`deadline_same_day` hits (rule dropped; PROGRESS). An attribution right
before a ">" quote, or before a quote running to the end, is never own
text, the first line included (its date is not the message's); answers
between quotes are kept. Known limit (review 3, measured): a quoted
message whose tail Outlook renders without the tab (a signature table, a
nested quote resuming) is kept as own text — 410 fixture messages.

## Storage, runs, stage

- One row per message (`evidence`, migrations 0008–0009): the message
  revision it was computed at, the schema id, the digest of its
  **context** (its scope verdict and reason, its store's owner identity,
  its source's capabilities, the owners' organisations) and the values
  as a JSON array aligned with the schema; not-applicable and
  unavailable values name their reason by an interned id
  (`evidence_reasons`). A schema (`evidence_schemas`) is the ordered list
  of `[name, effective version]` (`v<version>` plus a digest of the
  settings and pinned data the signal reads): a changed version, setting
  or data recomputes that signal only; a message-only signal of an
  unchanged message in an unchanged context is reused; only changed rows
  are written. A row of an older revision than the message's is stale:
  `Records` gives an empty record until the next run.
- A run (`Engine.Run`/`Start`, job kind `evidence`, one at a time:
  `ErrBusy`) reads the visible mail once for the shared context (the
  causal history and conversations, look-alike targets, template counts
  from the vectors of the current space), then computes the messages in
  batches (`evidence.batch_size`) on `evidence.workers` goroutines; a
  panic in a worker fails the run with that item's error (logged with its
  stack). A cancelled run keeps the batches it wrote. Runs start after
  every successful sync, every index run that stored or confirmed at
  least one vector (a failed run that wrote nothing and a 0/0 no-op
  changed no input and start none) and every change of a setting a
  signal declares (`evidence.after_runs`); a trigger that arrives during
  a run starts one more run after it (`jobs.Single`, requests coalesce);
  events `evidence.progress`, `evidence.finished`; runs persisted
  (`evidence_runs`) with the fingerprint of their inputs, the newest
  `evidence.runs.keep` kept, schemas no row uses removed.
- The learning stage `evidence` is done when the last succeeded run read
  the current inputs (`store.EvidenceInputs`: every visible message, its
  revision, role, verdict, reason and identity, its vector's sequence;
  plus the sources' capabilities, the schema — settings and data — and
  the vector space) and no message still waits for its details, so every
  chosen message has every signal its source can give; blocked
  (`evidence_run_failed`, actions `review_failures`, `run_evidence`) when
  the last run over the current inputs failed; pending until the time
  zone is confirmed. A status call recomputes the fingerprint only when
  the store's generation, its vector sequence, the schema, the space or
  the evidence writes moved.
- Queries for the UI: `Records`, `EachRecord`, `Coverage` (per signal:
  OK / partial / not applicable / unavailable with reasons, values or
  buckets, distinct count of opaque values; a message class the protocol
  does not document is counted as `custom`), `SignalList`, `RuleTable`,
  `FeatureLayout` (names, id, `Vector`: one reading of
  `evidence.features.hash_buckets`; its id changes with every cell name,
  a data vocabulary such as the languages included), `FactsText`.
  With `triage.facts.evidence` on, the facts evidence owns (reply,
  recipients, attachments, question marks, links, characters) reach the
  triage state from evidence only.

## Rules

A rule (`rules.json`, per id; users override per id in
`evidence.rules.file`, null removes) names a question, an answer (an
option, true/false, or a `{min, max}` level range) and conditions on
causal signals (`is`, `in`, `min`/`max`, `has`). A rule naming a
non-causal signal, two operators, a value outside the signal's type or
vocabulary, an unknown question, an option the question does not have,
an answer of another kind or levels outside the question's scale (the
triage taxonomy, `taxonomy.Load`) is refused at load. A number on a list
of ints is its length (the one accessor of rules, features and
coverage). No two shipped rules answer one question differently on the
same conditions.
It fires when every condition holds on a value that is there.
`Engine.RuleAnswers` is the read-time fallback behind the triage rule
layer: it applies the same rules to one message's stored record and returns
an answer only when the firing rules agree. For a score the narrowest firing
range wins and only an exact level is served; a range or two different exact
levels answer nothing. The answer carries the rule id and is never stored,
counted as coverage or used to open the gate.

`RuleSet.Measure` counts, per rule, the labelled messages, how many it
fires on and how many of those agree with the label: precision,
coverage, Wilson interval at `evidence.rules.z`; a rule is a key when
the lower bound reaches `evidence.rules.min_precision`. In the app the
labels are the install's judge labels (`Judge`, the Gemma layer, not
built: `UseJudge`); at dev time the same measurement runs on the gold
sample (train and dev splits only) in `scripts/evidence-fixture.sh`.

## Locale data (`internal/locale`)

`scripts/update-locale-data.sh` downloads pinned CLDR (release-48-2:
coverage levels, supplemental data, likely subtags, language aliases,
the main files of the `params.json` coverage levels and their parents),
the UDHR in XML project at one commit and UTS #39 confusables of Unicode
17.0.0, and generates with `scripts/datagen locale`:

| File | Content |
| --- | --- |
| `langid.json` | per language (CLDR modern/moderate locales with a UDHR translation of stage 4 and status y, all such translations together): its scripts, its CLDR exemplar letters (main, auxiliary) and its 500 most frequent 1–3-character n-grams |
| `locales.json` | per language, joined with its regional locales (`params.json` `regional_status`; `und` = root, ISO dates with a year only): month, weekday (and wide weekday) and day-period names, the chosen date/time patterns, the decimal and grouping separators of their default numbering systems (an alias to latn followed: bn, mr, my, ne), the calendars of its likely territory; per calendar: its system, current era start and whether its months are the Gregorian ones |
| `currency.json` | ISO 4217 codes in use; CLDR symbols mapped to their codes (ASCII-letter symbols left out: their codes are matched) |
| `confusables.json` | UTS #39 source → prototype |

`pin.json` records every input and output SHA-256 (a test checks the
outputs); `params.json` holds the generator's choices.

## Adding a signal

1. A file `sig_<name>.go` with one `register(Signal{…})`: name, version
   1, type (and vocabulary of an enum), `Needs`/`AnyOf`, `Causal`,
   `Context`, `Summary`, `Settings`, `Data`, `Unsure`/`Bound`, group,
   doc, extractor (a test checks the declared data against what the
   views read). Read inputs through
   `Input` (headers with cut awareness, own text, language, dates,
   amounts) and `Env` (history, threads, look-alikes, templates).
2. A numeric signal needs bucket bounds in `features.json`.
3. Tests for its values and its unavailable/partial cases; regenerate the
   reference below: `EVIDENCE_DOC_WRITE=1 go test -run
   TestTheDocReferenceIsGenerated ./internal/evidence`.
4. Changing what an extractor returns: bump its version (only it is
   recomputed).

## Measured on the fixture (2026-10-15, `scripts/evidence-fixture.sh`)

See `out/evidence-fixture.json` / `out/evidence-rules.json` (aggregates) for
the numbers.

## Signal reference

### headers

| Signal | Type | Needs | Causal | Context | Lexical | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `auth_dkim` v1 | enum: absent, pass, fail, softfail, neutral, none, temperror, permerror, policy, other | headers | yes | no | no | The DKIM result (RFC 6376) the receiving domain recorded in its Authentication-Results header (RFC 8601): pass, fail, neutral, none, temperror, permerror, policy, other, or absent. |
| `auth_dmarc` v1 | enum: absent, pass, fail, softfail, neutral, none, temperror, permerror, policy, other | headers | yes | no | no | The DMARC result (RFC 7489) the receiving domain recorded in its Authentication-Results header (RFC 8601): pass, fail, none, temperror, permerror, other, or absent. |
| `auth_spf` v1 | enum: absent, pass, fail, softfail, neutral, none, temperror, permerror, policy, other | headers | yes | no | no | The SPF result (RFC 7208) the receiving domain recorded in its Authentication-Results header (RFC 8601): pass, fail, softfail, neutral, none, temperror, permerror, policy, other, or absent. |
| `auto_response_suppress` v1 | bool | headers | yes | no | no | An X-Auto-Response-Suppress header (MS-OXCMAIL): the sender asked that no automatic reply answer it — machine-sent mail sets it. |
| `auto_submitted` v1 | enum: none, no, auto-generated, auto-replied, auto-notified, other | headers | yes | no | no | The Auto-Submitted keyword (RFC 3834, RFC 5436): no (written by a person), auto-generated, auto-replied, auto-notified, other; none without the header. |
| `hop_count` v1 | int | headers | yes | no | no | Received headers (RFC 5321 §4.4): the relays the message passed (a lower bound when the header block was cut). |
| `importance` v2 | enum: low, normal, high, other | headers | yes | no | no | The importance the sender set, read from the transport headers as they arrived (MS-OXCMAIL 2.2.3.2.5): the Importance header (RFC 2156: low, normal, high; any other value: other), else X-Priority (1-2 high, 3 normal, 4-5 low); not applicable when neither says (partial when the header block was cut). The message's importance property is not read: the owner can change it on a received message (importance_mark). |
| `list_id` v1 | bool | headers | yes | no | no | A List-Id header (RFC 2919): the message came through a mailing list. |
| `list_unsubscribe` v1 | enum: none, present, one_click | headers | yes | no | no | A List-Unsubscribe header (RFC 2369): none, present, or one_click when List-Unsubscribe-Post offers RFC 8058 one-click unsubscription (bulk senders must). |
| `mailer` v1 | category | headers | yes | no | no | The sending software (X-Mailer, else User-Agent) as an opaque categorical value: a hash of its normalized name (lower case, version digits as one #); none without either header. |
| `message_class` v1 | category | message_class | yes | no | no | The item's message class (MS-OXCMSG PidTagMessageClass: IPM.Note, IPM.Schedule.Meeting.Request, IPM.Note.Microsoft.Voicemail.UM…), lower case: a protocol value, kept as written. |
| `message_kind` v1 | enum: ordinary, meeting_request, meeting_cancel, meeting_response, delivery_report, read_receipt, auto_reply, recall | any of message_class, headers | yes | no | no | What the item is beyond a message (mail.KindOf, kinds.json): from its message class, multipart/report type, calendar METHOD and Auto-Submitted keyword. |
| `name_address` v1 | enum: none, same_domain, other_domain |  | yes | no | no | An address written inside the sender's display name, against the sender's own address: other_domain is the classic disguise ("ceo@example.com" <x@example.net>); none when the name holds no address. |
| `precedence` v1 | enum: none, bulk, list, junk, other | headers | yes | no | no | The Precedence header (bulk, list, junk; any other value is other): mail sent to many by a machine. |
| `read_receipt` v1 | bool |  | yes | no | no | The sender asked for a read receipt: the message's flag, or a Disposition-Notification-To header (RFC 8098). |
| `reply_to` v1 | enum: none, same_address, same_domain, other_domain | headers | yes | no | no | Where replies go (Reply-To, RFC 5322 §3.6.2) against the From address: same_address, same_domain, other_domain (the most distant of its addresses), none without the header. |
| `return_path` v1 | enum: none, null, same_domain, other_domain | headers | yes | no | no | The envelope sender (Return-Path, RFC 5321 §4.4) against the From address's registrable domain: same_domain, other_domain (a sending service or a forgery), null (<>: a bounce), none without the header. |
| `spam_flag` v1 | bool | headers | yes | no | no | A spam filter on the way flagged the message (X-Spam-Flag: YES, SpamAssassin's verdict header). |
| `spam_scl` v1 | int |  | yes | no | no | The Exchange spam confidence level (-1 trusted, 0..9 from clean to spam): the message's SCL property, else its X-MS-Exchange-Organization-SCL header; not applicable when neither is there (partial when the header block was cut). |
| `voting_options` v1 | bool |  | yes | no | no | The sender attached voting buttons (Message.VotingOptions is not empty): the message asks for a choice, so a verdict is being requested. An empty value treats absent and not-carried alike; the true direction is the one that carries meaning. |

### recipients

| Signal | Type | Needs | Causal | Context | Lexical | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `addressed_to_me` v1 | enum: to, cc, bcc, not_addressed | recipients | yes | no | no | How the owner received the message: in To, in Cc, in Bcc (a recipient or the source's addressed-to-me flag), or not addressed at all (a list, an alias, a forward). |
| `direction` v1 | enum: received, sent_by_owner |  | yes | no | no | Whether the owner sent the message (scope: sender or sent on the owner's behalf) or received it. |
| `external_recipients` v1 | int | recipients | yes | no | no | To and Cc recipients outside the owners' organisations (not an owner, not a directory name, a domain that is not one of the owners' organisations). |
| `recipient_count` v1 | int | recipients | yes | no | no | To and Cc recipients (Bcc is invisible to the others). |
| `sender_relation` v1 | enum: self, internal, external, directory, none |  | yes | no | no | The sender against the owner: self (the owner), internal (an organisation of the picked mailboxes' owners: registrable domain, shared free-mail domains excluded), external, directory (an Exchange distinguished name: an address of the owner's directory, no domain), none (no sender). |
| `sole_to` v1 | bool | recipients | yes | no | no | The owner is the only To recipient (written to them alone). |

### thread

| Signal | Type | Needs | Causal | Context | Lexical | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `back_and_forth` v2 | int |  | yes | yes | no | Changes of direction (owner to correspondent and back) in the conversation up to this message. |
| `is_reply` v1 | bool |  | yes | no | yes | The message answers or forwards another: it references a parent (In-Reply-To/References) or its subject starts with a reply or forward prefix (mail.SubjectPrefixes). |
| `owner_asked` v2 | enum: none, asked, not_asked |  | yes | yes | no | Whether the owner's latest earlier message in the conversation asked something: its own text (mail.CleanBody) holds a question mark of any script (UCD); none when the owner had not written. |
| `owner_replied_after` v2 | bool |  | no | yes | no | The owner answered this message later (threads reply matching). NOT causal: an outcome for evaluation only, never a feature, fact or rule input. |
| `sender_reminders` v2 | int |  | yes | yes | no | Messages the same sender sent in the conversation right before this one with no answer from the owner in between: how many times they are writing again. |
| `thread_gap_minutes` v2 | float |  | yes | yes | no | Minutes since the previous message of the conversation (not applicable for its first message). |
| `thread_last_sender` v2 | enum: none, owner, same_sender, other |  | yes | yes | no | Who wrote the message just before this one in its conversation: the owner, this message's sender, someone else, or none. |
| `thread_position` v2 | int |  | yes | yes | no | Messages of the conversation dated before this one, as the conversation stood at this message's own instant (0: it starts the thread; a later message joining two conversations never changes it); conversations are built from the causal view: the owner-scope mail of every folder but drafts, wherever it is filed now (threads.CausalRoles). |

### history

| Signal | Type | Needs | Causal | Context | Lexical | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `exchange_years` v2 | int |  | yes | yes | no | Calendar years (UTC) before this message in which the owner and the sender's organisation wrote to each other both ways (the address itself for a shared free-mail domain). |
| `first_time_sender` v2 | enum: known, new_address, new_address_new_org |  | yes | yes | no | Whether the owner had exchanged mail with this sender before: known (the address), new_address (a new address of a known organisation, or of a shared free-mail domain), new_address_new_org (nobody of that organisation either). |
| `lookalike_domain` v2 | enum: none, skeleton, edit |  | yes | yes | no | The sender's registrable domain imitates one the owner corresponds with (wrote to before this message, or the owner's own) without being it: skeleton (the same UTS #39 confusables skeleton, раураl.com for paypal.com), edit (within evidence.lookalike.max_edits edits, examp1e.com), none. Domains whose first label is shorter than evidence.lookalike.min_chars are never look-alikes. |
| `owner_sent_before` v2 | int |  | yes | yes | no | Messages the owner sent to this sender's address (To or Cc) before this one. |
| `owner_share` v2 | float |  | yes | yes | no | The owner's share of the exchange with this sender before this message: sent to it over sent plus received (0: the owner only listens; not applicable before any exchange). |
| `reply_latency_band` v2 | int |  | yes | yes | no | The band (index into threads.response_buckets: 0 fastest) of the owner's median response time to this sender before this message; not applicable before any reply. |
| `reply_rate` v2 | float |  | yes | yes | no | Share of this sender's earlier messages the owner had answered by this message's instant (threads reply matching, causal); not applicable before any. |
| `sender_first_seen_days` v2 | float |  | yes | yes | no | Days since the first message exchanged with this sender's address (either way); not applicable for a first contact. |
| `sender_last_seen_days` v2 | float |  | yes | yes | no | Days since the latest earlier message exchanged with this sender's address (either way); not applicable for a first contact. |
| `sender_received_before` v2 | int |  | yes | yes | no | Messages the owner received from this sender's address before this one (the causal view: owner-scope mail of every folder but drafts, wherever it is filed now). |

### content

| Signal | Type | Needs | Causal | Context | Lexical | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `amount_magnitude` v2 | int |  | yes | no | no | The order of magnitude (floor of log10) of the largest amount (in absolute value: a credit or a refund counts too) read unambiguously with the separators of the language and its regions; not applicable without one (an amount next to a currency code whose value is a plain integer is never read unambiguously: the code may be a word). |
| `attachment_bytes` v1 | int | attachments | yes | no | no | Total decoded size of the attachments shown as files (a lower bound when a size is unknown). |
| `attachment_count` v1 | int | attachments | yes | no | no | Attachments shown as files (inline images of a signature or a body are not counted). |
| `attachment_types` v1 | set | attachments | yes | no | no | The MIME types of the attachments shown as files (application/pdf, text/calendar…), distinct and sorted. |
| `currency_codes` v1 | set |  | yes | no | no | The currencies of the amounts (ISO 4217 codes; "ambiguous" for a symbol several currencies share), distinct and sorted. |
| `currency_count` v1 | int |  | yes | no | no | Amounts of money in the message's own text: a number next to an ISO 4217 code or a CLDR currency symbol (locale.Amounts). |
| `date_count` v3 | int |  | yes | no | no | How many full dates the message's own text names that resolve to a civil date (date_days, before the cap); the others are date_unresolved. |
| `date_days` v3 | ints |  | yes | no | no | Every full date the message's own text names that resolves to a civil date, as days from the message's day in the owner's zone (negative: before it): numeric and named dates of the CLDR patterns of its language and the language's regions (and ISO 8601), placed from the author's day (the Date header's offset) — a year-less one in the nearest year, a year in the language's calendar nearest it (converted when exact: Buddhist era, Persian arithmetic) —; the nearest evidence.dates.max, sorted. Ambiguous and unconvertible ones are date_unresolved; weekdays named alone are weekday_days. |
| `date_next_days` v3 | int |  | yes | no | no | Days to the nearest full date the own text names on or after the message's day (0: that same day); not applicable when it names none (the reason of an unresolved one when it names only such: ambiguous_order, calendar:<name>). |
| `date_past_days` v1 | int |  | yes | no | no | Days from the message's day to the nearest full date the own text names strictly before it (1: the day before; positive: overdue); 0 when it names a full date but none before it, not applicable when it names none (the reason of an unresolved one when it names only such: ambiguous_order, calendar:<name>). A cut body may hide a nearer past date, so the reported nearest is at least as far back as the whole text's (a rule takes it only as a maximum: at_most). |
| `date_unresolved` v2 | int |  | yes | no | no | Full dates the message's own text names that have no exact civil date: a numeric date its language's regions read in different orders (03/04/2026, no field above 12), or one written in a calendar without an exact conversion (an Islamic, Hebrew, Coptic… year): dates, never guessed. |
| `has_attachments` v1 | bool |  | yes | no | no | The message has attachments (the source's flag, or attachment metadata). |
| `has_calendar` v1 | bool | attachments | yes | no | no | The message carries iCalendar data (a calendar part with a METHOD, a text/calendar or application/ics attachment): an invitation, an answer or an event. |
| `inline_count` v1 | int | attachments | yes | no | no | Parts shown inside the body (images of a signature or a newsletter, a calendar part). |
| `language` v2 | enum (105 values, from the pinned data) |  | yes | no | no | The language of the own text (locale.Language: n-gram likelihood against the shipped profiles of the languages of its scripts; Hiragana and Katakana are one writing system, so either identifies Japanese; und below evidence.language.min_letters letters). |
| `link_count` v1 | int |  | yes | no | no | Links in the message's own text (subject and cleaned body; mail.Links). |
| `own_chars` v1 | int |  | yes | no | no | Characters of the message's own body text (mail.CleanBody), in every script alike. |
| `question_marks` v1 | int |  | yes | no | no | Question marks of any script (UCD: ? ？ ؟ ;…) in the message's own text. |
| `quoted_ratio` v1 | float |  | yes | no | no | Share of the body that is not the message's own text (quoted replies, forwarded headers, signature: mail.CleanBody), in characters; not applicable for an empty body. |
| `script` v1 | category |  | yes | no | no | The dominant Unicode script of the own text's letters (Latin, Han, Arabic…); none without letters. |
| `signature` v1 | enum: none, delimiter, contact_block |  | yes | no | no | How the message's own text ends, by structure (mail.Signature): an RFC 3676 "-- " delimiter, a closing paragraph of contact lines (phone, address, link), or none. |
| `template_matches` v1 | int |  | yes | yes | no | Earlier messages of the same sender (the last evidence.template.window with a vector) whose embedding is a near-duplicate of this one (cosine ≥ evidence.template.min_cosine): mail cut from one template. Unavailable without a vector of the current space. |
| `time_count` v1 | int |  | yes | no | no | Times of day the own text names (its language's CLDR time patterns: 14:30, 2:30 PM, 午後2:30…). |
| `weekday_days` v3 | ints |  | yes | no | no | The weekdays the own text names alone (wide CLDR names outside a date), each at its next occurrence from the author's day (the Date header's offset: the weekday the author meant), as days from the message's day in the owner's zone, sorted. |

### owner_actions

| Signal | Type | Needs | Causal | Context | Lexical | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `categories` v1 | set | categories | no | no | no | The owner's categories on the message, each as an opaque categorical value (the owner's taxonomy, hashed), sorted. NOT causal. |
| `flag_due_days` v1 | int | flags | no | no | no | Days from the message's day (owner's zone) to the due date of the owner's flag (a date of the owner's calendar, MS-OXOTASK). NOT causal. |
| `flag_status` v1 | enum: none, marked, complete, other | flags | no | no | no | The owner's follow-up flag (MS-OXOFLAG: none, marked, complete). NOT causal: the owner's later action. |
| `folder_path` v1 | category |  | no | no | no | The folder the message is filed in, as an opaque categorical value (the owner's own taxonomy, hashed). NOT causal. |
| `folder_role` v1 | enum: inbox, sent, drafts, deleted, junk, archive, other | folder_roles | no | no | no | The role of the folder the message is filed in (the source's role, never a name). NOT causal: filing is the owner's later action. |
| `importance_mark` v1 | enum: low, normal, high |  | no | no | no | The message's importance property now (MAPI PidTagImportance: 0 low, 1 normal, 2 high): set from the sender's headers on delivery, but the owner can change it on a received message (Outlook: Edit Message; inbox rules: MarkImportance). NOT causal: the owner's later action; not applicable when the source has none. |
| `last_verb` v1 | enum: none, open, reply, reply_all, forward, other | last_verb | no | no | no | The owner's last action on the message (MS-OXOMSG PidTagLastVerbExecuted: open, reply, reply_all, forward, other; none when never set). NOT causal. |
| `read_state` v1 | bool | read_state | no | no | no | The owner has read the message. NOT causal: the owner's later action. |

## Rule reference

A rule answers a question the route has not certified only when it is **definitional**: its signal semantics and the question's definition guarantee the answer. Every other rule is an estimate that teaches and answers only after its precision has been measured on this install. No rule may read a signal marked **lexical**: the rule layer uses structure, protocol and mailbox statistics, never a word list.

| Rule | Question | Answer | Definitional | When |
| --- | --- | --- | --- | --- |
| `asks_meeting_request` | asks_for | `"meeting_availability"` | yes | message_kind ∈ {meeting_request} |
| `asks_nothing_auto` | asks_for | `"nothing_fyi"` | no | auto_submitted ∈ {auto-generated, auto-notified} |
| `asks_nothing_response` | asks_for | `"nothing_fyi"` | no | message_kind ∈ {meeting_response, meeting_cancel} |
| `cat_auto_notifications` | category | `"notifications"` | no | auto_submitted ∈ {auto-generated, auto-notified} |
| `cat_currency_finance` | category | `"finance"` | no | currency_count ≥ 1 |
| `cat_report_notifications` | category | `"notifications"` | yes | message_kind ∈ {delivery_report, read_receipt, auto_reply} |
| `cat_scl_spam` | category | `"spam"` | no | spam_scl ≥ 5 |
| `cat_unsubscribe_marketing` | category | `"marketing"` | no | list_unsubscribe ∈ {present, one_click} |
| `cta_meeting_request` | call_to_action | `true` | yes | message_kind ∈ {meeting_request} |
| `cta_nothing_auto` | call_to_action | `false` | no | auto_submitted ∈ {auto-generated, auto-notified} |
| `deadline_24h` | deadline | `{"min":4,"max":4}` | no | date_next_days ≥ 1 ≤ 1 |
| `deadline_any` | deadline | `{"min":1,"max":5}` | no | date_count ≥ 1 |
| `deadline_month` | deadline | `{"min":2,"max":2}` | no | date_next_days ≥ 8 ≤ 31 |
| `deadline_months` | deadline | `{"min":1,"max":1}` | no | date_next_days ≥ 32 |
| `deadline_none` | deadline | `{"min":0,"max":0}` | no | date_count ≤ 0 |
| `deadline_overdue` | deadline | `{"min":5,"max":5}` | no | date_past_days ≥ 1 |
| `deadline_week` | deadline | `{"min":3,"max":3}` | no | date_next_days ≥ 2 ≤ 7 |
| `deadline_weekday` | deadline | `{"min":3,"max":5}` | no | weekday_days ≥ 1 |
| `no_question_mark` | is_question | `false` | no | question_marks ≤ 0 |
| `not_susp_internal` | suspicious | `false` | yes | sender_relation ∈ {internal, directory} |
| `not_susp_known_pass` | suspicious | `false` | yes | auth_dmarc ∈ {pass} and first_time_sender ∈ {known} |
| `question_mark` | is_question | `true` | no | question_marks ≥ 1 |
| `rel_auto_replied` | relationship | `"automated_service"` | no | auto_submitted ∈ {auto-replied} |
| `rel_auto_submitted` | relationship | `"automated_service"` | no | auto_submitted ∈ {auto-generated, auto-notified} |
| `rel_auto_suppress` | relationship | `"automated_service"` | no | auto_response_suppress = true |
| `rel_directory` | relationship | `"colleague"` | yes | sender_relation ∈ {directory} |
| `rel_internal` | relationship | `"colleague"` | yes | sender_relation ∈ {internal} |
| `rel_list_id` | relationship | `"automated_service"` | no | list_id = true |
| `rel_list_unsubscribe` | relationship | `"automated_service"` | no | list_unsubscribe ∈ {present, one_click} |
| `rel_one_click` | relationship | `"automated_service"` | no | list_unsubscribe ∈ {one_click} |
| `rel_precedence` | relationship | `"automated_service"` | yes | precedence ∈ {bulk, list, junk} |
| `rel_report` | relationship | `"automated_service"` | no | message_kind ∈ {delivery_report, read_receipt, auto_reply} |
| `reply_not_addressed` | reply_needed | `false` | no | addressed_to_me ∈ {not_addressed} |
| `reply_not_auto` | reply_needed | `false` | no | auto_submitted ∈ {auto-generated, auto-replied, auto-notified} |
| `reply_not_bulk` | reply_needed | `false` | yes | list_unsubscribe ∈ {present, one_click} |
| `reply_not_cc` | reply_needed | `false` | yes | addressed_to_me ∈ {cc} |
| `reply_not_kind` | reply_needed | `false` | no | message_kind ∈ {delivery_report, read_receipt, auto_reply, meeting_response, meeting_cancel} |
| `reply_reminder` | reply_needed | `true` | no | sender_reminders ≥ 1 and addressed_to_me ∈ {to} |
| `reply_sole_question` | reply_needed | `true` | no | sole_to = true and question_marks ≥ 1 |
| `request_one` | request_count | `{"min":1,"max":1}` | no | question_marks ≥ 1 ≤ 1 |
| `request_three` | request_count | `{"min":3,"max":3}` | no | question_marks ≥ 3 |
| `request_two` | request_count | `{"min":2,"max":2}` | no | question_marks ≥ 2 ≤ 2 |
| `signature_found` | has_signature | `true` | yes | signature ∈ {delimiter, contact_block} |
| `signature_none` | has_signature | `false` | no | signature ∈ {none} |
| `susp_dmarc_fail` | suspicious | `true` | no | auth_dmarc ∈ {fail} |
| `susp_lookalike` | suspicious | `true` | no | lookalike_domain ∈ {skeleton, edit} |
| `susp_name_address` | suspicious | `true` | no | name_address ∈ {other_domain} |
| `susp_reply_to_new` | suspicious | `true` | no | reply_to ∈ {other_domain} and first_time_sender ∈ {new_address_new_org} |
| `susp_scl` | suspicious | `true` | no | spam_scl ≥ 5 |
| `susp_spf_fail` | suspicious | `true` | no | auth_spf ∈ {fail, softfail} |
| `topic_calendar` | topic | `"meeting_scheduling"` | yes | has_calendar = true |
| `topic_meeting_kind` | topic | `"meeting_scheduling"` | yes | message_kind ∈ {meeting_request, meeting_cancel, meeting_response} |
