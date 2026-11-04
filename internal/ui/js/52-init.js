  // ---------------------------------------------------------------- errors and init

  function errorScreen(error, retry) {
    const api = error instanceof ApiError ? error : null;
    const failure = apiFailure(error);
    const problems = api ? api.problems : [];
    const problemTable = problems.length
      ? sortableTable({
        caption: t("error.problems.caption"),
        wrapperClass: "error-problems-wrap",
        className: "error-problems-table",
        columns: [
          {
            key: "stage",
            label: t("error.problem_stage"),
            type: "text",
            value: (problem) => problem.stage || "",
            render: (problem) => problem.stage || t("common.dash"),
          },
          {
            key: "subject",
            label: t("error.problem_subject"),
            type: "text",
            value: (problem) => problem.subject || "",
            render: (problem) => h("span", { class: "mono", text: problem.subject || t("common.dash") }),
          },
          {
            key: "problem",
            label: t("error.problem"),
            type: "text",
            value: (problem) => problemLabel(problem.code),
            render: (problem) => fragment(
              h("span", { text: problemLabel(problem.code) }),
              problemDiagnostic(problem)),
          },
        ],
        rows: problems,
      }).node
      : null;
    const body = h("div", { class: "screen narrow", dataset: { errorScreen: "1" } },
      h("div", { class: "screen-head" }, h("div", null,
        h("h2", { text: api && api.code === "learning_locked" ? t("error.locked_title") : t("error.screen_title") }),
        h("p", { class: "lede", text: failure.message }))),
      failure.diagnostic ? diagnosticDetails(failure.diagnostic, { paragraphClass: "fine mono" }) : null,
      api ? h("p", { class: "fine mono", text: t("error.http", { code: api.code, status: api.status || t("common.dash") }) }) : null,
      problemTable,
      retry ? h("div", { class: "button-row" }, button(t("common.try_again"), retry, { icon: "refresh", kind: "primary" })) : null);
    return body;
  }

  function watchLateToken() {
    // A window that loaded before its launcher supplied a token can still receive
    // one through a same-tab navigation. Reload so the normal handshake consumes
    // the fragment and strips it from the address.
    window.addEventListener("hashchange", () => {
      if (!state.token && (location.hash || "").startsWith("#token=")) location.reload();
    });
  }

  async function init() {
    watchLateToken();
    const token = readLaunchToken();
    if (!token) { showTokenError(); return; }
    state.token = token;
    dom.tokenScreen.hidden = true;
    dom.app.hidden = false;

    dom.shutdown.addEventListener("click", async () => {
      const confirm = await confirmDialog(t("shutdown.confirm_title"), t("shutdown.confirm_body"), t("shutdown.confirm_action"), true);
      if (!confirm) return;
      state.shuttingDown = true;
      stopBootWatch();
      if (eventSource) { eventSource.close(); eventSource = null; }
      setConnection("offline", t("connection.shutdown"));
      try {
        await request("/api/shutdown", { method: "POST" });
      } catch (error) {
        reportBackgroundError(error);
      }
      clear(dom.main);
      dom.main.append(h("div", { class: "screen narrow" },
        emptyState(t("shutdown.done_title"), t("shutdown.done_body"), null, "shutdown")));
      dom.shutdown.disabled = true;
    });

    window.addEventListener("hashchange", renderHistoryRoute);
    window.addEventListener("popstate", renderHistoryRoute);
    document.addEventListener("click", handleInternalLinkClick);
    installNavPrefetch();
    window.addEventListener("beforeunload", () => {
      stopBootWatch();
      if (eventSource) eventSource.close();
    });
    const skip = document.querySelector(".skip-link");
    if (skip) skip.addEventListener("click", (event) => { event.preventDefault(); dom.main.focus(); });

    const askNav = document.querySelector('[data-nav="ask"]');
    if (askNav) askNav.addEventListener("click", (event) => {
      state.askFocusPending = true;
      if (state.route === "ask") {
        event.preventDefault();
        focusAskInput();
      }
    });

    document.addEventListener("keydown", handleGlobalShortcut);

    // Mount the first screen before any read the route might need. The
    // skeleton and the boot strip are the honest answer while the warm is
    // still running; no list or count is invented in their place.
    const initial = parseRoute();
    state.route = initial.name;
    setHeader(initial.name);
    setTopbarMeta(...quickRouteMeta(initial));
    appendMain(routeSkeleton(initial.name));
    connectEvents();
    initGlobalSearch();
    startBootWatch();

    let languageValue = null;
    // The saved interface language is its own read: it decides how every
    // string of the first paint is rendered, so it must never be inferred
    // from the position of another promise in the startup batch.
    const languageReady = readLanguageSetting();
    // Learning and the job strip feed the shell chrome, never the route's
    // first content. They start with the paint and update their own chrome
    // when they land; a cold learning read must not hold the first screen.
    const learningReady = loadLearning().catch((error) => {
      reportError(error);
      return null;
    });
    loadJobs().catch(() => null);
    let gateValue = null;
    try {
      // The gate is permission, so the first paint waits for it. It is also
      // the one snapshot the boot route may reuse: the Hub and the Mail fast
      // path read it again through loadGate, and that read must be this one.
      const [, language, gate] = await Promise.all([loadStatus(), languageReady, loadGate()]);
      languageValue = language;
      gateValue = gate;
    } catch (error) {
      if (error instanceof ApiError && error.code === "network_error") {
        showTokenError("token.unreachable");
        return;
      }
      reportError(error);
    }
    languageValue = await languageReady;
    if (languageValue !== null) {
      await resolveAndApply(languageValue);
    } else {
      // Language is not a fatal dependency: keep the provisional navigator
      // choice and continue with the status-derived formatting zone.
      I18N.configure({ ownerTimeZone: state.status && state.status.time_zone ? state.status.time_zone : "" });
      localizeDocument();
    }
    // The first mount reuses the gate snapshot this boot already read. A
    // later navigation reads the gate again; only the mount that follows the
    // boot read is allowed to consume it.
    // The first-run screen opens before the first route paints. It is an
    // overlay over the shell, not a route, so the screen list and the router
    // stay unchanged; a failed setup read never blocks the shell.
    await maybeOpenSetup();
    gateBootSnapshot = gateValue && state.gate && state.gate.known ? state.gate : null;
    try {
      await renderRoute();
    } finally {
      gateBootSnapshot = null;
    }
    updateLearningChrome();
    // The zone auto-confirm needs the learning status and a POST, so it runs
    // after the content paint; a cold learning read never holds the screen.
    learningReady.then(() => {
      updateLearningChrome();
      return autoConfirmZone();
    }).catch(() => null);
  }

  provisionalLanguage().finally(() => {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init, { once: true });
    else init();
  });
