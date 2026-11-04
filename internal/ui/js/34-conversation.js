  // ---------------------------------------------------------------- conversation
  //
  // The conversation card is a compact read of the same thread truth the
  // priority screen and agenda use. It carries only owner-visible metadata
  // and collapsed snippets; shared/delegate bodies never reach this view.
  // The Node behavior test extracts the marked block, so keep its helpers
  // free of application state.

  // thread-view:start
  function threadPresentation(thread, currentId, earlierLimit = 3) {
    const source = thread && typeof thread === "object" ? thread : {};
    const messages = Array.isArray(source.messages) ? source.messages : [];
    const current = Number(currentId) || 0;
    const state = source.state && typeof source.state === "object" ? source.state : {};
    const rawState = String(state.state || "");
    const stateKey = threadStateKey(rawState);
    const visibility = String(state.visibility || "");
    const visibilityNote = visibility === "partial" || String(state.reason || "") === "visibility_gap";

    const names = threadParticipantNames(source.participants);

    const allEarlier = messages
      .filter((message) => message && Number(message.id) !== current)
      .map((message) => ({
        id: Number(message.id) || 0,
        sender: message.sender_name || message.sender_address || t("mail.sender_unknown"),
        subject: message.subject || "",
        snippet: String(message.snippet || ""),
        receivedAt: message.received_at || "",
        sent: !!message.sent,
      }))
      .filter((message) => message.id > 0);
    const earlier = earlierLimit > 0 ? allEarlier.slice(-earlierLimit) : allEarlier;

    const count = Math.max(0, Number(source.count) || messages.length || 0);
    return {
      conversationID: String(source.conversation_id || ""),
      stateLabel: threadStateLabel(stateKey),
      stateKind: threadStateKind(stateKey),
      topic: String(state.topic || ""),
      subtopic: String(state.subtopic || ""),
      visibilityNote,
      names: names.slice(0, 3),
      morePeople: Math.max(0, names.length - 3),
      count,
      countLabel: t("detail.thread.message_count", { count }),
      lastAt: source.last_at || (messages.length ? messages[messages.length - 1].received_at : ""),
      earlier,
      earlierHidden: Math.max(0, allEarlier.length - earlier.length),
      truncated: !!source.truncated,
      oldestID: messages.length ? Number(messages[0].id) || 0 : 0,
      newer: Math.max(0, Number(source.newer) || 0),
      newestID: Number(source.newest_message_id) || 0,
    };
  }

  function mergeThreadPage(thread, page) {
    const current = thread && typeof thread === "object" ? thread : {};
    const existing = Array.isArray(current.messages) ? current.messages : [];
    const older = page && Array.isArray(page.messages) ? page.messages : [];
    const known = new Set(existing.map((message) => Number(message.id)));
    const added = older.filter((message) => message && !known.has(Number(message.id)));
    const messages = added.concat(existing);
    return Object.assign({}, current, {
      messages,
      count: Math.max(Number(current.count) || 0, Number(page && page.count) || 0, messages.length),
      first_at: (page && page.first_at) || current.first_at || "",
      participants: page && Array.isArray(page.participants) && page.participants.length
        ? page.participants
        : current.participants,
      // An empty page cannot make progress; stop offering the button even if
      // the server still reports a truncated history.
      truncated: added.length ? !!(page && page.truncated) : false,
    });
  }

  function threadHostNode(message) {
    if (!message || !message.conversation_id) return null;
    return h("div", { class: "thread-host", dataset: { conversationId: String(message.conversation_id) } },
      h("div", { class: "thread-skeleton", "aria-busy": "true", "aria-label": t("detail.thread.loading") },
        skeletonLine("w-40")));
  }

  function threadSectionNode(thread, currentId, context = {}) {
    if (!thread || !Array.isArray(thread.messages) || !thread.messages.length) return null;
    let current = thread;
    const section = h("section", { class: "thread-section", "aria-label": t("detail.thread.title") });
    const view = threadPresentation(current, currentId);

    const head = h("div", { class: "thread-head" },
      h("h3", { class: "thread-title", text: t("detail.thread.title") }),
      view.topic ? h("span", { class: "thread-subject-path", dir: "auto", text: subjectPathLabel(view.topic, view.subtopic) }) : null,
      badge(view.stateLabel, view.stateKind));

    const participantParts = [];
    if (view.names.length) {
      const more = view.morePeople ? " " + t("detail.thread.more_people", { count: view.morePeople }) : "";
      participantParts.push(h("span", { class: "thread-people", dir: "auto", text: view.names.join(", ") + more }));
    }
    participantParts.push(h("span", { class: "thread-meta", text: view.countLabel }));
    if (view.lastAt) {
      participantParts.push(h("span", {
        class: "thread-meta",
        title: formatDate(view.lastAt),
        text: t("detail.thread.last_activity", { when: formatRelative(view.lastAt) }),
      }));
    }
    const participants = h("div", { class: "thread-participants", "aria-label": t("detail.thread.participants") },
      participantParts);

    const earlierHost = h("div", { class: "thread-earlier" });
    const errorLine = h("p", { class: "thread-error fine", hidden: true, text: t("detail.thread.error") });
    let loading = false;
    let expanded = false;

    function earlierRow(message) {
      const marker = message.sent ? t("detail.thread.sent") : t("detail.thread.received");
      return h("button", {
        type: "button",
        class: "thread-message-row" + (message.sent ? " sent" : ""),
        "aria-label": t("mail.open_message_aria", {
          sender: message.sender,
          subject: message.subject || t("mail.no_subject"),
        }),
        title: formatDate(message.receivedAt),
        dataset: { messageId: String(message.id) },
        onclick: () => selectThreadMessage(message.id, context),
      },
      h("span", { class: "thread-message-sender", dir: "auto", text: message.sender }),
      h("span", { class: "thread-message-snippet", dir: "auto", text: message.snippet || message.subject }),
      h("span", { class: "thread-message-marker", text: marker }),
      h("span", { class: "thread-message-date", text: formatRelative(message.receivedAt) }));
    }

    async function loadOlder() {
      if (loading) return;
      const oldest = threadPresentation(current, currentId).oldestID;
      if (!oldest || !current.conversation_id) return;
      loading = true;
      errorLine.hidden = true;
      renderEarlier();
      try {
        const page = await loadThread(current.conversation_id, oldest);
        current = mergeThreadPage(current, page);
        expanded = true;
        loading = false;
        renderEarlier();
      } catch {
        loading = false;
        errorLine.hidden = false;
        renderEarlier();
      }
    }

    function renderEarlier() {
      clear(earlierHost);
      const model = threadPresentation(current, currentId, expanded ? 0 : 3);
      if (model.earlier.length) {
        earlierHost.append(h("div", { class: "thread-message-list", "aria-label": t("detail.thread.earlier_title") },
          model.earlier.map(earlierRow)));
      } else if (model.count > 1) {
        earlierHost.append(h("p", { class: "thread-empty fine", text: t("detail.thread.earlier_empty") }));
      }
      if (model.truncated && model.oldestID) {
        earlierHost.append(h("div", { class: "thread-more" },
          h("span", { class: "fine", text: t("detail.thread.truncated") }),
          button(t("detail.thread.load_older"), loadOlder, { small: true, kind: "quiet", disabled: loading })));
      }
    }

    section.append(head, participants);
    if (view.visibilityNote) {
      section.append(h("p", { class: "thread-visibility", text: t("detail.thread.visibility_partial") }));
    }
    if (view.newer > 0) {
      section.append(h("div", { class: "thread-newer" },
        h("span", { class: "fine", text: t("detail.thread.newer", { count: view.newer }) }),
        view.newestID
          ? button(t("detail.thread.show_latest"), () => selectThreadMessage(view.newestID, context), { small: true, kind: "quiet" })
          : null));
    }
    section.append(earlierHost, errorLine);
    renderEarlier();
    return section;
  }

  async function fillThreadSection(host, conversationID, currentId, context = {}) {
    if (!host) return;
    let thread = null;
    try {
      // Anchor the first page at the opened message: in a long conversation
      // the latest page may contain only newer mail, which must never be
      // labelled "Earlier".
      thread = await loadThread(conversationID, 0, Number(currentId) || 0);
    } catch {
      // A missing or unavailable conversation is not an error toast: it
      // simply has no section in this message.
      host.remove();
      return;
    }
    if (!host.isConnected) return;
    const source = thread && typeof thread === "object" ? thread : {};
    let section = null;
    try {
      section = threadSectionNode(Object.assign({}, source, {
        conversation_id: source.conversation_id || conversationID,
      }), currentId, context);
    } catch {
      section = null;
    }
    if (section) host.replaceWith(section);
    else host.remove();
  }

  function hydrateMailThread(root, id, message, context = {}) {
    if (!root || !message || !message.conversation_id) return;
    const host = root.querySelector(".thread-host");
    if (!host) return;
    return fillThreadSection(host, message.conversation_id, id, context).catch(() => {});
  }
  // thread-view:end

  function mailDetailNode(id, messageView, triage, triageError, context, questions) {
    const message = validMessageView(messageView) ? messageView.message : null;
    const parts = [];
    if (message) {
      const displayedFolder = message.folder_path || message.folder_role;
      parts.push(messageCardNode(messageView, message));
      // messageCardNode already carries the message's own flags (partial,
      // truncated body or HTML, attachments). Repeating them here showed every
      // flag twice, with a second wording.
      parts.push(h("div", { class: "chips detail-flags" },
        actionBadge(context.item && context.item.action),
        message.folder_role && message.folder_role !== displayedFolder ? badge(allMailRoleLabel(message.folder_role)) : null,
        messageView.verdict ? badge(t("detail.badge.scope", { value: scopeVerdictLabel(messageView.verdict) })) : null));
      parts.push(threadHostNode(message));
    } else {
      // A detail without a message read is not a triage result: say the
      // message is gone instead of painting a title over an empty body.
      parts.push(unavailableState(t("detail.unavailable")));
    }

    const provenanceFlags = [];
    if (context.mode === "recent") provenanceFlags.push(routePill(context.routed, !!context.preview, gateCertification()));
    else if (context.preview) provenanceFlags.push(previewPill());
    if (triage && triage.decided_at) provenanceFlags.push(badge(t("detail.decided", { date: formatDate(triage.decided_at) })));
    if (triage && triage.stale) provenanceFlags.push(badge(t("detail.stale"), "warn"));
    if (triage && triage.escalate) provenanceFlags.push(badge(t("detail.escalated"), "warn"));
    if (triage && triage.failure) provenanceFlags.push(badge(t("detail.failed"), "danger", triage.failure));
    parts.push(h("div", { class: "detail-provenance" },
      h("span", { class: "fine", text: t("detail.source_layer") }),
      provenanceFlags,
      context.preview ? h("p", { class: "fine", text: t("mail.detail.preview_note") }) : null));

    if (triageError) {
      const value = triageError instanceof ApiError && triageError.code === "not_triaged"
        ? t("detail.not_triaged")
        : t("detail.triage_error", { message: apiFailure(triageError).message });
      parts.push(warningBanner(text(value)));
    } else if (triage) {
      if (triage.failure) {
        parts.push(warningBanner(h("div", null,
          h("strong", { text: t("detail.failure_title") }),
          h("p", { lang: "en", text: triage.failure }))));
      }
      if (triage.uncertain && triage.uncertain.length) {
        parts.push(warningBanner(text(t("detail.summary.uncertain_list", { questions: triageQuestionNames(triage.uncertain) }) + (triage.escalate ? t("detail.escalated_suffix") : ""))));
      }
      parts.push(h("section", { class: "detail-answers", "aria-labelledby": "detail-answers-title" },
        sectionHead(t("detail.summary.title"), { id: "detail-answers-title", level: "h3" }),
        triageSummaryNode(triage, questions),
        triage.missing && triage.missing.length ? h("p", { class: "fine", text: t("detail.summary.missing_list", { questions: triageQuestionNames(triage.missing) }) }) : null));
    }

    const firstQuestion = triage && triage.values ? orderedNames(triage.values, questions || {})[0] : "";
    parts.push(h("div", { class: "detail-actions" },
      link(t("mail.detail.teach"), routeHash("labels", firstQuestion ? { question: firstQuestion } : {}), { small: true }),
      button(t("mail.detail.ask"), () => {
        state.askPrefill = t("ask.prefill_message", { subject: message ? message.subject || t("mail.no_subject") : t("mail.message_number", { id: formatId(id) }) });
        navigate("ask");
      }, { small: true, kind: "quiet" })));
    return h("div", { class: "mail-detail-content" }, parts);
  }

