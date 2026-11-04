  // ---------------------------------------------------------------- language

  function localizeDocument(root = document) {
    for (const node of root.querySelectorAll("[data-i18n]")) node.textContent = t(node.dataset.i18n);
    for (const node of root.querySelectorAll("[data-i18n-aria]")) node.setAttribute("aria-label", t(node.dataset.i18nAria));
    for (const node of root.querySelectorAll("[data-i18n-title]")) node.setAttribute("title", t(node.dataset.i18nTitle));
    for (const node of root.querySelectorAll("[data-i18n-placeholder]")) node.setAttribute("placeholder", t(node.dataset.i18nPlaceholder));
  }

  function navLanguages() {
    try {
      if (Array.isArray(navigator.languages) && navigator.languages.length) return navigator.languages.slice();
      if (navigator.language) return [navigator.language];
    } catch { /* a restricted webview may not expose navigator */ }
    return [];
  }

  async function provisionalLanguage() {
    const resolved = I18N.resolve({ setting: "auto", navigatorLanguages: navLanguages() });
    await I18N.ensure(resolved.language);
    I18N.setLanguage(resolved);
    localizeDocument();
  }

  async function resolveAndApply(setting) {
    const value = setting === "en" || setting === "fr" ? setting : "auto";
    const resolved = I18N.resolve({
      setting: value,
      systemLanguage: state.systemLanguage,
      systemLocale: state.systemLocale,
      navigatorLanguages: navLanguages(),
    });
    await I18N.ensure(resolved.language);
    state.languageSetting = value;
    state.offerFrench = resolved.offerFrench;
    I18N.setLanguage(resolved);
    I18N.configure({ ownerTimeZone: state.status && state.status.time_zone ? state.status.time_zone : "" });
    localizeDocument();
    relabelConnection();
    // setHeader is the one writer of the shell's route attribute. Re-spelling
    // it here made the attribute a second fact that could name a route the
    // router had not mounted (and, with no token, one it never would).
    if (state.token && !state.shuttingDown) setHeader(state.route);
    updateLearningChrome();
    return resolved;
  }

  async function applyLanguageLive(setting) {
    // A dialog cannot be rebuilt in place in the new language: close it and
    // let the re-rendered screen take over. Its action is not applied.
    if (state.modalOpen && state.modalCloseFn) state.modalCloseFn();
    await resolveAndApply(setting);
    invalidateRouteCache();
    paintJobStrip();
    // The route identity did not change, but every node built with the old
    // catalog is now wrong. Force the mounted body through a full rebuild.
    if (state.token && !state.shuttingDown) renderRoute(true);
    else if (!state.token) showTokenError();
  }

  async function readLanguageSetting() {
    try {
      const view = await requestCached("/api/settings/ui.language", {}, CACHE_TTL.settings);
      return view && (view.value === "en" || view.value === "fr") ? view.value : "auto";
    } catch {
      return null;
    }
  }

  async function saveLanguageSetting(value, trigger) {
    await withBusy(trigger, t("settings.applying"), async () => {
      try {
        const updated = await request("/api/settings/ui.language", { method: "PUT", body: { value } });
        invalidateSettingsReads();
        applyLanguageLive(updated && (updated.value === "en" || updated.value === "fr") ? updated.value : value);
      } catch (error) { reportError(error); }
    });
  }

  function languageOfferNode() {
    if (!state.offerFrench || state.languageSetting !== "auto" || I18N.snapshot().language !== "en") return null;
    return warningBanner(h("div", null,
        h("strong", { text: t("language.offer_title") }),
        h("p", { text: t("language.offer_body") }),
        h("div", { class: "button-row" },
          button(t("language.offer_action"), async (event) => {
            await saveLanguageSetting("fr", event.currentTarget);
          }, { small: true, kind: "primary" }))));
  }

