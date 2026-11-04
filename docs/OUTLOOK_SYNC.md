# Outlook (classic, local COM) sync design

Researched 2026-10-14 (sources inline). Built and measured on the target
machine in phase 1b: "As built" and the measured tables below describe the
code. The research notes are kept where they still hold.

## Preconditions (checked at connect, each a named, user-facing error)

- Process not elevated (`TokenElevation`); elevated caller ⇒ `MK_E_UNAVAILABLE`
  / `CO_E_SERVER_EXEC_FAILURE 0x80080005`. Exe manifest `asInvoker`.
- Classic Outlook registered (`Outlook.Application` ProgID); new Outlook
  (`olk.exe`, `HKCU\Software\Microsoft\Office\16.0\Outlook\Preferences\UseNewOutlook=1`)
  has no COM.
- Attach only: `GetActiveObject`. Background work never starts Outlook
  (before 2026-10-15 it fell back to CoCreate + `Namespace.Logon`: a
  windowless Outlook, possibly racing Windows' restart after a crash);
  `Connector.StartOutlook` is the user's action and starts `outlook.exe`
  through the shell, with its window. `Logon` is off the member list.
- Object Model Guard: no prompts while Security Center reports AV "Good";
  otherwise guarded members (Body, Recipients, Sender*, PropertyAccessor,
  `Columns.Add`, `PrimarySmtpAddress`) prompt — detect and surface.
  learn.microsoft.com/.../security-behavior-of-the-outlook-object-model

## COM threading

- Outlook OOM is a single STA: ONE goroutine, `runtime.LockOSThread`,
  `CoInitializeEx(APARTMENTTHREADED)`, message pump (needed for events).
  Extra threads only add contention.
- Cache DISPIDs per interface (go-ole calls `GetIDsOfNames` on every
  `GetProperty`/`CallMethod`, doubling cross-process round trips).
- Retry `RPC_E_CALL_REJECTED 0x80010001` / `RPC_E_SERVERCALL_RETRYLATER
  0x8001010A` with backoff (IMessageFilter would need a hand-built vtable).
- Release every VT_DISPATCH immediately (Exchange caps open items).
- go-ole gaps to fill in-house: 2-D SAFEARRAY reading (`Table.GetArray`) and
  building a string SAFEARRAY for `PropertyAccessor.GetProperties`.

## Dates

- OOM built-in date members (VT_DATE) are **local time** → convert to UTC.
- Schema-named (`http://schemas.microsoft.com/mapi/proptag/0x…`) values and
  `PropertyAccessor` PT_SYSTIME are **already UTC** → never convert again
  (legacy export double-converted these — see PROGRESS finding 1).
- Store all timestamps as UTC RFC3339 with `Z`.

## As built (phase 1b, `internal/source/outlook`)

Everything above the `Object` interface is pure Go and unit-tested on Linux
against a fake object model; COM lives only in `*_windows.go`.

- **Thread:** one STA goroutine (`apartment.go`: `runtime.LockOSThread`,
  `CoInitializeEx(COINIT_APARTMENTTHREADED)`); one operation at a time. No
  message pump yet: nothing subscribes to Outlook events (phase 4).
- **Read-only by construction:** every object is wrapped by a guard that
  allows only the members listed in `model.go` (no Save/Delete/Move/Send,
  no puts; `Object` has no put). The same per-class list keys the DISPID
  cache. Items opened by EntryID (MailItem, ReportItem, MeetingItem…) are
  cached per object only, because their DISPIDs are not guaranteed to agree.
- **Busy:** `RPC_E_CALL_REJECTED`/`RPC_E_SERVERCALL_RETRYLATER`/
  `RPC_E_SERVERCALL_REJECTED`/`RPC_S_SERVER_TOO_BUSY` retried with
  `outlook.com.retry.*` backoff. Lost session ⇒ `ErrSessionLost` (a
  `source.ErrUnavailable`): the run ends, and the breaker decides when to
  reconnect ("Load and crash safety" below). Offline store ⇒
  `ErrStoreOffline`: that store is skipped, the rest syncs.
- **Own `IDispatch::Invoke`:** go-ole's leaks a BSTR per element when
  passing a string array (≈1 M leaks per full back-fill). SAFEARRAYs are read
  with `SafeArrayAccessData`. `GetArray` = rows × columns, column-major; every
  row's width and its binary EntryID column are checked, so a wrong layout
  fails as `errLayout` instead of scrambling fields. `GetProperties` takes a
  1-D `VARIANT(BSTR)` array and returns values in the same order (verified
  live).
- **Store selection (user decision 2026-10-15, superseding 2026-10-14):**
  selection is EXPLICIT. Nothing syncs until the user picks
  (`mailboxes.selected`, empty by default; `source.Pick`/`Unpick`). Every
  store (primary, delegate, shared, archive, public, PST, other) is
  discovered and listed (`Connector.Mailboxes()`, persisted by
  `syncer.Discover`, topic `sync.mailboxes`) with its key, kind, default
  flag, cached/online state and folder/item counts; none is preselected.
  Counting an unpicked store is opt-in (`outlook.discover.count_unpicked`,
  off by default; on, it is walked once per connector for its counts).
  Off, it is listed without counts (`-1`): a whole-profile walk is ≈2,600
  calls, most of them on online mailboxes, and an offline delegate can
  block a call. The key
  is `store-` + 12 hex digits of SHA-256(source id, StoreID) for every store,
  the default one included: stable across restarts and renames, never the
  localized display name, never a name that could start pointing at
  another store. A picked store that cannot be read is listed with its
  error, reported (learning blocker `mailbox_unreadable`) and waited on;
  nothing is substituted. Triage and the index cover the picked stores.
- **Discover:** `Namespace.Stores` → kind from `ExchangeStoreType`
  (0 primary, 1 delegate, 2 public, 4 shared; 3 is a PST when its
  `FilePath` is a .pst file, else other); the default store is only marked
  `Default` (when `DefaultStore` cannot be read no store is, and nothing is
  inferred). A delegate-type store owned by the profile user is the user's
  online archive (`archive`): Outlook exposes no archive marker (probed:
  `ExchangeStoreType`, `PR_MDB_PROVIDER`, the support mask;
  `PR_PROFILE_ALTERNATE_STORE_TYPE` is not readable, `MAPI_E_NOT_FOUND`).
  Roles come from `Store.GetDefaultFolder` EntryIDs (6, 5, 16, 3, 23), never
  from names.
- **Owner identity (reported, never inferred):** the default store's owner
  is `Namespace.CurrentUser.AddressEntry`, any other mailbox store's
  `PR_MAILBOX_OWNER_ENTRYID` → `GetAddressEntryFromID`. From the entry:
  `GetExchangeUser` → `PrimarySmtpAddress`, `Address` (the legacy DN that
  internal mail carries as the sender) and
  `PropertyAccessor.GetProperty(PR_EMS_AB_PROXY_ADDRESSES 0x800F101F)`
  (PtypMultipleString: `SMTP:` primary, `smtp:` aliases, `X500:` former
  DNs kept; `SIP:`, `SPO:` and other types dropped); a non-Exchange entry's
  `PR_SMTP_ADDRESS`; plus `Account.SmtpAddress` of every
  `Namespace.Accounts` item whose `DeliveryStore` is the store (POP/IMAP,
  PST delivery stores). Sources: Microsoft Learn (Outlook VBA:
  ExchangeUser.PrimarySmtpAddress, PropertyAccessor, Store.ExchangeStoreType,
  OlExchangeStoreType, Account.DeliveryStore) and [MS-OXOABK] §2.2.3.23
  (PidTagAddressBookProxyAddresses). Measured 2026-10-15 on the live
  profile: the default store's owner has 6 proxies (1 `SMTP`, 3 `smtp`,
  2 `SPO`) and a DN → identity of 5 addresses; delegate owners resolve to
  an Exchange user with a DN and a one-entry proxy list (probe re-run
  2026-10-15, review round L0). Only picked stores' addresses are reported and
  recorded, and only from a COMPLETE read (`Mailbox.IdentityComplete`):
  every read above either succeeds or finds the property absent
  (`MAPI_E_NOT_FOUND`); any other failure (an offline address book:
  `GetExchangeUser` → `MAPI_E_NETWORK_ERROR`, no rights) makes the read
  incomplete, is logged with the failed reads, and the recorded identity —
  and so every verdict — is kept until a complete read. Addresses are
  reported as Outlook gives them; `scope.NewIdentity` normalizes them. The walk
  uses `Folders.GetFirst/GetNext` plus ONE `GetProperties` per folder
  (EntryID, name, class, subfolders, type, hidden, content count, unread
  count, commit time). A folder that cannot be read (no rights on a delegate
  subfolder) is skipped and counted (`unreadable`); only a lost session, an
  offline store or cancellation end a store's walk. A folder is mail unless its class is Appointment/Contact/Task/
  Journal/StickyNote; that matches `DefaultItemType = 0` on the target
  profile (589 folders both ways). Search and hidden folders are skipped with
  their subtrees; `outlook.folders.exclude_patterns` applies to the path
  `\\Store\Folder\Sub`.
- **Change state (cursor):** `PR_CONTENT_COUNT` + `PR_CONTENT_UNREAD` (read
  flips do not move any modification time) + `PR_LOCAL_COMMIT_TIME_MAX`.
  Most cached-mode folders lack the commit time (1 of 29 on the primary
  store). Those add the newest `LastModificationTime` from a one-column table
  sorted by `[LastModificationTime]`; a schema name as the sort key is
  refused with `E_INVALIDARG`. Times are kept to the millisecond. Unchanged
  ⇒ `Sync` makes **no COM call**, unless the last snapshot is older than
  `outlook.sync.recheck_after` (default 15 min). That backstop exists
  because no folder property shows two read flips that cancel out in the
  unread count, or a change replicated with an older modification time. The
  state is read by Discover, before any row.
- **Sync (changed folder):** a three-column table (long-term EntryID
  `0x66700102`, UTC `0x30080040`, message flags) is the snapshot; its stamp
  is `mail.Message.Stamp`: modification time to the millisecond plus the
  message flags (a read flip moves no time). The syncer diffs it
  against the stored stamps (`Sink.Snapshot`) and returns the ids to
  deliver. One full-column table, sorted newest change first, then streams
  exactly those rows and stops at the last one. If Outlook refuses the sort,
  the table is read unsorted. A complete snapshot (row count = content count
  from Discover) also lets the syncer remove vanished messages after the
  run: moved if the Message-ID survives in the store, else deleted. A
  **safety valve** guards this, because a shrunken offline cache or a rebuilt
  OST looks exactly like a mass delete. A removal of ≥ `sync.removal.hold_count`
  (100) messages from one folder, or of more than `hold_ratio` (½) of a
  folder that large, is held: logged, published (`sync.removals_held`) and
  listed (`Store.HeldRemovals`). It is applied once it has persisted for
  `auto_apply_after` (72 h), or when confirmed (`Syncer.ConfirmRemovals`; the
  UI action comes in phase 4).
- **Folders no longer discovered** are probed (`FolderStatus`). One whose
  EntryID Outlook no longer finds is gone: its messages go through the
  valve, then the empty folder row is removed. One that still exists but is
  not synced (store disabled, folder excluded) becomes **inactive**: its
  mail is kept, outside scope, statistics and (phase 2) triage. Enabling it
  again reactivates it, and its cursor resumes incrementally. An offline
  store's folders are left untouched. This
  replaces the planned DASL date filter, which is locale-sensitive and gives
  no deletes.
- **Partial → details:** table rows become `Partial` messages (metadata and
  the 255-char preview searchable at once). The syncer's detail pass reads
  the rest newest first across stores (`ReadDetails`):
  `GetItemFromID(longTermID, storeID)` → `.Body` (`mail.body.max_chars`, shared with the RFC 5322 connector;
  UTF-16) → one `GetProperties`. That call reads headers, References and
  `PidTagLocalCommitTime`, plus, in full, what tables cap at 255 characters:
  Subject, sender name, Message-ID, In-Reply-To, ConversationIndex,
  categories (Keywords, joined invariantly), flag text → Recipients (one `GetProperties` per recipient:
  type, name, email, addrtype, `PR_SMTP_ADDRESS`; the address entry only
  when the row has no SMTP) → Attachments. The attachment list is complete
  or the detail read fails: if one attachment object cannot be read (a COM
  failure, or Outlook answering Nothing), the error carries the message id
  and the attachment index, the message stays partial and the next run
  retries. The rows and inline bytes stored by the previous complete read
  are kept; only an item class without an attachment collection is an empty
  list. The store's detail write is authoritative (it replaces the message's
  attachment rows with the list it is handed), so a shorter list must never
  reach it. A partial write keeps stored details. A message
  whose stamp did not move stays complete. An item failure leaves it partial
  for the next run — except an item whose read made Outlook raise an
  exception (`RPC_E_SERVERFAULT`): it is **set aside** (a quarantine item
  error; `store.detail_quarantine` at its revision), so the next run does
  not read it first again (newest first, it used to end three runs and stop
  the source with Outlook alive). The loss still counts in the breaker; the
  count is disclosed (`DetailReport.Quarantined`, sync-stage blocker
  `details_set_aside` with the action `retry_details` →
  `Syncer.RetryDetails`); a new revision of the message reads it again.
  The detail reads of a batch made before Outlook became unavailable are
  stored, never thrown away. `sync.details.enabled` (on) turns the detail
  pass off altogether (metadata only).
- **Tables refuse some properties:** `PidTagLocalCommitTime 0x67090040` makes
  the whole `GetArray` fail with `E_FAIL` (column acceptance is checked by
  the live `TestSmoke`), so it
  is read in the detail pass. Every other column of `columns.go` is accepted.
- **Senders:** `0x5D01001F`; otherwise a legacy DN (`0x0C1F001F`) is resolved
  once through its address-book EntryID (`0x0C190102`) → ExchangeUser →
  `PR_SMTP_ADDRESS` → the DN itself, cached per connector. The same applies
  to sent-representing (`0x5D02001F` / `0x0065001F` / `0x00410102`).
- **Dates:** every date is read by schema name, so it is already UTC
  (`Date.UTC`). Task due/start dates (PidLidTaskDueDate/StartDate) are
  **dates in the user's local time zone** with a midnight time
  ([MS-OXOTASK](https://learn.microsoft.com/en-us/openspecs/exchange_server_protocols/ms-oxotask/19974e79-5f5b-4ab9-9584-1c555c7718d9)
  2.2.2.2.4–5). They are stored as that date at 00:00, truncated to the
  day, and never treated as instants. No built-in local-time member is read, which also avoids the
  ambiguous local hour at the DST change.
- **Scope:** per store (see `syncer/scope.go`). Each delegate mailbox is
  judged against its own owner. The user's identity is never learned from
  one, and the user is never made its owner.
- **Measured:** `Stats` (calls, retries, per-member count, time and max,
  per-phase rows and times, detail sub-steps by store kind) and the
  `outlook.phase` topic.

## Load and crash safety (2026-10-15)

Incident: on the target, OUTLOOK.EXE crashed twice (Application log event
1000, faulting module `ntdll.dll`, exception `0xc0000374`: heap
corruption) during back-to-back full sync reruns.
The investigation found no memory bug in the connector's COM code (COM
marshalling copies every argument; ownership of BSTRs, SAFEARRAYs and
objects checked); the most likely cause is load: ≈1,250 calls per second,
each run on Outlook's own user-interface thread (Microsoft: "all calls to
the Outlook object model execute on Outlook's main foreground thread"),
each `GetItemFromID` loading a full item that add-ins (Google Workspace
Sync, Teams, Lync on the target) act on — and the reconnect logic and
harness made it worse (the next folder reconnected at once; the fallback
launched a windowless Outlook; a rerun hit an Outlook 90 s into its
restart). Microsoft gives no numeric call-rate limit; KB 257757 warns
that stress makes Office automation crash. Defences, each at its one
choke point:

- **Pace (`pace.go`, at the guard's one call path: every member call,
  every retry and the final `Release` of every object — a cross-process
  `IRemUnknown::RemRelease` into Outlook's thread, counted as
  `Class.Release`; about nine per detail read, unpaced before review 3).**
  After a call that took d the next waits d × (1 − share) / share, and
  never starts sooner than 1 / rate after the previous start, and never
  waits more than `outlook.pace.max_wait` (1 min): the wait is computed in
  seconds (float), so limits scaled toward zero can no longer overflow
  `time.Duration` (a negative wait: no pacing at all). The connector's own
  work between calls counts as rest, rest never carries over.
  Settings: `outlook.pace.busy_share` (0.25) and
  `outlook.pace.calls_per_second` (200) while the user is away,
  `outlook.pace.active.busy_share` (0.05) and
  `outlook.pace.active.calls_per_second` (20) while the user is active —
  last keyboard or mouse input of this Windows session (`GetLastInputInfo`,
  32-bit tick arithmetic across the 49.7-day wrap; a value "after" now
  reads as input now) more recent than `outlook.pace.idle_after` (2 min);
  an unreadable idle time counts as active. The active pace may not
  exceed the away pace (schema constraints). Measured in `Stats.Pace`
  (time waited, calls while active, transitions) and `Stats.Members`
  (releases included). At the defaults a detail read (≈10 calls and ≈6–9
  releases) runs at ≤ 12 items/s away, ≤ 1.2 items/s while the user works
  (reasoned). The detail pass's own 20 ms pause per 50 items
  (`sync.details.pause`) is gone: it paced nothing.
- **Busy.** A call Outlook still declines after `outlook.com.retry.attempts`
  (a dialog open) is `ErrOutlookBusy`, a `source.ErrUnavailable`: the run
  ends (the detail pass no longer counts it as one item's failure and goes
  on hammering), the session is kept, no loss is counted.
- **Breaker (`breaker.go`, in `Connector.op`, the only path that
  connects).** Lost-connection HRESULTs (`hresult.go`, Microsoft Learn COM
  and RPC codes): `RPC_E_DISCONNECTED 0x80010108`,
  `RPC_E_CONNECTION_TERMINATED 0x80010006`, `RPC_E_SERVER_DIED 0x80010007`,
  `RPC_E_SERVER_DIED_DNE 0x80010012`, `RPC_S_SERVER_UNAVAILABLE
  0x800706BA` (the usual one once Outlook died), `RPC_S_CALL_FAILED
  0x800706BE` (the incident's), `RPC_S_CALL_FAILED_DNE 0x800706BF`,
  `CO_E_OBJNOTCONNECTED 0x800401FD`, `CO_E_SERVER_STOPPING 0x80080008`, and
  `RPC_E_SERVERFAULT 0x80010105` (the server raised an exception COM
  caught: Outlook may be left corrupted, so the connector stops calling it
  too; in a detail read the item is also set aside, above) — on any call,
  attaching (`GetActiveObject`) included. After a loss: no reconnect for
  `outlook.breaker.cooldown` (5 min) × `cooldown_growth` (2)^(losses − 1),
  at most `cooldown_max` (2 h); then only once the session's OUTLOOK.EXE
  has been up `outlook.breaker.min_uptime` (15 min; read from the process
  list — Toolhelp snapshot, `GetProcessTimes` — never from Outlook); then
  at the pace × `resume_factor` (0.5) per loss within
  `outlook.breaker.window` (24 h). **Any other connect** — the first one
  after the app starts, or after the user's resume — to an Outlook up less
  than `min_uptime` (Outlook started at login moments ago) is allowed at the
  pace × `resume_factor` once more (state `easing`, until it has been up
  that long; a start time that cannot be read counts as "started now"):
  never refused, never at full pace. The factor never goes below
  `outlook.breaker.min_factor` (0.1). After `max_losses` (3) losses within
  the window, syncing Outlook stops until the user resumes it
  (`Connector.Resume`: the losses are forgotten, the uptime rule stays).
  States `closed`, `easing`, `cooling`, `warming`, `stopped` with their
  deadline, loss count, pace factor, last error and actions
  (`resume_source`, `start_source`) are disclosed (`Connector.Breaker`,
  `Health.Breaker`, topic `outlook.breaker` on every change, logged). With
  a keeper (`Connector.Keep(store)`, `source.StateKeeper`, table
  `source_state`) the losses, the stop and the last error survive an app
  restart; without one they live as long as the connector.
- **State reads never wait for an operation.** One mutex serialises the
  COM operations (and guards the session and the COM thread); the state —
  breaker, store list, folder states — has its own, never held across a
  call into Outlook: `Breaker`, `Resume`, `Stores`, `Mailboxes` answer at
  once during a long sync, and `Close` stops an operation in progress at
  its next wait (only a call already inside Outlook is waited for; the
  operation ends with `ErrClosed`).
- **Never launch from background work.** Attach only; Outlook not running
  ⇒ `ErrNotRunning` (a `source.ErrUnavailable`), no call made.
- **The run ends on an unavailable source.** `ErrSessionLost`,
  `ErrNotRunning`, `ErrOutlookBusy` and the breaker's refusals wrap
  `source.ErrUnavailable`; every walk of the connector stops on it, and
  `syncer.syncFolders`, the detail pass and the check of folders no longer
  discovered end the whole run on it (no next folder, no detail pass, no
  removals) instead of trying the next one.
- **New mail settles first.** The detail pass completes only messages
  received at least `sync.details.settle_after` (5 min) ago (Outlook may
  still be downloading or processing newer ones; the incident's second
  rerun read newest first right after a restart); `sync.details.max_per_run`
  (0: no cap) bounds one run's detail reads (a short verification run).

Not verified on the target yet (needs the user's approval): a run at the
new pace with Outlook up ≥ 15 min and the user away, then stepping the
rate up; whether an add-in is involved (the WER reports were not
retained; the read-only diagnostic showed the current Outlook was
restarted from explorer.exe, not through COM).

## Target machine (probed 2026-10-14, read-only)

Target machine: Windows 11 Pro 24H2, i5-13500T, 47.6 GB RAM, 126 GB free;
GPU **NVIDIA T400 4 GB** (Turing, compute 7.5, driver 596.51, 31 W) — **2,955 MiB
already in use** by the desktop at probe time, so ≈1 GB VRAM is free for
embeddings (plus Intel UHD 770 iGPU). An earlier "no GPU" note was an
unprobed assumption, corrected here; classic Outlook x64 16.0.20430 running (new Outlook not running);
WebView2 155; Windows Defender up to date (⇒ no Object Model Guard prompts);
Mailsense runs **unelevated as the user in session 1** (same session as
Outlook) — `GetActiveObject('Outlook.Application')` succeeds. Ollama absent.
`ExchangeConnectionMode` 700 (cached, connected). Stores: the owner's mailbox
(type 0, cached OST) + 6 delegate stores (type 1, **online, not cached**):
four shared mailboxes, a colleague's mailbox and that colleague's online
archive. Folder names are localized (`Boîte de réception`, `Éléments
envoyés`) — never match folders by name; use `GetDefaultFolder` / folder role.

## Go connector — MEASURED (2026-10-14)

Why the default is the primary store only: the whole profile holds 100,684
items in 589 mail folders, and 64 % of them (63,929 items, 570 folders) sit
in six ONLINE delegate stores. There every COM call is a server round trip:
≈23 ms per call on the online archive, 114 ms per item for details against
14.9 ms on the cached primary. All stores take 2 h 22 min. The PowerShell
estimate of 10–14 min covered only the primary store's ≈34k items. No
cross-store parallelism: the object model is one STA in the Outlook
process, so calls from more client threads would serialise there. This is
reasoned, not measured.

Whole profile: 7 stores (1 cached primary, 6 online delegates incl. one online
archive), 589 mail folders, 100,684 items at the start. The run's aggregate
report held counts, timings, HRESULTs and hashes only.

| Phase | Measured |
| --- | --- |
| Discover, 589 folders | **36.7 s**, ≈2,570 calls (4.4/folder). 23 s of it is ONE online store (262 folders, ≈23 ms per call); the cached primary takes 0.3 s. Warm = cold (server latency, not caching) |
| Snapshot (id + stamp + flags), all folders | 100,684 rows in 52.3 s; primary Inbox 20,067 rows in **0.26 s** |
| Full-column table pass (40 columns), all folders | 100,684 rows in 314.6 s (235 ms per 500-row `GetArray`); primary Inbox **1.49 s** (0.07 ms/row). Online stores run at 125–200 rows/s |
| **Metadata for the whole profile searchable after** | **≈7 min** (all 100,684 messages written as partial) |
| Details, cached primary | 34,546 items, **14.9 ms/item** (Inbox sample: open 0.6, body 3.7, props 1.4, recipients 2.8 ms) |
| Details, online delegates | 63,929 items, **114 ms/item** (Body avg 40 ms and GetItemFromID 26 ms over all stores; the online archive's old mail ≈190 ms/item). Stalls of ≈3 min seen once (server side; no retry, no error) |
| **Full sync wall time** | **2 h 22 min** (8,537 s), of which details 8,106 s, ≈90 % on online mailboxes |
| **Incremental right after** | **65.5 s**: Discover 35 s; 582 of 591 folders unchanged with **0 Sync calls**; 9 changed folders → 53,976 snapshot rows, 37 messages delivered and completed, 1 deletion |
| COM calls (full) | 1,072,920 calls, 0 busy retries; `GetProperties` 3.1 ms avg (251,933 calls) |
| SMTP chain | 150,453 of 150,538 recipients (99.94 %) had `PR_SMTP_ADDRESS` on the row. Sender DNs: 38,246 cache hits, 9 resolutions, 5 unresolvable (DN kept); 3 sender and 15 recipient DNs remain in 100,717 messages |
| Caps | 28 bodies over 100,000 chars cut; largest header block 33,689 chars (none cut at 65,536) |

Run 2 (all stores, after fixes 3–4 below): 100,720 items in 2 h 21 min;
**0 detail failures**, 0 pending, and the 2,209 items without `Recipients`
read as absent. Details took 9.0 ms/item on the primary vs 115 ms on the
delegates. The incremental sync took 41 s (587/591 unchanged; 3,093 calls,
34 s of it Discover). All 591 folders match `Items.Count` (100,736), with 0
read-flag or stamp mismatches, and the UnRead probe found 25/25 unchanged.

Run 3 (**the default: primary store only**): 34,645 items, 19 folders.
- **Full sync: 5 min 12 s.** The first Discover took 28.8 s, mostly the
  one-time count walk of the 6 disabled stores. The table pass took 13.7 s.
  Details took 5.8 ms/item (34,645 items in 202 s).
- **Incremental: 0.73 s.** 271 calls in total: Discover 0.2 s, and all 19
  folders unchanged with **0 Sync calls**.
- Checks: every count matches `Items.Count`; 0 missing, extra, read or stamp
  mismatches; the UnRead probe found 25/25 unchanged.
- Scope 34,334 owner / 184 shared / 127 unattributed. The six other stores
  are listed disabled with their keys and counts (166, 40,036, 87, 49, 528
  and 25,225 items).

Run 4 (after review round 1b, primary only): full sync 34,645 items in 5 min
18 s (details 5.9 ms/item, 0 failures). Incremental: 0.76 s, 271 calls.
**Snapshot backstop forced on all 19 folders: 1.79 s, 490 calls** (34,645
snapshot rows in 0.41 s); at the default 15-minute interval that is ≈2 s
per quarter hour. Cross-check: 19/19 folders equal `Items.Count`, with 0
read or stamp mismatches.

Checks after run 1's incremental sync, against live Outlook: **all 591 folders'
stored counts equal `Items.Count`** (100,717 = 100,717; 0 missing, 0 extra,
0 stamp differences). The UnRead probe opened 25 unread items twice: 24
unchanged, 0 changed, and 1 gone in the meantime. Timestamps: 0 empty, 0 in
the future, 0 before 1995. `sent_on` is later than `received_at` on 26
messages (8 by 1–6 h), so there is no systematic UTC shift.

Findings of the live runs (each fixed with a regression test):
1. `PidTagLocalCommitTime` in a Table fails the whole `GetArray` (E_FAIL); it
   moved to the detail `GetProperties`. It is empty on every item of this
   profile (and in the legacy export), so this Exchange setup does not keep it.
2. `Columns.Add` returns a Column object, which must be released.
3. 2,209 items (mostly one delegate folder and 67 Inbox reports) belong to
   classes without a `Recipients` member (DISP_E_UNKNOWNNAME). A missing
   member now reads as empty (`Details.Absent`).
4. Marking mail read does not move `PR_LAST_MODIFICATION_TIME`. One read flag
   went stale across the incremental sync. The folder cursor now includes
   `PR_CONTENT_UNREAD`, and the change stamp includes the read state
   (`mail.Message.Stamp`). Review 1b added the millisecond stamp and the
   snapshot backstop for the cases no folder property reveals.
5. `Folders.Item(i)` costs 3–7× more than `GetFirst/GetNext`, and
   `DefaultItemType`/`FolderPath` cost one call each per folder. Discover
   now does one `GetProperties` per folder.
6. Go vs PowerShell (same items): Inbox details 8.5–15 ms/item vs 17.4 ms; the
   table pass of the Inbox 1.49 s for 40 columns vs 1.87 s for 4 columns.

## Cost model — PowerShell probes (late binding, before the Go connector)

| Path | Measured |
| --- | --- |
| Legacy PS exporter | ~53 ms/item (34k in 30 min) |
| Table, 4 columns, `GetArray(500)`, Inbox 20,041 rows | **1,870 ms total ≈ 0.09 ms/row**; `GetArray` result dim 0 = rows (PowerShell view; verify order in Go SAFEARRAY bounds) |
| Full item: `GetItemFromID` + `.Body` + one `GetProperties`(6 props) + Recipients, Inbox ×200 | **17.4 ms/item** (get 5.2, body 4.2, propacc 2.0, recipients 5.8); avg body 3,167 chars |
| Same, Sent ×100 | 24.1 ms/item (get 8.8, body 5.4, propacc 2.4, recipients 7.2) |
| Same, delegate online store (a shared mailbox's Sent) ×50 | 23.3 ms/item |
| ⇒ initial full back-fill of ~34k | ≈ 10–14 min in background; metadata searchable after seconds (measured in Go: primary 34.5k in 8.6 min; the delegates were not in this estimate) |
| Unchanged folder (incremental) | measured in Go: 0 calls in Sync, ≈4.4 calls per folder in Discover |

Findings: `PropertyAccessor.GetProperties` returned transport headers up to
**19,424 chars** — the "~4 KB PST/OST string limit" note does not hold on this
build. 0 failures in 350 items. Live counts: Inbox 20,041, Sent 13,872 (the
legacy 19,999 was simply the size at export time — PROGRESS finding 6 resolved).
