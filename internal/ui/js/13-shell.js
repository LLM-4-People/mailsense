  // ---------------------------------------------------------------- navigation

  // navigateWith is the one cross-screen link of the interface. It is a real
  // anchor, so it is keyboard reachable, opens in a new tab with the platform
  // modifier, and Back returns to the route the owner left. A second ad hoc
  // href is how a link forgets the router's interception and reloads the
  // document. Callers name a SCREEN_ID and its route parameters, never a
  // hand-built hash.
  function navigateWith(label, name, params = {}, opts = {}) {
    const anchor = link(label, routeHash(name, params), opts);
    anchor.dataset.crosslink = name;
    return anchor;
  }

  let navInstalled = false;

  // navGroupState is the one stored disclosure state: a group id mapped to
  // whether the owner last left it open. Corrupt, foreign or unavailable
  // storage falls back to the default, every group open.
  function navGroupState() {
    try {
      const raw = window.localStorage.getItem(NAV_GROUPS_KEY);
      if (!raw) return {};
      const parsed = JSON.parse(raw);
      return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
    } catch { return {}; }
  }

  function writeNavGroupState(values) {
    try { window.localStorage.setItem(NAV_GROUPS_KEY, JSON.stringify(values)); } catch { /* storage is optional */ }
  }

  function navGroups() {
    return Array.from(document.querySelectorAll("[data-nav-group]"));
  }

  function navGroupOwnsRoute(group, name) {
    return !!group.querySelector('[data-nav="' + name + '"]');
  }

  function navGroupSummary(group) {
    return group.querySelector("summary");
  }

  // syncNavGroups applies the stored state and keeps the group that owns the
  // active route open. The stored value is never rewritten here, so a group
  // the owner closed reopens only for as long as its route is active.
  function syncNavGroups(name) {
    const stored = navGroupState();
    for (const group of navGroups()) {
      const id = group.dataset.navGroup || "";
      if (!id) continue;
      const open = navGroupOwnsRoute(group, name) || stored[id] !== false;
      group.open = open;
      const summary = navGroupSummary(group);
      if (summary) summary.setAttribute("aria-expanded", String(open));
    }
  }

  function navDrawerOpen() {
    const sidebar = document.querySelector(".sidebar");
    return !!sidebar && sidebar.classList.contains("drawer-open");
  }

  // setNavDrawer owns the narrow-window drawer state. On the desktop layout
  // the class and aria-expanded are harmless no-ops.
  function setNavDrawer(open, options = {}) {
    const sidebar = document.querySelector(".sidebar");
    const toggle = document.getElementById("nav-toggle");
    if (!sidebar || !toggle) return;
    sidebar.classList.toggle("drawer-open", open);
    toggle.setAttribute("aria-expanded", String(open));
    if (!open && options.focusToggle) toggle.focus();
  }

  // installNav wires the disclosure groups and the narrow-window drawer once.
  function installNav() {
    if (navInstalled) return;
    navInstalled = true;
    for (const group of navGroups()) {
      const summary = navGroupSummary(group);
      if (!summary || !group.dataset.navGroup) continue;
      // The toggle event also fires for the programmatic open in syncNavGroups,
      // so it only mirrors the state for assistive technology; it never
      // persists.
      group.addEventListener("toggle", () => {
        summary.setAttribute("aria-expanded", String(group.open));
      });
      // A click's default action toggles <details> after dispatch. The one
      // group the owner changed is persisted after that action, so the
      // forced-open active group cannot overwrite another stored choice.
      summary.addEventListener("click", () => {
        window.setTimeout(() => {
          const stored = navGroupState();
          stored[group.dataset.navGroup] = group.open;
          writeNavGroupState(stored);
        }, 0);
      });
    }
    const toggle = document.getElementById("nav-toggle");
    const nav = document.getElementById("nav-menu");
    if (!toggle || !nav) return;
    toggle.addEventListener("click", () => setNavDrawer(!navDrawerOpen()));
    // A destination that is already active changes no hash, so the router does
    // not run; the drawer still closes on the selection itself.
    nav.addEventListener("click", (event) => {
      const link = event.target instanceof Element ? event.target.closest("[data-nav]") : null;
      if (link) setNavDrawer(false);
    });
    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape" || state.modalOpen || !navDrawerOpen()) return;
      event.preventDefault();
      setNavDrawer(false, { focusToggle: true });
    });
  }

  // ---------------------------------------------------------------- events

  let eventSource = null;
  let learningRefreshTimer = 0;
  let eventsGap = false;
  let liveResyncTimer = 0;

  // mailSurface names the tab of the one Mail screen. Every Mail tab has the
  // routed name "mail", so matching only route names would refresh all four
  // tabs or none; the completion handlers name the surface that changed.
  function mailSurface(route) {
    if (!route || route.name !== "mail") return "";
    const mode = parseMode(route);
    if (mode === "important") return "priority";
    if (mode === "recent") return "triage";
    return mode;
  }

  // refreshVisibleRoutes refreshes the active screen only when it is one of
  // the read-only screens the event feeds. The route cache and API caches
  // are already invalidated; refreshActiveRoute keeps its focus guard, so a
  // field being typed in is never replaced.
  function refreshVisibleRoutes(...names) {
    if (!state.token || state.shuttingDown || state.modalOpen) return;
    const surface = state.route === "mail" ? mailSurface(parseRoute()) : state.route;
    if (!names.includes(state.route) && !names.includes(surface)) return;
    refreshActiveRoute();
  }

  // ------------------------------------------------------- invalidation graph
  //
  // One graph owns what a live data change makes wrong. A subscriber names a
  // topic; it never spells read prefixes, cached screens or refresh targets
  // itself. `reads` are the cached API reads the change makes wrong (the
  // cache choke point also clears the local models derived from them),
  // `routes` are the cached screens whose data advanced, and `refresh` are
  // the mounted screens the one coalescer may rebuild.
  //
  // A topic may also declare:
  //   forgetMail      drop the All-Mail local model with the reads;
  //   resetQuestions  drop the state copy of the question catalogue;
  //   allRoutes       the route cache as a whole (a route-only topic).
  //
  // invalidation-topics:start
  // A completed sync can add, remove or rewrite mail. These are the reads
  // that carry a row, a body, a classification, a search hit or a count
  // derived from it; status and learning feed the chrome around those
  // screens.
  const SYNC_FINISHED_READS = [
    "/api/messages?", "/api/messages/", "/api/conversations", "/api/companies",
    "/api/priority", "/api/hub", "/api/triage", "/api/brief", "/api/agenda",
    "/api/commitments", "/api/labels", "/api/status", "/api/learning",
    "/api/search",
  ];

  // Triage only reclassifies mail already stored: membership, bodies and the
  // sources' read state are untouched.
  const TRIAGE_FINISHED_READS = [
    "/api/triage", "/api/priority", "/api/conversations", "/api/agenda",
    "/api/brief", "/api/companies", "/api/hub", "/api/labels",
  ];

  // Picking or unpicking a store changes the visible mail at the next read
  // without a sync: every mail-derived read changes, and so do the gate and
  // search scope. The sync read set is the base; selection adds the reads
  // that only it can move. Search is named again here because a selection
  // change moves it without a sync.
  const SELECTION_READS = SYNC_FINISHED_READS.concat([
    "/api/search", "/api/answers", "/api/gate", "/api/threads",
  ]);

  const INVALIDATION_TOPICS = {
    // A label or judge write changes labels, stored answers, the Hub's label
    // gate, the learning status and the permission snapshot. It never
    // changes mail, companies, priority, conversations, commitments, threads,
    // triage, questions or Brief: those derive from message content, not from
    // a label.
    labels: {
      reads: ["/api/labels", "/api/answers", "/api/hub", "/api/learning", "/api/gate"],
      routes: ["labels", "answers", "hub", "learning"],
      refresh: ["answers", "hub", "learning"],
    },
    // A committed index run changes the vectors search ranks with, plus the
    // embeddings stage on Learning and Hub. It does not change mail rows,
    // companies or Brief.
    index: {
      reads: ["/api/search", "/api/learning", "/api/hub"],
      routes: ["search", "learning", "hub"],
      refresh: ["search", "learning", "hub"],
    },
    // A finished run always advances the embeddings stage, even when it
    // committed no vector. Only the committed half changes search data.
    indexStatus: {
      reads: ["/api/learning", "/api/hub"],
      routes: ["learning", "hub"],
      refresh: ["learning", "hub"],
    },
    mail: {
      reads: SYNC_FINISHED_READS,
      routes: ["mail", "conversations", "companies", "hub", "brief", "triage", "priority", "agenda", "search"],
      refresh: ["mail", "conversations", "companies", "hub", "brief", "triage", "priority", "agenda", "search"],
      forgetMail: true,
    },
    triage: {
      reads: TRIAGE_FINISHED_READS,
      routes: ["conversations", "companies", "hub", "brief", "triage", "priority", "agenda"],
      refresh: ["conversations", "companies", "hub", "brief", "triage", "priority", "agenda"],
    },
    // A sync can hold a removal back and a confirmation clears one. Only
    // Sources owns the confirmation card, so this topic never rebuilds a
    // mail view before the owner confirms the deletion.
    heldRemovals: {
      reads: ["/api/held-removals"],
      routes: ["sources"],
      refresh: [],
    },
    // A mailbox sync changes list membership, not the body of a message
    // already stored: the detail reads stay, the list reads go.
    mailboxes: {
      reads: ["/api/mailboxes", "/api/status", "/api/learning", "/api/hub", "/api/messages?"],
      routes: ["sources", "learning", "hub", "mail"],
      refresh: ["sources", "learning", "hub"],
      forgetMail: true,
    },
    // Discovery refreshes one source's mailbox list and its counts. It reads
    // no message, so stored mail, its reads and the All-Mail model stay.
    discovery: {
      reads: ["/api/mailboxes", "/api/status", "/api/learning", "/api/hub"],
      routes: ["sources", "learning", "hub"],
      refresh: ["sources", "learning", "hub"],
    },
    // Picking or unpicking a mailbox changes which stored mail is visible
    // everywhere, at the next store read. This is the mail topic's scope
    // plus the reads that derive from visibility (search, answers, gate,
    // threads) and the Sources screen that owns the action.
    selection: {
      reads: SELECTION_READS,
      routes: ["mail", "conversations", "companies", "hub", "brief", "triage", "priority", "agenda", "answers", "labels", "learning", "sources", "search"],
      refresh: ["mail", "conversations", "companies", "hub", "brief", "triage", "priority", "agenda", "answers", "labels", "learning", "sources", "search"],
      forgetMail: true,
    },
    // A job start or cancel changes the authoritative job snapshot and the
    // stage status around it, not the mail those jobs will later read.
    job: {
      reads: ["/api/jobs", "/api/learning", "/api/status", "/api/hub"],
      routes: ["sources", "learning", "hub"],
      refresh: ["sources", "learning", "hub"],
    },
    // A confirmed zone changes the status, the learning machine stage and
    // every formatted date. The data reads are exactly those three; every
    // cached screen is only marked stale, so its scroll and focus survive
    // until its next mount revalidates the formatting.
    timeZone: {
      reads: ["/api/status", "/api/learning", "/api/hub"],
      routes: [],
      refresh: [],
      allRoutes: true,
    },
    // Turning the test preview on or off changes permission, the learning
    // view and every gated read whose snapshot is keyed by the preview rule:
    // the ranked feed, the triage decisions, the Brief snapshot and the
    // conversation states that feed resolves. Their reads and derived models
    // drop with the mounted screens, so no screen paints a pre-flip snapshot.
    preview: {
      reads: ["/api/learning", "/api/hub", "/api/gate", "/api/priority", "/api/triage", "/api/conversations", "/api/brief"],
      routes: ["hub", "brief", "learning", "mail", "companies", "labels", "answers"],
      refresh: ["hub", "brief", "learning", "mail", "companies", "labels", "answers"],
    },
    // A display-HTML pass changes the stored body of one message. The list
    // rows and their membership are unchanged.
    detail: {
      reads: ["/api/messages/"],
      routes: ["mail"],
      refresh: [],
    },
    // Sync truth changes on every finished run, including a run that stored
    // no mail; the mail rows do not.
    hub: {
      reads: ["/api/hub"],
      routes: ["hub"],
      refresh: ["hub"],
    },
    // Evidence and layer-1 runs change the learning-derived views, including
    // the search ranking that reads their evidence.
    learning: {
      reads: [
        "/api/status", "/api/learning", "/api/hub", "/api/labels", "/api/answers",
        "/api/triage", "/api/priority", "/api/conversations", "/api/commitments",
        "/api/brief", "/api/companies", "/api/threads", "/api/questions", "/api/gate",
        "/api/search",
      ],
      routes: ["learning", "hub", "brief", "answers", "companies", "labels", "mail"],
      refresh: ["learning", "hub", "brief", "answers", "companies"],
      resetQuestions: true,
    },
    // A changed permission snapshot can make the derived screens wrong.
    gate: {
      reads: [],
      routes: ["hub", "brief", "learning", "mail", "companies", "labels"],
      refresh: [],
    },
    // The authoritative reads a job progress tick can stale before the forced
    // learning and gate reads settle.
    learningStatus: {
      reads: [
        "/api/hub", "/api/jobs", "/api/labels/summary", "/api/answers",
        "/api/commitments", "/api/brief", "/api/companies", "/api/threads", "/api/gate",
      ],
      routes: [],
      refresh: [],
    },
    // The broad write paths add configuration, sources, providers, jobs and
    // the Ask history to the learning reads.
    config: {
      reads: [
        "/api/settings", "/api/mailboxes", "/api/jobs", "/api/sidecar",
        "/api/providers", "/api/secrets", "/api/ask/history",
      ],
      routes: [],
      refresh: [],
    },
    // Process-level reads re-based after a dropped stream.
    runtime: {
      reads: ["/api/jobs", "/api/sidecar"],
      routes: [],
      refresh: [],
    },
    // The sidecar state is one read; its screen refreshes itself below.
    sidecar: {
      reads: ["/api/sidecar"],
      routes: [],
      refresh: [],
    },
    // Route data only: no read and no refresh, every cached screen.
    route: {
      reads: [],
      routes: [],
      refresh: [],
      allRoutes: true,
    },
  };
  // invalidation-topics:end

  // invalidateTopics is the one reactor for a live data change. It derives
  // the cache invalidation, the route stales (or drops) and the coalesced
  // refresh from the graph; a caller names topics, never prefixes.
  function invalidateTopics(topics, options = {}) {
    const hard = !!options.hard;
    const touchRoutes = options.routes !== false;
    const refresh = options.refresh !== false;
    const reads = new Set();
    const routes = new Set();
    const targets = new Set();
    let allRoutes = false;
    let forgetMail = false;
    let resetQuestions = false;
    for (const name of topics) {
      const topic = INVALIDATION_TOPICS[name];
      // A misspelled topic must fail loudly: silently skipping it would leave
      // every consumer on the previous snapshot.
      if (!topic) throw new Error("unknown invalidation topic: " + name);
      for (const prefix of topic.reads) reads.add(prefix);
      for (const route of topic.routes) routes.add(route);
      for (const route of topic.refresh) targets.add(route);
      if (topic.allRoutes) allRoutes = true;
      if (topic.forgetMail) forgetMail = true;
      if (topic.resetQuestions) resetQuestions = true;
    }
    // invalidateCache treats an empty prefix list as the full clear; a
    // route-only or gate-only topic must not wipe every read.
    if (reads.size) invalidateCache([...reads]);
    // loadQuestions short-circuits on its state copy. Dropping /api/questions
    // must drop that copy too, or a profile edit stays invisible until reload.
    if (resetQuestions) state.questions = null;
    if (forgetMail && options.forgetMail !== false) forgetAllMail();
    if (touchRoutes) {
      if (allRoutes) {
        if (hard) invalidateRouteCache();
        else staleRouteCache();
      } else if (routes.size) {
        const names = [...routes];
        if (hard) invalidateRouteCache(...names);
        else staleRouteCache(...names);
      }
    }
    if (refresh && targets.size) scheduleDataRefresh(...targets);
  }

  // invalidateLearningReads drops every cached read a write can stale. It is
  // the data half; callers that changed the mounted screens use the hard
  // route path through invalidateTopics instead.
  function invalidateLearningReads() {
    invalidateTopics(["learning"], { routes: false, refresh: false });
  }

  // invalidateLearningDerived is the write path: the reads and the mounted
  // screens both changed, so the next visit rebuilds.
  function invalidateLearningDerived() {
    invalidateTopics(["learning"], { hard: true });
  }

  // invalidateSettingsReads drops the reads every settings write can stale.
  // Three paths spelled the same two prefixes; a new one that forgot /status
  // left the header on the previous configuration.
  function invalidateSettingsReads() {
    invalidateCache(["/api/settings", "/api/status"]);
  }

  // invalidateAllReads is the broad read invalidation used when a write can
  // change anything: the learning-derived reads plus configuration, sources,
  // providers, jobs and the Ask history. The All-Mail model is local, not an
  // HTTP read, so the same owner drops it.
  function invalidateAllReads() {
    invalidateTopics(["learning", "config"], { routes: false, refresh: false });
    forgetAllMail();
  }

  // forgetAllMail drops the All-Mail list model after a write or a sync. It
  // is a local model, not an HTTP read, so invalidating the cache alone would
  // leave the next mount serving the previous rows. A cached All-Mail node
  // carries its own copy for the router's restore path; clear that copy too,
  // or the next mount would re-seat the model this function just dropped.
  function forgetAllMail() {
    state.allMail.loaded = false;
    state.allMail.capped = false;
    state.allMail.promise = null;
    state.allMail.morePromise = null;
    for (const entry of routeCache.values()) {
      if (entry.name !== "mail" || !entry.node || typeof entry.node.querySelector !== "function") continue;
      const list = entry.node.querySelector(".mail-list");
      if (list) list.__allMailModel = null;
    }
  }

  // forgetPriorityPages drops the ranked page models after a sync or a
  // reranking read. The map is a local model like All Mail: dropping the HTTP
  // cache alone would let the next mount paint the previous ranking. A cached
  // Important node carries the model it painted, so clear every one of its
  // tokens too and make the cached mount rebuild.
  function forgetPriorityPages() {
    state.priorityPages.clear();
    forgetPagingTokens("important");
  }

  // forgetTriagePage drops the accumulated recent-mail model after a triage
  // run. Its later pages carry decisions from the previous snapshot, so the
  // whole model, not only the first page, goes with the read.
  function forgetTriagePage() {
    state.triagePage = null;
    forgetPagingTokens("recent");
  }

  // forgetPagingTokens clears the token of every cached node that belongs to
  // one paging screen, so a cached mount cannot re-seat a model the caller
  // dropped. The screen name travels on the token's page; a wrapper may cache
  // the token on its content node, so every carrier in the subtree is cleared.
  function forgetPagingTokens(screen) {
    const drop = (node) => {
      // Only a cached, detached node loses its token. The mounted screen is
      // still painted and its Load more must stay live; dropping its token
      // would leave the visible control dead until the route remounts. Its
      // next page compares generated_at and refreshes when the snapshot moved.
      if (node.isConnected) return;
      const page = readPagingToken(node);
      if (page && page.screen === screen) node.__pagingModel = null;
    };
    for (const entry of routeCache.values()) {
      if (!entry.node) continue;
      drop(entry.node);
      if (typeof entry.node.querySelectorAll !== "function") continue;
      for (const node of entry.node.querySelectorAll("*")) drop(node);
    }
  }

  // eventCount reads one numeric report field. Missing or malformed fields
  // are zero, so one absent counter cannot turn a changed run into a no-op.
  function eventCount(value) {
    const count = Number(value);
    return Number.isFinite(count) ? count : 0;
  }

  // syncChangedMail reports whether a finished sync changed mail the screens
  // show. A run that only reconciled cursors or wrote nothing must not
  // rebuild a screen; a missing payload is an older server and also cannot
  // claim a change.
  function syncChangedMail(report) {
    const totals = report && report.totals ? report.totals : {};
    const removed = report && report.removed ? report.removed : {};
    const scope = report && report.scope ? report.scope : {};
    const details = report && report.details ? report.details : {};
    return eventCount(totals.written) > 0 ||
      eventCount(removed.deleted) + eventCount(removed.moved) > 0 ||
      eventCount(scope.judged) > 0 ||
      eventCount(details.completed) > 0;
  }

  // syncFinishedTopics names every topic one finished sync changed. The hub's
  // sync truth advances on every run, including one that wrote no mail, and a
  // held removal can change without any other write. The mail topic only runs
  // when the report says mail actually changed. The listener passes this union
  // to invalidateTopics once, so a changed-mail run clears the shared
  // /api/hub read in one cache and route pass instead of two.
  function syncFinishedTopics(report) {
    const topics = ["hub", "heldRemovals"];
    if (syncChangedMail(report)) topics.push("mail");
    return topics;
  }

  // triageChangedMail reports whether a finished triage run stored a verdict.
  function triageChangedMail(report) {
    return eventCount(report && report.decided) + eventCount(report && report.partial) > 0;
  }

  // indexCommitted mirrors index.Report.Committed: a run that stored or
  // confirmed at least one vector changed the searchable data; a run stopped
  // before any write (or a missing report from an older server) did not.
  function indexCommitted(report) {
    return eventCount(report && report.embedded) + eventCount(report && report.unchanged) > 0;
  }

  // invalidateAllData is the local write path: the reads and the mounted
  // screens both changed, so the next visit rebuilds. invalidateAllReads
  // already dropped the All-Mail model with the reads.
  function invalidateAllData() {
    invalidateAllReads();
    invalidateRouteCache();
  }

  // afterLearningDataChanged refreshes the read-only screens that show a
  // finished evidence or layer-1 stage. Teach and Mail keep their mounted
  // task (and typing); an inactive screen keeps its snapshot and revalidates
  // on the next mount, so a background run never discards scroll or focus.
  // The refresh itself is coalesced: the two stages finish within seconds of
  // each other and would otherwise rebuild the same screens twice.
  function afterLearningDataChanged() {
    invalidateTopics(["learning"]);
  }

  // scheduleDataRefresh is the one coalescer of live screen refreshes. It
  // keeps the union of the routes that asked, refreshes the learning status
  // once, and rebuilds the active route only if it is one of them.
  function scheduleDataRefresh(...routes) {
    for (const route of routes) pendingRefreshRoutes.add(route);
    if (dataRefreshTimer) return;
    dataRefreshTimer = window.setTimeout(async () => {
      dataRefreshTimer = 0;
      const names = [...pendingRefreshRoutes];
      pendingRefreshRoutes.clear();
      try {
        await loadLearning(true);
        refreshVisibleRoutes(...names);
      } catch (error) {
        reportBackgroundError(error);
      }
    }, 350);
  }

  let dataRefreshTimer = 0;
  const pendingRefreshRoutes = new Set();

  // learningStructure is what a live status must rebuild for: the stage set,
  // each stage's state and blockers, and the overall state/scope. Progress,
  // note and ETA change in place on the Learning screen.
  function learningStructure(status) {
    if (!status) return "";
    const stages = Array.isArray(status.stages) ? status.stages : [];
    return [status.state || "", status.scope || ""].concat(
      stages.map((stage) => [stage.id || "", stage.state || "",
        Array.isArray(stage.blockers) ? stage.blockers.length : 0].join(":")),
    ).join("|");
  }

  // applyLearningStatus patches a progress-only event in place. The payload
  // is the complete status, so no /api/learning read and no route rebuild is
  // needed; a structural change still goes through the one coalesced refresh.
  function applyLearningStatus(before) {
    const status = state.learning && state.learning.status;
    if (!status) return;
    if (learningStructure(status) !== before) {
      scheduleLearningRefresh();
      return;
    }
    if (state.route !== "learning") return;
    for (const stage of Array.isArray(status.stages) ? status.stages : []) {
      updateLearningStageNode(stage);
    }
  }

  // handleLogWake only refreshes while Logs is mounted. The frame carries a
  // cursor and a count, not entry text; another route ignores it untouched.
  function handleLogWake(event) {
    if (state.route !== "logs") return;
    let wake;
    try { wake = JSON.parse(event.data); } catch { return; }
    if (!wake || !(Number(wake.count) > 0)) return;
    scheduleLogRefresh();
  }

  // listenMailCompletion wires the two finished-run topics at the shell's one
  // event owner. The handlers only decide what the run changed; the graph
  // owns the reaction and the existing coalescer the visible refresh.
  function listenMailCompletion(source) {
    source.addEventListener("sync.finished", (event) => {
      let report = null;
      try { report = JSON.parse(event.data); } catch { report = null; }
      // One finished run names every topic it changed in one pass: the hub's
      // sync truth changes on every run, a held removal can change without a
      // mail write, and the mail topic only runs when the report says mail
      // actually changed. The union clears the shared /api/hub read once.
      invalidateTopics(syncFinishedTopics(report));
      // Sources owns the confirmation card, so its list still refreshes even
      // when no other topic changed.
      scheduleSourcesRefresh();
    });
    source.addEventListener("triage.finished", (event) => {
      let report = null;
      try { report = JSON.parse(event.data); } catch { report = null; }
      if (!triageChangedMail(report)) return;
      invalidateTopics(["triage"]);
    });
  }

  function connectEvents() {
    if (!state.token || state.shuttingDown) return;
    if (eventSource) eventSource.close();
    setConnection("reconnecting", t("connection.connecting_events"));
    const source = new EventSource("/api/events?token=" + encodeURIComponent(state.token));
    eventSource = source;
    source.addEventListener("open", () => {
      if (source !== eventSource) return;
      const reconnected = eventsGap;
      eventsGap = false;
      setConnection("live", t("connection.live"));
      if (reconnected) scheduleLiveResync();
    });
    source.addEventListener("error", () => {
      if (source !== eventSource || state.shuttingDown) return;
      eventsGap = true;
      if (source.readyState === EventSource.CLOSED) setConnection("offline", t("connection.offline"));
      else setConnection("reconnecting", t("connection.reconnecting"));
    });
    source.addEventListener("learning.status", (event) => {
      let payload;
      try { payload = JSON.parse(event.data); } catch { return; }
      const before = learningStructure(state.learning && state.learning.status);
      if (state.learning && state.learning.status) state.learning.status = payload;
      else state.learning = { status: payload, open: false };
      updateLearningChrome();
      applyLearningStatus(before);
    });
    source.addEventListener("index.progress", (event) => {
      let payload;
      try { payload = JSON.parse(event.data); } catch { return; }
      applyIndexProgress(payload);
    });
    source.addEventListener("index.finished", (event) => {
      let report = null;
      try { report = JSON.parse(event.data); } catch { report = null; }
      // The stage advanced on every finished run; only a run that stored or
      // confirmed a vector changed what search returns. A missing report is
      // an older server and cannot claim committed data.
      invalidateTopics([indexCommitted(report) ? "index" : "indexStatus"]);
    });
    source.addEventListener("sync.mailboxes", (event) => {
      try {
        const payload = JSON.parse(event.data);
        state.mailboxes = Array.isArray(payload) ? payload : null;
      } catch { state.mailboxes = null; }
      invalidateTopics(["mailboxes"]);
    });
    listenMailCompletion(source);
    source.addEventListener("jobs.update", (event) => {
      let info;
      try { info = JSON.parse(event.data); } catch { info = null; }
      if (!info || !info.id) return;
      // A progress tick is already painted by the strip and, for the one job
      // with a meter, patched in place. Only a job's first event and its
      // completion change what the screens list, so only those refresh.
      const known = state.jobs.has(info.id);
      upsertJob(info);
      const terminal = !!info.status && info.status !== "running";
      if (terminal && info.kind === "messages.html") {
        // A finished display-HTML pass changes the detail body; the list
        // rows are unchanged, so only detail and attachment reads are dropped.
        invalidateTopics(["detail"]);
      }
      if (!known || terminal) {
        scheduleLearningRefresh();
        scheduleSourcesRefresh();
      } else if (info.kind === "messages.html") {
        updateBackfillMeter(info);
      }
    });
    source.addEventListener("log.wake", handleLogWake);
    source.addEventListener("dropped", (event) => {
      let payload;
      try { payload = JSON.parse(event.data); } catch { return; }
      state.logDropped.push(payload);
      if (state.logDropped.length > 12) state.logDropped.shift();
      if (state.route === "logs") scheduleLogRender();
      // The stream skipped events: the shell must re-base every live read,
      // not only the visible log ring.
      scheduleLiveResync();
    });
    source.addEventListener("config.changed", async (event) => {
      let keys = [];
      try {
        const payload = JSON.parse(event.data || "{}");
        keys = Array.isArray(payload.keys) ? payload.keys : [];
      } catch { /* an older server may send no payload */ }
      const languageMayHaveChanged = keys.length === 0 || keys.includes("ui.language");
      if (languageMayHaveChanged) {
        const value = await readLanguageSetting();
        if (value !== null && value !== state.languageSetting) {
          await applyLanguageLive(value);
          return;
        }
      }
      const languageOnly = keys.length === 1 && keys[0] === "ui.language";
      if (!languageOnly) {
        // Another writer changed the configuration. The reads are dropped;
        // the mounted snapshots only advance, so an inactive screen keeps its
        // scroll and focus and revalidates when the owner returns.
        invalidateAllReads();
        invalidateTopics(["route"]);
      }
      if (!languageOnly && state.route === "settings") {
        toast(t("settings.config_changed_title"), "warn", t("settings.config_changed_body"));
      }
      if (!languageOnly) scheduleDataRefresh("settings", "brief", "hub", "learning");
    });
    source.addEventListener("sidecar.state", () => {
      invalidateTopics(["sidecar"]);
      if (state.route === "settings" && state.settingsTab === "runtime" && !state.modalOpen) refreshActiveRoute();
    });
    source.addEventListener("update.state", handleUpdateStateEvent);
    source.addEventListener("evidence.finished", () => afterLearningDataChanged());
    source.addEventListener("layer1.finished", () => afterLearningDataChanged());
    source.addEventListener("feedback.label", () => invalidateTopics(["labels"]));
  }

  // gateKey is what a status tick can change for the derived screens:
  // the gate, its scope, and whether the automatic answer screens may read.
  // Stage progress is not part of it: the topbar and the Learning screen show
  // that, and a progress tick must not throw away a cached Home, Brief, Mail
  // or Companies screen with its scroll position and focus.


  function scheduleLearningRefresh() {
    if (learningRefreshTimer) return;
    learningRefreshTimer = window.setTimeout(async () => {
      learningRefreshTimer = 0;
      try {
        const gateBefore = gateKey(state.gate);
        invalidateTopics(["learningStatus"], { routes: false, refresh: false });
        await loadLearning(true);
        // The gate is its own read: refresh it with the status so every
        // screen sees the same permission snapshot.
        try { await loadGate(); } catch { state.gate = null; }
        // Only a gate change can change what those screens are allowed to
        // show. A stage-progress tick still refreshes the Learning view below.
        if (gateKey(state.gate) !== gateBefore) {
          invalidateTopics(["gate"], { hard: true, refresh: false });
        }
        // A job progress tick must not rebuild Home: the job strip already
        // merges the event, and the completion events refresh the derived
        // screens. Only the learning view shows live job progress itself.
        if (state.route === "learning") await refreshActiveRoute();
      } catch (error) {
        reportBackgroundError(error);
      }
    }, 350);
  }

  let sourcesRefreshTimer = 0;

  function scheduleSourcesRefresh() {
    if (sourcesRefreshTimer) return;
    sourcesRefreshTimer = window.setTimeout(() => {
      sourcesRefreshTimer = 0;
      if (state.route === "sources") refreshActiveRoute();
    }, 500);
  }

  // scheduleLiveResync repairs everything a dropped stream can stale. It is
  // one coalesced pass: the shell status, the gate and the job strip are
  // re-read, the caches are invalidated, and the active screen is refreshed
  // only through the existing focus guard.
  function scheduleLiveResync() {
    if (liveResyncTimer) return;
    liveResyncTimer = window.setTimeout(async () => {
      liveResyncTimer = 0;
      if (!state.token || state.shuttingDown) return;
      // Every live topic plus the process-level reads re-bases the same
      // snapshot. The All-Mail model is forgotten after the mounted screen
      // settles, not before, so a Load more it already started is not lost.
      invalidateTopics(
        ["labels", "index", "mail", "triage", "mailboxes", "heldRemovals", "learning", "runtime", "route"],
        { hard: true, refresh: false, forgetMail: false },
      );
      try {
        await Promise.all([loadStatus(true), loadLearning(true), loadJobs(true), loadUpdateStatus().catch(() => null)]);
      } catch (error) {
        reportBackgroundError(error);
      }
      if (!state.modalOpen) await refreshActiveRoute();
      maybeOpenWhatsNew();
      // The mounted All-Mail screen is settled first, including a Load more
      // it already started; forgetting the model afterwards makes the next
      // mount re-read instead of restoring rows the dropped stream cannot
      // vouch for.
      forgetAllMail();
    }, 400);
  }

  // labelSummarySnapshot is the one label-summary fact at the UI boundary.
  // The hub snapshot already carries the same totals and question rows as
  // /api/labels/summary; a screen reads it while its read is current and
  // only reads the dedicated endpoint when it is not. It never returns both,
  // so the same counts cannot be painted from two sources in one render.
  // allowStale is only for the nav badge, which is a hint and must not blink
  // out on every background invalidation.
  function labelSummarySnapshot(allowStale = false) {
    const gate = state.hub && state.hub.gate ? state.hub.gate : null;
    if (!gate || !gate.labels) return null;
    // state.hub outlives its cache entry: an invalidation drops /api/hub but
    // not this pointer, and painting those counts would be stale.
    if (!allowStale && cached("/api/hub") === undefined) return null;
    return Object.assign({}, gate.labels, {
      questions: Array.isArray(gate.questions) ? gate.questions : [],
    });
  }

  function readLabelSummary() {
    const snapshot = labelSummarySnapshot();
    if (snapshot) return Promise.resolve({ value: snapshot, error: null });
    return settleRead(requestCached("/api/labels/summary", {}, CACHE_TTL.labelsSummary));
  }

  // labelSummaryUnavailable is the one failed label-summary slot: the shared
  // inline failed-read shape, the catalog's own title, and the retry that
  // re-reads the route. Answers, Learning and Labels all mount this block, so
  // the count surfaces never disagree about why they are unknown.
  function labelSummaryUnavailable(error) {
    return errorState(error, null,
      Object.assign({}, INLINE_READ_ERROR, { title: t("read.labels_unavailable") }));
  }

  function labelReviewCount() {
    const summary = labelSummarySnapshot(true);
    if (summary) return Number(summary.review) || 0;
    const cachedSummary = cachedStale("/api/labels/summary");
    return cachedSummary ? Number(cachedSummary.review) || 0 : 0;
  }

  function updateLearningChrome() {
    const pending = labelReviewCount();
    if (dom.navLabelsBadge) {
      dom.navLabelsBadge.hidden = pending <= 0;
      dom.navLabelsBadge.textContent = pending ? formatNumber(pending) : "";
      dom.navLabelsBadge.title = pending ? t("nav.review_badge", { count: pending }) : "";
    }
    const version = state.status && state.status.version ? state.status.version : t("common.dash");
    dom.metaVersion.textContent = version;
    dom.metaVersion.title = version;
    const launch = state.status && state.status.launch ? state.status.launch : "";
    dom.metaLaunch.textContent = launch ? launch.slice(0, 12) : t("common.dash");
    dom.metaLaunch.title = launch || "";
    const dataDir = state.status && state.status.data_dir ? state.status.data_dir : t("common.dash");
    dom.metaData.textContent = dataDir;
    dom.metaData.title = dataDir;
  }

  async function loadStatus(force = false) {
    const view = await requestCached("/api/status", { force }, CACHE_TTL.status);
    state.status = view;
    state.systemLanguage = view && view.system_language ? view.system_language : "";
    state.systemLocale = view && view.system_locale ? view.system_locale : "";
    I18N.configure({ ownerTimeZone: view && view.time_zone ? view.time_zone : "" });
    updateLearningChrome();
    return view;
  }

  async function loadLearning(force = false) {
    const view = await requestCached("/api/learning", { force }, CACHE_TTL.learning);
    state.learning = view;
    updateLearningChrome();
    return view;
  }

  // loadGate is the one owner of the permission snapshot: the Allow
  // decision, never the full learning status. It is not cached, because a
  // lock change must be seen by the next screen. Learning data describes
  // progress and blockers; it never grants access.
  // The gate is the shell's one permission owner and must stay uncached: a
  // stored answer could grant a screen the current server run no longer
  // allows. Concurrent renders, however, read the same snapshot; they share
  // one request and the next render after it settles reads again.
  let gateInflight = null;
  // gateBootSnapshot is the settled snapshot the boot read just produced.
  // The first route mount consumes it exactly once, so a Hub or Mail mount
  // does not send the same /api/gate request a second time at boot. Any
  // later navigation has already cleared it and reads the gate again.
  let gateBootSnapshot = null;

  function loadGate() {
    if (gateBootSnapshot) {
      const snapshot = gateBootSnapshot;
      gateBootSnapshot = null;
      return Promise.resolve(snapshot);
    }
    if (gateInflight) return gateInflight;
    gateInflight = request("/api/gate").then((view) => {
      state.gate = {
        known: true,
        allowed: !!view.allowed,
        open: !!view.open,
        preview: !!view.preview,
        status: view.status || null,
      };
      return state.gate;
    }).finally(() => {
      gateInflight = null;
    });
    return gateInflight;
  }

  function gateKnown() { return !!(state.gate && state.gate.known); }
  function gateOpen() { return !!(state.gate && state.gate.open); }
  function gatePreview() { return !!(state.gate && state.gate.preview && !state.gate.open); }
  function gateAllows() { return !!(state.gate && state.gate.allowed); }

  // gateCertification is the one reader of the questions-stage count. A null
  // result means the Hub has not loaded or the stage has no questions; callers
  // must render "unknown", never "certified".
  function gateCertification() {
    const gate = state.hub && state.hub.gate ? state.hub.gate : null;
    if (!gate) return null;
    const total = Math.max(0, Number(gate.total) || 0);
    if (!total) return null;
    const certified = Math.min(Math.max(0, Number(gate.certified) || 0), total);
    return { certified, total };
  }

  // readCertification is the route's one certification read: it waits for the
  // shared Hub snapshot, then returns the questions-stage count. A failed Hub
  // read is unknown (null), never the last snapshot: a route must not paint a
  // certified state it could not confirm.
  async function readCertification() {
    try { await loadHub(); } catch { return null; }
    return gateCertification();
  }

  // readGate refreshes the one permission snapshot for a route render. A
  // failed refresh keeps the last known snapshot and is returned as a settled
  // read: the gate can never reject a screen whose own reads succeeded.
  function readGate() {
    return settleRead(loadGate());
  }

  function gateKey(snapshot) {
    if (!snapshot || !snapshot.known) return "?";
    return (snapshot.open ? "o" : "c") + (snapshot.preview ? "p" : "-") + (snapshot.allowed ? "a" : "-");
  }

  async function loadQuestions(force = false) {
    if (state.questions && !force) return state.questions;
    state.questions = await requestCached("/api/questions", { force }, CACHE_TTL.questions);
    return state.questions;
  }

  async function loadMailboxes(force = false) {
    if (state.mailboxes && !force) return state.mailboxes;
    const view = await requestCached("/api/mailboxes", { force }, CACHE_TTL.mailboxes);
    state.mailboxes = Array.isArray(view.mailboxes) ? view.mailboxes : [];
    return state.mailboxes;
  }

  async function loadHub(force = false) {
    const view = await requestCached("/api/hub", { force }, CACHE_TTL.hub);
    state.hub = view;
    updateLearningChrome();
    return view;
  }

  function loadCompanies(search = "", limit = COMPANIES_LIST_LIMIT) {
    const query = String(search || "").trim();
    return requestCached("/api/companies?limit=" + encodeURIComponent(String(limit)) +
      (query ? "&q=" + encodeURIComponent(query) : ""), {}, CACHE_TTL.companies);
  }

  function companyPath(key, months = COMPANIES_MONTHS, eventDays = COMPANIES_EVENT_DAYS) {
    return "/api/companies/" + encodeURIComponent(String(key)) +
      "?months=" + encodeURIComponent(String(months)) +
      "&event_days=" + encodeURIComponent(String(eventDays));
  }

  function loadCompany(key, months = COMPANIES_MONTHS, eventDays = COMPANIES_EVENT_DAYS) {
    return requestCached(companyPath(key, months, eventDays), {}, CACHE_TTL.companies);
  }

  function priorityPath(filter, limit, offset = 0) {
    let path = "/api/priority?limit=" + String(limit) + "&filter=" + encodeURIComponent(filter || "all");
    // The initial URL and its cache key stay byte-identical; only a real
    // follow-up page names its offset.
    if (offset > 0) path += "&offset=" + String(offset);
    return path;
  }

  function recordPriority(filter, view, limit) {
    if (!view || !Array.isArray(view.items)) return;
    state.priorityTotals.set(filter, Number(view.total) || 0);
    // The page carries every filter's count from the same snapshot, so the
    // chips no longer need one single-row request each.
    if (view.counts && typeof view.counts === "object") {
      for (const [name, value] of Object.entries(view.counts)) {
        state.priorityTotals.set(name, Number(value) || 0);
      }
    }
    if (limit <= 1) return;
    // Every cached page is a first page. A follow-up page belongs to the
    // mounted list that asked for it, not to a fresh render of the route.
    const offset = Math.max(0, Number(view.offset) || 0);
    if (offset > 0) return;
    const current = state.priorityPages.get(filter);
    // A first-page revalidation must never shrink a list that already
    // appended later pages: the mounted list owns those rows.
    if (current && current.items.length > limit) return;
    if (!current || limit >= current.limit || Date.now() - current.at > CACHE_TTL.priority) {
      state.priorityPages.set(filter, {
        items: view.items,
        total: Number(view.total) || 0,
        offset,
        available: Number(view.available) || view.items.length,
        has_more: !!view.has_more,
        generated_at: view.generated_at || "",
        preview: !!view.preview,
        limit,
        at: Date.now(),
      });
    }
  }

  async function loadAgenda(hours = state.agendaHours, force = false) {
    // No hours means the configured default: the server owns
    // agenda.default_hours and reports the applied window as horizon_hours.
    const path = hours === null || hours === undefined || hours === ""
      ? "/api/agenda"
      : "/api/agenda?hours=" + encodeURIComponent(String(hours));
    return requestCached(path, { force }, CACHE_TTL.agenda);
  }

  async function loadPriority(filter = "all", limit = PRIORITY_PAGE_SIZE, force = false) {
    const view = await requestCached(priorityPath(filter, limit), { force }, CACHE_TTL.priority);
    recordPriority(filter, view, limit);
    return view;
  }

  // One conversation page. The API owns the opaque `before` cursor; an empty
  // state means All and an empty q means no search.
  function loadConversations(limit = CONVERSATIONS_PAGE_SIZE, before = "", state = "", q = "") {
    return requestCached(conversationPath(limit, before, state, q), {}, CACHE_TTL.conversations);
  }

  function cachedPriority(filter, minItems = 0) {
    const page = state.priorityPages.get(filter);
    if (!page) return null;
    // `at` is the snapshot time recordPriority stored. An accumulated list is
    // still one page model, and a page older than the read TTL is a cold read,
    // not a ranking worth painting while the screen revalidates.
    if (Date.now() - (Number(page.at) || 0) > CACHE_TTL.priority) return null;
    if (minItems && page.items.length < minItems) return null;
    return page;
  }

  function cacheAskView(view) {
    if (!view) return;
    if (view.id !== undefined && view.id !== null) state.askViews.set(String(view.id), view);
    if (view.question) state.askViews.set("q:" + view.question, view);
    // A long session asks many questions; the answer history itself is
    // bounded to 20, so the view cache needs no larger bound.
    while (state.askViews.size > 40) {
      const oldest = state.askViews.keys().next().value;
      if (oldest === undefined) break;
      state.askViews.delete(oldest);
    }
  }

  function cachedAskView(id) {
    return state.askViews.get(String(id)) || null;
  }

  async function loadAskHistory(force = false) {
    const view = await requestCached("/api/ask/history?limit=20", { force }, CACHE_TTL.askHistory);
    const items = view && Array.isArray(view.items) ? view.items : [];
    state.askHistory = items;
    loadAskHistoryError = null;
    // The rail is a four-field projection. A full view is cached only when
    // the owner submits an answer or opens a history row.
    return items;
  }

  async function loadAskHistoryItem(id) {
    const view = await requestCached("/api/ask/history/" + encodeURIComponent(String(id)), {}, CACHE_TTL.askHistory);
    cacheAskView(view);
    return view;
  }

  function noteAskAnswer(view) {
    cacheAskView(view);
    const items = Array.isArray(state.askHistory) ? state.askHistory.slice() : [];
    items.unshift(view);
    const seen = new Set();
    state.askHistory = items.filter((item) => {
      const key = item && item.id !== undefined && item.id !== null ? "id:" + item.id : "q:" + (item && item.question);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    }).slice(0, 20);
    invalidateCache(["/api/ask/history"]);
  }

  // A pre-gate read names the surface that offered the message: the label
  // queue page or the stored answer. The API checks that binding, so a bare
  // flag unlocks nothing. Every other read carries no query.
  function messagePath(id, options = {}) {
    if (options.forAnswer) return "/api/messages/" + encodeURIComponent(id) + "?for=answer";
    if (!options.forLabel) return "/api/messages/" + encodeURIComponent(id);
    const query = ["for=label"];
    if (options.question) query.push("question=" + encodeURIComponent(options.question));
    if (options.limit) query.push("limit=" + encodeURIComponent(String(options.limit)));
    if (options.review) query.push("review=true");
    return "/api/messages/" + encodeURIComponent(id) + "?" + query.join("&");
  }

  function loadMessage(id, options = {}) {
    return requestCached(messagePath(id, options), { force: !!options.force }, CACHE_TTL.message);
  }

  // One conversation page. `before` is the exclusive older cursor and is
  // always a message row id; zero starts at the latest page.
  function loadThread(conversationID, before = 0, at = 0) {
    const id = String(conversationID || "");
    if (!id) return Promise.resolve(null);
    const query = [];
    if (before > 0) query.push("before=" + encodeURIComponent(String(before)));
    if (at > 0) query.push("at=" + encodeURIComponent(String(at)));
    return requestCached(
      "/api/threads/" + encodeURIComponent(id) + (query.length ? "?" + query.join("&") : ""),
      {}, CACHE_TTL.thread);
  }

  function loadTriageDetail(id, force = false) {
    return requestCached("/api/triage/" + encodeURIComponent(id), { force }, CACHE_TTL.triageDetail);
  }

  function prefetchPriorityDetails(items) {
    const list = Array.isArray(items) ? items : [];
    for (const item of list.slice(0, PRIORITY_PREFETCH_SIZE)) {
      loadMessage(item.message_id).catch(() => {});
      loadTriageDetail(item.message_id).catch(() => {});
    }
  }

  function prefetchCore() {
    if (!state.token || state.shuttingDown) return;
    const run = () => {
      if (!state.token || state.shuttingDown) return;
      Promise.allSettled([
        loadLearning().catch(() => {}),
        loadStatus().catch(() => {}),
        loadQuestions().catch(() => {}),
        loadMailboxes().catch(() => {}),
        loadHub().catch(() => {}),
      ]).then(() => {
        // The gate keeps Home's ranked signals locked. Prefetching them while
        // it is closed would only produce refused requests and console noise,
        // so that surface waits until it can answer.
        if (!gateAllows()) return;
        // Home is the one primary destination warmed ahead of a visit: its
        // ranked signals and its agenda are small pages the landing screen
        // paints from. Triage (the 50-row page), the conversation digest,
        // company profiles, Brief, Ask history and message bodies belong to
        // screens the owner has not opened; they are read on first visit
        // instead of being shipped during boot.
        Promise.allSettled([
          loadPriority("all", PRIORITY_HOME_SIZE).catch(() => {}),
          loadAgenda(state.agendaHours).catch(() => {}),
        ]);
      });
    };
    if (typeof window.requestIdleCallback === "function") window.requestIdleCallback(run, { timeout: 1800 });
    else window.setTimeout(run, 320);
  }

