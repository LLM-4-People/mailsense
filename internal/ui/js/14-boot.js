  // ---------------------------------------------------------------- boot
  //
  // /api/boot is the one read allowed to answer before the warm finishes.
  // The strip shows real per-view progress; when it turns ready (or an older
  // server has no endpoint) the core prefetch starts once. A network or 5xx
  // failure hides the strip after a bounded wait without claiming readiness.

  function bootProgressNode(view) {
    const total = Math.max(Number(view && view.total) || 0, 1);
    const done = Math.min(Math.max(Number(view && view.done) || 0, 0), total);
    const progress = h("progress", {
      max: String(total),
      value: String(done),
      "aria-label": t("boot.progress_aria"),
    });
    const currentKey = view && view.current ? "boot.view." + view.current : "";
    const currentName = currentKey && I18N.has(currentKey) ? t(currentKey) : "";
    return h("div", { class: "boot-strip-inner" },
      h("span", { class: "boot-strip-title", text: t("boot.preparing") }),
      progress,
      h("span", { class: "boot-strip-count", text: t("boot.progress", {
        done: formatNumber(done),
        total: formatNumber(total),
      }) }),
      currentName ? h("span", { class: "boot-strip-current", text: t("boot.current", { view: currentName }) }) : null);
  }

  function bootWarningNode(view) {
    const total = Math.max(Number(view && view.total) || 0, 1);
    const done = Math.min(Math.max(Number(view && view.done) || 0, 0), total);
    const failed = Math.max(Number(view && view.failed) || 0, 0);
    const names = (Array.isArray(view && view.views) ? view.views : [])
      .filter((stage) => stage && stage.state === "failed")
      .map((stage) => {
        const key = "boot.view." + stage.name;
        return I18N.has(key) ? t(key) : stage.name;
      });
    return h("div", { class: "boot-strip-inner boot-strip-warning" },
      h("span", { class: "boot-strip-title", text: t("boot.ready_warning", { count: failed, failed: formatNumber(failed) }) }),
      h("span", { class: "boot-strip-count", text: t("boot.progress", {
        done: formatNumber(done),
        total: formatNumber(total),
      }) }),
      names.length ? h("span", { class: "boot-strip-current", text: t("boot.failed_views", { views: names.join(", ") }) }) : null,
      h("button", {
        type: "button",
        class: "boot-strip-dismiss",
        text: t("common.dismiss"),
        onclick: hideBoot,
      }));
  }

  function renderBoot(view) {
    if (!dom.bootStrip) return;
    const ready = !!view && view.state === "ready";
    const failed = ready ? Math.max(Number(view.failed) || 0, 0) : 0;
    if (!view || (ready && failed === 0)) {
      hideBoot();
      return;
    }
    clear(dom.bootStrip);
    if (ready) {
      // Readiness is not success: the warm can finish with failed views (for
      // example a missing decider). Keep the strip and name them.
      dom.bootStrip.classList.add("warning");
      dom.bootStrip.append(bootWarningNode(view));
    } else {
      dom.bootStrip.classList.remove("warning");
      dom.bootStrip.append(bootProgressNode(view));
    }
    dom.bootStrip.hidden = false;
  }

  function hideBoot() {
    if (!dom.bootStrip) return;
    dom.bootStrip.hidden = true;
    dom.bootStrip.classList.remove("warning");
    clear(dom.bootStrip);
  }

  function loadBoot() {
    return request("/api/boot");
  }

  function startBootWatch() {
    if (state.bootWatch || !state.token || state.shuttingDown) return;
    state.bootWatch = true;
    state.bootStartedAt = Date.now();
    const tick = async () => {
      state.bootTimer = null;
      if (!state.bootWatch || !state.token || state.shuttingDown) return;
      try {
        const view = await loadBoot();
        if (!state.bootWatch || !state.token || state.shuttingDown) return;
        state.boot = view;
        renderBoot(view);
        if (view && view.state === "ready") {
          finishBootWatch(true);
          return;
        }
      } catch (error) {
        if (!state.bootWatch || state.shuttingDown) return;
        if (error instanceof ApiError && error.status === 404) {
          // An older server has no boot contract: treat it as ready and keep
          // the shell's existing reads as the only progress signal.
          finishBootWatch(true);
          return;
        }
        if (error instanceof ApiError && error.status === 401) {
          state.bootWatch = false;
          hideBoot();
          return;
        }
        if (Date.now() - state.bootStartedAt > BOOT_POLL_DEADLINE_MS) {
          finishBootWatch(false);
          return;
        }
      }
      if (state.bootWatch && !state.shuttingDown) {
        const slow = Date.now() - state.bootStartedAt > BOOT_POLL_SLOW_AFTER_MS;
        state.bootTimer = window.setTimeout(tick, slow ? BOOT_POLL_SLOW_INTERVAL_MS : BOOT_POLL_INTERVAL_MS);
      }
    };
    tick();
  }

  function finishBootWatch(ready) {
    state.bootWatch = false;
    state.bootReady = !!ready;
    if (state.bootTimer) {
      window.clearTimeout(state.bootTimer);
      state.bootTimer = null;
    }
    const warning = ready && state.boot && state.boot.state === "ready" &&
      Math.max(Number(state.boot.failed) || 0, 0) > 0;
    if (!warning) hideBoot();
    if (ready) {
      if (!state.prefetchStarted) {
        state.prefetchStarted = true;
        prefetchCore();
      }
    }
  }

  function stopBootWatch() {
    state.bootWatch = false;
    if (state.bootTimer) {
      window.clearTimeout(state.bootTimer);
      state.bootTimer = null;
    }
    hideBoot();
  }

