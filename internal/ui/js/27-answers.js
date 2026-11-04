  // ---------------------------------------------------------------- answers

  function answersListPath(question, source, cursor) {
    const query = new URLSearchParams();
    if (question) query.set("question", question);
    if (source) query.set("source", source);
    if (cursor) query.set("cursor", String(cursor));
    query.set("limit", String(ANSWER_PAGE_SIZE));
    return "/api/answers?" + query.toString();
  }

  function answerDomId(name) {
    return "answer-q-" + String(name).replace(/[^A-Za-z0-9_-]/g, "-");
  }

  function answerQuestionLabel(name) {
    return h("div", { class: "answer-question" },
      h("span", { class: "answer-question-title", text: questionTitle(name) }),
      h("code", { class: "answer-question-key", lang: "en", text: name }));
  }

  async function renderAnswers(route, seq) {
    const questionFilter = route.params.get("question") || "";
    const rawSource = route.params.get("source") || "";
    const sourceFilter = rawSource === "manual" || rawSource === "judge" ? rawSource : "";
    const messageId = Math.max(0, Number(route.params.get("message")) || 0);
    const detailQuestion = questionFilter;
    const [summaryRead, questionsView, listRead] = await Promise.all([
      readLabelSummary(),
      loadQuestions(),
      settleRead(requestCached(answersListPath(questionFilter, sourceFilter, 0), {}, CACHE_TTL.answers)),
    ]);
    if (seq !== state.renderSeq) return h("div");
    const summary = summaryRead.value || {};
    const summaryError = summaryRead.error;
    const questions = Array.isArray(questionsView.questions) ? questionsView.questions : [];
    const manual = Number(summary.manual) || 0;
    const judge = Number(summary.judge) || 0;
    const review = Number(summary.review) || 0;

    setTopbarMeta(
      pill(t("answers.meta.manual", { count: manual }), "pending"),
      pill(t("answers.meta.judge", { count: judge }), "running"),
      review ? pill(t("labels.queue.to_review", { count: review }), "running") : null);

    const top = topNeedQuestion(summary.questions);
    const reviewQuestion = Array.isArray(summary.questions)
      ? summary.questions.filter((row) => Number(row.review) > 0).sort((a, b) => Number(b.review) - Number(a.review))[0]
      : null;
    const reviewTarget = questionFilter || (reviewQuestion ? reviewQuestion.question : top ? top.question : "");
    const reviewHref = routeHash("labels", { review: "1", question: reviewTarget });

    const known = questions.some((q) => q.name === questionFilter);
    const questionOptions = [h("option", { value: "", text: t("answers.filter.all_questions"), selected: questionFilter === "" })];
    for (const q of questions) {
      questionOptions.push(h("option", { value: q.name, text: questionTitle(q.name), selected: q.name === questionFilter }));
    }
    if (questionFilter && !known) {
      questionOptions.push(h("option", { value: questionFilter, text: questionTitle(questionFilter), selected: true }));
    }
    const questionSelect = h("select", {
      class: "select compact",
      "aria-label": t("answers.filter.question_aria"),
      onchange: (event) => navigate("answers", { question: event.currentTarget.value, source: sourceFilter }),
    }, questionOptions);

    const sources = h("nav", { class: "segmented", "aria-label": t("answers.source_aria") },
      tabButton(t("answers.source.all"), sourceFilter === "", () => navigate("answers", { question: questionFilter }), "answers-source-all"),
      tabButton(t("answers.source.you"), sourceFilter === "manual", () => navigate("answers", { question: questionFilter, source: "manual" }), "answers-source-manual"),
      tabButton(t("answers.source.judge"), sourceFilter === "judge", () => navigate("answers", { question: questionFilter, source: "judge" }), "answers-source-judge"));
    const filters = h("div", { class: "toolbar answers-filters" },
      questionSelect,
      sources);

    const header = h("header", { class: "answers-head" },
      h("div", { class: "answers-head-copy" },
        h("h2", { text: t("answers.header.title") }),
        h("p", { class: "lede", text: t("answers.header.sub") }),
        summaryError ? labelSummaryUnavailable(summaryError) : null),
      h("div", { class: "button-row" },
        link(t("answers.review_button"), reviewHref, { kind: "primary", small: true }),
        gateAllows() ? link(t("learning.open_mail"), "#/mail", { small: true, kind: "quiet" }) : null));

    const page = listRead.value;
    const listError = listRead.error;
    const items = page && Array.isArray(page.items) ? page.items : [];
    const pageState = {
      screen: "answers",
      question: questionFilter,
      source: sourceFilter,
      items: items.slice(),
      nextCursor: Number(page && page.next_cursor) || 0,
      hasMore: !!(page && page.has_more),
      loading: false,
      host: null,
    };
    state.answersPage = pageState;

    pageState.table = pageState.items.length
      ? createAnswersTable(pageState.items, messageId, detailQuestion, sourceFilter)
      : null;
    const pager = pagerNode({
      className: "pager-plain",
      note: t("answers.sort_note"),
      onMore: () => loadMoreAnswers(pageState, pageState.table, pager),
    });
    pagerPaint(pager, {
      showing: t("answers.shown", { count: pageState.items.length }),
      end: pageState.items.length && !pageState.hasMore ? t("answers.end") : "",
      error: pageState.error ? apiFailure(pageState.error).message : "",
      more: pageState.hasMore,
    });
    const list = listError
      ? errorState(listError, () => refreshActiveRoute(), {
          title: t("answers.error.list_title"), body: t("answers.error.list"),
          heading: "h3",
        })
      : pageState.items.length
        ? h("div", null, pageState.table.node, pager)
        : h("div", null,
            emptyState(t("answers.empty.title"), t("answers.empty.body"), [link(t("answers.review_button"), reviewHref, { small: true })], "answers"),
            h("p", { class: "fine", text: t("answers.shown", { count: 0 }) }));

    // The detail is one host inside the same screen. Opening a row is a
    // detail change, not a new Answers screen: the router keeps this node,
    // its page model and the list scroll, then patches only this host.
    const detailHost = h("div", { class: "answers-detail-host", hidden: true });
    const screen = h("div", { class: "screen answers-screen" },
      header,
      section(t("answers.filters.title"), t("answers.filters.sub"), null, filters),
      h("div", { class: "answers-body route-scroll section-scroll", dataset: { scrollKey: "answers-body" } },
        section(t("answers.list.title"), t("answers.list.sub"), null, list),
        detailHost));
    pageState.host = screen;
    pagingToken(screen, pageState);
    // A deep link reveals its highlighted group; a row click patches this
    // same host without moving the list the owner was reading.
    if (messageId) syncAnswersDetail(detailHost, messageId, detailQuestion, seq);
    return screen;
  }

  // syncAnswersDetail patches the mounted Answers detail host. The table,
  // its accumulated pages, its scroll and the focused row all stay in place.
  // Closing restores the list offset the detail pushed away from, so Back
  // returns the owner to the exact row they opened.
  function syncAnswersDetail(host, messageId, requestedQuestion, seq, options = {}) {
    if (!host) return;
    const id = Math.max(0, Number(messageId) || 0);
    const current = Math.max(0, Number(host.dataset.messageId) || 0);
    if (id === current) return;
    if (!id) {
      // A pending detail read must not paint into a pane the owner closed.
      state.answerDetailSeq += 1;
      clear(host);
      host.removeAttribute("data-message-id");
      host.hidden = true;
      restoreAnswersListScroll(host);
      syncAnswersListSelection("");
      return;
    }
    if (!current) rememberAnswersListScroll(host);
    host.dataset.messageId = String(id);
    host.hidden = false;
    clear(host);
    host.append(answersDetailSection(id, requestedQuestion, seq, options));
    syncAnswersListSelection(location.hash || "");
  }

  function rememberAnswersListScroll(host) {
    const body = host.closest(".answers-body");
    if (body) host.__answersListScroll = body.scrollTop;
  }

  function restoreAnswersListScroll(host) {
    const body = host.closest(".answers-body");
    const stored = host.__answersListScroll;
    host.__answersListScroll = null;
    if (body && Number.isFinite(stored)) body.scrollTop = Math.max(0, stored);
  }

  // syncAnswersListSelection mirrors the selected row without rebuilding the
  // table. A row link points at the exact hash the router just applied.
  function syncAnswersListSelection(hash) {
    for (const row of document.querySelectorAll(".answers-table tbody tr")) {
      const open = row.querySelector('a[href^="#/answers"]');
      row.classList.toggle("selected", !!hash && !!open && open.getAttribute("href") === hash);
    }
  }

  function createAnswersTable(items, selectedId, requestedQuestion, sourceFilter) {
    return sortableTable({
      caption: t("answers.list.caption"),
      wrapperClass: "answers-table-wrap",
      className: "answers-table",
      columns: [
        {
          key: "message",
          label: t("answers.table.message"),
          type: "number",
          value: (item) => Number(item.message_id) || 0,
          render: (item) => h("span", { class: "mono nowrap", text: "#" + formatId(item.message_id) }),
        },
        {
          key: "question",
          label: t("answers.table.question"),
          type: "text",
          value: (item) => questionTitle(item.question),
          render: (item) => answerQuestionLabel(item.question),
        },
        {
          key: "value",
          label: t("answers.table.value"),
          type: "text",
          value: (item) => answerValueText(answerQuestion(item), item.value),
          render: (item) => h("span", {
            class: "answer-value",
            text: answerValueText(answerQuestion(item), item.value),
          }),
        },
        {
          key: "source",
          label: t("answers.table.source"),
          type: "text",
          value: (item) => answerSourceLabel(item.source),
          render: (item) => badge(answerSourceLabel(item.source), item.source === "manual" ? "accent" : "plain"),
        },
        {
          key: "at",
          label: t("answers.table.when"),
          type: "date",
          defaultDirection: "desc",
          value: (item) => item.at,
          render: (item) => h("span", {
            class: "answer-when",
            title: formatDate(item.at),
            text: formatRelative(item.at),
          }),
        },
        {
          key: "open",
          label: t("answers.table.open"),
          type: "text",
          sortable: false,
          render: (item) => {
            const question = answerQuestion(item);
            const href = routeHash("answers", {
              message: String(item.message_id),
              question: item.question,
              source: sourceFilter,
            });
            return link(t("answers.table.open"), href, {
              small: true,
              aria: t("answers.open_aria", { id: formatId(item.message_id), question: questionTitle(item.question) }),
            });
          },
        },
      ],
      rows: items,
      rowClass: (item) => Number(item.message_id) === selectedId &&
        (!requestedQuestion || item.question === requestedQuestion) ? "selected" : "",
      persistKey: "answers.history",
    });
  }

  function appendAnswerRows(table, items) {
    if (table) table.append(items);
  }

  async function loadMoreAnswers(view, table, pager) {
    if (view.loading || !view.hasMore || !view.nextCursor) return;
    if (!pagingLive(view.host, view)) return;
    view.loading = true;
    view.error = null;
    try {
      await withBusy(pagerMoreButton(pager), t("common.loading"), async () => {
        const page = await request(answersListPath(view.question, view.source, view.nextCursor));
        // The render sequence belongs to the render. A cached remount keeps
        // this page (and its rows) alive under a new sequence, so only the
        // token on the mounted node can tell Load more it is still current.
        if (state.answersPage !== view || !pagingLive(view.host, view)) return;
        const items = Array.isArray(page.items) ? page.items : [];
        view.items.push(...items);
        view.nextCursor = Number(page.next_cursor) || 0;
        view.hasMore = !!page.has_more;
        appendAnswerRows(table, items);
        pagerPaint(pager, {
          showing: t("answers.shown", { count: view.items.length }),
          end: view.hasMore ? "" : t("answers.end"),
          error: "",
          more: view.hasMore,
        });
      });
    } catch (error) {
      view.error = error;
      pagerPaint(pager, { error: apiFailure(error).message });
      reportActionError(error);
    } finally {
      view.loading = false;
    }
  }

  function answersDetailSection(messageId, requestedQuestion, seq, options = {}) {
    const body = h("div", { class: "answers-detail", "aria-busy": "true" });
    loadAnswerDetail(messageId, requestedQuestion, body, seq, options);
    return section(t("mail.message_number", { id: formatId(messageId) }), t("answers.detail.sub"), null, body);
  }

  async function loadAnswerDetail(id, requestedQuestion, panel, seq, options = {}) {
    const detailSeq = ++state.answerDetailSeq;
    clear(panel);
    panel.setAttribute("aria-busy", "true");
    panel.append(loadingState(t("detail.loading", { id: formatId(id) })));
    const [messageResult, answersResult] = await Promise.allSettled([
      loadMessage(id, { forAnswer: true }),
      requestCached("/api/answers?message=" + encodeURIComponent(id) + "&limit=100", {}, CACHE_TTL.answers),
    ]);
    if (seq !== state.renderSeq || detailSeq !== state.answerDetailSeq) return;
    clear(panel);
    panel.setAttribute("aria-busy", "false");

    if (messageResult.status === "rejected") {
      const error = messageResult.reason;
      if (ignorableBackground(error)) return;
      if (removedMessageError(error)) {
        panel.append(unavailableState(t("detail.unavailable")));
        return;
      }
      panel.append(errorState(error, () => loadAnswerDetail(id, requestedQuestion, panel, seq, options),
        { title: t("answers.detail.load_error"), icon: "mail" }));
      return;
    }

    const view = messageResult.value;
    if (!validMessageView(view)) {
      panel.append(errorState(new ApiError(0, { code: "invalid_response" }),
        () => loadAnswerDetail(id, requestedQuestion, panel, seq, options),
        { title: t("answers.detail.load_error"), icon: "mail" }));
      return;
    }
    panel.append(messageCardNode(view, view.message));

    const answers = answersResult.status === "fulfilled" && Array.isArray(answersResult.value.items)
      ? answersResult.value.items : null;
    const firstQuestion = answers && answers.length ? answers[0].question : "";
    const focusQuestion = requestedQuestion || firstQuestion;
    const actions = h("div", { class: "button-row answers-detail-actions" },
      focusQuestion ? link(t("answers.detail.review_in_labels"), routeHash("labels", { review: "1", question: focusQuestion }), {
        small: true,
        aria: t("answers.detail.review_aria", { question: questionTitle(focusQuestion) }),
      }) : null,
      link(t("answers.detail.read_in_labels"), routeHash("labels", { question: focusQuestion }), { small: true }));
    panel.append(actions);

    if (answers === null) {
      const error = answersResult.reason;
      if (ignorableBackground(error)) return;
      panel.append(retryBanner(apiFailure(error).message, () => loadAnswerDetail(id, requestedQuestion, panel, seq, options)));
      return;
    }
    if (!answers.length) {
      panel.append(h("p", { class: "fine", text: t("answers.detail.no_answers") }));
      return;
    }
    panel.append(answerGroupsNode(answers, requestedQuestion));
    if (requestedQuestion && options.reveal !== false) {
      window.requestAnimationFrame(() => {
        if (!panel.isConnected) return;
        const target = panel.querySelector(".answer-group.highlight");
        if (target) target.scrollIntoView({ block: "start" });
      });
    }
  }

  function answerGroupsNode(answers, requestedQuestion) {
    const groups = new Map();
    for (const answer of answers) {
      if (!groups.has(answer.question)) groups.set(answer.question, []);
      groups.get(answer.question).push(answer);
    }
    const profile = state.questions && Array.isArray(state.questions.questions) ? state.questions.questions : [];
    const names = profile.map((q) => q.name).filter((name) => groups.has(name));
    for (const name of groups.keys()) {
      if (!names.includes(name)) names.push(name);
    }
    return h("div", { class: "answer-groups" }, names.map((name) => {
      const list = groups.get(name);
      const question = findQuestion(name);
      const highlighted = name === requestedQuestion;
      const headingId = answerDomId(name) + "-head";
      return h("section", {
        class: "answer-group" + (highlighted ? " highlight" : ""),
        id: answerDomId(name),
        "aria-labelledby": headingId,
      },
      h("div", { class: "answer-group-head" },
        h("h3", { id: headingId, text: questionTitle(name) }),
        h("code", { class: "answer-question-key", lang: "en", text: name }),
        question ? badge(questionTypeLabel(question.type), "plain") : null,
        badge(t("answers.detail.group_count", { count: list.length }), "plain"),
        highlighted ? badge(t("answers.detail.highlight"), "accent") : null,
        h("span", { class: "spacer" }),
        link(t("answers.detail.review_group"), routeHash("labels", { review: "1", question: name }), {
          small: true,
          aria: t("answers.detail.review_aria", { question: questionTitle(name) }),
        })),
      h("div", { class: "answer-label-list" }, list.map((answer) => h("div", { class: "answer-label-row" },
        badge(answerSourceLabel(answer.source), answer.source === "manual" ? "accent" : "plain"),
        h("span", { class: "answer-label-value", text: answerValueText(answerQuestion(answer), answer.value) }),
        h("span", { class: "answer-label-time", title: formatDate(answer.at), text: formatRelative(answer.at) })))));
    }));
  }

