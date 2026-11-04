// Synthetic, PII-free fixtures. Shapes mirror the Go DTOs named in each
// comment; when a server view changes, update the builder and the matching
// spec together. No real mailbox data ever enters this directory.

export const AT = "2026-10-25T10:00:00Z";
export const AT_LATER = "2026-10-25T11:00:00Z";

export const msg = {
  invoice: 101,
  budget: 102,
};

export const conversation = "conv-e2e";

/** internal/priority.Item */
export function priorityItem(
  id: number,
  overrides: Record<string, unknown> = {},
) {
  const invoice = id === msg.invoice;
  return {
    message_id: id,
    subject: invoice ? "Synthetic invoice" : "Synthetic budget",
    sender_name: invoice ? "Demo Sender" : "Ada Lovelace",
    sender_address: invoice ? "demo@example.test" : "ada@acme.test",
    received_at: invoice ? AT : AT_LATER,
    folder_role: "inbox",
    has_attachments: false,
    snippet: invoice
      ? "Please review the synthetic invoice before Friday."
      : "The synthetic budget is ready for review.",
    score: invoice ? 87.5 : 71.2,
    reasons: invoice ? ["reply"] : ["deadline"],
    preview: true,
    signals: {
      subject: invoice
        ? {
            topic: "billing_account",
            subtopic: "payment",
            path: "billing_account/payment",
          }
        : {
            topic: "project_work",
            subtopic: "approval",
            path: "project_work/approval",
          },
    },
    action: invoice
      ? { primary: "pay", waiting: "owner" }
      : { primary: "decide", waiting: "owner" },
    reply_open: true,
    ...overrides,
  };
}

/** internal/priority + api.PriorityView */
export function priorityView(filter = "all") {
  const items =
    filter === "all"
      ? [priorityItem(msg.invoice), priorityItem(msg.budget)]
      : filter === "reply"
        ? [priorityItem(msg.invoice)]
        : filter === "deadline"
          ? [priorityItem(msg.budget)]
          : [];
  return {
    items,
    total: items.length,
    offset: 0,
    available: items.length,
    has_more: false,
    counts: {
      all: 2,
      reply: 1,
      urgent: 0,
      security: 0,
      decision: 0,
      deadline: 1,
      workload: 0,
    },
    generated_at: AT,
    preview: true,
  };
}

/** api.MessageView */
export function messageView(id: number) {
  const invoice = id === msg.invoice;
  const synthetic = id >= 1000;
  const subject = synthetic
    ? `Synthetic message ${id}`
    : invoice
      ? "Synthetic invoice"
      : "Synthetic budget";
  const senderName = synthetic
    ? `Synthetic sender ${id}`
    : invoice
      ? "Demo Sender"
      : "Ada Lovelace";
  const senderAddress = synthetic
    ? `sender${id}@example.test`
    : invoice
      ? "demo@example.test"
      : "ada@acme.test";
  return {
    row_id: id,
    revision: 1,
    store_kind: "outlook",
    verdict: "owner",
    reason: "owner",
    message: {
      source_id: "src-e2e",
      store_id: "store-e2e",
      store_name: "Demo mailbox",
      folder_path: "Inbox",
      folder_role: "inbox",
      id: "msg-" + id,
      entry_id: "entry-" + id,
      long_term_entry_id: "long-" + id,
      search_key: "search-" + id,
      origin: "outlook",
      subject,
      sender_name: senderName,
      sender_address: senderAddress,
      sent_representing_address: senderAddress,
      recipients: [],
      addressed_to_me: "to",
      received_at: invoice ? AT : AT_LATER,
      sent_on: "",
      creation_time: invoice ? AT : AT_LATER,
      last_modification_time: invoice ? AT : AT_LATER,
      is_read: true,
      has_attachments: false,
      importance: 1,
      sensitivity: 0,
      body: synthetic
        ? `Synthetic body for message ${id}.`
        : invoice
          ? "Please review the synthetic invoice before Friday."
          : "The synthetic budget is ready for review.",
      body_truncated: false,
      attachments: [],
      folders: [],
      conversation_id: "",
      partial: false,
      alterations: [],
    },
    sender: {
      from_address: senderAddress,
      sender_address: senderAddress,
      on_behalf_of: false,
      reply_to: "",
      return_path: "",
      authentication: {
        reported: false,
        spf: "",
        dkim: "",
        dmarc: "",
        source: "",
      },
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

/** internal/triage.Result */
export function triageResult(id: number) {
  const invoice = id === msg.invoice;
  return {
    message_id: id,
    values: invoice
      ? {
          category: { type: "choice", choice: "finance", confidence: 0.9 },
          topic: { type: "choice", choice: "billing_account", confidence: 0.9 },
          asks_for: {
            type: "choice",
            choice: "payment_money",
            confidence: 0.9,
          },
        }
      : {},
    subject: invoice
      ? {
          topic: "billing_account",
          subtopic: "payment",
          path: "billing_account/payment",
          basis: "ask:payment_money",
        }
      : undefined,
    stale: false,
    escalate: false,
    preview: true,
  };
}

/** api question profile */
export function questionsView() {
  return { name: "e2e", fingerprint: "e2e", questions: [] };
}

/** store.Summary + internal/search.Hit */
export function searchHit(id: number) {
  const invoice = id === msg.invoice;
  return {
    row_id: id,
    source_id: "src-e2e",
    store_id: "store-e2e",
    id: "msg-" + id,
    subject: invoice ? "Synthetic invoice" : "Synthetic budget",
    sender_name: invoice ? "Demo Sender" : "Ada Lovelace",
    sender_address: invoice ? "demo@example.test" : "ada@acme.test",
    instant: invoice ? AT : AT_LATER,
    role: "inbox",
    has_attachments: false,
    score: 0.92,
    text_rank: 1,
    snippet: invoice
      ? "Please review the synthetic invoice before Friday."
      : "The synthetic budget is ready for review.",
  };
}

/** internal/search.Result */
/** internal/store.MessageListRow */
export function messageListRow(id: number) {
  const invoice = id === msg.invoice;
  return {
    row_id: id,
    subject: invoice ? "Synthetic invoice" : "Synthetic budget",
    sender_name: invoice ? "Demo Sender" : "Ada Lovelace",
    sender_address: invoice ? "demo@example.test" : "ada@acme.test",
    sent_representing_address: invoice ? "demo@example.test" : "ada@acme.test",
    instant: invoice ? AT : AT_LATER,
    received_at: invoice ? AT : AT_LATER,
    sent_on: "",
    role: invoice ? "inbox" : "sent",
    folder_path: invoice ? "Inbox" : "Sent",
    store_key: "store-e2e",
    store_name: "Demo mailbox",
    is_read: !invoice,
    has_attachments: false,
    snippet: invoice
      ? "Please review the synthetic invoice before Friday."
      : "The synthetic budget is ready for review.",
    recipients: [],
  };
}

/** api.MessagesView */
export type MessagesPage = {
  messages: ReturnType<typeof messageListRow>[];
  total: number;
  counts: Record<string, number>;
  next_before: string;
  has_more: boolean;
};

/** GET /api/messages: one first page with a cursor, then the rest. */
export function messageListPage(
  role = "all",
  before = "",
  sender = "",
  unread = false,
): MessagesPage {
  const rows = [messageListRow(msg.invoice), messageListRow(msg.budget)];
  if (sender || unread) {
    // One exact address, or one @domain: case-insensitive, never a
    // substring and never a display name. The unread filter is one
    // independent predicate composed with the sender. The page and its
    // role counts describe the same filtered population.
    const wanted = sender.trim().toLowerCase();
    const matches = (address: string) => {
      const value = address.trim().toLowerCase();
      const domain = wanted.startsWith("@") ? wanted.slice(1) : wanted;
      if (wanted.startsWith("@") || !wanted.includes("@")) {
        return value.endsWith("@" + domain);
      }
      return value === wanted;
    };
    const selected = rows.filter(
      (row) =>
        (!sender || matches(row.sender_address)) &&
        (!unread || row.is_read === false),
    );
    const counts: Record<string, number> = {
      inbox: 0,
      sent: 0,
      drafts: 0,
      deleted: 0,
      junk: 0,
      archive: 0,
      other: 0,
    };
    for (const row of selected) counts[row.role] = (counts[row.role] || 0) + 1;
    const filtered =
      role === "all" ? selected : selected.filter((row) => row.role === role);
    return {
      messages: filtered,
      total: filtered.length,
      counts,
      next_before: "",
      has_more: false,
    };
  }
  const counts = {
    inbox: 1,
    sent: 1,
    drafts: 0,
    deleted: 0,
    junk: 0,
    archive: 0,
    other: 0,
  };
  if (role !== "all") {
    const filtered = rows.filter((row) => row.role === role);
    return {
      messages: filtered,
      total: filtered.length,
      counts,
      next_before: "",
      has_more: false,
    };
  }
  if (!before) {
    return {
      messages: [rows[0]],
      total: 2,
      counts,
      next_before: "cursor-1",
      has_more: true,
    };
  }
  return {
    messages: [rows[1]],
    total: 2,
    counts,
    next_before: "",
    has_more: false,
  };
}

/** Synthetic rows 1000–1149, used by the progressive first-paint tests. */
export function syntheticMailRow(id: number) {
  return {
    ...messageListRow(id),
    row_id: id,
    subject: `Synthetic message ${id}`,
    sender_name: `Synthetic sender ${id}`,
    sender_address: `sender${id}@example.test`,
    sent_representing_address: "",
    role: "inbox",
    folder_path: "Inbox",
    snippet: `Synthetic preview ${id}.`,
  };
}

/** A 100-row first page and a 50-row second page (total 150). */
export function progressiveMailPage(before = ""): MessagesPage {
  const counts = {
    inbox: 150,
    sent: 0,
    drafts: 0,
    deleted: 0,
    junk: 0,
    archive: 0,
    other: 0,
  };
  if (!before) {
    return {
      messages: Array.from({ length: 100 }, (_, index) =>
        syntheticMailRow(1000 + index),
      ),
      total: 150,
      counts,
      next_before: "cursor-100",
      has_more: true,
    };
  }
  return {
    messages: Array.from({ length: 50 }, (_, index) =>
      syntheticMailRow(1100 + index),
    ),
    total: 150,
    counts,
    next_before: "",
    has_more: false,
  };
}

/** A 100-row sender first page and a 50-row second page (total 150). */
export function progressiveSenderMailPage(before = ""): MessagesPage {
  const counts = {
    inbox: 150,
    sent: 0,
    drafts: 0,
    deleted: 0,
    junk: 0,
    archive: 0,
    other: 0,
  };
  const row = (id: number) => ({
    ...syntheticMailRow(id),
    sender_name: "Digest",
    sender_address: "digest@example.test",
  });
  if (!before) {
    return {
      messages: Array.from({ length: 100 }, (_, index) => row(2000 + index)),
      total: 150,
      counts,
      next_before: "cursor-sender-100",
      has_more: true,
    };
  }
  return {
    messages: Array.from({ length: 50 }, (_, index) => row(2100 + index)),
    total: 150,
    counts,
    next_before: "",
    has_more: false,
  };
}

/** A 100-row unread sender first page and a 50-row second page (total 150). */
export function progressiveUnreadMailPage(before = ""): MessagesPage {
  const counts = {
    inbox: 150,
    sent: 0,
    drafts: 0,
    deleted: 0,
    junk: 0,
    archive: 0,
    other: 0,
  };
  const row = (id: number) => ({
    ...syntheticMailRow(id),
    sender_name: "Third",
    sender_address: "third@example.test",
    is_read: false,
  });
  if (!before) {
    return {
      messages: Array.from({ length: 100 }, (_, index) => row(3000 + index)),
      total: 150,
      counts,
      next_before: "cursor-unread-100",
      has_more: true,
    };
  }
  return {
    messages: Array.from({ length: 50 }, (_, index) => row(3100 + index)),
    total: 150,
    counts,
    next_before: "",
    has_more: false,
  };
}

/** GET /api/messages for a folder with no owner-visible mail. */
export function emptyMessageListPage(): MessagesPage {
  return {
    messages: [],
    total: 0,
    counts: {
      inbox: 0,
      sent: 0,
      drafts: 0,
      deleted: 0,
      junk: 0,
      archive: 0,
      other: 0,
    },
    next_before: "",
    has_more: false,
  };
}

export function searchView() {
  return {
    hits: [searchHit(msg.invoice)],
    total: 1,
    capped: false,
    text_matches: 1,
    text_matches_capped: false,
    terms: [{ term: "invoice", mode: "word" }],
    semantic: {
      used: false,
      reason: "index_warming",
      covered: 0,
      eligible: 2,
      space: "store:e2e",
    },
    notice: "",
    took: 42_000_000,
  };
}

/** api.CompanyCoverageWire */
export function companyCoverage() {
  return {
    contacts: 10,
    contacts_with_company: 8,
    declared_orgs: 2,
  };
}

/** api.CompanyRowWire */
export function companyRow(key: string, label: string, domain: string) {
  return {
    key,
    label,
    domains: [domain],
    people: 1,
    messages: 5,
    sent: 2,
    received: 3,
    last_at: AT,
  };
}

/** api.CompaniesView */
export function companiesView(
  companies = [
    companyRow("c:acme", "Acme", "acme.test"),
    companyRow("c:beta", "Beta", "beta.test"),
  ],
) {
  return {
    generated_at: AT,
    companies,
    coverage: companyCoverage(),
  };
}

/** api.CompanyProfileWire */
export function companyProfile(key: string, label: string, domain: string) {
  return {
    generated_at: AT,
    label,
    declared: true,
    domains: [domain],
    people: [
      {
        address: "ada@" + domain,
        name: key === "c:acme" ? "Ada Lovelace" : "Grace Hopper",
        department: "Engineering",
        job_title: key === "c:acme" ? "CTO" : "Admiral",
        phone: "+1 555 010 0100",
        declared: true,
        messages: 3,
        sent: 1,
        received: 2,
        last_at: AT,
      },
      {
        address: "support@" + domain,
        name: label + " Support",
        declared: true,
        messages: 2,
        sent: 0,
        received: 2,
        last_at: AT,
      },
    ],
    messages: 5,
    sent: 2,
    received: 3,
    threads: 2,
    first_at: "2026-08-01T09:00:00Z",
    last_at: AT,
    last_inbound_at: AT,
    last_outbound_at: "2026-10-24T10:00:00Z",
    last_90_days: { messages: 5, threads: 2, active_days: 3 },
    months: [
      { month: "2026-08", messages: 2, sent: 1, received: 1 },
      { month: "2026-09", messages: 1, sent: 0, received: 1 },
      { month: "2026-10", messages: 2, sent: 1, received: 1 },
    ],
    recent: [
      {
        message_id: msg.invoice,
        subject: "Synthetic invoice",
        sender_name: "Ada Lovelace",
        sender_address: "ada@" + domain,
        instant: AT,
        sent: false,
        has_attachments: true,
      },
    ],
    recent_total: 1,
    open_threads: [
      {
        subject: "Synthetic renewal",
        last_at: AT_LATER,
        state: "awaiting_owner",
        reason: "inbound_last",
        actor: "Ada Lovelace",
        message_id: msg.budget,
        messages: 3,
      },
    ],
    thread_states: {
      awaiting_owner: 1,
      awaiting_other: 0,
      answered_by_owner: 0,
      answered_by_other: 0,
      no_action: 0,
      unknown: 0,
    },
    events: [
      {
        subject: "Synthetic review",
        location: "Room A",
        organizer_name: "Ada Lovelace",
        organizer_address: "ada@" + domain,
        start: "2026-11-03T10:00:00Z",
        end: "2026-11-03T11:00:00Z",
        all_day: false,
        response_status: 3,
        is_recurring: false,
        online_url: "https://meet.example.test/review",
        attendee_count: 3,
      },
    ],
    events_total: 1,
    deadlines: [
      {
        message_id: msg.invoice,
        subject: "Synthetic invoice",
        sender_name: "Ada Lovelace",
        due: "2026-11-10T09:00:00Z",
      },
    ],
    deadlines_total: 1,
    people_contacted: 2,
    people_known: 2,
  };
}

/** api.AgendaWire */
export function agendaView(overrides: Record<string, unknown> = {}) {
  return {
    horizon_hours: 3,
    items: [
      {
        kind: "event",
        at: "2026-11-03T10:00:00Z",
        title: "Synthetic review",
        detail: "Project kickoff",
        location: "Room A",
        all_day: false,
        reasons: ["meeting"],
      },
      {
        kind: "reply",
        at: "2026-11-03T09:00:00Z",
        last_at: AT,
        title: "Synthetic renewal",
        message_id: msg.budget,
        linked_event_title: "Synthetic review",
        reasons: ["waiting_since", "meeting_with_them"],
      },
      {
        kind: "deadline",
        at: "2026-11-03T11:30:00Z",
        title: "Synthetic invoice",
        message_id: msg.invoice,
        reasons: ["due"],
      },
    ],
    events: 1,
    obligations: 1,
    backlog: 2,
    deadlines: 1,
    ...overrides,
  };
}

/** qa.AskView */
export function askView(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    conversation_id: conversation,
    question: "Summarize the synthetic invoice thread",
    answer:
      "The sender asks you to review the synthetic invoice before Friday. No decision has been recorded.",
    route: "auto",
    mode: "generated",
    intent: "freeform",
    counts: {},
    citations: [
      {
        message_id: msg.invoice,
        subject: "Synthetic invoice",
        sender_name: "Demo Sender",
        received_at: AT,
        reason: "reply",
      },
    ],
    provider: "stub-provider",
    model: "stub-model",
    took_ms: 12,
    preview: true,
    created_at: AT,
    ...overrides,
  };
}

/** qa.AskView in clarify mode with structured person candidates. */
export function clarifyView() {
  const options = [
    "Summarize the last exchange with Nadia Tremblay — Nadia Tremblay <nadia@atelier-nord.test>",
    "Summarize the last exchange with Nadia Smith — Nadia Smith <nadia.smith@beta.test>",
  ];
  return askView({
    question: "Summarize the last exchange with Nadia",
    answer: "",
    mode: "clarify",
    clarify: {
      question: "Which person do you mean?",
      options,
      candidates: [
        {
          kind: "person",
          label: "Nadia Tremblay",
          address: "nadia@atelier-nord.test",
          messages: 10,
          last_at: AT,
          question: options[0],
        },
        {
          kind: "person",
          label: "Nadia Smith",
          address: "nadia.smith@beta.test",
          messages: 3,
          last_at: AT_LATER,
          question: options[1],
        },
      ],
    },
  });
}

/** qa.AskView computed contacts view filtered by one topic. */
export function topicContactsView() {
  return askView({
    id: 12,
    question: "Who mentioned artificial intelligence?",
    answer: "",
    mode: "computed",
    intent: "contacts",
    plan: { kind: "contacts", text: "IA intelligence artificielle" },
    result: {
      rows: [
        {
          key: "p:nadia-bergeron",
          label: "Nadia Tremblay",
          address: "nadia@atelier-nord.test",
          messages: 6,
          last_at: AT,
        },
        {
          key: "p:nadia-smith",
          label: "Nadia Smith",
          address: "nadia.smith@beta.test",
          messages: 3,
          last_at: AT_LATER,
        },
      ],
      total: 9,
      capped: true,
    },
    counts: { contacts: 2, messages: 9 },
    citations: [],
    provider: "",
    model: "",
  });
}

/** api.LearningView with a live running embeddings stage. */
export function learningView(overrides: Record<string, unknown> = {}) {
  return {
    open: false,
    preview: true,
    status: {
      state: "blocked",
      scope: "store:e2e",
      at: AT,
      stages: [
        {
          id: "mailboxes",
          state: "done",
          progress: { done: 1, total: 1, unit: "mailboxes" },
        },
        {
          id: "embeddings",
          state: "running",
          progress: {
            done: 13_696,
            total: 34_938,
            unit: "messages",
            embedded: 13_650,
            unchanged: 46,
            failed: 0,
          },
        },
      ],
    },
    ...overrides,
  };
}

/** internal/hub.Hub with the questions-stage certification counts. */
export function hubCertificationView(certified: number, total: number) {
  return {
    generated_at: AT,
    gate: {
      open: true,
      state: "done",
      labels: { manual: 0, judge: 0, needed: 0, review: 0 },
      questions: [],
      blockers: [],
      certified,
      total,
      labels_done: 0,
      labels_total: 0,
    },
    mailbox: { stores: 1, messages: 1 },
    recent_answers: [],
  };
}

export function labelsSummaryView() {
  return {
    manual: 0,
    judge: 0,
    needed: 0,
    review: 0,
    questions: [],
  };
}

// statusView is the boot status every spec mocks; a test overrides only the
// field it asserts instead of restating the contract.
export function statusView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return { version: "e2e", time_zone: "UTC", data_dir: "/tmp/e2e", ...overrides };
}
