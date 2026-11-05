  // ---------------------------------------------------------------- brief
  //
  // The Brief is the mailbox's current work: what must be handled, what is
  // waiting, what changed, which organizations are active, and which topics
  // the priority mail suggests. The screen shell paints synchronously and
  // each section fills in place, so opening it never waits on a serial read.

  // brief-view:start
  // The subject and action labels are shared by the Brief, the mail row and
  // the detail screen. They live inside this marked block because the Node
  // behavior harness extracts it as one standalone module.
  function subjectPathLabel(topic, subtopic) {
    const parent = topic ? tOr("labels.option.topic." + topic, humanCode(topic)) : "";
    if (!subtopic) return parent;
    const child = tOr("subject." + topic + "." + subtopic, humanCode(subtopic));
    return parent ? parent + " › " + child : child;
  }

  function actionPrimaryLabel(primary) {
    const key = "action.primary." + String(primary || "");
    return tOr(key, "");
  }

  function actionBadge(action) {
    const primary = typeof action === "string" ? action
      : action && typeof action === "object" ? String(action.primary || "") : "";
    const label = actionPrimaryLabel(primary);
    if (!label || primary === "read") return null;
    const kind = primary === "verify" || primary === "pay" ? "warn"
      : primary === "wait" ? "plain" : "accent";
    return badge(label, kind);
  }

  // rowActionBadge keeps the compact mail row from repeating the thread
  // badge: reply and wait are already the thread state. A decision, payment,
  // send, schedule, do or verification is the additional step the row would
  // otherwise hide.
  function rowActionBadge(item) {
    const action = item && item.action;
    const primary = action && typeof action === "object" ? String(action.primary || "") : "";
    if (primary === "reply" || primary === "wait") return null;
    return actionBadge(action);
  }

  function briefSectionItems(section) {
    return section && Array.isArray(section.items) ? section.items : [];
  }

  function briefCount(value) {
    const number = Number(value);
    return Number.isFinite(number) && number > 0 ? Math.floor(number) : 0;
  }

  function briefSectionCount(section) {
    if (!section) return 0;
    if (section.count !== undefined && section.count !== null) {
      const count = Number(section.count);
      if (Number.isFinite(count) && count >= 0) return Math.floor(count);
    }
    return briefSectionItems(section).length;
  }

  // briefPayloadValid is the one payload contract of the Brief: every section
  // key is present. A response without them is broken, not an empty mailbox,
  // so the screen shows the failed read instead of five empty sentences.
  function briefPayloadValid(view) {
    if (!view || typeof view !== "object") return false;
    return ["must", "waiting", "changed", "relationships", "topics"]
      .every((key) => view[key] !== undefined);
  }

  function briefTitle(item) {
    const source = item && typeof item === "object" ? item : {};
    const value = String(source.title || source.subject || "").trim();
    return value || t("mail.no_subject");
  }

  function briefOrganizationLabel(organization) {
    if (!organization || typeof organization !== "object") return "";
    return String(organization.label || "").trim();
  }

  function briefOrganizationNode(organization) {
    const label = briefOrganizationLabel(organization);
    if (!label) return null;
    const key = String(organization.key || "").trim();
    if (!key) return h("span", { class: "brief-org", dir: "auto", text: label });
    return h("a", {
      class: "brief-org",
      href: routeHash("companies", { key }),
      "aria-label": t("brief.org_open_aria", { label }),
      dir: "auto",
      text: label,
    });
  }

  function briefOrganizationTextNode(organization) {
    const label = briefOrganizationLabel(organization);
    return label ? h("span", { class: "brief-org", dir: "auto", text: label }) : null;
  }

  function briefKindLabel(kind) {
    return tOr("brief.kind." + String(kind || "").toLowerCase(), t("brief.kind.other"));
  }

  function briefDirectionLabel(direction) {
    return String(direction || "").toLowerCase() === "owner"
      ? t("brief.direction.owner")
      : t("brief.direction.other");
  }

  function briefDueText(value) {
    if (!value) return "";
    const label = formatDate(value, { year: false });
    return label === t("common.dash") ? "" : label;
  }

  function briefStatusNode(item) {
    const source = item && typeof item === "object" ? item : {};
    const status = String(source.status || "").toLowerCase();
    const due = briefDueText(source.due);
    if (status === "overdue") {
      return badge(
        due ? t("brief.status.overdue_due", { date: due }) : t("brief.status.overdue"),
        "danger",
        source.due ? formatDate(source.due) : "");
    }
    if (due) return badge(t("mail.conversations.due", { date: due }), "warn", formatDate(source.due));
    if (status === "open") return badge(t("brief.status.open"), "plain");
    return null;
  }

  function briefEvidenceNode(item, subject) {
    const source = item && typeof item === "object" ? item : {};
    const raw = Array.isArray(source.evidence) ? source.evidence : [];
    const ids = [];
    for (const value of raw) {
      const id = Number(value);
      if (!Number.isFinite(id) || id <= 0 || ids.includes(id)) continue;
      ids.push(id);
    }
    const shown = ids.slice(0, 3);
    if (!shown.length) return null;
    return h("span", { class: "brief-evidence" },
      h("span", { class: "brief-evidence-label", text: t("brief.evidence") }),
      shown.map((id, index) => h("a", {
        class: "citation-chip",
        href: mailHref(id, "important"),
        "aria-label": t("brief.evidence_aria", { index: index + 1, subject }),
        title: t("brief.evidence_aria", { index: index + 1, subject }),
        text: String(index + 1),
      })));
  }

  function briefItemRow(item, section) {
    const source = item && typeof item === "object" ? item : {};
    const subject = briefTitle(source);
    const id = Number(source.message_id) || 0;
    const titleNode = id > 0
      ? h("a", {
        class: "brief-row-title",
        href: mailHref(id, "important"),
        "aria-label": t("brief.open_aria", { subject }),
        dir: "auto",
        text: subject,
      })
      : h("span", { class: "brief-row-title", dir: "auto", text: subject });
    const waiting = section === "waiting";
    return h("div", { class: "brief-row" },
      h("div", { class: "brief-row-main" },
        titleNode,
        h("div", { class: "brief-row-meta" },
          badge(briefKindLabel(source.kind), "plain"),
          actionBadge(source.action),
          source.topic ? h("span", { class: "brief-subject-path", dir: "auto", text: subjectPathLabel(source.topic, source.subtopic) }) : null,
          briefOrganizationNode(source.organization),
          source.person ? h("span", { class: "brief-person", dir: "auto", text: String(source.person) }) : null,
          waiting ? h("span", { class: "brief-direction", text: briefDirectionLabel(source.direction) }) : null)),
      h("div", { class: "brief-row-side" },
        briefStatusNode(source),
        briefEvidenceNode(source, subject)));
  }

  function briefChangedRow(item) {
    const source = item && typeof item === "object" ? item : {};
    const subject = briefTitle(source);
    const id = Number(source.message_id) || 0;
    const content = [
      h("div", { class: "brief-row-main" },
        h("div", { class: "brief-changed-top" },
          badge(threadStateLabel(source.state), threadStateKind(source.state)),
          h("span", { class: "brief-row-title", dir: "auto", text: subject })),
        h("div", { class: "brief-row-meta" },
          briefOrganizationTextNode(source.organization),
          source.person ? h("span", { class: "brief-person", dir: "auto", text: String(source.person) }) : null)),
      source.last_at ? h("div", { class: "brief-row-side" },
        h("span", {
          class: "brief-when",
          title: formatDate(source.last_at),
          text: conversationWhen(source.last_at),
        })) : null,
    ];
    if (id > 0) {
      return h("a", {
        class: "brief-row brief-changed-row",
        href: mailHref(id, "important"),
        "aria-label": t("brief.open_aria", { subject }),
      }, content);
    }
    return h("div", { class: "brief-row brief-changed-row" }, content);
  }

  // briefRelationshipRow renders one organization's open loops: the loop
  // total and the overdue and stored follow-up partitions the commitments
  // rollup sends. The digest's conversation and message counts are no longer
  // on this wire, so the row states only what the producer reports.
  function briefRelationshipRow(item) {
    const source = item && typeof item === "object" ? item : {};
    const organization = source.organization && typeof source.organization === "object" ? source.organization : {};
    const label = briefOrganizationLabel(organization) || String(source.title || "").trim() || t("brief.organization_unknown");
    const key = String(organization.key || "").trim();
    const openLoops = briefCount(source.count);
    const overdue = briefCount(source.overdue);
    const storedDue = briefCount(source.stored_due);
    const content = [
      h("div", { class: "brief-row-main" },
        h("span", { class: "brief-rel-title", dir: "auto", text: label }),
        h("div", { class: "brief-rel-stats" },
          h("span", { text: t("brief.relationships.open_loops", { count: openLoops }) }),
          overdue
            ? badge(t("brief.relationships.overdue", { count: overdue }), "danger")
            : null,
          storedDue
            ? badge(t("brief.relationships.stored_due", { count: storedDue }), "plain")
            : null)),
    ];
    if (key) {
      return h("a", {
        class: "brief-row brief-rel-row",
        href: routeHash("companies", { key }),
        "aria-label": t("brief.org_open_aria", { label }),
      }, content);
    }
    return h("div", { class: "brief-row brief-rel-row" }, content);
  }

  function briefTopicLabel(item) {
    const source = item && typeof item === "object" ? item : {};
    const key = String(source.title || "").trim();
    if (source.kind === "topic" && key) return tOr("labels.option.topic." + key, humanCode(key));
    if (source.kind === "subtopic" && source.topic && source.subtopic) {
      return tOr("subject." + source.topic + "." + source.subtopic, humanCode(source.subtopic));
    }
    return briefTitle(source);
  }

  function briefTopicNode(item) {
    const source = item && typeof item === "object" ? item : {};
    const parent = h("div", { class: "brief-topic" },
      h("span", { class: "brief-topic-name", dir: "auto", text: briefTopicLabel(source) }),
      h("span", { class: "brief-topic-count", text: t("detail.thread.message_count", { count: briefCount(source.count) }) }));
    const subs = Array.isArray(source.subtopics) ? source.subtopics : [];
    if (!subs.length) return parent;
    return h("div", { class: "brief-topic-group" }, parent,
      h("div", { class: "brief-subtopics" }, subs.map((sub) => {
        const child = sub && typeof sub === "object" ? sub : {};
        return h("div", { class: "brief-subtopic", title: briefTopicLabel(child) },
          h("span", { class: "brief-subtopic-name", dir: "auto", text: briefTopicLabel(child) }),
          h("span", { class: "brief-subtopic-count", text: formatNumber(briefCount(child.count)) }));
      })));
  }

  function briefTopicScopeLabel(scope) {
    const code = String(scope || "").trim();
    return tOr("brief.topics.scope." + code, t("brief.topics.scope_generic"));
  }

  function briefTopicSubLabel(section) {
    const scope = briefTopicScopeLabel(section && section.scope);
    const total = briefCount(section && section.total);
    if (!total) return scope;
    return metaLine([scope, t("brief.topics.coverage", {
      classified: formatNumber(briefCount(section && section.count)),
      total: formatNumber(total),
    })]);
  }

  function briefSectionBodyNode(kind, section) {
    const items = briefSectionItems(section);
    if (kind === "topics") {
      return items.length ? h("div", { class: "brief-topics" }, items.map(briefTopicNode)) : null;
    }
    if (!items.length) return h("p", { class: "brief-empty", text: t("brief.empty." + kind) });
    if (kind === "changed") return h("div", { class: "brief-rows" }, items.map(briefChangedRow));
    if (kind === "relationships") return h("div", { class: "brief-rows" }, items.map(briefRelationshipRow));
    return h("div", { class: "brief-rows" }, items.map((item) => briefItemRow(item, kind)));
  }

  function briefStatValues(view) {
    const known = !!view;
    return [
      { target: "brief-must", label: t("brief.stat.must"), count: briefSectionCount(view && view.must), known },
      { target: "brief-waiting", label: t("brief.stat.waiting"), count: briefSectionCount(view && view.waiting), known },
      { target: "brief-changed", label: t("brief.stat.changed"), count: briefSectionCount(view && view.changed), known },
      { target: "brief-relationships", label: t("brief.stat.relationships"), count: briefSectionCount(view && view.relationships), known },
    ];
  }

  function briefStatsNode(view, jump) {
    return statStrip(briefStatValues(view).map((stat) => ({
      label: stat.label,
      value: stat.known ? formatNumber(stat.count) : t("common.dash"),
      valueFirst: true,
      className: "brief-stat",
      href: "#" + stat.target,
      ariaLabel: t("brief.stat_aria", {
        label: stat.label,
        count: stat.known ? formatNumber(stat.count) : t("common.dash"),
      }),
      onclick: (event) => jump(event, stat.target),
    })), { className: "brief-stats head-stats", role: "group", ariaLabel: t("brief.stats_aria") });
  }
  // brief-view:end

  let briefSinceMemo = "";

  function briefSinceValid(value) {
    const since = String(value || "").trim();
    if (!since || !BRIEF_SINCE_RE.test(since)) return "";
    const parsed = Date.parse(since);
    if (!Number.isFinite(parsed) || parsed > Date.now()) return "";
    return since;
  }

  // The window is persisted only after "Mark seen". Until then the server
  // owns the default last-seven-days boundary, so the startup warm and the
  // first screen share one cache key instead of a client timestamp.
  function briefSinceValue() {
    if (briefSinceMemo) return briefSinceMemo;
    let stored = "";
    try { stored = localStorage.getItem(BRIEF_SINCE_KEY) || ""; } catch { /* storage may be unavailable */ }
    briefSinceMemo = briefSinceValid(stored);
    return briefSinceMemo;
  }

  function setBriefSince(value) {
    const normalized = briefSinceValid(value);
    if (!normalized) return false;
    briefSinceMemo = normalized;
    try { localStorage.setItem(BRIEF_SINCE_KEY, normalized); } catch { /* keep it in memory */ }
    return true;
  }

  function briefPath(since) {
    const value = String(since || "").trim();
    return value ? "/api/brief?since=" + encodeURIComponent(value) : "/api/brief";
  }

  function loadBrief(since) {
    return requestCached(briefPath(since || briefSinceValue()), {}, CACHE_TTL.brief);
  }

  function briefSectionNode(id, kind, extraClass, actions) {
    const countNode = h("span", { class: "pill plain no-dot brief-count", hidden: true });
    const subNode = h("p", { class: "section-sub", hidden: true });
    const bodyNode = h("div", { class: "brief-body section-scroll", dataset: { scrollKey: id } });
    const root = h("section", {
      id,
      class: ("surface brief-section " + (extraClass || "")).trim(),
      "aria-labelledby": id + "-title",
    },
    h("div", { class: "brief-section-head" },
      h("div", null,
        h("h2", { id: id + "-title", text: t("brief.section." + kind + ".title") }),
        subNode),
      h("div", { class: "section-actions" }, actions, countNode)),
    bodyNode);
    return { root, body: bodyNode, sub: subNode, count: countNode };
  }

  // briefErrorBlock is the failed read of the one brief grid. The shared
  // error state supplies the icon/copy/action hierarchy; the surface and the
  // alert role are the brief's presentation.
  function briefErrorBlock(error) {
    return h("div", { class: "surface brief-error", role: "alert" },
      errorState(error, () => refreshActiveRoute(), {
        title: t("brief.error.title"),
        icon: "alert",
        wrap: false,
      }));
  }

  // briefGateGuard is the one gate decision of the Brief: an unknown gate
  // is an error with a retry, a closed gate is the learning blockers, and an
  // allowed gate returns null so the screen can paint.
  function briefGateGuard() {
    if (!gateKnown()) {
      return errorState(null, () => loadGate().then(() => refreshActiveRoute()), {
        title: t("brief.gate_unknown_title"),
        body: t("brief.gate_unknown_body"),
        icon: "alert",
      });
    }
    if (!gateAllows()) return lockedScreen(state.learning || {}, "", t("brief.locked_title"));
    return null;
  }

  function renderBrief(route, seq) {
    const gateNode = briefGateGuard();
    if (gateNode) return gateNode;
    const since = briefSinceValue();
    const warm = cached(briefPath(since));
    // A route can outlive its data TTL. Paint the last Brief for the same
    // window while the read revalidates instead of flashing a skeleton.
    const known = warm || cachedStale(briefPath(since));
    let current = known || null;
    let loading = false;
    // Stale content paints at once; the screen must say it revalidates.
    const revalidating = !!known && !warm;

    const generatedNode = h("span", { class: "brief-generated" });
    const statusNode = h("span", { class: "sr-only", role: "status", text: revalidating ? t("brief.updating") : t("brief.loading") });
    const statsHost = h("div", { class: "brief-stats-host" });
    const seenButton = button(t("brief.mark_seen"), () => markBriefSeen(), { small: true, kind: "quiet", icon: "check" });
    seenButton.setAttribute("aria-label", t("brief.mark_seen_aria"));
    seenButton.hidden = true;
    seenButton.disabled = true;
    const suggestedPill = pill(t("brief.topics.suggested"), "accent");
    suggestedPill.hidden = true;

    const must = briefSectionNode("brief-must", "must", "brief-section-must", null);
    const waiting = briefSectionNode("brief-waiting", "waiting", "", null);
    const changed = briefSectionNode("brief-changed", "changed", "", [seenButton]);
    const relationships = briefSectionNode("brief-relationships", "relationships", "", null);
    const topics = briefSectionNode("brief-topics", "topics", "", [suggestedPill]);
    const grid = h("div", { class: "brief-grid" },
      must.root, waiting.root, changed.root, relationships.root, topics.root);
    const head = h("header", { class: "brief-head" },
      h("div", { class: "brief-head-copy" },
        h("h2", { class: "brief-heading", text: t("brief.heading") }),
        generatedNode,
        statusNode),
      statsHost);
    const screen = h("div", { class: "screen brief-screen" }, head, grid);

    function jump(event, id) {
      event.preventDefault();
      const target = document.getElementById(id);
      if (!target) return;
      target.scrollIntoView({ block: "start" });
      target.setAttribute("tabindex", "-1");
      target.focus({ preventScroll: true });
    }

    async function markBriefSeen() {
      const generated = current && current.generated_at;
      if (!generated || !setBriefSince(generated)) {
        toast(t("brief.mark_seen_failed"), "warn");
        return;
      }
      seenButton.disabled = true;
      invalidateCache(["/api/brief"]);
      invalidateRouteCache("brief");
      // The window is stored locally before the repaint; say so, then let
      // the refreshed Brief show the new boundary.
      toast(t("brief.mark_seen_done"), "info");
      await refreshActiveRoute();
    }

    function paintSection(target, kind, section) {
      const count = briefSectionCount(section);
      const body = briefSectionBodyNode(kind, section);
      if (kind === "topics" && !body) {
        target.root.hidden = true;
        return;
      }
      target.root.hidden = false;
      target.count.hidden = false;
      target.count.textContent = formatNumber(count);
      target.count.setAttribute("aria-label", t("brief.section.count_aria", {
        section: t("brief.section." + kind + ".title"),
        count,
      }));
      clear(target.body);
      target.body.append(body);
      if (kind === "topics") {
        target.sub.hidden = false;
        target.sub.textContent = briefTopicSubLabel(section);
      }
    }

    function paint(view) {
      current = view;
      const generated = view && view.generated_at ? String(view.generated_at) : "";
      const generatedLabel = generated ? formatDate(generated) : "";
      const generatedKnown = !!generatedLabel && generatedLabel !== t("common.dash");
      generatedNode.textContent = generatedKnown
        ? t("brief.generated", { when: generatedLabel })
        : t("brief.generated_unknown");
      generatedNode.title = generatedKnown ? generated : "";
      clear(statsHost);
      statsHost.append(briefStatsNode(view, jump));
      must.root.hidden = false;
      waiting.root.hidden = false;
      changed.root.hidden = false;
      relationships.root.hidden = false;
      paintSection(must, "must", view.must);
      paintSection(waiting, "waiting", view.waiting);
      paintSection(changed, "changed", view.changed);
      paintSection(relationships, "relationships", view.relationships);
      paintSection(topics, "topics", view.topics);
      suggestedPill.hidden = !(view.topics && (view.topics.suggested ||
        briefSectionItems(view.topics).some((item) => item && item.suggested)));
      const changedSince = view.changed && view.changed.since ? formatDate(view.changed.since) : "";
      changed.sub.hidden = false;
      changed.sub.textContent = changedSince && changedSince !== t("common.dash")
        ? t("brief.section.changed.sub", { when: changedSince })
        : t("brief.section.changed.sub_window");
      seenButton.disabled = !generatedKnown;
      seenButton.hidden = false;
      statusNode.textContent = "";
      screen.removeAttribute("aria-busy");
    }

    function paintSkeleton() {
      screen.setAttribute("aria-busy", "true");
      clear(statsHost);
      statsHost.append(briefStatsNode(null, jump));
      generatedNode.textContent = t("brief.loading");
      for (const target of [must, waiting, changed, relationships]) {
        target.root.hidden = false;
        target.sub.hidden = true;
        target.count.hidden = true;
        clear(target.body);
        target.body.append(h("div", { class: "brief-skeleton" },
          h("div", { class: "brief-skeleton-row" }, skeletonLine("w-40"), skeletonLine("w-80")),
          h("div", { class: "brief-skeleton-row" }, skeletonLine("w-60"), skeletonLine("w-40"))));
      }
      topics.root.hidden = true;
      suggestedPill.hidden = true;
      seenButton.disabled = true;
      seenButton.hidden = true;
      statusNode.textContent = t("brief.loading");
    }

    function paintError(error) {
      generatedNode.textContent = t("brief.generated_unknown");
      clear(statsHost);
      statsHost.append(briefStatsNode(null, jump));
      seenButton.disabled = true;
      seenButton.hidden = true;
      clear(grid);
      grid.append(briefErrorBlock(error));
      statusNode.textContent = "";
      screen.removeAttribute("aria-busy");
    }

    function load() {
      if (loading) return;
      loading = true;
      loadBrief(since).then((view) => {
        loading = false;
        if (!state.token || state.shuttingDown) return;
        if (briefPayloadValid(view)) paint(view);
        else paintError(new ApiError(0, { code: "invalid_response" }));
      }, (error) => {
        loading = false;
        if (!state.token || state.shuttingDown) return;
        if (error instanceof ApiError && error.code === "learning_locked") {
          // The gate closed after boot. Refresh the gate state, then let the
          // route rebuild its locked screen instead of a generic failure.
          loadLearning(true).then(() => {
            invalidateRouteCache("brief");
            if (state.token && !state.shuttingDown && state.route === "brief") refreshActiveRoute();
          }, (gateError) => {
            if (state.token && !state.shuttingDown) paintError(gateError);
          });
          return;
        }
        paintError(error);
      });
    }

    if (known) paint(known);
    else paintSkeleton();
    if (!warm) load();
    return screen;
  }

