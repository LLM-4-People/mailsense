# Desktop interface

Mailsense's desktop surface is a plain HTML document, two stylesheets, four
scripts and one vector icon embedded in the binary (`internal/ui`). There is
no build step, no framework, no web font and no CDN: the shell works offline
and every data-bearing screen talks to the local API described in
`docs/API.md`, plus the one pre-gate questions read noted below.

```
internal/ui/
  index.html   shell, icon sprite, token screen, modal, the one search box
  app.css      GENERATED from css/manifest.txt — do not edit
  css/         10 source stylesheets: base, shell, components, screens
  mail.css     tokens and styles of the sandboxed message iframe
  i18n.en.js   English catalog (flat keys, plural objects)
  i18n.fr.js   French catalog, including French-only settings/api wording
  i18n.js      t(), resolution, plurals, Intl formatters, zone selection
  app.js       GENERATED from js/manifest.txt — do not edit
  js/          24 source modules: core, language, HTTP, router, shell,
               boot, hub, brief, ask, learning, companies, sources,
               labels, answers, mail body/list, search, settings,
               providers, logs, actions, init
  favicon.svg  the brand mark and browser favicon (the one vector source)
```

The two bundles are generated output, never hand-edited:

```
go generate ./internal/ui     # writes app.js and app.css from the manifests
```

`internal/ui/bundle.go` is the one generator and `js/manifest.txt` /
`css/manifest.txt` are the one owners of order. The gate re-runs the
generator and fails when a bundle is stale, a module is unlisted, a module
or function exceeds its reviewed size, or two functions share a body.


`ui.Handler()` serves the embedded files. The API's static route delegates
to it, so the shell and the API share one origin, one listener and one
launch token.

## Brand mark

The mark is a cream seal on the deep-green tile: the seal is the physical
mail artifact and the trust promise; the green M is the monogram. It is
drawn once in `internal/ui/favicon.svg` and used in three places:

- the browser tab (`<link rel="icon">`);
- the sidebar and token screen (`<img class="brand-mark">`, 30 px, no CSS
  background or letter);
- the Windows executable and window icon.

The Windows assets are generated from the same SVG, never drawn twice:

```
cmd/mailsense/mailsense.ico          the multi-size ICO
cmd/mailsense/rsrc_windows_*.syso    the Go linker resource
```

Run `scripts/logo.sh` after changing the SVG. It renders 16, 24, 32, 48,
64, 128 and 256 px with Playwright, writes the ICO, and embeds it for
amd64 and arm64 with `rsrc` (`go install github.com/akavel/rsrc@latest`).
`internal/desktop` applies the resource to the WebView2 window with
`WM_SETICON`; a build without the resource still opens, with the system
default icon. The SVG stays the single owner; do not hand-edit the ICO or
the `.syso`. This Playwright use is only the rasterizer; the browser test
suite is separate (`scripts/test-ui.sh`, below).

## Launch and token handshake

1. The server binds `127.0.0.1:0` and writes the descriptor (URL, launch
   id, PID, build, token) to `<data>/runtime/api.json` with mode 0600.
2. The window or browser opens `http://127.0.0.1:<port>/#token=<token>`.
   The token travels only in the URL fragment, which is never sent to the
   server in a request line.
3. `app.js` copies the fragment's token to `sessionStorage` under
   `mailsense.token` and immediately replaces the address with `#/hub`
   through `history.replaceState`. The token is never written to the DOM,
   the console, a toast, an error or a log line.
4. Every ordinary API call sends `Authorization: Bearer <token>`. The one
   exception is `GET /api/events`, whose `EventSource` cannot set headers
   and therefore uses `?token=<token>`; `docs/API.md` describes that
   fallback.
5. A `401` clears the stored token, closes the event stream and replaces
   the surface with a "Reopen the app" screen. Closing and reopening the
   app is the only way to obtain a fresh token.
6. A window that loaded before its token arrived (for example a same-tab
   navigation to `#token=…`) reloads once the fragment appears, then runs
   the normal handshake. An empty or missing token shows the same reopen
   screen instead of an empty shell.

The **Shut down** button sets the UI's shutting-down state, closes the
event stream and posts `/api/shutdown`; the API's shutdown path then
destroys the window. Closing the window is the other direction: the
Windows window runner returns and requests the same API shutdown. On
platforms that open a browser, the API keeps running after the browser
closes because the runtime descriptor is still the way back in.

## Windowing

| Platform | Behaviour |
| --- | --- |
| Windows | A WebView2 window titled `Mailsense`, 1280×840, centred, created and run on one locked OS thread. Closing it requests shutdown; the API's shutdown path destroys it idempotently. Requires the WebView2 runtime. |
| Linux, macOS, BSD | The default browser opens the same URL when `DISPLAY` or `WAYLAND_DISPLAY` is set (macOS always). The launch is non-blocking: a missing display, browser or `xdg-open` is a warning, and the API keeps running for the runtime descriptor. |

`internal/desktop` is the only code that receives the bootstrap URL. It
refuses anything except an `http` URL on `127.0.0.1` with an explicit port,
no credentials, no query, the app root path and a non-empty
`#token=…` fragment. It contains no API logic.

## Screens

All routes are hash routes (`#/hub`, `#/settings?tab=runtime`); the server
always returns the same document. `SCREEN_IDS` in `app.js` owns the route
ids, the catalogs own each screen's title and eyebrow, the sidebar sets
`aria-current`, and `document.title` follows the active screen. The
sidebar order is **Home**, **Brief**, **Mail**, **Companies**, **Ask**,
**Teach**, **Sources**, **Settings**, **Logs**, then a **System** group
with **Learning**. Answer
history (`#/answers`) is not a primary nav item; it stays available from
Teach, Ask and Home. An empty hash, the launch-token handshake and an
unknown route all open Home; `#/ask` is the real Ask screen.

| Route | Purpose | Reads | Writes |
| --- | --- | --- | --- |
| `#/hub` | Compact Brief dashboard: replies/deadlines/meetings counts, Brief summary, Next 3 h agenda, Important mail; Recent activity, learning and mailbox details behind a closed **More** disclosure; the pre-gate review cockpit when preview is off | `/api/hub`, `/api/learning`, `/api/questions`, `/api/priority`, `/api/agenda`, `/api/ask/history` | `/api/settings/learning.test_mode` (preview toggle) |
| `#/brief` | Current work: must, waiting, changed since the last seen boundary, open relationships and suggested topics; every row links to its message or organization and carries evidence ids | `/api/brief?since=` | none |
| `#/mail` | All mail / Important / Conversations / Recent; master-detail list, message detail, and the conversation card (state, participants, earlier messages) above the friendly email summary; uncertified preview; the first all-mail page is read on the first visit, and the first click reads `/api/gate` without waiting for `/api/learning`. Search arrives from the shell bar and renders as a result page, not a tab | `/api/gate`, `/api/messages`, `/api/priority`, `/api/conversations`, `/api/triage?limit=&offset=`, `/api/triage/summary`, `/api/triage/{id}`, `/api/messages/{id}`, `/api/threads/{conversation_id}`, `/api/search?q=` | none |
| `#/companies?key=&q=` | Organization explorer: ranked external companies (including one-way mail), server-side search over the whole ranking, and a profile with evidence brief, next step, open-loop ledger, history, addresses, meetings and deadlines; visible rows are prefetched, and selection swaps only the profile | `/api/companies?limit=&q=`, `/api/companies/profiles?key=`, `/api/companies/{key}?months=&event_days=` | none |
| `#/ask` | Natural-language questions: structured, generated or retrieval-only answers, citations and history | `/api/ask`, `/api/ask/history` | `DELETE /api/ask/history` |
| `#/labels` | Teach: one message at a time, Label / Review Gemma (`?review=1`), human question titles, suggestion-first, progress | `/api/questions`, `/api/labels/summary`, `/api/labels/queue`, `/api/messages/{id}?for=label` | `/api/labels` |
| `#/answers` | Answer history: current manual/judge answers, filters, paging and one message's answer history | `/api/labels/summary`, `/api/answers`, `/api/questions`, `/api/messages/{id}?for=answer` | none; corrections link to Teach |
| `#/learning` | Plain-language readiness, next actions, steps and technical details | `/api/learning`, `/api/status`, `/api/jobs` | `/api/actions/*` through the actions, `/api/learning/zone` |
| `#/sources` | Mailboxes, discovery, picked set, declared owner address, display and inline-image backfill | `/api/mailboxes`, `/api/status`, `/api/jobs` | `/api/mailboxes/discover`, `pick`, `unpick`, `declare`, `undeclare`, `/api/actions/sync`, `/api/actions/backfill_html`, `/api/jobs/{id}/cancel` |
| `#/settings` | Simple/advanced settings, provider records and role assignment, one local engine; reload and reset | `/api/settings`, `/api/providers`, `/api/sidecar` | `/api/settings/{key}`, `/api/settings/reload`, `/api/secrets/{name}`, `/api/providers`, `/api/providers/{id}` (create/update/delete) and `/api/providers/{id}/test`, `/api/sidecar/install` |
| `#/logs` | The log ring with level/component filters, pause and follow; an expanded row prints every stored attribute (content values are already `[REDACTED]` by `logx`; the built-in token/email patterns always apply and custom `log.redact.patterns` only add expressions) | `/api/logs`, SSE `log.wake` | none |

Notes that the UI discloses instead of hiding:

- `GET /api/questions` is pre-gate and content-free, so Teach can list the
  questions and their fingerprints before learning completes. The queue
  depends on machine facts; while the time zone is unconfirmed, Teach
  shows that blocker and does not call the queue.
- Teach has two modes, **Label** and **Review Gemma**, carried by
  `review=1`. Review asks the queue with `review=true`; the suggestion is a
  first-class card above the alternatives and **Use suggestion** stores it
  as a manual label and advances. **Save and advance** stores a corrected
  answer and is disabled until a value is selected. Normal mode neither
  asks for nor shows a judge value. An empty review queue says that no
  Gemma suggestions are left for the question.
- `GET /api/priority` is owner-only and gated; it returns ranked items with
  a plain snippet, reasons, signals and a `preview` flag. The frontend
  never ranks a single triage page client-side. `/api/ask` is gated and
  returns `structured`, `generated` or `retrieval` answers with citations;
  `/api/ask/history` is local and clearable.
- The install overlay
  `%LOCALAPPDATA%\Mailsense\layer1-requirements.json` (the
  `layer1.requirements.file` target) sets
  `{"defaults":{"certify_sources":["manual"]}}`: with it the labels stage
  counts manual truth only, while judge labels remain teacher targets.
  This is installation configuration, not a code default: the shipped
  requirements are unchanged.
- Search accepts only `q`; the mailbox, role and attachment filters
  therefore narrow the returned page client-side, and the screen says so.
  There is one search entry point, the shell bar.
- The Recent tab never fetches the whole triage: it reads the summary and
  one bounded page at a time (`limit` 1…200, default 50), and "Load more"
  asks the next offset. The escalated filter applies to the loaded pages.
  The Important tab uses the server-side `/api/priority` ranking, not the
  newest page, and pages through the top-200 ranked snapshot with the same
  explicit offset.
- A per-message triage read does not carry the route layer. The detail
  panel names the certified route, the paid decider cache or "not reported
  by this per-message read" rather than inventing a layer.
- Blocker actions with no API counterpart (`start_source`,
  `resume_source`, `retry_details`, `reconnect_mailbox`) open guidance and
  offer the closest real action (discovery or sync). No button is a mock.
- Sources has a **Message display** panel for mail stored before display HTML
  or inline images were captured. **Fetch missing display data** posts
  `/api/actions/backfill_html` for the selected source. The button is
  disabled while a `messages.html` job runs or no source is registered; the
  panel shows done/total, the job's updated/skipped message and its error,
  and **Cancel** uses the ordinary job cancel. The panel is not behind the
  learning gate, and the job never changes the plain learning body.

## Home, Ask, Mail and Teach

The primary workspace is a single calm shell. Home answers "what matters
now", Ask answers a natural-language question, Mail shows important and
recent mail, and Teach turns a suggestion into one manual answer at a
time. All four share one cached data layer and one visual system.

### Home

`#/hub` is the Home screen. Its topbar is the gate pill, the warning-style
Preview pill when preview is on, the generated time and Refresh. The page
is a compact **Brief** dashboard, not a greeting: a single header row
shows the date and deterministic counts for replies owed, deadlines and
meetings, followed by a three-column grid. There is no Ask card on Home;
Ask is one menu click or `Ctrl+K` away.

- **Preview-off (pre-gate) state.** When the gate is closed and the test
  preview is off, Home is the review cockpit: the closed gate, the true
  `manual / needed` meter, the newest Gemma suggestions, the three
  questions with the most remaining manual answers, and links to Teach
  (review or normal). It never shows mail content. With no picked store or
  no mail in scope it offers Sources and Learning instead of zero counts.
  A met requirement links Learning instead of drawing a 0/0 meter.
- **Preview-on / open state.** Home shows a compact, truthful preview
  banner with **Open Mail** and **Disable preview**, then the Brief grid.
- **One section primitive.** Every Home column — Brief, Next, Important
  mail, and the three locked-state columns — is the same `homeSection`
  builder: a kicker, one title size (19 px display), an optional count
  chip, an optional action, a two-line sub, and a body that owns the
  column's scroll. The card clips; the body scrolls. The middle column
  used to grow past the grid and paint under More because its wrapper had
  no bounded height; the wrapper is now a one-row grid and the body is
  the scroll owner, so a long column never overlaps the disclosure. The
  More blocks reuse the same head markup in a flat variant (no surface,
  no own scroll), so the typography is identical there too. The learning
  block inside Brief is an inset (h3), not a fourth column.
  - **Brief.** The header counts replies owed, due follow-ups and
    upcoming meetings. The column then shows two or three highest-signal
    rows reused from priority/agenda, followed by a compact learning
    inset (progress, next question, review link). The full learning
    detail stays behind More. It is a summary of the same truth, not a
    second ranking.
  - **Next.** `GET /api/agenda` renders as one compact list:
    calendar occurrences, replies the owner still owes, and due
    follow-ups, in time order. A read without `hours` uses the
    configured `agenda.default_hours` and reports it as
    `horizon_hours`; the horizon chips (3 h, 8 h, 24 h, plus the
    configured value when different) redraw only the list, never the
    page. A reply that shares a
    participant with an upcoming meeting is shown before that meeting,
    with a `Before …` badge; an overdue commitment keeps a negative slack
    and stays visible. Mail rows open the message in Mail. An empty window
    says so instead of showing a zeroed meter.
  - **Thread truth on Important mail.** A row whose conversation was
    answered by a colleague carries an **Answered by …** badge and is no
    longer in the Reply filter; an unanswered inbound carries **Waiting
    on you**, one the owner answered **Waiting on them**, and a thread
    whose shared Sent folder is not synced **Check thread** instead of a
    false reply flag. The waiting badge reads the action truth first, so a
    history row in an open thread never claims the owner is being waited
    on.
  - **Important mail.** Up to six `/api/priority` items: sender, subject,
    the localized topic → subtopic path, plain snippet, relative time, a
    concrete action badge (Pay, Decide, Send, Follow up, Verify, …), why
    badges (security, decision, reply, urgent, deadline, action, question,
    workload, recent) and a Preview pill. Clicking opens Mail on that
    message, instantly from cache. Security uses the danger tone, urgent
    the warning tone, reply/decision the accent tone. With no ranked
    items, a composed empty state links Teach.
  - **More (closed by default).** Recent questions from `GET
    /api/ask/history` (each opening the full answer), the full learning
    rail (`Your answers / goal`, stored Gemma suggestions, top question,
    **Review suggestions**, **View learning**), and the mailbox line with
    store/message counts and a Sources link. The meter is the true manual
    count; suggestions do not close the gate.
  - **Ask.** The Ask screen owns the composer: a two-line input, Send,
    four localized suggestion chips and a keyboard hint. Submitting calls
    `POST /api/ask` and renders the answer inline: a composed sentence for
    structured intents, generated text for free-form answers, a typed
    table or agenda for computed plans, or an explicit no-match /
    unavailable note. Citations are rows with subject, sender, date and
    reason; each opens the message in Mail. A "what should I do next"
    question renders the same agenda rows, with the requested horizon.

### Ask

`#/ask` is a real screen, not a redirect. It is thread-first: a thread
header names the current, new or saved conversation above the transcript,
and the composer sits below the transcript at the bottom of the screen.
A **Recent questions** rail stays on the right.

- **Answer modes.** `structured` answers common intents (reply, urgent,
  deadline, security, decision, workload, priority, sender) from local
  priority data; `summary_today` is a real owner-day window over received
  mail, not the mailbox-wide attention counts. The client composes the
  localized sentence from `intent` and `counts`. `generated` answers a
  free-form question from numbered owner messages through `qa.provider`
  and returns an answer with citations. `computed` renders a typed plan,
  the agenda, or one organization's profile. `retrieval` is the honest
  fallback: the matching sources are shown with no written answer. Its
  notice names the cause: `qa_index_warming` while the semantic index
  warms, `qa_generation_failed` when a configured provider call or its
  output failed, `qa_no_grounded_answer` when no answer or citation
  survived grounding, `qa_unavailable` only when no provider or key is
  configured, `qa_local_no_answer` when Local retrieved sources but has no
  local written-answer model, and `no_matches` when nothing relevant
  exists. Only `qa_unavailable` links to provider settings; every other
  notice keeps the owner on the answer.
- **Routes.** Every send carries an explicit `route`: `auto` (default),
  `local` or `remote`. `auto` keeps the computed-or-provider behavior.
  `local` never calls a provider: exact sender, structured, company and
  clarification answers stay available, and a free-form question returns
  its matching sources with `qa_local_no_answer`. `remote` resolves the
  configured `qa.provider` before any branch and makes exactly one writing
  call; an unavailable remote model is a typed `qa_route_unavailable` 422,
  never a quiet local answer. The route is per turn, persisted with the
  history row, and never inherited. The composer's model selector reports
  Auto, Local and Remote (disabled and labelled when no provider is
  configured) and shows a one-line explanation of the selected route.
- **Answer language.** A written answer whose own text is confidently in
  another language than the requested `en`/`fr` is still shown in full,
  with its citations, mode pill and provider/model footer. The card adds an
  `answer_language` warning and no retry button: the check never retries,
  rewrites or falls back to retrieval, and re-asking calls the provider
  again because the answer was not cached. Quoted reply lines are stripped
  before detection, and short answers stay unjudged. The noticed answer is
  saved to history like any other answer.
- **Threads.** The first question starts a conversation: the server
  returns a `conversation_id`, and every follow-up or clarification option
  is sent with it and appended above the composer. The planner receives
  the newest six turns, so a short follow-up such as "et pour l'autre ?"
  resolves against the chat; every factual claim is still grounded in and
  cites the numbered owner messages. The thread header says **Current
  conversation**, **New conversation** or **Saved answer**, names the
  opening question, and shows the exchange count. The composer footer says
  whether the next message replies here or starts a new thread.
  **New conversation** clears the transcript and id, preserves the draft,
  and focuses the composer. A saved answer opens as a past answer, not an
  active thread; the next submit starts a new conversation rather than
  pretending the older turn is active context.
- **Clarification.** When a singular question matches several people or
  organizations, the clarification shows one button per candidate instead of
  repeating the follow-up text: the name, its email or domain, and the message
  count are the visible card, while a click submits the complete follow-up
  question (exactly one of `options`) that carries the exact address or
  domain. A plural question ("clients", "fournisseurs") is answered as a set
  and never asks which one. A shared mailbox (`info@`, `rh@`, `support@`)
  is never offered as a "which person?" choice when the question names a
  real person: the person-like candidates win, so "simon de services ex"
  answers Simon Berthier directly instead of listing the company's
  mailboxes beside him. A multi-token person name is one identity, and
  whether a query *is* a full name is learned from the address book itself:
  the candidates' display names say which tokens behave like given names
  (first position) and which like surnames (last position). When the query
  has at least two name terms covering both positions, a person must match
  all of them or be at the organization the question names. If nobody does,
  the card says **No one has that full name. Did you mean…** and offers at
  most four near-misses, rarest name token first. The rule is not a list of
  known questions: a mailbox of other names gets the same treatment from its
  own data.
  Names and addresses are text nodes with `dir="auto"`. Without candidates
  the card falls back to the plain option buttons.
- **Topic contacts.** A computed contacts answer whose plan carries the
  searched topic leads with that topic and the matching message total, and
  its table shows rank, contact, messages and last contact. The
  sent/received/threads columns are omitted because the rows describe one
  message population; a capped list says it kept the best-matching
  messages.
- **Organizations.** A question that asks about one organization's volume,
  people, open conversations or last contact is answered from its cached
  profile, instantly and without a model. A question that only names the
  organization as context ("who is the laptop for?") retrieves that
  organization's own mail and answers from it with citations; the generic
  profile is never substituted for a specific fact.
- **Citations.** Each citation shows a message number, subject, sender,
  received date and optional reason. Citation chips in generated text map
  back to those rows. Selecting one opens `#/mail?mode=important&id=…`
  or the recent-message detail; the list never exposes a body.
- **No-match.** A `no_matches` answer says that nothing relevant was found
  and suggests rephrasing. It shows no citation rows and no model footer.
- **Provider privacy.** The Ask composer carries a low-emphasis note:
  Auto and Remote use the configured answer provider and matching excerpts
  are sent when a model writes an answer; Local never calls a model. The
  note links to Settings → Providers. The answer footer names the route
  and, for a written answer or a provider fallback, the provider and
  model; local structured, no-match and warming-index answers say "Local
  search over your mail — no model was used."
- **History.** The rail lists recent questions newest first, deduplicated
  by question text (with a repeat count), and loads the stored answer on
  click. **Clear history** is confirmed and calls `DELETE
  /api/ask/history`; **Answer history** opens the label history. The
  transcript is `aria-live="polite"`; a completed answer returns focus to
  the composer so a follow-up is one keystroke away. Enter submits,
  Shift+Enter adds a line.

### Brief

`#/brief` is the current-work screen. It opens with four counts (must,
waiting, changed, relationships) and a two-column grid: **Must** carries an
accent edge and lists what the owner owes, **Waiting** lists what others
owe, **Changed** lists new conversation activity since the stored
boundary, **Relationships** lists organizations with open conversations,
and **Topics** is a whole-mailbox census of the classifier's topic answer,
localized in both languages, with a nested subtopic breakdown (count and
evidence) under each topic. Each row shows its state/status in friendly
words, its concrete action, its subject path, its organization, person,
relative time and evidence chips;
selecting a message opens the Mail detail, and selecting an organization
opens Companies. **Mark seen** stores the returned `generated_at` as the
next boundary; loading the screen never advances it. Empty sections say so.
The section count is the honest total before the row cap; Topics shows the
classified message total, and its subtitle says how many of the whole
mailbox's messages carry a topic.

### Mail

`#/mail` has four tabs: **All mail** (default), **Important**,
**Conversations** and **Recent**. It is a master/detail layout on wide
screens and a list-then-detail layout on narrow screens.

### Global search

The shell header owns the only search box. It is the same element on every
route, so its position never moves, and it is never duplicated inside a
screen.

- Typing two or more characters opens a local listbox with up to six hits
  from `GET /api/search?q=&limit=6`; the rows show subject, sender, date and
  score. Arrow keys move the active row, Enter opens it, Escape closes the
  list. The **See all results** row (or Enter with no active row) opens the
  result page.
- Operators narrow the query without a second form: `from:`/`sender:` an
  address or domain, `in:` a folder role, `has:attachment` or
  `-has:attachment`. The remaining words are the query.
- `/` moves focus into the box and selects its text from anywhere outside a
  text field, including inside the sandboxed message reader; the numeric
  keypad divide key works too. The box advertises the shortcut with
  `aria-keyshortcuts="/"` and a localized tooltip. Typing `/` while already
  in a text field inserts the character instead. Ctrl/Cmd+K still opens Ask.
  A deep link or Back to `#/mail?mode=search&q=…` fills the box with the
  query the result page is showing.
- The result page has no query input. It keeps the mailbox, role and
  attachment selects as filters over the returned page, shows the `from:`
  operator as a removable chip, and says that the shell bar owns the query.
  `GET /api/search` still accepts only `q`; the filters narrow the returned
  page client-side.

- **All mail.** `GET /api/messages?limit=100&counts=1&role=…` over the
  picked owner mailboxes, newest first. The first page is read on the first
  visit; boot does not ship it ahead of the screen. The first click asks
  `/api/gate` for the content decision and paints rows as soon as they land;
  `/api/learning` continues in the background and refreshes the gate/preview
  pills when it answers. Before either read resolves the route keeps the
  existing skeleton; no empty list, zero count or "no mail" is invented. Eight folder chips (All, Inbox,
  Sent, Drafts, Deleted, Junk, Archive, Other) carry the endpoint's
  whole-mailbox counts, not the current page's. A filter row above the chips
  holds one sender field (placeholder "Sender address or @domain"): Enter or
  Apply rewrites the URL with `sender=…` (the trimmed value; an all-space
  value trims to empty and the `sender` parameter is omitted, never sent as
  `sender=`; the server owns the comparison and rejects a present-but-empty
  value, a lone `@`, any control character (checked after trimming) or more than 320 runes),
  keeps the current folder and reloads the list and counts for that
  population. The filter is one exact address or one domain,
  never free text or a display name. The active sender is a chip with a clear
  action; clearing returns to the whole folder. Row links, the selected
  message and the role chips keep the active sender in the URL, so a deep
  link reopens the same filtered list. A sender with no mail shows "No mail
  from this sender" with the same clear action; the error path is unchanged.
  A second, single boolean toggle sits beside the folder chips: **Unread**.
  The folder chips remain their own `role="group"` labeled "Folder filter";
  the toggle has its own "Read state" group, so the folder label never
  covers a read-state control. It rewrites the URL with `unread=true` and
  composes with the folder and sender filters; turning it off removes the
  parameter (`unread=false` is also filter-off, never a read-only page) and
  the active chip is `aria-pressed=true`. A folder change, the sender
  Apply/clear and a row selection keep the read state; clearing unread keeps
  the folder and sender. When the filtered population is empty the
  precedence is sender copy, then "No unread mail", then the folder copy;
  the unread copy's "Clear unread filter" action removes only the read
  filter. A row's unread dot is `row.is_read === false` in every folder
  (never the old sent/drafts exclusion), so every row in the Unread view
  carries it. The server is the one filter owner: no row or count is
  filtered in the client. At narrow width the two groups wrap and stay
  inside the viewport instead of becoming a horizontal scroll pane.
  The first page is 100 real
  rows; the screen paints the first **24** in one fragment and appends the
  remaining rows after a double `requestAnimationFrame` (2 s bounded
  fallback). Both fragments use the same row builder and dedupe by
  `row_id`, so the first batch, the deferred rest and **Load more** cannot
  diverge; no placeholder or fake row is shown, and the 100-row order,
  selection, detail, counts, scroll, cache and Load more are unchanged. In
  the Round 36 10-pair series the first frame held **24 rows / 528 DOM
  nodes** (before: 100 / 1,436) and the response→first-frame median was
  **52.5 ms** (before 90 ms). The Playwright-visible response→list median
  was **113.5 ms** (before 123.5 ms), still above the ≤100 ms target;
  click-to-list was **98.5 ms** (before 114 ms) against ≤150 ms. The
  lower-load before already met the 150 ms and first-frame bounds, so
  Round 36 is a first-paint/regression improvement, not an absolute win;
  "immediately" is not claimed. Round 41 tested splitting that deferred rest
  into 40 + 36 rows, one batch per animation frame, with the 2 s fallback
  flushing the remainder: frames were 24/64/100 and the deferred long-task
  max fell 53 → 0 ms, but the Playwright-visible messages→list rose
  86 → 97 ms (paired median +5) and click-to-list 94 → 102 ms (+13), so the
  change was reverted; the shipped list still appends the rest once after
  the double `requestAnimationFrame`. The ≤100 ms messages→list and 150 ms
  click-to-list bounds remain open; both measured arms were below the ≥15
  high-load band, and the low-load before already met ≤100 ms, so no
  absolute win is claimed. The list's own scroll appends the next
  keyset page through `next_before`, with a **Load more** fallback where
  `IntersectionObserver` is unavailable. An in-flight Load more owns a
  transient `morePromise`; a same-filter background refresh waits for that
  page and rebuilds from the settled rows instead of painting a stale list
  and leaving "Loading more…" behind. The mounted list is capped at **300
  rows** (three 100-row pages). The list is newest first, so the cap keeps
  the newest rows and never drops one the reader has scrolled past; at the
  cap the pager stops offering Load more and says "Showing the first 300 of
  N messages", so the screen never claims the mailbox ended. A folder,
  sender or read-state change is a new list with its own cap. Boot no longer
  schedules a Mail prefetch: the screen's render owns its list model, so the
  first visit reads its own first page. Hovering the Mail destination still
  does not warm it, for the same reason. Selecting a row replaces the URL id
  and loads the detail in place; the loaded pages and scroll survive.
  Rows show the avatar, sender, relative time, subject, one-line snippet,
  folder badge (in All), attachments and an unread dot. A cursor belongs to
  the role, store, sender and read state it was issued for, so a folder,
  sender or Unread change starts a fresh page.
- **Important.** `/api/priority?limit=50&filter=…&offset=…` with filter
  chips for all, reply, urgent, security, decision, deadline and workload.
  `security` is a topic, `suspicious` is the threat judgment: the security
  chip and the Verify action follow only the suspicious answer, and automated
  bulk mail never enters Important. Counts come from the same response and
  cached page, not one request per chip. Rows show sender, subject, the topic → subtopic path, snippet, action and
  waiting badges, why badges, time and Preview. The footer reports
  `shown of available`, and **Load more** asks the next offset and appends
  only unseen message ids; focus stays on the control. When the filter has
  more candidates than the top-200 ranked snapshot, the footer labels the
  pageable cap honestly. Detail opened from Important adds
  the action badge; it shows the message header, the HTML/plain renderer
  below, and the stored answers as friendly badges (not a raw table). The selected
  message is deep-linkable (`?mode=important&id=…`) and opens instantly
  from cache. The preview banner and **Uncertified preview** pill remain;
  the gate pill stays closed.
- **Conversations.** `GET /api/conversations` as one dense row per
  conversation: a friendly state badge (accent for waiting on you),
  organization, subject, the localized topic → subtopic path, up to two
  participants plus `+N`, a one-line snippet, unread dot,
  message/sent/received counts, relative time, attachment and due badges. All six state chips stay visible with their
  counts; search is folded over subject and sender. Selecting a row opens
  its latest message in the same detail pane without rebuilding the list;
  **Load more** appends older rows through the opaque cursor and keeps
  scroll. The page and the state pages are warmed at startup, and an
  expired page is served immediately while it refreshes.
- **Recent.** The existing bounded `/api/triage` pages (50 rows, load
  more), with coverage/staleness/failure/preview semantics unchanged.
  The coverage disclosure labels its stored route rows **Judged** and
  says they include answers and abstentions; certification is tracked
  separately. A partial coverage count stays a warning pill; only full
  coverage reads as done. If the summary read fails, the rows stay and
  the disclosure says coverage is unavailable instead of **Judged 0/0**.
  The first page and summary survive navigation.
- **Search results.** The shell bar opens this page with `q`; there is no
  second query input. `/api/search?q=` supplies the rows and detail, and the
  filter selects (mailbox, role, attachment) narrow the returned page. Rows
  render the returned plain-text `snippet` as-is (no highlight markup); a
  full-text hit that matched only a field has no excerpt, and a semantic-only
  hit shows the cleaned body preview.
- **Detail.** The same sandboxed `messageCardNode` used by Teach and
  Answer history: sanitized HTML in an iframe with no scripts, popups or
  forms, plain-text fallback, blocked remote images, and an explicit
  `Open in Teach` / `Ask about this message` action. Below the message is
  a compact **Conversation** card: friendly thread state, the subject
  path of the last substantive move, participants and message count, the
  messages earlier than the opened one, and a
  **Load older** control when history remains. The first page is anchored
  at the opened message, so an old mail never shows newer mail as
  "earlier"; when newer mail exists, a **N newer messages** line offers
  **Show latest**. A shared/delegate reply can change the state and shows
  a visibility note; its content is never displayed.
- **About this email.** Plain-language facets summarize category, topic,
  request, reply need, deadline, relationship, attention, information
  sufficiency and effort. The relationship facet is labelled
  **Relationship** (not "From"), and an uncertain information-sufficiency
  answer reads **Not clear enough** rather than a bare "Incertain". Raw
  signal names and percentages stay behind **Model details**.
- **On behalf of.** When the received message's claimed `From` address
  differs from its envelope sender, the header carries a warning badge
  **On behalf of {address}** whose tooltip names the actual sender. The
  sender line keeps the envelope sender; the badge never rewrites either
  address.

### Companies

`#/companies` is the organization explorer. The left pane is a searchable
ranked list of external companies (label, domain, addresses, messages, last
exchange). The ranking includes a company with mail in either direction, so
a one-way correspondent is visible instead of waiting for the owner's first
reply. When a search hides the selected row, a note says so; the profile is
not silently showing an out-of-filter company.

The right pane is one profile surface with this order: identity header
(address book or domain, domain chips, address counts, an **Ask about this
company** action and the view timestamp), **Relationship** (deterministic
facts: date span, message direction, 90-day activity, open-loop counts and
one **Next step** derived from stored mail: an owed reply first, then the
nearest deadline, then the next meeting, then a thread waiting on them,
then the newest message), an all-time KPI strip, open
threads as a two-column **You owe / Waiting on them** ledger with the
resolver's reason, due commitments, active meetings, recent mail,
addresses, then the zero-filled 12-month volume chart. Book-wide address
book coverage lives only in the list pane, never repeated per company. A
profile with more rows than its display cap states “Showing X of Y.”

A domain organization is labeled from local evidence (business vocabulary
and observed person names), never from a web lookup; an unrecognized brand
stays one title-cased word. A deep link `#/companies?key=…` selects one
profile, including a company outside the ranked page (no row is selected
then) and including a profile whose ranking read failed or is empty;
opening a thread or message goes to Mail at that message. Selecting a
company swaps only the profile pane: the list, the page and the list's own
scroll position stay mounted and unchanged. Selecting the same row again
retries a failed profile. Visible rows are prefetched in one batch; the
list and profile use row indices as their DOM identity, never raw company
keys. Back/Forward between profiles is the same detail swap. Personal and
free-mail addresses with no declared company are not grouped, and the empty
state says so. A company with a list error is not cached as a route, so a
Back gesture cannot resurrect a failure the next read can fix.

### Teach

`#/labels` is a focused, one-message-at-a-time workspace. The header has
the mode segmented control (**Label** / **Review Gemma**), a link to
Answer history, and a question chip strip with a visible scroll
affordance. The selected question has a plain-language title, a one-line
hint, `Your answers / Needed` with a progress bar, the queued count, and
an **Advanced** disclosure for the requirement/judge details. A question
whose route precision cannot reach its target is not asked for more
labels: both stages follow the route certification, not the layer-1
training diagnostic, and its precision blocker and re-run/review remedies
are in Learning.

- **Question language.** The 17 questions, their hints, the common choice
  option names and the score endpoint labels are UI-owned EN/FR copy.
  The model-facing English instructions and option descriptions stay
  behind **How to decide**; the posted values never change.
- **Layout.** On wide screens the message is on the left (scrollable) and
  the answer panel on the right; both fill the route height and keep their
  own scrollbar. The panel has a task strip (position, Previous/Next), the
  question, the suggestion, the controls and a sticky action bar. The
  action bar's measured height becomes the panel's bottom padding, so the
  last option always clears it. The question strip is one horizontal row
  with its existing chevrons on desktop and wraps on narrow screens. On
  narrow or short viewports the panels stack and the header compacts.
- **Review flow.** The question and hint come first, then Gemma's
  suggestion as a first-class card with the readable value and **Use
  suggestion**. Choosing it posts one manual label and advances
  immediately; the alternatives remain available below ("or choose
  another answer"). **Save and advance** is disabled until a value is
  selected. **Skip** advances without writing. The last task shows a calm
  completion state with the next question (by remaining need) and Back
  home. The queue and progress update locally; a full route remount is
  not required.
- **Keyboard.** `1…9` select an option or level, Enter saves (or uses the
  suggestion in Review), `S` skips, `G` uses the suggestion, `?` opens
  the shortcut help. The shortcuts do not fire while typing in a field.
  Back to a cached workspace re-installs the same handler and observers on
  the mounted task. The next task heading receives focus after a save.

### Test preview (uncertified)

`learning.test_mode` is an explicit, reversible preview that lets the
owner test the automatic questions and answers while the learning gate
stays closed. It is not a second gate and never certifies anything.

- Home's learning rail and the Settings Learning group offer **Test
  preview**. The keyboard-accessible dialog explains that the preview
  unlocks Mail, Ask, the ordered answers and Search, that the gate stays
  closed, that answers are uncertified, that nothing is accepted or
  certified, and that it is reversible. Confirming `PUT`s `true` to
  `/api/settings/learning.test_mode` through the ordinary settings API
  and refreshes Home; there is no separate action endpoint.
- While the preview is on, Home shows a compact warning banner with
  **Open Mail** and **Disable preview**. The topbar still says **Gate
  closed** and a separate warning-style **Preview** pill appears next to
  it; preview never uses the open/ok style. The true manual meter and the
  gate/label requirements are untouched.
- Mail, Ask and Search render instead of the closed locked screen. Mail
  carries the persistent banner reading "Test preview — automatic
  answers are uncertified. The gate is still closed." and the detail note
  "The values below are the local model's best guess, shown before
  certification. They are not certified." The triage topbar and detail
  show a warning-style **Uncertified preview** pill/badge instead of the
  route pill; preview wins over every certification count. Outside the
  preview the route pill counts the questions stage: **Certified route**
  only when every question is certified, **N of M certified** for a
  partial route, and **Certification unknown** when the Hub has not
  loaded. A direct route awaits that shared Hub snapshot before it
  paints, so it does not keep an early unknown count; partial is the
  warning style and certified is the accent style. The screens never
  claim accuracy or certification.
- The answers shown in preview are the layer-1 model's threshold-free best
  guess, stored separately from the route. `learning.Machine.PreviewAnswers`
  owns the one rule: preview values apply only while the test setting is on
  and the real gate is closed. Once the gate opens, the certified route wins
  even with the setting still on, so no screen relabels a certified answer.
  Every displayed preview flag asks this rule: Conversations and Q&A report
  it, the Brief keys its composition by it, and `/api/learning` and
  `/api/gate` publish it instead of the raw `learning.test_mode` setting. A
  question the route abstained on for another reason (below target, no answer
  or refused) stays abstained, and every coverage counter and the summary
  counts remain the stored route's (the summary carries a note saying its
  counts are the certified route's, not the preview's). While preview answers
  apply, the ordered Important feed is derived from the same values and is
  labelled **Preview** on every item. Preview never accepts a label, certifies
  a question or feeds the gate, the judge or the report.
- Ask is explicit about its modes: structured answers stay local;
  generated answers name the Gemma provider and show the privacy note. A
  missing key/provider returns retrieval-only sources with
  `qa_unavailable`; a failed call or an ungrounded answer returns
  `qa_generation_failed` or `qa_no_grounded_answer`, and a warming index
  returns `qa_index_warming`, never an invented answer.
- Turning the preview off from the Home banner, the learning rail or
  Settings restores the locked Mail/Ask/Search screens at once; with the
  gate still closed, uncertified questions abstain again and no preview
  answer is read. The setting is ordinary configuration: it is not stored
  as a learned value and never feeds labels or certification.

### Answer history

`#/answers` is the label-history workspace, linked from Teach, Ask and
Home but not a primary nav item. It loads `/api/labels/summary`, the first
`/api/answers` page (50 items) and the profile questions in parallel.

- **Header.** "Answer history" with one plain sentence: these are your
  answers and Gemma's current suggestions, newest first; corrections
  happen in Teach. Primary **Review suggestions** and secondary **Open
  mail** actions. The topbar has three meaningful counts (from you,
  suggestions, to review), not a dashboard.
- **Filters.** A question select using the human question titles (raw
  name as a small secondary label), a source segmented control (All /
  You / Gemma), and **Review suggestions** using the selected question or
  the top question by remaining need. Changing a filter rewrites
  `#/answers?question=…&source=…`, closes the detail and starts a new
  cursor.
- **List.** Rows show the message number, human question title (with the
  raw name in small text), readable value, source chip, relative time and
  **Open**. Values use the same formatter as the review card: localized
  option name, Yes/No, or `level N · level text`. There is no subject,
  sender or body in the list.
- **Paging.** **Load more** sends `next_cursor`, is disabled while loading
  and disappears at the end; the list reports how many answers are shown
  and names the end state. An empty filter explains that no current
  answers match.
- **Detail.** `#/answers?message=<id>&question=<q>` fetches
  `/api/messages/{id}?for=answer` and `/api/answers?message=<id>&limit=100`
  in parallel. The message header and body use `messageCardNode` and the
  sandboxed renderer below. Every label for the message is grouped by
  human question title with its source and time; the requested question is
  marked. **Review / correct in Teach** opens the review flow for that
  question. Answer history never writes a label, so there is one write
  path.

### Learning and Settings

`#/learning` leads with a plain-language state ("A few answers will
unlock learning", "Learning", "Certification in progress", "Everything
is ready"), the gate and preview pills, a certified-question count, and
one to three concrete next actions (Answer a question, Review suggestions,
Get Gemma suggestions). An open gate with a partial route says how many
questions are certified; its state pill reads **Partly certified**
(**Certification partielle**) instead of **Ready**, and the rest abstain
until they certify. It lists the questions to start with using the
same human titles as Teach. **Step by step** maps the nine stages to plain
labels and hints (Mailboxes, This computer, Mail sync, Evidence,
Embeddings, Your answers, Local model, Calibration, Automatic answers)
with real progress/ETA. While an index run is active, the Embeddings step
shows the run's live `done / total messages` plus its embedded, unchanged
and failed counts, then settles on the full coverage when the run ends.
While a layer-1 run is active, the Local model step shows the run's live
`done / total questions`, its phase (preparing the features, building the
targets, fitting, cross-validating, measuring, certifying, settling,
answering), the question it is on and its ETA; the phase and question come
from the run's own step reports, not from the stored report, which is only
written when the run ends.
Raw stage ids, blocker codes, jobs and logs live under **Technical
details**. The screen never looks open while the gate is closed.

`#/settings?tab=general` opens in **Simple** view: eight everyday settings
in four groups (language and time zone; Ask enabled and its context size;
sync detail level and the two folder-role maps; the learning test preview),
with the localized description as the main text and the raw key as a small
mono label. Its default read is the `?keys=` projection of those eight, and
the "advanced options are hidden" count comes from the response's `total`
until the full list arrives.
**Advanced**, a search query, Providers and Runtime load the full 250-setting
list once and reuse it; returning to Simple renders the projected payload.
Searching always covers every setting and says so. Apply/Reset and
restart-pending behavior are unchanged.

`#/settings?tab=providers` owns provider records and the four roles: only
records the owner added are listed, each with a test, edit and delete
action, and the six everyday fields (label, protocol, server URL, path,
model, API key) plus one closed **Advanced** disclosure for the record id,
key name and capability fields. A save error opens **Advanced**, because a
message can name a field that only lives there. A key is written to the
secret store and never shown again. **Fetch models** reads the provider's official model list,
shows each model's context, output ceiling, prices and capabilities, and
prefills the editable record without saving it. It sends no inference
request and never replaces a hand-typed label.

`#/settings?tab=runtime` shows one local engine card, not a build table.
The Windows product publishes the single Vulkan build; the card names it
(`llama-server` and its version), the embedding model from the running
status or the selected managed embedder, and one state pill, with PID,
threads, since, restarts and disk size only when they apply. The engine
downloads, verifies and starts by itself when embeddings need it; there is
no variant picker, no install form, no CPU row and no per-row **Verify**.
**Refresh** is the only action; **Reinstall / Repair** sits inside one
closed **Advanced** disclosure. A remote embedding provider means the
local engine is not selected, and the card links to Providers instead of
claiming it runs.
When the build declares a machine capability the machine does not have
(Windows: Vulkan), the card shows the existing **Unavailable** pill and one
translated note (`settings.runtime.unavailable.vulkan` for no usable
device, `.probe_failed` when the check itself failed). It hides **Reinstall
/ Repair**, the first-use copy and the engine-size offer, because no
download can succeed; **Refresh** stays, invalidates the cached
`/api/sidecar` status and re-runs the probe. There is no CPU offer and no
variant picker for this state.

### Instant workspace

The shell is a single mounted document; navigation does not reload the
page or rebuild the sidebar. A TTL cache stores GET responses with
synchronous reads, in-flight deduplication, a 24 MiB byte budget with LRU
eviction, and epoch-guarded invalidation that drops only the reads it names;
one response above 8 MiB, or one whose compiled sender sheet is above 64 Ki
characters, still answers its caller but takes no cache slot. A bounded DOM
cache (24 routes) keeps the rendered node and the scroll position of every
named section. Revisiting a route appends its cached node
immediately, restores each section's own scroll and refreshes in the
background; the fresh node replaces it only if the route is still active and
focus is not in an editable field. A cached node is markup only: leaving a
route disconnects its document keyboard handler and observers. Each screen
declares one attach hook on its screen node; Companies restores its row
observer and Teach re-installs the mounted task's `1…9` / Enter / `S` / `G` /
`?` handler and both resize observers when the router re-appends the node. A
cached mount also returns focus to `#main` when the navigation started from
the sidebar, matching a fresh mount. A live language switch drops every
cached node and forces one full rebuild of the mounted body, because the
route identity alone would keep the old catalog's markup. The shell warms
the Home shell and its ranked signals and agenda once the boot contract
reports `ready` (or immediately against an older server with no `/api/boot`);
every other screen reads on first visit. The server opens its listener and
writes the runtime descriptor first; the desktop window opens against that
address while the derived warm runs in the background: the Mail first page and
the learning status beside the priority ranking, every conversation state page,
commitments, companies, agenda, hub counts and the Brief, then the semantic
prepare.
`mailsense ready` is logged when that warm finishes, not when the port
opens. After readiness an expired derived view is served stale while one
background refresh runs, so a 15-second boundary never freezes a route.
Cold first load and cold caches still make one local request. The shell
mounts the route-specific skeleton and opens the event stream before it
awaits `/api/status`, the language setting and `/api/gate`; `/api/learning`
and `/api/jobs` update the chrome after the first content paint, and the
first route mount reuses the gate snapshot the boot read produced instead of
sending `/api/gate` a second time. Navigation is live during the warm. The
topbar shows a small updating indicator for background refreshes instead of a
full-screen spinner.
A refresh that arrives during All-Mail Load more waits for the in-flight
page and appends the settled screen, so the indicator and the list share one
model instead of rebuilding a stale list. Writes invalidate the affected
keys and route nodes. An invalidation drops only the cached reads and
in-flight requests its prefixes name; an unrelated read that is already
running keeps its waiting readers on the same request.

### Request bounds and failed actions

Every `request()` runs under one `AbortController` and one timer, cleared in
`finally`. Reads and short writes default to 30 s (`REQUEST_TIMEOUT_MS`,
comfortably above the measured live cold reads of ~8 s for triage and ~9 s
for priority); Ask send and job starts pass `LONG_REQUEST_TIMEOUT_MS` (5
minutes). An abort maps to `ApiError(0, {code:"timeout"})`. A timed-out read
paints the shared error surface with Retry; a timed-out mutation reports
**No response yet**, re-reads the shared views, and never claims the server
rejected it. The live event stream uses `EventSource`, not this client.

Top-level click actions run through `runAction`: `withBusy` restores the
button in `finally`, and a rejected action reaches the one toast owner
exactly once. A form that already paints its failure inline catches it
inside the action, so nothing double-reports. Sources Refresh and Logs
Refresh use that boundary; a failed Sources refresh is no longer silent.

### Live updates and freshness

The shell opens one `EventSource` (`/api/events`) for the lifetime of a
tab and does not poll. Each event either refreshes the active read-only
screen (through `refreshActiveRoute`, with its route-cache semantics and
focus guard) or only invalidates caches for the next read. A `dropped` event
runs one coalesced live resync; if it lands on All Mail while Load more is in
flight, the refresh waits for that page before replacing the screen. Ask is
never rebuilt by these events, and a field that has focus blocks the DOM
replacement, so typing and the composer survive a background update.

| Event | Caches invalidated | Active screen refreshed |
| --- | --- | --- |
| `learning.status` | the authoritative status payload is replaced; `scheduleLearningRefresh` forces `/api/learning` and invalidates hub, jobs, label summary, answers, commitments, brief, companies and threads | Learning, Home, Brief |
| `index.progress` | none; the payload paints the cached embeddings stage and the mounted step in place (`done / total`, embedded, unchanged, failed); a missing cached status forces one read | Learning only (in place, no fetch) |
| `index.finished` | status, search, learning, hub; route nodes for Learning, Home, Brief, Companies and Mail | Learning, Home, Brief |
| `sync.mailboxes` | the payload replaces the mailbox list; mailboxes, status, learning, hub; Sources/Learning/Home route nodes | Sources, Learning, Home |
| `evidence.finished`, `layer1.finished` | status, learning, hub, labels, answers, triage, priority, conversations, commitments, brief, companies, threads and search; route nodes for Learning, Home, Brief, Answers, Companies, Teach and Mail | Learning, Home, Brief, Answers, Companies |
| `feedback.label` | the same derived reads as a finished stage, without search | Learning, Home, Brief, Answers, Companies |
| `config.changed` | settings, status, learning, hub, priority, conversations, commitments, brief, companies, secrets, providers and threads; Settings/Home/Brief/Mail/Companies/Ask/Learning route nodes | Settings (Providers and Runtime included), Home, Brief, Learning |
| `sidecar.state` | the local-runtime read | Settings → Runtime when visible |
| `jobs.update` | the payload is merged into the one shell job model (`state.jobs`) and the strip paints in place with no fetch; the learning refresh set and the Sources route are still scheduled | Learning, Home, Brief; Sources |

Teach and Mail are interactive: a finished stage or a new label
invalidates their caches and route nodes, but the mounted task is not
replaced under the user. Mail search results therefore refresh on the
next query or navigation without resetting the search field. The Learning
step, Answers, Companies, Home and Brief are read-only views and refresh
in place. `index.progress` is the only direct paint of index progress; it
never fetches, so it adds no refresh beside `jobs.update`.
`learning.status` remains the authoritative status: the index run's final
coverage arrives through it, not through the last progress payload.

A second strip sits below the job strip while the app is warming. It polls
`GET /api/boot` every 400 ms (no cache), shows `preparing`, the real
`done / total`, a native `<progress max=total value=done>` and the current
view name from the same twelve identifiers the API reports (`mail` first). At `ready` with
`failed == 0` it hides. At `ready` with `failed > 0` it stays visible in a
warning state: the title names the failed count, the strip lists the failed
view labels and a Dismiss action (the existing job-strip key) hides it; the
core prefetch still starts exactly once. It also hides on an older server's
404 (treated as ready), on 401, on shutdown and on unload. A network or 5xx
failure keeps polling to a bounded deadline and then hides without claiming
readiness, so the route's own error surface is the only thing that speaks.
The strip is outside `#main`, so it survives route remounts and is never
replaced by a fake list, zero count or "no mail".

`jobs.update` feeds the one global job strip between the topbar and
`#main`: each running job shows its name, live `done / total`, percent,
progress bar, message and a cancel action; a failure stays until dismissed
and a finished job lingers four seconds. A job that reports a stage (the
local classifier does) also shows the translated phase, the item it is on
(a question name) and its position within the phase (`step 2 / 5`), plus
an ETA measured from the job's own average rate. Every string the chip
adds is a catalog key (`jobs.stage.*`, `shell.job.step`), never a raw
sentence; only the question name is data and stays untranslated. The
payload is a complete `jobs.Info`
snapshot, so a progress event never fetches `/api/jobs`. The strip lives
outside `#main`, so refreshing or remounting a screen cannot remove it,
and it re-renders itself when the interface language changes.

The stream has one gap-repair path. A transport `error` marks the stream
as gapped; the next `open` schedules one coalesced resync after 400 ms
that forces `/api/status`, `/api/learning` and `/api/jobs`, invalidates the
live read caches and route nodes, and refreshes the active screen through
the existing focus guard. A synthetic `dropped` frame (buffer full, bus
drop, log-ring rotation or sequence reset) does the same, because the
missed events are not replayed. The initial job snapshot is read before
the stream opens, so it cannot race the first event. The footer's
connection label remains the only indication of the stream state; there
is still no timer-driven poll.

The backend side of this contract is in `docs/DEVELOPMENT.md`: an active
index run reports live `done / total` (its selected work, or the eligible
count until work is selected) and its embedded/unchanged/failed breakdown;
after completion or cancellation the stage reads the full coverage again.
Progress invalidates the learning status at most once per second, and
`index.finished` always rebuilds the final status. There is no global UI
poll: the shell re-reads only on navigation, an action, or one of these
events. The two-minute index reconcile is server-side maintenance; when
it starts a normal job, the same events update the screens.

At 1280 px Home is a three-column Brief/Next/Important grid, collapsing
to two below 1200 px; Brief is a two-column working grid; Mail is
master/detail; Teach is message + answer panel. At 1024 px Home stacks,
Brief becomes one column, Mail becomes list-then-detail and Teach stacks.
No primary screen scrolls horizontally; chips and filter controls wrap or
scroll with a visible affordance.

### Scrolling

The shell owns no scrollbar. `html`, `body` and `#main` are clipped; the
topbar, sidebar, job strip and boot strip stay fixed while a route is
mounted. On a wide window every route names its bounded sections with
`data-scroll-key` and the shared `.section-scroll` rule
(`overflow-y: auto`, never horizontal). Home's three columns, each Brief
card body, the Mail list and detail, the Companies list and profile, Ask's
transcript and history, Teach's message and answer panes, and Logs each
scroll only when their own content overflows. Document-shaped routes
(Sources, Settings, Learning, Answers) scroll one named route body under a
fixed header or side navigation.

Below 1024 px the stacked layouts use one bounded route-body scroller
(`.screen`) instead of squeezing every card into a tiny pane; the shell
chrome still does not scroll. Every named region is focusable with
`tabindex` so keyboard users can scroll it, and the route DOM cache restores
each section's scroll position independently. The Playwright gate checks
all routes at 360, 768, 1024, 1025, 1280, 1440 and 1920 px: `#main` never
scrolls, the document never scrolls, and no route body or section grows
sideways.

## Message rendering

Teach (`?for=label`), the Mail detail panel (triage and search) and the
Answers detail all call the same `messageCardNode`/`mailBodyNode`, so one
renderer serves every message surface.

- `view.body_html` is the compiled fragment described in `docs/API.md`;
  `app.js` never inserts stored mail HTML and never assigns HTML through
  `innerHTML`. The fragment goes into the `srcdoc` of an iframe with
  `sandbox="allow-same-origin"` and no `allow-scripts`, `allow-forms`,
  `allow-popups` or `allow-top-navigation`. The frame's own document adds
  `default-src 'none'; style-src 'self' blob:; img-src 'self' data: blob:;
  script-src 'none'; form-action 'none'; base-uri 'none'`, and the parent
  CSP includes the same `style-src` and `frame-src 'self'`.
- `mail.css` is the base reading sheet. `view.body_css` is linked after it as
  a per-message `blob:` stylesheet, so sender typography, colour and table
  layout apply without an inline style attribute. The blob URL is revoked
  after the sheet loads; each rerender builds a fresh one.
- The frame keeps same-origin access for two jobs: measuring the body to
  size the frame with its `height` attribute (inline styles are forbidden),
  and intercepting link clicks. A click never navigates the frame or the
  app: it opens a dialog with the exact destination and a **Copy link**
  button. This build has no OS opener seam, so the user opens the copied
  address in their own browser.
- Plain text (`body_format: "text"`) arrives as the escaped
  `message.body` in one `pre`: blank lines, indentation and quote prefixes
  are preserved, and only validated `http(s)`/`mailto` addresses are
  linkified. A message with no body says "(no body text)".
- Remote images are never loaded. Each becomes a sized "Remote image
  blocked" placeholder and the card reports how many were blocked; a 1×1
  remote pixel is suppressed entirely. There is no opt-in to load them.
- A `cid:` or `Content-Location` image with stored bytes arrives as a
  `data-inline` span. The shell fetches
  `/api/messages/{id}/attachments/{position}` with the bearer header, turns
  the response into a short-lived `blob:` URL and swaps in the image; the
  launch token never appears in an image URL. An inline image without bytes
  keeps an "Inline image unavailable" placeholder.
- Attachments are metadata chips (name, size, content type, inline marker).
  Bytes are stored only for safe inline rasters (PNG, JPEG, GIF, WebP) within
  the `mail.inline_assets.*` budgets; other attachments stay metadata-only.
- `body_html_truncated` and `body_truncated` become warning badges next to
  the subject. The body document is `dir="auto"` with
  `unicode-bidi: plaintext`; subject and sender also carry `dir="auto"`.
- The body is capped at 760 px and centered. A body taller than 70% of the
  viewport is clipped behind **Show full message** / **Collapse**.

## Interface language and formatting

The shell ships English and French. The `ui.language` setting is `auto`
(default), `en` or `fr`; it lives in `settings.json`, accepts the
`MAILSENSE_UI_LANGUAGE` environment override and never requires a restart.
The catalogs and the runtime are loaded before `app.js`, so the token and
loading screen are localized from the browser locale before any API call;
the saved setting is applied as soon as `/api/status` and the setting read
complete.

Resolution order:

1. An explicit `en` or `fr` wins. Its format locale is the system regional
   locale when that locale has the same language, otherwise `en-US` or
   `fr-FR`.
2. With `auto`, a detected system language of `en` or `fr` selects that
   catalog and keeps the compatible regional format (`fr-CA` stays
   Canadian French).
3. When detection returns nothing, the first supported `navigator.languages`
   entry is used.
4. Otherwise the interface is English and Settings shows a non-blocking
   **French is available** offer. A known but unsupported system language
   (for example `de-DE`) is never turned into French from the browser
   locale; the offer is the explicit path to French.

`internal/oslocale` performs detection once per process: Windows uses the
display language (`GetUserDefaultUILanguage` through `LCIDToLocaleName`)
with `GetUserDefaultLocaleName` as its regional fallback; Linux/Unix reads
`LC_ALL`, then `LC_MESSAGES`, then `LANG`, then the `LANGUAGE` preference
list. `C`, `POSIX` and malformed values mean "not detected"; a set
higher-precedence value wins even then, so lower variables do not override
it. The
server exposes the result as `system_language` and `system_locale` in
`/api/status` (`docs/API.md`); the browser is only the fallback signal
because a WebView2 runtime can disagree with the Windows display language.

Changing the control in Settings applies immediately: the current route
re-renders in the new catalog, `<html lang>` and `document.title` change,
and no page reload or restart occurs. A language-only `config.changed`
event re-reads the setting and applies it without the generic
"settings changed" toast. Reset returns to `auto`. A language change closes
an open dialog instead of leaving its controls in the previous language;
the dialog's pending action is not applied.

Dates, times, numbers, percentages, currency, durations and byte sizes use
`Intl` with the selected format locale. Owner-facing dates use the confirmed
`time.zone`. On the first run, while the Learning machine stage is blocked
on `time_zone_unconfirmed`, the shell confirms the browser's IANA zone
(`Intl.DateTimeFormat().resolvedOptions().timeZone`) through
`POST /api/learning/zone`; the manual dialog remains the fallback when the
browser refuses the zone or the call fails. Before confirmation, Learning
discloses that the machine zone is in use. Log timestamps always use the
machine zone. French output
uses 24-hour time, a decimal comma, grouped digits and a no-break space
before `%`; byte units are `o`, `ko`, `Mo`, `Go`, `To` and use base 1000.

Question names, instructions, criteria, option names, score levels and all
other taxonomy text are model-facing English data. They are displayed
unchanged in both languages and are not part of the catalogs; translating
them would change the profile or the taxonomy fingerprint. Codes, setting
keys, API paths, message ids and stored values also stay canonical.

## Shared chrome

- The sidebar groups the primary work (Home, Mail, Ask, Teach, Sources,
  Settings, Logs) above a **System** group with Learning. Teach carries
  the review badge. The footer shows the global SSE state (live,
  reconnecting, offline), the version, the launch id prefix and the data
  directory (`title` carries the full path). A warning badge on Learning
  counts open blockers.
- `Ctrl+K` (or `/` outside a field) focuses the Ask input; the sidebar
  shows the hint. The Ask screen is a real route, not a redirect.
- Toasts are the only transient error surface. API errors use the shared
  `{code, stage, subject, message, actions, measure}` problem shape; a
  `401` never becomes a toast.
- **Shut down** asks for confirmation, explains that learned data stays,
  then posts `/api/shutdown`. The screen becomes a terminal "shutting
  down" state.
- Keyboard: the skip link focuses `main`; every control is a native
  button, link, input or select; log rows toggle with Enter or Space; the
  native `<dialog>` traps focus and closes on Escape; focus returns to the
  trigger after a dialog. Teach adds `1…9`, Enter, `S`, `G` and `?` inside
  the answer workspace, never while typing in a field.
- Navigation is client-side and instant after the first visit: the
  sidebar/topbar stay mounted, a TTL cache answers GETs synchronously,
  and the route DOM cache restores the previous node and every section's
  scroll before a background refresh. A cold cache still makes one local
  request and shows a route skeleton; the topbar shows a small updating
  indicator for background work.
- The layout is checked at 1440, 1280 and 1024 px and at 768 px height:
  Home is three-column, then two, then stacks; Mail is master/detail then
  list-then-detail; Teach is message + answer panel then stacked and
  compacted. Reduced-motion preferences disable animation.

## Tables

Every data table that offers sorting uses one layer in `app.js`: the pure
`sortableOrder` / `sortableInferType` core plus the `sortableTable`
builder. Columns declare a key, localized label, type (`auto`, `text`,
`number`, `date`, `email`, `phone`), alignment, sortability, default
direction and `value`/`render` functions. Renderers format the human
value; sorters use the raw `value`, so numbers compare numerically, dates
compare as instants (RFC3339, calendar days, months, ISO weeks and Ask
weekday buckets), e-mails compare case-insensitively and phone numbers
compare by normalized digits. `auto` samples up to 40 non-empty cells and
requires every one to match, in the order date, e-mail, phone, number,
then text. Empty cells stay last in both directions; ties retain the
server order; the input array is never mutated.

- **Headers.** Every sortable column is a `<th scope="col">` with
  `aria-sort="none|ascending|descending"` around a native
  `<button class="sort-button">`. The ascending/descending indicator is
  CSS only and `aria-hidden`; the button keeps focus after activation.
  The rank column is not sortable, so the server ranking stays the
  default order until the owner chooses a sort.
- **Scrolling.** Each table wrapper is `tabindex="0"`, `role="region"`
  with a localized `aria-label`, plus an inset focus ring, so a wide
  table can be scrolled from the keyboard. Captions are always present
  (visually hidden) and every table has one.
- **Movement.** Sorting moves existing `<tr>` nodes instead of rebuilding
  rows, so listeners, checkbox state and focus survive. Optional trailing
  detail rows move with their parent row. Answers pagination appends new
  rows and re-applies the active sort; the footnote says that sorting
  covers the loaded pages.
- **Persistence.** A table may set a `persistKey`; only
  `{key, direction}` is stored at
  `localStorage["mailsense.table-sort.<key>"]`. Corrupt or unavailable
  storage is ignored. Markdown answer tables never persist.
- **Answers.** Generated text can contain a pipe table. It is recognized
  only when a header line is followed by a matching `:?-{3,}:?` separator
  row. Fenced code is ignored; pipes inside backticks or escaped with a
  backslash do not split cells; tables are capped at 12 columns and 200
  rows, with the overflow retained as text and a truncation note. Cells
  render through the same citation-aware text node (never raw markup), so
  `[n]` stays a chip. Plain answers keep their existing paragraph.
- **Company addresses.** The table is name, address, phone, messages, last
  contact and role. The messages cell carries the total plus the observed
  `sent · received` direction counts; a declared-only address shows **No
  mail yet**. The screen calls these rows addresses, not people, because one
  person may use several mailboxes. The phone is the address book's display
  form (business number preferred, mobile as the fallback) and stays blank
  when the source has none; it is sortable as a phone.
- **Surfaces.** Ask contact/company/time results, company people, Sources
  mailboxes and source registry, answer history, and the error problem
  list use the component. Logs remain chronological.
- **Design.** `.companies-people-table` is a `.data-table` modifier; all
  wrappers share the border, background, header typography and cell
  padding. Selected, hover and `:focus-within` states are consistent, and
  sort state is never conveyed by color alone. The Sources, Answers,
  company-people and error tables carry accessible captions.

## Design tokens

Tokens live once, in `:root` of `app.css`:

| Token group | Values |
| --- | --- |
| Surfaces | `--bg: #f4f1e9`, `--surface: #fdfbf6`, `--surface-raised: #fffdf8`, `--surface-sidebar: #efebe1` |
| Ink | `--ink: #1d2421`, `--ink-soft: #43504a`, `--muted: #737c76`, `--faint: #9aa39c` |
| Lines | `--line: #e2ddd1`, `--line-strong: #d3ccbd` |
| Accent (the only one) | `--accent: #1e5a46`, `--accent-ink: #174635`, `--accent-soft: #e6efe8`, `--accent-line: #bfd4c7` |
| Status | warning `#8a5a14` on `#f7eedb`, danger `#a23b2e` on `#f9e8e4`, success `#2f6248` on `#e7f0e9` |
| Shape | `--radius: 14px`, `--radius-sm: 9px`, `--radius-pill: 999px`, soft two-layer shadow |
| Spacing | `--space-1…7`: 4, 8, 12, 16, 22, 30, 40 px |
| Type | `--font-ui`: Segoe UI Variable/Segoe UI/Avenir Next/system; `--font-display`: Iowan Old Style/Palatino/Georgia serif for screen titles and answer headlines; `--font-mono`: Cascadia/SF Mono/Consolas; 14.5 px body, 28–32 px display |

The visual language is warm paper, deep green, calm editorial: hairline
borders, 9–14 px corners, display serif only where it carries meaning, one
accent, tabular numbers for counts, no gradients, no emoji, no external
images or fonts. Cards exist to group one job, not to decorate a
dashboard; inner controls use the smaller radius. The message iframe owns
a parallel token block in `mail.css` (`--mail-paper`, `--mail-ink`,
`--mail-ink-soft`, `--mail-muted`, `--mail-line`, `--mail-accent`, plus
quote, code and notice values), with matching `[data-theme="dark"]` values.
The wrapped document sets `data-theme="light"` and `color-scheme: light`
today; the dark palette is ready for the shell's dark mode and is selected
by that attribute, never by a media query.

## Adding a screen

1. Add the route to `SCREEN_IDS` in `app.js` and its `screen.<id>.title` /
   `screen.<id>.eyebrow` keys to both catalogs. The document title is derived
   from the active screen's dataset, so it has one formatting owner. The
   empty hash and unknown routes belong to Home; a new screen is reached by
   its own hash.
2. Add a sidebar link with a new `<symbol>` in the sprite and a
   `<use href="#i-…">`, and its `nav.<id>` key to both catalogs. No icon font
   or image file.
3. Write `renderName(route, seq)` next to the other screens. It returns a
   DOM node built with `h()`, reads through `requestCached()` (or a typed
   loader with a TTL), and checks `seq !== state.renderSeq` before returning
   so a slow response cannot overwrite a newer route. A screen that is safe
   to revisit should mark its route/data for the route DOM cache and
   invalidation; never keep a stale node after a write. A screen with a
   skeleton must make the same check before it fills the skeleton.
4. Dispatch the route in `renderRoute`'s switch.
5. Add the route to the screens table here and to the browser sweep used
   for verification. The Go tests check that the embedded files exist, the
   shell references its assets, the sidebar order and default route, both
   catalogs have every referenced key, `app.js` passes no hardcoded prose
   to a UI sink, and `app.js`/`app.css` contain no network dependency.

## Serving, caching and tests

- `/` serves `index.html` with `Cache-Control: no-store`; assets use
  `no-cache` plus a content-based strong `ETag`, so a binary upgrade is
  picked up on the next request. `/index.html` permanently redirects to
  `/`; unknown paths are plain 404s; other methods are 405 with
  `Allow: GET, HEAD`.
- The API adds `Content-Security-Policy: default-src 'none'; script-src
  'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self';
  connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action
  'none'; frame-ancestors 'none'`, plus `nosniff`, `no-referrer`, `DENY`
  framing and the cross-origin isolation headers.
- `go test ./internal/render ./internal/ui ./internal/desktop
  ./internal/api ./cmd/mailsense` covers the sanitizer and its synthetic
  corpus, the handler, redirects, conditional GET, offline/token
  invariants, URL validation, window seams, the questions route and the
  message body fields. `internal/ui` also parses both catalogs for parity
  and placeholders, checks the Home/Ask/Mail/Teach routes and sidebar
  order, scans
  the shell for hardcoded prose, and runs the real `i18n.js` under Node
  when Node is installed (resolution, plurals, numbers, dates and zone
  fallback).
- `scripts/test-ui.sh` runs the committed Playwright suite in `e2e/`.
  Every worker starts the real binary on an isolated temporary data
  directory, so the launch token, Bearer auth, CSP, static caching, 401
  handling and SSE are production code. Only content endpoints that need
  mail are fulfilled with synthetic fixtures; no personal mailbox data is
  used. It covers the token handshake, all primary routes, Ask follow-ups
  and clarify, Mail list/viewer/search, Companies, live Learning progress
  and the log tail. Traces and video stay off because the SSE URL carries
  the launch token.
- Manual verification, with a disposable data directory populated by a
  sync or a synthetic fixture import. This sweep still owns Windows-only
  and visual checks (WebView2 pixels, DPI, icon, Outlook COM, DPAPI),
  which Chromium cannot cover:

  ```sh
  scripts/test.sh
  MAILSENSE_DATA_DIR=/tmp/mailsense-ui go run ./cmd/mailsense
  # read "url" and "token" from /tmp/mailsense-ui/runtime/api.json, then open
  # <url>/#token=<token>; the token must stay in the fragment
  ```

  1. Home: the token lands on `#/hub`. With preview off, the closed-gate
     cockpit shows the true manual/needed meter and suggestion count, no
     subject/sender/snippet, and its review CTA opens Teach filtered. With
     preview on, Home shows the Brief header and the Brief / Next /
     Important grid, with recent activity and the learning rail behind
     **More**; the Important column is visible without page scrolling at
     1440×900 and 1280×768, and it scrolls only inside its own card when
     the list is longer; the gate pill stays **Gate closed**, the
     warning-style **Preview** pill and banner are present. Repeat in
     French.
  2. Ask: submit "What needs a reply today?" (structured) and confirm the
     composed sentence and citation rows appear without a model call. Ask
     an unrelated question (e.g. weather) and confirm **No matching mail**
     with no citations and the local-search footer. With a provider key,
     ask a free-form question and confirm the generated answer, citations,
     provider/model footer and privacy note. An initial Ask visit stays on
     the empty composer when the newest saved answer is a transient
     fallback (`qa_unavailable`, `qa_index_warming`,
     `qa_generation_failed` or `qa_no_grounded_answer`); opening it from
     the history rail marks it **Past answer** and keeps its citations,
     without the provider setup action when `qa.provider` is set. Clear
     history with the confirmation;
     reload and confirm the history is empty. Keyboard: Enter submits,
     Shift+Enter adds a line, `Ctrl+K` focuses the input.
  3. Mail: All mail is the default tab with folder chips and lazy pages;
     Important filter chips show endpoint totals;
     a row opens the detail instantly and deep-links; Recent still pages
     the bounded triage; Search still returns results. The detail uses the
     shared renderer, notices and badges. At 1024 px the list/detail stack
     and the body stays capped.
  4. Teach: pick a question and check the human title/hint, the message
     panel, the sticky actions and the keyboard help. In Review mode the
     question comes before Gemma's suggestion; **Use suggestion** saves and
     advances without a full reload; Save is disabled until a value is
     selected; the last option clears the action bar at 1440×900 and
     1280×768. Run a save from Label mode and confirm the progress, queue
     and toast update locally.
  5. Answer history: filter by question and source from the URL; **Load
     more** appends the next `next_cursor` page and ends clearly; a row
     opens the detail, renders the body through the safe renderer, groups
     labels by human question title and links to Teach without writing.
     Remove or hide the message and confirm the quiet unavailable state.
     Confirm `#/ask` is the Ask screen, not a redirect.
  6. Sources: **Fetch missing display data** disables while the job runs,
     progress advances over SSE, **Cancel** stops it, and the job appears on
     Learning. A source that cannot re-read messages ends with skipped
     counts, not an error.
  7. Language: Settings → **Interface language** → **Français**. Nav,
     screens, help, buttons, dialogs, dates and numbers switch without a
     reload; `<html lang>` becomes `fr-FR`; reloading the page and
     restarting the app keep French; **Reset** returns to automatic and the
     system language decides again. Repeat at 1024 px and check that no
     French label overflows.
  8. Settings: the general tab opens in **Simple** view with 8 everyday
     settings; **Show all settings** reveals 250 and persists for the
     session; searching finds a setting hidden in advanced mode and the
     count says it searches all. Apply/Reset still work and a
     restart-required setting shows its badge and pending banner.
  9. DevTools: the console is clean, Network shows no third-party request
     and no image fetched from a sender origin; an inline image is one
     authenticated request to the local
     `/api/messages/{id}/attachments/{position}` route, and the iframe
     document carries the frame CSP above. Warm navigation (Home → Mail →
     Ask → Teach → Home) shows cached content immediately, with no
     full-screen spinner on the repeat visit.
  10. Scrolling: at 1280, 1440 and 1920 px confirm the sidebar, topbar,
      job strip and boot strip stay fixed and no scrollbar appears at the
      window edge. Scroll Home's three columns, a Brief card body, the
      Mail list and detail, the Companies list and profile, Ask's
      transcript/history, Teach's panes, Sources, Settings, Learning,
      Answers and Logs; each scrolls only itself. Navigate away and back
      and confirm each section returns to its own position. At 360, 768
      and 1024 px confirm the route body is the one scroller and the
      document still does not scroll. No section or route scrolls
      sideways.
  11. Test preview: on the closed Home choose **Test preview**, confirm
      the dialog, and check that the Home banner, the separate **Preview**
      pill and **Open Mail** appear while the gate pill stays **Gate
      closed** and the manual meter is unchanged. Mail and Ask show the
      uncertified banner and return answers; Learning shows only the
      compact preview notice with its real closed stages. **Disable
      preview** restores the locked screens immediately. Repeat in
      French.
  12. Close with `MAILSENSE_DATA_DIR=/tmp/mailsense-ui scripts/stop-windows.sh`
      and confirm the descriptor and process are gone.
