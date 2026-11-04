---
layout: default
title: Console
---

# Console

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <a href="ask.html">Ask</a>
  <a href="install.html">Install</a>
  <a href="first-run.html">First run</a>
  <span class="doc-index-current" aria-current="page">Console</span>
  <a href="mailboxes.html">Mailboxes</a>
  <a href="providers.html">Providers</a>
  <a href="learning.html">Learning</a>
  <a href="faq.html">FAQ</a>
  <a href="privacy.html">Privacy</a>
  <a href="security.html">Security</a>
  <a href="troubleshooting.html">Troubleshooting</a>
  <a href="contributing.html">Contributing</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

The console is the local Mailsense window. Every destination reads the local API
and shows what that read returned, including partial indexes and failed jobs.
This page walks through the navigation, every screen, the states each screen
shows when a read is missing or blocked, and the links between screens.

## Navigation

The sidebar has five collapsible groups. The order below is the order in the
window.

| Group | Destinations | What it holds |
| --- | --- | --- |
| Work | Home, Brief, Mail, Companies | The ranked mail, the current work and the organizations |
| Ask | Ask, Answers | Questions in plain language and the stored answer history |
| Teach | Teach, Learning | One message at a time and the learning timeline |
| Configure | Sources, Settings | Mailboxes, identity, providers and runtime |
| System | Logs | The log ring and its filters |

Answers is a destination of its own under Ask. It is not a tab inside Teach, and
Teach keeps its own link to it.

Each group is a native disclosure. Opening or closing one stores that choice in
the browser's local storage. The group that owns the current screen stays open
while that screen is active. When local storage is unavailable, every group
opens.

On a narrow window the same destinations move into a drawer opened by the Menu
button. The button reports its state with `aria-expanded`; choosing a
destination closes the drawer, and Escape closes it and returns focus to the
button.

The sidebar foot holds the connection state, the version, the launch time and
the data directory. Shut down stops the local API and closes the window. It is
the only process control in the console.

### Keyboard

| Key | What it does |
| --- | --- |
| Tab and Shift+Tab | Move through the page in reading order |
| Skip to content | The first Tab reaches the skip link; Enter moves focus to the main region |
| `Ctrl+K` or `Cmd+K` | Open Ask and put the cursor in the question field |
| `/` | Focus the search box, unless the cursor is already in a text field |
| Arrow Up and Arrow Down | Move through search suggestions |
| Enter | Open the selected suggestion, or run the search |
| Escape | Close the suggestion list, leave the search box, close the drawer, or close a dialog |

Ask also accepts Enter to send, Shift+Enter for a new line, and Alt+N to start a
new conversation. Teach accepts 1 to 9 to choose an answer, Enter to save and
advance, G to use the judge suggestion, S to skip, and ? to show or hide the
shortcut help.

## First-run wizard

The wizard is an overlay, not a navigation destination. It opens once after the
launch reads, covers the shell, and walks through four steps: Welcome, Mail,
Model and Finish. Welcome shows the data directory, the Outlook stores already
found, the language and the time zone. Mail discovers and picks stores. Model
offers the local runtime, providers, or a skip. Finish writes the completion
marker and explains the default resource ceilings.

The wizard opens when `ui.setup.done` is false and no mailbox is picked. A store
that discovery found but nobody picked does not close it. Not now on the Mail
step lets the wizard finish without a mailbox, and Finish for now closes it.

There is no control in the console to reopen the wizard. To see it again, reset
`ui.setup.done` to false in Settings, unpick every mailbox in Sources, then quit
and reopen Mailsense. Resetting the marker alone or unpicking alone is not
enough, because both conditions must hold at launch.

## Home

Home answers what needs attention now. It combines the current Brief totals, the
next agenda items, important mail and a compact learning status.

The header shows the generated time and three counts: replies owed, deadlines
and meetings. The agenda section offers the 3 h, 8 h and 24 h horizons, plus the
configured default when it is another value; a chip reloads the agenda for that
window. The Brief section lists the three highest-ranked signals, and Open mail
goes to Mail. Review suggestions opens Teach in review mode for the question
that needs it. The More disclosure holds recent questions, recent answers,
learning progress and the mailbox summary, each with its own link.

When the learning gate is closed, Home becomes a cockpit. It shows the manual
answer target, the remaining questions and their progress, with Review
suggestions and Answer questions. Important mail stays locked. The learning rail
shows the first blocker, Open learning and the Test preview button. Test preview
unlocks the automatic answer screens without certifying anything; while it is
on, a banner offers Open mail and Disable preview.

Missing or failing pieces stay local to their section. An agenda read that has
not landed shows a dash in the counts and a skeleton in the section; a failed
agenda read says it is unavailable, and another horizon chip retries it. A
failed important-mail read offers Try again. A failed recent-question read keeps
the recent answers and offers Try again instead of claiming there is no
activity.

Related screens: Brief, Mail, Companies, Teach, Learning, Sources, Ask and
Answers. The More disclosure links to Ask and Answers directly.

## Brief

Brief answers what is open in the current work: commitments that need you,
replies you are waiting for, activity since the last seen boundary, open
relationships and suggested topics.

The four stat cards jump to their section. Each row opens its message in Mail or
its organization in Companies. Mark seen, on the Changed section, stores the
current generation time as the next boundary and reloads the Brief; it is
disabled until a generation time is known. The Topics section marks suggested
topics and shows classification coverage.

If the gate cannot be read, the screen shows an error with Try again and
nothing else. If the gate is closed, the screen shows the learning blockers and
Open learning. A failed Brief read offers Try again. If the browser refuses to
store the new boundary, Mark seen reports that the date could not be saved and
leaves the previous boundary in place.

Related screens: Mail, Companies, Learning and Teach.

## Mail

Mail answers what the mailbox contains, what is important, which conversations
are open and what a search found.

The tabs are All mail, Important, Conversations and Recent. All mail filters by
folder role, unread state and a sender address or `@domain`; the sender and
unread filters show a clear action when active. Important is the ranked list and
carries its own filters for replies, urgency, security, decisions, deadlines and
workload. Conversations filters by subject, sender and conversation state.
Recent reads the newest triaged mail one page at a time. Every paged list has
Load more and says when it reached the end or its cap.

The search box lives in the top bar, not in a tab. Typing shows suggestions;
Enter opens the selected hit or the full result page inside Mail. The result page
can filter by mailbox, folder role and attachments. On a screen with its own
list, the same box filters that list instead.

Selecting a message opens the detail pane. Show full message expands the body;
remote images stay blocked and the pane says how many. Ask about this message
opens Ask with a question about it, and Open in Teach opens the labeling
workbench for the first question. A conversation card shows its state,
participants and earlier messages.

A failed gate read shows an error with Try again; the screen never derives
permission from a stale learning status. A closed gate shows the blocker list
and Open learning, or a refresh action when no blocker is reported. Empty
folders say so and suggest another folder or the next sync. An empty Important
list offers Review suggestions. A refresh failure keeps the last list and says
it could not refresh. A search with no hit
suggests different words or a shorter question. With no message selected, the
detail pane asks you to choose one.

Related screens: Teach, Ask, Companies, Learning and Sources.

## Companies

Companies answers who the organizations are, what the relationship looks like
and what is still open with each one.

The list is ranked by stored mail. Search covers the whole ranking on the server,
so a company outside the first page is still reachable; the field also filters
the visible rows immediately while the request is pending. Selecting a row swaps
only the profile pane. Ask about this company opens Ask with a prepared
question. Profile entries open their message in Mail.

The profile shows the evidence brief, the next step, key figures, open threads
split by who moved last, due commitments, upcoming meetings, recent mail,
addresses and monthly volume. A declared organization and a domain organization
carry different badges. One-way mail is kept and named rather than hidden.

A closed gate shows the learning blockers and Open learning. No company yet
shows the empty state and explains that personal and free-mail addresses are not
grouped. A search with no match offers Clear search. A failed list read offers
Try again. A failed profile read stays in the profile pane with Try again and
does not discard the list. When the selected company falls outside the current
search, the pane says so instead of showing two panes that disagree.

Related screens: Mail, Ask and Learning.

## Ask

Ask answers a question about the mail in plain language. The answer can be a
computed count or list, matching messages, or a written answer with citations.

The composer has one question field and a Send button. The model selector offers
Auto, Local and Remote. Auto answers locally when it can and otherwise uses the
configured provider; Local never calls a model; Remote asks the named provider
to write the answer. Suggested questions fill the field. New conversation, or
Alt+N, starts a fresh thread, and the history sidebar reopens an earlier answer.
Citations open the message they name.

A disabled Q&A setting shows the reason and a link to the Q&A setting. A closed
gate shows Open learning and the Test preview button. A remote option that is
unconfigured, unusable or unknown says so and links to provider settings, while
Auto and Local stay available. A warming index or a search timeout keeps the
question and offers Try again. A failed written answer or an answer that
could not be grounded keeps the matching messages and reports the provider. No
match suggests different words, a shorter question or a broader topic. A question
that is too short is rejected at the field. An answer written in another language
is shown as written, with its citations, and the notice says that asking again
calls the provider again.

Related screens: Answers, Mail, Settings (Providers) and Learning. The full
guide covers the three modes, grounding, the provider request and the limits:
[Ask](ask.md).

## Answers

Answers shows what has already been answered, by you or by the judge, for each
question.

The question selector filters by question and the segmented control filters by
source: All, You or Judge. Review suggestions opens Teach in review mode. A row
opens the message detail and its answer history for that question; Read in Teach
and Review / correct in Teach go to the same message in the workbench. The list
pages with Load more and marks its end.

A failed list read offers Try again. An empty filter explains that answers appear
once they are stored and suggests another question or source. A failed detail
read reports the failure inside the detail pane, and a message with no stored
answer says so.

Related screens: Teach, Mail and Learning.

## Teach

Teach answers how one message should be labeled, one question at a time, and
turns the answer into a stored example.

The mode switch offers Label and Review suggestions. Label asks you to classify
the message in front of you. Review suggestions shows the judge suggestion
first, with Use suggestion to store it as a manual label and advance, or another
answer below. The question
chips are ordered by remaining need and scroll with the arrow buttons or the
left and right arrow keys. Save and advance stores every selected question and
moves on; Skip leaves the message unanswered. Previous and Next move within the
loaded queue. The Advanced disclosure shows the evaluation-label requirement, the
Run judge action with its estimated cost when one is reported, Open learning, and
the blocker diagnostic. The Answer history link opens Answers.

Each task shows the subject, the sender and the message. The queue is a page,
not the whole population: reaching the end of a page offers Continue, and a true
completion offers Next question and Back home. When the profile publishes no
questions, the screen says so and points to the profile file in Settings. When
the machine facts are unconfirmed, the screen names the time zone blocker and
offers its action instead of building a queue. A failed queue read offers Try
again without rebuilding the toolbar or losing the selected question. A failed
summary read leaves the counts as a dash and keeps the queue usable.

Keyboard: 1 to 9 chooses an option or level, Enter saves and advances, G uses the
judge suggestion when one exists, S skips, and ? toggles the shortcut help.

Related screens: Learning, Answers and Mail.

## Learning

Learning answers why the mailbox can or cannot answer yet, and what moves it
forward.

The callout shows the learning state, the gate, the certification count and the
next actions. The actions depend on the state: Open mail when the gate is open;
Answer a question and Review suggestions while the labels stage is incomplete;
and the run action a blocker offers, such as Get judge suggestions, Read the
mail clues or Train the classifier. The question links open the three questions
that help most. The side panel shows stage completion, blocker count, running
jobs with a link to Logs, label progress, scope, time zone, last update and
Refresh.

The timeline lists every stage with its state, progress, unit, estimate and
technical detail. Each blocker names its own fix: a failed run links to Logs, a
configuration blocker links to the matching Settings row, a missing embedder
links to the Runtime tab, and an unconfirmed time zone links to the zone setting.
The scope line says when no mailbox is selected, and the time zone line says
when it is the machine zone rather than a confirmed owner zone.

When no stage is reported, the timeline says so and Refresh reads it again. A
failed label summary is shown as unknown rather than zero. A preview run carries
a banner stating that nothing is certified.

Related screens: Teach, Mail, Logs, Settings and Sources.

## Sources

Sources answers which mailboxes Mailsense may read, who the owner is, and what
state each store, removal and display backfill is in.

The source selector chooses the connector, Discover reads its store list without
reading a message, Sync now starts a sync for the selected source, and Refresh
re-reads the screen. The mailbox table is sortable and has one row per store.
The picked checkbox adds a store to the learning scope and starts a sync for its
source, or removes it from that scope. Unpick asks for confirmation and explains
that the stored mail is kept for a later re-pick.

The owner identity column lists the addresses the source reports and the
addresses you declared. Declare opens a dialog and validates the address; a
declared address can be removed with its chip button. A store with no address
shows No owner address, which blocks learning until one is declared.

Each store shows its state as badges: online or offline, cached, default, not
discovered and picked. An offline badge carries the connector error as its
tooltip. A failed discovery or sync reports a toast and leaves the table in its
last known state. A failed sync or display-backfill job is named above the
cross-links with Open the failed job in Logs.

The Messages waiting for confirmation section lists every folder where a
complete sync found removed messages. Confirm removal names the folder and the
count in a warning dialog and then applies the removal. The card says when the
removal applies automatically. A failed held-removal read keeps its own retry
instead of showing an empty card.

Message display starts Fetch missing display data for the selected source,
shows done and total while it runs, shows its latest progress message and any
error, and offers Cancel through the ordinary job cancel. The button is disabled
while a display job runs or when no source is registered. The panel explains
that it stores display HTML and inline images only and leaves the learning body
alone.

The Source registry table lists the connectors known to this process, with their
id and kind. On a build with no live source, the screen says that discovery is
unavailable instead of pretending the table is empty because there is no mail.

### Source actions in the local API

The local API answers three owner actions on a source, each as
`POST /api/sources/{id}/{action}` and each returning the action, the source and,
for retry, the number of messages set aside again for a detail read:

- `start` starts classic Outlook as the owner would, through the connector's
  start capability. A connector without that capability answers
  `409 action_unsupported`.
- `resume` forgets the recorded connection losses so the next operation
  connects as a first one would. Outlook still has to be running. A connector
  without the resume capability answers `409 action_unsupported`.
- `retry-details` clears the set-aside list for that source so its next sync
  reads those messages again, and reports how many were set aside. It requires
  the source to read message details and answers `503 unavailable` when the sync
  service is not available.

The console does not call these three switches yet. When a learning blocker
offers Start the source, Reconnect, Resume or Retry details, the console opens a
short guidance dialog and then runs the real discovery or sync action it can
reach.

Related screens: Settings (Sync and Outlook filters), Learning and Logs.

## Settings

Settings answers what the app is configured to do and which layer supplied each
value. The precedence is default, then learned value, then `settings.json`, then
the environment.

### Simple view

General opens on the simple view unless you switched to the advanced view
earlier in the session. Simple and Advanced are a view toggle, and the choice
lasts for the session. The simple view shows the eight everyday settings in four
groups: language and time (`ui.language`, `time.zone`), Ask and suggestions
(`qa.enabled`, `qa.max_messages`), mail and folders (`sync.details.enabled`,
`triage.folder_roles`, `threads.folder_roles`) and learning
(`learning.test_mode`). Show all settings switches to the advanced view and the
note says how many settings are hidden.

### Advanced view

The advanced view lists every setting the General tab owns, grouped by schema
group. Each row shows the setting key, its description, the effective value, the
default, the layer badge, and the actions that apply:

- Apply writes the row's value. It is disabled when the value comes from the
  environment, because writing `settings.json` would not change the effective
  value.
- Reset removes the stored override and is disabled when the value already
  equals its default.
- A secret row never displays the stored value; leave it empty to keep the
  current value, or enter a new one to replace it.
- A data-file row names the external file and explains that the file is read at
  run time, so an edit is noticed without a restart.
- A learned row can be rejected, with the value and observation count shown.
- An environment row names the `MAILSENSE_` variable, disables Apply and says to
  unset the variable and restart Mailsense to use the file value.

The search box in the top bar searches every setting, including the rows owned by
the Providers and Runtime tabs. A match on another tab appears as a pointer with
its current and default value, and Open the tab takes you to the row. The top bar
also reports whether a restart is pending.

A row with the restart required badge needs a quit and reopen. In this schema
those rows cover storage, network, the query pool limit, file logging, the
secrets file, the config watch interval, the agenda default horizon and the
runtime directory. A saved value waiting for that restart appears in a banner
that names the keys and says exactly what the two actions mean. Reload from disk
re-reads `settings.json` and refreshes the console; it does not apply a
restart-required value. Only quitting and reopening Mailsense applies those
values. The banner is the only place the Reload button appears.

### Owning tabs

Providers owns the provider records and the four roles: Q&A, Judge, Triage and
Embeddings. Each role selects a record of the matching kind, and an unconfigured
role blocks its feature. Add provider opens a form with connection fields, a
protocol, a model, a key name, and an Advanced section for limits, price, retry
and protocol-specific fields. Fetch models sends one request to the provider's
model list and fills the form without saving. Test sends one request that may be
billed and reports success, latency, model and a short detail. Edit changes the
record. Delete is refused while a role selects it, and the confirmation says
that calls using the record will fail until another one is configured.

Local runtime owns the managed engine. It reports the embedding model, the state
(installing, starting, ready, idle, failed, stopped, or not selected), the
installed size, the process id, threads, restarts and the last start time. It
says when embeddings use a provider instead, with a link back to Providers. The
Advanced disclosure offers Reinstall / Repair when the build or model is known,
and the engine's own settings render as ordinary rows. Refresh re-reads the
runtime state. A broken build shows its reason; an unavailable Vulkan build
explains the driver or probe failure.

Mailbox selection and declared addresses are not editable here. The mailbox rows
point to Sources, which owns those writes.

A failed provider list says so and still lets you add a record. No provider yet
shows the empty state. A role that names a missing record says so. A failed
provider test reports the failure and links to the engine entries in Logs. A
runtime with no published build says the catalog is empty; a failed repair
reports the error in place.

Related screens: Sources, Learning, Logs and the provider links from Ask.

## Logs

Logs answers what the application did and why a job or a call failed. It reads
the in-memory ring and follows new entries over the event stream.

The toolbar filters by level and by component, pauses or resumes the live tail,
clears the current view and refreshes. Clearing the view does not clear the
server's ring; new entries continue to arrive. A link from another screen can
add a job filter, shown as a chip with its own clear action. Expanding a row
shows every stored attribute; values that look like secrets or content are
already redacted by the logger.

When the ring is empty, the table says so. When a filter hides every entry, it
names the filter instead. A job filter with no retained entry says that the ring
keeps only the most recent entries. A file logging failure, dropped subscriber
events, a truncated snapshot or event loss appears as a banner above the table.
An attribute that is no longer retained says so when its row is expanded.

Related screens: Learning, Sources and Settings.

## What the console cannot do yet

- There is no multi-user or tenant model. One launch token, one data directory
  and one owner serve the whole console; there are no user accounts, user roles
  or per-user separation.
- There is no restart control. Shut down stops the process. A restart-required
  setting takes effect only after you quit and start Mailsense again.
- Searches are not saved. Ask keeps a question history, but the search box keeps
  no list of past searches and there is no saved-search screen.
- There is no export. Messages, answers, learning data and logs can be read in
  the console, but no screen writes them to a file.
- There is no batch label undo. The local API answers a single-label delete at
  `DELETE /api/labels/{message_id}/{question}`, but the console exposes no undo
  control and there is no batch restore.
- The Linux build has no live mail source. It opens every screen, but Sources
  explains that discovery is unavailable outside classic Outlook on Windows.
- Windows behaviour is not verified from a Linux build. The WebView2 window and
  the classic Outlook connection cannot be exercised on this host, so their
  first display and COM behaviour remain unverified here.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/">Documentation</a> · <a href="privacy.html">Privacy</a></p>
<!-- footer:end -->
