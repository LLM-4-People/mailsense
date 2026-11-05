  // ---------------------------------------------------------------- actions and dialogs

  // runAction is the one top-level action boundary. withBusy owns the
  // trigger's disabled state and always restores it; this runner adds the
  // default report for an action that would otherwise fail silently. A
  // screen whose form already shows the failure inline catches it inside
  // `fn` (so nothing rejects) or passes `report: false`.
  async function runAction(trigger, label, fn, options = {}) {
    try {
      return await withBusy(trigger, label, fn);
    } catch (error) {
      if (options.report !== false) reportActionError(error);
      return undefined;
    }
  }

  async function executeAction(action, context, trigger) {
    if (!action || !action.id) return;
    const params = action.params || {};
    switch (action.id) {
      case "pick_mailboxes": navigate("sources"); return;
      case "discover_mailboxes":
        await withBusy(trigger, t("action_dialog.discover"), async () => { await discoverFromAction(params, trigger); });
        return;
      case "declare_address": await declareDialog(params.key || ""); return;
      case "confirm_time_zone": await zoneDialog(params); return;
      case "run_evidence": await startJobAction("/api/actions/run_evidence", undefined, t("action_dialog.evidence_started"), trigger); return;
      case "run_layer1": await startJobAction("/api/actions/run_layer1", undefined, t("action_dialog.layer1_started"), trigger); return;
      case "label_messages": navigate("labels", { question: params.question || "", count: params.count || "" }); return;
      case "run_judge": await judgeDialog(params, trigger); return;
      case "review_failures": navigate("logs", { component: params.log || params.component || "", level: params.level || "" }); return;
      case "review_requirements": navigate("settings", { tab: "general", q: "layer1.requirements.file" }); return;
      case "choose_teacher": navigate("settings", { tab: "general", q: "layer1.teachers.file" }); return;
      case "choose_embedder": navigate("settings", { tab: "runtime" }); return;
      case "start_source": await sourceGuidance("start", params, trigger); return;
      case "reconnect_mailbox": await sourceGuidance("reconnect", params, trigger); return;
      case "resume_source": await sourceGuidance("resume", params, trigger); return;
      case "retry_details": await sourceGuidance("retry", params, trigger); return;
      case "unpick_mailbox": await unpickMailbox(params.key, trigger); return;
      default: toast(t("action_dialog.action_not_handled"), "warn", t("action_dialog.action_not_handled_body", { action: action.id }));
    }
  }

  // afterBackgroundAction finishes one action. The caller names the topics
  // the write actually changed; the graph owns every read, cached screen and
  // refresh target. The shell snapshots are independent reads, so they run
  // together instead of queueing behind each other. `options` carries the
  // call's invalidation policy (a permission write drops gated markup; a zone
  // or job change only stales it).
  async function afterBackgroundAction(topics, options = {}) {
    if (!Array.isArray(topics) || !topics.length) {
      throw new Error("afterBackgroundAction requires at least one invalidation topic");
    }
    const affected = invalidateTopics(topics, Object.assign({}, options, { refresh: false }));
    await Promise.all([
      loadLearning(true),
      loadHub(true).catch(() => null),
      loadStatus(true).catch(() => null),
    ]);
    // The write can finish after the owner has moved on. Only the screen the
    // topics can change is rebuilt then; an unrelated cached route keeps its
    // DOM, scroll and focus for its next visit.
    const active = routeCache.get(state.routeKey);
    const affectedNow = affected.allRoutes ||
      [...affected.routes].some((name) => (active ? routeCacheMatches(active, name) : state.route === name));
    if (affectedNow) await refreshActiveRoute();
  }

  async function discoverFromAction(params, trigger) {
    let source = params.source || "";
    if (!source && params.key) {
      try {
        const boxes = await loadMailboxes(true);
        const box = boxes.find((item) => item.key === params.key);
        source = box ? box.source_id : "";
      } catch { /* the action falls back to the first source */ }
    }
    if (!source && state.status && Array.isArray(state.status.sources) && state.status.sources.length) source = state.status.sources[0].id;
    if (!source) { toast(t("action_dialog.no_source"), "warn", t("action_dialog.no_source_body")); navigate("sources"); return; }
    try {
      const result = await request("/api/mailboxes/discover", { method: "POST", body: { source } });
      state.mailboxes = Array.isArray(result.mailboxes) ? result.mailboxes : [];
      toast(t("action_dialog.discovered"), "info", t("action_dialog.discovered_body", { count: state.mailboxes.length }));
      await afterBackgroundAction(["discovery"]);
    } catch (error) { reportActionError(error); }
  }

  async function startJobAction(path, body, message, trigger) {
    await withBusy(trigger, t("action_dialog.starting"), async () => {
      try {
        const response = await request(path, {
          method: "POST",
          body: body === undefined ? {} : body,
          timeoutMs: LONG_REQUEST_TIMEOUT_MS,
        });
        toast(message, "info", response && response.job ? jobNameLabel(response.job.name) : "");
        await afterBackgroundAction(["job"]);
      } catch (error) { reportActionError(error); }
    });
  }

  async function unpickMailbox(key, trigger) {
    if (!key) return false;
    const ok = await confirmDialog(t("sources.unpick_confirm_title"), t("sources.unpick_confirm_body"), t("sources.unpick_confirm_action"), true);
    if (!ok) return false;
    let unpicked = false;
    await withBusy(trigger, t("sources.unpicking"), async () => {
      try {
        await request("/api/mailboxes/unpick", { method: "POST", body: { keys: [key] } });
        state.mailboxes = null;
        toast(t("sources.unpicked_toast"), "info", key);
        await afterBackgroundAction(["selection"], { hard: true });
        unpicked = true;
      } catch (error) { reportActionError(error); }
    });
    return unpicked;
  }

  // autoConfirmZone completes the machine stage the first time the shell
  // runs in a real browser: WebView2 already knows the system IANA zone, so
  // the owner should not have to retype it. The manual dialog remains the
  // fallback when the browser refuses the zone or the call fails.
  function browserTimeZone() {
    try {
      const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
      return typeof zone === "string" ? zone.trim() : "";
    } catch { return ""; }
  }

  // One page confirms at most once. The first call owns the POST and the one
  // success toast; a concurrent call shares its promise and a later call
  // receives the settled one, so a reload that still reports the blocker
  // cannot post or toast a second time. A failure stays silent: the blocker
  // remains and the manual dialog is the recovery path.
  let zoneAutoPromise = null;
  function autoConfirmZone() {
    if (zoneAutoPromise) return zoneAutoPromise;
    zoneAutoPromise = (async () => {
      const learning = state.learning;
      const status = learning && learning.status;
      if (!status || !Array.isArray(status.stages)) return false;
      const machine = status.stages.find((stage) => stage && stage.id === "machine");
      const blockers = machine && Array.isArray(machine.blockers) ? machine.blockers : [];
      if (!blockers.some((blocker) => blocker && blocker.code === "time_zone_unconfirmed")) return false;
      const zone = browserTimeZone() || (state.status && state.status.time_zone) || "";
      if (!zone) return false;
      try {
        await request("/api/learning/zone", { method: "POST", body: { zone } });
      } catch {
        // The blocker stays visible; the manual dialog is the recovery path.
        return false;
      }
      await Promise.all([loadLearning(true), loadStatus(true)]).catch(() => null);
      toast(t("learning.zone_auto", { zone }), "info", t("learning.zone_auto_body", { zone }));
      return true;
    })().catch(() => false);
    return zoneAutoPromise;
  }

  async function zoneDialog(params) {
    const detected = params.iana || "";
    const offset = params.offset ? " (" + params.offset + ")" : "";
    const input = h("input", { class: "input", type: "text", value: detected === "Local" ? "" : detected, placeholder: "Europe/Paris", list: "zone-suggestions", required: true });
    const datalist = h("datalist", { id: "zone-suggestions" }, ["UTC", "Europe/London", "Europe/Paris", "Europe/Berlin", "America/New_York", "America/Chicago", "America/Los_Angeles", "Asia/Tokyo", "Asia/Singapore", "Australia/Sydney"].map((zone) => h("option", { value: zone })));
    const errorLine = h("div", { class: "field-error" });
    const body = h("div", null,
      h("p", { text: t("action_dialog.zone.body") }),
      params.abbrev ? h("p", { class: "fine", text: t("action_dialog.zone.detected", { abbrev: params.abbrev, offset, zone: detected ? metaSeparator() + detected : "" }) }) : null,
      field(t("action_dialog.zone.input"), input),
      datalist,
      errorLine);
    const ok = await openDialog({
      title: t("action_dialog.zone.title"),
      body,
      confirm: t("action_dialog.zone.confirm"),
      onConfirm: async () => {
        const zone = input.value.trim();
        if (!zone) {
          markFieldError(input, errorLine, t("action_dialog.zone.required"));
          input.focus();
          return false;
        }
        markFieldError(input, errorLine, "");
        try {
          await request("/api/learning/zone", { method: "POST", body: { zone } });
          toast(t("action_dialog.zone.confirmed"), "info", zone);
          return true;
        } catch (error) {
          markFieldError(input, errorLine, apiFailure(error).message);
          input.focus();
          return false;
        }
      },
    });
    if (ok) { await afterBackgroundAction(["timeZone"]); }
  }

  async function judgeDialog(params, trigger) {
    const count = Math.max(1, Number(params.count) || 1);
    const question = params.question || "";
    const provider = params.provider || "";
    const cost = params.estimated_cost_usd || t("common.unknown");
    const known = cost !== t("common.unknown") && Number.isFinite(Number(cost));
    const body = h("div", null,
      h("p", { text: t("action_dialog.judge.body") }),
      h("div", { class: "dialog-cost" },
        h("div", { text: t("action_dialog.judge.cost", { cost: known ? formatUsd(cost) : t("common.unknown") }) }),
        h("div", { class: "fine", text: params.cost_note || (provider ? t("action_dialog.judge.provider", { provider }) : t("action_dialog.judge.provider_default")) })),
      h("p", { class: "fine", text: t("action_dialog.judge.summary", {
        count,
        question: question ? t("action_dialog.judge.for_question", { question }) : "",
        cost: known ? formatUsd(cost) : t("action_dialog.judge.unpriced"),
      }) }));
    const ok = await openDialog({
      title: t("action_dialog.judge.title"),
      confirm: t("action_dialog.judge.confirm"),
      body,
      onConfirm: async () => {
        const options = {
          limit: count,
          questions: question ? [question] : [],
          budget: { max_calls: count, max_usd: known ? Number(cost) : 0, allow_unpriced: !known },
        };
        if (provider) options.provider = provider;
        try {
          const response = await request("/api/actions/run_judge", {
            method: "POST",
            body: options,
            timeoutMs: LONG_REQUEST_TIMEOUT_MS,
          });
          toast(t("action_dialog.judge_started"), "info", response && response.job ? jobNameLabel(response.job.name) : "");
          return true;
        } catch (error) { reportActionError(error); return false; }
      },
    });
    if (ok) { await afterBackgroundAction(["job"]); }
  }

  async function sourceGuidance(mode, params, trigger) {
    const key = ["start", "reconnect", "resume", "retry"].includes(mode) ? mode : "default";
    const title = t("action_dialog.source." + key + "_title");
    const bodyText = t("action_dialog.source." + key + "_body");
    const body = h("div", null,
      h("p", { text: bodyText }),
      params.key ? h("p", { class: "mono fine", text: params.key }) : null,
      h("p", { class: "fine", text: t("action_dialog.source.api_note") }));
    const primaryLabel = mode === "start" || mode === "reconnect" ? t("action_dialog.source.discover_mailboxes") : t("action_dialog.source.run_sync");
    const ok = await openDialog({
      title,
      confirm: primaryLabel,
      body,
      onConfirm: async () => true,
    });
    if (!ok) return;
    if (mode === "start" || mode === "reconnect") await discoverFromAction(params, trigger);
    else await syncFromAction(params, trigger);
  }

  async function syncFromAction(params, trigger) {
    let source = params.source || "";
    if (!source && state.status && Array.isArray(state.status.sources) && state.status.sources.length) source = state.status.sources[0].id;
    if (!source) { toast(t("action_dialog.no_source"), "warn", t("action_dialog.no_source_body")); return; }
    await withBusy(trigger, t("action_dialog.starting"), async () => {
      try {
        const response = await request("/api/actions/sync", {
          method: "POST",
          body: { source },
          timeoutMs: LONG_REQUEST_TIMEOUT_MS,
        });
        toast(t("action_dialog.sync_started"), "info", response && response.job ? jobNameLabel(response.job.name) : "");
        await afterBackgroundAction(["job"]);
      } catch (error) { reportActionError(error); }
    });
  }

  // The explicit, reversible test preview: it uses the ordinary settings
  // API (no separate action endpoint), and reloads the learning view so
  // every screen re-reads the real gate and the preview flag.
  async function enablePreviewDialog() {
    const ok = await openDialog({
      title: t("preview.dialog.title"),
      eyebrow: t("preview.dialog.eyebrow"),
      body: h("div", null, h("p", { text: t("preview.dialog.body") })),
      confirm: t("preview.dialog.confirm"),
      onConfirm: async () => true,
    });
    if (ok) await savePreview(true, null);
  }

  async function disablePreview(trigger) {
    await savePreview(false, trigger);
  }

  async function savePreview(enabled, trigger) {
    await withBusy(trigger, null, async () => {
      try {
        await request("/api/settings/learning.test_mode", { method: "PUT", body: { value: enabled } });
        toast(enabled ? t("preview.enabled_toast") : t("preview.disabled_toast"), "info",
          enabled ? t("preview.enabled_toast_body") : t("preview.disabled_toast_body"));
        await afterBackgroundAction(["preview"], { hard: true });
      } catch (error) {
        reportActionError(error);
      }
    });
  }

  function confirmDialog(title, message, confirmLabel, danger = false) {
    return openDialog({
      title,
      body: h("p", { class: "modal-message", text: message }),
      confirm: confirmLabel,
      danger,
      onConfirm: async () => true,
    });
  }

  function openDialog(options) {
    if (state.modalOpen) return Promise.resolve(false);
    state.modalOpen = true;
    dom.modalTitle.textContent = options.title || t("modal.confirm");
    dom.modalEyebrow.textContent = options.eyebrow || t("modal.eyebrow");
    clear(dom.modalBody);
    if (options.body) dom.modalBody.append(options.body);
    clear(dom.modalActions);
    let resolve;
    const done = new Promise((r) => { resolve = r; });
    let settled = false;
    let busy = false;
    let confirm = null;
    const onSubmit = (event) => { event.preventDefault(); if (!busy && confirm) confirm.click(); };
    const onCancel = (event) => { event.preventDefault(); if (!busy) close(false); };
    const close = (value) => {
      if (settled) return;
      settled = true;
      busy = false;
      state.modalOpen = false;
      state.modalCloseFn = null;
      dom.modal.removeAttribute("aria-busy");
      dom.modalForm.removeEventListener("submit", onSubmit);
      dom.modal.removeEventListener("cancel", onCancel);
      dom.modal.close();
      resolve(value);
      scheduleWhatsNewAfterDialog();
    };
    state.modalCloseFn = () => { if (!busy) close(false); };
    const cancel = button(options.cancel || t("common.cancel"), () => { if (!busy) close(false); }, { kind: "quiet" });
    confirm = button(options.confirm || t("modal.confirm"), async () => {
      if (busy) return;
      busy = true;
      confirm.disabled = true;
      cancel.disabled = true;
      dom.modal.setAttribute("aria-busy", "true");
      try {
        const result = options.onConfirm ? await options.onConfirm() : true;
        if (result !== false) close(true);
      } finally {
        if (!settled) {
          busy = false;
          confirm.disabled = false;
          cancel.disabled = false;
          dom.modal.removeAttribute("aria-busy");
        }
      }
    }, { kind: options.danger ? "danger" : "primary" });
    dom.modalActions.append(cancel, confirm);
    dom.modalForm.addEventListener("submit", onSubmit);
    dom.modal.addEventListener("cancel", onCancel);
    const closeButton = dom.modal.querySelector("[data-modal-close]");
    // The header X is a cancel, so it obeys the same guard as Escape and
    // the Cancel button: during a write the dialog stays open and honest.
    if (closeButton) closeButton.onclick = () => { if (!busy) close(false); };
    dom.modal.showModal();
    const focusable = dom.modalBody.querySelector("input, select, textarea, button");
    if (focusable) focusable.focus();
    else confirm.focus();
    return done;
  }

