  // ---------------------------------------------------------------- hub

  // homeSection is the one Home column. Every Home section — brief, agenda,
  // important, the locked cockpit — is this primitive: a fixed head with one
  // title size, an optional kicker and count, and a body that owns the
  // column's scroll. `flat` drops the surface and the scroll for a block
  // inside the More disclosure, so the typography stays identical there too.
  function homeSection(options) {
    const { id, kicker, title, sub, count, actions, body, scrollKey, accent, flat } = options;
    const side = count != null || actions
      ? h("div", { class: "home-section-side" },
        count != null ? h("span", { class: "home-section-count", text: formatNumber(count) }) : null,
        actions ? h("div", { class: "section-actions" }, actions) : null)
      : null;
    const classes = ["home-section"];
    if (flat) classes.push("home-section-flat");
    else classes.push("surface");
    if (accent) classes.push("home-section-" + accent);
    return h("section", { class: classes.join(" "), "aria-labelledby": id },
      h("header", { class: "home-section-head" },
        h("div", { class: "home-section-title" },
          kicker ? h("span", { class: "home-section-kicker", text: kicker }) : null,
          h("h2", { id, text: title })),
        side,
        sub ? h("p", { class: "home-section-sub", title: sub, text: sub }) : null),
      h("div", { class: "home-section-body", dataset: scrollKey ? { scrollKey } : null }, body));
  }

  async function renderHub(route, seq) {
    // Learning feeds the shell chrome and other screens, never a Home
    // column. Start it now and keep the first paint off its critical path.
    settleRead(loadLearning()).then((read) => {
      if (read.error && seq === state.renderSeq) reportBackgroundError(read.error);
    });
    // The gate decides which Home this is; the Hub feeds the generated
    // stamp and the More rail. They are the shell reads and run together.
    const [view, gateRead] = await Promise.all([
      loadHub(),
      readGate(),
    ]);
    if (seq !== state.renderSeq) return h("div");
    const permissionKnown = !!gateRead.value || gateKnown();
    const open = permissionKnown && gateOpen();
    const preview = permissionKnown && gatePreview();
    const showPreview = preview && !open;
    setTopbarMeta(
      permissionKnown ? gatePill(open) : null,
      showPreview ? previewPill() : null,
      view && view.generated_at ? h("span", { class: "fine", title: formatDate(view.generated_at), text: formatDate(view.generated_at, { year: false }) }) : null);
    if (!open && !preview) return lockedHome(view);

    const columns = hubColumns(view);
    const generated = view && view.generated_at ? formatDate(view.generated_at, { year: false }) : "";
    const head = h("header", { class: "home-head" },
      h("div", { class: "home-head-title" },
        h("h2", { class: "home-head-heading", text: t("hub.brief.title") }),
        generated ? h("span", { class: "home-head-date", text: generated }) : null),
      columns.countsHost);

    const more = h("details", { class: "surface home-more" },
      h("summary", { class: "home-more-summary" },
        icon("chevron-right", "small"),
        h("span", { class: "home-more-title", text: t("hub.brief.more") }),
        h("span", { class: "fine", text: t("hub.brief.more_hint") })),
      h("div", { class: "home-more-body section-scroll", dataset: { scrollKey: "home-more" } },
        homeRecentSection(settleRead(loadAskHistory())),
        homeLearningSection(view),
        homeMailboxSection(view)));

    return h("div", { class: "screen home-screen" },
      head,
      showPreview ? previewBanner(t("preview.home_body"), [
        link(t("preview.open_mail"), routeHash("mail", { mode: "important" }), { kind: "primary", small: true }),
        button(t("preview.disable"), (event) => disablePreview(event.currentTarget), { small: true, kind: "quiet" }),
      ], true) : null,
      columns.grid,
      more);
  }

  // hubColumns wires the three independent Home reads to their own
  // sections. The returned grid is already mounted: each read fills its
  // section in place when it settles, so no column waits on another.
  function hubColumns(view) {
    // Each column owns its read. They start together and fill their own
    // section when they settle, so a cold priority read never holds the
    // agenda or the rest of the shell.
    const priorityRead = settleRead(loadPriority("all", PRIORITY_HOME_SIZE));
    const agendaRead = settleRead(loadAgenda(state.agendaHours));
    let priorityView = null;
    let priorityError = null;
    let prioritySettled = false;
    let agendaView = null;
    let agendaError = null;
    let agendaSettled = false;

    const countsHost = h("div", { class: "home-head-counts" });
    const drawCounts = (nextView, error) => {
      clear(countsHost);
      countsHost.append(statStrip(homeBriefStatNodes(nextView, error), {
        className: "head-stats",
        role: "group",
        ariaLabel: t("hub.brief.counts_aria"),
        title: t("hub.brief.counts_hint"),
      }));
    };
    // An agenda read that has not landed yet is not an empty agenda: the
    // counts say unknown until the read settles.
    drawCounts(null, null);

    let briefNode = homeBriefSection(null, null, null, view, true);
    let importantNode = homeImportantSection(null, null, true);
    const drawBrief = () => {
      if (!prioritySettled || !agendaSettled) return;
      briefNode = paintHomeSection(
        briefNode,
        homeBriefSection(priorityView, agendaView, priorityError, view));
      prepareScrollRegions(briefNode);
    };
    const drawImportant = () => {
      if (!prioritySettled) return;
      importantNode = paintHomeSection(
        importantNode,
        homeImportantSection(priorityView, priorityError));
      prepareScrollRegions(importantNode);
    };

    const agendaHost = h("div", { class: "home-section-host" });
    let horizonFocus = 0;
    const drawAgenda = (nextView, error, loading = false) => {
      clear(agendaHost);
      agendaHost.append(homeAgendaSection(nextView, error, loading ? null : (hours) => {
        state.agendaHours = hours;
        horizonFocus = hours;
        clear(agendaHost);
        agendaHost.append(homeAgendaSection(null, null, null, true));
        loadAgenda(hours, true).then(
          (next) => drawAgenda(next, null),
          (err) => drawAgenda(null, err));
      }, loading));
      drawCounts(nextView, error);
      prepareScrollRegions(agendaHost);
      // The chip that switched the horizon was destroyed with the section.
      // Once the loaded (or failed) repaint lands, return focus to the chip
      // that now carries aria-pressed="true".
      if (horizonFocus && !loading) {
        const key = agendaHorizonFocusKey(horizonFocus);
        horizonFocus = 0;
        const chip = agendaHost.querySelector('[data-focus-key="' + key + '"]');
        if (chip) chip.focus({ preventScroll: true });
      }
    };
    drawAgenda(null, null, true);

    priorityRead.then((read) => {
      // The section nodes are the ones this render built. They may already be
      // in the route cache (the owner left before a cold read landed), so the
      // fill must still land: a cached Home is not a frozen skeleton.
      prioritySettled = true;
      if (read.error) priorityError = read.error;
      else priorityView = read.value;
      if (priorityView) prefetchPriorityDetails(priorityView.items);
      drawImportant();
      drawBrief();
    });
    agendaRead.then((read) => {
      agendaSettled = true;
      if (read.error) {
        agendaError = read.error;
      } else {
        agendaView = read.value;
        // The first read omits hours; adopt the window the server applied so
        // the chips and the next request name the configured default.
        const applied = Number(agendaView && agendaView.horizon_hours);
        if (applied > 0) state.agendaHours = applied;
      }
      drawAgenda(agendaView, agendaError);
      drawBrief();
    });

    const grid = h("div", { class: "home-brief-grid" },
      briefNode,
      agendaHost,
      importantNode);
    return { countsHost, grid };
  }

  // paintHomeSection fills the mounted section from the freshly built one.
  // The root, its head and the focused action stay in place; only the body
  // and the count change. The loading skeleton already owns the body node,
  // so its scroll binding survives the first real fill too.
  function paintHomeSection(current, next) {
    const body = current.querySelector(".home-section-body");
    const nextBody = next.querySelector(".home-section-body");
    if (body && nextBody) {
      clear(body);
      while (nextBody.firstChild) body.append(nextBody.firstChild);
    }
    const side = current.querySelector(".home-section-side");
    const count = side ? side.querySelector(".home-section-count") : null;
    const nextSide = next.querySelector(".home-section-side");
    const nextCount = nextSide ? nextSide.querySelector(".home-section-count") : null;
    if (count && nextCount) count.textContent = nextCount.textContent;
    else if (count && !nextCount) count.remove();
    else if (!count && nextCount && side) side.prepend(nextCount);
    return current;
  }

  function homeSectionLoading() {
    // aria-busy marks each column while its own read is pending. The route
    // already announced the screen load; one live region per column would
    // announce "Loading" three times.
    return h("div", { class: "agenda-skeleton home-section-loading", "aria-busy": "true" },
      skeletonLine("w-80"), skeletonLine("w-60"), skeletonLine("w-40"));
  }

  function lockedHome(view) {
    const hub = view || {};
    const mailbox = hub.mailbox || {};
    const cockpit = hubCockpit(hub);
    const lockedImportant = homeSection({
      id: "home-locked-important-title",
      kicker: t("hub.important.kicker"),
      title: t("hub.important.title"),
      scrollKey: "home-locked-important",
      accent: "important",
      body: h("div", { class: "locked-note" }, icon("lock"),
        h("div", null,
          h("strong", { text: t("hub.important.locked_title") }),
          h("p", { class: "fine", text: t("hub.important.locked_body") }))),
    });
    const generated = view && view.generated_at ? formatDate(view.generated_at, { year: false }) : "";
    return h("div", { class: "screen home-screen" },
      h("header", { class: "home-head" },
        h("div", { class: "home-head-title" },
          h("h2", { class: "home-head-heading", text: t("hub.brief.title") }),
          generated ? h("span", { class: "home-head-date", text: generated }) : null)),
      h("div", { class: "home-brief-grid home-brief-grid-locked" },
        homeSection({
          id: "home-cockpit-title",
          kicker: t("hub.brief.kicker"),
          title: cockpit.title,
          sub: cockpit.sub,
          scrollKey: "home-cockpit",
          accent: "brief",
          body: cockpit.body,
        }),
        lockedImportant,
        homeSection({
          id: "home-locked-rail-title",
          kicker: t("hub.rail.kicker"),
          title: t("hub.rail.title"),
          scrollKey: "home-locked-rail",
          accent: "rail",
          body: h("div", { class: "rail-section-body" }, hubRailNodes(hub)),
        })));
  }

  function agendaHorizonLabel(hours) {
    if (hours === 24) return t("agenda.horizon.today");
    return t("agenda.horizon.hours", { count: hours });
  }

  function agendaHorizonFocusKey(hours) {
    return "agenda-horizon-" + String(hours);
  }

  function agendaRow(item, options = {}) {
    const kind = item.kind || "event";
    const at = item.at;
    const reasons = Array.isArray(item.reasons) ? item.reasons : [];
    // A reply row shows when the mail arrived, not the moment we decided to
    // surface it: "now" is an action hint, never a mail date.
    const stamp = kind === "reply" && item.last_at ? item.last_at : at;
    const when = h("span", { class: "agenda-when", title: formatDate(stamp), text: formatDate(stamp, { year: false }) });
    const meta = [];
    if (kind === "reply") meta.push(badge(t("agenda.kind.reply"), "accent"));
    if (kind === "reply" && !item.linked_event_title) meta.push(badge(t("agenda.act_now"), "plain"));
    if (kind === "deadline") meta.push(badge(t("agenda.kind.deadline"), "warn"));
    if (item.linked_event_title) meta.push(badge(t("agenda.before_meeting", { title: item.linked_event_title }), "plain"));
    else if (reasons.includes("meeting_with_them")) meta.push(badge(t("agenda.before_meeting_short"), "plain"));
    if (kind === "event" && item.location) meta.push(h("span", { class: "fine", text: item.location }));
    if (item.all_day) meta.push(badge(t("agenda.all_day"), "plain"));
    const body = h("div", { class: "agenda-main" },
      h("div", { class: "agenda-top" },
        h("span", { class: "agenda-title", dir: "auto", text: item.title || t("mail.no_subject") }),
        when),
      item.detail ? h("div", { class: "agenda-detail", dir: "auto", text: item.detail }) : null,
      meta.length ? h("div", { class: "agenda-meta" }, meta) : null);
    const rowClass = "agenda-row" + (options.compact ? " agenda-row-compact" : "");
    if (item.message_id) {
      return h("a", { class: rowClass + " agenda-row-link", href: mailHref(item.message_id, "important") }, body);
    }
    return h("div", { class: rowClass }, body);
  }

  // homeBriefStatNodes reads only the agenda view renderHub already loaded.
  // The counts are honest for the selected horizon and show an em dash while
  // that view is unavailable instead of inventing a zero.
  function homeBriefStatNodes(agenda, error) {
    const ready = !!agenda && !error;
    return [
      ["hub.brief.replies", ready ? agenda.obligations : null],
      ["hub.brief.deadlines", ready ? agenda.deadlines : null],
      ["hub.brief.meetings", ready ? agenda.events : null],
    ].map(([key, value]) => ({
      label: t(key),
      value: value === null ? t("common.dash") : formatNumber(Math.max(0, Number(value) || 0)),
      valueFirst: true,
      className: "home-head-stat",
      title: ready ? "" : t("hub.brief.stat_unavailable"),
    }));
  }

  // homeBriefSignals is a summary of the existing ranked pages, never a new
  // ranking: it takes the first three priority rows, then the agenda, then
  // says so in plain language. It adds no request of its own.
  function homeBriefSignals(priorityView, agendaView, priorityError) {
    const priority = priorityView && Array.isArray(priorityView.items) ? priorityView.items : [];
    if (priority.length) {
      return h("div", { class: "mail-list home-brief-list" },
        priority.slice(0, 3).map((item) => importantRow(item, { compact: true })));
    }
    const agenda = agendaView && Array.isArray(agendaView.items) ? agendaView.items : [];
    if (agenda.length) {
      const rows = h("div", { class: "agenda-list home-brief-list" },
        agenda.slice(0, 3).map((item) => agendaRow(item, { compact: true })));
      // The agenda fallback is still a page, not a successful priority
      // read: when the ranked signal failed, say so beside the rows instead
      // of letting the fallback hide the failure.
      if (!priorityError) return rows;
      return fragment(rows,
        h("p", { class: "fine home-brief-empty", text: t("hub.brief.signals_unavailable") }));
    }
    const message = priorityError ? t("hub.brief.signals_unavailable") : t("hub.brief.signals_empty");
    return h("p", { class: "fine home-brief-empty", text: message });
  }

  // hubGateContent is the hub's label, question and blocker detail. It is
  // content, never permission: gateOpen/gatePreview own the permission answer
  // on every Home column, so a stale hub snapshot cannot grant access.
  function hubGateContent(view) {
    return view && view.gate ? view.gate : {};
  }

  // homeLearningView is the one reading of the hub gate shared by the Brief
  // column and the full learning rail in More. The counts, the top question
  // and the review link have one owner and never disagree between them.
  function homeLearningView(view) {
    const gate = hubGateContent(view);
    const labels = gate.labels || {};
    const questions = Array.isArray(gate.questions) ? gate.questions : [];
    const manual = Math.max(0, Number(labels.manual) || 0);
    const needed = Math.max(0, Number(labels.needed) || 0);
    const review = Math.max(0, Number(labels.review) || 0);
    const top = topNeedQuestion(gate.questions);
    const reviewTarget = questions
      .slice()
      .sort((a, b) => (Number(b.review) || 0) - (Number(a.review) || 0) ||
        questionRemaining(b) - questionRemaining(a) ||
        localeCompare(a.question, b.question))[0] || top;
    const topRow = top ? questions.find((q) => q.question === top.question) : null;
    const reviewHref = routeHash("labels", { review: "1", question: reviewTarget ? reviewTarget.question : "" });
    const certification = gateCertification();
    const certified = certification ? certification.certified : 0;
    const total = certification ? certification.total : 0;
    const labelDone = Math.max(0, Number(gate.labels_done) || 0);
    const labelTotal = Math.max(0, Number(gate.labels_total) || 0);
    return { manual, needed, review, top, topRow, reviewHref, certified, total, labelDone, labelTotal };
  }

  function homeProgressNode(data, id) {
    // The labels stage's own progress is the truth: it counts the judge
    // labels the automatic loop buys, not only the manual ones.
    const done = data.labelTotal ? data.labelDone : data.manual;
    const total = data.labelTotal ? data.labelTotal : data.needed;
    const open = gateOpen();
    return h("div", { class: "home-progress" },
      h("div", { class: "home-progress-top" },
        h("strong", {
          id,
          text: t("hub.learning.progress", {
            manual: formatNumber(done),
            needed: formatNumber(total),
          }),
        }),
        gateKnown() ? gatePill(open) : null),
      h("progress", {
        max: Math.max(total, 1),
        value: Math.min(done, Math.max(total, 1)),
        "aria-label": t("hub.manual.aria", { manual: done, needed: total }),
      }),
      h("p", { class: "fine", text: open
        ? (data.total ? t("hub.open.certified", { certified: data.certified, total: data.total }) : t("hub.open.body"))
        : t("hub.learning.manual_note") }));
  }

  // homeBriefSection is the first column: the ranked signals, then the one
  // next action. It is a section like the other two, with the same head.
  function homeBriefSection(priorityView, agendaView, priorityError, view, loading = false) {
    const priority = priorityView && Array.isArray(priorityView.items) ? priorityView.items : [];
    const agenda = agendaView && Array.isArray(agendaView.items) ? agendaView.items : [];
    // The chip counts the rows the body actually shows, whichever source
    // supplied them (priority first, then the agenda fallback).
    const shown = priority.length ? Math.min(priority.length, 3) : Math.min(agenda.length, 3);
    return homeSection({
      id: "home-brief-title",
      kicker: t("hub.brief.kicker"),
      title: t("hub.brief.signals"),
      sub: t("hub.brief.signals_sub"),
      count: loading ? null : shown || null,
      actions: link(t("hub.important.open_mail"), routeHash("mail", { mode: "important" }), { small: true, kind: "quiet" }),
      scrollKey: "home-brief",
      accent: "brief",
      body: loading ? homeSectionLoading() : h("div", { class: "home-brief-body" },
        homeBriefSignals(priorityView, agendaView, priorityError),
        homeBriefLearning(view)),
    });
  }

  function homeBriefLearning(view) {
    const data = homeLearningView(view);
    const remaining = data.top ? questionRemaining(data.top) : 0;
    const hint = remaining > 0
      ? t("hub.brief.answers_needed", { count: remaining })
      : data.review > 0
        ? t("hub.brief.review_ready", { count: data.review })
        : t("hub.brief.top_question");
    const next = data.top
      ? h("a", { class: "home-brief-next", href: routeHash("labels", { question: data.top.question }) },
        h("span", { class: "home-brief-label", text: t("hub.brief.next_action") }),
        h("span", { class: "home-brief-next-title", text: questionTitle(data.top.question) }),
        h("span", { class: "fine", text: hint }))
      : h("p", { class: "fine", text: t("hub.learning.no_question") });
    return h("div", { class: "home-inset" },
      h("div", { class: "home-inset-head" },
        h("h3", { text: t("hub.learning.title") }),
        link(t("hub.learning.open"), "#/learning", { kind: "quiet", small: true })),
      homeProgressNode(data, "home-learning-progress"),
      next,
      h("div", { class: "home-inset-foot" },
        link(t("hub.learning.review"), data.reviewHref, { kind: "quiet", small: true })));
  }

  // agendaTruncationNotice is the one notice that the agenda list is a cut
  // page while its summary counts the whole result. It sits beside the list
  // in Home and Ask.
  function agendaTruncationNotice(view) {
    if (!view || !view.truncated) return null;
    return h("p", { class: "fine agenda-truncated", text: t("agenda.truncated") });
  }

  function homeAgendaSection(view, error, onHorizon, loading = false) {
    const hours = state.agendaHours;
    const items = view && Array.isArray(view.items) ? view.items : [];
    // The fixed choices plus the configured default when it is another
    // allowed value, so any 1…168 setting stays selectable.
    const horizons = [3, 8, 24];
    if (hours > 0 && !horizons.includes(hours)) horizons.push(hours);
    horizons.sort((a, b) => a - b);
    const chips = horizons.map((h) => {
      const chip = button(agendaHorizonLabel(h), () => {
        if (h === hours || !onHorizon) return;
        onHorizon(h);
      }, { small: true, kind: h === hours ? "primary" : "quiet", disabled: !onHorizon, pressed: h === hours });
      // The stable key lets Home put focus back on the pressed chip after the
      // horizon rebuild replaces the whole agenda section.
      chip.dataset.focusKey = agendaHorizonFocusKey(h);
      return chip;
    });
    let body;
    if (loading) {
      body = h("div", { class: "agenda-skeleton" }, skeletonLine("w-60"), skeletonLine("w-80"), skeletonLine("w-40"));
    } else if (error) {
      body = errorState(error, null, { retry: false });
    } else if (items.length) {
      body = h("div", { class: "agenda-list" }, items.map((item) => agendaRow(item)));
    } else {
      body = emptyState(t("agenda.empty_title"), t("agenda.empty_body"), [], "mail");
    }
    const backlog = view && Number(view.backlog) ? t("agenda.backlog", { count: Number(view.backlog) }) : "";
    const summary = view ? t("agenda.summary", {
      events: Number(view.events) || 0,
      obligations: Number(view.obligations) || 0,
      deadlines: Number(view.deadlines) || 0,
    }) + backlog : "";
    return homeSection({
      id: "home-agenda-title",
      kicker: t("agenda.kicker"),
      title: t("agenda.title", { horizon: agendaHorizonLabel(hours) }),
      sub: summary,
      count: view ? items.length : null,
      actions: h("div", { class: "agenda-horizons", role: "group", "aria-label": t("agenda.horizon_aria") }, chips),
      scrollKey: "home-agenda",
      accent: "agenda",
      body: h("div", { class: "agenda-body" }, body, agendaTruncationNotice(view)),
    });
  }

  function homeImportantSection(view, error, loading = false) {
    const items = view && Array.isArray(view.items) ? view.items.slice(0, PRIORITY_HOME_SIZE) : [];
    const body = loading
      ? homeSectionLoading()
      : error
        ? errorState(error)
        : items.length
          ? h("div", { class: "mail-list compact-list" }, items.map((item) => importantRow(item)))
          : emptyState(t("hub.important.empty_title"), t("hub.important.empty_body"), [
            link(t("hub.learning.review"), routeHash("labels", { review: "1" }), { small: true }),
          ], "mail");
    return homeSection({
      id: "home-important-title",
      kicker: t("hub.important.kicker"),
      title: t("hub.important.title"),
      sub: t("mail.important.sub"),
      count: loading || error ? null : items.length,
      actions: link(t("hub.important.open_mail"), routeHash("mail", { mode: "important" }), { small: true, kind: "quiet" }),
      scrollKey: "home-important",
      accent: "important",
      body,
    });
  }

  function homeRecentSection(askHistoryRead) {
    const host = h("div", { class: "rail-section-body" });
    function retry() {
      loadAskHistory(true).then(
        () => draw(null),
        (error) => draw(error));
    }
    function draw(historyError) {
      clear(host);
      appendChildren(host, homeRecentNodes(historyError, retry));
    }
    // Only a cached history is painted before this read settles: an empty
    // array here would claim there is no recent activity before it is known.
    if (Array.isArray(state.askHistory)) draw(null);
    askHistoryRead.then((read) => {
      // Same rule as the columns: the node may already be cached while the
      // owner is elsewhere; the read that started with it still fills it.
      draw(read.error);
    });
    return homeSection({
      id: "home-more-recent-title",
      title: t("hub.recent.title"),
      flat: true,
      actions: [
        link(t("hub.recent.ask_history"), "#/ask", { kind: "quiet" }),
        link(t("hub.recent.answer_history"), "#/answers", { kind: "quiet" }),
      ],
      body: host,
    });
  }

  function homeLearningSection(view) {
    const data = homeLearningView(view);
    return homeSection({
      id: "home-learning-title",
      title: t("hub.learning.title"),
      flat: true,
      actions: link(t("hub.learning.open"), "#/learning", { small: true, kind: "quiet" }),
      body: h("div", { class: "rail-facts" },
        homeProgressNode(data, "home-learning-rail-progress"),
        h("div", { class: "rail-fact" },
          h("span", { class: "fine", text: t("hub.learning.judge") }),
          h("strong", { text: formatNumber(data.review) })),
        data.top ? h("div", { class: "rail-question" },
          h("span", { class: "fine", text: t("hub.learning.top_question") }),
          h("a", { class: "rail-question-title", href: routeHash("labels", { question: data.top.question }), text: questionTitle(data.top.question) }),
          h("p", { class: "fine", text: t("hub.learning.question_progress", {
            manual: formatNumber(Math.max(0, Number(data.topRow && data.topRow.manual) || 0)),
            needed: formatNumber(Math.max(0, Number(data.topRow && data.topRow.needed) || 0)),
          }) })) : h("p", { class: "fine", text: t("hub.learning.no_question") }),
        h("div", { class: "button-row" },
          link(t("hub.learning.review"), data.reviewHref, { kind: "primary", small: true }))),
    });
  }

  function homeMailboxSection(view) {
    return homeSection({
      id: "home-mailbox-title",
      title: t("hub.rail.mailbox"),
      flat: true,
      body: h("div", { class: "rail-facts" },
        h("p", { class: "fine", text: view && view.mailbox
          ? metaLine([
            t("hub.mailbox.stores", { count: Number(view.mailbox.stores) || 0 }),
            t("hub.mailbox.scope", { messages: Number(view.mailbox.messages) || 0 }),
          ])
          : t("hub.mailbox.none") }),
        h("div", { class: "button-row" }, link(t("nav.sources"), "#/sources", { small: true }))),
    });
  }

  function homeRecentNodes(historyError, retry) {
    const questions = uniqueByQuestion(Array.isArray(state.askHistory) ? state.askHistory : []).slice(0, 3);
    const labels = state.hub && Array.isArray(state.hub.recent_answers) ? state.hub.recent_answers.slice(0, 3) : [];
    if (!questions.length && !labels.length && !historyError) {
      return [h("p", { class: "fine", text: t("hub.recent.empty") })];
    }
    const out = [];
    if (historyError) {
      // The rail keeps whatever labels it already has; only the failed
      // questions read is reported, and it is never "no recent activity".
      out.push(errorState(historyError, retry,
        Object.assign({}, INLINE_READ_ERROR, { title: t("hub.recent.error") })));
    }
    if (questions.length) {
      out.push(h("h3", { class: "rail-subhead", text: t("hub.recent.questions") }));
      out.push(h("div", { class: "recent-list" }, questions.map(({ item, count }) => h("a", {
        class: "recent-row",
        href: routeHash("ask", { id: String(item.id || "") }),
      },
      h("span", { class: "recent-question", dir: "auto", text: item.question }),
      h("span", { class: "recent-meta", text: metaLine([
        t("hub.recent.question_meta", {
          mode: askModeLabel(item.mode),
          time: formatRelative(item.created_at),
        }),
        count > 1 ? t("ask.history.repeat", { count }) : "",
      ]) })))));
    }
    if (labels.length) {
      out.push(h("h3", { class: "rail-subhead", text: t("hub.recent.labels") }));
      out.push(h("div", { class: "recent-list" }, labels.slice(0, 2).map((answer) => h("a", {
        class: "recent-row",
        href: routeHash("answers", { message: String(answer.message_id), question: answer.question }),
      },
      h("span", { class: "recent-question", lang: "en", text: answer.question }),
      h("span", { class: "recent-value", text: answerValueText(answerQuestion(answer), answer.value) }),
      h("span", { class: "recent-meta", text: formatRelative(answer.at) })))));
    }
    return out;
  }

  function importantRowClass(item) {
    const reasons = Array.isArray(item.reasons) ? item.reasons : [];
    for (const code of ["security", "urgent", "deadline", "reply", "decision"]) {
      if (reasons.includes(code)) return reasonKind(code);
    }
    return "";
  }

  // threadBadge reads the action classification first: it is the one truth
  // for who must move. A history row in an awaiting_owner thread owes
  // nothing and must not claim the owner is being waited on. Without an
  // action (an older payload or a fixture) the thread state is the fallback.
  function threadBadge(item) {
    const action = item && item.action && typeof item.action === "object" ? item.action : null;
    const waiting = action ? String(action.waiting || "") : "";
    switch (waiting) {
      case "owner": return badge(t("thread.waiting_you"), threadStateKind("awaiting_owner"));
      case "other": return badge(t("thread.waiting_them"), threadStateKind("awaiting_other"));
      case "unknown": return badge(t("thread.unknown"), threadStateKind("unknown"));
      case "none":
        if (item.thread_state === "answered_by_other") {
          return badge(t("thread.answered_other", { name: item.thread_actor || t("thread.colleague") }),
            threadStateKind("answered_by_other"));
        }
        return null;
    }
    switch (threadStateKey(item.thread_state)) {
      case "awaiting_owner": return badge(t("thread.waiting_you"), threadStateKind("awaiting_owner"));
      case "awaiting_other": return badge(t("thread.waiting_them"), threadStateKind("awaiting_other"));
      case "answered_by_other":
        return badge(t("thread.answered_other", { name: item.thread_actor || t("thread.colleague") }),
          threadStateKind("answered_by_other"));
      case "answered_by_owner": return null;
      case "unknown": return badge(t("thread.unknown"), threadStateKind("unknown"));
      default: return null;
    }
  }

  function importantRow(item, options = {}) {
    const reasons = Array.isArray(item.reasons) ? item.reasons.slice(0, options.compact ? 1 : 3) : [];
    const sender = item.sender_name || item.sender_address || t("mail.sender_unknown");
    const subject = item && item.signals && item.signals.subject ? item.signals.subject : {};
    const path = subject.topic ? subjectPathLabel(subject.topic, subject.subtopic) : "";
    const subjectText = item.subject || t("mail.no_subject");
    return mailRow({
      tag: "a",
      className: ("mail-row mail-row-important " + importantRowClass(item) + (options.compact ? " mail-row-compact" : "") + (options.selected ? " selected" : "")).trim(),
      href: mailHref(item.message_id, "important", options.filter ? { filter: options.filter } : {}),
      dataset: { messageId: String(item.message_id) },
      ariaLabel: t("mail.open_message_aria", { sender, subject: subjectText }),
      avatar: initials(sender),
      sender,
      timeTitle: formatDate(item.received_at),
      timeText: formatRelative(item.received_at),
      subject: subjectText,
      subjectTitle: subjectText,
      path: options.compact ? "" : path,
      snippet: options.compact ? undefined : (item.snippet || ""),
      why: [rowActionBadge(item), threadBadge(item), reasons.map(reasonBadge),
        item.preview ? previewPill() : null,
        item.has_attachments ? badge(t("detail.attachments")) : null],
      onClick: options.onSelect,
    });
  }

  // hubCockpit is the locked Home's first column: the gate state as one
  // section. It returns the title, sub and body so the shared homeSection
  // renders it exactly like every other column.
  function hubCockpit(view) {
    const gate = hubGateContent(view);
    const labels = gate.labels || {};
    const mailbox = view.mailbox || {};
    const manual = Math.max(0, Number(labels.manual) || 0);
    const needed = Math.max(0, Number(labels.needed) || 0);
    const judge = Math.max(0, Number(labels.judge) || 0);
    const stores = Math.max(0, Number(mailbox.stores) || 0);
    const messages = Math.max(0, Number(mailbox.messages) || 0);

    if (!stores) {
      return {
        title: t("hub.mailbox.empty_title"),
        sub: t("hub.mailbox.empty_body"),
        body: h("div", { class: "button-row" },
          link(t("hub.open_sources"), "#/sources", { kind: "primary", small: true }),
          link(t("common.open_learning"), "#/learning", { small: true })),
      };
    }
    if (!messages) {
      return {
        title: t("hub.mailbox.no_mail_title"),
        sub: t("hub.mailbox.no_mail_body"),
        body: h("div", { class: "button-row" },
          link(t("hub.open_sources"), "#/sources", { kind: "primary", small: true }),
          link(t("common.open_learning"), "#/learning", { small: true })),
      };
    }
    if (gateOpen()) {
      const certification = gateCertification();
      const certified = certification ? certification.certified : 0;
      const total = certification ? certification.total : 0;
      return {
        title: t("hub.open.title"),
        sub: total ? t("hub.open.certified", { certified, total }) : t("hub.open.body"),
        body: h("div", { class: "button-row" },
          link(t("learning.open_mail"), "#/mail", { kind: "primary", small: true })),
      };
    }
    if (needed === 0) {
      return {
        title: t("hub.met.title"),
        sub: t("hub.met.body"),
        body: h("div", { class: "button-row" },
          link(t("common.open_learning"), "#/learning", { kind: "primary", small: true }),
          link(t("hub.answer_questions"), "#/labels", { small: true })),
      };
    }

    const top = topNeedQuestion(gate.questions);
    const questionName = top ? top.question : "";
    const reviewHref = routeHash("labels", { review: "1", question: questionName });
    const answerHref = routeHash("labels", { question: questionName });
    const meter = h("div", { class: "meter hub-meter" },
      h("div", { class: "meter-top" },
        h("span", { text: t("hub.manual.label") }),
        h("strong", { text: formatNumber(manual) + " / " + formatNumber(needed) })),
      h("progress", {
        max: Math.max(needed, 1),
        value: Math.min(manual, Math.max(needed, 1)),
        "aria-label": t("hub.manual.aria", { manual, needed }),
      }),
      h("p", { class: "fine", text: t("hub.judge", { count: judge }) }),
      h("p", { class: "fine", text: t("hub.judge_note") }));

    const remaining = (Array.isArray(gate.questions) ? gate.questions : [])
      .filter((q) => questionRemaining(q) > 0)
      .sort((a, b) => questionRemaining(b) - questionRemaining(a) ||
        (Number(b.review) || 0) - (Number(a.review) || 0) ||
        localeCompare(a.question, b.question))
      .slice(0, 3);
    const questions = remaining.length
      ? h("div", { class: "hub-questions" },
          h("h3", { text: t("hub.questions.title") }),
          h("p", { class: "fine", text: t("hub.questions.sub") }),
          remaining.map((q) => h("a", {
            class: "hub-question",
            href: routeHash("labels", { review: "1", question: q.question }),
          },
          h("span", { class: "hub-question-name", text: questionTitle(q.question) }),
          h("span", { class: "hub-question-meta", text: t("hub.question.manual", { manual: Number(q.manual) || 0, needed: Number(q.needed) || 0 }) }),
          h("span", { class: "hub-question-meta", text: t("answers.meta.judge", { count: Number(q.judge) || 0 }) }),
          h("span", { class: "hub-question-action", text: t("hub.review_short") }))))
      : null;

    return {
      title: t("hub.closed.title"),
      sub: t("hub.closed.body"),
      body: h("div", { class: "home-cockpit-body" },
        meter,
        h("div", { class: "button-row hub-actions" },
          link(t("hub.review_suggestions"), reviewHref, { kind: "primary", small: true }),
          link(t("hub.answer_questions"), answerHref, { small: true })),
        questions),
    };
  }

  function hubRailNodes(view) {
    const gate = hubGateContent(view);
    const mailbox = view.mailbox || {};
    const messages = Math.max(0, Number(mailbox.messages) || 0);
    const stores = Math.max(0, Number(mailbox.stores) || 0);
    const blockers = Array.isArray(gate.blockers) ? gate.blockers : [];
    const blocker = blockers.length ? blockers[0] : null;
    const top = topNeedQuestion(gate.questions);
    const reviewHref = routeHash("labels", { review: "1", question: top ? top.question : "" });
    const open = gateOpen();
    const preview = gatePreview();
    const showPreview = preview && !open;

    const learning = h("section", { class: "hub-rail-section" },
      h("h3", { text: t("hub.rail.learning") }),
      h("div", { class: "button-row" }, gateKnown() ? gatePill(open) : null, showPreview ? previewPill() : null),
      blocker
        ? h("div", { class: "hub-blocker" },
            h("div", { class: "hub-blocker-meta" },
              h("span", { class: "blocker-code", text: blocker.code || t("blocker.code_fallback") }),
              h("strong", { text: blockerTitle(blocker) }),
              blockerSubjectText(blocker) ? h("span", { class: "blocker-subject", text: blockerSubjectText(blocker) }) : null),
            h("p", { class: "blocker-detail", text: blockerDetail(blocker) }),
            measureNode(blocker.measure))
        : h("p", { class: "fine", text: t("hub.no_blockers") }),
      h("div", { class: "button-row" },
        link(t("common.open_learning"), "#/learning", { small: true }),
        preview
          ? button(t("preview.disable"), (event) => disablePreview(event.currentTarget), { small: true })
          : open ? null : button(t("preview.hub_action"), () => enablePreviewDialog(), { small: true, icon: "alert" })));

    const recent = (Array.isArray(view.recent_answers) ? view.recent_answers : []).slice(0, 4);
    const answers = h("section", { class: "hub-rail-section" },
      h("h3", { text: t("hub.rail.answers") }),
      recent.length
        ? h("div", { class: "hub-recent" }, recent.map((answer) => h("a", {
            class: "hub-recent-row",
            href: routeHash("answers", { message: String(answer.message_id), question: answer.question }),
          },
          h("span", { class: "hub-recent-question", lang: "en", text: answer.question }),
          h("span", { class: "hub-recent-value", text: answerValueText(answerQuestion(answer), answer.value) }),
          h("span", { class: "hub-recent-meta" },
            badge(answerSourceLabel(answer.source), answer.source === "manual" ? "accent" : "plain"),
            h("span", { class: "fine", text: formatRelative(answer.at) })))))
        : h("div", { class: "hub-empty" },
            h("p", { class: "fine", text: t("hub.no_answers") }),
            h("p", { class: "fine", text: t("hub.no_answers_body") }),
            link(t("hub.review_suggestions"), reviewHref, { small: true })));

    const synced = !!mailbox.synced;
    const syncing = !!mailbox.running;
    const freshness = [];
    if (mailbox.last_success) freshness.push(t("hub.mailbox.last_sync", { when: formatRelative(mailbox.last_success) }));
    if (mailbox.newest_message) freshness.push(t("hub.mailbox.newest", { when: formatRelative(mailbox.newest_message) }));
    const mailboxSection = h("section", { class: "hub-rail-section" },
      h("h3", { text: t("hub.rail.mailbox") }),
      stores
        ? h("p", { class: "hub-mailbox-line" },
            messages ? h("span", { text: t("hub.mailbox.scope", { messages }) }) : h("span", { text: t("hub.mailbox.no_mail") }),
            h("span", { class: "hub-mailbox-sep", "aria-hidden": "true" }),
            h("span", { text: t("hub.mailbox.stores", { count: stores }) }))
        : h("p", { class: "hub-mailbox-line", text: t("hub.mailbox.none") }),
      h("div", { class: "hub-mailbox-state" },
        stores ? pill(syncing ? t("hub.mailbox.syncing") : synced ? t("hub.mailbox.synced") : t("hub.mailbox.not_synced"),
          synced && !syncing ? "done" : "pending", "no-dot") : null,
        link(t("nav.sources"), "#/sources", { small: true })),
      freshness.length ? h("p", { class: "fine hub-mailbox-freshness", text: metaLine(freshness) }) : null);

    return [learning, answers, mailboxSection];
  }
