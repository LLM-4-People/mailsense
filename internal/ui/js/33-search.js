  // ---------------------------------------------------------- global search

  // The shell search box is the same element on every route: submitting it
  // opens the Mail search screen, and typing opens a local listbox of the
  // best matches. Operators narrow the query without a second form:
  // from:/sender:, in:<role>, has:attachment, -has:attachment.

  const GLOBAL_SEARCH_MIN = 2;
  const GLOBAL_SEARCH_LIMIT = 6;
  let globalSearchTimer = 0;
  let globalSearchSeq = 0;
  let globalSearchHits = [];
  let globalSearchActive = -1;

  function parseGlobalQuery(raw) {
    const rawText = String(raw || "").trim();
    const out = { q: [], sender: "", role: "", attach: "" };
    for (const token of rawText.split(/\s+/)) {
      let match = token.match(/^(?:from|sender):(.+)$/i);
      if (match) {
        out.sender = match[1].replace(/^["']|["']$/g, "");
        continue;
      }
      match = token.match(/^in:(.+)$/i);
      const role = match ? match[1].toLowerCase() : "";
      if (role && SEARCH_ROLES.includes(role)) {
        out.role = role;
        continue;
      }
      if (/^has:(?:attachment|file)$/i.test(token)) {
        out.attach = "yes";
        continue;
      }
      if (/^-has:(?:attachment|file)$/i.test(token)) {
        out.attach = "no";
        continue;
      }
      out.q.push(token);
    }
    const q = out.q.join(" ").trim();
    // A query made only of operators still shows the typed text to the
    // search screen, where the filters are visible and editable.
    return { q: q || rawText, sender: out.sender, role: out.role, attach: out.attach };
  }

  function globalSearchValue() {
    return dom.globalSearchInput ? dom.globalSearchInput.value.trim() : "";
  }

  function closeGlobalSearch() {
    globalSearchSeq += 1;
    globalSearchHits = [];
    globalSearchActive = -1;
    if (dom.globalSearchResults) {
      dom.globalSearchResults.hidden = true;
      clear(dom.globalSearchResults);
    }
    if (dom.globalSearchInput) {
      dom.globalSearchInput.setAttribute("aria-expanded", "false");
      dom.globalSearchInput.removeAttribute("aria-activedescendant");
    }
  }

  function globalSearchNote(key, detail) {
    globalSearchHits = [];
    globalSearchActive = -1;
    clear(dom.globalSearchResults);
    // A listbox owns options, not paragraphs: a disabled option carries the
    // note without breaking the list's required children.
    dom.globalSearchResults.append(h("p", { class: "topbar-suggest-note", role: "option", "aria-disabled": "true", text: t(key) }));
    if (detail) dom.globalSearchResults.append(h("p", { class: "topbar-suggest-note fine", role: "option", "aria-disabled": "true", text: detail }));
    dom.globalSearchResults.hidden = false;
    dom.globalSearchInput.setAttribute("aria-expanded", "true");
  }

  function setGlobalSearchActive(index) {
    if (!globalSearchHits.length) return;
    const count = globalSearchHits.length;
    globalSearchActive = ((index % count) + count) % count;
    for (const node of dom.globalSearchResults.querySelectorAll("[data-index]")) {
      const active = Number(node.dataset.index) === globalSearchActive;
      node.classList.toggle("active", active);
      node.setAttribute("aria-selected", active ? "true" : "false");
    }
    const activeNode = dom.globalSearchResults.querySelector('[data-index="' + globalSearchActive + '"]');
    if (activeNode) dom.globalSearchInput.setAttribute("aria-activedescendant", activeNode.id);
  }

  function openGlobalSearchHit(hit, parsed) {
    closeGlobalSearch();
    navigate("mail", {
      mode: "search", q: parsed.q, sender: parsed.sender, role: parsed.role,
      attach: parsed.attach, id: String(hit.row_id),
    });
  }

  function submitGlobalSearch() {
    const parsed = parseGlobalQuery(globalSearchValue());
    if (!parsed.q) {
      dom.globalSearchInput.focus();
      return;
    }
    closeGlobalSearch();
    navigate("mail", { mode: "search", q: parsed.q, sender: parsed.sender, role: parsed.role, attach: parsed.attach });
  }

  function renderGlobalSearchResults(parsed, result) {
    const hits = Array.isArray(result && result.hits) ? result.hits.slice(0, GLOBAL_SEARCH_LIMIT) : [];
    clear(dom.globalSearchResults);
    globalSearchHits = hits;
    globalSearchActive = -1;
    for (const [index, hit] of hits.entries()) {
      const sender = hit.sender_name || hit.sender_address || t("mail.sender_unknown");
      const row = h("button", {
        type: "button", id: "global-search-option-" + index, tabIndex: -1,
        class: "topbar-suggest", role: "option", "aria-selected": "false",
        dataset: { index: String(index) },
      },
      h("span", { class: "topbar-suggest-main" },
        h("span", { class: "topbar-suggest-subject", dir: "auto", text: hit.subject || t("mail.no_subject") }),
        h("span", { class: "topbar-suggest-meta", dir: "auto", text: metaLine([sender, formatDate(hit.instant)]) })),
      h("span", { class: "topbar-suggest-score", text: formatNumber(hit.score || 0, 2) }));
      row.addEventListener("click", () => openGlobalSearchHit(hit, parsed));
      row.addEventListener("mousemove", () => setGlobalSearchActive(index));
      dom.globalSearchResults.append(row);
    }
    if (!hits.length) {
      dom.globalSearchResults.append(h("p", { class: "topbar-suggest-note", role: "option", "aria-disabled": "true", text: t("search.no_results_title") }));
    }
    const all = h("button", { type: "button", tabIndex: -1, class: "topbar-suggest topbar-suggest-all", role: "option", "aria-selected": "false" },
      h("span", { class: "topbar-suggest-main" },
        h("span", { class: "topbar-suggest-subject", text: t("search.open_all") }),
        h("span", { class: "topbar-suggest-meta", text: parsed.q })),
      icon("search"));
    all.addEventListener("click", submitGlobalSearch);
    dom.globalSearchResults.append(all);
    dom.globalSearchResults.hidden = false;
    dom.globalSearchInput.setAttribute("aria-expanded", "true");
  }

  // loadGlobalSearch fills the omnibox. Suggestions are text-only: every
  // pause in a typed prefix must not start the local embedding model. The
  // explicit Search page (renderSearch) keeps the semantic default.
  async function loadGlobalSearch() {
    const raw = globalSearchValue();
    const parsed = parseGlobalQuery(raw);
    const seq = ++globalSearchSeq;
    if (raw.length < GLOBAL_SEARCH_MIN) {
      closeGlobalSearch();
      return;
    }
    try {
      const result = await requestCached(
        "/api/search?q=" + encodeURIComponent(parsed.q) + "&limit=" + GLOBAL_SEARCH_LIMIT + "&semantic=off", {}, CACHE_TTL.search);
      if (seq !== globalSearchSeq) return;
      renderGlobalSearchResults(parsed, result);
    } catch (error) {
      if (seq !== globalSearchSeq) return;
      if (error && (error.status === 423 || error.code === "learning_locked")) globalSearchNote("search.locked");
      else globalSearchNote("search.global_error", apiFailure(error).message);
    }
  }

  function scheduleGlobalSearch() {
    if (globalSearchTimer) window.clearTimeout(globalSearchTimer);
    globalSearchTimer = window.setTimeout(() => {
      globalSearchTimer = 0;
      void loadGlobalSearch();
    }, 220);
  }

  function focusGlobalSearch() {
    if (!dom.globalSearchInput) {
      requestAskFocus();
      return;
    }
    dom.globalSearchInput.focus();
    dom.globalSearchInput.select();
  }

  // handleGlobalShortcut is the one owner of the shell keyboard shortcuts.
  // The top document and every message frame call it: a key event inside a
  // frame never reaches the parent document, so the frame bridges here.
  // "/" (including the numeric keypad) focuses the one search box; Ctrl/Cmd+K
  // opens Ask. Text fields keep both keys.
  function handleGlobalShortcut(event) {
    if (event.defaultPrevented || event.altKey) return;
    // A modal owns focus: the shell shortcut must not navigate or focus an
    // input behind it.
    if (state.modalOpen || document.querySelector("dialog[open]")) return;
    const key = String(event.key || "").toLowerCase();
    if ((event.ctrlKey || event.metaKey) && !event.shiftKey && key === "k") {
      event.preventDefault();
      requestAskFocus();
      return;
    }
    const slash = event.key === "/" || key === "divide" || event.code === "NumpadDivide";
    if (slash && !event.ctrlKey && !event.metaKey && !typingTarget(event.target)) {
      event.preventDefault();
      focusGlobalSearch();
    }
  }

  // scopedSearch turns the one shell search box into the current screen's
  // filter when that screen owns a list: the box never moves, and no second
  // search bar is rendered. Every route keeps its own descriptor so a cached
  // mount can restore the binding after a Back: the screen supplies its own
  // apply callback, its placeholder key, its accessible name and a value
  // getter for Back/re-render.
  //
  // scopedSearches: route -> { apply, placeholderKey, ariaKey, valueFn }
  const scopedSearches = new Map();
  let activeScopedRoute = "";

  function scopedSearchActive() {
    return activeScopedRoute !== "" && state.route === activeScopedRoute;
  }

  function setScopedSearch(route, apply, placeholderKey, ariaKey, valueFn) {
    scopedSearches.set(route, { apply, placeholderKey, ariaKey, valueFn: valueFn || null });
    if (state.route === route) activateScopedSearch(route);
  }

  // activateScopedSearch applies the route's registered descriptor to the
  // one shell bar. The router calls it after every mount: a cached screen
  // does not run its render again, so its registration is the only copy of
  // the binding left.
  function activateScopedSearch(route) {
    if (state.route !== route) return;
    const scoped = scopedSearches.get(route);
    if (!scoped || !dom.globalSearchInput) return;
    activeScopedRoute = route;
    dom.globalSearchInput.placeholder = t(scoped.placeholderKey);
    dom.globalSearchInput.setAttribute("aria-label", t(scoped.ariaKey));
    let value = scoped.valueFn ? String(scoped.valueFn() || "") : "";
    // The owner may have typed while this screen was rendering: a slow mount
    // can land between two keystrokes, and the route it paints still carries
    // the query of the previous one. While the bar holds focus its text is
    // newer than the route, so the screen adopts it and filters with it
    // instead of erasing the last character.
    const typed = document.activeElement === dom.globalSearchInput ? dom.globalSearchInput.value : "";
    if (typed && typed !== value) {
      value = typed;
      scoped.apply(value);
    } else if (!value && dom.globalSearchInput.value) {
      value = dom.globalSearchInput.value;
      scoped.apply(value);
    }
    if (dom.globalSearchInput.value !== value) dom.globalSearchInput.value = value;
    if (dom.globalSearchClear) dom.globalSearchClear.hidden = !value;
    closeGlobalSearch();
  }

  // clearScopedSearch deactivates the shell bar for a global route but keeps
  // every route's registration: a cached mount restores it without a render.
  function clearScopedSearch() {
    activeScopedRoute = "";
    if (!dom.globalSearchInput) return;
    dom.globalSearchInput.placeholder = t("search.global_placeholder");
    dom.globalSearchInput.setAttribute("aria-label", t("search.global_aria"));
    dom.globalSearchInput.value = "";
    if (dom.globalSearchClear) dom.globalSearchClear.hidden = true;
    closeGlobalSearch();
  }

  function applyScopedSearch(value) {
    const scoped = scopedSearches.get(activeScopedRoute);
    if (scoped) scoped.apply(String(value || ""));
  }

  function onGlobalSearchKeydown(event) {
    if (scopedSearchActive()) {
      if (event.key === "Enter") {
        event.preventDefault();
        applyScopedSearch(dom.globalSearchInput.value);
      } else if (event.key === "Escape") {
        dom.globalSearchInput.blur();
      }
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (dom.globalSearchResults.hidden) void loadGlobalSearch();
      else setGlobalSearchActive(globalSearchActive + 1);
      return;
    }
    if (event.key === "ArrowUp") {
      if (dom.globalSearchResults.hidden || !globalSearchHits.length) return;
      event.preventDefault();
      setGlobalSearchActive(globalSearchActive < 0 ? globalSearchHits.length - 1 : globalSearchActive - 1);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      if (globalSearchActive >= 0 && globalSearchHits[globalSearchActive]) {
        openGlobalSearchHit(globalSearchHits[globalSearchActive], parseGlobalQuery(globalSearchValue()));
      } else {
        submitGlobalSearch();
      }
      return;
    }
    if (event.key === "Escape") {
      if (!dom.globalSearchResults.hidden) closeGlobalSearch();
      else dom.globalSearchInput.blur();
    }
  }

  // A deep link or the Back button can land on the search screen; the shell
  // box then shows the query that screen is displaying. Every route starts
  // unscoped; a screen that owns a list registers its filter while rendering.
  function syncGlobalSearch(route) {
    if (!dom.globalSearchInput) return;
    if (route.name === "mail" && route.params.get("mode") === "search") {
      const q = route.params.get("q") || "";
      if (q && dom.globalSearchInput.value !== q) dom.globalSearchInput.value = q;
    }
    if (dom.globalSearchClear) dom.globalSearchClear.hidden = !dom.globalSearchInput.value;
  }

  function initGlobalSearch() {
    if (!dom.globalSearch || !dom.globalSearchInput) return;
    dom.globalSearch.addEventListener("submit", (event) => {
      event.preventDefault();
      if (scopedSearchActive()) {
        applyScopedSearch(dom.globalSearchInput.value);
        return;
      }
      submitGlobalSearch();
    });
    dom.globalSearchInput.addEventListener("input", () => {
      dom.globalSearchClear.hidden = !dom.globalSearchInput.value;
      if (scopedSearchActive()) {
        applyScopedSearch(dom.globalSearchInput.value);
        return;
      }
      scheduleGlobalSearch();
    });
    dom.globalSearchInput.addEventListener("keydown", onGlobalSearchKeydown);
    dom.globalSearchInput.addEventListener("focus", () => {
      if (scopedSearchActive()) return;
      if (globalSearchValue().length >= GLOBAL_SEARCH_MIN && dom.globalSearchResults.hidden) void loadGlobalSearch();
    });
    dom.globalSearchClear.addEventListener("click", () => {
      dom.globalSearchInput.value = "";
      dom.globalSearchClear.hidden = true;
      if (scopedSearchActive()) {
        applyScopedSearch("");
        dom.globalSearchInput.focus();
        return;
      }
      closeGlobalSearch();
      dom.globalSearchInput.focus();
    });
    document.addEventListener("click", (event) => {
      if (dom.globalSearchResults.hidden) return;
      if (!dom.globalSearch.contains(event.target)) closeGlobalSearch();
    });
    dom.globalSearchClear.hidden = !dom.globalSearchInput.value;
    dom.globalSearchInput.setAttribute("aria-expanded", "false");
  }

  function searchTermModeLabel(mode) {
    const key = "search.term_mode." + String(mode || "");
    return I18N.has(key) ? t(key) : String(mode || "");
  }

  // searchFilterCard is the one owner of the search filter controls: the
  // selects navigate through the route (so a filter change keeps the same
  // query), and the two clear actions differ. The sender chip drops only the
  // sender; the card's Clear drops every filter, including the sender.
  function searchFilterCard({ q, sender, store, role, attach, mailboxes, mailboxesError }) {
    const storeOptions = h("option", { value: "", text: t("search.any_mailbox") });
    const seenStores = new Set();
    for (const box of Array.isArray(mailboxes) ? mailboxes : []) {
      // The API filter is a mailbox store key (store-...), not the raw
      // connector store id: a raw id is rejected before the search runs.
      if (!box.key || seenStores.has(box.key)) continue;
      seenStores.add(box.key);
      storeOptions.append(h("option", { value: box.key, text: box.name || box.key, selected: box.key === store }));
    }
    const storeSelect = h("select", { class: "select compact", "aria-label": t("search.mailbox_aria") }, storeOptions);
    const roles = ["", ...SEARCH_ROLES];
    const roleSelect = h("select", { class: "select compact", "aria-label": t("search.role_aria") },
      roles.map((r) => h("option", { value: r, text: r ? allMailRoleLabel(r) : t("search.any_role"), selected: r === role })));
    const attachSelect = h("select", { class: "select compact", "aria-label": t("search.attachment_aria") },
      h("option", { value: "", text: t("search.any_attachment"), selected: attach === "" }),
      h("option", { value: "yes", text: t("search.with_attachments"), selected: attach === "yes" }),
      h("option", { value: "no", text: t("search.without_attachments"), selected: attach === "no" }));
    const filterNav = () => navigate("mail", {
      mode: "search", q, sender,
      store: storeSelect.value, role: roleSelect.value, attach: attachSelect.value,
    });
    storeSelect.addEventListener("change", filterNav);
    roleSelect.addEventListener("change", filterNav);
    attachSelect.addEventListener("change", filterNav);
    const keepFilters = () => ({
      mode: "search", q,
      store: storeSelect.value, role: roleSelect.value, attach: attachSelect.value,
    });
    const clearSender = () => navigate("mail", keepFilters());
    const clearFilters = () => navigate("mail", { mode: "search", q });
    return h("section", { class: "surface mail-search-card" },
      h("div", { class: "mail-search-head" },
        h("div", null,
          h("h2", { text: t("search.title") }),
          h("p", { class: "section-sub", text: t("search.bar_hint") }))),
      h("div", { class: "mail-search-filters" },
        senderChip(sender, clearSender),
        storeSelect, roleSelect, attachSelect,
        mailboxesError ? errorState(mailboxesError, null,
          Object.assign({}, INLINE_READ_ERROR, { title: t("read.mailboxes_unavailable") })) : null,
        store || role || attach ? button(t("search.clear_filters"), clearFilters, { small: true, kind: "quiet" }) : null,
        h("span", { class: "fine", text: t("search.filters_note") })));
  }

  async function renderSearch(route, seq, preview) {
    state.triageRouted = null;
    state.triagePreview = preview;
    const mailboxesRead = await settleRead(loadMailboxes());
    const mailboxes = mailboxesRead.value || [];
    const q = route.params.get("q") || "";
    const sender = route.params.get("sender") || "";
    const store = route.params.get("store") || "";
    const role = route.params.get("role") || "";
    const attach = route.params.get("attach") || "";
    // Every filter is the server's: it owns the match set and the total. The
    // old screen asked for one unfiltered page and then filtered those 20
    // rows client-side, so a filter could show zero while matching mail
    // existed beyond the page.
    const searchPath = () => {
      const params = ["q=" + encodeURIComponent(q)];
      if (sender) params.push("sender=" + encodeURIComponent(sender));
      if (store) params.push("store=" + encodeURIComponent(store));
      if (role) params.push("role=" + encodeURIComponent(role));
      if (attach) params.push("attach=" + encodeURIComponent(attach));
      return "/api/search?" + params.join("&");
    };
    let result = null;
    let searchError = null;
    if (q.trim()) {
      try { result = await requestCached(searchPath(), {}, CACHE_TTL.search); }
      catch (error) { searchError = error; }
    }
    if (seq !== state.renderSeq) return h("div");
    if (searchError) reportError(searchError);

    const form = searchFilterCard({ q, sender, store, role, attach, mailboxes, mailboxesError: mailboxesRead.error });

    if (!q.trim()) {
      return h("div", { class: "mail-search-empty" },
        emptyState(t("search.empty.title"), t("search.empty.body"), null, "search"));
    }
    if (searchError) {
      return h("div", { class: "mail-search-empty" }, form,
        errorState(searchError));
    }

    const hits = Array.isArray(result.hits) ? result.hits : [];
    const selectedId = Number(route.params.get("id")) || (hits[0] ? hits[0].row_id : 0);
    setTopbarMeta(
      gatePill(gateOpen()),
      preview ? previewPill() : null,
      pill(t("mail.tab.search")),
      pill(fmtCount(hits.length, Number(result.total) || 0, !!result.capped)),
      result.semantic && result.semantic.used ? pill(t("search.semantic_and_text"), "accent") : pill(t("search.text_only"), "pending"),
      pill(t("search.searched_in", { duration: formatDuration(result.took) })));
    const semanticUsed = !!(result.semantic && result.semantic.used);
    const notices = h("div", { class: "search-notices fine" },
      h("p", { text: semanticUsed ? t("search.notice.used") : t("search.notice.unavailable") }),
      result.recall ? h("p", { text: t("search.notice.recall") }) : null,
      result.notice ? diagnosticDetails(result.notice, { summary: "search.notice.diagnostic" }) : null,
      result.capped ? h("p", { text: t("search.capped_total") }) : null,
      result.text_matches_capped ? h("p", { text: t("search.capped") }) : null,
      result.terms && result.terms.length ? h("p", { text: t("search.terms", { terms: result.terms.map((term) => term.term + " (" + searchTermModeLabel(term.mode) + ")").join(", ") }) }) : null);
    const list = h("div", { class: "mail-list section-scroll", "aria-label": t("mail.list_aria"), dataset: { scrollKey: "mail-list" } });
    if (!hits.length) {
      const narrowed = !!(sender || store || role || attach);
      list.append(emptyState(narrowed ? t("search.no_filter_match_title") : t("search.no_results_title"),
        narrowed ? t("search.no_filter_match_body") : t("search.no_results_body"), null, "search"));
    } else {
      for (const hit of hits) list.append(searchRow(hit, selectedId));
    }
    const detail = mailDetailBody();
    if (selectedId) loadMailDetail(selectedId, detail, { preview, mode: "search", routed: null });
    else {
      clear(detail);
      detail.append(emptyState(t("search.select_result"), t("search.select_result_body"), null, "mail"));
    }
    return h("div", { class: "mail-search-results" },
      form, notices,
      mailWorkspace(mailPane(t("search.title"), fmtCount(hits.length, Number(result.total) || 0, !!result.capped), list), detail));
  }

  // fmtCount is the one result-count sentence. A capped search's total is a
  // lower bound, never an exact count.
  function fmtCount(shown, total, capped = false) {
    if (capped) return t("common.result_count_at_least", { count: total });
    return shown === total
      ? t("common.result_count", { count: total })
      : t("common.result_count_partial", { shown, total });
  }

  function searchRow(hit, selectedId) {
    const sender = hit.sender_name || hit.sender_address || t("mail.sender_unknown");
    return mailRow({
      tag: "button",
      className: "mail-row search-result" + (hit.row_id === selectedId ? " selected" : ""),
      dataset: { id: String(hit.row_id) },
      avatar: initials(sender),
      sender,
      timeText: formatDate(hit.instant),
      subject: hit.subject || t("mail.no_subject"),
      snippet: hit.snippet || "",
      snippetDir: null,
      why: [hit.role ? badge(allMailRoleLabel(hit.role)) : null,
        h("span", { class: "fine", text: t("search.table.score") + " " + formatNumber(hit.score || 0, 3) })],
      onClick: () => selectSearchHit(hit.row_id),
    });
  }

  function selectSearchHit(id) {
    const params = parseRoute().params;
    replaceRoute("mail", {
      mode: "search", q: params.get("q") || "", sender: params.get("sender") || "", store: params.get("store") || "",
      role: params.get("role") || "", attach: params.get("attach") || "", id: String(id),
    });
    syncMailListSelection(id);
    const panel = document.getElementById("mail-detail");
    if (panel) loadMailDetail(id, panel, { preview: state.triagePreview, mode: "search", routed: null });
  }

  function mailDetailPendingNode(id, item) {
    const subject = item && item.subject ? item.subject : t("mail.message_number", { id: formatId(id) });
    const sender = item ? metaLine([item.sender_name, item.sender_address]) : "";
    return h("div", { class: "mail-detail-skeleton", "aria-busy": "true" },
      h("div", { class: "detail-head" },
        h("div", null,
          h("h2", { class: "detail-title", dir: "auto", text: subject }),
          sender ? h("div", { class: "detail-sub", dir: "auto", text: sender }) : null,
          item && item.received_at ? h("div", { class: "fine", text: formatDate(item.received_at) }) : null)),
      skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-80"));
  }

  // appendMailDetailNode paints the message first, then starts the
  // conversation read. The message body never waits on the thread endpoint.
  // It is also the last gate: no caller can paint a detail without a usable
  // message read, which is what let a stored triage paint a bodyless stub.
  function appendMailDetailNode(panel, id, messageView, triage, triageError, context, questions) {
    clear(panel);
    if (!validMessageView(messageView)) {
      panel.append(errorState(new ApiError(0, { code: "invalid_response" }),
        () => loadMailDetail(id, panel, context)));
      return null;
    }
    const node = mailDetailNode(id, messageView, triage, triageError, context, questions);
    panel.append(node);
    const message = messageView.message;
    hydrateMailThread(node, id, message, context);
    return node;
  }

  // mail-detail-state:start
  // The detail state belongs to the message read alone. A stored triage result
  // describes a message whose body may already be gone, so its presence never
  // makes a bodyless view a message. A triage failure is supplementary: it is
  // returned with the ready state and painted as the inline banner.
  function mailDetailReadState(messageResult, triageResult) {
    if (messageResult.status === "rejected") {
      return removedMessageError(messageResult.reason)
        ? { state: "removed", error: messageResult.reason, triageError: null }
        : { state: "error", error: messageResult.reason, triageError: null };
    }
    if (!validMessageView(messageResult.value)) {
      return { state: "invalid", error: new ApiError(0, { code: "invalid_response" }), triageError: null };
    }
    return {
      state: "ready",
      error: null,
      triageError: triageResult && triageResult.status === "rejected" ? triageResult.reason : null,
    };
  }
  // mail-detail-state:end

  async function loadMailDetail(id, panel, context = {}) {
    if (!panel) return;
    panel.scrollTop = 0;
    panel.dataset.messageId = String(id);
    const requestSeq = (Number(panel.dataset.detailSeq) || 0) + 1;
    panel.dataset.detailSeq = String(requestSeq);
    const messagePreview = cached(messagePath(id));
    const triagePreview = cached("/api/triage/" + encodeURIComponent(id));
    const paintedQuestions = state.questions;
    // Only a usable message read paints a preview. A cached triage alone
    // describes a message this screen has not read yet.
    const previewPainted = validMessageView(messagePreview);
    if (previewPainted) {
      appendMailDetailNode(panel, id, messagePreview, triagePreview, null, context, paintedQuestions);
    } else {
      clear(panel);
      panel.append(mailDetailPendingNode(id, context.item));
    }
    const [messageResult, triageResult, questionsResult] = await Promise.allSettled([
      loadMessage(id),
      loadTriageDetail(id),
      loadQuestions(),
    ]);
    if (!panel.isConnected || panel.dataset.detailSeq !== String(requestSeq)) return;
    const read = mailDetailReadState(messageResult, triageResult);
    if (read.state === "removed") {
      clear(panel);
      panel.append(unavailableState(t("detail.unavailable")));
      return;
    }
    if (read.state !== "ready") {
      clear(panel);
      panel.append(errorState(read.error, () => loadMailDetail(id, panel, context)));
      return;
    }
    const messageView = messageResult.value;
    const triage = triageResult.status === "fulfilled" ? triageResult.value : null;
    const questions = questionsResult.status === "fulfilled" ? questionsResult.value : null;
    // The preview was painted from these exact cached objects: another paint
    // would recreate the iframe, its srcdoc and its inline-asset fetches for
    // the same pixels. `questions` is the one input outside those objects, so
    // a changed profile still repaints.
    if (previewPainted &&
      messageResult.status === "fulfilled" && triageResult.status === "fulfilled" &&
      messageView === messagePreview && triage === triagePreview &&
      read.triageError === null && questions === paintedQuestions) {
      return;
    }
    appendMailDetailNode(panel, id, messageView, triage, read.triageError, context, questions);
  }

  // A noul answer is the probability of yes. Certainty is the distance from
  // the undecided midpoint, so the same 0.6/0.8 thresholds read as likely and
  // definite on both the yes and the no side.
  function triageNoulReading(value) {
    if (!value || value.refused || value.abstain) return null;
    const probability = finiteNumber(value.number);
    if (probability === null) return null;
    // The server owns the class at the profile cutoff. The 0.5 fallback is
    // only for an older payload without `decided`.
    const decided = String(value.decided || "").trim().toLowerCase();
    const yes = decided === "yes" ? true : decided === "no" ? false : probability >= 0.5;
    const certainty = Math.max(probability, 1 - probability);
    return { probability, yes, certainty, likely: certainty >= 0.6, definite: certainty >= 0.8 };
  }

  function triageChoiceReading(value) {
    if (!value || value.refused || value.abstain) return null;
    const choice = String(value.choice || "").trim();
    if (!choice) return null;
    const confidence = finiteNumber(value.confidence);
    return { choice, confidence, uncertain: confidence !== null && confidence < 0.5 };
  }

  function triageScoreReading(value, q, fallbackMax) {
    if (!value || value.refused || value.abstain) return null;
    const level = finiteNumber(value.number);
    if (level === null) return null;
    const max = q && Array.isArray(q.levels) && q.levels.length > 1
      ? q.levels.length - 1
      : Math.max(0, Number.isFinite(Number(fallbackMax)) ? Number(fallbackMax) : 0);
    const decidedText = String(value.decided || "").trim();
    const decidedLevel = /^\d+$/.test(decidedText) ? Number(decidedText) : null;
    const confidence = finiteNumber(value.confidence);
    return {
      level: decidedLevel !== null
        ? Math.max(0, Math.min(max, decidedLevel))
        : Math.max(0, Math.min(max, Math.round(level))),
      confidence,
      uncertain: confidence !== null && confidence < 0.5,
    };
  }

  // The Mail detail uses the same stored-answer vocabulary as Answers and
  // the review flow. Only the detail's uncertainty wording stays local:
  // answerValueText owns the option, yes/no and level label.
  function triageChoiceLabel(questionName, reading) {
    if (!reading || reading.uncertain) return t("detail.summary.uncertain");
    const question = findQuestion(questionName) || { name: questionName, type: "choice", levels: [] };
    return answerValueText(question, reading.choice) || t("detail.summary.uncertain");
  }

  function triageScoreLabel(questionName, reading, fallback = "") {
    if (!reading) return fallback || t("detail.summary.uncertain");
    const question = findQuestion(questionName) || { name: questionName, type: "score", levels: [] };
    let label = answerValueText(question, reading.level);
    if (!label || label === t("common.dash")) label = fallback || t("detail.summary.uncertain");
    if (reading.uncertain) label += t("detail.summary.uncertain_suffix");
    return label;
  }

  function triageFacet(label, ...body) {
    return h("div", { class: "triage-facet" },
      h("h4", { class: "triage-facet-label", text: label }),
      body);
  }

  function triageAboutFacet(values, triage) {
    const category = triageChoiceReading(values.category);
    const topic = triageChoiceReading(values.topic);
    const hierarchy = triage && triage.subject && typeof triage.subject === "object" ? triage.subject : {};
    if (!category && !topic && !hierarchy.topic) return null;
    const categoryLabel = category ? triageChoiceLabel("category", category) : "";
    const topicLabel = topic ? triageChoiceLabel("topic", topic) : "";
    // The server owns the hierarchy. When it is present it replaces the
    // flat topic chip with the full path, so the screen never shows two
    // competing subject readings.
    const hierarchyLabel = hierarchy.topic ? subjectPathLabel(hierarchy.topic, hierarchy.subtopic) : "";
    const tags = [];
    if (categoryLabel) tags.push({ label: categoryLabel, kind: "accent" });
    const subjectLabel = hierarchyLabel || topicLabel;
    if (subjectLabel && subjectLabel !== categoryLabel) tags.push({ label: subjectLabel, kind: "" });
    const descriptions = [];
    if (category && !category.uncertain) {
      const key = "detail.summary.category." + category.choice;
      if (I18N.has(key)) descriptions.push(t(key));
    }
    if (topic && !topic.uncertain) {
      const key = "detail.summary.topic." + topic.choice;
      if (I18N.has(key)) descriptions.push(t(key));
    }
    return triageFacet(t("detail.summary.about"),
      tags.length ? h("div", { class: "triage-facet-tags" }, tags.map((tag) => badge(tag.label, tag.kind))) : null,
      descriptions.map((description) => h("p", { class: "triage-facet-desc", text: description })));
  }

  function triageAsksFacet(values) {
    const asks = triageChoiceReading(values.asks_for);
    const reply = triageNoulReading(values.reply_needed);
    if (!asks && !reply) return null;
    const children = [];
    if (asks) {
      if (asks.uncertain) {
        children.push(h("p", { class: "triage-facet-value", text: t("detail.summary.uncertain") }));
      } else {
        const key = "detail.summary.asks_for." + asks.choice;
        children.push(h("p", { class: "triage-facet-value", text: I18N.has(key) ? t(key) : t("detail.summary.asks_for.other") }));
      }
    }
    const fyi = asks && !asks.uncertain && asks.choice === "nothing_fyi";
    if (reply && !fyi) {
      if (!reply.likely) {
        children.push(h("p", { class: "triage-facet-note", text: t("detail.summary.reply_unclear") }));
      } else if (reply.yes) {
        children.push(h("div", { class: "triage-facet-badges" },
          badge(t("detail.summary.reply"), "accent", reply.definite ? "" : t("detail.summary.likely"))));
      } else {
        children.push(h("p", { class: "triage-facet-note", text: t("detail.summary.no_reply") }));
      }
    }
    return triageFacet(t("detail.summary.asks"), children);
  }

  function triageWhenFacet(map, values) {
    const deadline = triageScoreReading(values.deadline, map.get("deadline"), 5);
    if (!deadline) return null;
    return triageFacet(t("detail.summary.when"),
      h("p", { class: "triage-facet-value", text: triageScoreLabel("deadline", deadline) }));
  }

  function triageFromFacet(values) {
    const relationship = triageChoiceReading(values.relationship);
    if (!relationship) return null;
    return triageFacet(t("detail.summary.from"),
      h("p", { class: "triage-facet-value", text: triageChoiceLabel("relationship", relationship) }));
  }

  function triageAttentionFacet(map, values) {
    const urgency = triageScoreReading(values.urgency, map.get("urgency"), 4);
    const suspicious = triageNoulReading(values.suspicious);
    const suspiciousLikely = !!(suspicious && suspicious.yes && suspicious.likely);
    if (!urgency && !suspiciousLikely) return null;
    return triageFacet(t("detail.summary.attention"),
      urgency ? h("p", { class: "triage-facet-value", text: triageScoreLabel("urgency", urgency) }) : null,
      suspiciousLikely ? h("div", { class: "triage-facet-badges" }, badge(t("detail.summary.suspicious"), "warn")) : null);
  }

  function triageEnoughFacet(values) {
    const enough = triageNoulReading(values.info_sufficiency);
    if (!enough) return null;
    let label = t("detail.summary.uncertain");
    if (enough.likely) label = enough.yes ? t("detail.summary.sufficient") : t("detail.summary.insufficient");
    return triageFacet(t("detail.summary.enough"),
      h("p", { class: "triage-facet-value", text: label }));
  }

  function triageEffortFacet(map, values) {
    const effort = triageScoreReading(values.effort, map.get("effort"), 3);
    if (!effort) return null;
    return triageFacet(t("detail.summary.effort"),
      h("p", { class: "triage-facet-value", text: triageScoreLabel("effort", effort) }));
  }

  function triageLevelValue(q, value) {
    if (!q || !Array.isArray(q.levels) || !q.levels.length) return "";
    if (!value || value.refused || value.abstain) return "";
    const level = finiteNumber(value.number);
    if (level === null) return "";
    const index = Math.max(0, Math.min(q.levels.length - 1, Math.round(level)));
    return String(q.levels[index] || "");
  }

  // advancedAnswerValue is the debug formatter behind Model details: friendly
  // question and option names first, percentages and model level text second.
  function advancedAnswerValue(q, value) {
    if (!value) return t("common.dash");
    if (value.refused) return t("detail.answer.refused");
    if (value.abstain) {
      return value.abstain === "not_certified"
        ? t("detail.answer.not_certified")
        : t("detail.answer.abstained");
    }
    const type = q && q.type ? q.type : (value.type || "");
    const name = q && q.name ? q.name : "";
    if (type === "noul") {
      const reading = triageNoulReading(value);
      if (!reading) return t("common.dash");
      let label;
      if (!reading.likely) label = t("detail.summary.uncertain");
      else if (name === "suspicious" && !reading.yes) label = t("detail.summary.normal");
      else if (!reading.definite) label = t("detail.summary.likely_value", {
        value: reading.yes ? t("labels.answer.yes") : t("labels.answer.no"),
      });
      else label = reading.yes ? t("labels.answer.yes") : t("labels.answer.no");
      // A deterministic rule has no model probability; showing 100% would
      // claim a confidence it never stated.
      if (value.rule) return label;
      return metaLine([label, t("detail.answer.yes_probability", { percent: formatPercent(reading.probability, 0) })]);
    }
    if (type === "choice") {
      const reading = triageChoiceReading(value);
      if (!reading) return t("common.dash");
      const label = answerValueText(q || { name, type: "choice", levels: [] }, reading.choice) || t("detail.summary.uncertain");
      return reading.confidence === null
        ? label
        : metaLine([label, t("detail.table.confidence") + " " + formatPercent(reading.confidence, 0)]);
    }
    if (type === "score") {
      const reading = triageScoreReading(value, q, 0);
      if (!reading) return t("common.dash");
      const label = triageScoreLabel(name, { level: reading.level, uncertain: false },
        t("labels.answer.level_value", { level: reading.level }));
      return reading.confidence === null
        ? label
        : metaLine([label, t("detail.table.confidence") + " " + formatPercent(reading.confidence, 0)]);
    }
    return t("common.dash");
  }

  // triageListValue keeps the list preview on the same friendly vocabulary as
  // the detail summary: no raw question or option names, no percentages.
  function triageListValue(q, value) {
    if (!value) return t("common.dash");
    if (value.refused) return t("detail.answer.refused_short");
    if (value.abstain) return t("detail.answer.abstained_short");
    const type = q && q.type ? q.type : (value.type || "");
    if (type === "noul") {
      const reading = triageNoulReading(value);
      if (!reading) return t("common.dash");
      if (!reading.likely) return t("detail.summary.uncertain");
      return answerValueText(q, reading.yes);
    }
    if (type === "choice") {
      const reading = triageChoiceReading(value);
      if (!reading) return t("common.dash");
      if (reading.uncertain) return t("detail.summary.uncertain");
      return answerValueText(q, reading.choice);
    }
    if (type === "score") {
      const reading = triageScoreReading(value, q, 0);
      if (!reading) return t("common.dash");
      return answerValueText(q, reading.level) +
        (reading.uncertain ? t("detail.summary.uncertain_suffix") : "");
    }
    return t("common.dash");
  }

  function triageModelDetails(names, map, values) {
    const automatic = names.filter((name) => values[name] && values[name].rule).length;
    return h("details", { class: "triage-model-details" },
      h("summary", { text: t("detail.summary.model_details") }),
      automatic
        ? h("p", { class: "fine triage-model-note", text: t("detail.summary.automatic", { count: automatic }) })
        : null,
      h("div", { class: "triage-model-list" }, names.map((name) => {
        const value = values[name];
        const known = map.get(name) || null;
        const q = known || { name, type: value && value.type ? value.type : "", levels: [] };
        const levelText = triageLevelValue(q, value);
        return h("div", { class: "triage-model-row" },
          h("span", { class: "triage-model-q", text: questionTitle(name) }),
          h("span", { class: "triage-model-v" },
            h("span", { text: advancedAnswerValue(q, value) }),
            value && value.rule
              ? h("span", { class: "triage-model-rule", text: t("detail.answer.rule_uncertified") })
              : null,
            levelText ? h("span", { class: "triage-model-level", lang: "en", text: levelText }) : null));
      })));
  }

  function triageQuestionNames(names) {
    const seen = new Set();
    const out = [];
    for (const name of Array.isArray(names) ? names : []) {
      const key = String(name || "").trim();
      if (!key) continue;
      const label = questionTitle(key);
      if (!label || seen.has(label)) continue;
      seen.add(label);
      out.push(label);
    }
    return out.join(", ");
  }

  function triageSummaryNode(triage, questions) {
    if (!triage) return null;
    const profile = questions && Array.isArray(questions.questions) ? questions.questions : [];
    const map = questionMap({ questions: profile });
    const values = triage.values && typeof triage.values === "object" ? triage.values : {};
    const names = orderedNames(values, { questions: profile });
    if (!names.length) return h("p", { class: "fine", text: t("detail.no_values_body") });
    const facets = [
      triageAboutFacet(values, triage),
      triageAsksFacet(values),
      triageWhenFacet(map, values),
      triageFromFacet(values),
      triageAttentionFacet(map, values),
      triageEnoughFacet(values),
      triageEffortFacet(map, values),
    ].filter(Boolean);
    return h("div", { class: "triage-summary" },
      facets.length ? h("div", { class: "triage-facets" }, facets) : null,
      triageModelDetails(names, map, values));
  }

