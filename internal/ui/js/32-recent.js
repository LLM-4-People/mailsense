  // ------------------------------------------------------- recent triage paging
  //
  // The Recent tab owns a bounded server page. Its paging control is the
  // shared pager; this module keeps the page append and the shown/hidden
  // sentence out of the Mail shell.

  // triagePageIds is the one order of a triage page. The server's ids are
  // authoritative ("Newest first"); only an older server without ids falls
  // back to the descending row id this screen assumed before. An id without
  // a row is ignored and a duplicate keeps its first position.
  function triagePageIds(page, results) {
    const rows = results && typeof results === "object" ? results : {};
    const explicit = page && Array.isArray(page.ids);
    const ids = explicit ? page.ids : Object.keys(rows);
    const seen = new Set();
    const ordered = [];
    for (const raw of ids) {
      const id = Number(raw);
      if (!Number.isFinite(id) || id <= 0 || seen.has(id) || !rows[id]) continue;
      seen.add(id);
      ordered.push(id);
    }
    return explicit ? ordered : ordered.sort((a, b) => b - a);
  }

  // triageAppendIds appends a page's ids to the accumulated list, preserving
  // the first occurrence. It returns the ids that were actually added.
  function triageAppendIds(target, incoming) {
    const seen = new Set(target);
    const added = [];
    for (const id of incoming) {
      if (seen.has(id)) continue;
      seen.add(id);
      target.push(id);
      added.push(id);
    }
    return added;
  }

  // triageVisibleIds is the one projection from the page model to the rows
  // on screen, including the escalated-only filter.
  function triageVisibleIds(view, escalatedOnly) {
    if (!view) return [];
    const results = view.results || {};
    return escalatedOnly ? view.ids.filter((id) => results[id] && results[id].escalate) : view.ids;
  }

  // triageSelectedId keeps the requested id only while it is still on screen;
  // a refresh that removed it falls back to the first row.
  function triageSelectedId(visibleIds, requestedId) {
    return visibleIds.includes(requestedId) ? requestedId : (visibleIds[0] || 0);
  }

  // loadMoreTriage asks the next server page and appends it; it never
  // fetches the full triage view.
  async function loadMoreTriage(view, escalatedOnly, listHost, pager, host) {
    if (!view || state.triagePage !== view || view.loaded >= view.total) return;
    // The token and the mounted list, not the render sequence, say whether
    // this control still belongs to the on-screen screen. A detach-and-cache
    // keeps both; a replaced screen loses both.
    if (!pagingLive(host, view) || !listHost.isConnected) return;
    view.error = null;
    try {
      await withBusy(pagerMoreButton(pager), t("common.loading"), async () => {
        const page = await request("/api/triage?limit=" + TRIAGE_PAGE_SIZE + "&offset=" + view.loaded);
        if (state.triagePage !== view || !pagingLive(host, view) || !listHost.isConnected) return;
        const results = page.results && typeof page.results === "object" ? page.results : {};
        const pageIds = triagePageIds(page, results);
        for (const id of pageIds) view.results[id] = results[id];
        const added = triageAppendIds(view.ids, pageIds);
        view.total = Number(page.total) || view.total;
        view.loaded = Math.min(Number(page.offset) + TRIAGE_PAGE_SIZE, view.total);
        const visibleIds = triageVisibleIds(view, escalatedOnly);
        const selectedId = triageSelectedId(visibleIds, Number(parseRoute().params.get("id")) || 0);
        const visible = escalatedOnly ? added.filter((id) => view.results[id] && view.results[id].escalate) : added;
        for (const id of visible) listHost.append(triageListRow(id, view.results[id], state.questions || {}, selectedId, escalatedOnly));
        updateTriageCount(view, visibleIds.length, pager);
        if (view.loaded >= view.total) pagerFocusShowing(pager);
      });
    } catch (error) {
      view.error = error;
      updateTriageCount(view, triageVisibleIds(view, escalatedOnly).length, pager);
      reportActionError(error);
    }
  }

  // updateTriageCount paints the shared pager. A filter that hides every
  // loaded row says so instead of claiming that zero rows are shown and zero
  // remain unloaded.
  function updateTriageCount(view, shown, pager) {
    const total = view && view.total ? view.total : 0;
    const loaded = view ? Math.max(0, Number(view.loaded) || 0) : 0;
    const hidden = total > 0 && loaded > 0 && shown === 0;
    pagerPaint(pager, {
      hidden: !view || !total,
      showing: !total
        ? ""
        : hidden
          ? t("triage.filter_hidden", { count: loaded })
          : t("triage.shown", {
            shown: formatNumber(shown),
            remaining: formatNumber(Math.max(0, total - loaded)),
            count: shown,
          }),
      error: view && view.error ? apiFailure(view.error).message : "",
      more: !!view && loaded < total,
    });
  }
