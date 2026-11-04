# Porting spec — legacy Python → Go

Source of truth for legacy behavior; generated 2026-10-14 from the Python tree. Section 5 taxonomy JSON must be generated from the source with the command in the preamble, not copied from here.

All paths are relative to the repository root. Nothing in the project was modified.

The report runs longer than the 2,500-word target because section 5 quotes all 17 questions word for word, as asked. For the JSON config, don't copy the text from this report. Generate it from the source so it is exact:
`python3 -c "import json;from email_classifier.taxonomy import default_profile;print(json.dumps(default_profile(),indent=2,ensure_ascii=False))"`.
The current spec fingerprint is `9c9956169b0c`.

---

## 1. Message data model

The Python record is `EmailMessage` (`mailbox.py:20-65`). Every field is a string except four booleans: `is_read`, `has_attachments`, `reminder_set`, `read_receipt_requested`. `from_record` (`mailbox.py:73-106`) rejects unknown fields, rejects missing required fields, rejects a non-boolean in a boolean field, and turns null into `""`.

**Date format.** Every date is written as `yyyy-MM-ddTHH:mm:ssZ` in UTC, using InvariantCulture (`outlook_export.ps1:191-204`). A value with year ≤1900 or ≥4500 is stored as `""`, because Outlook uses `1/1/4501` to mean "no date". Formatting with CurrentCulture produced years 1402, 1445 and 2567 on fa-IR, ar-SA and th-TH, and a `.` time separator on fi-FI.

**Fields, with their Outlook source:**

| Field | Outlook Object Model member / MAPI property |
|---|---|
| `id` | `EntryID` (relative to the store; changes when the item is moved) |
| `subject`, `sender_name` | `Subject`, `SenderName` |
| `sender_address` | Tried in order (`ps1:121-129`): PA `0x5D01001F`, then `0x5D01001E` (PidTagSenderSmtpAddress) → `Sender.AddressEntry.GetExchangeUser().PrimarySmtpAddress` → AddressEntry PA `0x39FE001F/001E` (PidTagSmtpAddress) → raw `SenderEmailAddress`, which is a legacy DN `/O=EXCHANGELABS/…/CN=…` for internal senders. Exchange leaves PR_SENDER_SMTP_ADDRESS unset for internal senders. |
| `sent_representing_address` | PA `0x5D02001F/001E` (PidTagSentRepresentingSmtpAddress), then `0x0065001F` (PidTagSentRepresentingEmailAddress, which may be a DN). Empty on own-behalf sends (`ps1:131-142`). |
| `to/cc/bcc_addresses` | `Recipients` filtered by `Type` 1/2/3. Per recipient: `GetExchangeUser().PrimarySmtpAddress` → AE PA `0x39FE` → Recipient PA `0x39FE001E` → `Recipient.Address`. Joined with `", "` (`ps1:144-159`). |
| `addressed_to_me` | Inbox only. Reads PidTagMessageToMe `0x0057000B`, PidTagMessageCcMe `0x0058000B` and PidTagMessageRecipientMe `0x0059000B` (the last one covers Bcc). Logic: any=false → `no`; to=true → `to`; cc=true → `cc`; any=true → `bcc`; unreadable → `""` (`ps1:161-178`). |
| `received_at`, `sent_on`, `creation_time`, `last_modification_time` | `ReceivedTime`, `SentOn`, `CreationTime`, `LastModificationTime`. These are local time and are converted to UTC. |
| `last_verb_execution_time` / `last_verb_executed` | PA `0x10820040` / `0x10810003` (102 reply, 103 reply-all, 104 forward) |
| `message_flags` | PidTagMessageFlags `0x0E070003`, raw integer. mfEverRead = `0x400`. |
| `local_commit_time` | PidTagLocalCommitTime `0x67090040`. Empty when the store does not expose it. |
| `body_preview` | `Body` (plain text), cut to `BodyChars` (default 8000) |
| `headers` | PidTagTransportMessageHeaders `0x007D001F`, cut to `HeaderChars` (default 4000) |
| `internet_message_id` | `0x1035001F` |
| `conversation_id` | `ConversationID` |
| `has_attachments` | `Attachments.Count>0` (inline images count too) |
| `is_read` | `!UnRead` |
| `message_class`, `size`, `sensitivity` (0–3), `importance` (0–2), `categories` | Object Model members of the same names |
| `read_receipt_requested`, `voting_options`, `voting_response` | Object Model members of the same names |
| `spam_confidence` | SCL `0x40760003`. The earlier guess `0x0408` was wrong. |
| `flag_status` | PA `0x10900003`, falling back to `FlagStatus` (0 none, 1 complete, 2 marked) |
| `flag_request`, `task_due_date`, `task_start_date`, `reminder_set`, `reminder_time` | Object Model members. These are named PidLid properties: read them through the Object Model, never with hardcoded `0x85xx` tags. |
| `source`, `folder` | `"outlook"`; `"inbox"` or `"sent"` |

**Owner identity at export time:** `CurrentUser.AddressEntry` resolved through the same SMTP chain, falling back to `CurrentUser.Address` (`ps1:180-189`).

**Evidence-only fields.** These must never become rules: importance (the owner said it is "sometimes not a good indicator"), size, sensitivity, categories, flags, SCL, voting (populated on 0 of 33,842 items), and all the timing fields.

**Pitfalls**
- **There is no read time in MAPI** (no PidTagReadTime exists). The honest bracket is `received_at ≤ first read ≤ last_verb_execution_time`, plus mfEverRead, plus `local_commit_time` (last touch, including read-state flips). Never present any of these as a read time.
- **Transport headers are a received-message property.** They are filled on 19,988 of 19,999 inbox items and only 7 of 13,843 sent items. Empty on sent items is the correct value, not a capture failure.
- **Bcc** only appears on items you sent. On inbox items, Bcc is known only through `addressed_to_me='bcc'`. The old export had no Bcc capture, which left 109 items with no recipients at all.
- **Character caps.** A cap below 1 must fail the run: `Substring` threw on every item, every item was counted as skipped, and the export reported "DONE 0 N" as a success. The Python bounds are 1 to 20,000 in `config.py`, and 1 to 1,000,000 at the relay.
- Items are read with `Items.Sort("[ReceivedTime]", true)`, newest first, with an optional `Limit`. An error on one item is caught and counted as skipped.

**Latent bug found during this review (not in NOTES).** `Get-DateIso` calls `.ToUniversalTime()` on the results of `PropertyAccessor.GetProperty`. Microsoft documents that PropertyAccessor already returns PT_SYSTIME values in UTC. So `last_verb_execution_time` and `local_commit_time` are probably shifted by the local UTC offset. In Go: Object Model dates (VT_DATE, local time) need local→UTC conversion. PropertyAccessor dates are already UTC and must not be converted.

**Threading fields to add (recommendation).** Read PidTagInReplyToId `0x1042001F`, PidTagInternetReferences `0x1039001F` and PidTagConversationIndex `0x00710102` directly. They exist on sent items, where transport headers do not.

## 2. Outlook COM pitfalls

**Still apply when running locally:**
- **Integrity level.** An elevated process gets `CO_E_SERVER_EXEC_FAILURE 0x80080005` from CoCreate and `MK_E_UNAVAILABLE` from GetActiveObject when Outlook runs at medium integrity, because Outlook refuses COM activation across integrity levels (NOTES issue 2). The Go exe must be built with an `asInvoker` manifest and must never run as admin. Detect 0x80080005 and tell the user to run it unelevated. The `explorer.exe`/ShellExecute trick to de-elevate did not work on that Windows build.
- **`GetDefaultFolder(6/5)` returns only the default store's folders.** The live profile had 7 stores: four shared mailboxes, a colleague's mailbox, an online archive, and the owner's own store. Shared and delegate mailboxes and the archive are separate `Namespace.Stores`. If the Go app ever iterates stores, it must tag each item with its store and run the scope rules per store.
- **Shared-alias mail lands in the owner's own inbox** (146 items such as `wordpress@`). This is a scope problem, handled in section 3, not a store problem.
- **Locale.** Never format dates with the current culture, and never parse localized text. The fr-CA machine printed "En cours" and "Prêt" for task states. Use numeric enums. Compare identities with ordinal case-insensitive comparison (`strings.EqualFold`); the Turkish dotless-i broke `ToLower`.
- **Non-mail items** in the Inbox (meetings, NDRs, read receipts) lack some properties. Wrap each property read separately, as the script does.
- **Output failures versus item failures.** An item-level COM failure means skip that item and count it. A failure writing the output must abort the whole run (`ps1:365-383`). After the run, counts must reconcile, and a skip ratio of 1% or more is fatal (`outlook_relay.py:83,448-472`).
- **Not recorded in NOTES, worth checking:** the Object Model Guard can prompt when an external process reads `Body` or `Recipients` and antivirus is not current. Cached Exchange Mode's sync window limits what COM can see; the Inbox count of exactly 19,999 is suspicious.

**Specific to the relay (drop):** the scheduled-task vehicle and its 600 s exec cap, DONE/FAIL marker files and line-anchored parsing, the `schtasks` locale probe and its sentinel, the PowerShell traps (typed parameter name collision, `$script:` scoping, `-File` single-quote quoting, MethodInvocationException leaf unwrapping), the 8.3-path quoting bug in relay-remote, `--attach`, SHA-256 download sidecars, and `_validated_owner` command-line injection guards.

## 3. Scope and identity rules (`scope.py`)

> **Superseded in Go (L0, user decision 2026-10-15):** the owner is never
> inferred from the Sent folder. A store's identity is what its source
> reports for the picked mailbox (Outlook: primary SMTP, SMTP/X500 proxies,
> DN) plus the addresses the user declares; a store with neither has no
> verdicts and blocks learning. Verdicts come from that identity (sender or
> sent-on-behalf, recipients with RFC 5233 subaddresses, the addressed-to-me
> flag of sources that have one, Delivered-To/X-Original-To, List-Id); a
> Sent-folder item someone else sent is `shared`. See `internal/scope`.
> The legacy rules below are kept as the record of the Python behaviour.

**`discover_identity`** (`scope.py:222-254`)
- Scan only the Sent folder and count normalized senders (lowercased, trimmed).
- The owner SMTP is the explicit `--owner` if given. Otherwise it is the most frequent Sent sender that contains `@`. Otherwise it is the most frequent Sent sender of any form.
- Aliases are every legacy DN (regex `^/(o|ou|cn)=`, case-insensitive) seen as a Sent sender, lowercased, other than the SMTP itself. That a DN appears on the owner's own Sent mail is the proof.
- Other SMTP senders found in Sent, such as delegates, are never treated as owner identities.

**`scope_verdict`** (`scope.py:257-280`)
1. folder=sent → `owner` if `sender_address` is non-empty, else `unattributed`. SendAs items stay `owner`.
2. Inbox with `addressed_to_me` in {to, cc, bcc} → `owner`. With `no` → `shared`.
3. Fallback when the flag is empty: take the union of To, Cc and Bcc, split on `[;,]` and normalized. Empty → `unattributed`. Intersects the identities → `owner`. Otherwise → `shared`.

**Policy**
- Only `owner` mail feeds behaviour learning and statistics.
- `shared` and `unattributed` mail is kept for search and never deleted.
- Internal and external mail are treated identically.
- If no identity can be found (no Sent mail and no `--owner`), use all mail and log a warning (`cli.py:159`).
- Ground truth on the real corpus: owned by `scripts/fixture-import.sh` (its default `FIXTURE_EXPECT`), which asserts it on every fixture run.
- Never filter on whether a sender is in SMTP form: 12,740 Sent items carry the owner's own DN, and filtering would have deleted most of the sent history.

**Unify these in Go.** `history.detect_owner` (`history.py:235`) can return a DN and differs from `discover_identity`. `query.build_db` computes the scope column without `owner_address`. Go should have one identity function.

## 4. Threading (`history.py`, `threads.py`)

**Headers are not used for threading today.** Message-ID, In-Reply-To and References are captured but no code reads them.

**`thread_key`** (`history.py:70-75`)
- `c:`+ConversationID when present.
- Otherwise `s:`+subject with ONE leading prefix removed (regex `^\s*(re|fw|fwd|aw|tr|sv|vs|ref|enc|rif|vb)\s*:\s*`, case-insensitive), whitespace collapsed and lowercased.
- Known flaw: every mail with an empty subject and no ConversationID ends up in a single thread.

**`match_replies`** (`history.py:245-281`): how a sent reply is paired with the inbox message it answers.
- Messages with an unparseable `received_at` are dropped. Sort each thread by time. Inbox messages are pushed onto a pending list.
- For each sent message:
  - Let R = To ∪ Cc. Pick the most recent pending inbox message whose sender is in R. If R is empty, pick the most recent pending message.
  - If nothing matched and the pending senders are a single address, pick the last pending message.
  - Remove the chosen message from pending.
  - If the time difference is ≥0, record `(seconds, excerpt)`. The excerpt is the reply body, trimmed, newlines turned into spaces, first 240 chars.
- The sent item's `received_at` is used as its time; `sent_on` would be better.

**`their_replies`** (`threads.py:99-166`) is the mirror image: sent messages wait, and an inbox message answers the most recent sent one whose sender is in the inbox message's To/Cc, with the same single-counterpart fallback. There is no "empty recipients matches anything" rule here.

**Behaviour profile** (`build_behavior`, `history.py:284-380`)
- Per sender and per domain: received, replied, reply_rate, median/mean response minutes, p90 (index `round(0.9*(n-1))`, Python's half-even round), first_seen, last_seen.
- **Go (review round 2b):** one median everywhere, `mathx.Median` (`statistics.median`: the average of the two middle values for an even count), including the causal history's bucket median (the two middle replies are read when they straddle a bucket bound; `threads.TestFixtureMedians` checks every fixture standing against a brute-force median). One rank rule for percentiles, `mathx.Rank`: index `q·(n−1)` rounded half to even — legacy's p90 exactly (half-up would differ when n ≡ 6 mod 10). It is a rank selection; values still round half-up (`mathx.Round`).
- Totals add `sent` and `matched_sent`.
- Response buckets: `<1h`, `1-4h`, `4-24h`, `1-3d`, `>3d`, `no reply` (= inbox − replied).
- `volume_by_month` with received/sent/replied.
- Up to 3 most recent reply examples per sender: subject, received_at, responded_minutes, reply_preview.

**Conversation stats** (`threads.py:169-389`)
- Response medians in both directions, per domain and overall.
- Closing: who sent the thread's last message, attributed to the domain of the thread's first inbox message.
- Help balance, stance, info gaps, effort by domain, structural content means, per-domain averages of the model's signals, impersonation, volume patterns.

## 5. Taxonomy (`taxonomy.py:23-441`), quoted exactly

The 17 questions are listed in insertion order. Chunking depends on this order: the first 16 go in call 1 and `has_signature` goes alone in call 2.

**1. `category`** (choice). Instructions: "Classify this email into exactly ONE category by its NATURE — what kind of communication it is, not how important it feels. When several apply, take the earliest in this order: security, spam, finance, notifications, marketing, action, work. Internal colleague mail is categorized exactly like external mail. Any language; judge meaning, never words."
Criteria:
- security: "ACCOUNT-SECURITY events and threats: sign-in alerts, verification codes, password resets, MFA challenges, alerts about suspicious activity, and impersonation/phishing attempts. Evidence: the message concerns access to an account, or shows deception indicators. Note: 'security' describes what the mail is ABOUT; how likely it is to be a real threat is the separate suspicious judgment. A legitimate code from the real service is security with low suspicion."
- spam: "Unsolicited junk or bulk mail with no legitimate connection to the mailbox owner: generic promotions to a list, scams, chain letters. Evidence: no real relationship, no work context, mass-mailing nature."
- finance: "MONEY matters: invoices, payments, receipts, banking, billing, quotes, budgets, subscriptions, expenses, taxes, payroll. Evidence: amounts, payment instructions, or billing language."
- notifications: "AUTOMATED machine mail: alerts, digests, monitoring, CI/CD, build results, shipping updates, social-network activity, forum replies, webhooks. Evidence: system sender, generated content, no human sign-off — even when it asks for a click, it is still a notification, never action."
- marketing: "COMMERCIAL promotion: product announcements, newsletters, deals, webinar invitations, ads, press releases. Evidence: selling or promoting to an audience, not corresponding about work."
- action: "A HUMAN BEING explicitly asks the mailbox owner for something concrete: a reply to a question, a file, a decision, an approval, a task, a meeting, a payment. Evidence: an explicit ask directed at the owner in the text — without such an ask this category does not apply. Automated mail is never action regardless of what it requests."
- work: "HUMAN work correspondence that informs, updates, or coordinates without asking anything of the owner: status updates, threads, logistics, schedules, reports, receipts of process, thank-yous."
Report labels: Security, Spam, Finance, Notifications, Marketing, "Action required", "Work / FYI".

**2. `topic`** (choice). "What is this email about? Pick the single topic that best matches its request or content, regardless of language."
Criteria:
- project_work: "Ongoing projects, deliverables, tasks, and status of work."
- support_issue: "Problems, bug reports, incidents, troubleshooting, help requests."
- sales_inquiry: "Buying, selling, quotes, pricing, and commercial negotiation."
- billing_account: "Invoices, payments, account administration, contracts."
- meeting_scheduling: "Meetings, calls, availability, calendar invitations."
- technical_integration: "APIs, software setup, integrations, credentials, configuration."
- hiring: "Recruiting, job applications, interviews, staffing."
- partnership: "Partnerships, vendors, resellers, business development."
- internal_ops: "Company-internal operations, HR, policies, tools, administration."
- news_updates: "Newsletters, product updates, industry news, announcements."
- other: "Anything that fits none of the topics above."

**3. `reply_needed`** (noul). "Was a query directed at the mailbox owner present in this message when it arrived? Judge only what the message itself asks — whether it has since been handled is not knowable from the text and must not be guessed."
- true: "A query directed at the mailbox owner is present: a question aimed at them, or a direct request for information/confirmation that only they can give. Evidence: an interrogative sentence addressed to them, or an explicit ask to come back with an answer. Language is irrelevant; judge meaning."
- false: "No query is directed at the owner: pure FYI, acknowledgments, automated mail, the sender answering their own question, questions aimed at someone else, or a request that needs an action (send/approve/pay) rather than an answer."

**4. `urgency`** (score, 0–4). "How time-sensitive did the SENDER frame this as, at the moment the mail was sent? Judge the pressure the text expresses relative to the mail's own date — never relative to today, and never from your own guess of importance."
Levels:
- [0] "No time pressure stated or implied - whenever."
- [1] "Soft preference for speed - soon, when you can, this week if possible."
- [2] "A concrete near deadline - by Friday, tomorrow, end of week, before a named meeting that is days away."
- [3] "Explicitly urgent - urgent, ASAP, today, needed before end of day, a same-day meeting or deadline."
- [4] "Immediate and blocking - emergency, system down, everything is stopped, deadline is right now or already past when the mail was sent."

**5. `suspicious`** (noul). "Does this message show signs of being a phishing attempt, scam, or spoofed sender? Base the verdict only on deception indicators visible in the message — not on unfamiliarity."
- true: "Deception indicators are present: a spoofed or look-alike sender, credential or payment redirection, urgency combined with secrecy, an implausible request for money or data, a mismatch between who the mail claims to be and who it actually is, or an attempt to move the conversation to an unusual channel."
- false: "The mail reads as genuinely from who it claims to be: ordinary work context, consistent identity, no deception pattern. Unfamiliar sender or commercial tone alone is NOT suspicion — that is marketing or spam's job."

**6. `is_question`** (noul). "Does the TEXT contain questions that expect an answer — regardless of who must answer them? (Whether the answer must come from the mailbox owner specifically is a separate judgment.)"
- true: "The text contains at least one question that expects an answer from someone. Evidence: an interrogative sentence, or an implicit answer-request ('let me know', 'confirm whether…'). Rhetorical questions the text itself answers do not count."
- false: "Statements only, or rhetorical questions answered in the same message."

**7. `call_to_action`** (noul). "Does the message explicitly ask the mailbox owner to DO something concrete — as opposed to answering something? Judge only asks that are directed at them in the text."
- true: "An explicit, concrete action is requested of the mailbox owner: send, fix, review, approve, pay, register, attend, click. Evidence: an imperative or a directed request naming the act. Implied or background tasks that the mail only discusses do not count; automated mail never counts."
- false: "Nothing concrete is asked of the owner; the mail informs or closes."

**8. `decision_needed`** (noul). "Does the message ask the mailbox owner for a VERDICT — approve/reject, yes/no, choose between options? A decision is a judgment, not a task and not a mere question."
- true: "The owner must choose or rule on something to unblock progress: yes/no, approve or reject, pick between options, accept terms. Evidence: the mail explicitly presents a choice or asks for a verdict. A question that merely gathers information is not a decision."
- false: "No verdict is being asked for; information or work alone is requested."

**9. `asks_for`** (choice). "What is the sender primarily asking of the mailbox owner? Pick exactly one — the DOMINANT ask, by this precedence when several are present: a decision outranks a task, which outranks a document, which outranks a plain answer; money and time requests rank by what blocks the sender most. If nothing is asked, pick the FYI option."
Criteria:
- answer_question: "An answer to a question or a discussion."
- approval_decision: "An approval, decision, or sign-off."
- information_document: "Information, a document, a file, data, or access."
- payment_money: "Payment, quote, pricing, or budget."
- meeting_availability: "Time, availability, or a meeting."
- task_work: "Work to perform: fix, build, review, deliver, follow up."
- nothing_fyi: "Nothing at all; informational only."
- other: "None of the above."

**10. `deadline`** (score, 0–5). "What time frame does the text set for something to happen? Count explicit dates and deadlines AND meetings/events it proposes. Measure the distance from the mail's own date to the named date — a deadline two years ago was once two days away; judge it as the mail framed it."
Levels:
- [0] "No date, deadline, or event is mentioned."
- [1] "A date far from the mail's own date (months away)."
- [2] "A date within roughly a month of the mail's own date."
- [3] "A date within roughly a week of the mail's own date."
- [4] "A date within 24 hours of the mail's own date."
- [5] "Already overdue at the moment the mail was sent, or due that same day."

**11. `relationship`** (choice). "What is the sender's relationship to the mailbox owner? Judge from the content and the parties involved — internal colleagues, external clients, vendors and automated systems are distinct kinds of relationship. When unclear, pick unknown rather than guessing."
Criteria:
- existing_client: "An existing customer or client of the business."
- prospect_lead: "A potential customer, lead, or sales contact."
- colleague: "A coworker or someone inside the same organization."
- vendor_partner: "A supplier, partner, contractor, or service provider."
- automated_service: "An automated system, bot, notification, or no-reply service."
- recruiter: "Recruiting, HR, or job-related outreach."
- personal: "Personal, family, or friend."
- unknown: "Cannot be determined."

**12. `effort`** (score, 0–3). "How much work is it for the mailbox owner to handle this message properly? Estimate from what the text actually demands of them — reply length, research, building, coordinating. Judge the work, not the sender's rank or the message's importance."
Levels:
- [0] "None or seconds - nothing to do, or a one-line reply."
- [1] "Minutes - a short reply, a quick check, a click or a small lookup."
- [2] "Hours - real work: research, write, review, build, or fix something."
- [3] "Days - project-level work, coordination of several people, or a deliverable."

**13. `sentiment`** (score, 0–4). "What emotional tone does the SENDER express in this message toward the recipient or the situation? Judge the expressed tone only — politeness formulas are neutral."
Levels: "Very negative - angry, accusatory, or in conflict" · "Negative" · "Neutral" · "Positive" · "Very positive".

**14. `stance`** (choice). "What does this message primarily do in its conversation? Judge the message itself, whatever side of the exchange it is on."
Criteria:
- asks_questions: "Primarily asks questions or requests information."
- provides_answers: "Primarily provides answers, information, or an affirmative response."
- both: "Both asks and answers/provides in comparable measure."
- acknowledgment: "Pure acknowledgment: thanks, receipt confirmation, closing remarks."

**15. `info_sufficiency`** (noul). "Does the message contain everything needed to answer or act on it — context, specifics, links, files, dates — or is something missing that would force a follow-up question back? Evidence: can the needed response be written without inventing or hunting for facts?"
- true: "Self-contained: everything needed to act is present."
- false: "Incomplete: context, specifics, or attachments are missing."

**16. `request_count`** (score, 0–3). "How many DISTINCT asks does this message contain? Count by meaning — each separate question, demand, or task counts once; rephrasing the same ask in one message does not make it two."
Levels: "None - purely informational" · "One request" · "Two requests" · "Three or more requests".

**17. `has_signature`** (noul). "Does the message end with a personal signature block — a name with role, company, phone, address, or other contact details?"
- true: "Ends with a signature block."
- false: "No signature block."

**Roles** (`taxonomy.py:405-423`; the required question type is enforced in `profile.py:26-44`):

| Role | Question | Type |
|---|---|---|
| category | category | choice |
| topic | topic | choice |
| reply | reply_needed | noul |
| urgency | urgency | score |
| suspicious | suspicious | noul |
| question | is_question | noul |
| cta | call_to_action | noul |
| decision | decision_needed | noul |
| asks_for | asks_for | choice |
| deadline | deadline | score |
| relationship | relationship | choice |
| effort | effort | score |
| sentiment | sentiment | score |
| stance | stance | choice |
| info | info_sufficiency | noul |
| requests | request_count | score |
| signature | has_signature | noul |

**Thresholds:**

| Key | Default |
|---|---|
| reply | 0.5 |
| suspicious | 0.5 |
| low_confidence | 0.5 |
| urgent_from_level | 3 |
| question | 0.5 |
| cta | 0.5 |
| decision | 0.5 |
| deadline_soon_level | 3 |
| effort_heavy_level | 2 |
| sentiment_negative_max | 1.5 |
| info_gap | 0.5 |
| multi_request | 2 |

**Other defaults:**
- `prior_blend` {reply 0.3, suspicious 0.0, urgency 0.0}
- `prior_min_samples` 3
- `body_chars` 2000
- context {include_history, include_examples, include_facts all true; max_examples 6}

**Validation** (`profile.py:136-264`) reports every problem at once:
- choice: 2–255 options
- score: 2–10 levels
- noul: criteria keys only `true`/`false`
- unknown roles and unknown threshold keys are rejected
- level thresholds must be integers in [0, top], checked against the real scale even when the role was dropped
- template placeholders must belong to the known set

**Merging** (`profile.py:267-286`): questions, roles, thresholds, prior_blend and context merge key by key, and `null` removes a key. Other sections replace wholesale.

**Precedence:** environment `EC_STATE_BODY_CHARS` > profile file > built-ins.

## 6. Classification state (`classify.py`)

**Template**, exactly:
`"From: {sender}\nTo: {to_addresses}\nCc: {cc_addresses}\nDate: {received_at}\nSubject: {subject}\n\n{observed_facts}\n{history_context}\nEmail text:\n{body_preview}\n\n{examples}"`

**Rendering** (`build_state`, `classify.py:76-97`)
- First the body is truncated to `body_chars` (the model's view).
- `sender` is `"Name <addr>"`, or name, or address, or `"(unknown sender)"`.
- `body_preview` is trimmed.
- Fill the fields, replace `\n\n\n` with `\n\n` until none remain, then trim the result.

**Observed facts** (`features.py:378-397`) are computed on the truncated view. The block is `"Observed facts:\n"` followed by `- ` bullets, in this order:
1. "Subject is part of an ongoing thread (Re:/Fwd:)." — when the subject matches the prefix regex
2. "Addressed to {to} recipient(s) plus {cc} in Cc." — when to+cc > 1
3. "Has attachments."
4. "Contains {n} question mark(s)." — counts `?` in subject plus body
5. "Contains {n} link(s)." — matches `https?://|www\.`
6. "{n} words are in all caps." — matches `\b[A-ZÀ-Þ]{3,}\b`
7. "Length: {w} words in {s} sentences ({c} characters)." — always present. Words are `\S+`; sentences are the non-empty pieces after splitting on `[.!?]+[\s"')\]]*`.
8. "Arrived {Weekday} at {HH}:00 UTC." — when the date parses

**History context** (`history.py:159-185`) is prefixed with "How you handled this sender before: ". It is built from:
- "You replied to {replied} of the previous {received} messages from {this sender|the domain of this sender} ({rate:.0%}), typically within {fmt}." When there is no median, the sentence ends after the percentage with "."
- Then "Overall you reply to {replied} of {received} incoming messages ({rate:.0%}), median response time {fmt}."
- Duration format: `<60` → "N min"; `<1440` → "X.X h"; otherwise "X.X days".

**Examples block** (`feedback.py:176-202`) is headed "How the owner handled similar mail in the past:". Lines:
- `- You answered after {m:.0f} min a mail with subject "{s}" with: "{preview}"` — up to 3 reply examples for this sender
- `- Your decision on a mail from "{addr}" with subject "{s}": {question}={value}` — labels; float values are printed with `.2f`

**Answer parsing** (`classify.py:135-175`)
- Every profile question must be present, with `answer.type` equal to the question type.
- noul: `noul` in [0,1].
- choice: `choice` must be one of the criteria; `probabilities` keys must be a subset of the criteria and must include the choice; `confidence` in [0,1].
- score: `score` in [0, n−1]. It is a continuous expected value, e.g. 0.74. `probabilities` keys are `"0".."n-1"`; `confidence` in [0,1].
- Upper bounds allow a tolerance of 1e-9.
- The noul confidence is not stored.

**Prior blending** (`classify.py:178-189, 404-423`)
- Applies only to the reply, urgency and suspicious roles, and only when weight > 0, the value is numeric, and the prior exists with samples ≥ 1.
- Formula: `w·model + (1−w)·prior`, clamped to [0, n−1] for urgency and [0,1] otherwise.
- The reply prior is `(replied + k·global_rate)/(received + k)` with k = max(1, prior_min_samples). Lookup is by exact sender first, then by domain.
- The urgency prior is derived from the median response time: ≤120 min → 3.25, ≤480 → 2.5, ≤1440 → 1.75, ≤4320 → 1.25, otherwise 0.75, comparing the median rounded to 0.1 minute. It is dormant at weight 0. Go keeps exactly this (rules.json `resolution_minutes` 0.1: a bound b holds medians below b + 0.05), over the causal history.
- The suspicious prior comes from labels (section 8).

**Cache** (`classify.py:340-472`)
- JSONL lines `{id, fingerprint, state_hash, answers{name:{value, model_value, prior, confidence, probabilities}}}`.
- An entry is a hit only when the fingerprint AND `state_hash` (sha256 of the state, first 16 hex chars) match.
- Fingerprint: sha256 of `json.dumps(questions, sort_keys=True, ensure_ascii=True)`, first 12 hex chars. Python's default separators are `", "` and `": "`, and non-ASCII is escaped as `\uXXXX`. Go must reproduce this byte for byte to reuse existing caches.
- Each message is decided in 2 calls; 4 worker threads.

**Major cost risk found during this review.** `history_context` and the examples block include live global counts. Any new mail followed by `analyze` changes every state hash, so the whole cache is invalidated and all 33k messages are re-decided at 2 calls each. In Go, fix this before the first scan: bucket or round those numbers, or use a pinned behaviour snapshot.

## 7. SubQ Decisions client contract (`subq.py`)

**Request:** `POST {base}/systemone` (base `https://provider.example.test/v1`) with `Authorization: Bearer`, JSON body `{model:"subq-snap-preview", state, questions:{name:{type, instructions, criteria}}}`. `criteria` is an object for noul/choice and an ordered array for score.

**Response:** `{answers:{name:{type, noul | choice+probabilities+confidence | score+probabilities+confidence}}, usage.input_tokens}`. Only input tokens are billed.

**Chunking** (`subq.py:48,103-120`)
- At most 16 questions per call. A 17th question returns HTTP 422 "must NOT have more than 16 properties".
- Chunks are taken in insertion order, one call per chunk with the same state, and answers are merged.
- The state is billed on every chunk.
- For a pluggable SubQ/Jev/Clef interface, each provider must declare its own maximum.

**Retry policy** (`subq.py:52-58, 122-166`)
- Up to 5 attempts.
- Retry statuses 429, 502, 503, 504, with backoff `min(60, 2^attempt)`.
- Connection-level errors (`URLError`/`ConnectionError`, i.e. the request never left the host; this includes connect timeouts) are retried with `min(8, 2^attempt)`.
- A **read timeout is terminal**, because the request was delivered and retrying is a guaranteed double bill. In Go: retry dial errors only; a deadline after the request was written is terminal.
- 500 is never retried, since it may already have consumed tokens.
- 402 maps to "insufficient credits". 401/403 map to "key/model rejected".
- Any other OS-level error is wrapped and never escapes raw.
- The response must be a JSON object containing an `answers` object.

**`finite_number`** (`subq.py:202-218`) is the single number-coercion function.
- Booleans are not numbers, so a JSON `true` must never become 1.0.
- NaN and Inf are rejected.
- Numeric strings are accepted, because Python's `float("0.5")` succeeds.
- Two policies sit on top of it:
  - `answer_number` / `answer_probability` return a default instead of raising, and treat a probability outside [0,1] as the default. Used for ranking.
  - `classify._bounded_number` raises a `ContractError`, so a bad classification fails loudly.

## 8. Feedback and evaluation (`feedback.py`)

**Label storage.** `labels.jsonl` is append-only. Each line is `{message_id, question, value, created_at, sender_address, subject, source:"manual"}`.

**Parsing label input** (`cli.py:453`)
- choice: must be one of the criteria.
- noul: `true/yes/1` → 1.0, `false/no/0` → 0.0, or a float in [0,1].
- score: a float in [0, top].

**Few-shot selection** (`feedback.py:110-145`)
- Score each label: +3 for the same sender, else +2 for the same domain, plus the Jaccard overlap of subject tokens (`[a-zA-Z0-9@._-]{3,}`, lowercased).
- Keep labels with score > 0, sort by (score, created_at) descending, deduplicate on (message_id, question), keep up to max_examples (6).
- Only labels for questions in the current profile are used.
- The reply-example fallback to the domain is dead code, because examples are keyed by address only.

**Suspicious priors** (`feedback.py:149-173`)
- A label is positive when its question is literally `"suspicious"` with value ≥ 0.5, or when it is a category label of spam or security.
- Results are bucketed per address and per domain.
- An address needs 1 sample; a domain needs 2. The prior is the mean, with samples reported as 1.

**Evaluation** (`feedback.py:277-409`) compares labels against the final, blended values.
- noul: cut at 0.5; report accuracy, precision, recall, F1 and a confusion matrix.
- choice: accuracy, per-option recall, confusion matrix.
- score: mean absolute error and the share within 0.5 of a level.
- Weak labels: "did the owner reply", from `match_replies`, compared with `reply_needed`.
- **Leak:** the reply value being judged is 70% a prior learned from those same replies. In Go, compare weak labels against `model_value`.

**Calibration** (`feedback.py:424-482`)
- Applies to roles reply, suspicious, question, cta, decision, and info (threshold key `info_gap`), and needs at least 3 labeled pairs.
- Sweep the threshold from 0 to 1 in steps of 0.05 and maximize (F1, accuracy). Ties keep the lowest threshold.
- Output is suggested threshold values to paste into the profile.

## 9. Correlations and insights

The sample floor is `MIN_SAMPLE=5`. All latencies are the owner's reply time on inbox mail.

1. **Asks_for vs reply time:** median latency grouped by asks_for. No floor.
2. **Sentiment vs reply time:** sentiment is bucketed <1.5 negative, >2.5 positive, otherwise neutral. No floor.
3. **Deadline vs reply time:** grouped by `int(score)` (rounded down). No floor.
4. **Questions vs reply rate:** reply rate when is_question ≥ 0.5 versus below. Needs n ≥ 5 in total.
5. **Completeness vs reply time:** median latency when info > info_gap versus ≤ info_gap. Both groups must be non-empty.
6. **Effort by topic:** median effort per topic. Each topic needs ≥ 5.
7. **Length vs reply time:** Pearson r between word count (full body) and latency. Needs n ≥ 5 and non-zero standard deviation on both. |r| > 0.15 is reported as meaningful.
8. **Request count vs reply time:** grouped by `int(request_count)`. No floor.
9. **Reply rate by relationship:** each relationship needs ≥ 5.
10. **Reply stance vs their ask:** cross-tab of the counterpart message's asks_for against the stance of the owner's reply; top 20 cells. The current counterpart lookup is O(n²); index by thread key in Go.

**Insights** (`threads.py`, `insights.py`)
- Top 25 senders and top 15 domains.
- You-vs-them speed. A domain is "faster" when its factor is ≥ 1.15 and "slower" when ≤ 0.87.
- Closing rate. Help balance: >0.2 means you are the helper, <−0.2 means you request help; it counts only the asks in {answer, task, info, meeting, approval}.
- Stance, info gaps, effort per domain, structural content means, per-domain averages of the model's signals.
- Volume: weekday, week of month `W((day−1)//7+1)`, month, northern-hemisphere season, per month, year-over-year on months present in both years, fluctuation (population standard deviation and coefficient of variation, needs ≥ 2 months).
- Impersonation: inbox mail with suspicious ≥ threshold, flagged when the sender's domain is a known domain. Known domains are those classified as client/colleague/prospect plus any domain with ≥ 3 received messages. This catches spoofed or compromised exact domains; there is no look-alike detection.
- **All times are UTC, not the owner's local time.**
- **Rounding is inconsistent:** correlations round down with `int()`, while the report rounds half-up. Pick one rule in Go.

## 10. Invariants the Go version must keep

**Pipeline and storage**
- A run that classified 0 messages and had errors must never overwrite existing results. Keep the old ones and exit with an error (NOTES issue 4, `cli.py:384`).
- Per-message failures never abort a run. They are stored as null and reported separately from stale results (another spec). Exit code 4 means partial.
- Results decided under another question fingerprint are discarded, never mixed in. Coverage is always disclosed.
- The cache stores RAW model answers and priors are blended at load time. Saving the cache keeps the union of entries, so a run over a subset never evicts other entries.
- `body_chars` is the only place that truncates the state. Archive size never changes the state.
- Any change to the spec invalidates the cache through the fingerprint. Tests derive scale bounds from the taxonomy (the urgency change from 4 to 5 levels broke hardcoded tests). `urgent_from_level=3` means "explicitly urgent or worse".
- Writes are atomic: temp file plus rename. The DB is published by building it, then `VACUUM INTO` a temp file, then rename.
- Before reading inputs, take their fingerprints (size and mtime). The DB is rebuilt only on a mismatch, and stale content is never marked fresh.
- There is one derivation of `replied` and `response_minutes`. In `query`, `replied` used to be always 0.
- When a SQLite path goes into a `file:` URI it must be percent-encoded; a `?` or `#` in the path silently created a different empty DB.

**Input validation and parsing**
- Numeric settings reject booleans, fractional values and out-of-range values, and the error names the setting. Truncation budgets must be ≥ 1.
- Unknown profile keys are errors, not silently ignored.
- Item-level read failures are skipped and counted. Output failures abort the run. A skip ratio ≥ 1% is fatal. Counts must reconcile.
- Deduplicate on id (store id plus EntryID) and warn when ids are empty.
- Never parse localized strings. Dates are always UTC RFC3339 with `Z`. Months are 1-based; with 0-based months, January crashed. Normalize every stored timestamp to one string form, because `windows.py` compares `+00:00` bounds against `Z` strings.
- Correct MAPI tags: SCL is `0x40760003`; there is no PidTagFlagComplete, no PidTagReadTime, and no `IPM.Note.ReadReceipt` class.
- Never show a read time. Importance is evidence only.
- Never drop non-SMTP senders, because the DNs are the owner's own.

**Decision API**
- Chunk at the provider's question cap inside the client.
- Never retry after a read timeout or a 500.
- Booleans are never numbers.
- Use half-up rounding, not banker's rounding. Compare decisiveness margins on rounded whole percents.
- Clamp negative budgets; Python's negative indexing wrapped them around.

**Answer honesty.** These were learned in the `ask` and `factfind` work and apply directly to the Claude agent's tool results.
- Totals come from a full COUNT over the same WHERE clause, never from the limited rows. Label tables "Top N".
- Yes/no claims such as "all answered" are backed by an aggregate over the whole time window.
- A NULL decision column means unclassified. Filters that depend on decisions must degrade and say so, because `NULL >= 0.5` silently returned zero rows. Disclose "judged X of Y".
- A month given without a year means that month in every year, and the answer says so. An explicit year or month overrides a relative window. Every windowed answer or miss states the window it searched.
- "Last contact" uses only inbox mail; never quote the owner's own sent mail as the correspondent's words.
- Comparative questions group by domain with sample floors (≥ 3).
- Year-over-year compares only months present in both years.
- Search pools hits across the whole search budget instead of stopping at the first hit. Shortlists have one entry per unique address (one `noreply@` filled all 12 slots).
- Assert a single answer only when it is decisive: top ≥ 0.5 and ahead of the runner-up by ≥ 10 points. Otherwise show the ranked candidates with sender, date and subject.
- When judging candidates with a typed-decision model, use one forced choice, not separate yes/no questions; separate questions over-triggered (0.96 on "Bonjour", 12 of 12 entities ≥ 0.5).
- SQL from the agent: strip comments to a space (not nothing), check quotes, allow a single SELECT/WITH only, and reject mutation statements, including `UPDATE OR` and quoted or schema-qualified tables. Better still, also open SQLite read-only with `query_only`.

## 11. What to drop

- **`outlook_relay.py`** in full, plus the task, marker, scheduling and PowerShell parts of `outlook_export.ps1`. Port only its field mapping and SMTP resolution chain to Go COM.
- **`graph.py`, `msauth.py`**, and the `config.py` MS_* settings and token cache. Device-code sign-in, the Office client ID, and the AADSTS 65001/65002 handling all go.
- **`asking.py`**: the slot compiler, intents, facets, `candidate_tokens` and `parse_question`. Also the SubQ forced-choice pipeline in **`factfind.py`**, and the slot coupling in **`windows.py`**. The agent replaces all of these; keep their disclosure rules as tool contracts (section 10).
- **`cli.py`**, `report.py`'s markdown and console output (keep a CSV export if wanted), `log.py`, and the gzip JSONL artifacts with their `_resolve_artifact` sibling lookup. SQLite becomes the store.
- **The `.env` key file.** Use the Windows Credential Manager or DPAPI instead.
- `--attach`, the relay's marker-based recovery, and the "export already running" probe. If concurrent syncs are possible, replace the probe with a single-instance lock.
- **Keep and port:** `taxonomy` (as JSON), `profile` validation and merging, `classify`, `subq` (behind the decision-model interface), `features`, `scope`, `history`, `threads`, `correlations`, `insights` (as data for the app's views), `feedback`, and `query`'s schema and SQL validation (as the agent's read-only SQL tool).
