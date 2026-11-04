import { readFileSync } from "node:fs";
import path from "node:path";

// The screenshot fixtures are invented. Sender names and addresses come from
// senders.json, which the truth test in internal/site checks against the
// reserved .test domain, so no real mailbox can enter a capture.
type Sender = { name: string; address: string; role: string };

export const senders: Sender[] = JSON.parse(
  readFileSync(path.join(__dirname, "senders.json"), "utf8"),
) as Sender[];

export function sender(name: string): Sender {
  const found = senders.find((entry) => entry.name === name);
  if (!found) throw new Error("the capture cast has no sender named " + name);
  return found;
}

// FIXED_NOW is the clock every capture runs at. The capture config fixes the
// viewport and the zone; this fixes the date so relative times and "generated"
// stamps never drift between runs.
export const FIXED_NOW = "2026-11-03T09:15:00Z";

// The captures run the Linux preview, so the invented data directory uses the
// XDG path a Linux owner would see. A Windows path here would contradict the
// caption that says the screens come from the Linux preview.
export const DATA_DIR = "/home/sam/.local/share/mailsense";

const AT_EARLY = "2026-11-03T07:40:00Z";
const AT_SOON = "2026-11-03T08:20:00Z";
const AT_LAST = "2026-11-02T16:10:00Z";
const AT_OLDER = "2026-11-01T11:05:00Z";

type MailRecord = {
  id: number;
  subject: string;
  sender: string;
  at: string;
  role: "inbox" | "sent";
  read: boolean;
  snippet: string;
  body: string;
  reasons: string[];
  score: number;
  action: { primary: string; waiting: string };
  topic: string;
  subtopic: string;
  category: string;
  asksFor: string;
};

const MAIL: Record<number, MailRecord> = {
  4101: {
    id: 4101,
    subject: "Signed proposal for the Northwind launch",
    sender: "Atelier Nord",
    at: AT_EARLY,
    role: "inbox",
    read: false,
    snippet:
      "The signed copy is attached. Can you confirm the delivery window before Thursday?",
    body: "Hello,\n\nThe signed copy of the proposal is attached. We can hold the Thursday delivery window if you confirm by the end of tomorrow.\n\nThanks,\nAtelier Nord",
    reasons: ["decision", "deadline"],
    score: 94.5,
    action: { primary: "decide", waiting: "owner" },
    topic: "project_work",
    subtopic: "approval",
    category: "action",
    asksFor: "approval_decision",
  },
  4102: {
    id: 4102,
    subject: "Invoice 1042 needs a second look",
    sender: "Studio Lumen",
    at: AT_SOON,
    role: "inbox",
    read: false,
    snippet:
      "The payment link points to a domain we do not use. Please check before paying.",
    body: "Hello,\n\nInvoice 1042 is ready. The payment link points to a domain we do not use, so please check it before you pay. The amount and the reference are unchanged.\n\nStudio Lumen",
    reasons: ["security"],
    score: 100,
    action: { primary: "verify", waiting: "owner" },
    topic: "billing_account",
    subtopic: "payment",
    category: "security",
    asksFor: "payment_money",
  },
  4103: {
    id: 4103,
    subject: "Thursday pickup window",
    sender: "Harbor Logistics",
    at: AT_LAST,
    role: "inbox",
    read: true,
    snippet:
      "We can collect between 9 and 11. Does that still work for your team?",
    body: "Hello,\n\nWe can collect the two crates between 9 and 11 on Thursday. Does that still work for your team? If not, reply with a window that does.\n\nHarbor Logistics",
    reasons: ["reply", "deadline"],
    score: 71.2,
    action: { primary: "reply", waiting: "owner" },
    topic: "meeting_scheduling",
    subtopic: "availability",
    category: "action",
    asksFor: "meeting_availability",
  },
  4104: {
    id: 4104,
    subject: "Re: Signed proposal for the Northwind launch",
    sender: "Sam Rivera",
    at: AT_OLDER,
    role: "sent",
    read: true,
    snippet: "Thanks. I will confirm the delivery window tomorrow morning.",
    body: "Thanks. I will confirm the delivery window tomorrow morning.\n\nSam",
    reasons: [],
    score: 0,
    action: { primary: "none", waiting: "other" },
    topic: "project_work",
    subtopic: "approval",
    category: "work",
    asksFor: "nothing_fyi",
  },
  4105: {
    id: 4105,
    subject: "Workshop notes and the two open questions",
    sender: "Morgan Ellis",
    at: AT_OLDER,
    role: "inbox",
    read: true,
    snippet: "The notes are in the shared folder. Two questions are still open.",
    body: "Hello,\n\nThe workshop notes are in the shared folder. Two questions are still open: the delivery window and the colour of the second print run.\n\nMorgan",
    reasons: [],
    score: 0,
    action: { primary: "read", waiting: "none" },
    topic: "project_work",
    subtopic: "review",
    category: "work",
    asksFor: "information_document",
  },
};

function record(id: number): MailRecord {
  const found = MAIL[id];
  if (!found) throw new Error("the capture fixtures have no message " + id);
  return found;
}

function priorityItem(id: number) {
  const mail = record(id);
  const who = sender(mail.sender);
  return {
    message_id: id,
    subject: mail.subject,
    sender_name: who.name,
    sender_address: who.address,
    received_at: mail.at,
    folder_role: mail.role,
    has_attachments: false,
    snippet: mail.snippet,
    score: mail.score,
    reasons: mail.reasons,
    preview: false,
    signals: {
      subject: {
        topic: mail.topic,
        subtopic: mail.subtopic,
        path: mail.topic + "/" + mail.subtopic,
      },
    },
    action: mail.action,
    reply_open: mail.reasons.includes("reply"),
  };
}

export function priorityView(filter = "all") {
  const items = [4101, 4102, 4103].map(priorityItem);
  const selected =
    filter === "reply"
      ? items.filter((item) => item.reasons.includes("reply"))
      : filter === "deadline"
        ? items.filter((item) => item.reasons.includes("deadline"))
        : filter === "urgent"
          ? []
          : items;
  return {
    items: selected,
    total: selected.length,
    offset: 0,
    available: selected.length,
    has_more: false,
    counts: { all: 3, reply: 1, urgent: 0, security: 1, decision: 1, deadline: 2, workload: 0 },
    generated_at: FIXED_NOW,
    preview: false,
  };
}

export function messageListRow(id: number) {
  const mail = record(id);
  const who = sender(mail.sender);
  return {
    row_id: id,
    subject: mail.subject,
    sender_name: who.name,
    sender_address: who.address,
    sent_representing_address: who.address,
    instant: mail.at,
    received_at: mail.at,
    sent_on: "",
    role: mail.role,
    folder_path: mail.role === "inbox" ? "Inbox" : "Sent",
    store_key: "store-outlook",
    store_name: "Outlook",
    is_read: mail.read,
    has_attachments: false,
    snippet: mail.snippet,
    recipients: [],
  };
}

export function messageListPage() {
  return {
    messages: [4101, 4102, 4103, 4105, 4104].map(messageListRow),
    total: 5,
    counts: { inbox: 4, sent: 1, drafts: 0, deleted: 0, junk: 0, archive: 0, other: 0 },
    next_before: "",
    has_more: false,
  };
}

export function messageView(id: number) {
  const mail = record(id);
  const who = sender(mail.sender);
  return {
    row_id: id,
    revision: 1,
    store_kind: "outlook",
    verdict: "owner",
    reason: "owner",
    message: {
      source_id: "outlook",
      store_id: "store-outlook",
      store_name: "Outlook",
      folder_path: mail.role === "inbox" ? "Inbox" : "Sent",
      folder_role: mail.role,
      id: "msg-" + id,
      entry_id: "entry-" + id,
      long_term_entry_id: "long-" + id,
      search_key: "search-" + id,
      origin: "outlook",
      subject: mail.subject,
      sender_name: who.name,
      sender_address: who.address,
      sent_representing_address: who.address,
      recipients: [],
      addressed_to_me: "to",
      received_at: mail.at,
      sent_on: "",
      creation_time: mail.at,
      last_modification_time: mail.at,
      is_read: mail.read,
      has_attachments: false,
      importance: 1,
      sensitivity: 0,
      body: mail.body,
      body_truncated: false,
      attachments: [],
      folders: [],
      conversation_id: "",
      partial: false,
      alterations: [],
    },
    sender: {
      from_address: who.address,
      sender_address: who.address,
      on_behalf_of: false,
      reply_to: "",
      return_path: "",
      authentication: { reported: false, spf: "", dkim: "", dmarc: "", source: "" },
    },
    body_format: "text",
    body_html: "",
    body_css: "",
    body_html_truncated: false,
    body_remote_images: 0,
    body_blocked_images: 0,
    body_inline_images: 0,
    body_missing_inline: 0,
  };
}

export function triageResult(id: number) {
  const mail = record(id);
  return {
    message_id: id,
    values: {
      category: { type: "choice", choice: mail.category, confidence: 0.91 },
      topic: { type: "choice", choice: mail.topic, confidence: 0.88 },
      asks_for: { type: "choice", choice: mail.asksFor, confidence: 0.84 },
    },
    subject: {
      topic: mail.topic,
      subtopic: mail.subtopic,
      path: mail.topic + "/" + mail.subtopic,
      basis: "ask:" + mail.asksFor,
    },
    stale: false,
    escalate: false,
    preview: false,
  };
}

export function agendaView() {
  return {
    horizon_hours: 8,
    items: [
      {
        kind: "reply",
        at: "2026-11-03T08:30:00Z",
        last_at: AT_OLDER,
        title: "Workshop notes and the two open questions",
        message_id: 4105,
        reasons: ["waiting_since"],
      },
      {
        kind: "reply",
        at: "2026-11-03T09:00:00Z",
        last_at: AT_LAST,
        title: "Thursday pickup window",
        message_id: 4103,
        linked_event_title: "Northwind launch review",
        reasons: ["waiting_since", "meeting_with_them"],
      },
      {
        kind: "event",
        at: "2026-11-03T10:00:00Z",
        title: "Northwind launch review",
        detail: "Launch review",
        location: "Room 2",
        all_day: false,
        reasons: ["meeting"],
      },
      {
        kind: "event",
        at: "2026-11-03T11:00:00Z",
        title: "Supplier call",
        detail: "Delivery desk",
        location: "Call",
        all_day: false,
        reasons: ["meeting"],
      },
      {
        kind: "deadline",
        at: "2026-11-03T12:30:00Z",
        title: "Signed proposal for the Northwind launch",
        message_id: 4101,
        reasons: ["due"],
      },
      {
        kind: "deadline",
        at: "2026-11-03T15:00:00Z",
        title: "Invoice 1042 needs a second look",
        message_id: 4102,
        reasons: ["due"],
      },
    ],
    events: 2,
    obligations: 2,
    backlog: 2,
    deadlines: 2,
  };
}

export function hubView() {
  return {
    generated_at: FIXED_NOW,
    gate: {
      open: true,
      state: "done",
      labels: { manual: 812, judge: 35, needed: 1284, review: 12 },
      questions: [
        { question: "category", manual: 412, judge: 20, needed: 500, review: 8 },
        { question: "topic", manual: 400, judge: 15, needed: 500, review: 4 },
      ],
      blockers: [],
      certified: 4,
      total: 9,
      labels_done: 812,
      labels_total: 1284,
    },
    mailbox: { stores: 1, messages: 1284 },
    recent_answers: [
      {
        message_id: 4102,
        question: "category",
        value: "security",
        at: "2026-11-03T08:55:00Z",
      },
    ],
  };
}

const ASK_QUESTIONS: Record<string, string[]> = {
  en: [
    "Who is waiting on the Northwind proposal?",
    "Which invoice still needs a check?",
    "When is the pickup window?",
    "What did the workshop cover?",
    "Which threads went quiet?",
    "What changed since yesterday?",
  ],
  fr: [
    "Qui attend la proposition Northwind ?",
    "Quelle facture demande encore une vérification ?",
    "Quand a lieu le créneau d'enlèvement ?",
    "Que couvraient les notes d'atelier ?",
    "Quels fils sont restés sans réponse ?",
    "Qu'est-ce qui a changé depuis hier ?",
  ],
};

const ASK_ANSWERS: Record<string, string> = {
  en: "Three threads still need you. Atelier Nord is holding the Thursday delivery window until you confirm the signed proposal. Harbor Logistics asked which pickup window works, and Studio Lumen sent an invoice whose payment link points somewhere unexpected. The first two are ordinary replies. The invoice is the one to check before you pay.",
  fr: "Trois fils attendent encore votre réponse. Atelier Nord garde la fenêtre de livraison de jeudi jusqu'à ce que vous confirmiez la proposition signée. Harbor Logistics demande quel créneau d'enlèvement convient, et Studio Lumen a envoyé une facture dont le lien de paiement mène ailleurs. Les deux premiers sont des réponses ordinaires. La facture est le message à vérifier avant de payer.",
};

export function askHistoryView(lang = "en") {
  const questions = ASK_QUESTIONS[lang] || ASK_QUESTIONS.en;
  return {
    items: questions.map((question, index) => ({
      id: 7 - index,
      question,
      mode: "generated",
      created_at: index === 0 ? "2026-11-03T08:50:00Z" : "2026-11-0" + Math.max(1, 3 - index) + "T15:30:00Z",
    })),
  };
}

export function askAnswerView(lang = "en", question?: string, id = 8) {
  const mail = record(4101);
  const questions = ASK_QUESTIONS[lang] || ASK_QUESTIONS.en;
  return {
    id,
    conversation_id: "conv-screens",
    question: question || questions[0],
    answer: ASK_ANSWERS[lang] || ASK_ANSWERS.en,
    route: "auto",
    mode: "generated",
    intent: "freeform",
    counts: {},
    citations: [
      {
        message_id: 4101,
        subject: mail.subject,
        sender_name: sender(mail.sender).name,
        received_at: mail.at,
        reason: "decision",
      },
      {
        message_id: 4103,
        subject: record(4103).subject,
        sender_name: record(4103).sender,
        received_at: record(4103).at,
        reason: "reply",
      },
      {
        message_id: 4102,
        subject: record(4102).subject,
        sender_name: record(4102).sender,
        received_at: record(4102).at,
        reason: "security",
      },
    ],
    provider: "Workstation endpoint",
    model: "mail-8b",
    took_ms: 640,
    preview: false,
    created_at: FIXED_NOW,
  };
}

export function questionsView() {
  return {
    name: "screens",
    fingerprint: "screens",
    questions: [
      {
        name: "category",
        type: "choice",
        instructions: "Pick the category that best describes the message itself.",
        options: [
          { name: "security", description: "A security or trust problem." },
          { name: "finance", description: "A money matter." },
          { name: "action", description: "An action is requested." },
          { name: "work", description: "A work update." },
        ],
      },
      {
        name: "topic",
        type: "choice",
        instructions: "Choose the closest subject area.",
        options: [
          { name: "project_work", description: "Work on a project." },
          { name: "billing_account", description: "Billing or an account." },
          { name: "meeting_scheduling", description: "A meeting or a time." },
          { name: "other", description: "Nothing else fits." },
        ],
      },
      {
        name: "reply_needed",
        type: "bool",
        instructions: "Yes when a person is waiting on an answer from you.",
      },
    ],
  };
}

export function labelsSummaryView() {
  return {
    manual: 812,
    judge: 35,
    needed: 1284,
    review: 12,
    questions: [
      { question: "category", manual: 412, judge: 20, needed: 500, review: 8 },
      { question: "topic", manual: 400, judge: 15, needed: 500, review: 4 },
      { question: "reply_needed", manual: 0, judge: 0, needed: 284, review: 0 },
    ],
  };
}

export function labelsQueueView(question = "category") {
  const tasks =
    question === "category"
      ? [
          {
            message_id: 4102,
            question: "category",
            reason: "uncertain",
            suggestion: "security",
            suggestion_source: "judge",
          },
          {
            message_id: 4103,
            question: "category",
            reason: "uncertain",
            suggestion: "action",
            suggestion_source: "judge",
          },
          {
            message_id: 4101,
            question: "category",
            reason: "uncertain",
            suggestion: "action",
            suggestion_source: "judge",
          },
        ]
      : [];
  return {
    question,
    strategy: "uncertainty",
    tasks,
    total: tasks.length,
    has_more: false,
  };
}

export function learningView() {
  return {
    open: true,
    preview: false,
    status: {
      state: "learning",
      scope: "store:outlook",
      at: "2026-11-03T09:05:00Z",
      stages: [
        {
          id: "mailboxes",
          state: "done",
          progress: { done: 1, total: 1, unit: "mailboxes" },
        },
        {
          id: "embeddings",
          state: "done",
          progress: {
            done: 1284,
            total: 1284,
            unit: "messages",
            embedded: 1284,
            unchanged: 0,
            failed: 0,
          },
        },
        {
          id: "labels",
          state: "running",
          eta: 1_860,
          progress: { done: 812, total: 1284, unit: "messages" },
        },
        {
          id: "questions",
          state: "blocked",
          progress: { done: 4, total: 9, unit: "questions" },
          blockers: [
            {
              code: "layer1_too_few_labels",
              subject: "category",
              measure: { done: 412, needed: 500, unit: "labels" },
            },
          ],
        },
      ],
    },
  };
}

export function settingsView(lang = "en") {
  const row = (
    key: string,
    group: string,
    type: string,
    value: unknown,
    description: string,
  ) => ({
    key,
    type,
    default: type === "bool" ? false : "",
    group,
    description,
    value,
    layer: "file",
    restart_pending: false,
  });
  const language = {
    ...row("ui.language", "Interface", "enum", lang, "The language of the interface."),
    enum: ["auto", "en", "fr"],
  };
  return {
    settings: [
      language,
      row("time.zone", "Interface", "string", "Europe/Paris", "The zone used to show dates and times."),
      row("qa.enabled", "Ask", "bool", true, "Allow questions over the local index."),
      row("qa.max_messages", "Ask", "int", 12, "How many messages one answer may read."),
      row("sync.details.enabled", "Mailboxes", "bool", true, "Read message bodies during a sync."),
      row("triage.folder_roles", "Mailboxes", "string", "Inbox, Archive", "Folders that triage reads."),
      row("threads.folder_roles", "Mailboxes", "string", "Inbox, Sent", "Folders used to follow threads."),
      row("learning.test_mode", "Learning", "bool", false, "Run learning on a small preview set."),
    ],
    total: 24,
    restart_pending: [],
  };
}

export function statusView(lang: string) {
  return {
    version: "0.1.0",
    launch: "2026-11-03T08:58:00Z",
    time_zone: "Europe/Paris",
    data_dir: DATA_DIR,
    system_language: lang === "fr" ? "fr" : "en",
    system_locale: lang === "fr" ? "fr-FR" : "en-US",
    sources: [{ id: "outlook", kind: "outlook" }],
  };
}

export function mailboxView(selected: boolean) {
  return {
    id: "store-outlook",
    key: "store-outlook",
    name: "Outlook",
    source_id: "outlook",
    kind: "outlook",
    selected,
    discovered: true,
    default: true,
    folders: 12,
    items: 1284,
    identity: { addresses: [sender("Sam Rivera").address] },
    declared: [],
  };
}

export function setupView(lang: string) {
  return {
    needed: true,
    data_dir: DATA_DIR,
    // A fresh database holds the schema and nothing else. Reporting the real
    // size keeps the capture honest instead of showing an unavailable fact.
    database_bytes: 2_097_152,
    database_size_known: true,
    time_zone: "Europe/Paris",
    language: lang === "fr" ? "fr" : "en",
    done: false,
  };
}

export function bootView() {
  return { state: "ready", done: 6, total: 6, failed: 0, views: [] };
}

export function gateView() {
  return { allowed: true, open: true, preview: false };
}
