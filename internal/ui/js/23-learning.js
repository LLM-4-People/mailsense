  // ---------------------------------------------------------------- learning

  // certificationCounts formats the one certification snapshot for copy and
  // pills. An unknown snapshot contributes no numbers.
  function certificationCounts(certification) {
    if (!certification) return {};
    return {
      certified: formatNumber(certification.certified),
      total: formatNumber(certification.total),
    };
  }

  async function renderLearning(route, seq) {
    const [view, summaryRead, certification] = await Promise.all([
      loadLearning(),
      readLabelSummary(),
      readCertification(),
    ]);
    if (seq !== state.renderSeq) return h("div");
    const status = view.status || {};
    const stages = Array.isArray(status.stages) ? status.stages : [];
    const blockers = allBlockers(view);
    const done = stages.filter((stage) => stage.state === "done").length;
    const running = stages.filter((stage) => stage.state === "running").length;
    const scope = status.scope ? status.scope.split(",").filter(Boolean) : [];
    // Permission is the shell's gate snapshot, never the learning status:
    // the status's own open flag described the same gate but could disagree
    // with the topbar. An unknown gate is not painted as closed.
    const permissionKnown = gateKnown();
    const open = permissionKnown && gateOpen();
    const preview = permissionKnown && gatePreview();
    const showPreview = preview && !open;
    const rawState = status.state || "learning";
    const stateValue = open ? "open" : rawState === "running" ? "learning" : rawState;
    const certificationParams = certificationCounts(certification);
    const partial = !!(open && certification && certification.certified < certification.total);
    const titleValue = partial ? "partial" : stateValue;
    const jobs = jobSnapshot();
    const runningJobs = jobs.filter((job) => job.status === "running");
    const labelsSummary = summaryRead.value;
    // A review count that could not be read is unknown, not zero: the review
    // action stays absent until the summary lands.
    const review = summaryRead.error ? null : Number(labelsSummary && labelsSummary.review) || 0;

    const questionNames = [];
    for (const entry of blockers) {
      const name = entry.blocker && entry.blocker.subject;
      if (name && questionHelp(name) && !questionNames.includes(name)) questionNames.push(name);
    }
    const topQuestions = questionNames.slice(0, 3);
    const firstQuestion = topQuestions[0] || "";
    const reviewRows = labelsSummary && Array.isArray(labelsSummary.questions) ? labelsSummary.questions : [];
    const reviewQuestion = reviewRows
      .filter((row) => Number(row.review) > 0)
      .sort((a, b) => Number(b.review) - Number(a.review))[0];
    const labelsDone = stages.some((stage) => stage.id === "labels" && stage.state === "done");
    const runChoice = blockers
      .flatMap((entry) => (Array.isArray(entry.blocker.actions) ? entry.blocker.actions : [])
        .filter((action) => String(action.id || "").startsWith("run_"))
        .map((action) => ({ action, entry })))
      .find(Boolean) || null;

    const actions = learningActions(open, labelsDone, review, firstQuestion, reviewQuestion, runChoice);

    const stateKind = partial ? "warn" : stateValue === "blocked" ? "blocked" : stateValue === "learned" ? "done" : stateValue === "open" ? "done" : "running";
    const questionLinks = !open ? learningQuestionLinks(topQuestions) : null;

    const callout = h("section", { class: "learning-callout " + (stateValue === "blocked" ? "blocked" : "") },
      h("div", { class: "learning-callout-top" },
        h("div", { class: "learning-callout-copy" },
          h("div", { class: "button-row" },
            pill(learningStateText(partial ? "partial" : stateValue), stateKind),
            permissionKnown ? gatePill(open) : null,
            certification ? pill(t("learning.certified_count", certificationParams),
              certification && certification.certified >= certification.total ? "accent" : "warn") : null,
            showPreview ? previewPill() : null),
          h("h2", { text: learningTitle(titleValue) }),
          h("p", { class: "lede", text: learningMeaning(titleValue, certificationParams) })),
        h("div", { class: "learning-next" },
          h("p", { class: "learning-next-title", text: t("learning.next.title") }),
          actions.length ? h("div", { class: "button-row" }, actions) : h("p", { class: "fine", text: t("common.none") }))),
      showPreview ? warningBanner(h("div", null, h("strong", { text: t("preview.learning_note") }))) : null,
      questionLinks);

    const zone = I18N.zoneInfo();
    const zoneText = zone.source === "owner"
      ? t("learning.time_zone_owner", { zone: zone.zone })
      : zone.source === "utc-fallback"
        ? t("learning.time_zone_utc")
        : t("learning.time_zone_machine", { zone: zone.zone });
    const side = h("aside", { class: "learning-side" },
      kv([
        [t("learning.stages"), running
          ? t("learning.stages.done_running", { done: formatNumber(done), total: formatNumber(stages.length), running: formatNumber(running) })
          : t("learning.stages.done", { done: formatNumber(done), total: formatNumber(stages.length) })],
        [t("learning.blockers"), blockers.length ? formatNumber(blockers.length) : t("common.none")],
        [t("learning.running_jobs"), runningJobs.length
          ? h("span", null,
            text(runningJobs.map((job) => jobNameLabel(job.name)).join(", ") + " "),
            navigateWith(t("logs.open"), "logs", runningJobs.length === 1 ? { job: runningJobs[0].id } : {}, { bare: true }))
          : t("learning.running_jobs_none")],
        summaryRead.error ? [t("learning.label_progress"), labelSummaryUnavailable(summaryRead.error)] : null,
        [t("learning.scope"), scope.length ? formatNumber(scope.length) : t("learning.scope_none")],
        [t("learning.time_zone"), zoneText],
        [t("learning.updated"), formatRelative(status.at)],
        [t("learning.last_checked"), formatDate(status.at)],
      ]),
      h("div", { class: "meter" },
        h("div", { class: "meter-top" }, h("span", { text: t("learning.stage_completion") }), h("strong", { text: stages.length ? formatPercent(done / stages.length, 0) : t("common.dash") })),
        h("progress", { max: Math.max(stages.length, 1), value: done, "aria-label": t("learning.stage_completion") })),
      h("div", { class: "button-row" },
        button(t("common.refresh"), () => refreshActiveRoute(), { icon: "refresh", small: true, kind: "quiet" })));

    const timeline = stages.length
      ? h("div", { class: "timeline" }, stages.map((stage) => stageNode(stage, view)))
      : emptyState(t("learning.timeline.empty_title"), t("learning.timeline.empty_body"), [], "learning");

    return h("div", { class: "screen learning-screen" },
      h("div", { class: "learning-summary section-scroll", dataset: { scrollKey: "learning-summary" } }, callout, side),
      h("div", { class: "learning-body route-scroll section-scroll", dataset: { scrollKey: "learning-body" } },
        section(t("learning.timeline.title"), t("learning.timeline.sub"), null, timeline),
        blockers.length ? section(t("learning.blockers.title"), t("learning.blockers.sub", { count: blockers.length }), null,
          h("div", { class: "blocker-list" }, blockers.map((entry) => learningBlockerNode(entry)))) : null));
  }

  // learningQuestionLinks is the one list of the questions that help most: a
  // link to the label queue for each, in the callout and nowhere else.
  function learningQuestionLinks(topQuestions) {
    if (!topQuestions.length) return null;
    return h("div", { class: "learning-questions" },
      h("h3", { text: t("learning.questions.title") }),
      h("p", { class: "fine", text: t("learning.questions.sub") }),
      h("div", { class: "learning-question-links" }, topQuestions.map((name) =>
        navigateWith(questionTitle(name), "labels", { question: name }, {
          bare: true,
          class: "learning-question-link",
          icon: "arrow-right",
          trailingIcon: true,
        }))));
  }

  // A blocker names where its fix lives. A failed run is diagnosed in Logs; a
  // configuration blocker in the settings it names. The link rides under the
  // shared blocker card, so every screen keeps the same blocker wording.
  const LEARNING_BLOCKER_LINKS = {
    evidence_run_failed: { screen: "logs", label: "logs.open" },
    layer1_run_failed: { screen: "logs", label: "logs.open" },
    layer1_config_invalid: { screen: "settings", label: "learning.link.settings", params: { tab: "general", q: "layer1." } },
    evidence_config_invalid: { screen: "settings", label: "learning.link.settings", params: { tab: "general", q: "evidence." } },
    time_zone_unconfirmed: { screen: "settings", label: "learning.link.settings", params: { tab: "general", q: "time.zone" } },
    no_embedder: { screen: "settings", label: "learning.link.settings", params: { tab: "runtime" } },
  };

  function learningBlockerNode(entry) {
    const node = blockerNode(entry);
    const code = String(entry && entry.blocker && entry.blocker.code ? entry.blocker.code : "");
    const target = LEARNING_BLOCKER_LINKS[code];
    if (!target) return node;
    node.append(h("div", { class: "button-row" },
      navigateWith(t(target.label), target.screen, target.params || {}, { small: true })));
    return node;
  }

  // learningActions is the callout's next-step list. A review count that
  // could not be read stays absent instead of becoming a zero action.
  function learningActions(open, labelsDone, review, firstQuestion, reviewQuestion, runChoice) {
    const actions = [];
    if (open) {
      actions.push(button(t("learning.open_mail"), () => navigate("mail"), { icon: "mail", kind: "primary", small: true }));
      return actions;
    }
    if (!labelsDone) {
      actions.push(navigateWith(t("learning.action.answer"), "labels", firstQuestion ? { question: firstQuestion } : {}, { kind: "primary", small: true }));
    }
    if (review > 0) {
      actions.push(navigateWith(t("learning.action.review", { count: review }), "labels", {
        review: "1",
        question: reviewQuestion ? reviewQuestion.question : firstQuestion,
      }, { small: true }));
    }
    if (runChoice) {
      actions.push(button(actionLabel(runChoice.action), (event) => executeAction(
        runChoice.action,
        { blocker: runChoice.entry.blocker, stage: runChoice.entry.stage },
        event.currentTarget,
      ), { small: true, kind: "quiet", icon: "play" }));
    }
    return actions;
  }

  function stageProgressText(stage) {
    const total = stage.progress ? Number(stage.progress.total) || 0 : 0;
    const done = stage.progress ? Number(stage.progress.done) || 0 : 0;
    const unit = stage.progress && stage.progress.unit ? measureUnit(stage.progress.unit, total) : "";
    const pct = total > 0 ? percentOf(done, total) : null;
    if (total <= 0) return t("common.not_measured");
    return unit
      ? t("learning.progress", { done: formatNumber(done), total: formatNumber(total), unit, percent: formatNumber(pct) })
      : t("learning.progress_no_unit", { done: formatNumber(done), total: formatNumber(total), percent: formatNumber(pct) });
  }

  // stageProgressDetail is the optional breakdown of a running index
  // (embedded, unchanged, failed); other stages have none.
  function stageProgressDetail(stage) {
    const progress = stage && stage.progress ? stage.progress : null;
    if (!progress) return "";
    const parts = [];
    if (progress.stage) parts.push(jobStageLabel(progress.stage));
    if (progress.detail) parts.push(questionTitle(progress.detail));
    // The same step position the job chip shows, from the same fields.
    if (Number(progress.step_total) > 0) {
      parts.push(t("shell.job.step", {
        done: formatNumber(progress.step_done || 0),
        total: formatNumber(progress.step_total),
      }));
    }
    const embedded = Number(progress.embedded) || 0;
    const unchanged = Number(progress.unchanged) || 0;
    const failed = Number(progress.failed) || 0;
    if (embedded > 0) parts.push(t("learning.progress_embedded", { count: embedded }));
    if (unchanged > 0) parts.push(t("learning.progress_unchanged", { count: unchanged }));
    if (failed > 0) parts.push(t("learning.progress_failed", { count: failed }));
    return metaLine(parts);
  }

  function stageNode(stage, learning) {
    const total = stage.progress ? Number(stage.progress.total) || 0 : 0;
    const done = stage.progress ? Number(stage.progress.done) || 0 : 0;
    const pct = total > 0 ? percentOf(done, total) : null;
    const detail = stageProgressDetail(stage);
    const eta = stage.eta ? formatDuration(stage.eta) : "";
    const blockers = Array.isArray(stage.blockers) ? stage.blockers : [];
    return h("div", { class: "stage " + (stage.state || "pending"), dataset: { stageId: stage.id || "" } },
      h("div", { class: "stage-rail" }, h("span", { class: "stage-dot", "aria-hidden": "true" })),
      h("div", { class: "stage-body" },
        h("div", { class: "stage-top" },
          h("span", { class: "stage-title", text: stageTitle(stage.id) }),
          statusPill(stage.state || "pending"),
          blockers.length ? badge(t("learning.blocker_badge", { count: blockers.length }), "warn") : null),
        stageHint(stage.id) ? h("div", { class: "stage-hint", text: stageHint(stage.id) }) : null,
        h("div", { class: "stage-progress" },
          pct !== null ? h("progress", { max: Math.max(total, 1), value: Math.min(done, Math.max(total, 1)), class: stage.state === "blocked" ? "warn" : "", "aria-label": t("learning.stage_progress_aria", { stage: stageTitle(stage.id) }) }) : null,
          h("span", { class: "progress-text", text: stageProgressText(stage) }),
          h("span", { class: "stage-progress-detail", hidden: !detail, text: detail }),
          eta ? h("span", { class: "eta", text: t("common.eta", { duration: eta }) }) : null),
        blockers.length ? h("div", { class: "blocker-list" }, blockers.map((blocker) => blockerNode({ stage: stage.id, blocker }))) : null,
        technicalRows([
          [t("learning.technical_stage"), stage.id || ""],
          [t("common.diagnostic"), stage.note || "", "en"],
        ])));
  }

  // updateLearningStageNode paints one live index progress event into the
  // mounted Learning screen, so the stage advances without re-fetching or
  // rebuilding the route (focus, scroll and the rest of the page stay).
  function updateLearningStageNode(stage) {
    if (!stage || !stage.id) return;
    const node = document.querySelector('[data-stage-id="' + stage.id + '"]');
    if (!node) return;
    const total = stage.progress ? Number(stage.progress.total) || 0 : 0;
    const done = stage.progress ? Number(stage.progress.done) || 0 : 0;
    const pct = total > 0 ? percentOf(done, total) : null;
    node.className = "stage " + (stage.state || "pending");
    const pillNode = node.querySelector(".stage-top .pill");
    if (pillNode) {
      pillNode.textContent = statusLabel(stage.state || "pending");
      pillNode.className = "pill " + statusKind(stage.state || "pending");
    }
    const label = node.querySelector(".progress-text");
    let bar = node.querySelector("progress");
    if (pct !== null && !bar) {
      const host = node.querySelector(".stage-progress");
      if (host) {
        bar = document.createElement("progress");
        host.insertBefore(bar, label || host.firstChild);
      }
    }
    if (bar && pct !== null) {
      bar.max = Math.max(total, 1);
      bar.value = Math.min(done, Math.max(total, 1));
      bar.className = stage.state === "blocked" ? "warn" : "";
      bar.setAttribute("aria-label", t("learning.stage_progress_aria", { stage: stageTitle(stage.id) }));
    }
    if (label) label.textContent = stageProgressText(stage);
    const detail = node.querySelector(".stage-progress-detail");
    if (detail) {
      const detailText = stageProgressDetail(stage);
      detail.textContent = detailText;
      detail.hidden = !detailText;
    }
  }

  // applyIndexProgress is the direct paint path for index.progress. It
  // updates the cached learning payload and the visible stage in place; it
  // never fetches, so it cannot add refresh churn beside jobs.update and
  // never touches the Ask composer.
  function applyIndexProgress(payload) {
    if (!payload) return;
    const done = Math.max(0, Number(payload.done) || 0);
    const total = Math.max(0, Number(payload.total) || 0);
    const embedded = Math.max(0, Number(payload.embedded) || 0);
    const unchanged = Math.max(0, Number(payload.unchanged) || 0);
    const failed = Math.max(0, Number(payload.failed) || 0);
    const stages = state.learning && state.learning.status && Array.isArray(state.learning.status.stages)
      ? state.learning.status.stages : null;
    if (!stages) {
      scheduleLearningRefresh();
      return;
    }
    const stage = stages.find((entry) => entry && entry.id === "embeddings");
    if (!stage) {
      scheduleLearningRefresh();
      return;
    }
    stage.state = "running";
    stage.progress = Object.assign({}, stage.progress || {}, {
      done, total, unit: "messages", embedded, unchanged, failed,
    });
    stage.blockers = [];
    if (state.route === "learning") updateLearningStageNode(stage);
  }

