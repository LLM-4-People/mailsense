  // ---------------------------------------------------------------- conversations
  //
  // The thread-first Mail list reads one digest page at a time. This block is
  // pure: the Node behavior test extracts it, so it must not touch application
  // state. Friendly state labels are the only state wording it ever renders.

  // conversations-view:start
  function conversationFilterState(value) {
    const raw = String(value || "");
    return threadStateValid(raw) ? raw : "";
  }

  // The behavior harness runs this block on its own and calls this name.
  function conversationStateLabel(value) {
    return threadStateLabel(value);
  }

  function conversationQuery(value) {
    return Array.from(String(value == null ? "" : value).trim()).slice(0, 200).join("");
  }

  function conversationViewKey(stateValue, queryValue) {
    return (conversationFilterState(stateValue) || "all") + "|" + conversationQuery(queryValue);
  }

  function conversationPath(limit, before, stateValue, queryValue) {
    const query = ["limit=" + encodeURIComponent(String(limit))];
    if (before) query.push("before=" + encodeURIComponent(String(before)));
    const stateFilter = conversationFilterState(stateValue);
    if (stateFilter) query.push("state=" + encodeURIComponent(stateFilter));
    const q = conversationQuery(queryValue);
    if (q) query.push("q=" + encodeURIComponent(q));
    return "/api/conversations?" + query.join("&");
  }

  function conversationWhen(value) {
    if (!value) return t("mail.conversations.unknown_date");
    const relative = formatRelative(value);
    return relative && relative !== t("common.dash") ? relative : t("mail.conversations.unknown_date");
  }

  function conversationPeople(row) {
    const source = row && typeof row === "object" ? row : {};
    const names = threadParticipantNames(source.participants);
    if (!names.length) {
      const fallback = String(source.sender_name || source.sender_address || "").trim();
      if (fallback) names.push(fallback);
    }
    const total = Math.max(Number(source.participant_count) || 0, names.length);
    const shown = names.slice(0, 2);
    return { shown, more: Math.max(0, total - shown.length) };
  }

  function conversationPeopleLabel(row) {
    const people = conversationPeople(row);
    if (!people.shown.length) return t("mail.sender_unknown");
    return people.shown.join(", ") + (people.more > 0 ? " " + t("detail.thread.more_people", { count: people.more }) : "");
  }

  function conversationRowModel(row) {
    const source = row && typeof row === "object" ? row : {};
    const state = source.state && typeof source.state === "object" ? source.state : {};
    const messages = Math.max(0, Number(source.messages) || 0);
    const sent = Math.max(0, Number(source.sent) || 0);
    const received = Math.max(0, Number(source.received) || 0);
    const unread = Math.max(0, Number(source.unread) || 0);
    const subject = String(source.subject || "").trim() || t("mail.no_subject");
    const organization = source.organization && typeof source.organization === "object"
      ? String(source.organization.label || "").trim()
      : "";
    const lastAt = String(source.last_at || "");
    const absolute = lastAt ? formatDate(lastAt) : "";
    const dueDate = String(source.due_date || "").trim();
    return {
      conversationID: String(source.conversation_id || ""),
      lastMessageID: Number(source.last_message_id) || 0,
      subject,
      organization,
      people: conversationPeopleLabel(source),
      snippet: String(source.snippet || ""),
      subjectPath: state.topic ? subjectPathLabel(state.topic, state.subtopic) : "",
      stateLabel: threadStateLabel(state.state),
      stateKind: threadStateKind(state.state),
      countLabel: t("detail.thread.message_count", { count: messages }),
      sentReceived: t("mail.conversations.sent_received", { sent, received }),
      unread: unread > 0,
      unreadLabel: unread > 0 ? t("mail.conversations.unread", { count: unread }) : "",
      when: conversationWhen(lastAt),
      whenTitle: absolute && absolute !== t("common.dash") ? absolute : t("mail.conversations.unknown_date"),
      hasAttachments: !!source.has_attachments,
      attachmentLabel: t("mail.conversations.attachments"),
      dueDate,
      dueLabel: dueDate ? t("mail.conversations.due", { date: formatDay(dueDate) }) : "",
    };
  }

  function conversationRowNode(row, options = {}) {
    const model = conversationRowModel(row);
    const node = h("button", {
      type: "button",
      class: ("mail-row conversation-row" + (options.selected ? " selected" : "") + (model.unread ? " unread" : "")).trim(),
      dataset: { messageId: String(model.lastMessageID), conversationId: model.conversationID },
      "aria-label": t("mail.conversations.open_aria", { subject: model.subject }),
      title: model.whenTitle,
    },
    h("span", { class: "conversation-row-top" },
      badge(model.stateLabel, model.stateKind),
      model.organization ? h("span", { class: "conversation-org", dir: "auto", title: model.organization, text: model.organization }) : null,
      h("span", { class: "spacer" }),
      h("span", { class: "conversation-when", title: model.whenTitle, text: model.when }),
      model.unread ? h("span", { class: "conversation-unread", role: "img", "aria-label": model.unreadLabel }) : null),
    h("span", { class: "conversation-subject-row" },
      h("span", { class: "conversation-subject", dir: "auto", title: model.subject, text: model.subject }),
      h("span", { class: "conversation-count", text: model.countLabel }),
      model.hasAttachments ? badge(model.attachmentLabel) : null,
      model.dueDate ? badge(model.dueLabel, "warn", model.dueDate) : null),
    h("span", { class: "conversation-row-meta" },
      model.subjectPath ? h("span", { class: "conversation-subject-path", dir: "auto", title: model.subjectPath, text: model.subjectPath }) : null,
      h("span", { class: "conversation-people", dir: "auto", text: model.people }),
      h("span", { class: "conversation-snippet", dir: "auto", text: model.snippet }),
      h("span", { class: "spacer" }),
      h("span", { class: "conversation-traffic", text: model.sentReceived })));
    node.addEventListener("click", (event) => {
      if (event && event.preventDefault) event.preventDefault();
      if (options.onSelect) options.onSelect(row);
    });
    return node;
  }

  function conversationCountValues(counts) {
    const source = counts && typeof counts === "object" ? counts : {};
    const values = {};
    let all = 0;
    for (const key of THREAD_STATES) {
      const value = Math.max(0, Number(source[key]) || 0);
      values[key] = value;
      all += value;
    }
    values.all = all;
    return values;
  }

  function conversationViewFromPage(page, stateValue, queryValue) {
    const source = page && typeof page === "object" ? page : {};
    const nextBefore = String(source.next_before || "");
    const coverage = source.coverage && typeof source.coverage === "object" ? source.coverage : {};
    return {
      state: conversationFilterState(stateValue),
      q: conversationQuery(queryValue),
      rows: Array.isArray(source.conversations) ? source.conversations.slice() : [],
      total: Math.max(0, Number(source.total) || 0),
      counts: conversationCountValues(source.counts),
      nextBefore,
      hasMore: !!source.has_more && !!nextBefore,
      preview: !!source.preview,
      coverage: {
        messages: Math.max(0, Number(coverage.messages) || 0),
        conversations: Math.max(0, Number(coverage.conversations) || 0),
        unthreaded: Math.max(0, Number(coverage.unthreaded) || 0),
      },
    };
  }

  function conversationMergePage(view, page) {
    const next = conversationViewFromPage(page, view ? view.state : "", view ? view.q : "");
    if (!view) return next;
    const rows = Array.isArray(view.rows) ? view.rows.slice() : [];
    const seen = new Set(rows.map((row) => String((row && row.conversation_id) || "")));
    let added = 0;
    for (const row of next.rows) {
      const id = String((row && row.conversation_id) || "");
      if (!id || seen.has(id)) continue;
      seen.add(id);
      rows.push(row);
      added += 1;
    }
    return Object.assign({}, next, {
      rows,
      total: Math.max(next.total, rows.length),
      hasMore: !!next.hasMore && added > 0,
    });
  }

  function conversationCoverageLabel(view) {
    const coverage = view && view.coverage ? view.coverage : {};
    return t("mail.conversations.coverage", {
      conversations: t("mail.conversations.count", { count: Math.max(0, Number(coverage.conversations) || 0) }),
      messages: t("detail.thread.message_count", { count: Math.max(0, Number(coverage.messages) || 0) }),
    });
  }

  function conversationTotalLabel(view) {
    return t("mail.conversations.count", { count: Math.max(0, Number(view && view.total) || 0) });
  }

  function conversationEmptyCopy(filtered) {
    return filtered
      ? { title: t("mail.conversations.no_match_title"), body: t("mail.conversations.no_match_body") }
      : { title: t("mail.conversations.empty_title"), body: t("mail.conversations.empty_body") };
  }

  function conversationErrorCopy(error) {
    const failure = apiFailure(error);
    return {
      title: t("mail.conversations.error_title"),
      body: failure && failure.message ? failure.message : t("mail.conversations.error_body"),
    };
  }
  // conversations-view:end

