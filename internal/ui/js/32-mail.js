  // ---------------------------------------------------------------- mail

  async function renderMail(route, seq) {
    // The full status feeds the chrome, but it never holds the list: the
    // small gate read decides whether content may paint, and the learning
    // promise refreshes the topbar when it lands.
    const learningPromise = loadLearning().catch(() => null);
    let gate;
    try {
      gate = await loadGate();
    } catch (error) {
      // The gate is permission, not a hint: when its authoritative read
      // fails, paint the error and retry instead of deriving access from a
      // stale learning status. Retry goes through the router: a renderer
      // only returns a node, so calling it here would discard the result.
      if (seq !== state.renderSeq) return h("div");
      return errorState(error, () => refreshActiveRoute());
    }
    if (seq !== state.renderSeq) return h("div");
    const mode = parseMode(route);
    if (mode !== "all" && allMailObserver) {
      allMailObserver.disconnect();
      allMailObserver = null;
    }
    const allowed = !!gate.allowed;
    const known = state.learning || null;
    // The gate is authoritative. `state.learning` may be stale (the gate
    // route is uncached); it is only a fallback when the gate itself is
    // absent, in which case the loadGate catch above built `gate` from it.
    const preview = !!gate.preview;
    // Preview is a fallback, not a permanent lock: once the real gate is
    // open, certified answers win even if learning.test_mode is still on.
    const realOpen = !!gate.open;
    const open = allowed && (realOpen || !preview);
    const showPreview = allowed && preview && !realOpen;
    setTopbarMeta(gatePill(open), showPreview ? previewPill() : null, pill(t("mail.tab." + mode)));
    const refreshChrome = (learning) => {
      if (!learning || seq !== state.renderSeq) return;
      // The gate answer already owns the gate/preview pills; the learning
      // arrival updates the shell chrome, not the authoritative gate state.
      updateLearningChrome();
    };
    if (!allowed) {
      learningPromise.then(refreshChrome);
      let view = gate.status ? { status: gate.status } : known;
      if (!view) view = await learningPromise;
      if (seq !== state.renderSeq) return h("div");
      if (!view) throw new Error("learning gate closed without a status");
      return lockedScreen(view, t("screen.mail.title"));
    }

    const tabs = h("nav", { class: "segmented mail-tabs", "aria-label": t("mail.view_aria") },
      tabButton(t("mail.tab.all"), mode === "all", () => navigate("mail", { mode: "all" }), "mail-tab-all"),
      tabButton(t("mail.tab.important"), mode === "important", () => navigate("mail", { mode: "important" }), "mail-tab-important"),
      tabButton(t("mail.tab.conversations"), mode === "conversations", () => navigate("mail", conversationParams(route)), "mail-tab-conversations"),
      tabButton(t("mail.tab.recent"), mode === "recent", () => navigate("mail", { mode: "recent" }), "mail-tab-recent"));
    // Each mode names its own list. The Important preview note is not a
    // description of Search, Conversations or Recent.
    const headNote = open ? t("mail.gate_open_note")
      : mode === "all" ? t("mail.all.note")
        : mode === "important" ? t("mail.important.note")
          : "";
    const head = h("div", { class: "mail-head" }, tabs,
      headNote ? h("span", { class: "fine", text: headNote }) : null);
    let content;
    if (mode === "all") content = await renderAllMail(route, seq, showPreview, open);
    else if (mode === "conversations") content = renderConversations(route, showPreview);
    else if (mode === "search") content = await renderSearch(route, seq, showPreview);
    else if (mode === "recent") content = await renderRecent(route, seq, showPreview);
    else content = await renderImportant(route, seq);
    if (seq !== state.renderSeq) return h("div");
    const screen = h("div", { class: "screen mail-screen" },
      head,
      showPreview ? previewBanner(t("preview.mail_note"), null, true) : null,
      content);
    // The route cache stores this outer screen, not the mode's content. Carry
    // the content's paging token onto it so a cached Important/Recent mount
    // restores the list model instead of rebuilding the screen.
    const contentPage = readPagingToken(content);
    if (contentPage) pagingToken(screen, contentPage);
    // The route cache sees this outer screen; propagate an inner All-Mail
    // read failure so Back cannot restore a stale error without a request.
    if (content && content.dataset && content.dataset.errorScreen) screen.dataset.errorScreen = "1";
    learningPromise.then(refreshChrome);
    return screen;
  }

  function parseMode(route) {
    const raw = route && route.params ? route.params.get("mode") || "all" : "all";
    if (raw === "all") return "all";
    if (raw === "conversations") return "conversations";
    if (raw === "search") return "search";
    if (raw === "triage" || raw === "recent") return "recent";
    if (raw === "important") return "important";
    return "all";
  }

  // conversationParams keeps the selected message and the state/search
  // filters when the tab or a row moves inside the same mode.
  function conversationParams(route, overrides = {}) {
    const current = route && route.params ? route.params : null;
    const params = { mode: "conversations" };
    const id = current ? current.get("id") : "";
    const stateFilter = conversationFilterState(current ? current.get("state") : "");
    const q = conversationQuery(current ? current.get("q") : "");
    if (id) params.id = id;
    if (stateFilter) params.state = stateFilter;
    if (q) params.q = q;
    return Object.assign(params, overrides);
  }

  function conversationCountsForQuery(query) {
    for (const candidate of state.conversationPages.values()) {
      if (candidate && candidate.q === query && candidate.counts) return candidate.counts;
    }
    return null;
  }

  // cacheConversationView keeps one bounded page per state/search route.
  function cacheConversationView(key, model) {
    state.conversationPages.delete(key);
    state.conversationPages.set(key, model);
    while (state.conversationPages.size > MAX_CONVERSATION_CACHE) {
      const oldest = state.conversationPages.keys().next().value;
      if (oldest === undefined) break;
      state.conversationPages.delete(oldest);
    }
    return model;
  }

  // tabButton is one segment of a route switch. The segments navigate to
  // another URL, so they are not tabs over one panel: aria-current is the
  // honest state and the group is a labelled nav.
  function lockedScreen(learning, feature, title) {
    const blockers = allBlockers(learning);
    return h("div", { class: "screen locked-screen" },
      lockedCard(title || t("mail.locked_title", { feature }), t("mail.locked_body"),
        h("div", { class: "button-row" }, button(t("common.open_learning"), () => navigate("learning"), { kind: "primary", small: true, icon: "learning" }))),
      blockers.length
        ? section(t("learning.blockers.title"), null, null, h("div", { class: "blocker-list" }, blockers.map((entry) => blockerNode(entry))))
        : emptyState(t("mail.learning_incomplete_title"), t("mail.learning_incomplete_body"), [button(t("common.refresh"), () => refreshActiveRoute(), { icon: "refresh", small: true })], "learning"));
  }

  const PRIORITY_FILTERS = ["all", "reply", "urgent", "security", "decision", "deadline", "workload"];
  const MAIL_ROLES = ["all", "inbox", "sent", "drafts", "deleted", "junk", "archive", "other"];
  // The search filter and the `in:` token read the same role vocabulary as
  // All Mail; MAIL_ROLES is its one declaration.
  const SEARCH_ROLES = MAIL_ROLES.filter((role) => role !== "all");
  // The first page is 100 rows, but a full list takes long enough to build
  // that the first row waits behind all of them. Paint enough real rows to
  // fill one screen, then append the rest after the frame.
  const ALL_MAIL_INITIAL_ROWS = 24;
  // The mounted list is capped at three 100-row pages. The list is newest
  // first, so the cap keeps the newest rows and shows a first-N-of-total
  // notice instead of loading forever; it never drops a row the reader has
  // already scrolled past. A different population is a new route.
  const ALL_MAIL_MAX_ROWS = 300;
  const pendingAllMailRows = new WeakSet();
  let allMailObserver = null;
  // The filter of the fragments being built right now. One row builder is
  // shared by the first batch, the deferred rest and Load more; the value is
  // set synchronously just before each append so a detached list can never
  // borrow another filter's href.
  let allMailRowFilter = { sender: "", unread: false };

  function mailPane(title, sub, ...body) {
    return h("section", { class: "surface mail-list-pane", "aria-label": title },
      h("div", { class: "mail-pane-head" },
        h("div", null,
          h("h2", { text: title }),
          sub ? h("p", { class: "section-sub", text: sub }) : null)),
      ...body);
  }

  function mailWorkspace(listPane, detailBody) {
    return h("div", { class: "mail-layout" },
      listPane,
      h("section", { class: "surface mail-detail-pane", "aria-label": t("mail.detail_aria") }, detailBody));
  }

  function mailDetailBody() {
    return h("div", { class: "mail-detail-body section-scroll", id: "mail-detail", dataset: { scrollKey: "mail-detail" } });
  }

  // A failed background refresh keeps the painted rows and offers the one
  // retry beside them. Important and Conversations share the same notice.
  function mailErrorLine() {
    return h("div", { class: "conversation-error", hidden: true });
  }

  function showMailErrorLine(line, message, retry) {
    line.hidden = false;
    clear(line);
    line.append(h("span", { text: message }), button(t("common.try_again"), retry, { small: true, kind: "quiet" }));
  }

  // all-mail:start
  // The all-mail view is the owner-scoped keyset page of /api/messages. It
  // keeps the loaded rows in one model, appends the next page on the list's
  // own scroll, and never remounts the list when a row is selected. The
  // folder chips use the counts the same endpoint returns, so they always
  // describe the whole mailbox rather than the current page.
  function allMailRole(value) {
    const role = String(value || "all");
    return MAIL_ROLES.includes(role) ? role : "all";
  }

  function allMailRoleLabel(role) {
    return t("mail.role." + allMailRole(role));
  }

  // scopeVerdictLabel is the owner-facing name of a stored scope verdict.
  function scopeVerdictLabel(verdict) {
    const key = "scope.verdict." + String(verdict || "");
    return I18N.has(key) ? t(key) : String(verdict || "");
  }

  // The sender filter is one exact address or one domain. The server owns
  // the comparison form and the validation; the UI keeps the trimmed value
  // it typed so the URL and a cursor page describe the same request.
  function allMailSender(value) {
    return String(value || "").trim();
  }

  // The unread filter is one boolean. Only true/1 turn it on; every other
  // value (false, empty, absent, junk) is the filter-off state, never a
  // read-only page.
  function allMailUnread(value) {
    const raw = String(value || "").toLowerCase();
    return raw === "true" || raw === "1";
  }

  function allMailPath(role, before, sender, unread) {
    const query = new URLSearchParams({ limit: "100", counts: "1" });
    if (role !== "all") query.set("role", role);
    if (sender) query.set("sender", sender);
    if (unread) query.set("unread", "true");
    if (before) query.set("before", before);
    return "/api/messages?" + query.toString();
  }

  function allMailTotal(counts) {
    if (!counts || typeof counts !== "object") return 0;
    let total = 0;
    for (const role of MAIL_ROLES) {
      if (role === "all") continue;
      total += Math.max(0, Number(counts[role]) || 0);
    }
    return total;
  }

  function allMailPreview() {
    return gatePreview();
  }

  function allMailRowNode(row, options = {}) {
    const sender = row.sender_name || row.sender_address || t("mail.sender_unknown");
    const subject = row.subject || t("mail.no_subject");
    const when = row.instant || row.received_at || row.sent_on || "";
    const role = allMailRole(row.role);
    return mailRow({
      tag: "a",
      className: ("mail-row all-mail-row" + (options.selected ? " selected" : "") + (row.is_read === false ? " unread" : "")).trim(),
      href: mailHref(row.row_id, "all", { role: options.role || "all", sender: options.sender || "", unread: options.unread ? "true" : "" }),
      dataset: { messageId: String(row.row_id) },
      ariaLabel: t("mail.open_message_aria", { sender, subject }),
      avatar: initials(sender),
      sender,
      spacer: true,
      unread: row.is_read === false,
      timeTitle: when ? formatDate(when) : "",
      timeText: when ? formatRelative(when) : "",
      subject,
      subjectTitle: subject,
      snippet: row.snippet || "",
      why: [options.role === "all" ? badge(allMailRoleLabel(role)) : null,
        row.has_attachments ? badge(t("detail.attachments")) : null],
      onClick: options.onSelect ? (event) => {
        event.preventDefault();
        options.onSelect(row);
      } : null,
    });
  }

  // One row-builder owner for the first batch, the deferred rest and Load
  // more: dedupe by row id and keep the fragment insert before the sentinel.
  // The accepted rows are returned so the model can grow with exactly the
  // rows that entered the DOM, without a second dedupe rule.
  function allMailRowID(row) {
    return String(row && row.row_id != null ? row.row_id : "");
  }

  // allMailSeenRows seeds the model's id set from the rows it already holds;
  // the set travels with the model, so a page never rescans the mounted list.
  function allMailSeenRows(rows) {
    const seen = new Set();
    for (const row of Array.isArray(rows) ? rows : []) {
      const id = allMailRowID(row);
      if (id) seen.add(id);
    }
    return seen;
  }
  function allMailRowsFragment(rows, role, selectedId, seen) {
    const fragment = document.createDocumentFragment();
    const { sender, unread } = allMailRowFilter;
    const accepted = [];
    for (const row of rows) {
      const id = allMailRowID(row);
      if (!id || seen.has(id)) continue;
      seen.add(id);
      fragment.append(allMailRowNode(row, {
        role,
        sender,
        unread,
        selected: Number(row.row_id) === selectedId,
        onSelect: (picked) => selectAllMailRow(picked, role, sender, unread),
      }));
      accepted.push(row);
    }
    return { fragment, added: accepted.length, rows: accepted };
  }

  function appendAllMailRows(list, rows, more, role, selectedId, seen) {
    const { fragment, added, rows: accepted } = allMailRowsFragment(rows, role, selectedId, seen);
    if (!added) return { added: 0, rows: [] };
    if (more && more.parentNode === list) list.insertBefore(fragment, more);
    else list.append(fragment);
    return { added, rows: accepted };
  }

  function queueAllMailRows(list, rest, more, role, seen, sender, unread) {
    if (!rest.length) return;
    pendingAllMailRows.add(list);
    let done = false;
    let timer = 0;
    const finish = () => {
      if (done) return;
      done = true;
      window.clearTimeout(timer);
      try {
        if (more.parentNode === list) {
          allMailRowFilter = { sender, unread };
          appendAllMailRows(list, rest, more, role, 0, seen);
          if (list.isConnected && state.route === "mail") {
            const current = Number(parseRoute().params.get("id") || 0) || 0;
            if (current) syncMailListSelection(current);
          }
        }
      } catch (error) {
        reportError(error);
      } finally {
        pendingAllMailRows.delete(list);
      }
    };
    timer = window.setTimeout(finish, 2000);
    window.requestAnimationFrame(() => window.requestAnimationFrame(finish));
  }

  function paintAllMailMore(more, list, seq, role, sender, unread) {
    if (!more) return;
    const model = state.allMail;
    if (model.role !== role || model.sender !== sender || model.unread !== unread) return;
    const hasRows = model.rows.length > 0;
    const capped = hasRows && model.capped;
    pagerPaint(more, {
      hidden: !hasRows,
      showing: hasRows && model.loading ? t("mail.all.loading")
        : capped ? t("mail.all.capped", { shown: formatNumber(model.rows.length), total: formatNumber(model.total) })
          : "",
      end: hasRows && !model.loading && !model.hasMore && !capped ? t("mail.all.end") : "",
      error: hasRows && model.error ? apiFailure(model.error).message : "",
      more: hasRows && !model.loading && model.hasMore && !capped,
      onMore: () => loadMoreAllMail(list, seq, role, sender, unread),
    });
    if ((!model.hasMore || model.capped) && allMailObserver) {
      allMailObserver.disconnect();
      allMailObserver = null;
    }
  }

  async function loadMoreAllMail(list, seq, role, sender, unread) {
    const model = state.allMail;
    if (!list || !list.isConnected || model.role !== role || model.sender !== sender || model.unread !== unread || model.loading || !model.hasMore || model.capped) return;
    if (pendingAllMailRows.has(list)) return;
    const more = list.querySelector(".pager");
    model.loading = true;
    model.error = null;
    let settle;
    const pending = new Promise((resolve) => { settle = resolve; });
    model.morePromise = pending;
    try {
      paintAllMailMore(more, list, seq, role, sender, unread);
      const view = await request(allMailPath(role, model.next, sender, unread));
      if (state.allMail !== model || !list.isConnected || model.role !== role || model.sender !== sender || model.unread !== unread) return;
      const rows = Array.isArray(view.messages) ? view.messages : [];
      // The cap is enforced before the append, so the DOM, the model and the
      // notice agree on one count; rows that do not fit stay on the server.
      const budget = Math.max(0, ALL_MAIL_MAX_ROWS - model.rows.length);
      const pageRows = rows.slice(0, budget);
      allMailRowFilter = { sender, unread };
      const result = appendAllMailRows(list, pageRows, more, role, 0, model.seen);
      // The model grows by exactly the rows the builder accepted; a cached
      // refresh can then repaint every loaded row from model.rows.
      model.rows = model.rows.concat(result.rows);
      model.total = Number(view.total) || model.total;
      model.counts = view.counts && typeof view.counts === "object" ? view.counts : model.counts;
      model.next = view.next_before || "";
      model.hasMore = !!view.has_more && result.added > 0;
      model.capped = model.rows.length >= ALL_MAIL_MAX_ROWS && (model.hasMore || rows.length > result.added);
      list.__allMailModel = allMailSnapshot(state.allMail);
    } catch (error) {
      model.error = error;
      reportActionError(error);
    } finally {
      try {
        model.loading = false;
        if (model.morePromise === pending) model.morePromise = null;
        if (state.allMail === model) list.__allMailModel = allMailSnapshot(model);
        if (state.allMail === model && more && more.isConnected) {
          paintAllMailMore(more, list, seq, role, sender, unread);
          // A tall window can show the next sentinel without a scroll: keep
          // filling one page at a time until it leaves the viewport.
          if (model.hasMore && !model.capped) {
            const listBox = list.getBoundingClientRect();
            const moreBox = more.getBoundingClientRect();
            if (moreBox.top <= listBox.bottom + 160) {
              window.requestAnimationFrame(() => loadMoreAllMail(list, seq, role, sender, unread));
            }
          }
        }
      } finally {
        settle();
      }
    }
  }

  function observeAllMailMore(list, more, seq, role, sender, unread) {
    if (allMailObserver) {
      allMailObserver.disconnect();
      allMailObserver = null;
    }
    if (state.allMail.role !== role || state.allMail.sender !== sender || state.allMail.unread !== unread || state.allMail.capped || !state.allMail.hasMore || !more || more.hidden || typeof IntersectionObserver !== "function") return;
    allMailObserver = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) loadMoreAllMail(list, seq, role, sender, unread);
    }, { root: list, rootMargin: "200px 0px" });
    allMailObserver.observe(more);
  }

  function selectAllMailRow(row, role, sender, unread) {
    const id = Number(row && row.row_id) || 0;
    if (!id) return;
    replaceRoute("mail", { mode: "all", role, sender: sender || "", unread: unread ? "true" : "", id: String(id) });
    syncMailListSelection(id);
    const panel = document.getElementById("mail-detail");
    if (panel) loadMailDetail(id, panel, { preview: allMailPreview(), mode: "all", item: row });
  }

  // allMailSnapshot is the part of the All-Mail model a rendered list owns.
  // The cached-route restore reads it back from the node; in-flight promises
  // and transient loading flags never travel with it.
  function allMailSnapshot(model) {
    return {
      role: model.role,
      sender: model.sender,
      unread: model.unread,
      rows: model.rows,
      total: model.total,
      counts: model.counts,
      next: model.next,
      hasMore: model.hasMore,
      capped: model.capped,
      loaded: model.loaded,
    };
  }

  async function renderAllMail(route, seq, showPreview, open) {
    const role = allMailRole(route && route.params ? route.params.get("role") : "");
    const sender = allMailSender(route && route.params ? route.params.get("sender") : "");
    const unread = allMailUnread(route && route.params ? route.params.get("unread") : "");
    let selectedId = Number(route && route.params ? route.params.get("id") : 0) || 0;
    let model = state.allMail;
    let error = null;
    if (model.role !== role || model.sender !== sender || model.unread !== unread) {
      // A filter change replaces the model. The old object may still have a
      // first-page or Load more request in flight; its identity guards then
      // fail and it can neither clear loading nor repaint this filter's
      // sentinel.
      if (allMailObserver) {
        allMailObserver.disconnect();
        allMailObserver = null;
      }
      model = state.allMail = {
        role,
        sender,
        unread,
        rows: [],
        total: 0,
        counts: null,
        next: "",
        hasMore: false,
        capped: false,
        seen: new Set(),
        loaded: false,
        promise: null,
        loading: false,
        morePromise: null,
        error: null,
      };
    }
    if (!model.loaded) {
      if (!model.promise) {
        const requested = role;
        const requestedSender = sender;
        const requestedUnread = unread;
        let pending;
        pending = request(allMailPath(role, "", sender, unread)).then((view) => {
          // The closure owns the model it started with; a filter change
          // replaces state.allMail, so this write is dropped.
          if (state.allMail !== model || model.role !== requested || model.sender !== requestedSender || model.unread !== requestedUnread) return;
          model.rows = Array.isArray(view.messages) ? view.messages : [];
          model.total = Number(view.total) || 0;
          model.counts = view.counts && typeof view.counts === "object" ? view.counts : null;
          model.next = view.next_before || "";
          model.hasMore = !!view.has_more;
          model.capped = false;
          model.seen = allMailSeenRows(model.rows);
          model.loaded = true;
        }).finally(() => {
          if (model.promise === pending) model.promise = null;
        });
        model.promise = pending;
      }
      try { await model.promise; } catch (err) { error = err; }
    }
    if (seq !== state.renderSeq) return h("div");
    while (model.loading && model.morePromise && state.allMail === model) {
      const pending = model.morePromise;
      await pending;
      if (state.allMail !== model) break;
      // Defensive: a broken owner must not spin. The normal finally clears both.
      if (model.morePromise === pending && model.loading) break;
    }
    if (seq !== state.renderSeq) return h("div");
    if (error) reportError(error);

    const unreadParam = unread ? "true" : "";
    const clearSender = () => navigate("mail", { mode: "all", role, unread: unreadParam });
    const clearUnread = () => navigate("mail", { mode: "all", role, sender });
    const counts = model.counts || {};
    // Two labeled groups share one filter strip: the folder chips keep the
    // folder label, and the read-state toggle has its own group so a screen
    // reader never hears "Folder filter" over a read-state control.
    const chips = h("div", { class: "filter-chips" },
      senderChip(sender, clearSender),
      h("div", { class: "filter-chip-group", role: "group", "aria-label": t("mail.role_aria") },
        MAIL_ROLES.map((name) => {
          const count = name === "all" ? allMailTotal(counts) : Math.max(0, Number(counts[name]) || 0);
          return filterChip({
            label: allMailRoleLabel(name),
            count: count > 0 ? count : undefined,
            active: name === role,
            focusKey: "mail-role-" + name,
            onPick: () => navigate("mail", { mode: "all", role: name, sender, unread: unreadParam }),
          }).chip;
        })),
      h("div", { class: "filter-chip-group", role: "group", "aria-label": t("mail.filter.state_aria") },
        h("button", {
          type: "button",
          class: "chip chip-button unread-chip" + (unread ? " active" : ""),
          "aria-pressed": unread ? "true" : "false",
          dataset: { focusKey: "mail-unread-toggle" },
          onclick: () => navigate("mail", { mode: "all", role, sender, unread: unread ? "" : "true" }),
        }, h("span", { text: t("mail.filter.unread") }))));

    const list = h("div", { class: "mail-list section-scroll", "aria-label": t("mail.list_aria"), dataset: { scrollKey: "mail-list" } });
    const more = pagerNode({
      className: "pager-list",
      onMore: () => loadMoreAllMail(list, seq, role, sender, unread),
    });
    let deferred = null;
    if (error) {
      list.append(errorState(error));
      list.append(more);
    } else if (!model.rows.length) {
      const actions = sender
        ? [button(t("mail.filter.sender_clear"), clearSender, { small: true, icon: "refresh" })]
        : unread
          ? [button(t("mail.filter.unread_clear"), clearUnread, { small: true, icon: "refresh" })]
          : null;
      list.append(emptyState(
        sender ? t("mail.all.empty_sender_title") : unread ? t("mail.all.empty_unread_title") : t("mail.all.empty_title"),
        sender ? t("mail.all.empty_sender_body") : unread ? t("mail.all.empty_unread_body") : t("mail.all.empty_body"),
        actions, "mail"));
      list.append(more);
    } else {
      // The sentinel exists before the rows so the fragments can be inserted
      // in front of it; the first real batch is the only thing the first
      // paint waits for. The local set dedupes this page against itself; the
      // model's id set (all rows it holds) is what a later page checks.
      const seen = new Set();
      const rest = model.rows.slice(ALL_MAIL_INITIAL_ROWS);
      // A bare /mail shows the first message in the detail pane; the list
      // must say the same instead of leaving every row unselected.
      if (!selectedId && model.rows[0]) selectedId = Number(model.rows[0].row_id) || 0;
      list.append(more);
      allMailRowFilter = { sender, unread };
      appendAllMailRows(list, model.rows.slice(0, ALL_MAIL_INITIAL_ROWS), more, role, selectedId, seen);
      if (rest.length) deferred = { rest, seen, sender, unread };
    }
    paintAllMailMore(more, list, seq, role, sender, unread);
    if (deferred) queueAllMailRows(list, deferred.rest, more, role, deferred.seen, deferred.sender, deferred.unread);
    observeAllMailMore(list, more, seq, role, sender, unread);

    const detail = mailDetailBody();
    const selectedRow = model.rows.find((row) => Number(row.row_id) === selectedId) || null;
    const detailId = selectedId;
    if (detailId) {
      loadMailDetail(detailId, detail, {
        preview: showPreview,
        mode: "all",
        item: selectedRow || model.rows[0] || null,
      });
    } else {
      detail.append(emptyState(t("mail.select.title"), t("mail.select.body"), null, "mail"));
    }

    // Every painted All-Mail list carries the model it was built from, so a
    // later cached hit can restore the same filter instead of borrowing
    // another route's state. A read failure is never a cacheable screen.
    list.__allMailModel = allMailSnapshot(model);
    setTopbarMeta(
      gatePill(open),
      showPreview ? previewPill() : null,
      pill(t("mail.tab.all")),
      pill(t("mail.all.count", { count: model.total })));
    const workspace = mailWorkspace(mailPane(t("mail.all.title"), t("mail.all.sub"), null, chips, list), detail);
    if (error) workspace.dataset.errorScreen = "1";
    return workspace;
  }
  // all-mail:end

  async function renderImportant(route, seq) {
    const rawFilter = route.params.get("filter") || "all";
    const filter = PRIORITY_FILTERS.includes(rawFilter) ? rawFilter : "all";
    let view = cachedPriority(filter);
    // A Home-sized page (6) is not a usable Important page: painting it and
    // then rebuilding the list when the 50-item read lands is a visible jump.
    if (view && view.limit < PRIORITY_PAGE_SIZE) view = null;
    let error = null;
    let refreshPromise = null;
    if (view) {
      // An accumulated list already extends past the snapshot a refresh
      // reads; revalidating it would discard the pages the owner loaded.
      if (view.items.length <= PRIORITY_PAGE_SIZE) refreshPromise = loadPriority(filter, PRIORITY_PAGE_SIZE);
    } else {
      try { view = await loadPriority(filter, PRIORITY_PAGE_SIZE); }
      catch (err) { error = err; }
    }
    if (seq !== state.renderSeq) return h("div");
    if (error) reportError(error);
    let items = view && Array.isArray(view.items) ? view.items : [];
    const selectedId = Number(route.params.get("id")) || (items[0] ? items[0].message_id : 0);
    const selectedItem = items.find((item) => item.message_id === selectedId) || null;
    importantTopbarMeta(view);
    if (view) prefetchPriorityDetails(items);

    const filterNodes = new Map();
    const chips = h("div", { class: "filter-chips", role: "group", "aria-label": t("mail.filter_aria") },
      PRIORITY_FILTERS.map((name) => {
        const cell = filterChip({
          label: priorityFilterLabel(name),
          count: null,
          active: name === filter,
          focusKey: "important-filter-" + name,
          onPick: () => navigate("mail", { mode: "important", filter: name }),
        });
        filterNodes.set(name, cell.count);
        return cell.chip;
      }));
    updatePriorityFilterCounts(state.priorityTotals, filterNodes);

    const list = h("div", { class: "mail-list section-scroll", "aria-label": t("mail.list_aria"), dataset: { scrollKey: "mail-list" } });
    const errorLine = mailErrorLine();
    const page = {
      screen: "important",
      view, filter, list, items, selectedId, loading: false,
      // The next offset starts after the rows this render painted. It is
      // restored with the page object on a cached mount.
      next: (view ? Math.max(0, Number(view.offset) || 0) : 0) + items.length,
      // recordPriority owns the snapshot time; the token carries it so a
      // cached mount can reject a page older than the read TTL.
      at: (state.priorityPages.get(filter) || {}).at || 0,
      host: null,
      error: null,
      pager: null,
      button: null,
    };
    page.pager = pagerNode({ onMore: () => loadMoreImportant(page) });
    page.button = pagerMoreButton(page.pager);
    if (error) {
      list.append(errorState(error));
    } else if (!items.length) {
      list.append(importantEmptyState());
    } else {
      appendImportantRows(list, items, filter, selectedId, view);
      list.append(page.pager);
      paintImportantMore(page);
    }
    const detail = mailDetailBody();
    if (selectedId) loadMailDetail(selectedId, detail, { preview: view ? view.preview : false, mode: "important", item: selectedItem });
    else {
      clear(detail);
      detail.append(emptyState(t("mail.select.title"), t("mail.select.body"), null, "mail"));
    }
    function applyRefresh(fresh) {
      if (seq !== state.renderSeq || !list.isConnected) return;
      // The cached view can be the exact object already painted: rebuilding
      // 50 rows for the same data is pure work.
      if (fresh === view) return;
      if (page.loading || page.items.length > PRIORITY_PAGE_SIZE) return;
      // A focused row or footer owns the mounted list. Replacing it would
      // move focus; keep this snapshot until the focus leaves.
      if (list.contains(document.activeElement)) return;
      view = fresh;
      items = Array.isArray(fresh.items) ? fresh.items : [];
      page.view = fresh;
      page.items = items;
      page.next = (Math.max(0, Number(fresh.offset) || 0)) + items.length;
      clear(list);
      if (!items.length) {
        list.append(importantEmptyState());
      } else {
        appendImportantRows(list, items, filter, selectedId, fresh);
        list.append(page.pager);
        paintImportantMore(page);
      }
      importantTopbarMeta(fresh);
      updatePriorityFilterCounts(state.priorityTotals, filterNodes);
      prefetchPriorityDetails(items);
    }
    function refreshFailed(err) {
      if (seq !== state.renderSeq || !list.isConnected) return;
      if (!view) { reportError(err); return; }
      showMailErrorLine(errorLine, t("mail.important.refresh_error"), refresh);
    }
    // A retry re-runs the same first-page revalidation. The accumulated-page
    // rule and applyRefresh's focus guard keep the owner's rows in place.
    function refresh() {
      if (seq !== state.renderSeq || !list.isConnected) return;
      errorLine.hidden = true;
      if (page.items.length > PRIORITY_PAGE_SIZE) return;
      loadPriority(filter, PRIORITY_PAGE_SIZE).then(applyRefresh).catch(refreshFailed);
    }
    if (refreshPromise) {
      refreshPromise.then(applyRefresh).catch(refreshFailed);
    }
    const screen = mailWorkspace(mailPane(t("mail.important.title"), t("mail.important.sub"), chips, errorLine, list), detail);
    page.host = screen;
    pagingToken(screen, page);
    return screen;
  }

  // selectedMailRow is the one highlighted node. A click clears the previous
  // node and stores the new one instead of walking every mounted row.
  let selectedMailRow = null;
  function syncMailListSelection(id) {
    const selected = document.querySelector(
      '.mail-list-pane .mail-row[data-message-id="' + String(id) + '"], .search-result[data-id="' + String(id) + '"]');
    const row = selectedMailRow;
    if (row && row !== selected) {
      row.classList.remove("selected");
      row.removeAttribute("aria-current");
    }
    selectedMailRow = selected;
    if (selected) {
      selected.classList.add("selected");
      selected.setAttribute("aria-current", "true");
    }
  }

  // A conversation row is just another message: replace the id so the Mail
  // list stays on the same route, then paint that message in the same pane.
  function selectThreadMessage(id, context = {}) {
    const messageId = Number(id) || 0;
    if (!messageId) return;
    const params = paramsObject(parseRoute().params);
    params.id = String(messageId);
    if (!params.mode) params.mode = context.mode || "important";
    replaceRoute("mail", params);
    syncMailListSelection(messageId);
    const panel = document.getElementById("mail-detail");
    if (!panel) return;
    loadMailDetail(messageId, panel, {
      preview: !!context.preview,
      mode: params.mode || "important",
      routed: context.routed || null,
      item: null,
    });
  }

  function selectImportantRow(id, filter, view) {
    replaceRoute("mail", { mode: "important", filter, id: String(id) });
    syncMailListSelection(id);
    const panel = document.getElementById("mail-detail");
    const item = view && Array.isArray(view.items) ? view.items.find((entry) => entry.message_id === id) : null;
    if (panel) loadMailDetail(id, panel, { preview: view ? view.preview : false, mode: "important", item });
  }

  function fetchConversationPage(key, stateFilter, query) {
    const existing = state.conversationLoads.get(key);
    if (existing) return existing;
    const epoch = apiEpoch;
    const promise = loadConversations(CONVERSATIONS_PAGE_SIZE, "", stateFilter, query)
      .then((page) => {
        // A write invalidated the read while it was in flight: never let the
        // old page repopulate the derived cache.
        if (epoch !== apiEpoch) return null;
        return cacheConversationView(key, conversationViewFromPage(page, stateFilter, query));
      })
      .finally(() => {
        if (state.conversationLoads.get(key) === promise) state.conversationLoads.delete(key);
      });
    state.conversationLoads.set(key, promise);
    return promise;
  }

  // selectConversationRow opens the latest message of a conversation without
  // rebuilding the list: same route, new id, synchronized row highlight.
  function selectConversationRow(row, view) {
    const id = Number(row && row.last_message_id) || 0;
    if (!id) return;
    replaceRoute("mail", conversationParams(parseRoute(), { id: String(id) }));
    syncMailListSelection(id);
    const panel = document.getElementById("mail-detail");
    if (panel) loadMailDetail(id, panel, { preview: !!(view && view.preview), mode: "conversations", item: null });
  }

  // renderConversations paints its shell and cached first page synchronously.
  // Only a missing or stale page starts a read; row selection never rebuilds.
  function renderConversations(route, showPreview) {
    state.triagePreview = showPreview;
    const filterState = conversationFilterState(route.params.get("state"));
    const query = conversationQuery(route.params.get("q"));
    const viewKey = conversationViewKey(filterState, query);
    const routeKey = routeCacheKey(route);
    const routeLive = state.routeKey === routeKey;
    let view = state.conversationPages.get(viewKey) || null;
    if (!view) {
      const page = cached(conversationPath(CONVERSATIONS_PAGE_SIZE, "", filterState, query));
      if (page) {
        view = cacheConversationView(viewKey, conversationViewFromPage(page, filterState, query));
      }
    }
    let selectedId = Number(route.params.get("id")) || 0;
    if (!selectedId && view && view.rows.length) selectedId = Number(view.rows[0].last_message_id) || 0;
    const initialCounts = view ? view.counts : conversationCountsForQuery(query);
    // Coverage is global to the mailbox, so a state switch keeps the line
    // while its filtered page loads.
    const knownCoverage = view || [...state.conversationPages.values()].find((candidate) => candidate && candidate.coverage);

    const totalNode = h("span", { class: "pill conversations-total", hidden: !view });
    const coverageNode = h("p", {
      class: "section-sub conversations-coverage",
      text: knownCoverage ? conversationCoverageLabel(knownCoverage) : t("mail.conversations.loading"),
    });
    const chipCounts = new Map();
    const chips = h("div", { class: "filter-chips conversations-chips", role: "group", "aria-label": t("mail.conversations.filter_aria") },
      [""].concat(THREAD_STATES).map((name) => {
        const cell = filterChip({
          label: name ? conversationStateLabel(name) : t("mail.filter.all"),
          count: null,
          active: name === filterState,
          focusKey: "conversation-state-" + (name || "all"),
          onPick: () => navigate("mail", conversationParams(route, { state: name, id: "" })),
        });
        chipCounts.set(name || "all", cell.count);
        return cell.chip;
      }));

    function paintChipCounts(counts, known) {
      const values = conversationCountValues(counts);
      for (const [name, node] of chipCounts.entries()) {
        node.textContent = known ? formatNumber(values[name]) : "";
      }
    }
    paintChipCounts(initialCounts, !!initialCounts);

    const errorLine = mailErrorLine();
    const list = h("div", {
      class: "mail-list conversations-list section-scroll",
      "aria-label": t("mail.conversations.list_aria"),
      "aria-busy": view ? "false" : "true",
      dataset: { scrollKey: "mail-list" },
    });
    const foot = pagerNode({ className: "pager-pane", hidden: true, onMore: () => loadMore() });
    const detail = mailDetailBody();

    const pane = h("section", {
      class: "surface mail-list-pane conversations-pane",
      "aria-label": t("mail.conversations.title"),
    },
    h("div", { class: "mail-pane-head conversations-head" },
      h("div", { class: "conversations-title-row" },
        h("div", null,
          h("h2", { text: t("mail.conversations.title") }),
          coverageNode),
        totalNode)),
    chips,
    errorLine,
    list,
    foot);

    function liveNow() {
      return state.routeKey === routeKey && pane.isConnected;
    }

    function syncTopbar(model, live) {
      if (!live) return;
      setTopbarMeta(
        gatePill(gateOpen()),
        showPreview ? previewPill() : null,
        pill(t("mail.tab.conversations")),
        model ? pill(conversationTotalLabel(model), "pending") : null);
    }

    function syncChrome(model, live) {
      totalNode.hidden = !model;
      if (model) {
        totalNode.textContent = conversationTotalLabel(model);
        coverageNode.hidden = false;
        coverageNode.textContent = conversationCoverageLabel(model);
        paintChipCounts(model.counts, true);
      }
      list.setAttribute("aria-busy", model ? "false" : "true");
      pagerPaint(foot, {
        hidden: !(model && model.hasMore && model.nextBefore),
        showing: model ? conversationTotalLabel(model) : "",
        error: "",
        more: !!(model && model.hasMore && model.nextBefore),
      });
      syncTopbar(model, live);
    }

    function openDetail(id, model, live) {
      if (!id) {
        // A cached shell with no conversation selected still gets its empty
        // detail pane; loading an actual thread waits until the pane is live.
        clear(detail);
        detail.append(emptyState(t("mail.select.title"), t("mail.select.body"), null, "mail"));
        return;
      }
      if (!live) return;
      if (Number(detail.dataset.messageId || 0) === id) return;
      loadMailDetail(id, detail, {
        preview: !!(model && model.preview),
        mode: "conversations",
        item: null,
      });
    }

    function conversationRow(row) {
      return conversationRowNode(row, {
        selected: Number(row.last_message_id) === selectedId,
        onSelect: (picked) => {
          const id = Number(picked.last_message_id) || 0;
          if (!id) return;
          selectedId = id;
          selectConversationRow(picked, view);
        },
      });
    }

    function paintRows(model) {
      clear(list);
      if (!model.rows.length) {
        const filtered = !!(filterState || query);
        const copy = conversationEmptyCopy(filtered);
        const actions = filtered
          ? [button(t("mail.conversations.clear_filters"), () => navigate("mail", { mode: "conversations" }), { small: true })]
          : [button(t("common.refresh"), () => loadView(), { small: true, icon: "refresh" })];
        list.append(emptyState(copy.title, copy.body, actions, "mail"));
        return;
      }
      for (const row of model.rows) list.append(conversationRow(row));
    }

    function paintSkeleton() {
      clear(list);
      for (let i = 0; i < 6; i += 1) {
        list.append(h("div", { class: "conversation-row conversation-skeleton", "aria-hidden": "true" },
          h("div", { class: "conversation-skeleton-lines" },
            skeletonLine("w-40"), skeletonLine("w-80"), skeletonLine("w-60"))));
      }
    }

    function paintError(error, live) {
      const copy = conversationErrorCopy(error);
      clear(list);
      list.setAttribute("aria-busy", "false");
      list.append(errorState(error, () => loadView(), { title: copy.title, body: copy.body }));
      totalNode.hidden = true;
      coverageNode.hidden = true;
      pagerPaint(foot, { hidden: true, showing: "", error: "", more: false });
      syncTopbar(null, live);
    }

    function applyModel(model, keepRows) {
      view = cacheConversationView(viewKey, model);
      errorLine.hidden = true;
      if (!selectedId && model.rows.length) selectedId = Number(model.rows[0].last_message_id) || 0;
      pane.dataset.selectedMessageId = String(selectedId || 0);
      const live = liveNow();
      syncChrome(model, live);
      // A background repaint clears the list; the owner's reading position is
      // part of the mounted screen, not of the model.
      const scroll = list.scrollTop;
      if (keepRows && model.rows.length) syncMailListSelection(selectedId);
      else paintRows(model);
      if (scroll > 0) list.scrollTop = scroll;
      openDetail(selectedId, model, live);
    }

    let loading = false;

    function loadView() {
      if (loading) return;
      loading = true;
      errorLine.hidden = true;
      if (!view) {
        paintSkeleton();
        list.setAttribute("aria-busy", "true");
      }
      fetchConversationPage(viewKey, filterState, query).then((model) => {
        loading = false;
        if (!model) {
          // A write invalidated the read; the live shell retries against the
          // new epoch instead of leaving its skeleton up forever.
          if (!state.shuttingDown && state.token) loadView();
          return;
        }
        const keepRows = !!view && list.contains(document.activeElement);
        applyModel(model, keepRows);
      }).catch((error) => {
        loading = false;
        if (view) {
          showMailErrorLine(errorLine, t("mail.conversations.refresh_error"), loadView);
          list.setAttribute("aria-busy", "false");
          return;
        }
        paintError(error, liveNow());
      });
    }

    let loadingMore = false;

    function loadMore() {
      if (loadingMore || !view || !view.hasMore || !view.nextBefore) return;
      const currentView = view;
      loadingMore = true;
      errorLine.hidden = true;
      pagerPaint(foot, { error: "" });
      withBusy(pagerMoreButton(foot), t("mail.conversations.loading_more"), () => {
        return loadConversations(CONVERSATIONS_PAGE_SIZE, currentView.nextBefore, filterState, query).then((page) => {
          if (state.conversationPages.get(viewKey) !== currentView) return;
          const before = currentView.rows.length;
          const merged = conversationMergePage(currentView, page);
          view = cacheConversationView(viewKey, merged);
          const scroll = list.scrollTop;
          const added = merged.rows.slice(before);
          for (const row of added) list.append(conversationRow(row));
          list.scrollTop = scroll;
          syncChrome(merged, liveNow());
        });
      }).catch((error) => {
        pagerPaint(foot, { error: apiFailure(error).message });
        showMailErrorLine(errorLine, t("mail.conversations.load_more_error"), () => {
          invalidateCache(["/api/conversations"]);
          errorLine.hidden = true;
          loadView();
        });
      }).finally(() => {
        loadingMore = false;
      });
    }

    pane.dataset.selectedMessageId = String(selectedId || 0);
    if (view) {
      syncChrome(view, routeLive);
      paintRows(view);
      openDetail(selectedId, view, routeLive);
    } else {
      paintSkeleton();
      syncChrome(null, false);
      openDetail(selectedId, { preview: showPreview }, routeLive);
    }

    const fresh = !!view && cached(conversationPath(CONVERSATIONS_PAGE_SIZE, "", filterState, query)) !== undefined;
    if (!fresh) loadView();

    return mailWorkspace(pane, detail);
  }

  async function ensureTriageView(force = false) {
    // The coverage summary is supplementary: a failed read leaves the list
    // page intact and marks coverage unknown instead of rejecting the screen.
    const [summaryRead, page] = await Promise.all([
      settleRead(requestCached("/api/triage/summary", { force }, CACHE_TTL.triageSummary)),
      requestCached("/api/triage?limit=" + TRIAGE_PAGE_SIZE + "&offset=0", { force }, CACHE_TTL.triagePage),
    ]);
    if (summaryRead.error) {
      state.triageSummaryError = summaryRead.error;
    } else {
      state.triageSummary = summaryRead.value;
      state.triageSummaryError = null;
    }
    const results = page.results && typeof page.results === "object" ? page.results : {};
    let view = state.triagePage;
    const hadView = !!view;
    if (!view) {
      view = {
        screen: "recent",
        key: "all",
        routed: !!page.routed,
        total: Number(page.total) || 0,
        limit: Number(page.limit) || TRIAGE_PAGE_SIZE,
        results: Object.assign({}, results),
        ids: [],
        loaded: 0,
      };
      state.triagePage = view;
    } else {
      view.routed = !!page.routed;
      view.total = Number(page.total) || view.total;
      for (const [id, result] of Object.entries(results)) view.results[id] = result;
    }
    const pageIds = triagePageIds(page, results);
    if (Array.isArray(page.ids)) {
      // A refresh of the first loaded page is authoritative: its exact order
      // replaces the old ids, so an id the server dropped is no longer
      // selectable. An accumulated model keeps its older loaded pages after
      // the refreshed first page.
      const firstPage = !hadView || view.loaded <= (Number(page.limit) || TRIAGE_PAGE_SIZE);
      view.ids = firstPage ? pageIds : pageIds.concat(view.ids.filter((id) => !pageIds.includes(id)));
    } else {
      view.ids = triagePageIds(page, view.results);
    }
    if (view.total > 0) view.ids = view.ids.slice(0, view.total);
    view.loaded = Math.min(Math.max(view.loaded, Number(page.limit) || TRIAGE_PAGE_SIZE, view.ids.length), view.total);
    return view;
  }

  // recentCoverageDetails owns the Recent tab's coverage disclosure. Its
  // counts are stored route rows: an answer or an abstention. The labels say
  // "Judged" so the strip never implies certification, which is tracked on
  // the Learning screen and the route pill.
  function recentCoverageDetails(summary, coverage) {
    if (!coverage) {
      return h("p", { class: "fine triage-coverage-unavailable", text: t("mail.coverage_unavailable") });
    }
    const coverageStrip = statStrip([
      { label: t("triage.coverage.eligible"), value: formatNumber(coverage.eligible || 0) },
      { label: t("triage.coverage.fresh"), value: formatNumber(coverage.fresh || 0) },
      { label: t("triage.coverage.incomplete"), value: formatNumber(coverage.incomplete || 0) },
      { label: t("triage.coverage.stale"), value: formatNumber(coverage.stale || 0) },
      { label: t("triage.coverage.failed"), value: formatNumber(coverage.failed || 0) },
      { label: t("triage.coverage.unjudged"), value: formatNumber(coverage.unjudged || 0) },
    ]);
    return h("details", { class: "triage-coverage" },
      h("summary", { text: t("mail.coverage_details") }),
      coverageStrip,
      h("p", { class: "fine", text: t("mail.coverage_note") }),
      summary && summary.preview ? h("p", { class: "fine", text: t("triage.summary.preview_note") }) : null);
  }

  async function renderRecent(route, seq, preview) {
    const escalatedOnly = route.params.get("escalated") === "1";
    let view = null;
    let error = null;
    let certification = null;
    try {
      // The page carries the answers; the question catalogue is the shell's
      // one cached read, shared with Labels, Answers and Ask. The
      // certification read waits for the shared Hub snapshot so a direct
      // route never paints before its count lands.
      [view, , certification] = await Promise.all([ensureTriageView(), loadQuestions(), readCertification()]);
    } catch (err) { error = err; }
    if (seq !== state.renderSeq) return h("div");
    if (error) reportError(error);
    // A failed summary is unknown coverage, not a zero-coverage snapshot: the
    // rows still render and the topbar omits the count.
    const summary = state.triageSummaryError ? null : state.triageSummary;
    const profile = state.questions || {};
    const results = view ? view.results : {};
    const coverage = summary && summary.coverage && typeof summary.coverage === "object" ? summary.coverage : null;
    const coverageKind = !coverage ? "" : Number(coverage.eligible) > 0 && Number(coverage.fresh) >= Number(coverage.eligible)
      ? "done" : Number(coverage.fresh) > 0 ? "warn" : "pending";
    const visibleIds = triageVisibleIds(view, escalatedOnly);
    const selectedId = triageSelectedId(visibleIds, Number(route.params.get("id")) || 0);
    state.triageRouted = view ? view.routed : null;
    state.triagePreview = preview;
    setTopbarMeta(
      gatePill(gateOpen()),
      preview ? previewPill() : null,
      pill(t("mail.tab.recent")),
      coverage ? pill(t("triage.coverage", { fresh: formatNumber(coverage.fresh || 0), eligible: formatNumber(coverage.eligible || 0) }), coverageKind) : null,
      routePill(view ? view.routed : null, preview, certification));

    const escalatedToggle = h("input", { type: "checkbox", checked: escalatedOnly, dataset: { focusKey: "recent-escalated-toggle" }, onchange: (event) => {
      navigate("mail", { mode: "recent", escalated: event.currentTarget.checked ? "1" : "" });
    } });
    const countLabel = pagerNode({ className: "pager-inline", hidden: true });
    const list = h("div", { class: "mail-list section-scroll", "aria-label": t("mail.list_aria"), dataset: { scrollKey: "mail-list" } });
    // The root is built after the control, so the closure reads this binding
    // when it runs, not when it was created. The token on that root is the
    // paging screen's liveness signal across cached remounts.
    let screen = null;
    pagerPaint(countLabel, { onMore: () => loadMoreTriage(view, escalatedOnly, list, countLabel, screen) });
    if (view) {
      for (const id of visibleIds) list.append(triageListRow(id, results[id], profile, selectedId, escalatedOnly));
      updateTriageCount(view, visibleIds.length, countLabel);
    } else {
      list.append(error ? errorState(error, () => refreshActiveRoute())
        : emptyState(t("triage.empty.title"), t("triage.empty.none"),
          [button(t("common.try_again"), () => refreshActiveRoute(), { small: true, icon: "refresh" })], "mail"));
    }
    if (view && !view.total) {
      clear(list);
      list.append(emptyState(t("triage.empty.title"), view.routed ? t("triage.empty.routed") : t("triage.empty.none"),
        [button(t("common.refresh"), () => { forgetTriagePage(); state.triageSummary = null; refreshActiveRoute(); }, { icon: "refresh", small: true })], "mail"));
    }
    const coverageDetails = recentCoverageDetails(summary, coverage);
    const headTools = h("div", { class: "mail-list-tools" },
      h("label", { class: "check" }, escalatedToggle, t("triage.escalated_only")),
      h("span", { class: "spacer" }),
      countLabel);
    const detail = mailDetailBody();
    if (selectedId) loadMailDetail(selectedId, detail, { preview, mode: "recent", routed: view ? view.routed : null });
    else {
      clear(detail);
      detail.append(emptyState(t("triage.select_message"), t("triage.select_message_body"), null, "mail"));
    }
    screen = mailWorkspace(mailPane(t("mail.recent.title"), t("mail.recent.sub"), headTools, coverageDetails, list), detail);
    if (view) pagingToken(screen, view);
    return screen;
  }

  function triageListRow(id, result, profile, selectedId, escalatedOnly) {
    const questions = questionMap(profile);
    // The page ships the list projection: the first answered value, the
    // answered/abstained/missing counts and the row flags. The full answer
    // map stays on the detail read.
    const answered = Number(result && result.answered) || 0;
    const abstained = Number(result && result.abstained) || 0;
    const automatic = Number(result && result.automatic) || 0;
    const missing = Number(result && result.missing) || 0;
    const first = result && result.first_question ? String(result.first_question) : "";
    const firstQuestion = first
      ? (questions.get(first) || { name: first, type: result.first_value && result.first_value.type ? result.first_value.type : "", levels: [] })
      : null;
    let firstValue = firstQuestion ? triageListValue(firstQuestion, result.first_value) : t("common.dash");
    if (firstQuestion && result.first_value && result.first_value.rule) {
      firstValue += t("detail.answer.uncertified_suffix");
    }
    return mailRow({
      tag: "button",
      className: "mail-row triage-row" + (id === selectedId ? " selected" : ""),
      dataset: { messageId: String(id) },
      avatar: String(id).slice(-2),
      sender: t("mail.message_number", { id: formatId(id) }),
      senderDir: null,
      timeText: result && result.decided_at ? formatDate(result.decided_at) : t("common.dash"),
      subject: first ? t("triage.first_value", { question: questionTitle(first), value: firstValue }) : t("common.dash"),
      subjectDir: null,
      snippet: t("triage.row.answers", { count: answered }) +
        (abstained ? t("triage.row.abstained_suffix", { count: abstained }) : "") +
        (automatic ? t("triage.row.automatic_suffix", { count: automatic }) : "") +
        (missing ? t("triage.row.missing_suffix", { count: missing }) : ""),
      snippetDir: null,
      why: [
        result && result.failure ? badge(t("detail.failed"), "danger", result.failure) : null,
        result && result.escalate ? badge(t("detail.escalated"), "warn") : null,
        result && result.stale ? badge(t("detail.stale"), "warn") : null,
        !(result && result.failure) && !(result && result.escalate) && !(result && result.stale) ? badge(t("state.ok"), "ok") : null,
        missing ? badge(t("detail.missing", { count: missing }), "warn") : null,
      ],
      onClick: () => {
        replaceRoute("mail", {
          mode: "recent", id: String(id),
          escalated: escalatedOnly ? "1" : "",
        });
        syncMailListSelection(id);
        const panel = document.getElementById("mail-detail");
        if (panel) loadMailDetail(id, panel, { preview: state.triagePreview, mode: "recent", routed: state.triageRouted });
      },
    });
  }

