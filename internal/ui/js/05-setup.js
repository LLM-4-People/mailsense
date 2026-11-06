  // ---------------------------------------------------------------- setup
  //
  // The first-run screen is a short, resumable sequence over the existing
  // owners: the settings store (language and the completion marker), the zone
  // route, the mailbox route, and the provider and runtime screens. It never
  // writes a second configuration document.
  //
  // One overlay carries a basic path and an optional advanced path. Both read
  // the same routes Settings reads; the advanced path only adds role, runtime,
  // storage and network facts, plus the two writes Settings also performs
  // (provider test and runtime install).

  const SETUP_STEPS = ["welcome", "mailbox", "model", "finish"];
  const SETUP_MODES = ["basic", "advanced"];
  const SETUP_PROGRESS_KEY = "mailsense.setup.step";
  const SETUP_MODE_KEY = "mailsense.setup.mode";

  // SETUP_SETTING_KEYS is the one projection the wizard reads from the schema.
  // Every row here is read-only in the overlay; the link under each group opens
  // the Settings tab that owns the write.
  const SETUP_SETTING_KEYS = [
    "embed.provider",
    "sync.auto",
    "sync.details.enabled",
    "sync.removal.auto_apply_after",
    "log.file.enabled",
    "log.file.max_files",
    "log.file.max_size_mb",
    "log.level",
    "http.proxy_url",
    "http.timeout.total",
  ];

  // SETUP_READS is the one pre-fetch table. Every read starts when the overlay
  // opens and settles on its own: one slow or failed read never holds another,
  // and no step issues a read of its own.
  const SETUP_READS = {
    status: () => request("/api/status"),
    sources: () => request("/api/sources"),
    mailboxes: () => request("/api/mailboxes"),
    providers: () => request("/api/providers"),
    sidecar: () => request("/api/sidecar"),
    learning: () => request("/api/learning"),
    removals: () => request("/api/held-removals"),
    settings: () => request("/api/settings?keys=" + SETUP_SETTING_KEYS.map(encodeURIComponent).join(",")),
  };

  let setupJobToken = 0;

  async function loadSetup() {
    return request("/api/setup");
  }

  // maybeOpenSetup runs once after the launch reads. A failed read is not a
  // reason to block the shell: Sources and Settings stay reachable.
  async function maybeOpenSetup() {
    let view = null;
    try {
      view = await loadSetup();
    } catch {
      state.setup = null;
      return false;
    }
    state.setup = view;
    if (!view || !view.needed) {
      clearSetupProgress();
      return false;
    }
    state.setupStep = savedSetupStep();
    state.setupMode = savedSetupMode();
    state.setupDraft = {
      language: view.language || "auto",
      zone: view.time_zone || browserZone(),
      keys: null,
      source: "",
    };
    state.setupNotNow = false;
    state.setupActionError = "";
    state.setupBusy = false;
    state.setupCompleting = false;
    state.setupPaintedStep = "";
    openSetup();
    return true;
  }

  function browserZone() {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
    } catch {
      return "";
    }
  }

  function browserLanguage() {
    try {
      return navigator.language || "";
    } catch {
      return "";
    }
  }

  // setupStored and its two writers keep the wizard resumable across a reload.
  // They store progress only: the completion marker stays ui.setup.done.
  function setupStored(key) {
    try {
      return window.sessionStorage.getItem(key) || "";
    } catch {
      return "";
    }
  }

  function setupRemember(key, value) {
    try {
      window.sessionStorage.setItem(key, value);
    } catch { /* storage may be unavailable */ }
  }

  function setupForget(key) {
    try {
      window.sessionStorage.removeItem(key);
    } catch { /* storage may be unavailable */ }
  }

  function savedSetupStep() {
    const saved = setupStored(SETUP_PROGRESS_KEY);
    return SETUP_STEPS.includes(saved) ? saved : "welcome";
  }

  function savedSetupMode() {
    const saved = setupStored(SETUP_MODE_KEY);
    return SETUP_MODES.includes(saved) ? saved : "basic";
  }

  function clearSetupProgress() {
    setupForget(SETUP_PROGRESS_KEY);
    setupForget(SETUP_MODE_KEY);
  }

  // ------------------------------------------------------------ pre-fetch

  function setupRead(name) {
    return state.setupPrefetch ? state.setupPrefetch[name] : null;
  }

  function setupReadValue(name) {
    const read = setupRead(name);
    return read && read.state === "ready" ? read.value : null;
  }

  function setupReadFailed(name) {
    const read = setupRead(name);
    return !!(read && read.state === "error");
  }

  function readSetup(name) {
    const read = setupRead(name);
    if (!read) return;
    read.state = "loading";
    read.value = null;
    read.error = null;
    paintSetupLive(name);
    paintSetupProblems(name);
    SETUP_READS[name]().then((value) => {
      if (setupRead(name) !== read) return;
      read.state = "ready";
      read.value = value;
      paintSetupLive(name);
      paintSetupProblems(name);
    }).catch((error) => {
      if (setupRead(name) !== read) return;
      read.state = "error";
      read.error = error;
      paintSetupLive(name);
      paintSetupProblems(name);
    });
  }

  function retrySetupRead(name) {
    readSetup(name);
  }

  function startSetupReads() {
    const reads = {};
    state.setupPrefetch = reads;
    for (const name of Object.keys(SETUP_READS)) reads[name] = { state: "loading", value: null, error: null };
    for (const name of Object.keys(SETUP_READS)) readSetup(name);
  }

  function setSetupReadValue(name, value) {
    const read = setupRead(name);
    if (!read) return;
    read.state = "ready";
    read.value = value;
    read.error = null;
    paintSetupLive(name);
    paintSetupProblems(name);
  }

  // setupLive is one fact that repaints itself when its reads settle. The
  // renderer receives the values in the declared order. While a read is in
  // flight the fact shows a skeleton; a failed read says it is unavailable,
  // and the step's problem list carries the Retry action.
  function setupLive(reads, render, tag = "span") {
    const list = Array.isArray(reads) ? reads : [reads];
    const node = h(tag, { class: "setup-value" + (tag === "span" ? "" : " setup-value-block") });
    node.dataset.setupLive = "1";
    node.__setupReads = list;
    node.__setupRender = render;
    paintSetupLiveNode(node);
    return node;
  }

  function paintSetupLiveNode(node) {
    const reads = node.__setupReads || [];
    const values = reads.map((name) => setupRead(name));
    node.replaceChildren();
    if (values.some((read) => !read || read.state === "loading")) {
      node.setAttribute("aria-busy", "true");
      node.append(h("span", { class: "skeleton-line w-60 setup-skeleton" }));
      return;
    }
    node.removeAttribute("aria-busy");
    if (values.some((read) => read.state === "error")) {
      node.append(h("span", { class: "fine", text: t("setup.not_available") }));
      return;
    }
    const rendered = node.__setupRender(...values.map((read) => read.value));
    if (Array.isArray(rendered)) node.append(...rendered.filter(Boolean));
    else if (rendered !== undefined && rendered !== null) node.append(rendered);
  }

  function paintSetupLive(readName) {
    const root = document.getElementById("setup-root");
    if (!root) return;
    for (const node of root.querySelectorAll("[data-setup-live]")) {
      const reads = node.__setupReads || [];
      if (readName && !reads.includes(readName)) continue;
      replaceSetupNode(node, () => paintSetupLiveNode(node));
    }
  }

  // setupProblems is the one inline failure list of a step. Each failed read
  // names what is missing and offers its own Retry; the rest of the step keeps
  // the facts that did land.
  function setupProblems(reads) {
    const node = h("div", { class: "setup-problems" });
    node.dataset.setupProblems = "1";
    node.__setupReads = reads;
    paintSetupProblemsNode(node);
    return node;
  }

  function paintSetupProblemsNode(node) {
    const reads = node.__setupReads || [];
    const failed = reads.filter((name) => setupReadFailed(name));
    node.replaceChildren();
    for (const name of failed) {
      const read = setupRead(name);
      node.append(h("div", { class: "setup-problem" },
        h("p", { class: "fine", text: t("setup.read." + name) }),
        retryBanner(apiFailure(read.error).message, () => retrySetupRead(name))));
    }
  }

  function paintSetupProblems(readName) {
    const root = document.getElementById("setup-root");
    if (!root) return;
    for (const node of root.querySelectorAll("[data-setup-problems]")) {
      const reads = node.__setupReads || [];
      if (readName && !reads.includes(readName)) continue;
      replaceSetupNode(node, () => paintSetupProblemsNode(node));
    }
  }

  // replaceSetupNode keeps keyboard focus and the caret where they were when a
  // background read lands. Without it a late pre-fetch would take the caret
  // out of the zone field or a settings link.
  function replaceSetupNode(node, paint) {
    const active = document.activeElement;
    const inside = !!(active && node.contains(active));
    const id = inside ? active.id : "";
    let start = null, end = null;
    if (inside && typeof active.selectionStart === "number" && typeof active.selectionEnd === "number") {
      start = active.selectionStart;
      end = active.selectionEnd;
    }
    paint();
    if (!inside) return;
    const next = id ? document.getElementById(id) : null;
    if (!next || !node.contains(next) || typeof next.focus !== "function") {
      focusSetupHeading();
      return;
    }
    next.focus();
    if (start !== null && typeof next.setSelectionRange === "function") {
      try { next.setSelectionRange(start, end); } catch { /* not a text field */ }
    }
  }

  // ------------------------------------------------------------ shell

  function setupStepHeading() {
    return h("h2", {
      id: "setup-step-title",
      class: "setup-step-title",
      tabIndex: -1,
      text: t("setup.step." + state.setupStep),
    });
  }

  function setupStepListNode() {
    return h("ol", { class: "setup-steps", "aria-label": t("setup.steps_aria") },
      SETUP_STEPS.map((name, index) => h("li", {
        class: "setup-step" + (name === state.setupStep ? " current" : "") + (setupStepDone(name) ? " done" : ""),
        "aria-current": name === state.setupStep ? "step" : null,
      },
      h("span", { class: "setup-step-number", text: String(index + 1) }),
      h("span", { class: "setup-step-label", text: t("setup.step." + name) }))));
  }

  function setupStepDone(name) {
    return SETUP_STEPS.indexOf(name) < SETUP_STEPS.indexOf(state.setupStep);
  }

  function setupModeSwitch() {
    const mode = state.setupMode === "advanced" ? "advanced" : "basic";
    const setMode = (next) => {
      if (state.setupMode === next) return;
      state.setupMode = next;
      setupRemember(SETUP_MODE_KEY, next);
      paintSetup();
    };
    const basic = button(t("setup.mode.basic"), () => setMode("basic"), { pressed: mode === "basic", small: true }); basic.id = "setup-mode-basic";
    const advanced = button(t("setup.mode.advanced"), () => setMode("advanced"), { pressed: mode === "advanced", small: true }); advanced.id = "setup-mode-advanced";
    return h("div", { class: "setup-mode" },
      h("div", { class: "segmented", role: "group", "aria-label": t("setup.mode_aria") }, basic, advanced),
      h("p", { class: "fine", text: t(mode === "advanced" ? "setup.mode.advanced_hint" : "setup.mode.basic_hint") }));
  }

  function setupShell(body, actions) {
    return h("div", { class: "setup-card surface" },
      h("div", { class: "setup-head" },
        h("p", { class: "eyebrow", text: t("setup.eyebrow") }),
        h("h1", { id: "setup-title", text: t("setup.title") }),
        h("p", { class: "lede", text: t("setup.intro") })),
      setupStepListNode(),
      h("div", { class: "setup-body" }, body, setupActionErrorNode()),
      h("div", { class: "setup-actions" }, actions));
  }

  function setupActionErrorNode() {
    return h("div", {
      id: "setup-action-error",
      class: "field-error setup-action-error",
      role: "alert",
      hidden: !state.setupActionError,
      text: state.setupActionError || "",
    });
  }

  function showSetupActionError(message) {
    state.setupActionError = message || "";
    const node = document.getElementById("setup-action-error");
    if (!node) return;
    node.textContent = state.setupActionError;
    node.hidden = !state.setupActionError;
  }

  function openSetup() {
    const root = h("div", {
      id: "setup-root",
      class: "setup-root",
      role: "dialog",
      "aria-modal": "true",
      "aria-labelledby": "setup-title",
    });
    document.body.append(root);
    if (dom.app) dom.app.hidden = true;
    document.addEventListener("keydown", onSetupKeydown);
    document.addEventListener("focusin", onSetupFocusIn);
    paintSetup();
    startSetupReads();
  }

  function closeSetup() {
    stopSetupJobWatch();
    const root = document.getElementById("setup-root");
    if (root) root.remove();
    document.removeEventListener("keydown", onSetupKeydown);
    document.removeEventListener("focusin", onSetupFocusIn);
    if (dom.app) dom.app.hidden = false;
    state.setup = null;
    state.setupPrefetch = null;
    state.setupDraft = null;
    state.setupStep = "welcome";
    state.setupPaintedStep = "";
    state.setupNotNow = false;
    state.setupActionError = "";
    window.setTimeout(() => maybeOpenWhatsNew(), 0); // let the next route mount first
  }

  function focusSetupHeading() {
    const heading = document.getElementById("setup-step-title");
    if (heading) heading.focus();
  }

  // setupFocusable is the dialog's tab order. The hidden shell is outside the
  // root, so a Tab or a stray focus can never land in it.
  function setupFocusable(root) {
    return Array.from(root.querySelectorAll(
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    )).filter((node) => node.getClientRects().length > 0);
  }

  function onSetupKeydown(event) {
    // A native dialog is a top-layer surface of its own: it owns Tab and
    // Escape until it closes.
    if (state.modalOpen) return;
    if (event.key === "Escape") {
      // While a setup write is in flight the overlay stays open: closing would
      // hide a failure the owner still has to see.
      if (state.setupBusy || state.setupCompleting) return;
      event.preventDefault();
      const note = setupPauseNote();
      closeSetup();
      toast(t("setup.paused_title"), "info", note);
      return;
    }
    if (event.key !== "Tab") return;
    const root = document.getElementById("setup-root");
    if (!root) return;
    const items = setupFocusable(root);
    if (!items.length) return;
    const first = items[0];
    const last = items[items.length - 1];
    const heading = document.getElementById("setup-step-title");
    if (!root.contains(document.activeElement) || document.activeElement === heading) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
      return;
    }
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function onSetupFocusIn(event) {
    // The confirm dialog is a native top-layer dialog: its focus is outside
    // the overlay on purpose.
    if (state.modalOpen) return;
    const root = document.getElementById("setup-root");
    if (!root || event.target === root) return;
    if (!root.contains(event.target)) focusSetupHeading();
  }

  function paintSetup() {
    const root = document.getElementById("setup-root");
    if (!root) return;
    const view = state.setup || {};
    const step = state.setupStep === "mailbox" ? mailboxStep()
      : state.setupStep === "model" ? modelStep()
        : state.setupStep === "finish" ? finishStep()
          : welcomeStep(view);
    const active = document.activeElement;
    const activeId = active && active.id ? active.id : "";
    const scroll = root.scrollTop;
    root.replaceChildren(setupShell(step.body, step.actions));
    root.scrollTop = scroll;
    if (state.setupPaintedStep !== state.setupStep) {
      state.setupPaintedStep = state.setupStep;
      focusSetupHeading();
      return;
    }
    const next = activeId ? document.getElementById(activeId) : null;
    if (next && root.contains(next) && typeof next.focus === "function") next.focus();
  }

  // ------------------------------------------------------------ welcome

  function setupFact(label, value) {
    return h("div", null,
      h("dt", { text: label }),
      h("dd", null, value instanceof Node ? value : text(value)));
  }

  function setupFactsNode(view) {
    const detected = browserZone();
    const language = browserLanguage();
    return h("dl", { class: "setup-facts" },
      setupFact(t("setup.welcome.data_dir"), h("span", { class: "mono", text: view.data_dir || t("common.dash") })),
      setupFact(t("setup.welcome.db_size"), view.database_size_known
        ? h("span", { class: "mono", text: formatBytes(view.database_bytes) })
        : h("span", { class: "fine", text: t("setup.advanced.storage.db_unknown") })),
      setupFact(t("setup.welcome.zone"), h("span", { text: view.time_zone || detected || t("common.dash") })),
      setupFact(t("setup.welcome.language"), h("span", { text: language || t("common.dash") })),
      setupFact(t("setup.welcome.stores_label"), setupLive(["mailboxes"], (mailboxes) => {
        const boxes = Array.isArray(mailboxes && mailboxes.mailboxes) ? mailboxes.mailboxes : [];
        if (!boxes.length) return h("span", { class: "fine", text: t("setup.welcome.no_stores") });
        const selected = boxes.filter((box) => box.selected).length;
        const other = boxes.length - selected;
        const parts = [t("setup.welcome.stores_value", { selected, total: boxes.length })];
        if (other > 0) parts.push(t("setup.welcome.other_stores", { count: other }));
        return h("span", { text: metaLine(parts) });
      })));
  }

  function welcomeStep(view) {
    const language = h("select", { class: "select", id: "setup-language" },
      ["auto", "en", "fr"].map((value) => h("option", {
        value,
        selected: value === state.setupDraft.language,
        text: t("setup.language." + value),
      })));
    language.addEventListener("change", () => { state.setupDraft.language = language.value; });
    const zone = h("input", {
      class: "input",
      id: "setup-zone",
      type: "text",
      value: state.setupDraft.zone,
      autocomplete: "off",
      spellcheck: "false",
    });
    zone.addEventListener("input", () => { state.setupDraft.zone = zone.value; });
    const detected = browserZone();
    const body = h("div", { class: "setup-form" },
      setupStepHeading(),
      h("p", { class: "lede", text: t("setup.welcome.body") }),
      setupModeSwitch(),
      setupFactsNode(view),
      field(t("setup.welcome.language"), language),
      field(t("setup.welcome.zone"), zone, detected
        ? t("setup.welcome.zone_detected", { zone: detected })
        : t("setup.welcome.zone_none")),
      setupLive(["mailboxes"], (mailboxes) => {
        const boxes = Array.isArray(mailboxes && mailboxes.mailboxes) ? mailboxes.mailboxes : [];
        return h("p", { class: "fine", text: boxes.length ? t("setup.welcome.found") : t("setup.welcome.none") });
      }),
      setupProblems(["status", "mailboxes"]),
      state.setupMode === "advanced" ? advancedSections() : null);
    const actions = [
      button(t("setup.continue"), (event) => saveWelcome(event.currentTarget), { kind: "primary" }),
    ];
    return { body, actions };
  }

  async function saveWelcome(trigger) {
    const ok = await runSetupAction(trigger, t("setup.saving"), async () => {
      const view = state.setup || {};
      const zone = String(state.setupDraft.zone || "").trim();
      if (zone && zone !== String(view.time_zone || "")) {
        await request("/api/learning/zone", { method: "POST", body: { zone } });
      }
      const language = state.setupDraft.language;
      if (language && language !== view.language) {
        await request("/api/settings/ui.language", { method: "PUT", body: { value: language } });
        // Apply the choice before the next step paints. The overlay is not a
        // route, so the live language helper would rebuild the screen behind
        // it; this reuses the one resolver and then lets the step repaint.
        await resolveAndApply(language);
      }
    });
    if (ok) enterSetupStep("mailbox");
  }

  // saveSetupProgress keeps the furthest step the owner reached. Going back
  // to change a choice does not lower it, so a reload never repeats a step
  // that is already done.
  function saveSetupProgress(name) {
    const current = savedSetupStep();
    setupRemember(SETUP_PROGRESS_KEY, SETUP_STEPS.indexOf(name) > SETUP_STEPS.indexOf(current) ? name : current);
  }

  function enterSetupStep(name) {
    state.setupStep = name; state.setupActionError = "";
    saveSetupProgress(name);
    paintSetup();
  }

  // ------------------------------------------------------------ mailbox

  function setupBoxes() {
    const value = setupReadValue("mailboxes");
    return value && Array.isArray(value.mailboxes) ? value.mailboxes : [];
  }

  // setupMailboxKeys is the draft selection when the owner touched it, and the
  // server selection otherwise. A failed or late read therefore cannot erase a
  // choice the owner already made.
  function setupMailboxKeys(boxes) {
    if (Array.isArray(state.setupDraft.keys)) return state.setupDraft.keys;
    return (Array.isArray(boxes) ? boxes : []).filter((box) => box.selected).map((box) => box.key);
  }

  function setupItems(boxes) {
    let total = 0;
    for (const box of boxes) {
      const items = Number(box && box.items);
      if (!Number.isFinite(items) || items < 0) return null;
      total += items;
    }
    return total;
  }

  function setupMailboxSummary(value) {
    const boxes = Array.isArray(value && value.mailboxes) ? value.mailboxes : [];
    const keys = setupMailboxKeys(boxes);
    const selected = boxes.filter((box) => keys.includes(box.key));
    if (keys.length === 0) {
      return h("p", { class: "fine", text: t("setup.mailbox.none_selected") });
    }
    const items = setupItems(selected);
    return h("p", { class: "fine", text: items === null
      ? t("setup.mailbox.summary_unknown", { stores: selected.length })
      : t("setup.mailbox.summary", { stores: selected.length, messages: formatNumber(items) }) });
  }

  function setupMailboxSummaryNode() {
    const node = setupLive(["mailboxes"], setupMailboxSummary);
    node.id = "setup-mailbox-summary";
    return node;
  }

  function updateSetupMailboxSummary() {
    const node = document.getElementById("setup-mailbox-summary");
    if (node) paintSetupLiveNode(node);
  }

  function setupMailboxList(value) {
    const boxes = Array.isArray(value && value.mailboxes) ? value.mailboxes : [];
    if (!boxes.length) return null;
    const keys = setupMailboxKeys(boxes);
    return h("div", { class: "setup-mailbox-list" }, boxes.map((box) => {
      const input = h("input", {
        type: "checkbox",
        name: "setup-mailbox",
        value: box.key,
        checked: keys.includes(box.key),
      });
      const items = Number(box.items);
      input.addEventListener("change", () => {
        const current = setupMailboxKeys(boxes).slice();
        const at = current.indexOf(box.key);
        if (input.checked && at < 0) current.push(box.key);
        if (!input.checked && at >= 0) current.splice(at, 1);
        state.setupDraft.keys = current;
        updateSetupMailboxSummary();
      });
      return h("label", { class: "setup-mailbox" },
        input,
        h("span", { class: "setup-mailbox-name", text: box.name || box.key }),
        h("span", { class: "fine mono", text: box.key }),
        h("span", { class: "fine", text: Number.isFinite(items) && items >= 0
          ? t("setup.mailbox.items", { count: items })
          : "" }));
    }));
  }

  function mailboxStep() {
    const body = h("div", { class: "setup-form" },
      setupStepHeading(),
      h("p", { text: t("setup.mailbox.body") }),
      state.setupDraft.skippedMailbox ? h("p", { class: "fine setup-inline-note" },
        h("span", { text: t("setup.mailbox.skipped") }),
        button(t("setup.open_sources"), () => leaveSetup("sources"), { small: true })) : null,
      setupLive(["sources"], setupSourceSelect, "div"),
      setupLive(["sources"], setupSourceActions, "div"),
      setupLive(["sources", "mailboxes"], setupMailboxHint),
      setupLive(["mailboxes"], setupMailboxList),
      setupMailboxSummaryNode(),
      setupProblems(["status", "sources", "mailboxes"]));
    const actions = [
      button(t("setup.back"), () => enterSetupStep("welcome")),
      button(t("setup.skip"), () => {
        state.setupDraft.skippedMailbox = true;
        enterSetupStep("model");
      }),
      button(t("setup.continue"), (event) => saveMailbox(event.currentTarget), { kind: "primary" }),
    ];
    return { body, actions };
  }

  async function discoverMailboxes(trigger) {
    const sources = setupSources(setupReadValue("sources"));
    const source = setupSelectedSource(sources);
    if (!source || source.available === false) {
      showSetupActionError(source
        ? t("setup.mailbox.source_unavailable", { name: setupSourceName(source), reason: setupSourceReason(source) })
        : t("setup.mailbox.no_source"));
      return;
    }
    await runSetupAction(trigger, t("setup.mailbox.discovering"), async () => {
      const result = await request("/api/mailboxes/discover", {
        method: "POST",
        body: { source: source.id },
      });
      state.setupDraft.skippedMailbox = false;
      setSetupReadValue("mailboxes", result);
    });
  }

  async function saveMailbox(trigger) {
    const boxes = setupBoxes();
    const keys = setupMailboxKeys(boxes);
    if (boxes.length > 0 && keys.length === 0) {
      showSetupActionError(t("setup.mailbox.required_body"));
      return;
    }
    const ok = await runSetupAction(trigger, t("setup.saving"), async () => {
      if (keys.length > 0) {
        const result = await request("/api/mailboxes/pick", { method: "POST", body: { keys } });
        setSetupReadValue("mailboxes", result);
        invalidateCache(["/api/mailboxes", "/api/status", "/api/learning", "/api/gate", "/api/hub"]);
      }
      state.setupDraft.skippedMailbox = false;
    });
    if (ok) enterSetupStep("model");
  }

  // ------------------------------------------------------------ model

  function setupProviderFacts() {
    const view = setupReadValue("providers");
    if (!view) return null;
    applyProviderCatalog(view);
    const records = providerListFrom(view);
    const roles = orderedProviderRoles();
    const decider = roles.find((role) => role.setting === "triage.decider") || null;
    return { view, records, roles, decider };
  }

  function setupRuntimeSnapshot() {
    const settings = setupReadValue("settings");
    const providers = setupReadValue("providers");
    const sidecar = setupReadValue("sidecar");
    if (!settings || !providers || !sidecar) return null;
    return setupRuntimeSnapshotFrom(sidecar, settings, providers);
  }

  function setupRuntimeSnapshotFrom(sidecar, settings, providers) {
    applyProviderCatalog(providers);
    const choice = embedderChoice(settings, providers);
    const run = runtimeStatus(sidecar, choice);
    const build = runtimeBuild(sidecar, run);
    return {
      choice,
      run,
      build,
      model: runtimeModel(run, choice),
      state: runtimeState(run, build, choice),
    };
  }

  function setupRuntimeSentence() {
    const snapshot = setupRuntimeSnapshot();
    if (!snapshot) return h("span", { class: "fine", text: t("setup.not_available") });
    if (!snapshot.choice || !snapshot.choice.local) return t("setup.model.runtime_remote");
    if (!snapshot.build) return t("settings.runtime.no_builds");
    if (snapshot.build.installed) return t("setup.model.runtime_ready");
    return t("setup.model.runtime_missing");
  }

  function setupDeciderSentence(facts) {
    if (!facts) return h("span", { class: "fine", text: t("setup.not_available") });
    if (!facts.records.length) return t("setup.model.none");
    if (!facts.decider || !facts.decider.configured) return t("setup.model.needs_decider");
    if (facts.decider.available === false) return t("setup.model.needs_decider");
    return t("setup.model.ready", { provider: setupProviderName(facts.view, facts.decider.provider) });
  }

  function modelStep() {
    const body = h("div", { class: "setup-form" },
      setupStepHeading(),
      h("p", { text: t("setup.model.body") }),
      state.setupDraft.skippedMailbox ? h("p", { class: "fine setup-inline-note" },
        h("span", { text: t("setup.mailbox.skipped") }),
        button(t("setup.open_sources"), () => leaveSetup("sources"), { small: true })) : null,
      setupLive(["providers"], (providers) => {
        const facts = setupProviderFacts();
        if (!facts) return h("p", { class: "fine", text: t("setup.model.none") });
        return h("p", { class: "fine" },
          text(t("setup.model.provider_count", { count: facts.records.length }) + " " + setupDeciderSentence(facts)));
      }),
      h("ul", { class: "setup-list" },
        h("li", null,
          h("strong", { text: t("setup.model.local") }),
          h("span", null, setupLive(["sidecar", "settings", "providers"], setupRuntimeSentence))),
        h("li", null,
          h("strong", { text: t("setup.model.remote") }),
          h("span", { text: t("setup.model.remote_body") }))),
      h("p", { class: "fine", text: t("setup.model.skip") }),
      setupProblems(["providers", "sidecar", "settings"]));
    const actions = [
      button(t("setup.back"), () => enterSetupStep("mailbox")),
      button(t("setup.model.providers"), () => leaveSetup("settings", { tab: "providers" }), { small: true }),
      button(t("setup.continue"), () => enterSetupStep("finish"), { kind: "primary" }),
    ];
    return { body, actions };
  }

  // ------------------------------------------------------------ advanced

  function setupProviderName(view, id) {
    const record = providerListFrom(view).find((entry) => entry && entry.id === id);
    return (record && (record.label || record.id)) || id || t("common.dash");
  }

  function setupRolePill(role) {
    if (!role || !role.configured) return pill(t("setup.model.role_missing"), "warn");
    if (role.available === false) return pill(t("setup.model.role_unavailable"), "blocked");
    return pill(t("setup.model.role_ready"), "ok");
  }

  function setupAdvancedProviders() {
    const facts = setupProviderFacts();
    if (!facts) return h("p", { class: "fine", text: t("setup.not_available") });
    return fragment(
      facts.roles.length
        ? h("dl", { class: "setup-roles" }, facts.roles.map((role) => h("div", null,
          h("dt", { text: providerRoleLabel(role) }),
          h("dd", { class: "setup-role-value" },
            h("span", { text: role.provider ? setupProviderName(facts.view, role.provider) : t("settings.providers.role.unconfigured") }),
            setupRolePill(role),
            role.detail ? h("span", { class: "fine", text: boundedReason(role.detail) }) : null))))
        : h("p", { class: "fine", text: t("setup.advanced.providers.no_roles") }),
      facts.records.length
        ? h("div", { class: "setup-provider-list" }, facts.records.map((record) => {
          const resultHost = h("div", { class: "provider-card-result", "aria-live": "polite" });
          return h("div", { class: "setup-provider" },
            h("div", { class: "setup-provider-head" },
              h("strong", { text: record.label || record.id }),
              h("span", { class: "fine mono", text: record.protocol || "" }),
              record.key_present
                ? badge(t("settings.providers.key_present"), "ok")
                : badge(t("settings.providers.no_key"), "warn")),
            h("div", { class: "button-row" },
              button(t("settings.providers.test"), (event) => testSetupProvider(record, resultHost, event.currentTarget), { small: true })),
            resultHost);
        }))
        : h("p", { class: "fine", text: t("setup.advanced.providers.empty") }),
      h("p", { class: "fine", text: t("setup.advanced.providers.test_hint") }),
      h("div", { class: "button-row" },
        button(t("setup.advanced.providers.open"), () => leaveSetup("settings", { tab: "providers" }), { small: true })));
  }

  async function testSetupProvider(record, host, trigger) {
    const name = record.label || record.id || t("settings.providers.title");
    const ok = await confirmDialog(
      t("settings.providers.test_confirm_title", { name }),
      t("settings.providers.test_confirm_body"),
      t("settings.providers.test_confirm_action"));
    if (!ok) return;
    state.setupBusy = true;
    try {
      await withBusy(trigger, t("settings.providers.testing"), async () => {
        const started = performance.now();
        clear(host);
        try {
          const result = await request("/api/providers/" + encodeURIComponent(record.id) + "/test", { method: "POST" });
          const elapsed = Math.round(performance.now() - started);
          const ms = result && result.latency_ms !== undefined ? result.latency_ms : elapsed;
          const duration = formatDuration(Number(ms) * 1e6);
          const passed = !result || result.ok !== false;
          const model = (result && result.model) || record.model || t("common.dash");
          const detail = providerTestDetail(result);
          host.append(h("div", { class: "provider-result" + (passed ? "" : " error") },
            h("div", { class: "setup-result-line" },
              badge(passed ? t("state.ok") : t("settings.providers.test_failed"), passed ? "ok" : "danger"),
              h("span", { text: t(passed ? "settings.providers.test_result_ok" : "settings.providers.test_result_fail", { duration }) })),
            h("div", { class: "fine", text: detail
              ? t("settings.providers.test_detail", { model, detail })
              : t("settings.providers.test_detail_empty", { model }) })));
        } catch (error) {
          const duration = formatDuration(Math.round(performance.now() - started) * 1e6);
          host.append(h("div", { class: "provider-result error" },
            h("div", { class: "setup-result-line" },
              badge(t("settings.providers.test_failed"), "danger"),
              h("span", { text: t("settings.providers.test_result_fail", { duration }) })),
            h("div", { text: apiFailure(error).message })));
        }
      });
    } finally {
      state.setupBusy = false;
    }
  }

  function setupRuntimeView() {
    const snapshot = setupRuntimeSnapshot();
    if (!snapshot) return h("span", { class: "fine", text: t("setup.not_available") });
    const { choice, run, build, state: runtimeRunState } = snapshot;
    if (!choice || !choice.local) {
      return h("div", { class: "setup-runtime" },
        h("p", { class: "fine", text: t("setup.advanced.runtime.no_local") }),
        h("div", { class: "button-row" },
          button(t("setup.open_providers"), () => leaveSetup("settings", { tab: "providers" }), { small: true })));
    }
    if (!build) return h("p", { class: "fine", text: t("settings.runtime.no_builds") });
    if (build.unavailable) {
      return h("p", { class: "fine warn-text", text: t("setup.advanced.runtime.unavailable", {
        reason: tOr("settings.runtime.unavailable." + build.unavailable, build.unavailable),
      }) });
    }
    const installed = !!build.installed;
    const size = installed && build.size_bytes > 0
      ? t("setup.advanced.runtime.size", { size: formatBytes(build.size_bytes) })
      : !installed && build.download_bytes > 0
        ? t("setup.advanced.runtime.download", { size: formatBytes(build.download_bytes) })
        : t("setup.advanced.runtime.size_unknown");
    const busyStates = ["installing", "starting", "tuning", "probing", "canary", "restarting"];
    const busy = busyStates.includes(runtimeRunState.code);
    const running = state.setupInstallJob || busy
      ? h("p", { class: "fine", role: "status", text: t("setup.advanced.runtime.running") })
      : null;
    const progressHost = h("div", { class: "setup-runtime-progress", role: "status", "aria-live": "polite" });
    const install = !installed && !state.setupInstallJob && !busy
      ? button(t("setup.advanced.runtime.install"), (event) => installSetupRuntime(build, snapshot.model, progressHost, event.currentTarget), {
        small: true,
        kind: "primary",
        disabled: !snapshot.model,
      })
      : null;
    return h("div", { class: "setup-runtime" },
      h("div", { class: "setup-runtime-head" },
        h("span", { class: "mono", text: build.runtime || t("settings.runtime.no_runtime") }),
        build.version ? h("span", { class: "fine mono", text: build.version }) : null,
        runtimeStatePill(runtimeRunState)),
      h("p", { class: "fine", text: size }),
      installed ? null : h("p", { class: "fine", text: t("setup.advanced.runtime.auto_note") }),
      snapshot.model ? null : h("p", { class: "fine", text: t("setup.advanced.runtime.install_unavailable") }),
      running,
      install,
      progressHost,
      h("div", { class: "button-row" },
        button(t("setup.advanced.runtime.open"), () => leaveSetup("settings", { tab: "runtime" }), { small: true })));
  }

  async function installSetupRuntime(build, model, host, trigger) {
    if (state.setupInstallJob) return;
    state.setupBusy = true;
    try {
      await withBusy(trigger, t("setup.advanced.runtime.starting"), async () => {
        clear(host);
        try {
          const response = await request("/api/sidecar/install", {
            method: "POST",
            body: { runtime: build.runtime, model, variant: build.variant },
          });
          const job = response && response.job ? response.job : null;
          if (!job || !job.id) {
            host.append(h("p", { class: "fine", text: t("setup.advanced.runtime.started") }));
            return;
          }
          watchSetupJob(job.id, host);
        } catch (error) {
          host.append(h("p", { class: "field-error", text: t("setup.advanced.runtime.failed", { message: apiFailure(error).message }) }));
        }
      });
    } finally {
      state.setupBusy = false;
      if (state.setupInstallJob && trigger.isConnected) trigger.disabled = true;
    }
  }

  function paintSetupJob(host, info) {
    const progress = info && info.progress ? info.progress : {};
    const total = Number(progress.total) || 0, done = Number(progress.done) || 0;
    let sentence = t("setup.advanced.runtime.started"), failed = false;
    if (info && info.status === "succeeded") sentence = t("setup.advanced.runtime.done");
    else if (info && info.status === "failed") {
      failed = true;
      sentence = t("setup.advanced.runtime.failed", { message: jobCauseText(info) || statusLabel(info.status) });
    } else if (info && info.status === "running" && total > 0) {
      sentence = t("setup.advanced.runtime.progress", {
        done: formatNumber(done),
        total: formatNumber(total),
        unit: progress.unit ? measureUnit(progress.unit, total) : "",
      });
    }
    host.replaceChildren(
      h("p", { class: failed ? "field-error" : "fine", text: sentence }),
      total > 0 ? h("progress", {
        max: Math.max(total, 1),
        value: Math.min(done, Math.max(total, 1)),
        "aria-label": t("setup.advanced.runtime.progress_aria"),
      }) : null);
  }

  // watchSetupJob is the one progress reader of the install action. It stops
  // when the overlay closes, and its token makes a stale poll harmless.
  function watchSetupJob(id, host) {
    state.setupInstallJob = id;
    const token = ++setupJobToken;
    const tick = async () => {
      if (token !== setupJobToken || !state.setupPrefetch) return;
      try {
        const info = await request("/api/jobs/" + encodeURIComponent(id));
        if (token !== setupJobToken) return;
        paintSetupJob(host, info);
        if (info && info.status === "running") {
          window.setTimeout(tick, 1000);
          return;
        }
        state.setupInstallJob = "";
        invalidateCache(["/api/sidecar"]);
        retrySetupRead("sidecar");
      } catch (error) {
        if (token !== setupJobToken) return;
        host.replaceChildren(h("p", { class: "field-error", text: t("setup.advanced.runtime.progress_failed", { message: apiFailure(error).message }) }));
        state.setupInstallJob = "";
      }
    };
    tick();
  }

  function stopSetupJobWatch() {
    setupJobToken += 1;
    state.setupInstallJob = "";
  }

  function setupSettingViews(settings, keys) {
    const views = settings && Array.isArray(settings.settings) ? settings.settings : [];
    return keys.map((key) => views.find((view) => view && view.key === key)).filter(Boolean);
  }

  // setupSettingFact renders one schema row read-only. The value, layer badges
  // and environment hint come from the settings owners, so the overlay cannot
  // disagree with the Settings screen. The write stays there.
  function setupSettingFact(view) {
    const secret = view.secret === true;
    const value = secret
      ? (view.value === "" || view.value === null || view.value === undefined
        ? t("common.none")
        : t("setup.advanced.network.set"))
      : settingValueText(view.value);
    const env = view.layer === "env"
      ? t("settings.env_hint", { env: settingEnvName(view.key) })
      : "";
    return h("div", { class: "setup-setting" },
      h("div", { class: "setup-setting-copy" },
        h("span", { class: "setting-title", text: settingDescription(view) }),
        h("code", { class: "setting-key", text: view.key }),
        settingRowBadges(view, false)),
      h("div", { class: "setup-setting-value" },
        h("span", { class: "mono", text: value }),
        env ? h("span", { class: "field-hint", text: env }) : null));
  }

  function setupStorageFacts(view) {
    return h("dl", { class: "setup-facts" },
      setupFact(t("setup.welcome.data_dir"), h("span", { class: "mono", text: view.data_dir || t("common.dash") })),
      setupFact(t("setup.advanced.storage.db"), view.database_size_known
        ? h("span", { class: "mono", text: formatBytes(view.database_bytes) })
        : h("span", { class: "fine", text: t("setup.advanced.storage.db_unknown") })));
  }

  function setupAdvancedStorage(settings) {
    const views = setupSettingViews(settings, [
      "sync.details.enabled",
      "sync.removal.auto_apply_after",
      "log.file.enabled",
      "log.file.max_files",
      "log.file.max_size_mb",
      "log.level",
    ]);
    return fragment(
      h("p", { class: "fine", text: t("setup.advanced.storage.local") }),
      views.length ? h("div", { class: "setup-setting-list" }, views.map(setupSettingFact)) : null,
      h("div", { class: "button-row" },
        button(t("setup.open_general"), () => leaveSetup("settings", { tab: "general" }), { small: true })));
  }

  function setupAdvancedNetwork(settings) {
    const views = setupSettingViews(settings, ["http.proxy_url", "http.timeout.total"]);
    return fragment(
      views.length ? h("div", { class: "setup-setting-list" }, views.map(setupSettingFact)) : null,
      h("div", { class: "button-row" },
        button(t("setup.advanced.network.open"), () => leaveSetup("settings", { tab: "general", q: "http.proxy_url" }), { small: true })));
  }

  function setupAdvancedGroup(id, body) {
    return h("section", { class: "setup-advanced-group", "aria-labelledby": "setup-advanced-" + id },
      h("h4", { id: "setup-advanced-" + id, text: t("setup.advanced." + id + ".title") }),
      h("p", { class: "fine", text: t("setup.advanced." + id + ".sub") }),
      body);
  }

  function advancedSections() {
    return h("section", { class: "setup-advanced", "aria-labelledby": "setup-advanced-title" },
      h("div", { class: "setup-advanced-head" },
        h("h3", { id: "setup-advanced-title", text: t("setup.advanced.title") }),
        h("p", { class: "fine", text: t("setup.advanced.sub") })),
      setupAdvancedGroup("providers", setupLive(["providers"], setupAdvancedProviders)),
      setupAdvancedGroup("runtime", setupLive(["sidecar", "settings", "providers"], setupRuntimeView)),
      setupAdvancedGroup("storage", fragment(
        setupStorageFacts(state.setup || {}),
        setupLive(["settings"], setupAdvancedStorage))),
      setupAdvancedGroup("network", setupLive(["settings"], setupAdvancedNetwork)),
      setupProblems(["providers", "sidecar", "settings"]));
  }

  // ------------------------------------------------------------ finish

  function setupLearning(value) {
    const status = value && value.status ? value.status : null;
    if (!status || !Array.isArray(status.stages)) return null;
    return status;
  }

  function setupLearningSentence(value) {
    const status = setupLearning(value);
    if (!status) return t("setup.finish.learning_unknown");
    const zone = String(state.setupDraft.zone || (state.setup && state.setup.time_zone) || "").trim();
    if (!zone) return t("setup.finish.learning_zone");
    if (status.state === "learned") return t("setup.finish.learning_done");
    const next = status.stages.find((stage) => stage.state !== "done");
    if (!next) return t("setup.finish.learning_done");
    return t("setup.finish.learning_next", {
      stage: stageTitle(next.id),
      state: learningStateText(next.state),
    });
  }

  function setupSyncEta(value) {
    const status = setupLearning(value);
    if (!status) return null;
    const stage = status.stages.find((entry) => entry.id === "sync" && Number(entry.eta) > 0);
    return stage ? Number(stage.eta) : null;
  }

  function setupFinishSync(mailboxes) {
    const boxes = Array.isArray(mailboxes && mailboxes.mailboxes) ? mailboxes.mailboxes : [];
    const keys = setupMailboxKeys(boxes);
    const selected = boxes.filter((box) => keys.includes(box.key));
    if (keys.length === 0) return t("setup.finish.sync_none");
    const items = setupItems(selected);
    return items === null
      ? t("setup.finish.sync_unknown", { stores: selected.length })
      : t("setup.finish.sync_will", { stores: selected.length, messages: formatNumber(items) });
  }

  function setupSyncAuto(settings) {
    const auto = setupSettingViews(settings, ["sync.auto"])[0];
    return auto && auto.value === false ? t("setup.finish.sync_auto_off") : null;
  }

  function setupHeldSentence(value) {
    const folders = Array.isArray(value && value.folders) ? value.folders : [];
    const held = folders.reduce((total, folder) => total + (Number(folder && folder.held) || 0), 0);
    return held > 0 ? t("setup.finish.held", { count: held }) : t("setup.finish.held_none");
  }

  function setupBlockingReasons() {
    const reasons = [];
    const boxes = setupBoxes();
    if (state.setupPrefetch && !setupReadFailed("mailboxes") && setupReadValue("mailboxes")) {
      if (setupMailboxKeys(boxes).length === 0) {
        reasons.push({ key: "setup.finish.blocked_store", label: t("setup.open_sources"), destination: ["sources", {}] });
      }
    }
    const zone = String(state.setupDraft.zone || (state.setup && state.setup.time_zone) || "").trim();
    if (!zone) {
      reasons.push({
        key: "setup.finish.blocked_zone",
        label: t("setup.open_general"),
        destination: ["settings", { tab: "general", q: "time.zone" }],
      });
    }
    const facts = setupProviderFacts();
    if (facts && facts.roles.length) {
      const decider = facts.roles.find((role) => role.setting === "triage.decider");
      if (decider && (!decider.configured || decider.available === false)) {
        reasons.push({
          key: "setup.finish.blocked_provider",
          label: t("setup.open_providers"),
          destination: ["settings", { tab: "providers" }],
        });
      }
    }
    const snapshot = setupRuntimeSnapshot();
    if (snapshot && snapshot.choice && snapshot.choice.local && snapshot.build && !snapshot.build.installed && !snapshot.build.unavailable) {
      reasons.push({
        key: "setup.finish.blocked_runtime",
        label: t("setup.open_runtime"),
        destination: ["settings", { tab: "runtime" }],
      });
    }
    return reasons;
  }

  function setupNotNowContent() {
    const reasons = setupBlockingReasons();
    return fragment(
      h("p", { class: "fine", text: reasons.length ? t("setup.finish.not_now_body") : t("setup.finish.not_now_none") }),
      reasons.length
        ? h("ul", { class: "setup-list" }, reasons.map((reason) => h("li", null,
          h("span", { text: t(reason.key) }),
          button(reason.label, () => leaveSetup(reason.destination[0], reason.destination[1]), { small: true }))))
        : null);
  }

  function setupNotNowPanel() {
    return h("section", { class: "setup-not-now", "aria-labelledby": "setup-not-now-title" },
      h("h3", { id: "setup-not-now-title", text: t("setup.finish.not_now_title") }),
      setupLive(["mailboxes", "providers", "sidecar", "settings"], setupNotNowContent, "div"),
      h("div", { class: "button-row" },
        button(t("setup.finish.not_now_close"), (event) => completeSetup(event.currentTarget, null, true), { kind: "primary" })));
  }

  function finishStep() {
    const body = h("div", { class: "setup-form" },
      setupStepHeading(),
      h("p", { text: t("setup.finish.body") }),
      h("dl", { class: "setup-facts setup-facts-next" },
        setupFact(t("setup.finish.sync_label"), h("span", { class: "setup-sync-line" },
          setupLive(["mailboxes"], setupFinishSync),
          h("span", { class: "setup-sync-auto" }, setupLive(["settings"], setupSyncAuto)))),
        setupFact(t("setup.finish.eta_label"), setupLive(["learning"], (learning) => {
          const eta = setupSyncEta(learning);
          return h("span", { text: eta ? t("setup.finish.eta_known", { duration: formatDuration(eta) }) : t("setup.finish.eta_unknown") });
        })),
        setupFact(t("setup.finish.learning_label"), setupLive(["learning"], (learning) => h("span", { text: setupLearningSentence(learning) }))),
        setupFact(t("setup.finish.held_label"), setupLive(["removals"], (removals) => h("span", { text: setupHeldSentence(removals) })))),
      h("p", { class: "fine", text: t("setup.finish.limits") }),
      setupProblems(["mailboxes", "learning", "removals", "settings", "providers", "sidecar"]),
      state.setupNotNow ? setupNotNowPanel() : null);
    const later = button(t("setup.finish.later"), () => {
      state.setupNotNow = !state.setupNotNow; paintSetup();
    }); later.id = "setup-finish-later";
    const done = button(t("setup.finish.done"), (event) => completeSetup(event.currentTarget, null, false), { kind: "primary" }); done.id = "setup-finish-done";
    const actions = [
      button(t("setup.back"), () => enterSetupStep("model")),
      later,
      done,
    ];
    return { body, actions };
  }

  function setupBusyAction(trigger, label, fn) {
    state.setupBusy = true;
    return withBusy(trigger, label, fn).finally(() => { state.setupBusy = false; });
  }

  async function runSetupAction(trigger, label, fn) {
    showSetupActionError("");
    try {
      await setupBusyAction(trigger, label, fn);
      return true;
    } catch (error) {
      showSetupActionError(apiFailure(error).message);
      return false;
    }
  }

  // completeSetup writes the one completion marker through the ordinary
  // settings route. The flag makes a double click or a slow response write it
  // once; a failed write clears the flag so the owner can retry.
  async function completeSetup(trigger, destination, later) {
    if (state.setupCompleting) return;
    state.setupCompleting = true;
    try {
      const ok = await runSetupAction(trigger, t("setup.saving"), async () => {
        await request("/api/settings/ui.setup.done", { method: "PUT", body: { value: true } });
        clearSetupProgress();
        closeSetup();
        invalidateCache(["/api/setup", "/api/mailboxes", "/api/status", "/api/learning", "/api/gate", "/api/hub"]);
        if (later) toast(t("setup.later_title"), "info", t("setup.later_body"));
        if (destination) navigate(destination.name, destination.params || {});
        else navigate("hub");
      });
      if (!ok) return;
    } finally {
      state.setupCompleting = false;
    }
  }

  // leaveSetup opens the screen that owns a choice without writing the marker.
  // The wizard reopens at the furthest step on the next start, and the note
  // says so instead of pretending setup finished.
  // setupPauseNote keeps the pause honest: a picked store is the backend's
  // proof of setup, so the overlay no longer reopens by itself after one.
  function setupPauseNote() {
    const picked = setupMailboxKeys(setupBoxes()).length > 0 || !!(state.setup && state.setup.selected);
    return t(picked ? "setup.leave_note_picked" : "setup.leave_note");
  }

  function leaveSetup(name, params) {
    const note = setupPauseNote();
    closeSetup();
    invalidateCache(["/api/setup"]);
    toast(t("setup.paused_title"), "info", note);
    navigate(name, params || {});
  }
