  // ---------------------------------------------------------------- settings

  // General opens on the curated projection; Providers, Runtime, Advanced and
  // search read the whole schema through the same cache.
  const SETTINGS_FULL_PATH = "/api/settings";
  const SETTINGS_SIMPLE_PATH = SETTINGS_FULL_PATH + "?keys=" +
    SETTINGS_SIMPLE_KEY_LIST.map(encodeURIComponent).join(",");
  let settingsFullInflight = null;

  // The tabs that own settings rows of their own. General owns everything
  // else, and its search hands a match on another tab to that tab with the
  // key in the query, so the owner lands on the row that matched.
  const SETTINGS_RUNTIME_GROUP = "Local runtime";
  const SETTINGS_UPDATES_GROUP = "Updates";
  const SETTINGS_MAILBOX_SOURCE = "sources";
  const SETTINGS_MAILBOX_KEYS = new Set(["mailboxes.selected", "mailboxes.declared_addresses"]);

  // settingOwnerTab is the one answer to "which tab edits this setting". The
  // owner renders the editable row; every other tab links to it.
  function settingOwnerTab(view) {
    const key = view && view.key ? view.key : "";
    if (PROVIDER_ROLE_SETTINGS.has(key)) return "providers";
    if (view && view.group === SETTINGS_RUNTIME_GROUP) return "runtime";
    if (view && view.group === SETTINGS_UPDATES_GROUP) return "updates";
    return "general";
  }

  // settingEnvName is the environment override the server reads for a key:
  // dots become underscores and the name is upper-cased (config.EnvName).
  function settingEnvName(key) {
    return "MAILSENSE_" + String(key || "").split(".").join("_").toUpperCase();
  }

  // settingValueText is the one reading of a schema value in the owner's
  // language: a list, a boolean, a number and an empty value each read the
  // same way whether it is the effective value or the default.
  function settingValueText(value) {
    if (Array.isArray(value)) return value.length ? value.map(jsonValue).join(", ") : t("common.none");
    if (typeof value === "boolean") return value ? t("settings.yes") : t("settings.no");
    if (value === null || value === undefined) return t("common.dash");
    const plain = String(value);
    return plain === "" ? t("common.none") : plain;
  }

  // settingDefaultText is the schema default in the owner's language. The
  // view carries it as the schema's own value, so a default list, boolean or
  // number reads the same way the effective value does.
  function settingDefaultText(view) {
    return settingValueText(view ? view.default : undefined);
  }

  // settingValuesEqual compares the effective value with the schema default.
  // A duration is compared in nanoseconds because the schema states its
  // shortest form ("15m") while the effective value carries Go's canonical
  // form ("15m0s"): the two name the same wait.
  function settingValuesEqual(view, left, right) {
    if (view && view.type === "duration") {
      const leftNanos = durationNanos(left);
      const rightNanos = durationNanos(right);
      if (leftNanos !== null && rightNanos !== null) return leftNanos === rightNanos;
    }
    if (Array.isArray(left) && Array.isArray(right)) {
      return left.length === right.length && left.every((item, index) => settingValuesEqual(view, item, right[index]));
    }
    if (left && right && typeof left === "object" && typeof right === "object") {
      return jsonValue(left) === jsonValue(right);
    }
    return String(left) === String(right);
  }

  function settingIsDefault(view) {
    return settingValuesEqual(view, view.value, view.default);
  }

  // settingsTabSearchNote is the deep-link note on the tab a search handed a
  // match to. It names what is filtered, offers the whole tab, and offers the
  // full search that reaches every setting.
  function settingsTabSearchNote(query, matches, tab) {
    const wanted = String(query || "").trim();
    if (!wanted) return null;
    return h("div", { class: "settings-simple-note" },
      h("p", { class: "fine", text: t("settings.search.tab_shown", { count: matches, query: wanted }) }),
      h("div", { class: "button-row" },
        navigateWith(t("settings.search.show_tab"), "settings", { tab }, { small: true }),
        navigateWith(t("settings.filter_placeholder"), "settings", { tab: "general", q: wanted }, { small: true, kind: "primary" })));
  }

  // applySettingsTabSearch filters a tab the search handed a match to. The
  // rows keep their place; only the ones whose corpus matched stay visible,
  // and the note in front says how many did and how to see the whole tab.
  function applySettingsTabSearch(content, tab) {
    const query = String(state.settingsQuery || "").trim();
    if (!query) return content;
    const rows = [...content.querySelectorAll(".setting-row")];
    let matches = 0;
    for (const row of rows) {
      const match = row.dataset.match === "1";
      row.hidden = !match;
      if (match) matches++;
    }
    content.prepend(settingsTabSearchNote(query, matches, tab));
    return content;
  }

  // loadAllSettings is the one lazy full read. Show all, Advanced, search and
  // a cache hit share it, so the toggles never race a second request.
  function loadAllSettings() {
    if (state.settingsAll && cached(SETTINGS_FULL_PATH) === state.settingsAll) {
      return Promise.resolve(state.settingsAll);
    }
    if (settingsFullInflight) return settingsFullInflight;
    settingsFullInflight = requestCached(SETTINGS_FULL_PATH, {}, CACHE_TTL.settings)
      .then((view) => {
        state.settingsAll = view;
        return view;
      })
      .finally(() => { settingsFullInflight = null; });
    return settingsFullInflight;
  }

  async function renderSettings(route, seq) {
    const requested = route.params.get("tab") || "general";
    // The old key screen is now the provider screen; keep its links working.
    const tab = requested === "keys" ? "providers" : requested;
    state.settingsTab = tab;
    state.settingsQuery = route.params.get("q") || "";
    let status = null;
    let settings = null;
    let providers = null;
    let sidecar = null;
    let update = null;
    if (tab === "providers") {
      const providerRead = requestCached("/api/providers", {}, CACHE_TTL.providers)
        .catch((error) => ({ providers: [], error }));
      [settings, providers] = await Promise.all([
        requestCached(SETTINGS_FULL_PATH, {}, CACHE_TTL.settings),
        providerRead,
      ]);
    } else if (tab === "runtime") {
      // The engine card names the selected managed embedder even when idle.
      const providerRead = requestCached("/api/providers", {}, CACHE_TTL.providers)
        .catch((error) => ({ providers: [], error }));
      [settings, providers, sidecar, status] = await Promise.all([
        requestCached(SETTINGS_FULL_PATH, {}, CACHE_TTL.settings),
        providerRead,
        requestCached("/api/sidecar", {}, CACHE_TTL.sidecar),
        loadStatus(),
      ]);
    } else if (tab === "updates") {
      // Update status is a persisted local read, not a cached route payload:
      // the check writes it and the SSE event carries the same shape.
      const updateRead = loadUpdateStatus().catch((error) => ({ read_error: error }));
      [settings, status, update] = await Promise.all([
        requestCached(SETTINGS_FULL_PATH, {}, CACHE_TTL.settings),
        loadStatus(),
        updateRead,
      ]);
    } else {
      [status, settings] = await Promise.all([
        loadStatus(),
        requestCached(SETTINGS_SIMPLE_PATH, {}, CACHE_TTL.settings),
      ]);
    }
    if (seq !== state.renderSeq) return h("div");
    state.settingsData = { status, settings, providers, sidecar, update };

    if (tab === "providers") {
      const meta = [];
      if (settings && settings.restart_pending && settings.restart_pending.length) {
        meta.push(pill(t("settings.restart_pending", { count: settings.restart_pending.length }), "warn"));
      } else if (settings) {
        meta.push(pill(t("settings.no_restart_pending"), "done"));
      }
      setTopbarMeta(...meta);
    } else if (tab === "runtime") {
      setTopbarMeta(pill(t("settings.tab.runtime")));
    } else if (tab === "updates") {
      setTopbarMeta(pill(t("settings.tab.updates")));
    }

    const tabs = [
      ["general", t("settings.tab.general")],
      ["providers", t("settings.tab.providers")],
      ["runtime", t("settings.tab.runtime")],
      ["updates", t("settings.tab.updates")],
    ];
    const subnav = h("nav", { class: "subnav", "aria-label": t("settings.sections_aria") }, tabs.map(([id, label]) =>
      tabButton(label, id === tab, () => navigate("settings", { tab: id, q: state.settingsQuery }), "settings-tab-" + id)));
    const content = tab === "providers" ? providersPanel(settings, providers)
      : tab === "runtime" ? runtimePanel(sidecar, providers, settings, status)
        : tab === "updates" ? updatesPanel(update, settings, status)
          : generalSettingsPanel(settings, status);
    // A search that handed a match to another tab filters that tab's rows and
    // says how many matched.
    const filtered = tab === "general" ? content : applySettingsTabSearch(content, tab);
    if (tab !== "general") bindSettingsTabSearch(tab);
    const screen = h("div", { class: "screen settings-screen" },
      h("div", { class: "settings-layout" }, subnav, filtered));
    revealSettingMatch(seq);
    return screen;
  }

  // revealSettingMatch carries a deep-linked search to the row it names: the
  // row builder marks the match, and this scrolls it into view with the
  // keyboard on it. The row stays the same element, so the focus is a real
  // tab stop for the next Tab.
  function revealSettingMatch(seq) {
    // While the owner types in the shell bar the list filters under the caret;
    // moving the view on each keystroke would fight the typing.
    if (document.activeElement === dom.globalSearchInput) return;
    if (!state.settingsQuery.trim()) return;
    window.requestAnimationFrame(() => {
      if (seq !== state.renderSeq) return;
      const row = dom.main.querySelector('.setting-row[data-match="1"]');
      if (!row || !row.isConnected) return;
      row.scrollIntoView({ block: "center" });
      row.focus({ preventScroll: true });
    });
  }

  function settingsList(...children) {
    return h("div", { class: "settings-list section-scroll", dataset: { scrollKey: "settings-list" } }, children);
  }

  function settingGroupLabel(group) {
    return tOr("settings.group." + group, group);
  }

  // resourceUsageNode is the live host reading against the configured
  // ceilings. The five resource settings below own the policy; this readout
  // makes their effect observable without opening a log or a terminal. It is
  // built from the status read and after a saved resource limit, never by a
  // polling loop.
  function resourceUsageNode(status) {
    const res = status && status.resources;
    if (!res || !res.usage) return null;
    const order = ["cpu", "ram", "disk"];
    const readings = order
      .filter((key) => res.usage[key] && res.usage[key].known)
      .map((key) => resourceSentenceName(key) + " " + Math.round(Number(res.usage[key].percent)) + " %");
    if (!readings.length) return null;
    const limits = res.limits || {};
    const limitReadings = order
      .map((key) => resourceSentenceName(key) + " " + Number(limits[key]) + " %");
    const paused = res.paused && res.paused.resource
      ? t("settings.resource_usage.paused", {
          resource: resourceSentenceName(res.paused.resource),
          percent: Math.round(Number(res.paused.percent)),
          limit: res.paused.limit,
        })
      : "";
    return h("div", { class: "setting-usage fine", role: "status", "aria-live": "polite" },
      h("span", { text: t("settings.resource_usage.now", { values: metaLine(readings) }) }),
      h("span", {
        class: "setting-usage-limits" + (limits.enabled ? "" : " setting-usage-off"),
        text: limits.enabled
          ? t("settings.resource_usage.limits", { values: metaLine(limitReadings) })
          : t("settings.resource_usage.disabled"),
      }),
      paused ? h("span", { class: "setting-usage-paused", text: paused }) : null);
  }

  // refreshResourceUsage replaces the readout after a resource limit is
  // saved. The save already succeeded; a failed reading keeps the last one.
  async function refreshResourceUsage() {
    try {
      const next = await request("/api/status");
      if (state.settingsData) state.settingsData.status = next;
      const current = document.querySelector(".setting-usage");
      if (!current) return;
      const node = resourceUsageNode(next);
      if (node) current.replaceWith(node);
      else current.remove();
    } catch { /* the reading is optional */ }
  }

  // settingsGroupNode is the one look of a settings group: title, count,
  // the optional live readout and rows. corpus is the optional search text
  // General filters on. Every tab renders its rows through settingRow.
  function settingsGroupNode(title, views, status, opts = {}) {
    const rows = views.map((view) => settingRow(view, status, {
      simple: !!opts.simple,
      tab: opts.tab,
    }));
    const usage = opts.usage ? resourceUsageNode(status) : null;
    return h("section", { class: "setting-group", dataset: opts.corpus ? { search: opts.corpus } : null },
      h("div", { class: "setting-group-head" },
        h("h2", { text: title }),
        opts.count === false ? null : h("span", { class: "fine", text: t("settings.group_count", { count: views.length }) })),
      usage,
      rows);
  }

  function settingDescription(view) {
    return tOr("settings." + (view.key || "") + ".description", view.description || "");
  }

  function simpleSettingDescription(view) {
    return tOr("settings.simple.text." + (view.key || ""), settingDescription(view));
  }

  function simpleSettingGroup(groupId) {
    return tOr("settings.simple.group." + groupId, groupId);
  }

  function settingsAdvanced() {
    try { return window.sessionStorage.getItem(SETTINGS_ADVANCED_KEY) === "1"; } catch { return false; }
  }

  function rememberSettingsAdvanced(value) {
    try { window.sessionStorage.setItem(SETTINGS_ADVANCED_KEY, value ? "1" : "0"); } catch { /* storage may be unavailable */ }
  }

  // generalSettingsList drops the rows owned by the Providers, Local runtime
  // and Updates screens, whether the payload is the projection or the full
  // list.
  function generalSettingsList(view) {
    return (Array.isArray(view.settings) ? view.settings : [])
      .filter((item) => !PROVIDER_ROLE_SETTINGS.has(item.key))
      // The Local engine tab owns the engine's settings (status and controls
      // together); a second copy in General made two screens disagree. The
      // Updates tab owns its three check settings for the same reason.
      .filter((item) => item.group !== SETTINGS_RUNTIME_GROUP && item.group !== SETTINGS_UPDATES_GROUP);
  }

  function settingsSimpleCount(byKey) {
    let count = 0;
    for (const group of SETTINGS_SIMPLE_GROUPS) {
      for (const key of group.keys) if (byKey.has(key)) count++;
    }
    return count;
  }

  function settingsModeToggle(advanced, setAdvanced) {
    const modeButton = (value, label) => h("button", {
      type: "button",
      "aria-pressed": value === advanced ? "true" : "false",
      text: label,
      onclick: () => setAdvanced(value),
    });
    return h("div", { class: "segmented settings-view-toggle", role: "group", "aria-label": t("settings.view_aria") },
      modeButton(false, t("settings.mode.simple")),
      modeButton(true, t("settings.mode.advanced")));
  }

  function settingsPendingBanner(pending) {
    if (!pending.length) return null;
    return warningBanner(h("div", null,
      h("strong", { text: t("settings.restart_banner", { count: pending.length }) }),
      text(pending.join(", ")),
      // Reload re-reads settings.json. It cannot apply a restart-required
      // value: only quitting and reopening the app can. The copy says which
      // action does what instead of letting the toast imply a restart.
      h("p", { class: "field-hint", text: t("settings.restart_explain") }),
      h("div", { class: "button-row" },
        button(t("settings.reload"), async (event) => {
          await withBusy(event.currentTarget, t("settings.reloading"), async () => {
            try {
              await request("/api/settings/reload", { method: "POST", body: {} });
              invalidateAllData();
              toast(t("settings.reloaded"), "info");
              await refreshActiveRoute();
            }
            catch (error) { reportActionError(error); }
          });
        }, { small: true }))));
  }

  function settingsPanelMeta(searching, advanced, shown, pending) {
    const pendingCount = pending.length;
    const stateText = searching
      ? t("settings.meta.shown", { count: shown })
      : advanced
        ? t("settings.meta.advanced")
        : t("settings.meta.simple");
    setTopbarMeta(
      pill(stateText, searching ? "pending" : advanced ? "accent" : "done"),
      pill(pendingCount ? t("settings.restart_pending", { count: pendingCount }) : t("settings.no_restart_pending"), pendingCount ? "warn" : "done"));
  }

  function settingsGroupView(status, title, views, corpusGroup, simpleRows = false, opts = {}) {
    const searchCorpus = [
      corpusGroup, settingGroupLabel(corpusGroup),
      title,
      ...views.flatMap((v) => [v.key, v.description, settingDescription(v), simpleSettingDescription(v)]),
    ].filter(Boolean).join(" ").toLowerCase();
    return settingsGroupNode(title, views, status, { simple: simpleRows, corpus: searchCorpus, count: !!corpusGroup,
      tab: opts.tab,
      usage: views.some((view) => String(view.key || "").startsWith("resource.")) });
  }

  function settingsGroupNodes(status, list, byKey, advanced, searching, opts = {}) {
    const nodes = [];
    if (!advanced && !searching) {
      for (const group of SETTINGS_SIMPLE_GROUPS) {
        const views = group.keys.map((key) => byKey.get(key)).filter(Boolean);
        if (!views.length) continue;
        nodes.push(settingsGroupView(status, simpleSettingGroup(group.id), views, "", true, opts));
      }
      return nodes;
    }
    const groups = new Map();
    for (const view of list) {
      const group = view.group || "Other";
      if (!groups.has(group)) groups.set(group, []);
      groups.get(group).push(view);
    }
    for (const [group, views] of [...groups.entries()].sort((a, b) => localeCompare(a[0], b[0]))) {
      nodes.push(settingsGroupView(status, settingGroupLabel(group), views, group, false, opts));
    }
    return nodes;
  }

  // settingsListScopes is the two views of one full payload: every schema
  // setting for search, and the General tab's own rows for Advanced.
  function settingsListScopes(view) {
    const all = Array.isArray(view.settings) ? view.settings : [];
    const general = generalSettingsList(view);
    return {
      search: { list: all, byKey: new Map(all.map((item) => [item.key, item])) },
      full: { list: general, byKey: new Map(general.map((item) => [item.key, item])) },
    };
  }

  function generalSettingsPanel(settings, status) {
    // Provider roles live on the Providers screen, their single owner.
    const simpleList = generalSettingsList(settings);
    // total is the server's schema count; a payload without it (an older
    // server or a test stub) falls back to what it actually carried.
    const total = typeof settings.total === "number" ? settings.total : simpleList.length;
    const pending = Array.isArray(settings.restart_pending) ? settings.restart_pending : [];
    const simpleByKey = new Map(simpleList.map((view) => [view.key, view]));
    // Search reads the whole schema, including the rows the Providers and
    // Local runtime tabs own.
    let searchScope = null;
    let fullScope = null;
    const search = h("input", { class: "input", type: "search", hidden: true, value: state.settingsQuery, placeholder: t("settings.filter_placeholder") });
    const count = h("span", { class: "fine", "aria-live": "polite" });
    let advanced = settingsAdvanced();
    let searching = !!state.settingsQuery.trim();
    let fullLoaded = false;
    const body = h("div", { class: "settings-groups" });
    const empty = emptyState(t("settings.no_match_title"), t("settings.no_match_body"), null, "settings");
    empty.hidden = true;

    const toggle = settingsModeToggle(advanced, setAdvanced);

    const intro = h("div", { class: "settings-simple-intro" },
      h("div", null,
        h("h3", { text: t("settings.simple.title") }),
        h("p", { class: "fine", text: t("settings.simple.sub") })));

    const hiddenNote = h("p", { class: "fine" });
    const simpleNote = h("div", { class: "settings-simple-note" },
      hiddenNote,
      button(t("settings.simple.show_all"), () => setAdvanced(true), { small: true, kind: "primary" }));

    const advancedNote = h("div", { class: "settings-simple-note" },
      h("p", { class: "fine", text: t("settings.simple.advanced_note") }),
      button(t("settings.simple.back"), () => setAdvanced(false), { small: true }));

    const searchNote = h("p", { class: "settings-search-note fine" });

    // hiddenCount counts every schema setting the simple view does not show:
    // the formula must not change when the full list arrives, or the
    // sentence jumps (242 -> 220) after Show all.
    function hiddenCount() {
      return Math.max(0, total - settingsSimpleCount(simpleByKey));
    }

    // scopeOf is the one answer to what the current mode renders: search
    // covers every setting, Advanced the General tab's own rows.
    const scopeOf = () => searching && fullLoaded ? searchScope
      : advanced && fullLoaded ? fullScope : { list: simpleList, byKey: simpleByKey };

    async function ensureFull() {
      // A settings write invalidates the read cache; the full list is then
      // stale even though it is already in hand, so reload it before use.
      if (fullLoaded && state.settingsAll && cached(SETTINGS_FULL_PATH) === state.settingsAll) return;
      try {
        const scopes = settingsListScopes(await loadAllSettings());
        fullLoaded = true;
        searchScope = scopes.search;
        fullScope = scopes.full;
      } catch (error) {
        reportActionError(error);
      }
    }

    async function setAdvanced(value) {
      advanced = value;
      rememberSettingsAdvanced(value);
      if (value) await ensureFull();
      draw();
    }

    function draw() {
      const q = search.value.trim();
      searching = !!q;
      state.settingsQuery = search.value;
      clear(body);
      const scope = scopeOf();
      appendChildren(body, [settingsGroupNodes(status, scope.list, scope.byKey, advanced, searching, { tab: "general" }), empty]);
      intro.hidden = advanced || searching;
      simpleNote.hidden = advanced || searching;
      advancedNote.hidden = !advanced || searching;
      searchNote.hidden = !searching;
      searchNote.textContent = searching ? t("settings.search.all", { count: scope.list.length }) : "";
      hiddenNote.textContent = t("settings.simple.hidden", { count: hiddenCount() });
      for (const node of toggle.querySelectorAll("button")) {
        node.setAttribute("aria-pressed", node === toggle.children[advanced ? 1 : 0] ? "true" : "false");
      }
      applyFilter();
    }

    function applyFilter() {
      state.settingsQuery = search.value;
      const q = search.value.trim().toLowerCase();
      const scope = scopeOf();
      let shown = 0;
      for (const group of body.querySelectorAll(".setting-group")) {
        let visible = 0;
        for (const row of group.querySelectorAll(".setting-row")) {
          const match = !q || String(row.dataset.search || "").includes(q);
          row.hidden = !match;
          if (match) visible++;
        }
        group.hidden = visible === 0;
        shown += visible;
      }
      empty.hidden = shown !== 0;
      count.textContent = searching
        ? t("settings.filter_count", { shown, total: scope.length })
        : advanced
          ? t("settings.filter_count", { shown, total: scope.length })
          : t("settings.simple.count", { count: shown });
      settingsPanelMeta(searching, advanced, shown, pending);
    }

    search.addEventListener("input", () => {
      const next = !!search.value.trim();
      if (next && !fullLoaded) {
        // Search always covers every setting: load the full list once, then
        // filter the rows the next draw renders.
        void ensureFull().then(() => {
          if (next !== searching) draw();
          else applyFilter();
        });
        return;
      }
      if (next !== searching) draw();
      else applyFilter();
    });

    const pendingBanner = settingsPendingBanner(pending);

    const panel = settingsList(
      sectionHead(t("settings.title"), { actions: [toggle, count], actionsClass: "settings-actions" }),
      intro,
      searchNote,
      simpleNote,
      advancedNote,
      languageOfferNode(),
      pendingBanner,
      body);
    draw();
    bindSettingsScopedSearch(panel, search);
    // Advanced persists for the session and search may be deep-linked: both
    // need the whole schema, so load it after the projected first paint.
    if (advanced || searching) void ensureFull().then(draw);
    return panel;
  }

  // bindSettingsScopedSearch keeps the shell bar as the only visible search
  // field. The hidden input keeps the existing draw/apply filter path; every
  // keystroke in the bar is proxied to it.
  function bindSettingsScopedSearch(panel, search) {
    panel.append(search);
    setScopedSearch("settings", (value) => {
      search.value = value;
      search.dispatchEvent(new Event("input"));
    }, "settings.filter_placeholder", "settings.filter_aria", () => state.settingsQuery);
  }

  // bindSettingsTabSearch keeps the same bar honest on the tabs that are not
  // General. Their rows are grouped by owner, so the bar re-renders the tab
  // with the new query instead of proxying a detached panel: Back returns to
  // the filtered view and the row builder owns the match badge.
  function bindSettingsTabSearch(tab) {
    setScopedSearch("settings", (value) => {
      state.settingsQuery = String(value || "");
      replaceRoute("settings", { tab, q: state.settingsQuery });
      renderRoute();
    }, "settings.filter_placeholder", "settings.filter_aria", () => state.settingsQuery);
  }

  // settingRowBadges is the one badge row of a settings row: the active search
  // match, the effective layer and the schema's flags. A restart badge carries
  // what a restart means, because Reload cannot apply it.
  function settingRowBadges(view, matched) {
    const layer = view.layer || "default";
    return h("div", { class: "setting-badges" },
      matched ? badge(t("settings.search.match"), "accent") : null,
      badge(tOr("settings.layer." + layer, t("settings.layer.unknown", { layer })), layer === "env" ? "danger" : layer === "file" ? "warn" : layer === "learned" ? "accent" : ""),
      view.restart_required ? badge(t("settings.badge.restart_required"), "warn") : null,
      view.restart_pending ? badge(t("settings.badge.restart_pending"), "warn", t("settings.restart_explain")) : null,
      view.secret ? badge(t("settings.badge.secret"), "plain") : null,
      view.data_file ? badge(t("settings.badge.data_file"), "plain") : null,
      view.learnable ? badge(t("settings.badge.learnable"), "plain") : null);
  }

  // settingRow is the one settings row builder: General, Providers and the
  // Local runtime tab all render through it, so the schema default, the reset
  // state, the environment lock and the restart badge cannot drift. opts.tab
  // names the tab rendering the row: a setting owned by another tab renders as
  // a pointer there instead of a second editor.
  function settingRow(view, status, opts = {}) {
    const key = view.key || "";
    const group = view.group || "Other";
    const layer = view.layer || "default";
    const tab = opts.tab || "general";
    const owner = settingOwnerTab(view);
    const localizedGroup = settingGroupLabel(group);
    const localizedDescription = opts.simple ? simpleSettingDescription(view) : settingDescription(view);
    const corpus = [key, group, localizedGroup, view.description, settingDescription(view),
      simpleSettingDescription(view), t("settings.tab." + owner)].filter(Boolean).join(" ").toLowerCase();
    // The route owns the search; every row reads the one value so the match
    // badge and the tab filter can never disagree.
    const query = String(state.settingsQuery || "").trim().toLowerCase();
    const matched = !!query && corpus.includes(query);
    if (owner !== tab) return settingOwnerRow(view, owner, { corpus, simple: opts.simple });
    const envLocked = layer === "env";
    const envHint = envLocked ? t("settings.env_hint", { env: settingEnvName(key) }) : "";
    const mailbox = SETTINGS_MAILBOX_KEYS.has(key);
    const badges = settingRowBadges(view, matched);
    const description = h("div", { class: "setting-title", text: localizedDescription });
    // A mailbox setting is written by the mailbox endpoints, which refuse a
    // settings write by design. It shows what it holds and where it is
    // managed, never an Apply that the server answers with 409.
    const control = mailbox
      ? { node: h("p", { class: "field-hint", text: t("settings.mailbox_hint") }), value: () => view.value }
      : opts.control ? opts.control(view, status) : settingControl(view, status, opts, localizedDescription || key);
    const left = h("div", { class: "setting-copy" },
      description,
      h("code", { class: "setting-key", text: key }),
      badges);
    const right = h("div", { class: "setting-control" }, control.node);
    // Every row names the schema default, so a value can be judged against it.
    right.append(h("div", { class: "setting-meta" },
      h("span", { text: t("settings.default_label") }), text(" "),
      h("code", { class: "mono", text: settingDefaultText(view) })));
    if (envHint) right.append(h("div", { class: "field-hint", text: envHint }));
    if (view.data_file) {
      const path = joinPath(status && status.data_dir ? status.data_dir : "", view.value);
      right.append(h("div", { class: "setting-meta" },
        h("span", { text: t("settings.edit_file") }),
        h("code", { class: "path", text: path || String(view.value || "") })));
      right.append(h("div", { class: "field-hint", text: t("settings.edit_file_hint") }));
    }
    if (view.learned) right.append(learnedBlock(view, view.learned));
    const error = h("div", { class: "field-error" });
    const isLanguage = key === "ui.language";
    // commitSetting is the one write of a setting: Apply and Reset differed
    // only by method and copy, and each spelled its own invalidation list.
    const commitSetting = async (event, { method, busy, done, failed }) => {
      // withBusy disables the trigger before the request, so the focus owner
      // must be captured before that: after the row is replaced the trigger
      // is detached and activeElement is already <body>.
      const hadFocus = row.contains(event.currentTarget);
      await withBusy(event.currentTarget, busy, async () => {
        markFieldError(control.node, error, "");
        let body;
        if (method === "PUT") {
          try { body = { value: control.value() }; } catch (err) { markFieldError(control.node, error, err.message); return; }
        }
        try {
          const updated = await request("/api/settings/" + encodeURIComponent(key), { method, body });
          if (isLanguage) {
            invalidateSettingsReads();
            await applyLanguageLive(updated && (updated.value === "en" || updated.value === "fr") ? updated.value : "auto");
            return;
          }
          invalidateAllReads();
          staleRouteCache();
          if (key === "learning.test_mode") await loadLearning(true);
          Object.assign(view, updated);
          if (key.startsWith("resource.")) void refreshResourceUsage();
          // The row is rebuilt after a save. A keyboard owner whose Apply
          // button is destroyed would otherwise land on <body>; keep the
          // focus in the row that replaced it.
          const next = settingRow(view, status, opts);
          row.replaceWith(next);
          if (hadFocus) {
            const target = next.querySelector(".setting-actions button, input, select");
            if (target) target.focus();
          }
          toast(t(done), "info", key);
          updateLearningChrome();
        } catch (err) {
          markFieldError(control.node, error, err instanceof ApiError ? apiFailure(err).message : t(failed));
          // A user-initiated write always reports, even while an unrelated
          // background prefetch is running: reportError would swallow it.
          reportActionError(err);
        }
      });
    };
    // The two actions carry the row's own state: an environment layer wins
    // over the file, and a value already at its default has nothing to
    // reset. Both say so instead of offering a write that cannot show.
    const atDefault = settingIsDefault(view);
    const actions = mailbox
      ? h("div", { class: "setting-actions" },
        navigateWith(t("settings.managed_in_sources"), SETTINGS_MAILBOX_SOURCE, {}, { small: true, icon: "arrow-right" }))
      : opts.actions === false ? null
        : h("div", { class: "setting-actions" },
          button(t("settings.apply"), (event) => commitSetting(event, {
            method: "PUT", busy: t("settings.applying"), done: "settings.saved", failed: "settings.rejected_value",
          }), { kind: "primary", small: true, disabled: envLocked, title: envHint }),
          button(t("settings.reset"), (event) => commitSetting(event, {
            method: "DELETE", busy: t("settings.resetting"), done: "settings.reset_done", failed: "settings.reset_failed",
          }), { small: true, kind: "quiet", disabled: atDefault, title: atDefault ? t("settings.reset_at_default") : "" }),
          key.startsWith("log.") ? navigateWith(t("logs.open"), "logs", {}, { small: true }) : null,
          view.format ? badge(view.format, "plain") : null);
    if (actions) right.append(actions);
    right.append(error);
    const row = h("div", {
      class: "setting-row",
      dataset: { search: corpus, simple: opts.simple ? "1" : "0", setting: key },
      tabindex: matched ? "-1" : null,
    }, left, right);
    if (matched) row.dataset.match = "1";
    return row;
  }

  // settingOwnerRow is a search match that lives on another tab. It names the
  // owning tab and links there with the key in the query, so the owner lands
  // on the row itself. It never writes: the owning tab is the only editor.
  function settingOwnerRow(view, owner, opts = {}) {
    const key = view.key || "";
    const label = t("settings.tab." + owner);
    const left = h("div", { class: "setting-copy" },
      h("div", { class: "setting-title", text: settingDescription(view) }),
      h("code", { class: "setting-key", text: key }),
      h("div", { class: "setting-badges" },
        badge(label, "accent"),
        badge(tOr("settings.layer." + (view.layer || "default"), t("settings.layer.unknown", { layer: view.layer || "default" })), "plain")));
    const right = h("div", { class: "setting-control" },
      h("div", { class: "setting-meta" },
        h("span", { text: t("settings.current_value") }), text(" "),
        h("code", { class: "mono", text: settingValueText(view.value) })),
      h("div", { class: "setting-meta" },
        h("span", { text: t("settings.default_label") }), text(" "),
        h("code", { class: "mono", text: settingDefaultText(view) })),
      navigateWith(t("settings.search.open_owned", { tab: label }), "settings", { tab: owner, q: key },
        { small: true, icon: "arrow-right", class: "setting-open-owner" }));
    return h("div", { class: "setting-row", dataset: { search: opts.corpus || "", simple: opts.simple ? "1" : "0" } }, left, right);
  }

  // settingControl builds one control; the caller names it with the setting's
  // own title, never a generic field label that repeats on every row.
  function settingControl(view, status, opts = {}, label) {
    const type = view.type || "string";
    const value = view.value;
    const min = typeof view.min === "number" ? view.min : undefined;
    const max = typeof view.max === "number" ? view.max : undefined;
    if (view.secret) {
      const input = h("input", { class: "input", type: "password", autocomplete: "new-password", placeholder: value === "[REDACTED]" || value === "" ? t("settings.secret_placeholder_replace") : t("settings.secret_placeholder") });
      return {
        node: field(label, input, t("settings.secret_hint"), { srOnly: true }),
        value: () => {
          if (!input.value) throw new Error(t("settings.secret_required"));
          return input.value;
        },
      };
    }
    if (type === "bool") {
      const select = h("select", { class: "select" },
        h("option", { value: "true", text: t("settings.yes"), selected: value === true }),
        h("option", { value: "false", text: t("settings.no"), selected: value === false }));
      return { node: field(label, select, null, { srOnly: true }), value: () => select.value === "true" };
    }
    if (type === "int" || type === "float") {
      const input = h("input", { class: "input", type: "number", step: type === "int" ? "1" : "any", min: min === undefined ? null : String(min), max: max === undefined ? null : String(max), value: value === null || value === undefined ? "" : String(value) });
      return {
        node: field(label, input, min !== undefined || max !== undefined ? t("settings.range", { min: min === undefined ? t("common.dash") : min, max: max === undefined ? t("common.dash") : max }) : "", { srOnly: true }),
        value: () => {
          if (input.value === "") throw new Error(t("settings.number_required"));
          const n = Number(input.value);
          if (!Number.isFinite(n)) throw new Error(t("settings.number_finite"));
          if (type === "int" && !Number.isInteger(n)) throw new Error(t("settings.number_integer"));
          return n;
        },
      };
    }
    if (type === "enum") {
      const options = Array.isArray(view.enum) ? view.enum : [];
      const optionLabel = (option) => {
        if (view.key === "ui.language") {
          if (option === "auto") return t("language.automatic");
          if (option === "en") return t("language.english");
          if (option === "fr") return t("language.french");
        }
        const key = "settings.enum." + view.key + "." + option;
        return I18N.has(key) ? t(key) : option;
      };
      const select = h("select", { class: "select" }, options.map((option) => h("option", { value: option, text: optionLabel(option), selected: option === value })));
      return { node: field(label, select, null, { srOnly: true }), value: () => select.value };
    }
    if (type === "list") {
      const items = Array.isArray(value) ? value : [];
      const textarea = h("textarea", { class: "textarea", rows: String(Math.max(3, Math.min(10, items.length + 1))), spellcheck: "false" });
      textarea.value = items.map((item) => typeof item === "string" ? item : String(item)).join("\n");
      const hint = t("settings.list_hint") +
        (opts.simple ? "" : (view.item_type ? t("settings.list_item_type", { type: view.item_type }) : "") +
          (view.format ? t("settings.list_format", { format: view.format }) : ""));
      return {
        node: field(label, textarea, hint, { srOnly: true }),
        value: () => {
          const lines = textarea.value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
          if (view.item_type === "int") return lines.map((line) => {
            const n = Number(line);
            if (!Number.isInteger(n)) throw new Error(t("settings.list_integer"));
            return n;
          });
          if (view.item_type === "float") return lines.map((line) => {
            const n = Number(line);
            if (!Number.isFinite(n)) throw new Error(t("settings.list_number"));
            return n;
          });
          return lines;
        },
      };
    }
    if (type === "duration") {
      const input = h("input", { class: "input", type: "text", value: value === null || value === undefined ? "" : String(value), placeholder: t("settings.duration_placeholder") });
      return { node: field(label, input, t("settings.duration_hint"), { srOnly: true }), value: () => {
        if (!input.value.trim()) throw new Error(t("settings.duration_required"));
        return input.value.trim();
      } };
    }
    const input = h("input", { class: "input", type: "text", value: value === null || value === undefined ? "" : String(value), placeholder: view.pattern ? t("settings.pattern_placeholder", { pattern: view.pattern }) : "" });
    return {
      node: field(label, input, view.format && !opts.simple ? t("settings.format_hint", { format: view.format }) : "", { srOnly: true }),
      value: () => input.value,
    };
  }

  function learnedBlock(view, learned) {
    const key = view.key;
    const provenance = learned.provenance || {};
    const head = h("div", { class: "learned-head" },
      h("strong", { text: t("settings.learned.title") }),
      badge(t("settings.learned.observations", { count: learned.sample || 0 })),
      provenance.learner ? badge(provenance.learner, "plain") : null,
      provenance.learned_at ? h("span", { class: "fine", text: formatDate(provenance.learned_at) }) : null,
      learned.ignored ? badge(t("settings.learned.not_applied"), "warn", learned.ignored) : null);
    const valueNode = h("div", { class: "learned-value", text: jsonValue(learned.value) });
    const rejectable = learned.value !== undefined && learned.value !== null;
    const isList = Array.isArray(learned.value);
    const rejectionButtons = [];
    if (rejectable) {
      if (isList) {
        for (const item of learned.value) {
          rejectionButtons.push(h("span", { class: "chip" }, text(jsonValue(item)),
            iconButton("close", t("settings.learned.reject_aria", { value: jsonValue(item) }), (event) => learnedDecision(key, item, true, event.currentTarget), { small: true })));
        }
      } else {
        rejectionButtons.push(button(t("settings.learned.reject"), (event) => learnedDecision(key, learned.value, true, event.currentTarget), { small: true, kind: "quiet" }));
      }
    }
    const rejected = Array.isArray(learned.rejected) ? learned.rejected : [];
    const rejectedNode = rejected.length
      ? h("div", { class: "rejected-list" }, rejected.map((item) => h("div", { class: "rejected-row" },
          h("span", { text: t("settings.learned.rejected") }),
          h("span", { class: "mono", text: jsonValue(item) }),
          button(t("settings.learned.allow"), (event) => learnedDecision(key, item, false, event.currentTarget), { small: true, kind: "quiet" }))))
      : null;
    return h("div", { class: "learned" },
      head,
      learned.ignored ? h("div", { class: "field-hint", text: learned.ignored }) : null,
      valueNode,
      rejectionButtons.length ? h("div", { class: "chips" }, rejectionButtons) : null,
      rejectedNode);
  }

  async function learnedDecision(key, value, reject, trigger) {
    await withBusy(trigger, reject ? t("settings.learned.rejecting") : t("settings.learned.allowing"), async () => {
      try {
        const path = "/api/settings/learned/" + encodeURIComponent(key) + (reject ? "/reject" : "/unreject");
        const response = await request(path, { method: "POST", body: { value } });
        invalidateSettingsReads();
        invalidateRouteCache("settings");
        const learned = Array.isArray(response.learned) ? response.learned.find((item) => item.key === key) : null;
        const row = trigger.closest(".setting-row");
        // The General tab now carries only the projected eight; learnable rows
        // live in the lazy full list, so the row's own payload wins there.
        const view = (state.settingsData && state.settingsData.settings && Array.isArray(state.settingsData.settings.settings)
          ? state.settingsData.settings.settings : [])
          .find((item) => item.key === key)
          || (state.settingsAll && Array.isArray(state.settingsAll.settings) ? state.settingsAll.settings : [])
            .find((item) => item.key === key);
        if (view) {
          view.learned = learned || null;
          const status = state.settingsData ? state.settingsData.status : null;
          if (row) row.replaceWith(settingRow(view, status, { simple: row.dataset.simple === "1" }));
        }
        toast(reject ? t("settings.learned.rejected_toast") : t("settings.learned.allowed_toast"), "info", key);
      } catch (error) { reportActionError(error); }
    });
  }

  // provider-editor:start
  //
  // One record shape drives the editor, the role selects and the request
  // body. `protocol` derives `kind`; the UI never keeps a second provider
  // catalog. Advanced fields stay in this table so every record field has
  // one owner and one rendered control.
  // The server's providers view is the one provider catalog: protocols,
  // their kinds, and the role settings with their kinds all come from
  // engine. Only presentation order and translated labels live here.
  const PROVIDER_ALL_KINDS = ["decider", "generator", "embedder"];
  const PROVIDER_ROLE_ORDER = ["qa.provider", "judge.provider", "triage.decider", "embed.provider"];
  const PROVIDER_ROLE_LABEL_KEYS = {
    "qa.provider": "settings.providers.role.qa",
    "judge.provider": "settings.providers.role.judge",
    "triage.decider": "settings.providers.role.triage",
    "embed.provider": "settings.providers.role.embed",
  };
  const PROVIDER_ROLE_SETTINGS = new Set(Object.keys(PROVIDER_ROLE_LABEL_KEYS));
  // The official model list may advertise a very large output ceiling. The
  // reservation is a budget ceiling, not the model's fact, so it stays small.
  const PROVIDER_RESERVE_OUTPUT_LIMIT = 16384;
  const PROVIDER_DEFAULT_CHARS_PER_TOKEN = 3;
  const PROVIDER_DEFAULT_RESERVE_TOKENS_PER_BYTE = 1;
  const PROVIDER_DISCOVERY_ERROR_HINTS = {
    provider_discovery_auth: "settings.providers.discovery.auth",
    provider_discovery_not_found: "settings.providers.discovery.no_endpoint",
  };
  const PROVIDER_ADVANCED_GROUPS = [
    {
      id: "limits",
      fields: [
        { name: "context_tokens", type: "int", min: 0, kinds: PROVIDER_ALL_KINDS, hintKey: "settings.providers.field.context_tokens_hint" },
        { name: "chars_per_token", type: "float", min: 0, kinds: PROVIDER_ALL_KINDS },
        { name: "reserve_tokens_per_byte", type: "float", min: 0, max: 4, kinds: ["decider", "generator"], hintKey: "settings.providers.field.reserve_tokens_per_byte_hint" },
        { name: "reserve_output_tokens", type: "int", min: 0, kinds: ["decider", "generator"], hintKey: "settings.providers.field.reserve_output_tokens_hint" },
      ],
    },
    {
      id: "decider",
      fields: [
        { name: "max_questions", type: "int", min: 1, kinds: ["decider"] },
        { name: "probability_tolerance", type: "float", min: 0, max: 1, kinds: ["decider"], hintKey: "settings.providers.field.probability_tolerance_hint" },
        { name: "envelope", type: "select", options: ["", "cloudflare"], kinds: ["decider"] },
        { name: "noul_fold", type: "text", kinds: ["decider"], hintKey: "settings.providers.field.noul_fold_hint" },
      ],
    },
    {
      id: "generation",
      fields: [
        { name: "response_format", type: "select", options: ["", "json_object"], kinds: ["generator"] },
        { name: "reasoning_effort", type: "select", options: ["", "none", "minimal", "low", "medium", "high", "xhigh", "max"], kinds: ["generator"] },
      ],
    },
    {
      id: "cost",
      fields: [
        { name: "price_input_per_mtok", type: "float", min: 0, kinds: ["decider", "generator"], hintKey: "settings.providers.field.price_hint" },
        { name: "price_output_per_mtok", type: "float", min: 0, kinds: ["decider", "generator"] },
      ],
    },
    {
      id: "retry",
      fields: [
        { name: "retry.max_attempts", type: "int", min: 1, kinds: PROVIDER_ALL_KINDS },
        { name: "retry.statuses", type: "list_int", kinds: PROVIDER_ALL_KINDS, hintKey: "settings.providers.field.retry_statuses_hint" },
        { name: "retry.quota_codes", type: "list_text", kinds: PROVIDER_ALL_KINDS, hintKey: "settings.providers.field.retry_quota_codes_hint" },
      ],
    },
    {
      id: "embedding",
      fields: [
        { name: "runtime", type: "text", kinds: ["embedder"], hintKey: "settings.providers.field.runtime_hint" },
        { name: "embedding.dimensions", type: "int", min: 1, kinds: ["embedder"] },
        { name: "embedding.matryoshka", type: "list_int", kinds: ["embedder"], hintKey: "settings.providers.field.embedding_matryoshka_hint" },
        { name: "embedding.query_template", type: "text", kinds: ["embedder"] },
        { name: "embedding.document_template", type: "text", kinds: ["embedder"] },
        { name: "embedding.empty_title", type: "text", kinds: ["embedder"] },
        { name: "embedding.norm_tolerance", type: "float", min: 0, max: 1, kinds: ["embedder"] },
        { name: "embedding.max_batch", type: "int", min: 1, kinds: ["embedder"] },
        { name: "embedding.reserved_tokens", type: "int", min: 0, kinds: ["embedder"] },
      ],
    },
  ];

