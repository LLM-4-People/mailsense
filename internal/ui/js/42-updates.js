  // ---------------------------------------------------------------- updates
  //
  // One owner for the update surface: the Settings Updates tab reads the
  // persisted status, and the first-open What's New overlay consumes the same
  // state. The server owns the check, the skip marker and the seen marker, so
  // the client keeps no cache copy of its own.

  function updateStatusOf(info) {
    return info && typeof info === "object" && !Array.isArray(info) ? info : null;
  }

  // A local Check/Skip owns the next paint. The SSE event it causes must not
  // start a second refresh that replaces the control under the keyboard; the
  // short window covers a marker event that arrives just after the response.
  const UPDATE_ACTION_QUIET_MS = 1000;
  let updateActionQuietUntil = 0;

  // applyUpdateStatus is the one write of the status snapshot. It returns the
  // accepted object so a caller can tell a malformed event from a real one.
  function applyUpdateStatus(info) {
    const next = updateStatusOf(info);
    if (next) state.update = next;
    syncUpdateBadge();
    return next;
  }

  // syncUpdateBadge keeps the one persistent affordance honest: the Settings
  // nav dot is the only update signal that survives dismissing the overlay.
  function syncUpdateBadge() {
    const show = !!(state.update && state.update.available && !state.update.skipped);
    const dot = document.getElementById("nav-settings-update");
    const label = document.getElementById("nav-settings-update-text");
    if (dot) {
      dot.hidden = !show;
      dot.title = show ? t("update.whats_new.eyebrow") : "";
    }
    if (label) {
      label.hidden = !show;
      label.textContent = show ? t("update.whats_new.eyebrow") : "";
    }
  }

  async function loadUpdateStatus() {
    return applyUpdateStatus(await request("/api/update"));
  }

  function updateCheckedText(info) {
    return info && info.checked_at ? formatDate(info.checked_at) : t("settings.update.never_checked");
  }

  function updateHeadline(info) {
    if (!info) return t("settings.update.not_checked");
    if (info.skipped && info.latest) return t("settings.update.skipped", { version: info.latest });
    if (info.available && info.latest) return t("settings.update.available", { version: info.latest });
    if (info.checked_at) return t("settings.update.up_to_date");
    return t("settings.update.not_checked");
  }

  function updateFacts(info) {
    if (!info) return null;
    return kv([
      [t("settings.update.current"), info.current || t("common.dash")],
      info.latest ? [t("settings.update.latest"), info.latest] : null,
      [t("settings.update.last_checked"), updateCheckedText(info)],
    ]);
  }

  // inlineReleaseNodes renders the small inline subset the release body uses:
  // bold, inline code and links (as their label only). Every value becomes a
  // text node, never HTML.
  function inlineReleaseNodes(value) {
    const frag = document.createDocumentFragment();
    const pattern = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)]+\))/g;
    const source = String(value || "");
    let at = 0;
    let match;
    while ((match = pattern.exec(source))) {
      if (match.index > at) frag.append(document.createTextNode(source.slice(at, match.index)));
      const token = match[0];
      if (token.startsWith("**")) frag.append(h("strong", { text: token.slice(2, -2) }));
      else if (token.startsWith("`")) frag.append(h("code", { text: token.slice(1, -1) }));
      else frag.append(document.createTextNode(token.replace(/^\[([^\]]+)\]\([^)]+\)$/, "$1")));
      at = match.index + token.length;
    }
    if (at < source.length) frag.append(document.createTextNode(source.slice(at)));
    return frag;
  }

  // releaseNotesBody turns the bounded Markdown release body into headings,
  // paragraphs and bullets. It is deliberately not a general Markdown engine:
  // the note is display copy, and no remote markup becomes DOM.
  function releaseNotesBody(value) {
    const root = h("div", { class: "update-notes-body" });
    let paragraph = [];
    let list = null;
    const flushParagraph = () => {
      if (!paragraph.length) return;
      root.append(h("p", null, inlineReleaseNodes(paragraph.join(" "))));
      paragraph = [];
    };
    const closeList = () => { list = null; };
    for (const raw of String(value || "").split(/\r?\n/)) {
      const line = raw.trim();
      if (!line) {
        flushParagraph();
        closeList();
        continue;
      }
      const heading = line.match(/^(#{1,6})\s+(.+)$/);
      if (heading) {
        flushParagraph();
        closeList();
        root.append(h(heading[1].length <= 2 ? "h3" : "h4", { text: heading[2] }));
        continue;
      }
      const bullet = line.match(/^[-*]\s+(.+)$/);
      if (bullet) {
        flushParagraph();
        if (!list) {
          list = h("ul");
          root.append(list);
        }
        list.append(h("li", null, inlineReleaseNodes(bullet[1])));
        continue;
      }
      closeList();
      paragraph.push(line);
    }
    flushParagraph();
    if (!root.children.length) root.append(h("p", { text: String(value || "") }));
    return root;
  }

  function updateNotes(info) {
    if (!info || !info.available || !info.notes) return null;
    return h("details", { class: "update-notes" },
      h("summary", { text: t("settings.update.notes") }),
      releaseNotesBody(info.notes));
  }

  async function checkForUpdates(trigger) {
    const hadFocus = document.activeElement === trigger;
    updateActionQuietUntil = Date.now() + UPDATE_ACTION_QUIET_MS;
    await withBusy(trigger, t("settings.update.checking"), async () => {
      try {
        // The server bounds one check at 30 s; the client waits a little
        // longer so a slow upstream answer is shown, not aborted early.
        applyUpdateStatus(await request("/api/update/check", {
          method: "POST", body: {}, timeoutMs: 60000,
        }));
        await refreshActiveRoute();
      } catch (error) {
        reportActionError(error);
      }
    });
    if (hadFocus) {
      const next = document.getElementById("update-check");
      if (next) next.focus({ preventScroll: true });
    }
  }

  async function skipUpdate(trigger, version) {
    if (!version) return;
    const hadFocus = document.activeElement === trigger;
    updateActionQuietUntil = Date.now() + UPDATE_ACTION_QUIET_MS;
    await withBusy(trigger, t("settings.update.skipping"), async () => {
      try {
        applyUpdateStatus(await request("/api/update/skip", {
          method: "POST", body: { version },
        }));
        await refreshActiveRoute();
      } catch (error) {
        reportActionError(error);
      }
    });
    if (hadFocus) {
      // The skipped action is gone after the refresh; Check now is the next
      // useful control in the same card.
      const next = document.getElementById("update-check");
      if (next) next.focus({ preventScroll: true });
    }
  }

  // updatesPanel is the Updates tab: the facts, the skip marker, the release
  // notes and the three update settings the tab owns.
  function updatesPanel(info, settings, status) {
    const readError = info && info.read_error;
    const data = readError ? null : (updateStatusOf(info) || state.update);
    const failedText = data && data.error_code
      ? t("settings.update.failed", { message: data.last_error || data.error_code })
      : "";
    const headline = readError ? t("settings.update.unavailable")
      : failedText && !data.available ? failedText : updateHeadline(data);
    const headlineWarn = !!readError || !!(failedText && data && !data.available);
    const views = (settings && Array.isArray(settings.settings) ? settings.settings : [])
      .filter((view) => view && view.group === SETTINGS_UPDATES_GROUP);
    const group = views.length
      ? settingsGroupNode(t("settings.update.settings_title"), views, status, { count: false, tab: "updates" })
      : null;
    const check = button(t("settings.update.check"), (event) => checkForUpdates(event.currentTarget), { icon: "refresh", small: true });
    check.id = "update-check";
    const skip = data && data.available && data.latest && !data.skipped
      ? button(t("settings.update.skip"), (event) => skipUpdate(event.currentTarget, data.latest), { small: true })
      : null;
    if (skip) skip.id = "update-skip";
    const card = h("div", { class: "update-status" },
      h("p", { class: "update-headline" + (headlineWarn ? " warn-text" : ""), text: headline }),
      updateFacts(data),
      readError ? h("p", { class: "fine warn-text", text: apiFailure(readError).message }) : null,
      failedText && data.available ? h("p", { class: "fine warn-text", text: failedText }) : null,
      skip ? h("div", { class: "button-row" }, skip) : null,
      updateNotes(data));
    return settingsList(h("div", { class: "update-panel" },
      section(t("settings.update.title"), t("settings.update.sub"), check, card, group)));
  }

  // markUpdateSeen is best-effort: a failed marker must not trap the owner in
  // a dialog, and the next launch offers the notes again rather than claiming
  // they were seen.
  async function markUpdateSeen(version) {
    if (!version) return;
    try {
      applyUpdateStatus(await request("/api/update/seen", { method: "POST", body: { version } }));
    } catch { /* the panel still shows the release */ }
  }

  // maybeOpenWhatsNew is the one first-open trigger. It never fights setup or
  // another modal, and one version is offered at most once per session.
  function maybeOpenWhatsNew() {
    const info = updateStatusOf(state.update);
    if (!info || !info.available || !info.latest || !info.notes) return;
    if (info.notes_seen || info.skipped) return;
    if (state.shuttingDown || state.modalOpen || document.getElementById("setup-root") || document.querySelector("dialog[open]")) return;
    if (state.whatsNewPrompted === info.latest) return;
    state.whatsNewPrompted = info.latest;
    let confirmed = false;
    const body = h("div", null,
      releaseNotesBody(info.notes),
      h("p", { class: "fine update-current", text: t("update.whats_new.current", { current: info.current || t("common.dash") }) }));
    openDialog({
      title: t("update.whats_new.title", { version: info.latest }),
      eyebrow: t("update.whats_new.eyebrow"),
      body,
      confirm: t("update.whats_new.got_it"),
      cancel: t("modal.close"),
      onConfirm: async () => {
        confirmed = true;
        await markUpdateSeen(info.latest);
        return true;
      },
    }).then(() => {
      // Closing with Later, Escape or the header X still counts as seen: the
      // notes were shown. Confirm already wrote the same marker.
      if (!confirmed) void markUpdateSeen(info.latest);
    });
  }

  // onUpdateState is the one SSE consumer: the persisted status is the read
  // model, and a mounted Updates tab re-reads after a background check.
  function onUpdateState(info) {
    if (!applyUpdateStatus(info)) return;
    if (state.route === "settings" && state.settingsTab === "updates" && !state.modalOpen && Date.now() > updateActionQuietUntil) {
      void refreshActiveRoute();
    }
    maybeOpenWhatsNew();
  }

  // handleUpdateStateEvent parses the one SSE payload. Keeping it outside
  // connectEvents keeps that dispatcher inside its reviewed function budget.
  function handleUpdateStateEvent(event) {
    let status;
    try { status = JSON.parse(event.data); } catch { return; }
    onUpdateState(status);
  }

  // A boot dialog (the timezone confirmation) can hold the modal while the
  // update lands. Any dialog close retries the one overlay once.
  function scheduleWhatsNewAfterDialog() {
    window.setTimeout(() => maybeOpenWhatsNew(), 0);
  }
