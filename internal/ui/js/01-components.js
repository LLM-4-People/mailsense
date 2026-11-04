  // ---------------------------------------------------------------- components

  // diagnosticDetails is the one English diagnostic disclosure: a summary the
  // caller names and one English paragraph. The toast, the Ask failure, the
  // source backfill, the Labels blocker, the Search notice and the error screen
  // all read it, so the disclosure markup and its language cannot drift.
  function diagnosticDetails(text, options = {}) {
    if (!text) return null;
    const { summary = "common.diagnostic", paragraphClass = "", dataset = null } = options;
    const paragraph = { lang: "en", text };
    if (paragraphClass) paragraph.class = paragraphClass;
    if (dataset) paragraph.dataset = dataset;
    return h("details", { class: "blocker-diagnostic", lang: "en" },
      h("summary", { text: t(summary) }),
      h("p", paragraph));
  }

  // thread-state:start
  // THREAD_STATES is the one thread-state taxonomy: validity, label and badge
  // kind derive from it, so the list, the conversation card, the filter chips
  // and the Brief cannot disagree about the same thread (answered_by_other
  // was "ok" in the list and "plain" in the card).
  const THREAD_STATES = [
    "awaiting_owner", "awaiting_other", "answered_by_owner",
    "answered_by_other", "no_action", "unknown",
  ];

  function threadStateValid(value) {
    return THREAD_STATES.includes(String(value || ""));
  }

  function threadStateKey(value) {
    const raw = String(value || "");
    return THREAD_STATES.includes(raw) ? raw : "unknown";
  }

  function threadStateLabel(value) {
    return t("detail.thread.state." + threadStateKey(value));
  }

  function threadStateKind(value) {
    switch (threadStateKey(value)) {
      case "awaiting_owner": return "accent";
      case "answered_by_owner":
      case "answered_by_other": return "ok";
      case "unknown": return "warn";
      default: return "plain";
    }
  }

  // threadParticipantNames is the one participant-name projection of a
  // thread row: the display name, deduplicated by address when it has one.
  // The conversation list and the thread card both read it.
  function threadParticipantNames(participants) {
    const names = [];
    const seen = new Set();
    for (const participant of Array.isArray(participants) ? participants : []) {
      const name = String((participant && (participant.name || participant.address)) || "").trim();
      const key = String((participant && (participant.address || participant.name)) || "").trim().toLowerCase();
      if (!name || !key || seen.has(key)) continue;
      seen.add(key);
      names.push(name);
    }
    return names;
  }
  // thread-state:end

  // markFieldError is the one field-error boundary: the message is an
  // alert, the form control is marked invalid, and the message becomes part
  // of that control's description. Clearing removes all three, so a stale
  // aria-invalid cannot outlive its message.
  let fieldErrorSeq = 0;
  function markFieldError(control, node, message) {
    if (!node) return;
    node.setAttribute("role", "alert");
    if (!node.id) node.id = "field-error-" + String(++fieldErrorSeq);
    node.textContent = message || "";
    node.hidden = !message;
    if (!control) return;
    const formControl = control.matches && control.matches("input, select, textarea, button")
      ? control
      : (control.querySelector ? control.querySelector("input, select, textarea, button") || control : control);
    const ids = new Set(String(formControl.getAttribute("aria-describedby") || "").split(/\s+/).filter(Boolean));
    if (message) {
      formControl.setAttribute("aria-invalid", "true");
      ids.add(node.id);
    } else {
      formControl.removeAttribute("aria-invalid");
      ids.delete(node.id);
    }
    if (ids.size) formControl.setAttribute("aria-describedby", [...ids].join(" "));
    else formControl.removeAttribute("aria-describedby");
  }

  async function withBusy(trigger, label, fn) {
    const target = trigger && trigger.querySelector ? trigger.querySelector("span") : null;
    const old = target ? target.textContent : "";
    // Disabling a focused control moves focus to the body. Remember that the
    // trigger owned focus, and put it back when the work finishes: a Save,
    // Apply or Load more must not send the keyboard user to the shell top.
    const ownedFocus = !!(trigger && typeof document !== "undefined" && document.activeElement === trigger);
    if (trigger) trigger.disabled = true;
    if (target && label) target.textContent = label;
    try {
      return await fn();
    } finally {
      if (trigger && trigger.isConnected) trigger.disabled = false;
      if (target && target.isConnected && label) target.textContent = old;
      if (ownedFocus && trigger && trigger.isConnected && document.activeElement === document.body) {
        trigger.focus();
      }
    }
  }

  // pagingToken seats a paged screen's model on the node that rendered it.
  // A cached mount re-appends that node without rerunning its render, so a
  // closure that captured the render sequence would look dead while its
  // control is on screen. The node owns the one liveness token instead: every
  // Load more compares the token it finds mounted, and the router restores the
  // screen's model from the same node on a cached hit.
  function pagingToken(node, page) {
    if (node && page) node.__pagingModel = page;
    return page;
  }

  function readPagingToken(node) {
    return node && node.__pagingModel ? node.__pagingModel : null;
  }

  function pagingLive(node, page) {
    return !!node && !!page && readPagingToken(node) === page && node.isConnected;
  }

  // stat-strip:start
  // statItem is the one label/value cell of every stat strip. A value-first
  // cell (Home, Brief) and a link cell (Brief's jump anchors) are the same
  // structure, so the three screens cannot drift apart again.
  function statItem(item) {
    const parts = [
      h("span", { class: "stat-label", text: item.label }),
      h("strong", { class: "stat-value", title: item.title || "", text: item.value == null ? t("common.dash") : String(item.value) }),
    ];
    if (item.valueFirst) parts.reverse();
    const attrs = { class: ("stat " + (item.href ? "link " : "") + (item.className || "")).trim() };
    if (item.href) attrs.href = item.href;
    if (item.onclick) attrs.onclick = item.onclick;
    if (item.ariaLabel) attrs["aria-label"] = item.ariaLabel;
    return h(item.href ? "a" : "div", attrs, parts);
  }

  function statStripItems(items) {
    return items.map(statItem);
  }

  // statStrip is the one card of stat cells. The screens pass their own
  // layout class; the border, background and cell separators have one owner.
  function statStrip(items, opts = {}) {
    return h("div", {
      class: ("stat-strip " + (opts.className || "")).trim(),
      role: opts.role || null,
      "aria-label": opts.ariaLabel || null,
      title: opts.title || "",
    }, opts.note ? h("p", { class: "stat-strip-note", text: opts.note }) : null, statStripItems(items));
  }
  // stat-strip:end

  // warning-banner:start
  // warningBanner is the one inline warning surface: an alert icon and the
  // caller's content (a sentence, a strong/paragraph block, or an action
  // row). Nine screens spelled the same three nodes by hand. A warning is a
  // live status by default so a screen reader hears a failed supplementary
  // read without the caller remembering role="status".
  function warningBanner(content, opts = {}) {
    const attrs = { class: ("warning-banner " + (opts.className || "")).trim() };
    const role = opts.role === undefined ? "status" : opts.role;
    if (role) attrs.role = role;
    return h("div", attrs, icon(opts.icon || "alert"), content);
  }
  // warning-banner:end

  // retry-banner:start
  // retryBanner is the one inline failed read with a Try again action.
  // message is the already normalized apiFailure sentence, so each caller
  // keeps its own error object; extra is the caller's additional detail (a
  // rejected provider file's problems) between the message and the action.
  // The action is the shared button primitive, so it stays a real button in
  // the tab order.
  function retryBanner(message, onRetry, extra) {
    return warningBanner(fragment(
      h("span", { text: message }),
      extra || null,
      button(t("common.try_again"), onRetry, { small: true, icon: "refresh" })));
  }
  // retry-banner:end

  // tab-button:start
  // tabButton is one segment of a route switch. The segments navigate to
  // another URL, so they are not tabs over one panel: aria-current is the
  // honest state and the group is a labelled nav. The stable focus key lets
  // the router put focus back on the same segment after the route rebuild.
  function tabButton(label, selected, onClick, focusKey = "") {
    const attrs = { type: "button", "aria-current": selected ? "page" : null, text: label };
    if (focusKey) attrs.dataset = { focusKey };
    const b = h("button", attrs);
    b.addEventListener("click", onClick);
    return b;
  }
  // tab-button:end

  // filter-chip:start
  // filterChip is one pressed chip of a filter strip, with an optional count
  // badge. `count` undefined drops the badge (a folder chip hides a zero),
  // null keeps an empty badge for a painter that fills it later, and a number
  // prints it. The badge node is returned so a page painter updates the same
  // span instead of rebuilding the chip. The stable focus key lets the router
  // put focus back on the chip after the filter strip is rebuilt.
  function filterChip(opts) {
    const active = !!opts.active;
    const count = opts.count === undefined ? null
      : h("span", {
        class: "filter-count",
        "aria-hidden": "true",
        text: opts.count === null ? "" : formatNumber(opts.count),
      });
    const chip = h("button", {
      type: "button",
      class: "chip chip-button" + (active ? " active" : ""),
      "aria-pressed": active ? "true" : "false",
      dataset: { focusKey: opts.focusKey },
      onclick: opts.onPick,
    }, h("span", { text: opts.label }), count);
    return { chip, count };
  }
  // filter-chip:end

  // sender-chip:start
  // senderChip is the one removable chip that names the sender filter. The
  // mail list and the search screen show it above their other filters, with
  // the sender ellipsized and one clear control that keeps the rest of the
  // screen's filter state. An empty sender renders nothing, so no call site
  // spells the "only when a sender is set" ternary.
  function senderChip(sender, onClear) {
    if (!sender) return null;
    return h("span", { class: "chip sender-chip", title: sender },
      h("span", { class: "sender-chip-label", dir: "auto", text: t("mail.filter.sender_chip", { sender }) }),
      h("button", { type: "button", "aria-label": t("mail.filter.sender_clear"), title: t("mail.filter.sender_clear"), onclick: onClear },
        icon("close", "small")));
  }
  // sender-chip:end

  // pager:start
  // pagerNode is the one footer of every paged list: the showing count, the
  // load-more control, the end line and a failed-page line share the same
  // structure and live region. A host may add a layout class or a note.
  function pagerNode(opts = {}) {
    const showing = h("span", { class: "pager-showing", role: "status", "aria-live": "polite", tabIndex: -1 });
    const end = h("span", { class: "pager-end fine" });
    const error = h("span", { class: "pager-error", role: "alert", hidden: true });
    const note = opts.note ? h("span", { class: "pager-note fine", text: opts.note }) : null;
    const more = button(opts.moreLabel || t("common.load_more"), (event) => {
      const handler = node.__pager ? node.__pager.onMore : null;
      if (handler) handler(event);
    }, { small: true, kind: "quiet" });
    const node = h("div", { class: ("pager " + (opts.className || "")).trim(), hidden: opts.hidden || undefined },
      showing, end, error, note, h("span", { class: "spacer" }), more);
    node.__pager = { showing, end, error, note, more, onMore: opts.onMore || null };
    pagerPaint(node, opts);
    return node;
  }

  // pagerPaint applies the pager's four states without rebuilding it, so a
  // page load keeps the focused Load more control and its position.
  function pagerPaint(node, state = {}) {
    const model = node && node.__pager;
    if (!model) return node;
    if (state.showing !== undefined) {
      model.showing.textContent = state.showing || "";
      model.showing.hidden = !state.showing;
    }
    if (state.end !== undefined) {
      model.end.textContent = state.end || "";
      model.end.hidden = !state.end;
    }
    if (state.error !== undefined) {
      model.error.textContent = state.error || "";
      model.error.hidden = !state.error;
    }
    if (state.note !== undefined && model.note) {
      model.note.textContent = state.note || "";
      model.note.hidden = !state.note;
    }
    if (state.more !== undefined) model.more.hidden = !state.more;
    if (state.hidden !== undefined) node.hidden = !!state.hidden;
    if (state.onMore !== undefined) model.onMore = state.onMore;
    return node;
  }

  function pagerMoreButton(node) {
    return node && node.__pager ? node.__pager.more : null;
  }

  function pagerFocusShowing(node) {
    const model = node && node.__pager;
    if (!model || !model.showing.isConnected) return;
    model.showing.focus({ preventScroll: true });
  }
  // pager:end

  // unavailable-state:start
  // unavailableState is one message that can no longer be shown: the
  // answers panel and the search detail had the same icon-and-sentence block
  // (and search reused the answers wording).
  function unavailableState(message, iconName = "mail") {
    return h("div", { class: "unavailable" }, icon(iconName), h("p", { text: message }));
  }
  // unavailable-state:end

  // locked-card:start
  // lockedCard is the one "learning is not open yet" panel: a lock, a
  // heading, one sentence and whatever action the screen gives. Mail and the
  // labels workbench had the same three nodes spelled by hand.
  function lockedCard(title, body, ...extra) {
    return h("div", { class: "locked" },
      h("div", { class: "button-row" }, icon("lock"), h("h2", { text: title })),
      h("p", { class: "lede", text: body }),
      extra);
  }
  // locked-card:end

  // mail-row:start
  // mailRow is the one list-row skeleton of Mail, Search, Important and the
  // triage list: avatar, sender/time line, subject, optional path and
  // snippet, and the reason badges. The caller owns the element type, href,
  // classes, selection, dataset, accessible name and click action; the
  // structure and class names have one owner so the four screens cannot
  // drift. A null *Dir omits the attribute the source element did not have.
  function mailRow(opts) {
    const attrs = { class: opts.className, dataset: opts.dataset || {}, "aria-label": opts.ariaLabel || null };
    // The open message is announced as the current item, not only painted:
    // a screen reader cannot see the .selected background.
    if (opts.selected) attrs["aria-current"] = "true";
    if (opts.tag === "a") attrs.href = opts.href;
    else attrs.type = "button";
    const senderAttrs = { class: "mail-row-sender", text: opts.sender };
    if (opts.senderDir !== null) senderAttrs.dir = opts.senderDir || "auto";
    const subjectAttrs = { class: "mail-row-subject", text: opts.subject };
    if (opts.subjectDir !== null) subjectAttrs.dir = opts.subjectDir || "auto";
    if (opts.subjectTitle !== undefined) subjectAttrs.title = opts.subjectTitle;
    const timeAttrs = { class: "mail-row-time", text: opts.timeText || "" };
    if (opts.timeTitle !== undefined) timeAttrs.title = opts.timeTitle;
    const snippetAttrs = { class: "mail-row-snippet", text: opts.snippet };
    if (opts.snippetDir !== null) snippetAttrs.dir = opts.snippetDir || "auto";
    const node = h(opts.tag || "a", attrs,
      h("span", { class: "mail-avatar", "aria-hidden": "true", text: opts.avatar }),
      h("span", { class: "mail-row-main" },
        h("span", { class: "mail-row-top" },
          h("span", senderAttrs),
          opts.unread ? h("span", { class: "unread-dot", role: "img", "aria-label": t("mail.unread_aria") }) : null,
          opts.spacer ? h("span", { class: "spacer" }) : null,
          h("span", timeAttrs)),
        h("span", subjectAttrs),
        opts.path ? h("span", { class: "mail-row-path", dir: "auto", title: opts.path, text: opts.path }) : null,
        opts.snippet !== undefined ? h("span", snippetAttrs) : null,
        h("span", { class: "mail-row-why" }, opts.why)));
    if (opts.onClick) node.addEventListener("click", opts.onClick);
    return node;
  }
  // mail-row:end

  // measure:start
  function measureNode(measure) {
    if (!measure) return null;
    const have = Number(measure.have) || 0;
    const need = Number(measure.need) || 0;
    const unit = measureUnit(measure.unit, need);
    const pct = percentOf(have, need);
    // A precision bound and its target differ in the third decimal (0.985
    // against 0.99); two decimals rendered them equal. Counts stay whole.
    const whole = have % 1 === 0 && need % 1 === 0;
    const digits = whole ? 0 : (need <= 1 && have <= 1 ? 3 : 2);
    return h("div", { class: "blocker-measure" },
      h("span", { class: "measure-text", text: formatNumber(have, digits) + " / " + formatNumber(need, digits) + (unit ? " " + unit : "") + (need > 0 ? " (" + formatPercent(pct / 100, 0) + ")" : "") }),
      h("progress", { max: Math.max(need, 1), value: Math.min(have, Math.max(need, 1)), class: pct >= 100 ? "" : "warn", "aria-label": t("common.progress_aria", { unit }) }));
  }
  // measure:end

  // state-block:start
  // emptyState is the one empty surface of every screen. The wrapper, icon,
  // heading and copy hierarchy live here. A host that is itself the box (a
  // pane with its own padding and background) asks for the content without
  // the wrapper; a heading level and a host class are presentation the screen
  // owns, the same way sectionHead takes them.
  function emptyState(title, body, actions, iconName = "alert", opts = {}) {
    // A caller may pass one node (the Ask suggestions) or a list; both are
    // one action row, and an empty list is no row at all.
    const actionNodes = Array.isArray(actions) ? actions.filter(Boolean) : actions ? [actions] : [];
    const headingAttrs = { text: title };
    if (opts.headingId) headingAttrs.id = opts.headingId;
    if (opts.headingTabIndex !== undefined) headingAttrs.tabIndex = opts.headingTabIndex;
    const content = [
      opts.icon === false ? null : h("div", { class: "empty-icon" }, icon(iconName, "large")),
      opts.heading === false ? null : h(opts.heading || "h2", headingAttrs),
      opts.body === false ? null : h("p", { text: body }),
      actionNodes.length ? h("div", { class: "button-row" }, actionNodes) : null,
    ];
    if (opts.wrap === false) return fragment(...content);
    return h("div", { class: opts.hostClass || "empty", role: opts.role || null, hidden: opts.hidden || undefined }, content);
  }

  // errorState is the one failed-read surface. It owns the default retry
  // wording and icon; a caller that needs another title, body, icon,
  // presentation or a different action set (or none) passes it instead of
  // rebuilding the state.
  function errorState(error, retry, opts = {}) {
    const actions = Array.isArray(opts.actions) ? opts.actions
      : opts.retry === false ? []
      : [button(t("common.try_again"), retry || (() => refreshActiveRoute()), { small: true, icon: "refresh" })];
    return emptyState(opts.title || t("error.title"), opts.body || apiFailure(error).message,
      actions, opts.icon || "alert", opts);
  }

  // A loading surface is a live status: a screen reader must hear the wait
  // even when focus stays elsewhere. A caller with its own live region opts
  // out with role: "".
  function loadingState(message = t("common.loading"), opts = {}) {
    return h("div", { class: ("loading " + (opts.className || "")).trim(), role: opts.role === undefined ? "status" : (opts.role || null) },
      h("span", { class: "spinner", "aria-hidden": "true" }), text(message));
  }
  // state-block:end
