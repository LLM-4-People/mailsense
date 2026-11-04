  // ---------------------------------------------------------------- setup
  //
  // The first-run screen is a short, resumable sequence over the existing
  // owners: the settings store (language and the completion marker), the zone
  // route, the mailbox route, and the provider and runtime screens. It never
  // writes a second configuration document.

  const SETUP_STEPS = ["welcome", "mailbox", "model", "finish"];

  async function loadSetup() {
    return request("/api/setup");
  }

  // maybeOpenSetup runs once after the launch reads. A failed read is not a
  // reason to block the shell: Sources and Settings stay reachable.
  async function maybeOpenSetup() {
    let view = null;
    try {
      view = await loadSetup();
    } catch {
      state.setup = null;
      return false;
    }
    state.setup = view;
    if (!view || !view.needed) return false;
    state.setupStep = "welcome";
    state.setupDraft = { language: view.language || "auto", zone: view.time_zone || browserZone() };
    openSetup();
    return true;
  }

  function browserZone() {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
    } catch {
      return "";
    }
  }

  function setupStepDone(name) {
    return SETUP_STEPS.indexOf(name) < SETUP_STEPS.indexOf(state.setupStep);
  }

  function setupStepListNode() {
    return h("ol", { class: "setup-steps", "aria-label": t("setup.steps_aria") },
      SETUP_STEPS.map((name, index) => h("li", {
        class: "setup-step" + (name === state.setupStep ? " current" : "") + (setupStepDone(name) ? " done" : ""),
      },
      h("span", { class: "setup-step-number", text: String(index + 1) }),
      h("span", { class: "setup-step-label", text: t("setup.step." + name) }))));
  }

  function setupShell(body, actions) {
    return h("div", { class: "setup-card surface" },
      h("div", { class: "setup-head" },
        h("p", { class: "eyebrow", text: t("setup.eyebrow") }),
        h("h1", { id: "setup-title", text: t("setup.title") }),
        h("p", { class: "lede", text: t("setup.intro") })),
      setupStepListNode(),
      h("div", { class: "setup-body" }, body),
      h("div", { class: "setup-actions" }, actions));
  }

  function openSetup() {
    const root = h("div", {
      id: "setup-root",
      class: "setup-root",
      role: "dialog",
      "aria-modal": "true",
      "aria-labelledby": "setup-title",
    });
    document.body.append(root);
    if (dom.app) dom.app.hidden = true;
    paintSetup();
  }

  function closeSetup() {
    const root = document.getElementById("setup-root");
    if (root) root.remove();
    if (dom.app) dom.app.hidden = false;
    state.setup = null;
  }

  function setupFacts(view) {
    return h("dl", { class: "setup-facts" },
      h("div", null,
        h("dt", { text: t("setup.welcome.data_dir") }),
        h("dd", { class: "mono", text: view.data_dir || t("common.dash") })),
      h("div", null,
        h("dt", { text: t("setup.welcome.mailboxes") }),
        h("dd", { text: String(view.mailboxes || 0) })));
  }

  function welcomeStep(view) {
    const language = h("select", { class: "select", id: "setup-language" },
      ["auto", "en", "fr"].map((value) => h("option", {
        value,
        selected: value === state.setupDraft.language,
        text: t("setup.language." + value),
      })));
    language.addEventListener("change", () => { state.setupDraft.language = language.value; });
    const zone = h("input", {
      class: "input",
      id: "setup-zone",
      type: "text",
      value: state.setupDraft.zone,
      autocomplete: "off",
      spellcheck: "false",
    });
    zone.addEventListener("input", () => { state.setupDraft.zone = zone.value; });
    const body = h("div", { class: "setup-form" },
      setupFacts(view),
      field(t("setup.welcome.language"), language),
      field(t("setup.welcome.zone"), zone, t("setup.welcome.zone_hint")),
      h("p", { class: "fine", text: view.mailboxes ? t("setup.welcome.found") : t("setup.welcome.none") }));
    const actions = [
      button(t("setup.continue"), (event) => saveWelcome(event.currentTarget), { kind: "primary" }),
    ];
    return { body, actions };
  }

  async function saveWelcome(trigger) {
    await runAction(trigger, t("setup.saving"), async () => {
      const view = state.setup || {};
      const zone = String(state.setupDraft.zone || "").trim();
      if (zone) {
        await request("/api/learning/zone", { method: "POST", body: { zone } });
      }
      const language = state.setupDraft.language;
      if (language && language !== view.language) {
        await request("/api/settings/ui.language", { method: "PUT", body: { value: language } });
        // Apply the choice before the next step paints. The overlay is not a
        // route, so the live language helper would rebuild the screen behind
        // it; this reuses the one resolver and then lets the step repaint.
        await resolveAndApply(language);
      }
      await enterMailboxStep();
    });
  }

  async function enterMailboxStep() {
    state.setupStep = "mailbox";
    state.setupMailboxes = await loadMailboxes(true).catch(() => []);
    paintSetup();
  }

  function setupSourceSelect() {
    const sources = state.status && Array.isArray(state.status.sources) ? state.status.sources : [];
    if (sources.length === 0) return null;
    return h("select", { class: "select", id: "setup-source", "aria-label": t("setup.mailbox.source") },
      sources.map((source) => h("option", { value: source.id, text: metaLine([source.id, source.kind]) })));
  }

  function mailboxStep() {
    const boxes = Array.isArray(state.setupMailboxes) ? state.setupMailboxes : [];
    const sourceSelect = setupSourceSelect();
    const discover = sourceSelect
      ? button(t("setup.mailbox.discover"), (event) => discoverMailboxes(event.currentTarget), { icon: "search" })
      : null;
    const list = boxes.length
      ? h("div", { class: "setup-mailbox-list" }, boxes.map((box) => h("label", { class: "setup-mailbox" },
        h("input", {
          type: "checkbox",
          name: "setup-mailbox",
          value: box.key,
          checked: !!box.selected,
        }),
        h("span", { class: "setup-mailbox-name", text: box.name || box.key }),
        h("span", { class: "fine mono", text: box.key }))))
      : h("p", { class: "fine", text: sourceSelect ? t("setup.mailbox.empty") : t("setup.mailbox.no_source") });
    const body = h("div", { class: "setup-form" },
      h("p", { text: t("setup.mailbox.body") }),
      sourceSelect,
      discover,
      list);
    const actions = [
      button(t("setup.back"), () => { state.setupStep = "welcome"; paintSetup(); }),
      button(t("setup.skip"), () => { state.setupStep = "model"; paintSetup(); }),
      button(t("setup.continue"), (event) => saveMailbox(event.currentTarget), { kind: "primary" }),
    ];
    return { body, actions };
  }

  async function discoverMailboxes(trigger) {
    await runAction(trigger, t("setup.mailbox.discovering"), async () => {
      const source = document.getElementById("setup-source");
      const result = await request("/api/mailboxes/discover", {
        method: "POST",
        body: { source: source ? source.value : "" },
      });
      state.setupMailboxes = Array.isArray(result.mailboxes) ? result.mailboxes : [];
      paintSetup();
    });
  }

  async function saveMailbox(trigger) {
    await runAction(trigger, t("setup.saving"), async () => {
      const boxes = Array.isArray(state.setupMailboxes) ? state.setupMailboxes : [];
      const keys = Array.from(document.querySelectorAll('input[name="setup-mailbox"]:checked')).map((node) => node.value);
      if (boxes.length > 0 && keys.length === 0) {
        toast(t("setup.mailbox.required_title"), "warn", t("setup.mailbox.required_body"));
        return;
      }
      if (keys.length > 0) {
        await request("/api/mailboxes/pick", { method: "POST", body: { keys } });
      }
      state.setupStep = "model";
      paintSetup();
    });
  }

  function modelStep() {
    const body = h("div", { class: "setup-form" },
      h("p", { text: t("setup.model.body") }),
      h("ul", { class: "setup-list" },
        h("li", null,
          h("strong", { text: t("setup.model.local") }),
          h("span", { text: t("setup.model.local_body") })),
        h("li", null,
          h("strong", { text: t("setup.model.remote") }),
          h("span", { text: t("setup.model.remote_body") }))),
      h("p", { class: "fine", text: t("setup.model.skip") }));
    const actions = [
      button(t("setup.back"), () => { state.setupStep = "mailbox"; paintSetup(); }),
      button(t("setup.model.providers"), (event) => completeSetup(event.currentTarget, { name: "settings", params: { tab: "providers" } }), { small: true }),
      button(t("setup.continue"), () => { state.setupStep = "finish"; paintSetup(); }, { kind: "primary" }),
    ];
    return { body, actions };
  }

  function finishStep() {
    const body = h("div", { class: "setup-form" },
      h("p", { text: t("setup.finish.body") }),
      h("p", { class: "fine", text: t("setup.finish.limits") }));
    const actions = [
      button(t("setup.back"), () => { state.setupStep = "model"; paintSetup(); }),
      button(t("setup.finish.later"), (event) => completeSetup(event.currentTarget, null, true)),
      button(t("setup.finish.done"), (event) => completeSetup(event.currentTarget, null, false), { kind: "primary" }),
    ];
    return { body, actions };
  }

  function paintSetup() {
    const root = document.getElementById("setup-root");
    if (!root) return;
    const view = state.setup || {};
    const step = state.setupStep === "mailbox" ? mailboxStep()
      : state.setupStep === "model" ? modelStep()
        : state.setupStep === "finish" ? finishStep()
          : welcomeStep(view);
    const previous = document.activeElement;
    root.replaceChildren(setupShell(step.body, step.actions));
    const first = root.querySelector("input, select, button");
    if (first && (!previous || previous === document.body || root.contains(previous))) first.focus();
  }

  async function completeSetup(trigger, destination, later) {
    await runAction(trigger, t("setup.saving"), async () => {
      await request("/api/settings/ui.setup.done", { method: "PUT", body: { value: true } });
      closeSetup();
      invalidateCache(["/api/setup"]);
      if (later) toast(t("setup.later_title"), "info", t("setup.later_body"));
      if (destination) navigate(destination.name, destination.params || {});
      else navigate("hub");
    });
  }
