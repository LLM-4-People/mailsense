  // clearAskHistory is the rail's one destructive action: clear the stored
  // answers, empty the workspace and resync the route. It is parameterized by
  // the rail's own render and sync so the factory stays within its budget.
  async function clearAskHistory(event, workspace, rerender, syncRoute) {
    // Confirmation awaits, and the dialog's close reparents focus: the click
    // target is gone by the time the work starts. Capture it first so the
    // Clear button can enter its busy state and take focus back.
    const trigger = event && event.currentTarget ? event.currentTarget : null;
    const ok = await confirmDialog(t("ask.history.clear_title"), t("ask.history.clear_body"), t("ask.history.clear_action"), true);
    if (!ok) return;
    await withBusy(trigger, null, async () => {
      try {
        await request("/api/ask/history", { method: "DELETE" });
        state.askHistory = [];
        state.askViews.clear();
        invalidateCache(["/api/ask/history"]);
        workspace.showEmpty();
        rerender();
        syncRoute("");
        workspace.focus();
        toast(t("ask.history.cleared"), "info");
      } catch (error) { reportError(error); }
    });
  }

  // createAskHistoryRail owns the Ask history rail: its reads, its selection
  // and its route sync. The list is a four-field projection; a full answer is
  // fetched only when the owner opens a row.
  // askHistoryListNode is the history rows: one button per question, newest
  // first, with the repeat count and relative time.
  function askHistoryListNode(items, onSelect) {
    return h("div", { class: "history-list section-scroll", dataset: { scrollKey: "ask-history" } },
      uniqueByQuestion(items).map(({ item, count }) => h("button", {
        type: "button",
        class: "history-item",
        "aria-label": t("ask.history.open_aria", { question: item.question || "" }),
        dataset: { answerId: String(item.id || ""), question: item.question || "" },
        onclick: () => onSelect(item),
      },
      h("span", { class: "history-question", dir: "auto", text: item.question }),
      h("span", { class: "history-meta", text: metaLine([askModeLabel(item.mode), formatRelative(item.created_at), count > 1 ? t("ask.history.repeat", { count }) : ""]) }))));
  }

  // askHistoryErrorNode is the failed history read: the API's own message and
  // the retry the rail supplies.
  function askHistoryErrorNode(error, retry) {
    return errorState(error, retry, Object.assign({}, INLINE_READ_ERROR, {
      title: t("ask.history.history_error"),
      body: apiFailure(error).message,
    }));
  }

  function createAskHistoryRail(workspace) {
    const historyHost = h("div", { class: "ask-history-list" });
    const historyNode = h("section", { class: "surface rail-card ask-history-card", "aria-labelledby": "ask-history-title" },
      sectionHead(t("ask.history.title"), { id: "ask-history-title" }),
      historyHost,
      h("div", { class: "button-row ask-history-actions" },
        link(t("ask.history.label_history"), "#/answers", { small: true, kind: "quiet" })));

    function latestHistoryRow() {
      const items = Array.isArray(state.askHistory) ? state.askHistory : [];
      return items.length ? items[0] : null;
    }

    async function latestFullAskView() {
      const row = latestHistoryRow();
      if (!row) return null;
      const cached = cachedAskView(row.id);
      if (cached) return cached;
      try {
        return await loadAskHistoryItem(row.id);
      } catch {
        return null;
      }
    }

    function isEarlierView(view) {
      const latest = latestHistoryRow();
      if (!latest || !view) return false;
      if (latest.id !== undefined && latest.id !== null && view.id !== undefined && view.id !== null) {
        return String(latest.id) !== String(view.id);
      }
      return questionKey(latest.question) !== questionKey(view.question);
    }

    function syncAskRoute(id) {
      const hash = routeHash("ask", id ? { id: String(id) } : {});
      if (location.hash === hash) return;
      history.replaceState(null, "", location.pathname + location.search + hash);
      state.routeKey = routeCacheKey(parseRoute());
    }

    function updateAskHistorySelection() {
      const currentView = workspace.currentView();
      const selectedId = currentView && currentView.id !== undefined && currentView.id !== null ? String(currentView.id) : "";
      const selectedQuestion = currentView ? questionKey(currentView.question) : "";
      for (const item of historyHost.querySelectorAll(".history-item")) {
        const sameId = selectedId && item.dataset.answerId === selectedId;
        const sameQuestion = selectedQuestion && questionKey(item.dataset.question) === selectedQuestion;
        if (sameId || sameQuestion) item.setAttribute("aria-current", "true");
        else item.removeAttribute("aria-current");
      }
    }

    async function selectHistoryItem(item) {
      let view = item ? cachedAskView(item.id) : null;
      if (!view && item) {
        try {
          view = await loadAskHistoryItem(item.id);
        } catch (error) {
          reportError(error);
          return;
        }
      }
      if (!view) return;
      const earlier = isEarlierView(view);
      workspace.showView(view, { focus: true, earlier, past: true });
      updateAskHistorySelection();
      syncAskRoute(earlier ? String(view.id || "") : "");
    }

    function renderAskHistory() {
      clear(historyHost);
      const items = Array.isArray(state.askHistory) ? state.askHistory : [];
      if (loadAskHistoryError && !items.length) {
        historyHost.append(askHistoryErrorNode(loadAskHistoryError, () => {
          loadAskHistory(true).then(renderAskHistory).catch((error) => {
            loadAskHistoryError = error;
            renderAskHistory();
          });
        }));
        return;
      }
      if (!items.length) {
        historyHost.append(h("p", { class: "fine", text: t("ask.history.empty") }));
        return;
      }
      historyHost.append(askHistoryListNode(items, selectHistoryItem));
      historyHost.append(button(t("ask.history.clear"), (event) => clearAskHistory(event, workspace, renderAskHistory, syncAskRoute),
        { small: true, kind: "danger" }));
      prepareScrollRegions(historyHost);
      updateAskHistorySelection();
    }

    async function selectRequested(id) {
      if (!id) return { view: null, earlier: false, found: false };
      const cached = cachedAskView(id);
      try {
        const view = cached || await loadAskHistoryItem(id);
        return { view, earlier: isEarlierView(view), found: true };
      } catch (error) {
        reportError(error);
        return { view: null, earlier: false, found: false };
      }
    }

    return {
      node: historyNode,
      render: renderAskHistory,
      updateSelection: updateAskHistorySelection,
      syncRoute: syncAskRoute,
      latestFullView: latestFullAskView,
      selectRequested,
    };
  }
