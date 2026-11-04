  // ---------------------------------------------------------------- logs

  async function renderLogs(route, seq) {
    if (route.params.has("component")) state.logFilters.component = route.params.get("component") || "";
    if (route.params.has("level")) state.logFilters.level = route.params.get("level") || "";
    // The retained ring already holds the previous visit. Ask only for what
    // arrived since: a remount no longer re-reads 500 entries (~22 KB gzip).
    // When the ring rotated past the cursor, take the page once and resync.
    const since = state.logLast || 0;
    let snapshot = await request(logPagePath(since));
    if (seq !== state.renderSeq) return h("div");
    if (since > 0 && Number(snapshot.oldest_seq) > since + 1) {
      snapshot = await request(logPagePath(0));
      if (seq !== state.renderSeq) return h("div");
    }
    mergeLogSnapshot(snapshot);

    const level = h("select", { class: "select compact", "aria-label": t("logs.level_aria") },
      h("option", { value: "", text: t("logs.all_levels"), selected: state.logFilters.level === "" }),
      ["debug", "info", "warn", "error"].map((value) => h("option", { value, text: value.toUpperCase(), selected: state.logFilters.level === value })));
    const component = h("input", { class: "input compact", type: "search", hidden: true, value: state.logFilters.component, placeholder: t("logs.component_placeholder"), list: "log-components" });
    const components = [...new Set([...state.logEntries.values()].map((entry) => entry.component).filter(Boolean))].sort();
    const datalist = h("datalist", { id: "log-components" }, components.map((value) => h("option", { value })));
    level.addEventListener("change", () => {
      state.logFilters.level = level.value;
      replaceRoute("logs", { level: level.value, component: state.logFilters.component });
      renderLogRows(tableHost);
    });
    // A keystroke updates the filter state immediately; the table rebuild is
    // coalesced so typing never recreates a thousand rows per character.
    let filterTimer = 0;
    const scheduleFilteredRender = () => {
      // Trailing debounce: every keystroke restarts the window, so a fast
      // typist pays one rebuild after the last character. A leading-edge
      // throttle rebuilt the 1000-row table several times per second.
      if (filterTimer) window.clearTimeout(filterTimer);
      filterTimer = window.setTimeout(() => {
        filterTimer = 0;
        if (tableHost.isConnected) renderLogRows(tableHost);
      }, 150);
    };
    component.addEventListener("input", () => {
      state.logFilters.component = component.value;
      replaceRoute("logs", { level: state.logFilters.level, component: component.value });
      scheduleFilteredRender();
    });
    const pause = button(state.logPaused ? t("logs.resume") : t("logs.pause"), () => {
      state.logPaused = !state.logPaused;
      if (!state.logPaused && state.logPending.length) {
        for (const entry of state.logPending) mergeLogEntry(entry);
        state.logPending = [];
        trimLogEntries();
      }
      // The label and the glyph are one control: a "Resume" button with a
      // pause glyph says two different things.
      pause.querySelector("span").textContent = state.logPaused ? t("logs.resume") : t("logs.pause");
      const glyph = pause.querySelector("svg");
      if (glyph) glyph.replaceWith(icon(state.logPaused ? "play" : "pause", "small"));
      renderLogRows(tableHost);
      updateLogMeta();
    }, { icon: state.logPaused ? "play" : "pause", small: true });
    const clearButton = button(t("logs.clear"), () => {
      state.logEntries.clear();
      state.logPending = [];
      state.logExpanded.clear();
      // The meta describes this view's ring. Leaving the old range and tail
      // state behind would claim entries the owner just cleared.
      state.logOldest = 0;
      state.logLast = 0;
      state.logTruncated = false;
      renderLogRows(tableHost);
      updateLogMeta();
      toast(t("logs.cleared"), "info", t("logs.cleared_body"));
    }, { icon: "trash", small: true, kind: "quiet" });
    const refresh = button(t("common.refresh"), async (event) => {
      await runAction(event.currentTarget, t("sources.refreshing"), async () => { await refreshActiveRoute(); });
    }, { icon: "refresh", small: true });

    const toolbar = h("div", { class: "log-toolbar" }, level, pause, clearButton, refresh,
      h("span", { class: "spacer" }),
      h("span", { class: "log-meta", id: "log-meta" }));
    const bannerHost = h("div", { id: "log-banners" });
    const tableHost = h("div", { id: "log-rows" });
    const screen = h("div", { class: "screen logs-screen" },
      section(t("logs.live_title"), t("logs.live_sub"), null, toolbar, bannerHost, tableHost));
    screen.append(component, datalist);
    setScopedSearch("logs", (value) => {
      component.value = value;
      component.dispatchEvent(new Event("input"));
    }, "logs.component_placeholder", "logs.component_aria", () => state.logFilters.component);
    renderLogBanners(bannerHost);
    renderLogRows(tableHost);
    updateLogMeta();
    return screen;
  }

  // The page never carries attributes: each expanded row reads its own entry
  // through /api/logs/{seq}. Both the initial page and every tail read say so
  // explicitly, so a large attribute set cannot come back by accident.
  function logPagePath(since) {
    return since > 0
      ? "/api/logs?since=" + String(since) + "&attrs=off"
      : "/api/logs?attrs=off";
  }

  // mergeLogEntry replaces one held entry without dropping attributes a row
  // already fetched. A page and a live tail both carry scalars and has_attrs
  // only, so the lazy map has to survive every refresh of the same sequence.
  function mergeLogEntry(entry) {
    if (!entry || typeof entry.seq !== "number") return;
    const held = state.logEntries.get(entry.seq);
    if (held && entry.attrs === undefined) {
      if (held.attrs !== undefined) entry.attrs = held.attrs;
      if (held.attrsGone) entry.attrsGone = true;
      if (held.attrsLoading) entry.attrsLoading = true;
      if (held.attrsError) entry.attrsError = held.attrsError;
    }
    state.logEntries.set(entry.seq, entry);
  }

  // mergeLogSnapshot is the one owner of applying a /api/logs page to the
  // view: the initial render replaces the retained set, a live wake merges
  // only the entries it does not hold (and honours pause).
  function mergeLogSnapshot(snapshot, live = false) {
    state.logHealth = snapshot.health || null;
    state.logOldest = snapshot.oldest_seq || 0;
    state.logLast = Math.max(state.logLast || 0, Number(snapshot.last_seq) || 0);
    state.logTruncated = !!snapshot.truncated;
    const entries = Array.isArray(snapshot.entries) ? snapshot.entries : [];
    if (live) {
      for (const entry of entries) addLogEntry(entry);
      return;
    }
    for (const entry of entries) mergeLogEntry(entry);
    trimLogEntries();
  }

  // log.wake carries only a cursor and a count. The Logs screen reads the
  // entries through this one coalesced tail read; every other route ignores
  // the wake before parsing it.
  let logRefreshTimer = 0;
  let logRefreshInFlight = false;
  let logRefreshAgain = false;

  function scheduleLogRefresh() {
    if (state.route !== "logs" || logRefreshTimer) return;
    logRefreshTimer = window.setTimeout(() => {
      logRefreshTimer = 0;
      void refreshLogTail();
    }, 120);
  }

  async function refreshLogTail() {
    if (state.route !== "logs") return;
    if (logRefreshInFlight) {
      logRefreshAgain = true;
      return;
    }
    logRefreshInFlight = true;
    const since = state.logLast || 0;
    try {
      let snapshot = await request(logPagePath(since));
      if (state.route !== "logs") return;
      if (since > 0 && Number(snapshot.oldest_seq) > since + 1) {
        snapshot = await request(logPagePath(0));
        if (state.route !== "logs") return;
      }
      mergeLogSnapshot(snapshot, true);
      scheduleLogRender();
    } catch (error) {
      reportBackgroundError(error);
    } finally {
      logRefreshInFlight = false;
      if (logRefreshAgain) {
        logRefreshAgain = false;
        scheduleLogRefresh();
      }
    }
  }

  function trimLogEntries() {
    // Live entries arrive in sequence order. Trimming in batches leaves a
    // small bounded slack, so the ring is not sorted on every insert.
    const limit = MAX_LOG_ENTRIES + 256;
    if (state.logEntries.size <= limit) return;
    const keys = [...state.logEntries.keys()].sort((a, b) => a - b);
    for (const key of keys.slice(0, state.logEntries.size - MAX_LOG_ENTRIES)) state.logEntries.delete(key);
    // An expansion of a trimmed entry would otherwise keep its id forever.
    for (const seq of state.logExpanded) {
      if (!state.logEntries.has(seq)) state.logExpanded.delete(seq);
    }
  }

  function addLogEntry(entry) {
    if (!entry || typeof entry.seq !== "number") return;
    if (state.route !== "logs") return;
    state.logLast = Math.max(state.logLast, entry.seq);
    if (state.logPaused) {
      state.logPending.push(entry);
      if (state.logPending.length > MAX_LOG_ENTRIES) state.logPending.shift();
      updateLogMeta();
      return;
    }
    mergeLogEntry(entry);
    trimLogEntries();
    scheduleLogRender();
  }

  let logRenderTimer = 0;
  function scheduleLogRender() {
    if (logRenderTimer) return;
    logRenderTimer = window.setTimeout(() => {
      logRenderTimer = 0;
      if (state.route !== "logs") return;
      const tableHost = document.getElementById("log-rows");
      if (tableHost) appendLogRows(tableHost);
      const bannerHost = document.getElementById("log-banners");
      if (bannerHost) renderLogBanners(bannerHost);
      updateLogMeta();
    }, 120);
  }

  function filteredLogs() {
    const level = state.logFilters.level;
    const component = state.logFilters.component.trim().toLowerCase();
    return [...state.logEntries.values()]
      .sort((a, b) => a.seq - b.seq)
      .filter((entry) => {
        if (level && String(entry.level || "").toLowerCase() !== level) return false;
        if (component && !String(entry.component || "").toLowerCase().includes(component)) return false;
        return true;
      });
  }

  // logRowNode is the one row builder of a full paint and a live append.
  // The table keeps its row semantics: the expand control is a real button,
  // and a roving tab stop makes the whole table one Tab stop instead of one
  // per entry (up to a thousand).
  function logRowNode(entry) {
    const level = String(entry.level || "").toUpperCase();
    const expanded = state.logExpanded.has(entry.seq);
    const row = h("tr", { class: "log-row", dataset: { seq: String(entry.seq) } });
    const toggle = iconButton("chevron-right", expanded ? t("logs.collapse") : t("logs.expand"), () => {
      if (state.logExpanded.has(entry.seq)) state.logExpanded.delete(entry.seq);
      else state.logExpanded.add(entry.seq);
      syncLogRowExpanded(row, entry);
    }, { small: true });
    toggle.classList.add("log-toggle");
    toggle.id = "log-toggle-" + entry.seq;
    toggle.setAttribute("aria-expanded", expanded ? "true" : "false");
    // The attribute row exists only while expanded; a collapsed toggle must
    // not point at an element that is not in the document.
    if (expanded) toggle.setAttribute("aria-controls", "log-attrs-" + entry.seq);
    toggle.tabIndex = state.logFocusSeq === entry.seq ? 0 : -1;
    toggle.addEventListener("focus", () => { state.logFocusSeq = entry.seq; });
    toggle.addEventListener("keydown", (event) => {
      const move = { ArrowDown: 1, ArrowUp: -1 }[event.key];
      if (move) {
        event.preventDefault();
        focusLogToggle(entry.seq, move);
      } else if (event.key === "Home") {
        event.preventDefault();
        focusLogToggle(0, 1, true);
      } else if (event.key === "End") {
        event.preventDefault();
        focusLogToggle(0, -1, true);
      }
    });
    appendChildren(row, [
      h("td", { class: "log-toggle-cell" }, toggle),
      h("td", { class: "log-time", text: formatClock(entry.time) }),
      h("td", null, h("span", { class: "log-level " + level, text: level })),
      h("td", { class: "log-component", text: entry.component || "" }),
      h("td", { class: "log-msg", lang: "en", text: entry.msg || "" }),
    ]);
    row.addEventListener("click", (event) => {
      // A click on the control is the control's business; the row stays a
      // mouse shortcut for the rest of its surface.
      if (event.target && event.target.closest && event.target.closest(".log-toggle")) return;
      toggle.click();
    });
    return row;
  }

  // focusLogToggle moves the roving tab stop by one row (or to the first/last
  // row when edge is set).
  function focusLogToggle(seq, step, edge = false) {
    const buttons = [...document.querySelectorAll("#log-rows .log-toggle")];
    if (!buttons.length) return;
    let index = buttons.findIndex((node) => node.id === "log-toggle-" + seq);
    if (edge) index = step > 0 ? 0 : buttons.length - 1;
    else index = Math.min(buttons.length - 1, Math.max(0, index + step));
    for (const node of buttons) node.tabIndex = -1;
    buttons[index].tabIndex = 0;
    buttons[index].focus({ preventScroll: false });
  }

  // syncLogTabStops keeps exactly one tab stop after a paint or append and
  // keeps the remembered row tabbable when it is still shown.
  function syncLogTabStops(host) {
    const buttons = [...host.querySelectorAll(".log-toggle")];
    if (!buttons.length) return;
    let target = buttons[0];
    if (state.logFocusSeq) {
      const remembered = buttons.find((node) => node.id === "log-toggle-" + state.logFocusSeq);
      if (remembered) target = remembered;
    }
    for (const node of buttons) node.tabIndex = node === target ? 0 : -1;
  }

  // logRowsNodes returns each visible row followed by its attribute row when
  // it is expanded. A detached row has no parent, so the full paint cannot
  // use row.after() to insert it.
  function logRowsNodes(entries) {
    const out = [];
    for (const entry of entries) {
      out.push(logRowNode(entry));
      if (state.logExpanded.has(entry.seq)) {
        out.push(logAttrsRow(entry));
        void ensureLogAttrs(entry.seq);
      }
    }
    return out;
  }

  function logAttrsRow(entry) {
    let content;
    if (entry.attrsGone) content = h("pre", { text: t("logs.attrs_gone") });
    else if (entry.attrsError) content = retryBanner(apiFailure(entry.attrsError).message, () => retryLogAttrs(entry.seq));
    else if (entry.attrsLoading) content = h("pre", { text: t("logs.attrs_loading") });
    else {
      const attrs = entry.attrs && typeof entry.attrs === "object" ? entry.attrs : {};
      const lines = Object.keys(attrs).sort().map((key) => key + "=" + jsonValue(attrs[key])).join("\n") || t("logs.no_attributes");
      content = h("pre", { text: lines });
    }
    return h("tr", { id: "log-attrs-" + entry.seq, dataset: { logAttrsFor: String(entry.seq) } },
      h("td", { colspan: "5", class: "log-attrs" }, content));
  }

  // ensureLogAttrs reads one expanded row's attributes at most once. The
  // page carried has_attrs instead of the map; the fetched map stays on the
  // entry (bounded by the row cap), so a collapse/re-expand or a page merge
  // never asks again. A rotated-out entry keeps the retained-message row,
  // while a failed read keeps its retry until the owner asks again.
  async function ensureLogAttrs(seq) {
    const entry = state.logEntries.get(seq);
    if (!entry || entry.has_attrs !== true || entry.attrs !== undefined ||
      entry.attrsLoading || entry.attrsGone || entry.attrsError) return;
    entry.attrsLoading = true;
    syncLogAttrsRow(entry);
    try {
      const detail = await request("/api/logs/" + String(seq));
      const held = state.logEntries.get(seq);
      if (!held) return;
      held.attrs = detail && detail.attrs && typeof detail.attrs === "object" ? detail.attrs : {};
    } catch (error) {
      const held = state.logEntries.get(seq);
      if (!held) return;
      // Only a 404 means the entry rotated out of the retained ring. A
      // transport or server failure leaves the attributes unknown, so it
      // keeps the row expanded with an inline retry instead of claiming the
      // entry vanished.
      if (error instanceof ApiError && error.status === 404) held.attrsGone = true;
      else held.attrsError = error;
    } finally {
      const held = state.logEntries.get(seq);
      if (held) {
        held.attrsLoading = false;
        syncLogAttrsRow(held);
      }
    }
  }

  // retryLogAttrs clears a failed attribute read and runs the same bounded
  // one-entry read again. A 404 still settles as gone; any other failure
  // returns to this retry surface instead of claiming the entry vanished.
  function retryLogAttrs(seq) {
    const entry = state.logEntries.get(seq);
    if (!entry) return;
    entry.attrsError = null;
    ensureLogAttrs(seq);
  }

  function syncLogAttrsRow(entry) {
    if (!state.logExpanded.has(entry.seq)) return;
    const current = document.getElementById("log-attrs-" + entry.seq);
    if (current) current.replaceWith(logAttrsRow(entry));
  }

  function syncLogRowExpanded(row, entry) {
    const open = state.logExpanded.has(entry.seq);
    const next = row.nextElementSibling;
    const shown = next && next.dataset.logAttrsFor === String(entry.seq);
    if (open && !shown) {
      // A page merge replaces the map's entry object; the row keeps the one
      // it was painted with. Read the current entry so a fetch that landed
      // in between is not lost on re-expand.
      const current = state.logEntries.get(entry.seq) || entry;
      row.after(logAttrsRow(current));
      void ensureLogAttrs(current.seq);
    } else if (!open && shown) next.remove();
    const toggle = row.querySelector(".log-toggle");
    if (toggle) {
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) toggle.setAttribute("aria-controls", "log-attrs-" + entry.seq);
      else toggle.removeAttribute("aria-controls");
      toggle.setAttribute("aria-label", open ? t("logs.collapse") : t("logs.expand"));
      toggle.setAttribute("title", open ? t("logs.collapse") : t("logs.expand"));
    }
  }

  function logFilterKey() {
    return state.logFilters.level + "\u0000" + state.logFilters.component.trim().toLowerCase();
  }

  function renderLogRows(host) {
    if (!host) return;
    clear(host);
    host._logState = null;
    const entries = filteredLogs();
    const shown = entries.slice(-1000);
    if (!shown.length) {
      host.append(emptyState(t("logs.empty_title"), state.logFilters.level || state.logFilters.component ? t("logs.empty_filtered") : t("logs.empty_ring"), null, "logs"));
      return;
    }
    const tbody = h("tbody", null, logRowsNodes(shown));
    const table = h("table", { class: "log-table" },
      h("caption", { class: "sr-only", text: t("logs.table.caption") }),
      h("thead", null, h("tr", null, h("th", { class: "log-toggle-head", "aria-hidden": "true", scope: "col" }),
        h("th", { scope: "col", text: t("logs.table.time") }), h("th", { scope: "col", text: t("logs.table.level") }),
        h("th", { scope: "col", text: t("logs.table.component") }), h("th", { scope: "col", text: t("logs.table.message") }))),
      tbody);
    // appendChildren skips a null note; Node.append(null) would insert the
    // literal text "null" and add a line box the section cannot absorb.
    appendChildren(host, [h("div", { class: "logs section-scroll", dataset: { scrollKey: "logs" } }, table), logShowingNode(entries.length, shown.length)]);
    host._logState = { filterKey: logFilterKey(), seqs: shown.map((entry) => entry.seq), tbody, retained: entries.length };
    syncLogTabStops(host);
  }

  function logShowingNode(retained, shown) {
    if (retained <= shown) return null;
    return h("p", { class: "fine log-showing", text: t("logs.showing", { shown: formatNumber(shown), retained: formatNumber(retained) }) });
  }

  function updateLogShowing(host, painted, retained) {
    const note = host.querySelector(".log-showing");
    const shown = painted.seqs.length;
    if (retained > shown) {
      if (note) note.textContent = t("logs.showing", { shown: formatNumber(shown), retained: formatNumber(retained) });
      else host.append(logShowingNode(retained, shown));
    } else if (note) {
      note.remove();
    }
  }

  // appendLogRows paints a live batch without rebuilding the table. It falls
  // back to a full paint when the filter changed, the shown window lost an
  // entry to the ring, or an older entry arrived (both change the window).
  function appendLogRows(host) {
    const painted = host._logState;
    if (!painted || painted.filterKey !== logFilterKey() || !painted.tbody.isConnected) {
      renderLogRows(host);
      return;
    }
    const entries = filteredLogs();
    const present = new Set(entries.map((entry) => entry.seq));
    for (const seq of painted.seqs) {
      if (!present.has(seq)) { renderLogRows(host); return; }
    }
    const last = painted.seqs.length ? painted.seqs[painted.seqs.length - 1] : 0;
    const fresh = entries.filter((entry) => entry.seq > last);
    if (entries.length !== painted.seqs.length + fresh.length) { renderLogRows(host); return; }
    if (!fresh.length) { updateLogShowing(host, painted, entries.length); return; }
    painted.tbody.append(...logRowsNodes(fresh));
    painted.seqs.push(...fresh.map((entry) => entry.seq));
    while (painted.seqs.length > 1000) {
      const seq = painted.seqs.shift();
      const row = painted.tbody.querySelector('tr[data-seq="' + seq + '"]');
      if (!row) continue;
      const attrs = row.nextElementSibling;
      if (attrs && attrs.dataset.logAttrsFor === String(seq)) attrs.remove();
      row.remove();
    }
    syncLogTabStops(host);
    updateLogShowing(host, painted, entries.length);
  }

  function renderLogBanners(host) {
    if (!host) return;
    clear(host);
    const warnings = [];
    const health = state.logHealth;
    if (health) {
      if (health.file_failing) {
        warnings.push(h("div", null,
          h("span", { text: t("logs.file_failing", { detail: "", count: health.file_errors }) }),
          health.last_file_error ? h("span", { lang: "en", text: t("logs.file_failing_detail", { error: health.last_file_error }) }) : null));
      } else if (health.file_errors) {
        warnings.push(h("div", { text: t("logs.file_errors", { count: health.file_errors }) }));
      }
      if (health.bus_dropped) warnings.push(h("div", { text: t("logs.bus_dropped", { count: health.bus_dropped }) }));
    }
    if (state.logTruncated) warnings.push(h("div", { text: t("logs.snapshot_truncated") }));
    for (const dropped of state.logDropped.slice(-5)) {
      const topics = dropped.topics && typeof dropped.topics === "object"
        ? Object.entries(dropped.topics).map(([topic, count]) => topic + " ×" + count).join(", ") : "";
      warnings.push(h("div", null,
        h("span", { text: t("logs.event_loss", { reason: "" }) }),
        h("span", { lang: "en", text: dropped.reason || t("logs.reason.unknown") }),
        dropped.total ? h("span", { text: t("logs.event_loss_total", { count: dropped.total }) }) : null,
        topics ? h("span", { text: t("logs.event_loss_topics", { topics }) }) : null));
    }
    if (!warnings.length) return;
    host.append(warningBanner(h("div", null, warnings)));
  }

  function updateLogMeta() {
    const meta = document.getElementById("log-meta");
    if (!meta) return;
    clear(meta);
    const total = state.logEntries.size;
    meta.append(
      h("span", { text: t("logs.meta.retained", { count: total }) }),
      h("span", { text: t("logs.meta.ring", { from: formatNumber(state.logOldest), to: formatNumber(Math.max(state.logLast, state.logOldest)) }) }),
      state.logPaused ? h("span", { class: "warn-text", text: t("logs.meta.paused", { count: state.logPending.length }) }) : h("span", { class: "ok-text", text: t("logs.meta.tailing") }));
  }

