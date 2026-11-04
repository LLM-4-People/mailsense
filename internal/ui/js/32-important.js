  // ------------------------------------------------------- important mail

  function priorityFilterLabel(filter) {
    return t("mail.filter." + filter);
  }

  function updatePriorityFilterCounts(counts, filterNodes) {
    for (const [filter, node] of filterNodes.entries()) {
      const total = counts.get(filter);
      node.textContent = total === undefined ? "" : formatNumber(total);
      node.hidden = total === undefined;
    }
  }

  function importantTopbarMeta(view) {
    setTopbarMeta(
      gatePill(gateOpen()),
      gatePreview() ? previewPill() : null,
      pill(t("mail.tab.important")),
      view ? pill(t("mail.important.count", { count: view.total }), "pending") : null);
  }

  function importantEmptyState() {
    return emptyState(t("mail.important.empty_title"), t("mail.important.empty_body"),
      [link(t("mail.important.empty_cta"), routeHash("labels", { review: "1" }), { small: true })], "mail");
  }

  // importantPageShape reads the server's paging contract. The fallbacks keep
  // an older response (items plus total only) honest instead of inventing a
  // second paging rule.
  function importantPageShape(view, items) {
    const offset = Number(view && view.offset);
    const available = Number(view && view.available);
    const start = Number.isFinite(offset) ? offset : 0;
    const depth = Number.isFinite(available) ? available : items.length;
    return {
      offset: start,
      available: depth,
      total: Number(view && view.total) || 0,
      hasMore: view && view.has_more !== undefined ? !!view.has_more : start + items.length < depth,
    };
  }

  function paintImportantMore(page) {
    const shape = importantPageShape(page.view, page.items);
    const capped = shape.total > shape.available
      ? t("mail.important.capped", { total: formatNumber(shape.total), available: formatNumber(shape.available) })
      : "";
    const showing = t("mail.important.showing", {
      shown: formatNumber(page.items.length),
      available: formatNumber(shape.available),
    });
    pagerPaint(page.pager, {
      showing: metaLine([showing, capped]),
      end: "",
      error: page.error ? apiFailure(page.error).message : "",
      more: shape.hasMore,
    });
  }

  function appendImportantRows(list, items, filter, selectedId, view, before = null) {
    for (const item of items) {
      const row = importantRow(item, {
        filter,
        selected: item.message_id === selectedId,
        onSelect: (event) => {
          event.preventDefault();
          selectImportantRow(item.message_id, filter, view);
        },
      });
      if (before) list.insertBefore(row, before);
      else list.append(row);
    }
  }

  // loadMoreImportant asks the page after the rows the server already gave
  // us, appends only unseen ids and advances from the response's own offset.
  async function loadMoreImportant(page) {
    if (page.loading || !page.button.isConnected || !pagingLive(page.host, page)) return;
    page.loading = true;
    page.error = null;
    try {
      await withBusy(page.button, t("common.loading"), async () => {
        const before = page.view;
        const next = Number(page.next) || page.items.length;
        const fresh = await request(priorityPath(page.filter, PRIORITY_PAGE_SIZE, next));
        // A cached remount re-appends this screen without rerunning its
        // render. The token it carries is the liveness signal; the captured
        // render sequence belongs to the render, not to the paging control.
        if (!pagingLive(page.host, page) || !page.list.isConnected) return;
        // A rebuild between pages is a different ranking. Never splice two
        // snapshots: restart the screen from its first page instead.
        if (before.generated_at && fresh.generated_at && before.generated_at !== fresh.generated_at) {
          invalidateCache(["/api/conversations", "/api/priority"]);
          refreshActiveRoute();
          return;
        }
        const rows = Array.isArray(fresh.items) ? fresh.items : [];
        const seen = new Set(page.items.map((item) => item.message_id));
        for (const item of rows) {
          if (!item || seen.has(item.message_id)) continue;
          seen.add(item.message_id);
          page.items.push(item);
          appendImportantRows(page.list, [item], page.filter, page.selectedId, page.view, page.pager);
        }
        const start = Number(fresh.offset);
        // The response owns the offset it actually served; the next page
        // starts after its raw rows, including any row already on screen.
        page.next = (Number.isFinite(start) ? start : next) + rows.length;
        const available = Number(fresh.available);
        page.view.available = Number.isFinite(available) ? available : page.items.length;
        page.view.has_more = !!fresh.has_more;
        page.view.total = Number(fresh.total) || page.view.total;
        page.view.items = page.items;
        paintImportantMore(page);
        if (!page.view.has_more) pagerFocusShowing(page.pager);
        prefetchPriorityDetails(page.items);
      });
    } catch (error) {
      page.error = error;
      paintImportantMore(page);
      reportActionError(error);
    } finally {
      page.loading = false;
    }
  }
