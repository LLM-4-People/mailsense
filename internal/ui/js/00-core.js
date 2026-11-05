  const I18N = window.MailsenseI18n;
  if (!I18N) throw new Error("Mailsense i18n runtime is missing");
  const {
    t, tOr, formatNumber, formatId, formatPercent, formatUsd, formatDuration, formatBytes,
    formatDate, formatClock, formatRelative, formatMonth, formatDay,
    localeCompare, compareText, parseTime, documentTitle, metaSeparator, metaLine,
  } = I18N;

  const TOKEN_KEY = "mailsense.token";
  const MAX_LOG_ENTRIES = 5000;
  const TRIAGE_PAGE_SIZE = 50;
  const ANSWER_PAGE_SIZE = 50;
  const PRIORITY_PAGE_SIZE = 50;
  const PRIORITY_HOME_SIZE = 6;
  // Detail rows warmed before a click. Every prefetched message renders and
  // ships its body, so this is one row, not the whole home list.
  const PRIORITY_PREFETCH_SIZE = 1;
  const CONVERSATIONS_PAGE_SIZE = 30;
  const COMPANIES_LIST_LIMIT = 100;
  const COMPANIES_MONTHS = 12;
  const COMPANIES_EVENT_DAYS = 30;
  // A mount prefetches the selected row and its next two neighbours; the
  // rest stay cold until they scroll into view or the owner clicks them.
  const COMPANY_PREFETCH_BATCH = 3;
  const MAX_ROUTE_CACHE = 24;
  const MAX_CONVERSATION_CACHE = 12;
  // A cached screen paints immediately. It is revalidated in the background
  // only after this window, so a menu click never pays a second full build.
  const ROUTE_FRESH_MS = 20000;
  // A finished job stays in the global strip briefly; a failure stays until
  // the owner dismisses it (jobLinger returns no expiry).
  const JOB_STRIP_LINGER_MS = 4000;
  // The boot strip polls the start-up contract. The deadline is longer than
  // the server's 90 s warm bound; a reachable server that is still warming
  // keeps the strip, an unreachable one stops it and lets the route error
  // surfaces speak instead of claiming readiness.
  const BOOT_POLL_INTERVAL_MS = 400;
  // After five seconds the strip is describing a real warm, not a fast start:
  // a two-second poll keeps the progress honest without 300 requests.
  const BOOT_POLL_SLOW_AFTER_MS = 5000;
  const BOOT_POLL_SLOW_INTERVAL_MS = 2000;
  const BOOT_POLL_DEADLINE_MS = 120000;
  const SCREEN_IDS = ["hub", "brief", "mail", "companies", "ask", "labels", "answers", "learning", "sources", "settings", "logs"];
  const NAV_GROUPS_KEY = "mailsense.nav.groups";
  const SETTINGS_ADVANCED_KEY = "mailsense.settings.advanced";
  const BRIEF_SINCE_KEY = "mailsense.brief.since";
  const BRIEF_SINCE_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

  // One TTL per read surface. Short enough to stay honest, long enough that
  // menu changes never wait on the loopback API.
  const CACHE_TTL = {
    learning: 12000,
    status: 30000,
    hub: 18000,
    brief: 15000,
    // The catalogue changes only on a profile or configuration write, which
    // invalidates it; the TTL only bounds a missed event.
    questions: 60000,
    mailboxes: 60000,
    priority: 15000,
    conversations: 15000,
    companies: 20000,
    agenda: 12000,
    triageSummary: 20000,
    triagePage: 15000,
    triageDetail: 30000,
    labelsSummary: 20000,
    labelsQueue: 20000,
    // The answers rows follow label writes, which the live events
    // invalidate; the TTL only bounds a missed event, so it must outlive the
    // background revalidation window or every repaint refetches.
    answers: ROUTE_FRESH_MS * 3,
    askHistory: 12000,
    message: 60000,
    thread: 15000,
    search: 20000,
    settings: 30000,
    providers: 30000,
    sidecar: 15000,
    jobs: 10000,
  };

  const state = {
    token: "",
    status: null,
    learning: null,
    gate: null,
    jobs: new Map(),
    hub: null,
    route: "hub",
    routeKey: "",
    renderSeq: 0,
    shuttingDown: false,
    questions: null,
    mailboxes: null,
    agendaHours: null,
    answersPage: null,
    answerDetailSeq: 0,
    settingsQuery: "",
    settingsTab: "general",
    settingsData: null,
    settingsAll: null,
    update: null,
    whatsNewPrompted: "",
    labelAnswers: {},
    labelExtras: [],
    labelTaskSeq: 0,
    labelKeyHandler: null,
    labelObserver: null,
    labelChipObserver: null,
    companyObserver: null,
    logEntries: new Map(),
    logPending: [],
    logPaused: false,
    logExpanded: new Set(),
    logFocusSeq: 0,
    logHealth: null,
    logOldest: 0,
    logLast: 0,
    logTruncated: false,
    logDropped: [],
    logFilters: { level: "", component: "", job: "" },
    triageRouted: null,
    triagePreview: false,
    triagePage: null,
    triageSummary: null, triageSummaryError: null,
    priorityPages: new Map(),
    priorityTotals: new Map(),
    allMail: { role: "", sender: "", unread: false, rows: [], total: 0, counts: null, next: "", hasMore: false, capped: false, seen: new Set(), loaded: false, promise: null, loading: false, morePromise: null },
    conversationPages: new Map(),
    conversationLoads: new Map(),
    companiesQuery: "",
    companiesCount: 0,
    companyKeys: [],
    companyDetailKey: "",
    companyDetailPendingKey: "",
    companyDetailFailedKey: "",
    askHistory: null,
    askViews: new Map(),
    askFocusPending: false,
    askPrefill: "",
    modalOpen: false,
    modalCloseFn: null,
    languageSetting: "auto",
    systemLanguage: "",
    systemLocale: "",
    offerFrench: false,
    languageReady: false,
    boot: null,
    bootReady: false,
    bootWatch: false,
    bootTimer: null,
    bootStartedAt: 0,
    prefetchStarted: false,
    connectionKind: "",
  };

  // The label questions the profile names, with the one UI-owned help key
  // base per question. Model-facing names and instructions stay English;
  // the titles, hints and scale endpoints are interface wording.
  const QUESTION_HELP = {
    category: { help: "category" },
    topic: { help: "topic" },
    reply_needed: { help: "reply_needed" },
    urgency: { help: "urgency", low: true, high: true },
    suspicious: { help: "suspicious" },
    is_question: { help: "is_question" },
    call_to_action: { help: "call_to_action" },
    decision_needed: { help: "decision_needed" },
    asks_for: { help: "asks_for" },
    deadline: { help: "deadline", low: true, high: true },
    relationship: { help: "relationship" },
    effort: { help: "effort", low: true, high: true },
    sentiment: { help: "sentiment", low: true, high: true },
    stance: { help: "stance" },
    info_sufficiency: { help: "info_sufficiency" },
    request_count: { help: "request_count", low: true, high: true },
    has_signature: { help: "has_signature" },
  };

  // The one curated owner of the everyday settings. Simple view is not a
  // group filter: each key is chosen because a normal owner can understand
  // and change it without touching secrets, files or runtime internals.
  const SETTINGS_SIMPLE_GROUPS = [
    { id: "language", keys: ["ui.language", "time.zone"] },
    { id: "answers", keys: ["qa.enabled", "qa.max_messages"] },
    { id: "mail", keys: ["sync.details.enabled", "triage.folder_roles", "threads.folder_roles"] },
    { id: "learning", keys: ["learning.test_mode"] },
  ];
  // One ordered list feeds both the default projection query and the simple
  // view, so the request and its rendered rows cannot drift apart.
  const SETTINGS_SIMPLE_KEY_LIST = SETTINGS_SIMPLE_GROUPS.flatMap((group) => group.keys);
  const SETTINGS_SIMPLE_KEYS = new Set(SETTINGS_SIMPLE_KEY_LIST);

  const apiCache = new Map();
  // One session can read every message, thread and search it visits; an
  // unbounded cache would grow with the mailbox. The entry count is the
  // secondary bound: a single MessageView can carry a multi-megabyte body,
  // so the byte budget below is what keeps the cache's memory honest.
  const MAX_API_CACHE = 240;
  const MAX_API_CACHE_BYTES = 24 * 1024 * 1024;
  // A response above this bound is answered but not cached: keeping one
  // maximum-size body would evict everything useful beside it.
  const MAX_API_CACHE_ENTRY_BYTES = 8 * 1024 * 1024;
  // The compiled sender sheet is presentation data. A pathological sheet is
  // left out of the cache instead of taking a slot beside its message body.
  const MAX_CACHED_BODY_CSS = 64 * 1024;
  let apiCacheBytes = 0;
  const apiInflight = new Map();
  const routeCache = new Map();
  const companyPrefetchInflight = new Set();
  // The company explorer prefetches the profiles it can see. This buffer is
  // a UI warm-up, not an API cache: it never satisfies a fresh read, but a
  // click paints from it while the list stays mounted. It is bounded and
  // dropped on a full or explicit company resync.
  const companyProfileWarm = new Map();
  let companyDetailSeq = 0;
  const navPrefetchLinks = new Set();
  const routePrefetchInflight = new Set();
  let prefetchActive = 0;
  let prefetchErrorSuppressUntil = 0;
  let prefetchQueue = Promise.resolve();
  let routeRenderInFlight = 0;
  let apiEpoch = 0;

  const $ = (id) => document.getElementById(id);
  const dom = {
    app: $("app"),
    main: $("main"),
    tokenScreen: $("token-screen"),
    tokenTitle: $("token-title"),
    tokenMessage: $("token-message"),
    screenTitle: $("screen-title"),
    screenEyebrow: $("screen-eyebrow"),
    topbarMeta: $("topbar-meta"),
    topbarUpdate: $("topbar-update"),
    globalSearch: $("global-search"),
    globalSearchInput: $("global-search-input"),
    globalSearchClear: $("global-search-clear"),
    globalSearchResults: $("global-search-results"),
    jobStrip: $("job-strip"),
    bootStrip: $("boot-strip"),
    jobLive: $("job-live"),
    connection: $("connection"),
    connectionText: $("connection-text"),
    metaVersion: $("meta-version"),
    metaLaunch: $("meta-launch"),
    metaData: $("meta-data"),
    shutdown: $("shutdown-button"),
    navLabelsBadge: $("nav-labels-badge"),
    toasts: $("toasts"),
    modal: $("modal"),
    modalTitle: $("modal-title"),
    modalEyebrow: $("modal-eyebrow"),
    modalBody: $("modal-body"),
    modalActions: $("modal-actions"),
    modalForm: $("modal-form"),
  };

  function screenTitle(name) {
    return tOr("screen." + name + ".title", name);
  }

  function screenEyebrow(name) {
    return t("screen." + name + ".eyebrow");
  }

  function blockerTitle(blocker) {
    return t("blocker." + blocker.code + ".title", null, humanCode(blocker.code));
  }

  function blockerDetail(blocker) {
    const key = "blocker." + blocker.code + ".detail";
    if (I18N.has(key)) return t(key);
    // An unrecognized blocker still gets prose in French; the server's
    // English diagnostic is disclosed by blockerDetailNode.
    if (I18N.snapshot().language === "fr") return t("blocker.fallback_detail");
    return blocker.detail || t("blocker.fallback_detail");
  }

  function actionLabel(action) {
    return t("action." + action.id, null, humanCode(action.id));
  }

  function stageTitle(stageId) {
    return tOr("learning.stage." + stageId + ".title", t("stage." + stageId, null, humanCode(stageId)));
  }

  function stageHint(stageId) {
    return tOr("learning.stage." + stageId + ".hint", "");
  }

  function learningStateText(value) {
    const code = String(value || "");
    return tOr("learning.state." + code, statusLabel(code));
  }

  function learningTitle(value) {
    const key = "learning.title." + value;
    return I18N.has(key) ? t(key) : learningStateText(value);
  }

  function learningMeaning(value, params) {
    const key = "learning.meaning." + value;
    return I18N.has(key) ? t(key, params) : t("learning.callout.closed");
  }

  function reasonText(code) {
    return t("reason." + code, null, String(code || ""));
  }

  function statusLabel(value) {
    const code = String(value || "");
    return t("state." + code, null, humanCode(code));
  }

  // jobStageLabel is a job's stage key in the reader's language (a
  // humanized key when the catalog has no entry: a new stage is never
  // blank and never shows a raw catalog id).
  function jobStageLabel(stage) {
    const key = String(stage || "");
    if (!key) return "";
    return tOr("jobs.stage." + key, humanCode(key));
  }

  // ---------------------------------------------------------------- helpers

  function icon(name, extra = "") {
    const template = document.getElementById("icon-template");
    const svg = template.content.firstElementChild.cloneNode(true);
    svg.setAttribute("class", ("icon " + extra).trim());
    const use = svg.querySelector("use");
    use.setAttribute("href", "#i-" + name);
    return svg;
  }

  function h(tag, attrs, ...children) {
    const node = document.createElement(tag);
    if (attrs) {
      for (const [key, value] of Object.entries(attrs)) {
        if (value === undefined || value === null || value === false) continue;
        if (key === "class") node.className = value;
        else if (key === "text") node.textContent = value == null ? "" : String(value);
        else if (key === "dataset") {
          for (const [k, v] of Object.entries(value)) node.dataset[k] = v;
        } else if (key.startsWith("on") && typeof value === "function") {
          node.addEventListener(key.slice(2).toLowerCase(), value);
        } else if (key === "value" || key === "checked" || key === "disabled" ||
          key === "selected" || key === "hidden" || key === "multiple" ||
          key === "required" || key === "readOnly" || key === "tabIndex") {
          node[key] = value;
        } else if (key === "style") {
          // The CSP is style-src 'self' with no inline allowance. A style
          // attribute set with setAttribute is blocked; CSSOM assignment is
          // not, so the same declarations apply without loosening the policy.
          node.style.cssText = String(value);
        } else {
          node.setAttribute(key, value === true ? "" : String(value));
        }
      }
    }
    appendChildren(node, children);
    return node;
  }

  function appendChildren(node, children) {
    for (const child of children) {
      if (child === null || child === undefined || child === false) continue;
      if (Array.isArray(child)) appendChildren(node, child);
      else if (child instanceof Node) node.append(child);
      else node.append(document.createTextNode(String(child)));
    }
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function text(value) {
    return document.createTextNode(value == null ? "" : String(value));
  }

  function fragment(...children) {
    const node = document.createDocumentFragment();
    appendChildren(node, children);
    return node;
  }

  function badge(value, kind = "", title = "") {
    return h("span", { class: "badge " + kind, text: value, title });
  }

  function pill(value, kind = "", className = "") {
    return h("span", { class: ("pill " + kind + " " + className).trim(), text: value });
  }

  let fieldSeq = 0;

  function field(label, control, hint, options = {}) {
    // A label has to name its control, not merely sit above it: without a
    // for/id pair neither a screen reader nor Playwright's getByLabel can
    // associate them. The hint is part of the control's name, so it is
    // referenced by aria-describedby instead of floating beside it.
    if (!control.id) control.id = "field-" + String(++fieldSeq);
    let hintNode = null;
    if (hint) {
      const hintId = control.id + "-hint";
      const describedBy = control.getAttribute("aria-describedby");
      control.setAttribute("aria-describedby", describedBy ? describedBy + " " + hintId : hintId);
      hintNode = h("span", { class: "field-hint", id: hintId, text: hint });
    }
    const labelNode = h("label", { for: control.id, text: label });
    // A settings row already shows its description as the visible title. The
    // control still needs its accessible name, so the label is announced but
    // not painted a second time.
    if (options.srOnly) labelNode.className = "sr-only";
    return h("div", { class: "field" }, labelNode, control, hintNode);
  }

  function button(label, onClick, opts = {}) {
    const node = h("button", {
      type: "button",
      class: ("button " + (opts.kind || "") + " " + (opts.small ? "small" : "")).trim(),
      title: opts.title || "",
      disabled: !!opts.disabled,
      "aria-pressed": opts.pressed === undefined ? null : String(opts.pressed),
    }, opts.icon ? icon(opts.icon, opts.small ? "small" : "") : null, h("span", { text: label }));
    if (onClick && !opts.disabled) node.addEventListener("click", onClick);
    return node;
  }

  function iconButton(name, label, onClick, opts = {}) {
    const node = h("button", {
      type: "button",
      class: "icon-button " + (opts.kind || ""),
      "aria-label": label,
      title: label,
    }, icon(name, opts.small ? "small" : ""));
    if (onClick) node.addEventListener("click", onClick);
    return node;
  }

  // link is the anchor twin of button: it must honour the same options, or a
  // site that asks for a small quiet link silently gets a full-size primary
  // one (34 call sites did). A prose link inside a sentence sets bare: it
  // keeps its own class and the surrounding text's look instead of becoming
  // a button.
  function link(label, href, opts = {}) {
    return h("a", {
      class: ((opts.bare ? "" : "button ") + (opts.kind || "") + " " + (opts.small ? "small" : "") + " " + (opts.class || "")).trim(),
      href,
      title: opts.title || "",
      "aria-label": opts.aria || null,
    }, opts.icon && !opts.trailingIcon ? icon(opts.icon, opts.small ? "small" : "") : null,
    h("span", { text: label }),
    opts.icon && opts.trailingIcon ? icon(opts.icon, opts.small ? "small" : "") : null);
  }


  function humanCode(code) {
    if (!code) return t("common.unknown");
    return String(code).replace(/_/g, " ");
  }

  // typingTarget reports whether a key event belongs to a text field. The
  // shell shortcuts stay out of the way while the owner is typing, in the
  // top document and in a same-origin message frame alike.
  function typingTarget(target) {
    if (!target || typeof target.closest !== "function") return false;
    if (target.isContentEditable) return true;
    return !!target.closest("input, textarea, select, [contenteditable='true']");
  }

  // finiteNumber accepts only a finite numeric value; an absent, empty or
  // malformed value stays absent instead of becoming zero. The triage facets
  // and the provider form share this one rule.
  // finite-number:start
  function finiteNumber(value) {
    if (value === null || value === undefined || value === "") return null;
    const number = Number(value);
    return Number.isFinite(number) ? number : null;
  }
  // finite-number:end

  function safeDecode(value) {
    try { return decodeURIComponent(value); } catch { return value; }
  }

  function joinPath(base, rel) {
    if (!rel) return base || "";
    if (/^([A-Za-z]:[\\/]|\\\\|\/)/.test(rel)) return rel;
    if (!base) return rel;
    const sep = base.includes("\\") && !base.includes("/") ? "\\" : "/";
    return base.replace(/[\\/]+$/, "") + sep + rel;
  }

  function jsonValue(value) {
    if (value === undefined || value === null) return t("common.dash");
    if (typeof value === "string") return value;
    if (typeof value === "boolean") return value ? "true" : "false";
    try { return JSON.stringify(value); } catch { return String(value); }
  }

  function findQuestion(name) {
    const list = state.questions && state.questions.questions ? state.questions.questions : [];
    return list.find((q) => q.name === name) || null;
  }

  function questionMap(profile) {
    const map = new Map();
    const list = profile && Array.isArray(profile.questions) ? profile.questions : [];
    for (const q of list) map.set(q.name, q);
    return map;
  }

  function orderedNames(values, profile) {
    const names = [];
    const list = profile && Array.isArray(profile.questions) ? profile.questions : [];
    for (const q of list) {
      if (values && Object.prototype.hasOwnProperty.call(values, q.name)) names.push(q.name);
    }
    for (const name of Object.keys(values || {})) {
      if (!names.includes(name)) names.push(name);
    }
    return names;
  }

  function allBlockers(learning) {
    const out = [];
    const stages = learning && learning.status && Array.isArray(learning.status.stages) ? learning.status.stages : [];
    for (const stage of stages) {
      const blockers = Array.isArray(stage.blockers) ? stage.blockers : [];
      for (const blocker of blockers) out.push({ stage: stage.id, blocker });
    }
    return out;
  }

  function stageBlockers(learning, id) {
    return allBlockers(learning).filter((entry) => entry.stage === id);
  }

  function blockerDetailNode(blocker) {
    return h("p", { class: "blocker-detail", text: blockerDetail(blocker) });
  }

  // technicalRows is the one disclosure for server-facing identifiers and
  // diagnostics. The friendly heading, detail and actions stay outside it.
  function technicalRows(rows) {
    const present = rows.filter((row) => row && row[1] !== undefined && row[1] !== null && String(row[1]).trim() !== "");
    if (!present.length) return null;
    return h("details", { class: "blocker-technical" },
      h("summary", { text: t("common.technical_details") }),
      h("dl", { class: "technical-list" }, present.flatMap(([label, value, lang]) => [
        h("dt", { text: label }),
        h("dd", lang ? { lang } : null, { text: String(value) }),
      ])));
  }

  function blockerTechnicalNode(blocker, stageId) {
    const primary = blockerDetail(blocker);
    const diagnostic = blocker.detail && blocker.detail !== primary ? blocker.detail : "";
    return technicalRows([
      [t("common.code"), blocker.code || ""],
      [t("learning.technical_stage"), stageId || ""],
      [t("learning.technical_subject"), blocker.subject || "", "en"],
      [t("common.diagnostic"), diagnostic, "en"],
    ]);
  }

  // measureUnit translates a learning measure unit. The server owns the
  // stable English unit; the catalog owns its wording, and an unknown unit
  // stays as the server sent it (diagnostic data, never hidden).
  function measureUnit(unit, count) {
    const unitText = String(unit || "").trim();
    if (!unitText) return "";
    const key = "measure.unit." + unitText.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_|_$/g, "");
    return tOr(key, unitText, { count: Number(count) || 0 });
  }

  // percent:start
  // percentOf is the one rounded percentage of a part over a total: a
  // negative or unknown total is 0, and nothing can exceed 100.
  function percentOf(part, total) {
    const whole = Number(total) || 0;
    if (whole <= 0) return 0;
    const value = (Number(part) || 0) / whole;
    return Math.min(100, Math.round(value * 100));
  }
  // percent:end


  // blockerSubjectText is the one label of a blocker's question: the friendly
  // title when the gate named a known question, the raw name otherwise. A
  // surface that paints the subject reads it here, so the rail cannot show an
  // internal id where the card shows a title.
  function blockerSubjectText(blocker) {
    const name = blocker && blocker.subject ? String(blocker.subject) : "";
    if (!name) return "";
    return questionHelp(name) ? questionTitle(name) : name;
  }

  function blockerNode(entry) {
    const blocker = entry.blocker;
    const actions = Array.isArray(blocker.actions) ? blocker.actions : [];
    const subjectText = blockerSubjectText(blocker);
    const subject = subjectText ? h("span", { class: "blocker-subject", text: subjectText }) : null;
    const buttons = actions.map((action) => button(
      actionLabel(action),
      (event) => executeAction(action, { blocker, stage: entry.stage }, event.currentTarget),
      { small: true, kind: action.id.startsWith("run_") ? "primary" : "" }
    ));
    const measure = measureNode(blocker.measure);
    return h("div", { class: "blocker" },
      h("div", { class: "blocker-head" },
        h("strong", { text: blockerTitle(blocker) }),
        subject),
      blockerDetailNode(blocker),
      measure,
      buttons.length ? h("div", { class: "blocker-actions" }, buttons) : null,
      blockerTechnicalNode(blocker, entry.stage));
  }

  // section-head:start
  // sectionHead is the one title/sub/actions row. A screen that needs a
  // different heading level, id or layout class says so instead of
  // re-spelling the markup (six places did).
  function sectionHead(title, opts = {}) {
    return h("div", { class: ("section-head " + (opts.className || "")).trim() },
      h("div", null,
        h(opts.level === "h3" ? "h3" : "h2", opts.id ? { id: opts.id, text: title } : { text: title }),
        opts.sub ? h("p", { class: "section-sub", text: opts.sub }) : null),
      opts.actions ? h("div", { class: ("section-actions " + (opts.actionsClass || "")).trim() }, opts.actions) : null);
  }
  // section-head:end

  function section(title, sub, actions, ...body) {
    return h("section", { class: "section" }, sectionHead(title, { sub, actions }), body);
  }


  function kv(rows) {
    const list = h("dl", { class: "kv" });
    for (const row of rows) {
      if (!row) continue;
      list.append(h("dt", { text: row[0] }));
      const dd = h("dd");
      if (row[1] instanceof Node) dd.append(row[1]);
      else dd.textContent = row[1] == null ? t("common.dash") : String(row[1]);
      list.append(dd);
    }
    return list;
  }

  function initials(value) {
    const words = String(value || "").trim().split(/\s+/).filter(Boolean);
    if (!words.length) return "?";
    if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
    return (words[0][0] + words[words.length - 1][0]).toUpperCase();
  }

  // reasonKind is the one severity of a priority reason: security outranks
  // everything, a deadline and urgency are urgent, a needed reply or decision
  // is attention. The badge and the row class are two presentations of this
  // one classification (they disagreed on deadline: warn vs urgent).
  function reasonKind(code) {
    if (code === "security") return "danger";
    if (code === "urgent" || code === "deadline") return "urgent";
    if (code === "reply" || code === "decision") return "attention";
    return "";
  }

  function reasonBadge(code) {
    const kind = reasonKind(code);
    return badge(reasonText(code), kind === "urgent" ? "warn" : kind || "plain");
  }

  function askModeLabel(mode) {
    return t("ask.mode." + (mode || "retrieval"), null, humanCode(mode));
  }

  function mailHref(id, mode = "important", extra = {}) {
    const params = Object.assign({ mode }, extra);
    if (id) params.id = String(id);
    return routeHash("mail", params);
  }

  // A citation may be a sent or archived message, which Important does not
  // list: open the mode that holds every folder instead of a list without
  // the row.
  function citationHref(citation) {
    return mailHref(citation && citation.message_id, "all");
  }

  function statusKind(value) {
    const v = String(value || "");
    return v === "done" || v === "succeeded" || v === "open" || v === "learned" ? "done"
      : v === "blocked" || v === "failed" ? "blocked"
        : v === "running" || v === "learning" ? "running"
          : "pending";
  }

  function statusPill(value) {
    return pill(statusLabel(value), statusKind(value));
  }

  function gatePill(open) {
    return pill(open ? t("learning.gate_open") : t("learning.gate_closed"), open ? "open" : "closed");
  }

  // The test preview is deliberately never the open/ok style: it unlocks
  // the automatic answer screens without claiming the gate or accuracy.
  function previewPill() {
    return pill(t("preview.pill"), "preview");
  }

  function previewBanner(detail, actions, compact = false) {
    return h("section", { class: "preview-banner" + (compact ? " compact" : ""), role: "status" },
      icon("alert"),
      h("div", { class: "preview-banner-body" },
        h("strong", { text: compact ? t("preview.compact") : t("preview.banner") }),
        detail ? h("p", { text: detail }) : null,
        actions && actions.length ? h("div", { class: "button-row" }, actions) : null));
  }

  // The route pill counts certification; the preview always wins.
  function routePill(routed, preview, certification) {
    if (!routed) return pill(t("triage.paid_decider"), "pending");
    if (preview) return pill(t("triage.uncertified_route"), "preview");
    if (certification && certification.certified >= certification.total) {
      return pill(t("triage.certified_route"), "accent");
    }
    if (certification) {
      return pill(t("triage.partial_route", {
        certified: formatNumber(certification.certified),
        total: formatNumber(certification.total),
      }), "warn");
    }
    return pill(t("triage.route_unknown"), "pending");
  }

  function answerSourceLabel(source) {
    if (source === "manual") return t("answers.source.you");
    if (source === "judge") return t("answers.source.judge");
    return t("common.unknown");
  }

  function questionRemaining(q) {
    return Math.max((Number(q.needed) || 0) - (Number(q.manual) || 0), 0);
  }

  function questionHelp(name) {
    return QUESTION_HELP[name] || null;
  }

  function questionTitle(name) {
    const help = questionHelp(name);
    return help ? t("labels.q." + help.help + ".title") : humanCode(name);
  }

  function questionHint(name) {
    const help = questionHelp(name);
    return help ? t("labels.q." + help.help + ".hint") : "";
  }

  function questionScaleEndpoint(name, end) {
    const help = questionHelp(name);
    return help && help[end] ? t("labels.q." + help.help + "." + end) : "";
  }

  function questionTypeLabel(type) {
    const key = "labels.type." + String(type || "");
    return I18N.has(key) ? t(key) : humanCode(type);
  }

  function optionLabel(questionName, optionName) {
    return tOr("labels.option." + questionName + "." + optionName, String(optionName));
  }

  function levelText(level) {
    return t("labels.answer.level_value", { level });
  }

  function questionKey(value) {
    return String(value == null ? "" : value).trim().toLocaleLowerCase();
  }

  function uniqueByQuestion(items, valueOf) {
    const seen = new Map();
    for (const item of Array.isArray(items) ? items : []) {
      const value = valueOf ? valueOf(item) : item && item.question;
      const key = questionKey(value);
      if (!key) continue;
      const found = seen.get(key);
      if (found) found.count += 1;
      else seen.set(key, { item, count: 1 });
    }
    return [...seen.values()];
  }

  // topNeedQuestion is the review target: the question with the most manual
  // answers left to store, then the most suggestions to review, then the
  // profile's first question.
  function topNeedQuestion(questions) {
    const list = Array.isArray(questions) ? questions : [];
    let best = null;
    let bestRemaining = 0;
    for (const q of list) {
      const remaining = questionRemaining(q);
      if (remaining > bestRemaining) { best = q; bestRemaining = remaining; }
    }
    if (best) return best;
    let bestReview = 0;
    for (const q of list) {
      const review = Number(q.review) || 0;
      if (review > bestReview) { best = q; bestReview = review; }
    }
    return best || list[0] || null;
  }

  function answerQuestion(item) {
    return findQuestion(item.question) || { name: item.question, type: item.type || "", levels: [] };
  }

  // answerValueText is the one readable rendering of a stored label value:
  // the option name, Yes/No, or the level index with its taxonomy text.
  // Noul labels are stored as a number in [0,1] (true/false/1/0 parse to it)
  // and score labels as a number; the review flow exposes the same decoded
  // primitives, so Answers reuses this mapping.
  function answerValueText(question, value) {
    if (value === null || value === undefined) return t("common.dash");
    const type = question && question.type ? question.type : "";
    if (type === "choice") return optionLabel(question && question.name ? question.name : "", String(value));
    if (type === "noul") {
      if (value === true || value === "true" || value === "yes" || value === 1 || value === "1") return t("labels.answer.yes");
      if (value === false || value === "false" || value === "no" || value === 0 || value === "0") return t("labels.answer.no");
      const number = Number(value);
      if (Number.isFinite(number)) return number >= 0.5 ? t("labels.answer.yes") : t("labels.answer.no");
      return jsonValue(value);
    }
    if (type === "score") {
      const number = Number(value);
      if (!Number.isFinite(number)) return jsonValue(value);
      const level = Math.round(number);
      const key = "detail.summary." + (question && question.name ? question.name : "") + "." + level;
      return I18N.has(key) ? t(key) : levelText(level);
    }
    return jsonValue(value);
  }

  // ---------------------------------------------------------------- sortable tables

  // sortable:start
  //
  // One sortable-table contract for every dynamic data table.
  //
  // Column: {
  //   key, label, type: "auto"|"text"|"number"|"date"|"email"|"phone",
  //   align, sortable, defaultDirection,
  //   value(row, index), render(row, index)
  // }
  //
  // The pure half (parsers, inference, comparison, order) never touches the
  // DOM. The builder creates rows once and only moves the existing <tr>
  // nodes, so listeners, focus and checkbox state survive every sort.

  const SORTABLE_TYPES = ["auto", "text", "number", "date", "email", "phone"];
  const SORTABLE_SAMPLE_LIMIT = 40;
  const SORTABLE_DAY_MS = 24 * 60 * 60 * 1000;
  const SORTABLE_WEEKDAY_INDEX = {
    monday: 0, tuesday: 1, wednesday: 2, thursday: 3,
    friday: 4, saturday: 5, sunday: 6,
  };

  function sortableNormalizeDirection(value) {
    if (value === "asc" || value === "ascending") return "asc";
    if (value === "desc" || value === "descending") return "desc";
    return "";
  }

  function sortableIsEmpty(value) {
    return value === null || value === undefined ||
      (typeof value === "string" && value.trim() === "");
  }

  function sortableNormalizeColumn(column) {
    const spec = column && typeof column === "object" ? column : {};
    const key = String(spec.key == null ? "" : spec.key);
    const type = SORTABLE_TYPES.includes(spec.type) ? spec.type : "auto";
    const align = spec.align === "num" || spec.align === "right" || spec.align === "end" ? "num" : "";
    return {
      key,
      label: spec.label == null ? "" : String(spec.label),
      type,
      align,
      sortable: spec.sortable !== false,
      defaultDirection: sortableNormalizeDirection(spec.defaultDirection) === "desc" ? "desc" : "asc",
      value: typeof spec.value === "function"
        ? spec.value
        : (row) => (row == null ? "" : row[key]),
      render: typeof spec.render === "function" ? spec.render : null,
    };
  }

  // sortableNumberValue reads a raw number, or one human-formatted fallback
  // cell. Spacing and apostrophe grouping are removed; the last of a mixed
  // dot/comma pair is the decimal separator; a single separator with exactly
  // three trailing digits is grouping unless the integer part is "0".
  function sortableNumberValue(value) {
    if (typeof value === "number") return Number.isFinite(value) ? value : null;
    if (typeof value === "bigint") {
      const number = Number(value);
      return Number.isFinite(number) ? number : null;
    }
    if (typeof value !== "string") return null;
    let raw = value.trim();
    if (!raw || !/^[+\-\u2212]?[\d\s.,\u00a0\u202f\u2009\u2007'’]+$/.test(raw)) return null;
    let sign = 1;
    if (raw[0] === "-" || raw[0] === "\u2212") { sign = -1; raw = raw.slice(1); }
    else if (raw[0] === "+") raw = raw.slice(1);
    raw = raw.replace(/[\s\u00a0\u202f\u2009\u2007'’]/g, "");
    if (!raw || !/[0-9]/.test(raw)) return null;
    const dot = raw.lastIndexOf(".");
    const comma = raw.lastIndexOf(",");
    let normalized = "";
    if (dot >= 0 && comma >= 0) {
      const decimal = dot > comma ? "." : ",";
      const grouping = decimal === "." ? "," : ".";
      const at = raw.lastIndexOf(decimal);
      const whole = raw.slice(0, at).split(grouping).join("");
      const fraction = raw.slice(at + 1);
      if (!/^\d+$/.test(whole) || !/^\d+$/.test(fraction)) return null;
      normalized = whole + "." + fraction;
    } else {
      const separator = dot >= 0 ? "." : comma >= 0 ? "," : "";
      if (!separator) {
        if (!/^\d+$/.test(raw)) return null;
        normalized = raw;
      } else {
        const parts = raw.split(separator);
        if (parts.length > 2) {
          if (!parts[0] || !parts.every((part, index) => index === 0 ? /^\d+$/.test(part) : /^\d{3}$/.test(part))) return null;
          normalized = parts.join("");
        } else {
          const whole = parts[0];
          const fraction = parts[1];
          if (!/^\d+$/.test(fraction)) return null;
          if (!whole) normalized = "0." + fraction;
          else if (fraction.length === 3 && whole !== "0") normalized = whole + fraction;
          else normalized = whole + "." + fraction;
        }
      }
    }
    const number = Number(normalized);
    return Number.isFinite(number) ? sign * number : null;
  }

  function sortableCalendarInstant(year, month, day) {
    const value = Date.UTC(year, month - 1, day);
    const date = new Date(value);
    if (!Number.isFinite(value) ||
      date.getUTCFullYear() !== year ||
      date.getUTCMonth() !== month - 1 ||
      date.getUTCDate() !== day) return null;
    return value;
  }

  function sortableIsoWeekInstant(year, week) {
    if (week < 1 || week > 53) return null;
    const januaryFourth = Date.UTC(year, 0, 4);
    const weekday = (new Date(januaryFourth).getUTCDay() + 6) % 7;
    const monday = januaryFourth - weekday * SORTABLE_DAY_MS;
    const value = monday + (week - 1) * 7 * SORTABLE_DAY_MS;
    const thursday = new Date(value + 3 * SORTABLE_DAY_MS);
    const isoYear = thursday.getUTCFullYear();
    const firstThursday = Date.UTC(isoYear, 0, 4);
    const firstMonday = firstThursday - ((new Date(firstThursday).getUTCDay() + 6) % 7) * SORTABLE_DAY_MS;
    const isoWeek = Math.floor((value - firstMonday) / (7 * SORTABLE_DAY_MS)) + 1;
    return isoYear === year && isoWeek === week ? value : null;
  }

  // sortableDateInstant compares real instants, never localized display
  // strings. It covers RFC3339, calendar days, months, ISO weeks and the
  // weekday bucket keys produced by Ask time answers.
  function sortableDateInstant(value) {
    if (value instanceof Date) {
      const time = value.getTime();
      return Number.isFinite(time) ? time : null;
    }
    if (typeof value !== "string") return null;
    const raw = value.trim();
    if (!raw) return null;
    const weekday = SORTABLE_WEEKDAY_INDEX[raw.toLowerCase()];
    if (weekday !== undefined) return Date.UTC(1970, 0, 5 + weekday * 7);
    let match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw);
    if (match) return sortableCalendarInstant(Number(match[1]), Number(match[2]), Number(match[3]));
    match = /^(\d{4})-(\d{2})$/.exec(raw);
    if (match) {
      const month = Number(match[2]);
      if (month < 1 || month > 12) return null;
      return Date.UTC(Number(match[1]), month - 1, 1);
    }
    match = /^(\d{4})-W(\d{2})$/i.exec(raw);
    if (match) return sortableIsoWeekInstant(Number(match[1]), Number(match[2]));
    match = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?(Z|[+-]\d{2}:?\d{2})$/.exec(raw);
    if (match) {
      if (sortableCalendarInstant(Number(match[1]), Number(match[2]), Number(match[3])) === null) return null;
      if (Number(match[4]) > 23 || Number(match[5]) > 59 || Number(match[6] || 0) > 60) return null;
      const time = Date.parse(raw);
      return Number.isFinite(time) ? time : null;
    }
    return null;
  }

  function sortableEmailKey(value) {
    if (typeof value !== "string") return null;
    const raw = value.trim();
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(raw) ? raw.toLowerCase() : null;
  }

  function sortablePhoneKey(value) {
    let raw = "";
    if (typeof value === "number") raw = Number.isInteger(value) ? String(value) : "";
    else if (typeof value === "string") raw = value.trim();
    if (!raw) return null;
    if (sortableDateInstant(raw) !== null) return null;
    if (!/^[+(]?[\d\s()./\-\u00a0\u202f]*$/.test(raw)) return null;
    const digits = raw.replace(/\D/g, "");
    return digits.length >= 7 && digits.length <= 20 ? digits : null;
  }

  function sortableMatchesType(type, value) {
    if (type === "date") return sortableDateInstant(value) !== null;
    if (type === "email") return sortableEmailKey(value) !== null;
    if (type === "phone") return sortablePhoneKey(value) !== null;
    if (type === "number") return sortableNumberValue(value) !== null;
    return true;
  }

  // sortableInferType samples at most 40 non-empty cells and requires every
  // one to match. A single explicit column type never re-infers.
  function sortableInferType(column, rows) {
    const spec = sortableNormalizeColumn(column);
    if (spec.type !== "auto") return spec.type;
    const list = Array.isArray(rows) ? rows : [];
    const sample = [];
    for (let index = 0; index < list.length && sample.length < SORTABLE_SAMPLE_LIMIT; index += 1) {
      const value = spec.value(list[index], index);
      if (sortableIsEmpty(value)) continue;
      sample.push(value);
    }
    if (!sample.length) return "text";
    for (const type of ["date", "email", "phone", "number"]) {
      if (sample.every((value) => sortableMatchesType(type, value))) return type;
    }
    return "text";
  }

  // sortableSortKey parses one cell once. A parsed null key is an invalid
  // value for an explicit type and therefore sorts with the empties.
  function sortableSortKey(type, value) {
    if (type === "date") return sortableDateInstant(value);
    if (type === "number") return sortableNumberValue(value);
    if (type === "email") return sortableEmailKey(value);
    if (type === "phone") return sortablePhoneKey(value);
    return value == null ? "" : String(value);
  }

  // sortableCompareKeys returns the type-aware order for parsed keys. Text
  // goes through the single collator exported by i18n.js.
  function sortableCompareKeys(type, left, right) {
    if (left === null && right === null) return 0;
    if (left === null) return 1;
    if (right === null) return -1;
    if (type === "phone") {
      if (left.length !== right.length) return left.length < right.length ? -1 : 1;
      return left === right ? 0 : left < right ? -1 : 1;
    }
    if (type === "date" || type === "number") {
      return left === right ? 0 : left < right ? -1 : 1;
    }
    return compareText(left, right);
  }

  // sortableOrder returns the row positions in sorted order. It never
  // mutates rows: empty cells stay last in both directions and equal values
  // keep their original (server) order.
  function sortableOrder(rows, column, direction) {
    const list = Array.isArray(rows) ? rows : [];
    const spec = sortableNormalizeColumn(column);
    const resolved = sortableInferType(spec, list);
    const descending = sortableNormalizeDirection(direction) === "desc";
    const decorated = list.map((row, index) => {
      const value = spec.value(row, index);
      const key = sortableSortKey(resolved, value);
      return { index, key, empty: sortableIsEmpty(value) || key === null };
    });
    decorated.sort((left, right) => {
      if (left.empty || right.empty) {
        if (left.empty && right.empty) return left.index - right.index;
        return left.empty ? 1 : -1;
      }
      const compared = sortableCompareKeys(resolved, left.key, right.key);
      if (compared !== 0) return descending ? -compared : compared;
      return left.index - right.index;
    });
    return decorated.map((entry) => entry.index);
  }

  function sortableStorageKey(persistKey) {
    return "mailsense.table-sort." + String(persistKey || "");
  }

  // sortableReadPreference ignores corrupt, foreign and unavailable
  // storage; sorting is an enhancement, never a startup failure.
  function sortableReadPreference(persistKey) {
    if (!persistKey) return null;
    try {
      const raw = window.localStorage.getItem(sortableStorageKey(persistKey));
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object") return null;
      const direction = sortableNormalizeDirection(parsed.direction);
      const key = typeof parsed.key === "string" ? parsed.key : "";
      return key && direction ? { key, direction } : null;
    } catch { return null; }
  }

  function sortableWritePreference(persistKey, state) {
    if (!persistKey || !state) return;
    try {
      window.localStorage.setItem(sortableStorageKey(persistKey), JSON.stringify({
        key: state.key,
        direction: state.direction,
      }));
    } catch { /* storage is optional */ }
  }

  // sortableTable builds the accessible shell once. `append` adds new rows
  // (Answers pagination) and re-applies the active sort; `setSort` only moves
  // existing nodes and updates aria-sort on the owning <th>.
  function sortableTable(options) {
    const config = options || {};
    const columns = (Array.isArray(config.columns) ? config.columns : []).map(sortableNormalizeColumn);
    const caption = String(config.caption || "");
    const wrapperClass = ("table-wrap " + String(config.wrapperClass || "")).trim();
    const wrapper = h("div", {
      class: wrapperClass,
      tabindex: "0",
      role: "region",
      "aria-label": String(config.label || t("table.region_aria", { caption })),
    });
    const table = h("table", { class: ("data-table " + String(config.className || "")).trim() });
    if (caption) {
      table.append(h("caption", {
        class: config.captionVisible ? "table-caption" : "sr-only",
        text: caption,
      }));
    }
    const headRow = h("tr");
    const headers = columns.map((column) => {
      const attrs = { scope: "col" };
      if (column.align) attrs.class = column.align;
      if (!column.sortable) {
        const th = h("th", attrs, text(column.label));
        headRow.append(th);
        return { column, th, button: null };
      }
      attrs.class = (attrs.class ? attrs.class + " " : "") + "sortable";
      attrs["aria-sort"] = "none";
      const buttonNode = h("button", {
        type: "button",
        class: "sort-button",
        "aria-label": t("table.sort_by", { column: column.label }),
      },
      h("span", { class: "sort-label", text: column.label }),
      h("span", { class: "sort-indicator", "aria-hidden": "true" }));
      const th = h("th", attrs, buttonNode);
      headRow.append(th);
      return { column, th, button: buttonNode };
    });
    const body = h("tbody");
    table.append(h("thead", null, headRow), body);
    wrapper.append(table);

    let sortState = null;
    const records = [];

    function updateHeaders() {
      for (const header of headers) {
        if (!header.button) continue;
        const active = !!sortState && sortState.key === header.column.key;
        header.th.setAttribute("aria-sort", active
          ? (sortState.direction === "asc" ? "ascending" : "descending")
          : "none");
        header.button.setAttribute("aria-label", active
          ? t(sortState.direction === "asc" ? "table.sort_ascending" : "table.sort_descending", { column: header.column.label })
          : t("table.sort_by", { column: header.column.label }));
      }
    }

    function applySort() {
      if (!sortState) return;
      const column = columns.find((entry) => entry.sortable && entry.key === sortState.key);
      if (!column) return;
      const order = sortableOrder(records.map((record) => record.row), column, sortState.direction);
      for (const position of order) {
        const record = records[position];
        body.append(record.node);
        for (const detail of record.details) body.append(detail);
      }
    }

    function setSort(key, direction, setOptions = {}) {
      const header = headers.find((entry) => entry.button && entry.column.key === key);
      if (!header) return;
      const normalized = sortableNormalizeDirection(direction) || header.column.defaultDirection;
      sortState = { key: header.column.key, direction: normalized };
      updateHeaders();
      applySort();
      if (setOptions.persist !== false) sortableWritePreference(config.persistKey, sortState);
    }

    function append(rows) {
      const list = Array.isArray(rows) ? rows : [];
      for (const row of list) {
        const index = records.length;
        const tr = h("tr", config.rowClass ? { class: config.rowClass(row, index) } : null);
        for (const column of columns) {
          // The stacked phone layout labels each cell from its column, so a
          // table stays readable without a horizontal scroll pane.
          const attrs = { "data-label": column.label, "data-key": column.key };
          if (column.align) attrs.class = column.align;
          if (column.render) {
            tr.append(h("td", attrs, column.render(row, index)));
            continue;
          }
          const value = column.value(row, index);
          tr.append(h("td", attrs, sortableIsEmpty(value) ? "" : String(value)));
        }
        let detail = typeof config.detail === "function" ? config.detail(row, index) : null;
        if (detail && typeof detail === "object" && detail.nodeType === 11 && detail.childNodes) {
          detail = Array.from(detail.childNodes);
        }
        const details = (Array.isArray(detail) ? detail : [detail]).filter(Boolean);
        body.append(tr);
        for (const node of details) body.append(node);
        records.push({ row, index, node: tr, details });
      }
      applySort();
    }

    for (const header of headers) {
      if (!header.button) continue;
      header.button.addEventListener("click", () => {
        const active = sortState && sortState.key === header.column.key ? sortState.direction : "";
        const next = active === "asc" ? "desc" : active === "desc" ? "asc" : header.column.defaultDirection;
        setSort(header.column.key, next);
      });
    }

    const stored = sortableReadPreference(config.persistKey);
    if (stored && headers.some((entry) => entry.button && entry.column.key === stored.key)) sortState = stored;
    updateHeaders();
    append(config.rows || []);

    return { node: wrapper, table, columns, append, setSort, state: () => sortState };
  }
  // sortable:end

