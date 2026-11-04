  // ---------------------------------------------------------------- labels

  function afterLabelWrite() {
    // Labels, answers, the Hub's label gate and the learning status advanced.
    // The workbench itself advances locally; a revisit starts from a fresh
    // task, so its cached markup is dropped while Mail, Companies, Home and
    // Brief keep their mounted DOM: a label write does not change them.
    invalidateTopics(["labels"], { hard: true, refresh: false });
    // Refresh the chrome and the next visit in the background. The current
    // task advances locally, so answering never waits on a route rebuild.
    Promise.allSettled([loadLearning(true), loadHub(true)]).then(updateLearningChrome);
  }

  function normalizeLabelSummary(view, questions) {
    const source = view && typeof view === "object" ? view : {};
    const serverRows = Array.isArray(source.questions) ? source.questions : [];
    const byName = new Map();
    for (const row of serverRows) {
      if (row && row.question) byName.set(String(row.question), row);
    }
    const rows = questions.map((q) => {
      const row = byName.get(q.name) || {};
      return {
        question: q.name,
        manual: Math.max(0, Number(row.manual) || 0),
        judge: Math.max(0, Number(row.judge) || 0),
        needed: Math.max(0, Number(row.needed) || 0),
        review: Math.max(0, Number(row.review) || 0),
      };
    });
    const total = (key) => rows.reduce((sum, row) => sum + row[key], 0);
    const number = (value, fallback) => {
      const parsed = Number(value);
      return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
    };
    return {
      manual: number(source.manual, total("manual")),
      judge: number(source.judge, total("judge")),
      needed: number(source.needed, total("needed")),
      review: number(source.review, total("review")),
      questions: rows,
    };
  }

  function labelSummaryRow(summary, name) {
    const rows = summary && Array.isArray(summary.questions) ? summary.questions : [];
    return rows.find((row) => row.question === name) || null;
  }

  function labelQuestionRows(questions, summary) {
    return questions.map((q, order) => {
      const row = labelSummaryRow(summary, q.name) || { question: q.name, manual: 0, judge: 0, needed: 0, review: 0 };
      return {
        q,
        manual: row.manual,
        judge: row.judge,
        needed: row.needed,
        review: row.review,
        remaining: questionRemaining(row),
        order,
      };
    }).sort((a, b) => b.remaining - a.remaining || b.review - a.review || a.order - b.order);
  }

  function labelQueueText(count, review) {
    return review ? t("labels.queue.to_review", { count }) : t("labels.queue.queued", { count });
  }

  // detachLabelsScreen is the one detach of the workspace's document-level
  // keyboard handler and both observers. Leaving the route and rebuilding it
  // both go through here; the screen's attach hook recreates them when the
  // router restores the cached node.
  function detachLabelsScreen() {
    if (state.labelKeyHandler) {
      document.removeEventListener("keydown", state.labelKeyHandler);
      state.labelKeyHandler = null;
    }
    if (state.labelObserver) {
      state.labelObserver.disconnect();
      state.labelObserver = null;
    }
    if (state.labelChipObserver) {
      state.labelChipObserver.disconnect();
      state.labelChipObserver = null;
    }
  }

  // observeLabelChips owns the chip strip's resize observer. A cached restore
  // runs it again after the router has dropped the old observer.
  function observeLabelChips(chipsHost, updateChipScrollState, scrollSelectedChip, force = false) {
    if ((prefetchActive > 0 && !force) || typeof ResizeObserver !== "function" || !chipsHost.isConnected) return;
    if (state.labelChipObserver) {
      state.labelChipObserver.disconnect();
      state.labelChipObserver = null;
    }
    let width = chipsHost.clientWidth;
    state.labelChipObserver = new ResizeObserver(() => {
      updateChipScrollState();
      if (chipsHost.clientWidth !== width) {
        width = chipsHost.clientWidth;
        scrollSelectedChip();
      }
    });
    state.labelChipObserver.observe(chipsHost);
  }

  async function renderLabels(route, seq) {
    if (prefetchActive === 0) detachLabelsScreen();
    // The label summary and the permission refresh are both settled: a gate
    // or summary failure degrades one slot, never the queue and task reads.
    const [questionsView, learning, summaryRead, gateRead] = await Promise.all([
      loadQuestions(),
      loadLearning(),
      readLabelSummary(),
      readGate(),
    ]);
    if (seq !== state.renderSeq) return h("div");
    const questions = Array.isArray(questionsView.questions) ? questionsView.questions : [];
    if (!questions.length) {
      setTopbarMeta(pill(t("labels.no_questions_pill")));
      return h("div", { class: "screen" }, emptyState(t("labels.no_questions_title"), t("labels.no_questions_body"), null, "labels"));
    }

    // One source per render: the hub snapshot when it is current, otherwise
    // the dedicated summary read. They are never merged into one count.
    const rawSummary = summaryRead.value;
    const summaryKnown = !!rawSummary;
    const summary = normalizeLabelSummary(rawSummary, questions);
    const rows = labelQuestionRows(questions, summary);
    const requested = route.params.get("question") || "";
    const review = route.params.get("review") === "1";
    const reviewRows = rows.slice().sort((a, b) => b.review - a.review || b.remaining - a.remaining);
    const defaultRow = review && reviewRows[0] && reviewRows[0].review > 0 ? reviewRows[0] : rows[0];
    const selectedName = requested && questions.some((q) => q.name === requested) ? requested : defaultRow.q.name;
    const question = questions.find((q) => q.name === selectedName) || questions[0];
    const count = route.params.get("count") || "";
    // A gate this render cannot vouch for is unknown, not closed: the pills
    // stay absent while the queue and task keep painting.
    const permissionKnown = !!gateRead.value || gateKnown();
    const learningOpen = permissionKnown && gateOpen();
    // A gate that could not be refreshed this render keeps the shell's last
    // known preview; only a never-read gate suppresses the permission pills.
    const showPreview = permissionKnown && gatePreview();
    const labelsStage = (learning.status && Array.isArray(learning.status.stages) ? learning.status.stages : []).find((stage) => stage.id === "labels");
    const selectedBlocker = stageBlockers(learning, "labels").find((entry) => entry.blocker.subject === selectedName) || null;
    const judgeAction = selectedBlocker ? (selectedBlocker.blocker.actions || []).find((action) => action.id === "run_judge") : null;
    const machineBlocker = stageBlockers(learning, "machine")[0];
    let tasks = [];
    let queueLoaded = false;
    let queueError = null;
    let queueTotal = 0;
    let queueHasMore = false;
    let atEnd = false;
    let currentRow = labelSummaryRow(summary, selectedName) || { question: selectedName, manual: 0, judge: 0, needed: 0, review: 0 };
    let currentQuestion = question;
    let suggestion;
    let saving = false;
    // taskActions is the mounted task's busy owner. Save, Accept, Skip,
    // Previous and Next go through it; the question controls do not.
    let taskActions = null;
    function setTaskBusy(on) {
      if (taskActions) taskActions.set(on);
    }
    // The active task owns the keyboard handler and the action-height
    // observer. The hook survives leaving the route so a cached mount can
    // re-install exactly the task the owner left mounted.
    let activeTaskAttach = null;

    state.labelAnswers = {};
    state.labelExtras = [];

    const queueCountText = () => labelsQueueCountText({ machineBlocker, atEnd, queueError, queueLoaded, queueTotal, shown: tasks.length, review });
    const progressText = (row) => ({
      manual: Math.max(0, Number(row && row.manual) || 0),
      needed: Math.max(0, Number(row && row.needed) || 0),
    });

    function updateTopbar() {
      const value = progressText(currentRow);
      setTopbarMeta(
        pill(questionTitle(currentQuestion.name), "accent"),
        summaryKnown ? pill(t("labels.topbar.progress", { manual: formatNumber(value.manual), needed: formatNumber(value.needed) }), value.manual >= value.needed ? "done" : "pending") : null,
        pill(review ? t("labels.mode.review") : t("labels.mode.label"), review ? "accent" : "pending"));
    }

    const chipsHost = h("nav", {
      class: "labels-question-chips",
      tabIndex: 0,
      "aria-label": t("labels.question.switcher_aria"),
      "aria-keyshortcuts": "ArrowLeft ArrowRight Home End",
      title: t("labels.question.scroll_hint"),
    });
    const chipStep = () => Math.max(180, Math.round(chipsHost.clientWidth * 0.8));
    const chipPrev = iconButton("chevron-left", t("labels.question.scroll_prev"), () => {
      chipsHost.scrollLeft = Math.max(0, chipsHost.scrollLeft - chipStep());
      updateChipScrollState();
    }, { small: true });
    const chipNext = iconButton("chevron-right", t("labels.question.scroll_next"), () => {
      chipsHost.scrollLeft = Math.min(chipsHost.scrollWidth - chipsHost.clientWidth, chipsHost.scrollLeft + chipStep());
      updateChipScrollState();
    }, { small: true });
    chipPrev.classList.add("labels-chips-nav", "prev");
    chipNext.classList.add("labels-chips-nav", "next");
    chipPrev.hidden = true;
    chipNext.hidden = true;
    const chipsWrap = h("div", { class: "labels-chips" }, chipsHost, chipPrev, chipNext);

    function updateChipScrollState() {
      const max = Math.max(0, chipsHost.scrollWidth - chipsHost.clientWidth);
      const overflow = max > 1;
      const hasLeft = chipsHost.scrollLeft > 1;
      const hasRight = overflow && chipsHost.scrollLeft < max - 1;
      chipsWrap.classList.toggle("has-left", hasLeft);
      chipsWrap.classList.toggle("has-right", hasRight);
      chipPrev.hidden = !hasLeft;
      chipNext.hidden = !hasRight;
      chipPrev.disabled = !hasLeft;
      chipNext.disabled = !hasRight;
    }

    chipsHost.addEventListener("scroll", updateChipScrollState, { passive: true });
    chipsHost.addEventListener("keydown", (event) => {
      if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
      const step = Math.max(180, Math.round(chipsHost.clientWidth * 0.8));
      let target = null;
      if (event.key === "ArrowRight") target = chipsHost.scrollLeft + step;
      else if (event.key === "ArrowLeft") target = chipsHost.scrollLeft - step;
      else if (event.key === "Home") target = 0;
      else if (event.key === "End") target = chipsHost.scrollWidth;
      if (target === null) return;
      event.preventDefault();
      event.stopPropagation();
      chipsHost.scrollLeft = Math.max(0, Math.min(chipsHost.scrollWidth - chipsHost.clientWidth, target));
      updateChipScrollState();
    });

    const statusHost = h("div", { class: "labels-status" });
    const workbenchHost = h("div", { class: "labels-workbench-host" });

    function setLabelKeyHandler(handler, force = false) {
      if (prefetchActive > 0 && !force) return;
      if (state.labelKeyHandler) document.removeEventListener("keydown", state.labelKeyHandler);
      state.labelKeyHandler = handler;
      if (handler) document.addEventListener("keydown", handler);
    }

    function stopLabelObserver() {
      if (state.labelObserver) {
        state.labelObserver.disconnect();
        state.labelObserver = null;
      }
    }

    function drawQuestionChips() {
      clear(chipsHost);
      for (const row of (review ? reviewRows : rows)) {
        const selected = row.q.name === selectedName;
        const chip = h("a", {
          class: "labels-qchip" + (selected ? " selected" : ""),
          href: routeHash("labels", { question: row.q.name, count, review: review ? "1" : "" }),
          "aria-current": selected ? "page" : null,
          dataset: { focusKey: "labels-question-" + row.q.name },
          title: questionTitle(row.q.name),
        },
        h("span", { class: "labels-qchip-title", text: questionTitle(row.q.name) }),
        h("span", { class: "labels-qchip-meta" },
          summaryKnown ? h("span", { text: t("labels.question.chip_progress", { manual: formatNumber(row.manual), needed: formatNumber(row.needed) }) }) : null,
          row.review > 0 ? h("span", { class: "labels-qchip-review", text: t("labels.queue.to_review", { count: row.review }) }) : null));
        chipsHost.append(chip);
      }
    }

    function scrollSelectedChip() {
      const selectedChip = chipsHost.querySelector(".labels-qchip.selected");
      if (!selectedChip || !chipsHost.isConnected) {
        updateChipScrollState();
        return;
      }
      // Keep the whole selected chip clear of both edge fades. When it needs
      // to move, align its left edge inside the strip instead of leaving the
      // strip starting in the middle of an earlier chip.
      const inset = 34;
      const max = Math.max(0, chipsHost.scrollWidth - chipsHost.clientWidth);
      const hostRect = chipsHost.getBoundingClientRect();
      const chipRect = selectedChip.getBoundingClientRect();
      const visible = chipRect.left >= hostRect.left + inset && chipRect.right <= hostRect.right - inset;
      if (!visible) {
        let target = chipsHost.scrollLeft + (chipRect.left - hostRect.left) - inset;
        if (target > max) target = chipsHost.scrollLeft + (chipRect.right - (hostRect.right - inset));
        chipsHost.scrollLeft = Math.max(0, Math.min(max, target));
      }
      updateChipScrollState();
    }

    function scheduleChipScroll() {
      window.setTimeout(scrollSelectedChip, 0);
      window.requestAnimationFrame(scrollSelectedChip);
    }

    function drawStatus() {
      clear(statusHost);
      const value = summaryKnown ? progressText(currentRow) : { manual: null, needed: null };
      const max = Math.max(Number(value.needed) || 0, 1);
      const advanced = h("details", { class: "labels-advanced" },
        h("summary", { text: t("labels.advanced.summary") }),
        h("div", { class: "labels-advanced-body" },
          h("p", { class: "fine", text: review ? t("labels.requirement.sub_review") : t("labels.requirement.sub") }),
          labelsStage && labelsStage.state === "done" ? h("p", { class: "fine", text: t("labels.requirement.met") }) : null,
          !selectedBlocker ? h("p", { class: "fine", text: t("labels.requirement.none") }) : null,
          selectedBlocker && selectedBlocker.blocker.measure ? measureNode(selectedBlocker.blocker.measure) : null,
          h("div", { class: "button-row" },
            judgeAction ? button(metaLine([t("labels.run_judge"), judgeAction.params && judgeAction.params.estimated_cost_usd
              ? (judgeAction.params.estimated_cost_usd === "unknown" ? t("labels.cost_unknown") : formatUsd(judgeAction.params.estimated_cost_usd))
              : t("labels.cost_unknown")]), (event) => executeAction(judgeAction, { blocker: selectedBlocker.blocker, stage: "labels" }, event.currentTarget), { small: true, icon: "play" }) : null,
            link(t("common.open_learning"), "#/learning", { small: true, kind: "quiet" })),
          selectedBlocker && selectedBlocker.blocker.detail
            ? diagnosticDetails(selectedBlocker.blocker.detail, { paragraphClass: "fine" }) : null));

      statusHost.append(h("section", { class: "labels-progress surface", "aria-labelledby": "labels-progress-title" },
        h("div", { class: "labels-progress-main" },
          h("div", { class: "labels-progress-head" },
            h("div", null,
              h("h2", { id: "labels-progress-title", text: questionTitle(currentQuestion.name) }),
              h("p", { class: "section-sub", text: questionHint(currentQuestion.name) })),
            h("div", { class: "labels-pills" }, permissionKnown ? gatePill(learningOpen) : null, permissionKnown && showPreview ? previewPill() : null)),
          h("div", { class: "labels-stats" },
            h("div", { class: "labels-stat" },
              h("span", { class: "labels-stat-label", text: t("labels.progress.your") }),
              h("strong", { text: value.manual === null ? t("common.dash") : formatNumber(value.manual) })),
            h("div", { class: "labels-stat" },
              h("span", { class: "labels-stat-label", text: t("labels.progress.needed") }),
              h("strong", { text: value.needed === null ? t("common.dash") : formatNumber(value.needed) })),
            h("span", { class: "labels-queue-count", text: queueCountText() })),
          summaryKnown ? h("progress", {
            max,
            value: Math.min(value.manual, max),
            "aria-label": t("labels.progress.aria", { question: questionTitle(currentQuestion.name) }),
          }) : null),
        advanced));
    }

    function nextQuestionRow() {
      const available = rows.filter((row) => row.q.name !== currentQuestion.name);
      return available.find((row) => row.remaining > 0) || available[0] || rows[0] || null;
    }

    function showMorePage() {
      state.labelTaskSeq += 1;
      activeTaskAttach = null;
      atEnd = false;
      setLabelKeyHandler(null);
      stopLabelObserver();
      clear(workbenchHost);
      const next = nextQuestionRow();
      const nextHref = next ? routeHash("labels", { question: next.q.name, count }) : "#/labels";
      workbenchHost.append(labelsMoreNode(queueTotal, () => loadQueue(true), nextHref));
      drawStatus();
      focusHeading(workbenchHost, "#labels-more-title");
    }

    function showComplete(answered) {
      if (queueHasMore) {
        showMorePage();
        return;
      }
      state.labelTaskSeq += 1;
      activeTaskAttach = null;
      atEnd = !answered;
      setLabelKeyHandler(null);
      stopLabelObserver();
      clear(workbenchHost);
      const next = nextQuestionRow();
      const nextHref = next ? routeHash("labels", { question: next.q.name, count }) : "#/labels";
      workbenchHost.append(labelsCompleteNode(answered, nextHref));
      drawStatus();
      focusHeading(workbenchHost, "#labels-complete-title");
    }

    function showEmptyQueue() {
      state.labelTaskSeq += 1;
      activeTaskAttach = null;
      setLabelKeyHandler(null);
      stopLabelObserver();
      clear(workbenchHost);
      workbenchHost.append(labelsEmptyNode(review,
        () => navigate("labels", { question: selectedName, count }), () => loadQueue()));
    }

    function applySaved(items, hadSuggestion) {
      for (const item of items) {
        const row = labelSummaryRow(summary, item.question);
        if (row) {
          row.manual += 1;
          if (hadSuggestion && item.question === currentQuestion.name) row.review = Math.max(0, row.review - 1);
        }
      }
      summary.manual += items.length;
      if (hadSuggestion) summary.review = Math.max(0, summary.review - 1);
      currentRow = labelSummaryRow(summary, currentQuestion.name) || currentRow;
    }

    // goToTask and skipTask are the only guarded ways to replace the mounted
    // task. showLabelTask stays directly callable for the post-save advance.
    function goToTask(index) {
      if (saving) return;
      if (index < 0 || index >= tasks.length) return;
      showLabelTask(index, { focusHeading: true });
    }

    function skipTask(taskIndex) {
      if (saving) return;
      if (taskIndex + 1 < tasks.length) showLabelTask(taskIndex + 1, { focusHeading: true });
      else showComplete(false);
    }

    async function showLabelTask(index, options = {}) {
      if (index < 0 || index >= tasks.length) return;
      taskActions = null;
      activeTaskAttach = null;
      setLabelKeyHandler(null);
      stopLabelObserver();
      atEnd = false;
      const taskSeq = ++state.labelTaskSeq;
      const task = tasks[index];
      state.labelAnswers = {};
      state.labelExtras = [];
      suggestion = undefined;
      clear(workbenchHost);

      const messagePane = h("section", { class: "labels-message-pane section-scroll", "aria-label": t("labels.message_pane_aria"), dataset: { scrollKey: "labels-message" } },
        loadingState(t("labels.task.loading")));
      const answerScroll = h("div", { class: "labels-answer-scroll section-scroll", tabIndex: 0, role: "region", "aria-label": t("labels.answer_pane_aria"), dataset: { scrollKey: "labels-answer" } });
      const actionBar = h("div", { class: "labels-actions" });
      const prevButton = button(t("labels.task.prev"), () => goToTask(index - 1), { small: true, kind: "quiet" });
      const nextButton = button(t("labels.task.next"), () => goToTask(index + 1), { small: true, kind: "quiet" });
      const taskStrip = h("div", { class: "labels-task-strip" },
        h("div", { class: "labels-task-position" },
          h("span", { "aria-current": "step", text: t("labels.task.position", { index: index + 1, total: tasks.length }) }),
          h("progress", { max: Math.max(tasks.length, 1), value: index + 1, "aria-label": t("labels.task.position_aria", { index: index + 1, total: tasks.length }) })),
        h("div", { class: "labels-task-nav" }, prevButton, nextButton));
      const answerPane = h("section", { class: "labels-answer-pane" }, taskStrip, answerScroll, actionBar);
      workbenchHost.append(h("div", { class: "labels-task-layout" }, messagePane, answerPane));
      prepareScrollRegions(workbenchHost);
      answerScroll.append(loadingState(t("labels.task.loading_answer")));

      let view;
      try {
        view = await loadMessage(task.message_id, { forLabel: true, question: task.question, limit: queueLimit, review });
      } catch (error) {
        if (taskSeq !== state.labelTaskSeq) return;
        clear(workbenchHost);
        workbenchHost.append(errorScreen(error, () => goToTask(index)));
        reportError(error);
        return;
      }
      // A prefetched or refreshed route renders while its node is still
      // detached, so connection is not a cancellation signal: the task must
      // paint into the node the router is about to cache. The task sequence
      // is the one stale-result guard (a newer task or completion bumped it).
      if (taskSeq !== state.labelTaskSeq) return;

      const message = view.message || {};
      clear(messagePane);
      messagePane.append(messageCardNode(view, message));
      if (review && task.suggestion !== undefined && task.suggestion !== null) {
        suggestion = task.suggestion;
        state.labelAnswers[question.name] = suggestion;
      }

      const subject = message.subject || t("mail.no_subject");
      const sender = message.sender_name || message.sender_address || t("mail.sender_unknown");
      const heading = h("h2", { id: "labels-task-title", class: "labels-task-subject", tabIndex: -1, dir: "auto", text: subject });
      const errorLine = h("div", { class: "field-error", role: "alert" });
      const primaryHost = h("div", { class: "labels-primary" });
      const extrasHost = h("div", { class: "labels-extras" });
      const shortcuts = h("details", { class: "labels-shortcuts" },
        h("summary", { text: t("labels.shortcuts.summary") }),
        h("ul", null,
          h("li", { text: t("labels.shortcuts.numbers") }),
          h("li", { text: t("labels.shortcuts.enter") }),
          h("li", { text: t("labels.shortcuts.suggestion") }),
          h("li", { text: t("labels.shortcuts.skip") }),
          h("li", { text: t("labels.shortcuts.help") })));

      clear(answerScroll);
      answerScroll.append(h("header", { class: "labels-task-head" },
        heading,
        h("p", { class: "labels-task-sender", dir: "auto", text: sender })),
      primaryHost, extrasHost);

      function questionForm(q, opts = {}) {
        const heading = opts.heading === false ? null : questionHeading(q, opts);
        const headingId = opts.headingId || (heading ? heading.id : "labels-q-" + radioName(q.name));
        return h("section", {
          class: "labels-question" + (opts.extra ? " labels-question-extra" : "") +
            (opts.heading === false ? " labels-question-body" : ""),
          "aria-labelledby": headingId,
        },
        heading ? heading.node : null,
        taxonomyDetails(q),
        answerControl(q, radioName(q.name), state.labelAnswers[q.name], headingId));
      }

      function answerControl(q, key, selected, labelId) {
        if (q.type === "choice") {
          return h("div", { class: "labels-choice-grid", role: "radiogroup", "aria-labelledby": labelId },
            (q.options || []).map((option, i) => {
              const input = h("input", {
                type: "radio",
                name: key,
                value: option.name,
                checked: selected === option.name,
                onchange: () => { state.labelAnswers[q.name] = option.name; errorLine.textContent = ""; updateSaveState(); },
              });
              return h("label", { class: "labels-choice" }, input,
                h("span", { class: "labels-choice-face" },
                  h("span", { class: "labels-choice-name", text: optionLabel(q.name, option.name) }),
                  i < 9 ? h("span", { class: "labels-key", "aria-hidden": "true", text: String(i + 1) }) : null));
            }));
        }
        if (q.type === "noul") {
          return h("div", { class: "labels-yesno", role: "radiogroup", "aria-labelledby": labelId },
            h("label", { class: "labels-yesno-option yes" },
              h("input", { type: "radio", name: key, value: "true", checked: selected === true || selected === "true" || selected === 1 || selected === "1", onchange: () => { state.labelAnswers[q.name] = true; errorLine.textContent = ""; updateSaveState(); } }),
              h("span", { text: t("labels.answer.yes") })),
            h("label", { class: "labels-yesno-option no" },
              h("input", { type: "radio", name: key, value: "false", checked: selected === false || selected === "false" || selected === 0 || selected === "0", onchange: () => { state.labelAnswers[q.name] = false; errorLine.textContent = ""; updateSaveState(); } }),
              h("span", { text: t("labels.answer.no") })));
        }
        if (q.type === "score") {
          const levels = Array.isArray(q.levels) ? q.levels : [];
          const max = Math.max(levels.length - 1, 0);
          const selectedLevel = Number.isInteger(Number(selected)) && Number(selected) >= 0 && Number(selected) <= max ? Number(selected) : -1;
          const meaning = h("div", { class: "labels-scale-meaning", "aria-live": "polite" });
          const drawMeaning = (level) => {
            clear(meaning);
            if (level < 0) {
              meaning.append(h("p", { class: "fine", text: t("labels.scale.none", { max }) }));
              return;
            }
            meaning.append(h("p", { class: "labels-scale-selected", text: t("labels.scale.selected", { level }) }));
            if (levels[level]) meaning.append(h("p", { class: "labels-level-meaning", lang: "en", text: levels[level] }));
          };
          const track = h("div", { class: "labels-scale-track", role: "radiogroup", "aria-labelledby": labelId },
            levels.map((level, i) => {
              const input = h("input", {
                type: "radio",
                name: key,
                value: String(i),
                checked: selectedLevel === i,
                onchange: () => {
                  state.labelAnswers[q.name] = i;
                  drawMeaning(i);
                  errorLine.textContent = "";
                  updateSaveState();
                },
              });
              return h("label", { class: "labels-scale-step", title: level }, input,
                h("span", { class: "labels-scale-dot", text: String(i) }));
            }));
          drawMeaning(selectedLevel);
          return h("div", { class: "labels-scale" },
            h("div", { class: "labels-scale-ends" },
              h("span", { text: questionScaleEndpoint(q.name, "low") }),
              h("span", { text: questionScaleEndpoint(q.name, "high") })),
            track,
            meaning);
        }
        return h("p", { class: "field-hint", text: t("labels.answer.unsupported") });
      }

      function drawExtras() {
        clear(extrasHost);
        const others = questions.filter((q) => q.name !== question.name);
        if (!others.length) return;
        const forms = h("div", { class: "labels-extra-forms" });
        const drawExtraQuestionForms = () => {
          clear(forms);
          for (const name of state.labelExtras) {
            const q = questions.find((item) => item.name === name);
            if (q) forms.append(questionForm(q, { extra: true }));
          }
        };
        const choices = h("div", { class: "labels-extra-choices" }, others.map((q) => {
          const input = h("input", {
            type: "checkbox",
            checked: state.labelExtras.includes(q.name),
            onchange: (event) => {
              if (event.currentTarget.checked) {
                if (!state.labelExtras.includes(q.name)) state.labelExtras.push(q.name);
              } else {
                state.labelExtras = state.labelExtras.filter((name) => name !== q.name);
                delete state.labelAnswers[q.name];
              }
              drawExtraQuestionForms();
            },
          });
          return h("label", { class: "check" }, input, h("span", { text: questionTitle(q.name) }));
        }));
        extrasHost.append(h("details", { class: "labels-extra" },
          h("summary", { text: t("labels.form.also") }),
          choices,
          forms));
        drawExtraQuestionForms();
      }

      function selectedNames() {
        return [question.name, ...state.labelExtras];
      }

      function hasPrimaryAnswer() {
        return Object.prototype.hasOwnProperty.call(state.labelAnswers, question.name);
      }

      function focusFirstAnswer() {
        const control = answerScroll.querySelector('input[name="' + radioName(question.name) + '"]');
        if (control) control.focus({ preventScroll: false });
        else heading.focus({ preventScroll: false });
      }

      function updateSaveState() {
        if (taskActions) taskActions.sync();
      }

      async function saveTask(trigger, override) {
        if (saving) return;
        if (override === undefined && !hasPrimaryAnswer()) {
          focusFirstAnswer();
          return;
        }
        const items = [];
        const missing = [];
        if (override !== undefined) {
          items.push({ message_id: task.message_id, question: question.name, value: override });
        } else {
          for (const name of selectedNames()) {
            const q = questions.find((item) => item.name === name);
            if (!q) continue;
            if (!Object.prototype.hasOwnProperty.call(state.labelAnswers, name)) { missing.push(q); continue; }
            items.push({ message_id: task.message_id, question: name, value: state.labelAnswers[name] });
          }
        }
        if (missing.length) {
          const control = answerScroll.querySelector('input[name="' + radioName(missing[0].name) + '"]');
          markFieldError(control, errorLine, t("labels.form.answer_all"));
          if (control) control.focus({ preventScroll: false });
          return;
        }
        if (!items.length) {
          errorLine.textContent = t("labels.form.answer_one");
          return;
        }
        const savedId = task.message_id;
        saving = true;
        try {
          await withBusy(trigger, t("labels.form.saving"), async () => {
            // withBusy captures the trigger's focus before this disables it.
            setTaskBusy(true);
            try {
              const hadSuggestion = suggestion !== undefined;
              const result = await request("/api/labels", { method: "POST", body: { items } });
              const stored = result && Array.isArray(result.stored) ? result.stored.length : items.length;
              applySaved(items, hadSuggestion);
              // The toast's live region is the one announcement: a second
              // status write made a screen reader hear the save twice.
              toast(t("labels.form.saved"), "info", t("labels.form.saved_body", { count: stored, id: formatId(savedId) }));
              state.labelAnswers = {};
              state.labelExtras = [];
              // The task identity, not the captured index, decides which
              // entry leaves and which task follows it in a changed queue.
              const savedAt = tasks.findIndex((item) => item.message_id === savedId);
              if (savedAt >= 0) tasks.splice(savedAt, 1);
              // The saved task left the queue; keep the total honest.
              queueTotal = Math.max(tasks.length, queueTotal - 1);
              drawQuestionChips();
              scheduleChipScroll();
              drawStatus();
              updateTopbar();
              afterLabelWrite();
              const current = tasks.findIndex((item) => item.message_id === savedId);
              if (current >= 0) {
                showLabelTask(current, { focusHeading: true });
              } else if (savedAt >= 0 && savedAt < tasks.length) {
                showLabelTask(savedAt, { focusHeading: true });
              } else {
                showComplete(true);
              }
            } catch (error) {
              reportActionError(error);
            }
          });
        } finally {
          saving = false;
          setTaskBusy(false);
        }
      }

      const saveButton = button(t("labels.form.save"), (event) => saveTask(event.currentTarget), { kind: suggestion !== undefined ? "" : "primary", icon: "check" });
      const skipButton = button(t("labels.form.skip"), () => skipTask(index), { kind: "quiet" });
      const useSuggestionButton = suggestion !== undefined
        ? button(t("labels.review.accept"), (event) => saveTask(event.currentTarget, suggestion), { kind: "primary", icon: "check" })
        : null;
      taskActions = labelTaskActions(actionBar);
      taskActions.register([
        { node: saveButton, blocked: () => !hasPrimaryAnswer() },
        { node: useSuggestionButton, blocked: () => false },
        { node: skipButton, blocked: () => false },
        { node: prevButton, blocked: () => index === 0 },
        { node: nextButton, blocked: () => index + 1 >= tasks.length },
      ]);
      updateSaveState();

      const suggestionCard = suggestion !== undefined
        ? h("section", { class: "review-suggestion labels-suggestion", "aria-label": t("labels.review.kicker") },
            h("div", { class: "review-suggestion-copy" },
              h("p", { class: "review-suggestion-kicker", text: t("labels.review.kicker") }),
              h("p", { class: "review-suggestion-value" },
                h("strong", { text: answerValueText(question, suggestion) })),
              h("p", { class: "fine", text: t("labels.review.accept_body") })),
            h("div", { class: "button-row" }, useSuggestionButton))
        : null;

      if (suggestionCard) {
        const primaryHeading = questionHeading(question);
        primaryHost.append(h("section", {
          class: "labels-question labels-question-primary",
          "aria-labelledby": primaryHeading.id,
        },
        primaryHeading.node,
        suggestionCard,
        h("p", { class: "labels-review-other", text: t("labels.review.other") }),
        questionForm(question, { heading: false, headingId: primaryHeading.id })));
      } else {
        primaryHost.append(questionForm(question));
      }
      drawExtras();

      clear(actionBar);
      actionBar.append(h("div", { class: "labels-action-row" },
        saveButton,
        skipButton),
      shortcuts,
      errorLine);

      const syncActionHeight = () => {
        updateChipScrollState();
        if (!actionBar.isConnected) return;
        const height = Math.ceil(actionBar.getBoundingClientRect().height);
        if (height > 0) answerPane.style.setProperty("--labels-actions-h", height + "px");
      };
      function observeActionHeight(force = false) {
        if ((prefetchActive > 0 && !force) || typeof ResizeObserver !== "function") return;
        if (state.labelObserver) {
          state.labelObserver.disconnect();
          state.labelObserver = null;
        }
        state.labelObserver = new ResizeObserver(syncActionHeight);
        state.labelObserver.observe(actionBar);
      }
      syncActionHeight();
      observeActionHeight();

      function selectByNumber(number) {
        if (!currentQuestion) return;
        let value;
        if (currentQuestion.type === "choice") {
          const option = (currentQuestion.options || [])[number - 1];
          if (!option) return;
          value = option.name;
        } else if (currentQuestion.type === "noul") {
          if (number === 1) value = true;
          else if (number === 2) value = false;
          else return;
        } else if (currentQuestion.type === "score") {
          const levels = Array.isArray(currentQuestion.levels) ? currentQuestion.levels : [];
          if (number - 1 >= levels.length) return;
          value = number - 1;
        } else {
          return;
        }
        const inputs = primaryHost.querySelectorAll('input[name="' + radioName(currentQuestion.name) + '"]');
        for (const input of inputs) {
          if (String(input.value) === String(value)) {
            input.checked = true;
            input.dispatchEvent(new Event("change", { bubbles: true }));
            return;
          }
        }
      }

      function handleKey(event) {
        if (state.route !== "labels" || state.modalOpen || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
        const target = event.target;
        if (target instanceof Element) {
          if (target.isContentEditable || target.closest("textarea, select")) return;
          const input = target.closest("input");
          if (input && !["radio", "checkbox", "button"].includes(String(input.type || ""))) return;
        }
        if (event.key === "?") {
          event.preventDefault();
          shortcuts.open = !shortcuts.open;
          return;
        }
        if (target instanceof Element && target.closest("button, a, summary")) return;
        if (/^[1-9]$/.test(event.key)) {
          event.preventDefault();
          selectByNumber(Number(event.key));
          return;
        }
        if (event.key === "Enter") {
          event.preventDefault();
          if (!hasPrimaryAnswer()) {
            focusFirstAnswer();
            return;
          }
          const current = state.labelAnswers[currentQuestion.name];
          const sameAsSuggestion = suggestion !== undefined &&
            (current === suggestion || String(current) === String(suggestion));
          if (sameAsSuggestion && !state.labelExtras.length && useSuggestionButton) saveTask(useSuggestionButton, suggestion);
          else saveTask(saveButton);
          return;
        }
        const key = String(event.key || "").toLowerCase();
        if (key === "s") {
          event.preventDefault();
          skipTask(index);
        } else if (key === "g" && suggestion !== undefined && useSuggestionButton) {
          event.preventDefault();
          saveTask(useSuggestionButton, suggestion);
        }
      }

      answerScroll.scrollTop = 0;
      if (options.focusHeading) heading.focus({ preventScroll: true });
      setLabelKeyHandler(handleKey);
      activeTaskAttach = () => {
        if (!screen.isConnected || state.route !== "labels") return;
        setLabelKeyHandler(handleKey, true);
        observeActionHeight(true);
        observeLabelChips(chipsHost, updateChipScrollState, scrollSelectedChip, true);
      };
    }
    const toolbar = labelsToolbar({ review, selectedName, count, summaryKnown, summaryError: summaryRead.error, chipsWrap });

    drawQuestionChips();
    drawStatus();
    updateTopbar();
    observeLabelChips(chipsHost, updateChipScrollState, scrollSelectedChip);

    const screen = h("div", { class: "screen labels-screen" }, toolbar, statusHost, workbenchHost);
    // The route cache re-appends this exact node after a Back. Re-install the
    // mounted task's handler and observers without rebuilding the task.
    screen.__routeAttach = () => { if (activeTaskAttach) activeTaskAttach(); };
    if (state.labelKeyHandler) document.removeEventListener("keydown", state.labelKeyHandler);
    state.labelKeyHandler = null;

    if (machineBlocker) {
      setLabelKeyHandler(null);
      setTopbarMeta(
        pill(questionTitle(selectedName), "accent"),
        pill(t("labels.machine_required"), "warn"));
      clear(workbenchHost);
      workbenchHost.append(section(t("labels.waiting.title"), t("labels.waiting.sub"), null,
        lockedCard(t("labels.waiting.heading"), t("labels.waiting.body"), blockerNode(machineBlocker))));
      scheduleChipScroll();
      return screen;
    }

    const queueLimit = Math.min(200, Math.max(1, Number(count) || 20));
    // A queue retry reloads the queue in place. It must not rebuild the whole
    // labels screen: the toolbar, chips and keyboard handler stay mounted.
    async function loadQueue(force = false) {
      try {
        queueError = null;
        const queueView = await requestCached("/api/labels/queue?question=" + encodeURIComponent(selectedName) + "&limit=" + String(queueLimit) + (review ? "&review=true" : ""), { force }, CACHE_TTL.labelsQueue);
        if (seq !== state.renderSeq) return false;
        tasks = queueView && Array.isArray(queueView.tasks) ? queueView.tasks : [];
        queueTotal = Number(queueView && queueView.total) || tasks.length;
        queueHasMore = !!(queueView && queueView.has_more);
        queueLoaded = true;
      } catch (error) {
        if (seq !== state.renderSeq) return false;
        queueError = error;
        queueLoaded = true;
        reportError(error);
      }
      drawStatus();
      updateTopbar();
      if (queueError) {
        clear(workbenchHost);
        workbenchHost.append(queueUnavailableState(queueError, () => loadQueue(true)));
      } else if (!tasks.length) {
        showEmptyQueue();
      } else {
        showLabelTask(0);
      }
      return true;
    }
    if (!(await loadQueue())) return h("div");
    scheduleChipScroll();
    return screen;
  }

  // labelsQueueCountText is the one queue-count wording: a page is not the
  // whole population, so it names both when the server reported more.
  function labelsQueueCountText({ machineBlocker, atEnd, queueError, queueLoaded, queueTotal, shown, review }) {
    if (machineBlocker) return t("labels.queue.waiting");
    if (atEnd) return t("labels.queue.end");
    if (queueError) return t("labels.queue.error_title");
    if (!queueLoaded) return t("common.loading");
    if (queueTotal > shown) return t("labels.queue.showing", { shown: formatNumber(shown), total: formatNumber(queueTotal) });
    return labelQueueText(shown, review);
  }

  function focusHeading(host, selector) {
    const heading = host.querySelector(selector);
    if (heading) heading.focus({ preventScroll: false });
  }

  // labelTaskActions is the one busy owner for a mounted task's action bar.
  // Each registered control carries the availability it has outside a save,
  // so clearing the busy state restores exactly those controls instead of
  // guessing from the current task's index.
  function labelTaskActions(bar) {
    let entries = [];
    let busy = false;
    const apply = () => {
      bar.setAttribute("aria-busy", busy ? "true" : "false");
      for (const entry of entries) {
        const disabled = busy || entry.blocked();
        entry.node.disabled = disabled;
        entry.node.setAttribute("aria-disabled", disabled ? "true" : "false");
        entry.node.classList.toggle("is-disabled", disabled);
      }
    };
    return {
      register(list) {
        entries = list.filter((entry) => entry.node);
        apply();
      },
      set(on) {
        busy = on;
        apply();
      },
      sync: apply,
    };
  }

  function labelsEmptyNode(review, onLabel, onRefresh) {
    const body = review
      ? emptyState(t("labels.queue.no_review_title"), t("labels.queue.no_review_body"),
        [button(t("labels.mode.label"), onLabel, { small: true }),
          button(t("common.refresh"), onRefresh, { icon: "refresh", small: true })], "labels")
      : emptyState(t("labels.queue.empty_title"), t("labels.queue.empty_body"), null, "labels");
    return h("div", { class: "labels-empty-queue" }, body);
  }

  // labelsMoreNode is the end-of-page surface: the queue is a page, not the
  // whole population, so the screen offers the next read instead of claiming
  // completion.
  function labelsMoreNode(total, onContinue, nextHref) {
    return h("section", { class: "surface labels-complete", "aria-labelledby": "labels-more-title" },
      emptyState(t("labels.more.title"), t("labels.more.body", { total: formatNumber(total) }), [
        button(t("labels.more.continue"), onContinue, { kind: "primary", small: true }),
        link(t("labels.complete.next"), nextHref, { small: true, kind: "quiet" }),
      ], "labels", { headingId: "labels-more-title", headingTabIndex: -1, wrap: false }));
  }

  // labelsCompleteNode is the true completion surface, shown only when the
  // server said the returned page is the whole population.
  function labelsCompleteNode(answered, nextHref) {
    return h("section", { class: "surface labels-complete", "aria-labelledby": "labels-complete-title" },
      emptyState(t("labels.complete.title"), answered ? t("labels.complete.body") : t("labels.complete.end_body"), [
        link(t("labels.complete.next"), nextHref, { kind: "primary", small: true }),
        link(t("labels.complete.home"), "#/hub", { small: true, kind: "quiet" }),
      ], "check", { headingId: "labels-complete-title", headingTabIndex: -1, wrap: false }));
  }

  // labelSummaryUnavailable is the one failed summary read of the Teach
  // screen. The chips, queue and mounted task keep working; only the counts
  // are unknown, so the progress surfaces render a dash rather than 0/0.
  // labelsToolbar is the screen's one header: the mode switch, the history
  // link and the optional summary failure. It stays out of renderLabels so
  // the screen orchestrator remains within its reviewed size.
  function labelsToolbar({ review, selectedName, count, summaryKnown, summaryError, chipsWrap }) {
    const modes = h("nav", { class: "segmented", "aria-label": t("labels.mode_aria") },
      tabButton(t("labels.mode.label"), !review, () => navigate("labels", { question: selectedName, count }), "labels-mode-label"),
      tabButton(t("labels.mode.review"), review, () => navigate("labels", { question: selectedName, count, review: "1" }), "labels-mode-review"));
    return h("section", { class: "labels-toolbar surface" },
      h("div", { class: "labels-toolbar-row" },
        h("div", { class: "labels-toolbar-copy" },
          h("h2", { text: t("labels.intro.title") }),
          h("p", { class: "section-sub", text: review ? t("labels.subtitle.review") : t("labels.subtitle.label") })),
        h("div", { class: "labels-toolbar-actions" }, modes,
          link(t("answers.history_link"), "#/answers", { small: true, kind: "quiet" }))),
      summaryKnown ? null : labelSummaryUnavailable(summaryError),
      chipsWrap);
  }

  // queueUnavailableState is the one failed queue read: the failure's own
  // message, a retry and the way to Learning (the same machine gates both).
  function queueUnavailableState(queueError, retry) {
    const message = apiFailure(queueError).message;
    return errorState(queueError, retry, {
      title: t("labels.queue.unavailable_title"),
      body: message || t("labels.queue.error_body"),
      icon: "labels",
      actions: [button(t("common.try_again"), retry, { icon: "refresh", small: true }),
        button(t("common.open_learning"), () => navigate("learning"), { small: true })],
    });
  }

  function radioName(name) {
    return "answer-" + name.replace(/[^A-Za-z0-9_-]/g, "-");
  }

  // taxonomyDetails and questionHeading are pure view builders shared by the
  // primary question and every extra form. They stay outside the render owner
  // so its task state stays within the reviewed function budget.
  function taxonomyDetails(q) {
    const details = [];
    if (q.instructions) details.push(h("p", { text: q.instructions }));
    if (q.type === "choice" && Array.isArray(q.options)) {
      details.push(h("ul", { class: "labels-taxonomy-list" }, q.options.map((option) => h("li", null,
        h("strong", { text: option.name }),
        option.description ? h("span", { text: option.description }) : null))));
    } else if (q.type === "noul") {
      details.push(h("dl", { class: "labels-taxonomy-list" },
        q.true ? h("div", null, h("dt", { text: t("labels.answer.yes") }), h("dd", { text: q.true })) : null,
        q.false ? h("div", null, h("dt", { text: t("labels.answer.no") }), h("dd", { text: q.false })) : null));
    } else if (q.type === "score" && Array.isArray(q.levels)) {
      details.push(h("ol", { class: "labels-taxonomy-list" }, q.levels.map((level) => h("li", { text: level }))));
    }
    return h("details", { class: "labels-how" },
      h("summary", { text: t("labels.how") }),
      h("p", { class: "fine", text: t("labels.taxonomy_note") }),
      h("div", { class: "labels-how-body", lang: "en" }, details));
  }

  function questionHeading(q, opts = {}) {
    const headingId = "labels-q-" + radioName(q.name) + (opts.extra ? "-extra" : "");
    return {
      id: headingId,
      node: h("div", { class: "labels-question-heading" },
        h("div", { class: "labels-question-head" },
          h("h3", { id: headingId, class: "labels-question-title", text: questionTitle(q.name) }),
          badge(questionTypeLabel(q.type), "plain")),
        h("p", { class: "labels-question-hint", text: questionHint(q.name) })),
    };
  }

