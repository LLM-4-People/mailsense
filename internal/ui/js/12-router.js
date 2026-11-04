  // ---------------------------------------------------------------- token and router

  function readLaunchToken() {
    const hash = location.hash || "";
    let fromUrl = "";
    if (hash.startsWith("#token=")) {
      fromUrl = safeDecode(hash.slice("#token=".length));
      const base = location.pathname + location.search;
      history.replaceState(null, "", base + "#/hub");
    }
    if (fromUrl) {
      try { sessionStorage.setItem(TOKEN_KEY, fromUrl); } catch { /* keep it in memory */ }
      return fromUrl;
    }
    try {
      const stored = sessionStorage.getItem(TOKEN_KEY);
      if (stored) return stored;
    } catch { /* storage may be unavailable */ }
    return "";
  }

  function showTokenError(messageKey = "token.reopen_message") {
    if (dom.app) dom.app.hidden = true;
    if (dom.tokenScreen) dom.tokenScreen.hidden = false;
    // Keep the data-i18n key in sync with the painted text. Otherwise a
    // language pass that lands after the 401 would restore the boot title
    // from the static shell and hide the reopen instruction.
    if (dom.tokenTitle) {
      dom.tokenTitle.setAttribute("data-i18n", "token.reopen_title");
      dom.tokenTitle.textContent = t("token.reopen_title");
    }
    if (dom.tokenMessage) {
      dom.tokenMessage.setAttribute("data-i18n", messageKey);
      dom.tokenMessage.textContent = t(messageKey);
    }
  }

  function parseRouteFromHash(hash) {
    const raw = String(hash || "").replace(/^#/, "");
    if (!raw || raw.startsWith("token=")) return { name: "hub", params: new URLSearchParams() };
    const cut = raw.indexOf("?");
    const path = cut === -1 ? raw : raw.slice(0, cut);
    const query = cut === -1 ? "" : raw.slice(cut + 1);
    const name = path.replace(/^\/+/, "").split("/")[0] || "hub";
    return { name: SCREEN_IDS.includes(name) ? name : "hub", params: new URLSearchParams(query) };
  }

  function parseRoute() {
    return parseRouteFromHash(location.hash);
  }

  function paramsObject(params) {
    const out = {};
    if (!params) return out;
    for (const [key, value] of params) out[key] = value;
    return out;
  }

  function routeHash(name, params) {
    const query = new URLSearchParams();
    if (params) {
      for (const [key, value] of Object.entries(params)) {
        if (value !== undefined && value !== null && value !== "") query.set(key, String(value));
      }
    }
    return "#/" + name + (query.toString() ? "?" + query.toString() : "");
  }

  function goToHash(hash) {
    if (!hash || !hash.startsWith("#/")) return false;
    if ((location.hash || "") === hash) {
      renderRoute();
      return true;
    }
    history.pushState(null, "", location.pathname + location.search + hash);
    renderRoute();
    return true;
  }

  function navigate(name, params = {}) {
    goToHash(routeHash(name, params));
  }

  function handleInternalLinkClick(event) {
    if (event.defaultPrevented || (event.button !== undefined && event.button !== 0) ||
      event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const anchor = event.target instanceof Element ? event.target.closest('a[href^="#/"]') : null;
    if (!anchor || (anchor.target && anchor.target !== "_self") || anchor.hasAttribute("download")) return;
    const hash = anchor.getAttribute("href") || "";
    if (!hash.startsWith("#/")) return;
    event.preventDefault();
    goToHash(hash);
  }

  function renderHistoryRoute() {
    if (!state.token || state.shuttingDown) return;
    const raw = (location.hash || "").replace(/^#/, "");
    if (raw && !raw.startsWith("/") && !raw.startsWith("token=")) return;
    const route = parseRoute();
    if (routeCacheKey(route) === state.routeKey) {
      // The URL still owns the selected company or stored answer:
      // Back/Forward between two details is a detail change, not a reason
      // to rebuild the list the owner was reading.
      if (route.name === "companies") syncCachedCompanyDetail(route);
      if (route.name === "answers") syncCachedAnswersDetail(route);
      attachCachedRoute();
      activateScopedSearch(route.name);
      return;
    }
    renderRoute();
  }

  function replaceRoute(name, params = {}) {
    const hash = routeHash(name, params);
    if (location.hash !== hash) history.replaceState(null, "", location.pathname + location.search + hash);
  }

  function setHeader(name) {
    const title = screenTitle(name);
    const eyebrow = screenEyebrow(name);
    document.documentElement.dataset.screen = name;
    dom.screenTitle.textContent = title;
    dom.screenEyebrow.textContent = eyebrow;
    document.title = documentTitle();
    for (const item of document.querySelectorAll("[data-nav]")) {
      if (item.dataset.nav === name) item.setAttribute("aria-current", "page");
      else item.removeAttribute("aria-current");
    }
    installNav();
    syncNavGroups(name);
    setNavDrawer(false);
  }

  function setTopbarMeta(...nodes) {
    clear(dom.topbarMeta);
    for (const node of nodes) {
      if (!node) continue;
      dom.topbarMeta.append(node instanceof Node ? node : text(String(node)));
    }
  }

  function setConnection(kind, label) {
    dom.connection.classList.remove("live", "reconnecting", "offline");
    dom.connection.classList.add(kind);
    // The live state owns this label after the first event: a later
    // catalog pass must not restore the server-rendered "Connecting".
    dom.connectionText.removeAttribute("data-i18n");
    dom.connectionText.textContent = label;
    state.connectionKind = kind;
  }

  // relabelConnection re-applies the current stream state in the newly
  // selected language; setConnection owns the text so localizeDocument
  // cannot reset it to the server-rendered "Connecting".
  function relabelConnection() {
    const kind = state.connectionKind;
    if (!kind) return;
    if (state.shuttingDown) setConnection("offline", t("connection.shutdown"));
    else if (kind === "live") setConnection("live", t("connection.live"));
    else if (kind === "offline") setConnection("offline", t("connection.offline"));
    else setConnection("reconnecting", eventSource ? t("connection.reconnecting") : t("connection.connecting_events"));
  }

  // ---------------------------------------------------------------- route cache

  function routeCacheKey(route) {
    const sorted = new URLSearchParams();
    // Selecting a message only changes the detail pane; the Mail list and
    // its scroll position are the same route. Keeping the id out of the key
    // makes a menu click paint the cached list instead of rebuilding it.
    const params = new URLSearchParams(route.params ? route.params.entries() : []);
    if (route.name === "mail") params.delete("id");
    // A company selection changes only the detail pane. Keeping the key out
    // of the screen identity lets the list and both scroll positions survive
    // the click, exactly like a message selection in Mail.
    if (route.name === "companies") params.delete("key");
    // An Answers row opens a stored message in the detail pane below the
    // list. The list, its accumulated pages and its scroll are the route;
    // the message id is detail state applied by syncCachedAnswersDetail.
    if (route.name === "answers") params.delete("message");
    const entries = [...params.entries()].sort((a, b) => a[0].localeCompare(b[0]) || a[1].localeCompare(b[1]));
    for (const [key, value] of entries) sorted.append(key, value);
    return route.name + "|" + sorted.toString();
  }

  function trimRouteCache() {
    while (routeCache.size > MAX_ROUTE_CACHE) {
      const oldest = routeCache.keys().next().value;
      if (oldest === undefined) break;
      routeCache.delete(oldest);
    }
  }

  function touchRouteCache(key, entry) {
    routeCache.delete(key);
    routeCache.set(key, entry);
    trimRouteCache();
  }

  // A cached node is only the markup. Screen-owned listeners and observers
  // are disconnected when the route is left, so the owner declares one
  // attach function on its screen node and the router invokes it after the
  // node is re-appended. Companies and Teach each own exactly one hook.
  function routeCacheEntry(route, node, scroll = 0, scrolls = null) {
    return {
      name: route.name,
      // Every Mail tab is cached under the one routed name "mail"; the topic
      // graph names the surface (search, priority, triage, conversations) it
      // changes, so the entry carries which tab it paints.
      surface: route.name === "mail" ? mailSurface(route) : "",
      node,
      scroll,
      scrolls: scrolls || {},
      at: Date.now(),
      attach: typeof node.__routeAttach === "function" ? node.__routeAttach : null,
    };
  }

  function attachCachedRoute(key = state.routeKey) {
    const entry = routeCache.get(key);
    if (entry && typeof entry.attach === "function") entry.attach();
  }

  function storeActiveRoute() {
    if (!state.routeKey) return;
    const entry = routeCache.get(state.routeKey);
    if (!entry) return;
    // The app shell never scrolls. Capture every named route section so a
    // cached screen restores each pane where the owner left it.
    entry.scroll = 0;
    entry.scrolls = captureScrollState(dom.main);
    entry.at = entry.at || Date.now();
    touchRouteCache(state.routeKey, entry);
  }

  // routeCacheMatches names one cached screen. A routed name matches its
  // entry; a Mail surface matches the tab it paints.
  function routeCacheMatches(entry, name) {
    return entry.name === name || (!!entry.surface && entry.surface === name);
  }

  // invalidateRouteCache drops cached screens whose DOM itself is wrong now
  // (another language, changed settings, a write): the next visit rebuilds.
  function invalidateRouteCache(...names) {
    if (!names.length) {
      routeCache.clear();
      return;
    }
    for (const [key, entry] of [...routeCache.entries()]) {
      if (names.some((name) => routeCacheMatches(entry, name))) routeCache.delete(key);
    }
  }

  // staleRouteCache marks screens whose data advanced while they were away.
  // The mounted snapshot (scroll, focus and any open disclosure) is still the
  // right thing to show for one visit; the route revalidates after mounting,
  // so a background event never throws the screen the owner left away.
  function staleRouteCache(...names) {
    for (const entry of routeCache.values()) {
      if (!names.length || names.some((name) => routeCacheMatches(entry, name))) entry.stale = true;
    }
  }

  function canReplaceMain() {
    const active = document.activeElement;
    // The shell search bar is the mounted screen's own filter (one combobox
    // serves every screen), so a background repaint while the owner types
    // there would throw away the screen under the caret and re-run its reads.
    // It is treated exactly like a focused control inside the screen: the
    // refresh waits for the focus to leave.
    if (active && active === dom.globalSearchInput) return false;
    if (!active || active === document.body || active === dom.main) return true;
    if (!dom.main.contains(active)) return true;
    if (active.isContentEditable) return false;
    return !active.matches("input, textarea, select");
  }

  // A refresh skipped because an input had focus runs once the focus leaves
  // the editable control, so the screen never stays stale after typing.
  let refreshOnBlur = false;

  function deferRefreshUntilBlur() {
    if (refreshOnBlur) return;
    refreshOnBlur = true;
    const handler = () => {
      if (!canReplaceMain()) return;
      document.removeEventListener("focusout", handler);
      refreshOnBlur = false;
      refreshActiveRoute();
    };
    document.addEventListener("focusout", handler);
  }

  // captureMainFocus remembers the focused row before a screen rebuild.
  // appendMain clears #main, which destroys the focused node and drops the
  // keyboard focus to <body>; the next Tab then restarts at the page top.
  function captureMainFocus() {
    const active = document.activeElement;
    if (!active || active === dom.main || !dom.main.contains(active)) return null;
    const out = { id: active.id || "" };
    for (const key of ["messageId", "seq", "companyIndex", "id", "focusKey"]) {
      if (active.dataset && active.dataset[key]) out[key] = active.dataset[key];
    }
    return out;
  }

  function restoreMainFocus(previous) {
    if (!previous) return;
    let node = previous.id ? document.getElementById(previous.id) : null;
    if (!node) {
      for (const key of ["messageId", "seq", "companyIndex", "id", "focusKey"]) {
        const value = previous[key];
        if (!value) continue;
        const attribute = "data-" + key.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
        // CSS.escape is the one escaping owner: a hand-rolled quote escape
        // left the backslash unescaped, so a value carrying both characters
        // built a selector that matched the wrong row (or threw).
        node = dom.main.querySelector("[" + attribute + "=" + CSS.escape(String(value)) + "]");
        if (node) break;
      }
    }
    if (node && dom.main.contains(node) && typeof node.focus === "function") {
      node.focus({ preventScroll: true });
      return;
    }
    // The replaced screen may have no equivalent row. #main is the stable
    // landmark named by the screen title; an unnamed .screen is not.
    dom.main.focus({ preventScroll: true });
  }

  // restoreFocus is false for a background repaint: it must never move focus
  // out of the shell search or a dialog. An explicit navigation still returns
  // focus to #main when the replaced screen had no addressable control.
  function appendMain(node, scroll = 0, scrolls = null, restoreFocus = true, focusCarry = null) {
    const previousFocus = restoreFocus ? (focusCarry || captureMainFocus()) : null;
    clear(dom.main);
    dom.main.append(node);
    if (node && node.classList && node.classList.contains("screen")) {
      node.setAttribute("tabindex", "0");
      if (node.dataset.screenScrollBound !== "1") {
        node.dataset.screenScrollBound = "1";
        node.addEventListener("scroll", () => {
          if (!dom.main.contains(node)) return;
          const entry = routeCache.get(state.routeKey);
          if (!entry) return;
          entry.scrolls = entry.scrolls || {};
          entry.scrolls.__screen = node.scrollTop;
        }, { passive: true });
      }
    }
    prepareScrollRegions(node);
    applyScrollState(node, scrolls);
    // Restore only a focus that was inside the replaced screen: a background
    // repaint must never pull focus out of the global search box or a dialog.
    restoreMainFocus(previousFocus);
    dom.main.scrollTop = scroll;
  }

  function routeScreen(root) {
    if (!root) return null;
    return root.classList && root.classList.contains("screen")
      ? root
      : root.querySelector(".screen");
  }

  function captureScrollState(root) {
    const scrolls = {};
    if (!root) return scrolls;
    const screen = routeScreen(root);
    if (screen) scrolls.__screen = screen.scrollTop;
    for (const node of root.querySelectorAll("[data-scroll-key]")) {
      scrolls[node.dataset.scrollKey] = node.scrollTop;
    }
    return scrolls;
  }

  function applyScrollState(root, scrolls) {
    if (!root || !scrolls) return;
    const screen = routeScreen(root);
    if (screen && Number.isFinite(scrolls.__screen)) {
      screen.scrollTop = Math.max(0, scrolls.__screen);
    }
    for (const node of root.querySelectorAll("[data-scroll-key]")) {
      const value = scrolls[node.dataset.scrollKey];
      if (Number.isFinite(value)) node.scrollTop = Math.max(0, value);
    }
  }

  // A bounded region must be reachable by keyboard once the page itself no
  // longer scrolls. Scroll events do not bubble, so each region also records
  // its own position for the route cache.
  function prepareScrollRegions(root) {
    if (!root) return;
    for (const node of root.querySelectorAll("[data-scroll-key]")) {
      if (!node.hasAttribute("tabindex")) node.setAttribute("tabindex", "0");
      if (node.dataset.scrollBound === "1") continue;
      node.dataset.scrollBound = "1";
      node.addEventListener("scroll", () => {
        if (!dom.main.contains(node)) return;
        const entry = routeCache.get(state.routeKey);
        if (!entry) return;
        entry.scrolls = entry.scrolls || {};
        entry.scrolls[node.dataset.scrollKey] = node.scrollTop;
      }, { passive: true });
    }
  }

  function isErrorScreen(node) {
    return !!(node && node.dataset && node.dataset.errorScreen);
  }

  // routeFailed is the one route-level failure test: a full error screen or
  // a screen that marked itself transiently failed. A fragment (an inline
  // surface) has no dataset and is never a failed route node.
  function routeFailed(node) {
    return !!(node && node.dataset && (node.dataset.errorScreen === "1" || node.dataset.routeError === "1"));
  }

  function quickRouteMeta(route) {
    // The skeleton topbar must not claim a closed gate before the permission
    // snapshot is known; the mounted screen repaints from the same helpers.
    const known = gateKnown();
    const open = known && gateOpen();
    const showPreview = known && gatePreview();
    const nodes = [known ? gatePill(open) : null, showPreview ? previewPill() : null];
    if (route.name === "mail") {
      const mode = parseMode(route);
      nodes.push(pill(t("mail.tab." + mode)));
    }
    if (route.name === "labels") {
      const pending = labelReviewCount();
      if (pending) nodes.push(pill(t("labels.queue.to_review", { count: pending }), "running"));
    }
    if (route.name === "companies" && state.companiesCount > 0) {
      nodes.push(pill(t("companies.list.count", { count: state.companiesCount })));
    }
    return nodes;
  }

  function skeletonLine(width) {
    return h("div", { class: "skeleton-line " + width });
  }

  // skeletonScreen is one placeholder route: the busy flag and the one live
  // announcement every screen needs, so no route can ship a silent wait.
  function skeletonScreen(className, ...children) {
    return h("div", { class: "screen " + (className || ""), "aria-busy": "true" },
      h("span", { class: "sr-only", role: "status", text: t("common.loading") }),
      ...children);
  }

  function skeletonPanel(...children) {
    return h("div", { class: "surface skeleton-panel" }, children);
  }

  // skeletonSection mirrors the Home section primitive (head + scrolling
  // body), so the placeholder has the same shape as the content it replaces.
  function skeletonSection(...children) {
    return h("section", { class: "surface home-section" },
      h("header", { class: "home-section-head" },
        h("div", { class: "home-section-title" },
          skeletonLine("w-30"),
          skeletonLine("w-40"))),
      h("div", { class: "home-section-body skeleton-panel" }, children));
  }

  function routeSkeleton(name) {
    if (name === "hub") {
      return skeletonScreen("home-screen",
        h("header", { class: "home-head" },
          h("div", { class: "home-head-title" },
            skeletonLine("w-30"),
            skeletonLine("w-40")),
          h("div", { class: "home-head-counts" },
            skeletonLine("w-30"),
            skeletonLine("w-30"),
            skeletonLine("w-30"))),
        h("div", { class: "home-brief-grid" },
          skeletonSection(skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80")),
          skeletonSection(skeletonLine("w-80"), skeletonLine("w-60")),
          skeletonSection(skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80"))),
        h("div", { class: "surface home-more home-more-skeleton" }, skeletonLine("w-30")));
    }
    if (name === "brief") {
      return skeletonScreen("brief-screen",
        h("header", { class: "brief-head" },
          h("div", { class: "brief-head-copy" },
            skeletonLine("w-30"),
            skeletonLine("w-40")),
          h("div", { class: "brief-stats-host" },
            h("div", { class: "brief-stats" },
              h("div", { class: "stat" }, skeletonLine("w-30"), skeletonLine("w-40")),
              h("div", { class: "stat" }, skeletonLine("w-30"), skeletonLine("w-40")),
              h("div", { class: "stat" }, skeletonLine("w-30"), skeletonLine("w-40")),
              h("div", { class: "brief-stat" }, skeletonLine("w-30"), skeletonLine("w-40"))))),
        h("div", { class: "brief-grid" },
          h("div", { class: "surface skeleton-panel brief-section-must" },
            skeletonLine("w-30"), skeletonLine("w-80"), skeletonLine("w-60")),
          skeletonPanel(skeletonLine("w-30"), skeletonLine("w-60"), skeletonLine("w-40")),
          skeletonPanel(skeletonLine("w-30"), skeletonLine("w-60"), skeletonLine("w-40"))));
    }
    if (name === "mail") {
      return skeletonScreen("mail-screen",
        h("div", { class: "mail-head" }, skeletonLine("w-30"), skeletonLine("w-60")),
        h("div", { class: "mail-layout" },
          skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80")),
          skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60"))));
    }
    if (name === "companies") {
      return skeletonScreen("companies-screen",
        h("div", { class: "companies-layout" },
          h("aside", { class: "surface companies-list-pane" },
            h("div", { class: "companies-list-head" },
              skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60")),
            h("div", { class: "companies-skeleton-list" },
              Array.from({ length: 7 }, () => h("div", { class: "companies-skeleton-row" },
                skeletonLine("w-40"), skeletonLine("w-60"))))),
          h("section", { class: "surface companies-profile companies-profile-skeleton-surface" },
            h("div", { class: "companies-profile-head" },
              skeletonLine("w-30"), skeletonLine("w-60"), skeletonLine("w-40")),
            h("div", { class: "companies-profile-skeleton" },
              skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80")))));
    }
    if (name === "ask") {
      return skeletonScreen("ask-screen",
        h("div", { class: "ask-layout" },
          h("div", { class: "ask-main" }, skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60"))),
          h("aside", { class: "ask-history" }, skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60")))));
    }
    if (name === "labels") {
      return skeletonScreen("",
        h("div", { class: "labels-task-layout" },
          skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60")),
          skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60"))));
    }
    if (name === "settings") {
      return skeletonScreen("",
        h("div", { class: "settings-layout" },
          skeletonPanel(skeletonLine("w-40"), skeletonLine("w-60"), skeletonLine("w-40")),
          skeletonPanel(skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80"))));
    }
    return skeletonScreen("",
      skeletonPanel(skeletonLine("w-30"), skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80")));
  }

  async function renderRouteNode(route, seq) {
    let node;
    try {
      switch (route.name) {
        case "hub": node = await renderHub(route, seq); break;
        // Brief paints its shell synchronously; each section fills in place.
        case "brief": node = renderBrief(route, seq); break;
        case "companies": node = await renderCompanies(route, seq); break;
        case "ask": node = await renderAsk(route, seq); break;
        case "sources": node = await renderSources(route, seq); break;
        case "labels": node = await renderLabels(route, seq); break;
        case "answers": node = await renderAnswers(route, seq); break;
        case "mail": node = await renderMail(route, seq); break;
        case "settings": node = await renderSettings(route, seq); break;
        case "learning": node = await renderLearning(route, seq); break;
        case "logs": node = await renderLogs(route, seq); break;
        default: node = await renderHub(route, seq); break;
      }
    } catch (error) {
      if (seq !== state.renderSeq) return null;
      node = errorScreen(error, () => refreshActiveRoute());
      reportError(error);
    }
    return node;
  }

  // The topbar indicator is the foreground refresh owner. A cached
  // revalidation paints the mounted screen and stays silent: it must never
  // outlive the screen it belongs to.
  function showUpdating(active) {
    if (!dom.topbarUpdate) return;
    dom.topbarUpdate.hidden = !active;
  }

  async function refreshActiveRoute() {
    routeRenderInFlight += 1;
    try {
      return await refreshActiveRouteNow();
    } finally {
      routeRenderInFlight -= 1;
    }
  }

  async function refreshActiveRouteNow() {
    if (!state.token || state.shuttingDown) return;
    // A focused input owns the mounted screen: building the replacement only
    // to throw it away wastes the route's reads and its DOM work. Refresh once
    // the focus leaves (the screen is otherwise refreshed on the next event
    // or navigation).
    if (!canReplaceMain()) {
      deferRefreshUntilBlur();
      return;
    }
    const route = parseRoute();
    const seq = ++state.renderSeq;
    showUpdating(true);
    const node = await renderRouteNode(route, seq);
    if (!node || seq !== state.renderSeq || state.route !== route.name) {
      showUpdating(false);
      return;
    }
    const key = routeCacheKey(route);
    if (key !== state.routeKey) {
      showUpdating(false);
      return;
    }
    if (isErrorScreen(node) && !isErrorScreen(dom.main.firstElementChild)) {
      showUpdating(false);
      return;
    }
    if (canReplaceMain()) {
      const scrolls = captureScrollState(dom.main);
      // A live refresh keeps focus where the owner put it. captureMainFocus
      // returns null outside #main, so the shell search and a dialog are
      // untouched; a control inside the replaced screen is restored instead
      // of falling to <body>.
      appendMain(node, 0, scrolls, true);
      if (routeFailed(node)) {
        routeCache.delete(key);
      } else {
        const entry = routeCache.get(key);
        if (entry) {
          entry.node = node;
          entry.at = Date.now();
          entry.scroll = 0;
          entry.scrolls = scrolls;
          entry.attach = typeof node.__routeAttach === "function" ? node.__routeAttach : null;
          touchRouteCache(key, entry);
        } else {
          touchRouteCache(key, routeCacheEntry(route, node, 0, scrolls));
        }
      }
    }
    showUpdating(false);
  }

  // refreshCachedRoute repaints a stale cached screen in the background.
  // The click already painted the cached node, so this never blocks the
  // menu. If the owner moves on, the topbar metadata is restored.
  function refreshCachedRoute(route, key, entry) {
    if (entry.refreshing) return;
    entry.refreshing = true;
    const seq = ++state.renderSeq;
    const savedMeta = Array.from(dom.topbarMeta.childNodes);
    const run = async () => {
      try {
        if (seq !== state.renderSeq || state.routeKey !== key) return;
        const node = await renderRouteNode(route, seq);
        if (seq !== state.renderSeq || state.routeKey !== key) {
          clear(dom.topbarMeta);
          for (const child of savedMeta) dom.topbarMeta.append(child);
          return;
        }
        // A background refresh that failed must leave the usable cached
        // screen in place. Storing the failure would turn a transient list
        // error into the next paint of this route.
        if (!node || routeFailed(node) || !canReplaceMain()) {
          return;
        }
        const scrolls = captureScrollState(dom.main);
        // A cached revalidation must not steal focus from the shell search.
        appendMain(node, 0, scrolls, false);
        entry.node = node;
        entry.at = Date.now();
        entry.stale = false;
        entry.scroll = 0;
        entry.scrolls = scrolls;
        entry.attach = typeof node.__routeAttach === "function" ? node.__routeAttach : null;
        touchRouteCache(key, entry);
      } finally {
        entry.refreshing = false;
      }
    };
    if (typeof window.requestIdleCallback === "function") window.requestIdleCallback(() => { run().catch(() => {}); }, { timeout: 1200 });
    else window.setTimeout(() => { run().catch(() => {}); }, 80);
  }

  // restoreCachedAllMail re-seats state.allMail before a cached All-Mail
  // node is re-appended. The node owns a snapshot of the model it was
  // rendered with, so a cached hit is valid only while that snapshot still
  // describes the route's role and sender. An entry without a matching
  // snapshot is discarded by the caller and rendered fresh: a visually
  // restored pane whose model belongs to another filter would have dead
  // Load more and scroll paths.
  function restoreCachedAllMail(route, seq, entry) {
    if (route.name !== "mail" || parseMode(route) !== "all") return true;
    const list = entry.node.querySelector(".mail-list");
    const more = list ? list.querySelector(".pager") : null;
    const role = allMailRole(route.params.get("role"));
    const sender = allMailSender(route.params.get("sender"));
    const unread = allMailUnread(route.params.get("unread"));
    const snapshot = list ? list.__allMailModel : null;
    if (!list || !more || !snapshot || !snapshot.loaded || snapshot.role !== role || snapshot.sender !== sender || snapshot.unread !== unread) return false;
    state.allMail = {
      role: snapshot.role,
      sender: snapshot.sender,
      unread: snapshot.unread,
      rows: snapshot.rows,
      total: snapshot.total,
      counts: snapshot.counts,
      next: snapshot.next,
      hasMore: snapshot.hasMore,
      capped: !!snapshot.capped,
      seen: allMailSeenRows(snapshot.rows),
      loaded: snapshot.loaded,
      promise: null,
      loading: false,
      morePromise: null,
    };
    paintAllMailMore(more, list, seq, role, sender, unread);
    observeAllMailMore(list, more, seq, role, sender, unread);
    return true;
  }

  // restoreCachedPaging re-seats a paged screen's model from the token its
  // rendered node carries. A cached mount re-appends the node without running
  // the screen's render, so this is the only place its model comes back. A
  // node without a token cannot page and is rebuilt fresh; an Important model
  // older than the priority TTL is a cold read, not a painted ranking.
  function restoreCachedPaging(route, node) {
    if (route.name === "answers") {
      const page = readPagingToken(node);
      if (!page || page.screen !== "answers") return false;
      pagingToken(node, page);
      state.answersPage = page;
      return true;
    }
    if (route.name !== "mail") return true;
    const mode = parseMode(route);
    if (mode !== "important" && mode !== "recent") return true;
    const page = readPagingToken(node);
    if (!page || page.screen !== mode) return false;
    if (mode === "important" &&
        Date.now() - (Number(page.at) || 0) > CACHE_TTL.priority) return false;
    pagingToken(node, page);
    if (mode === "recent") state.triagePage = page;
    return true;
  }

  // syncCachedCompanyDetail applies a company key that changed without
  // changing the screen identity. The list node and its scroll stay exactly
  // where they are; only the profile pane is replaced.
  function syncCachedCompanyDetail(route) {
    if (!route || route.name !== "companies") return;
    const host = document.querySelector(".companies-layout > .companies-profile");
    if (!host) return;
    const requested = String(route.params.get("key") || "").trim();
    // An empty key means the first row. A key outside the ranked list (a
    // deep link from Brief or Ask) is still a real profile; it has no row,
    // so its index is the -1 sentinel and the detail key is tracked in JS,
    // never in the DOM.
    const index = requested ? state.companyKeys.indexOf(requested) : (state.companyKeys.length ? 0 : -1);
    const key = index >= 0 ? state.companyKeys[index] : requested;
    if (!key) return;
    syncCompanySelection(index);
    if (state.companyDetailKey === key && state.companyDetailFailedKey !== key &&
        !host.classList.contains("companies-profile-updating")) return;
    if (state.companyDetailPendingKey === key) return;
    loadCompanyDetail(key, host, index);
  }

  // syncCachedAnswersDetail keeps a cached Answers screen honest when the
  // URL names a different stored answer. The route key excludes the message
  // id, so the loaded pages, the table and the answers-body scroll stay
  // mounted; only the detail host is patched.
  function syncCachedAnswersDetail(route) {
    if (!route || route.name !== "answers") return;
    const host = document.querySelector(".answers-screen .answers-detail-host");
    if (!host) return;
    syncAnswersDetail(
      host,
      Math.max(0, Number(route.params.get("message")) || 0),
      route.params.get("question") || "",
      state.renderSeq,
      { reveal: false });
  }

  // syncCachedMailDetail keeps a cached Mail list honest when the URL names
  // a different message than the detail pane currently shows.
  function syncCachedMailDetail(route) {
    if (route.name !== "mail") return;
    const mode = route.params.get("mode") || "important";
    const panel = document.getElementById("mail-detail");
    if (!panel) return;
    let id = Number(route.params.get("id") || 0);
    if (!id) {
      // A cached Conversations screen has no id in the URL until a row is
      // clicked. Repaint its first thread, not an empty detail pane.
      if (mode !== "conversations" || panel.dataset.messageId) return;
      const pane = document.querySelector(".mail-list-pane.conversations-pane");
      id = Number(pane && pane.dataset.selectedMessageId) || 0;
      if (!id) {
        const view = state.conversationPages.get(conversationViewKey(route.params.get("state"), route.params.get("q")));
        id = view && view.rows.length ? Number(view.rows[0].last_message_id) || 0 : 0;
      }
      if (!id) return;
    }
    // The list pane is part of the cached screen too: a company or Home
    // deep link must move the selected row, not only the detail body.
    syncMailListSelection(id);
    if (Number(panel.dataset.messageId || 0) === id) return;
    loadMailDetail(id, panel, { preview: state.triagePreview, mode, routed: state.triageRouted });
  }

  // force rebuilds the mounted body even when the route identity did not
  // change. A live language switch repaints the chrome and must not leave
  // the old catalog's nodes in #main.
  async function renderRoute(force = false) {
    routeRenderInFlight += 1;
    try {
      return await renderRouteNow(force);
    } finally {
      routeRenderInFlight -= 1;
    }
  }

  async function renderRouteNow(force = false) {
    if (!state.token || state.shuttingDown) return;
    const route = parseRoute();
    state.route = route.name;
    if (route.name !== "labels" && state.labelKeyHandler) {
      document.removeEventListener("keydown", state.labelKeyHandler);
      state.labelKeyHandler = null;
    }
    if (route.name !== "labels" && state.labelObserver) {
      state.labelObserver.disconnect();
      state.labelObserver = null;
    }
    if (route.name !== "labels" && state.labelChipObserver) {
      state.labelChipObserver.disconnect();
      state.labelChipObserver = null;
    }
    if (route.name !== "companies" && state.companyObserver) {
      state.companyObserver.disconnect();
      state.companyObserver = null;
    }
    const key = routeCacheKey(route);
    const sameRoute = !force && state.routeKey === key;
    storeActiveRoute();
    setHeader(route.name);
    // A query typed while the next screen was still rendering belongs to that
    // screen: it must not be wiped by the route change (the screen's own
    // setScopedSearch adopts the surviving value).
    const typedSearch = !sameRoute && document.activeElement === dom.globalSearchInput
      ? dom.globalSearchInput.value : "";
    if (!sameRoute) clearScopedSearch();
    if (typedSearch) {
      dom.globalSearchInput.value = typedSearch;
      if (dom.globalSearchClear) dom.globalSearchClear.hidden = false;
    }
    syncGlobalSearch(route);

    if (sameRoute) {
      // Clicking the active destination keeps the mounted screen and both
      // scroll positions. A company key is detail state: swap only the
      // profile node so the list never flashes or jumps.
      if (route.name === "companies") syncCachedCompanyDetail(route);
      if (route.name === "answers") syncCachedAnswersDetail(route);
      attachCachedRoute();
      // The mounted screen kept its registration, but the shell bar is the
      // router's to repaint on every mount: a same-route click must not leave
      // a global placeholder over a screen that still filters.
      activateScopedSearch(route.name);
      return;
    }
    setTopbarMeta(...quickRouteMeta(route));

    state.routeKey = key;
    // A forced rebuild never paints the old language's markup, even if a
    // caller forgot to invalidate the cache first.
    const cachedEntry = force ? null : routeCache.get(key);
    const seq = ++state.renderSeq;

    if (cachedEntry && cachedEntry.node) {
      if (restoreCachedAllMail(route, seq, cachedEntry) && restoreCachedPaging(route, cachedEntry.node)) {
        appendMain(cachedEntry.node, 0, cachedEntry.scrolls || null);
        touchRouteCache(key, cachedEntry);
        if (state.askFocusPending) focusAskInput();
        syncCachedMailDetail(route);
        syncCachedCompanyDetail(route);
        syncCachedAnswersDetail(route);
        attachCachedRoute(key);
        // A cached screen is only markup: its render never runs again, so the
        // registration made on the original mount is what restores the
        // filter placeholder, accessible name and value here.
        activateScopedSearch(route.name);
        // A navigation that started from the sidebar leaves focus on that
        // link. The fresh-render path returns it to #main; a cached mount
        // must do the same or the screen's own shortcuts never see a key.
        restoreNavigationFocus();
        if (cachedEntry.stale || Date.now() - (cachedEntry.at || 0) > ROUTE_FRESH_MS) {
          refreshCachedRoute(route, key, cachedEntry);
        }
        return;
      }
      routeCache.delete(key);
    }

    // The skeleton and the real screen are two mounts of one navigation.
    // Capture the trigger once, before the skeleton clears #main, and hand it
    // to the final mount; otherwise the second capture sees only <body> and a
    // route switch loses the keyboard position.
    const carriedFocus = captureMainFocus();
    appendMain(routeSkeleton(route.name), 0, null, false);
    showUpdating(false);
    const node = await renderRouteNode(route, seq);
    if (!node || seq !== state.renderSeq || state.routeKey !== key) return;
    appendMain(node, 0, null, true, carriedFocus);
    if (!routeFailed(node)) {
      touchRouteCache(key, routeCacheEntry(route, node));
    } else {
      routeCache.delete(key);
    }
    // The screen registered its mounted node while rendering; re-applying the
    // descriptor here makes the router the one mount point that owns the
    // shell bar, exactly like the cached path above.
    activateScopedSearch(route.name);
    restoreNavigationFocus();
  }

  // restoreNavigationFocus puts the keyboard where the navigation left it, and
  // is the one rule both mount paths agree on. A mount returns focus to #main
  // when the trigger was a link (otherwise the screen's own shortcuts never
  // see a key), but it never pulls focus out of the shell search bar: that bar
  // is the mounted screen's own filter, so stealing focus would stop the owner
  // mid-query and hand the screen to the next background repaint.
  function restoreNavigationFocus() {
    if (state.askFocusPending) {
      focusAskInput();
      return;
    }
    const active = document.activeElement;
    if (active === dom.globalSearchInput) return;
    if (!dom.main.contains(active)) dom.main.focus({ preventScroll: true });
  }

  // PREFETCH_UNSAFE names the screens whose render writes module-level
  // state. A prefetch runs before mount, so it would mutate the screen the
  // owner is using (Teach answers/task sequence, Companies selection,
  // Answers page, Settings query/tab, Mail list models, Search results).
  // Those screens are warmed on navigation only until their render has a
  // shadow context; removing an entry is the follow-up when it does.
  const PREFETCH_UNSAFE = new Set(["labels", "companies", "answers", "settings", "mail", "search"]);

  // Nav prefetch warms the route DOM cache without switching screens. It
  // renders with the current render sequence, restores the topbar metadata,
  // and drops the result if a real navigation started meanwhile.
  function scheduleNavPrefetch(anchor) {
    if (!anchor) return;
    scheduleRoutePrefetch(anchor.getAttribute("href") || "");
  }

  // scheduleRoutePrefetch builds one destination at idle, at most once per
  // href. A hover/focus uses it; the core warm-up uses it for the primary
  // screen so the first click is an append, not a fetch plus render.
  function scheduleRoutePrefetch(href) {
    if (!href.startsWith("#/") || navPrefetchLinks.has(href)) return;
    navPrefetchLinks.add(href);
    const run = () => {
      const attempt = () => prefetchRoute(parseRouteFromHash(href));
      const queued = prefetchQueue.then(attempt, attempt);
      // A prefetch that lost a race with a real navigation did not warm the
      // screen. Drop the once-only marker so a later idle or hover retries.
      prefetchQueue = queued.then((cached) => {
        if (!cached) navPrefetchLinks.delete(href);
      }, () => { navPrefetchLinks.delete(href); });
    };
    if (typeof window.requestIdleCallback === "function") window.requestIdleCallback(run, { timeout: 1200 });
    else window.setTimeout(run, 40);
  }

  async function prefetchRoute(route) {
    if (!state.token || state.shuttingDown || routeRenderInFlight > 0) return false;
    const key = routeCacheKey(route);
    if (key === state.routeKey || routeCache.has(key)) return true;
    // The destination's render owns module state; rendering it here would
    // write over the mounted screen. The href stays marked so a later hover
    // does not retry, and a real navigation renders the screen normally.
    if (PREFETCH_UNSAFE.has(route.name)) return true;
    // Building the default Mail All destination while an All-Mail list is
    // mounted would replace state.allMail and disconnect its one observer.
    if (route.name === "mail" && parseMode(route) === "all" &&
        state.route === "mail" && parseMode(parseRoute()) === "all") return false;
    if (routePrefetchInflight.has(key)) return true;
    routePrefetchInflight.add(key);
    const seq = state.renderSeq;
    const activeRoute = state.routeKey;
    const savedMeta = Array.from(dom.topbarMeta.childNodes);
    prefetchActive += 1;
    prefetchErrorSuppressUntil = Date.now() + 8000;
    let node = null;
    try {
      node = await renderRouteNode(route, seq);
    } finally {
      prefetchActive = Math.max(0, prefetchActive - 1);
      routePrefetchInflight.delete(key);
      if (seq === state.renderSeq && state.routeKey === activeRoute) {
        clear(dom.topbarMeta);
        for (const child of savedMeta) dom.topbarMeta.append(child);
      }
    }
    if (!node || !node.firstChild || seq !== state.renderSeq || state.routeKey !== activeRoute) return false;
    if (routeFailed(node)) return false;
    if (!routeCache.has(key)) touchRouteCache(key, routeCacheEntry(route, node));
    return true;
  }

  function installNavPrefetch() {
    for (const item of document.querySelectorAll("[data-nav]")) {
      item.addEventListener("pointerenter", () => scheduleNavPrefetch(item), { passive: true });
      item.addEventListener("focus", () => scheduleNavPrefetch(item));
    }
  }
