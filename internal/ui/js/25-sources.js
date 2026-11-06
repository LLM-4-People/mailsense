  // ---------------------------------------------------------------- sources

  async function renderSources(route, seq) {
    // Five independent reads: the screen paints once all have settled, but
    // it no longer pays extra round trips in series. The job snapshot goes
    // through loadJobs (the only job merger); the permission refresh is
    // settled so a failed gate cannot reject the reads that did succeed, and
    // the held-removal list is a supplementary read with its own inline error.
    const [status, mailboxes, jobsRead, gateRead, heldRead] = await Promise.all([
      loadStatus(),
      loadMailboxes(),
      settleRead(loadJobs()),
      readGate(),
      settleRead(readHeldRemovals()),
    ]);
    if (seq !== state.renderSeq) return h("div");
    const sources = Array.isArray(status.sources) ? status.sources : [];
    const selectedSource = route.params.get("source") || (sources[0] ? sources[0].id : "");
    const selected = mailboxes.filter((box) => box.selected).length;
    // A failed refresh leaves the shell's last known snapshot in place; only
    // a gate this render cannot vouch for withholds the permission pill.
    const permissionKnown = !!gateRead.value || gateKnown();

    setTopbarMeta(
      pill(t("sources.mailbox_count", { count: mailboxes.length })),
      pill(t("sources.picked_count", { count: selected }), selected ? "done" : "pending"),
      permissionKnown ? gatePill(gateOpen()) : null);

    const discoverSelect = h("select", { class: "select compact", "aria-label": t("sources.discover_aria"), disabled: sources.length === 0 },
      sources.length ? sources.map((source) => h("option", { value: source.id, selected: source.id === selectedSource, text: source.display_name || source.id })) : h("option", { text: t("sources.no_source_registered") }));
    // The route parameter is the one owner of the chosen source: a rebuild
    // after Refresh or Discover must not silently fall back to the first
    // source, and the backfill label and request must agree.
    discoverSelect.addEventListener("change", () => navigate("sources", { source: discoverSelect.value }));
    const discoverButton = button(t("sources.discover"), async (event) => {
      await withBusy(event.currentTarget, t("sources.discovering"), async () => {
        try {
          const result = await request("/api/mailboxes/discover", { method: "POST", body: { source: discoverSelect.value } });
          state.mailboxes = Array.isArray(result.mailboxes) ? result.mailboxes : [];
          toast(t("sources.discovery_finished"), "info", t("sources.discovery_finished_body", { count: state.mailboxes.length }));
          await afterBackgroundAction(["discovery"]);
        } catch (error) { reportActionError(error); }
      });
    }, { icon: "search", kind: "primary", disabled: sources.length === 0 });
    const refreshButton = button(t("common.refresh"), async (event) => {
      await runAction(event.currentTarget, t("sources.refreshing"), async () => {
        invalidateCache(["/api/learning", "/api/hub", "/api/status", "/api/mailboxes", "/api/gate"]);
        invalidateRouteCache("sources", "hub", "mail");
        await loadLearning(true);
        await refreshActiveRoute();
      });
    }, { icon: "refresh" });

    const toolbar = h("div", { class: "toolbar" }, discoverSelect, discoverButton,
      syncNowButton(discoverSelect, sources.length > 0), refreshButton,
      h("span", { class: "fine", text: t("sources.discovery_note") }));

    const jobs = Array.isArray(jobsRead.value) ? jobsRead.value : [];
    const htmlJobs = jobs.filter((job) => job.kind === "messages.html");
    const htmlJob = htmlJobs.length ? htmlJobs[htmlJobs.length - 1] : null;
    // The one job this screen can report: a sync or a display-HTML backfill
    // that failed. Its detail is the job id in Logs, the only screen that
    // holds the retained entries.
    const failedJob = jobs
      .filter((job) => job && job.status === "failed" && (job.kind === "sync" || job.kind === "messages.html"))
      .sort((a, b) => String(b.finished_at || "").localeCompare(String(a.finished_at || "")))[0] || null;

    const mailboxTable = mailboxes.length
      ? sortableTable({
        caption: t("sources.mailboxes.caption"),
        wrapperClass: "sources-mailbox-table-wrap",
        className: "sources-mailbox-table",
        columns: mailboxColumns(),
        rows: mailboxes,
        persistKey: "sources.mailboxes",
      }).node
      : emptyState(t("sources.empty.title"), sources.length
        ? t("sources.empty.body")
        : t("sources.empty.no_source"),
        sources.length ? [discoverButton] : null, "sources");

    const registry = sources.length
      ? sortableTable({
        caption: t("sources.registry.caption"),
        wrapperClass: "sources-registry-wrap",
        className: "sources-registry-table",
        columns: [
          {
            key: "source",
            label: t("sources.table.source"),
            type: "text",
            value: (source) => source.id || "",
            render: (source) => h("span", { class: "mono", text: source.id }),
          },
          {
            key: "kind",
            label: t("sources.table.kind"),
            type: "text",
            value: (source) => source.kind || "",
            render: (source) => source.kind || t("common.dash"),
          },
        ],
        rows: sources,
        persistKey: "sources.registry",
      }).node
      : emptyState(t("sources.registry.empty_title"), t("sources.registry.empty_body"), null, "sources");

    const held = heldRead.value && Array.isArray(heldRead.value.folders) ? heldRead.value.folders : [];
    return h("div", { class: "screen sources-screen" },
      h("div", { class: "route-scroll section-scroll", dataset: { scrollKey: "sources-body" } },
        section(t("sources.section.discover.title"), t("sources.section.discover.sub"), null, toolbar, sourcesCrossLinks(failedJob)),
        section(t("sources.section.mailboxes.title"), t("sources.section.mailboxes.sub"), null, mailboxTable),
        heldRemovalsSection(held, heldRead.error),
        htmlBackfillSection(htmlJob, selectedSource, jobsRead.error),
        section(t("sources.registry.title"), t("sources.registry.sub"), null, registry)));
  }

  // sourcesCrossLinks is the one row of links out of Sources: the filters that
  // decide what syncs, the failure that stopped it, and the learning it feeds.
  // Each link carries the route parameters that land on the exact row or job.
  function sourcesCrossLinks(failedJob) {
    return fragment(
      h("div", { class: "button-row" },
        navigateWith(t("sources.link.sync_settings"), "settings", { tab: "general", q: "sync." }, { small: true }),
        navigateWith(t("sources.link.outlook_filters"), "settings", { tab: "general", q: "outlook." }, { small: true }),
        navigateWith(t("nav.learning"), "learning", {}, { small: true })),
      failedJobNote(failedJob));
  }

  // failedJobNote names the failed sync or backfill and links to the job in
  // Logs. The job screen does not exist yet, so the link filters the Logs
  // screen by the job id the job manager writes on every entry.
  function failedJobNote(job) {
    if (!job || !job.id) return null;
    return h("p", { class: "fine" },
      text(jobSummaryName(job) + " "),
      navigateWith(t("sources.link.failed_job"), "logs", { job: job.id }, { bare: true }));
  }

  // readHeldRemovals is intentionally uncached: the list is small, it is
  // fetched on Sources mount, and the sync/confirmation paths refresh the
  // route so the owner never acts on a stale held count.
  function readHeldRemovals() {
    return request("/api/held-removals");
  }

  // heldRemovalsSection is the one confirmation card for removals a sync held
  // back. An empty list renders nothing; a failed supplementary read keeps
  // its retry instead of painting an empty card.
  function heldRemovalsSection(folders, error) {
    if (error) {
      return section(t("sources.held.title"), null, null,
        errorState(error, null, Object.assign({}, INLINE_READ_ERROR, {
          title: t("read.held_removals_unavailable"),
        })));
    }
    if (!folders.length) return null;
    return section(t("sources.held.title"), null, null,
      h("div", { class: "held-removals" }, folders.map(heldRemovalRow)));
  }

  function heldRemovalRow(folder) {
    const held = Number(folder && folder.held) || 0;
    const folderID = folder && folder.folder_id != null ? String(folder.folder_id) : "";
    const path = folder && folder.path ? folder.path : t("common.dash");
    // applies_at is the deadline a later sync applies the whole held set,
    // computed from sync.removal.auto_apply_after. The card states that
    // deadline instead of promising the messages stay; an absent or
    // unreadable value is the confirm-only policy the server reports with an
    // omitted field.
    const appliesAt = parseTime(folder && folder.applies_at);
    const action = button(t("sources.held.confirm"), (event) => confirmHeldRemoval(folder, event.currentTarget), {
      small: true,
      kind: "danger",
    });
    action.id = "held-removal-confirm-" + folderID;
    action.dataset.heldFolder = folderID;
    return h("div", { class: "held-removal", dataset: { heldFolder: folderID } },
      h("div", { class: "held-removal-copy" },
        h("p", {
          class: "held-removal-body",
          text: t("sources.held.body", { count: held, held, path }),
        }),
        h("p", {
          class: "held-removal-applies",
          text: appliesAt
            ? t("sources.held.applies", { when: formatDate(folder.applies_at) })
            : t("sources.held.applies_confirm"),
        }),
        h("p", {
          class: "held-removal-meta",
          text: t("sources.held.meta", {
            stored: Number(folder && folder.stored) || 0,
            count: Number(folder && folder.stored) || 0,
            since: formatDate(folder && folder.since),
          }),
        })),
      action);
  }

  // syncNowButton is the one manual sync trigger the Hub copy and the
  // sync.auto setting description promise. Without it, a picked mailbox whose
  // first sync was cancelled or failed cannot be retried when auto sync is
  // off.
  function syncNowButton(discoverSelect, enabled) {
    return button(t("sources.sync.now"), async (event) => {
      const source = discoverSelect.value;
      if (!source) {
        toast(t("sources.no_source_available"), "warn", t("sources.no_source_available_body"));
        return;
      }
      await withBusy(event.currentTarget, t("sources.syncing"), async () => {
        try {
          await request("/api/actions/sync", {
            method: "POST",
            body: { source },
            timeoutMs: LONG_REQUEST_TIMEOUT_MS,
          });
          toast(t("sources.sync.started"), "info", t("sources.sync.started_body"));
          await afterBackgroundAction(["job"]);
        } catch (error) { reportActionError(error); }
      });
    }, { icon: "mail", kind: "secondary", disabled: !enabled });
  }

  // confirmHeldRemoval names the folder and its count in the existing danger
  // dialog, then refreshes the held list through the graph. A not_found means
  // another writer already removed the folder: refresh before reporting it.
  async function confirmHeldRemoval(folder, trigger) {
    const held = Number(folder && folder.held) || 0;
    const folderID = folder && folder.folder_id != null ? String(folder.folder_id) : "";
    const path = folder && folder.path ? folder.path : t("common.dash");
    const ok = await confirmDialog(
      t("sources.held.confirm"),
      t("sources.held.confirm_body", { count: held, held, path }),
      t("sources.held.confirm"),
      true,
    );
    if (!ok) {
      if (trigger && trigger.isConnected) trigger.focus({ preventScroll: true });
      return;
    }
    await withBusy(trigger, t("sources.held.confirming"), async () => {
      try {
        const result = await request("/api/held-removals/" + encodeURIComponent(folderID) + "/confirm", {
          method: "POST",
          body: {},
        });
        const removal = result && result.removal ? result.removal : {};
        toast(t("sources.held.done"), "info", t("sources.held.result", {
          deleted: Number(removal.deleted) || 0,
          moved: Number(removal.moved) || 0,
        }));
        await afterBackgroundAction(["mail", "heldRemovals"], { hard: true });
        focusHeldRemoval(folderID);
      } catch (error) {
        if (error instanceof ApiError && error.code === "not_found") {
          invalidateTopics(["heldRemovals"], { hard: true });
          await refreshActiveRoute();
        }
        reportActionError(error);
        focusHeldRemoval(folderID);
      }
    });
  }

  // focusHeldRemoval keeps the keyboard on the row that opened the dialog.
  // The row is gone after its folder is confirmed, so the next held row takes
  // focus, and the stable main landmark is the last fallback.
  function focusHeldRemoval(folderID) {
    const same = document.getElementById("held-removal-confirm-" + folderID);
    if (same && same.isConnected && dom.main.contains(same)) {
      same.focus({ preventScroll: true });
      return;
    }
    const next = dom.main.querySelector("button[data-held-folder]");
    if (next) {
      next.focus({ preventScroll: true });
      return;
    }
    dom.main.focus({ preventScroll: true });
  }

  // htmlBackfillSection is the owner-triggered one-time re-read of display
  // HTML for messages stored before it was captured. The job is the ordinary
  // jobs.Manager job (kind messages.html); progress and cancellation use the
  // existing /api/jobs endpoints. Plain text remains the learning body.
  function htmlBackfillSection(job, sourceID, jobsError) {
    const running = !!job && job.status === "running";
    const progress = job && job.progress ? job.progress : null;
    const total = progress ? Number(progress.total) || 0 : 0;
    const done = progress ? Number(progress.done) || 0 : 0;
    const start = button(t("sources.backfill.start"), async (event) => {
      await withBusy(event.currentTarget, t("sources.backfill.starting"), async () => {
        try {
          // The rendered label and the request read the same route-owned
          // value, so they cannot name different sources.
          await request("/api/actions/backfill_html", {
            method: "POST",
            body: { source: sourceID || "" },
            timeoutMs: LONG_REQUEST_TIMEOUT_MS,
          });
          toast(t("sources.backfill.started"), "info", t("sources.backfill.started_body"));
          await afterBackgroundAction(["job"]);
        } catch (error) { reportActionError(error); }
      });
    }, { icon: "refresh", kind: "primary", disabled: running || !sourceID });
    const cancel = running ? button(t("sources.backfill.cancel", { source: jobNameLabel(job.name || job.kind || job.id) }), async (event) => {
      await cancelJob(job.id, event.currentTarget);
      await afterBackgroundAction(["job"]);
    }, { small: true, kind: "quiet" }) : null;
    // The meter reads the same job name and progress sentence as the shell
    // strip; only its host and the live progress element are local.
    const meter = job ? h("div", { class: "backfill-progress", dataset: { backfillJob: String(job.id) } },
      h("div", { class: "meter-top" },
        h("span", { dataset: { backfillName: "" }, text: jobSummaryName(job) }),
        h("strong", { dataset: { backfillCount: "" }, text: total > 0 ? jobProgressText(done, total, "messages") : "" })),
      total > 0 ? h("progress", { dataset: { backfillProgress: "" }, max: Math.max(total, 1), value: Math.min(done, Math.max(total, 1)), "aria-label": t("sources.backfill.progress_aria") }) : null,
      progress && progress.message
        ? diagnosticDetails(progress.message, { summary: "sources.backfill.details",
            paragraphClass: "fine", dataset: { backfillMessage: "" } })
        : null,
      job.error ? h("p", { class: "field-error", lang: "en", text: job.error }) : null) : null;
    return section(t("sources.backfill.title"), t("sources.backfill.sub"),
      null,
      h("div", { class: "backfill" },
        h("p", { class: "fine", text: t("sources.backfill.source", { source: sourceID || t("sources.backfill.source_none") }) }),
        h("div", { class: "button-row" }, start, cancel),
        jobsError ? errorState(jobsError, null,
          Object.assign({}, INLINE_READ_ERROR, { title: t("read.jobs_unavailable") })) : meter));
  }

  // updateBackfillMeter paints a live backfill progress event into the meter
  // that already exists. The learning screen owns stage progress the same
  // way: one node per fact, patched instead of rebuilding the route.
  function updateBackfillMeter(info) {
    const meter = document.querySelector('[data-backfill-job="' + String(info.id) + '"]');
    if (!meter) return;
    const progress = info.progress || {};
    const total = Number(progress.total) || 0;
    const done = Number(progress.done) || 0;
    const name = meter.querySelector("[data-backfill-name]");
    if (name) name.textContent = jobSummaryName(info);
    const bar = meter.querySelector("[data-backfill-progress]");
    if (bar) {
      bar.max = Math.max(total, 1);
      bar.value = Math.min(done, Math.max(total, 1));
    }
    const count = meter.querySelector("[data-backfill-count]");
    if (count) {
      count.textContent = total > 0 ? jobProgressText(done, total, "messages") : "";
    }
    const message = meter.querySelector("[data-backfill-message]");
    if (message && progress.message) message.textContent = progress.message;
    // The disclosure is created only when the first progress event carries a
    // diagnostic; a later event without one leaves the last sentence in
    // place.
  }

  // mailbox-subtitle:start
  // The Sources cell is two lines: the mailbox name (or its record key) and
  // `source_id · key`, joined with the shell's one metadata separator. The raw
  // Exchange EntryID stays in the API payload and in the search store filter;
  // it is never rendered in the table.

  function mailboxTitle(box) {
    return box.name || box.key || "";
  }

  function mailboxSubtitleParts(box) {
    const parts = [];
    if (box.source_id) parts.push({ text: String(box.source_id), mono: false });
    if (box.key) parts.push({ text: String(box.key), mono: true });
    return parts;
  }

  // mailbox-subtitle:end

  // mailbox-row:start
  // syncMailboxPickLabel derives the checkbox name from its current state.
  // The old node computed it once, so a focused box kept saying "Unpick"
  // after a cancel restored it to picked.
  function syncMailboxPickLabel(input, box) {
    input.setAttribute("aria-label", input.checked
      ? t("sources.unpick_aria", { mailbox: mailboxTitle(box) })
      : t("sources.pick_aria", { mailbox: mailboxTitle(box) }));
  }

  function mailboxPickNode(box) {
    const input = h("input", {
      type: "checkbox",
      checked: !!box.selected,
      onchange: async (event) => {
        const input = event.currentTarget;
        syncMailboxPickLabel(input, box);
        if (!input.checked) {
          // Unpick has one owner: the confirmed action shared with every
          // other unpick entry point. A cancelled confirmation restores the
          // box instead of leaving the row half-changed.
          if (!await unpickMailbox(box.key, input)) {
            input.checked = true;
            syncMailboxPickLabel(input, box);
          }
          return;
        }
        input.disabled = true;
        try {
          const result = await request("/api/mailboxes/pick", { method: "POST", body: { keys: [box.key] } });
          state.mailboxes = Array.isArray(result.mailboxes) ? result.mailboxes : [];
          toast(t("sources.picked_toast"), "info", mailboxTitle(box));
          await afterBackgroundAction(["selection"], { hard: true });
        } catch (error) {
          input.checked = false;
          reportActionError(error);
        } finally {
          if (input.isConnected) input.disabled = false;
          syncMailboxPickLabel(input, box);
        }
      },
    });
    syncMailboxPickLabel(input, box);
    return input;
  }

  function mailboxNameCellNode(box) {
    const subtitle = mailboxSubtitleParts(box);
    return h("div", null,
      h("div", { class: "row-title", text: mailboxTitle(box) }),
      subtitle.length
        ? h("div", { class: "secondary" }, subtitle.map((part, index) => [
          index ? text(metaSeparator()) : null,
          part.mono ? h("span", { class: "mono", text: part.text }) : text(part.text),
        ]))
        : null);
  }

  function mailboxStateBadges(box) {
    return h("div", { class: "chips" },
      box.error ? badge(t("sources.state.offline"), "danger", box.error || "") : badge(t("sources.state.online"), "ok"),
      box.cached ? badge(t("sources.state.cached")) : null,
      box.default ? badge(t("sources.state.default")) : null,
      box.discovered ? null : badge(t("sources.state.not_discovered"), "warn"),
      box.selected ? badge(t("sources.state.picked"), "accent") : null);
  }

  function mailboxStateSortValue(box) {
    return [
      box.error ? t("sources.state.offline") : t("sources.state.online"),
      box.cached ? t("sources.state.cached") : "",
      box.default ? t("sources.state.default") : "",
      box.discovered ? "" : t("sources.state.not_discovered"),
      box.selected ? t("sources.state.picked") : "",
    ].filter(Boolean).join(" ");
  }

  function mailboxIdentityCellNode(box) {
    const reported = box.identity && Array.isArray(box.identity.addresses) ? box.identity.addresses : [];
    const declared = Array.isArray(box.declared) ? box.declared : [];
    const identityChips = [];
    for (const address of reported) identityChips.push(h("span", { class: "chip", text: address }));
    for (const address of declared) {
      identityChips.push(h("span", { class: "chip" },
        text(address),
        h("button", { type: "button", "aria-label": t("sources.identity.remove_aria", { address }), title: t("sources.identity.remove_title"), onclick: async (event) => {
          const target = event.currentTarget;
          target.disabled = true;
          try {
            const result = await request("/api/mailboxes/undeclare", { method: "POST", body: { key: box.key, address } });
            state.mailboxes = Array.isArray(result.mailboxes) ? result.mailboxes : [];
            toast(t("sources.identity.removed"), "info", address);
            await afterBackgroundAction(["discovery", "gate"], { hard: true });
          } catch (error) { target.disabled = false; reportActionError(error); }
        } }, icon("close", "small"))));
    }
    if (!reported.length && !declared.length) identityChips.push(badge(t("sources.identity.none"), "warn"));
    const declare = button(t("sources.identity.declare"), () => declareDialog(box.key), { small: true, icon: "plus" });
    return h("div", { class: "mailbox-identity" },
      h("div", { class: "chips" }, identityChips),
      h("div", { class: "button-row" }, declare));
  }

  function mailboxIdentitySortValue(box) {
    const reported = box.identity && Array.isArray(box.identity.addresses) ? box.identity.addresses : [];
    const declared = Array.isArray(box.declared) ? box.declared : [];
    return reported.concat(declared).join(" ");
  }

  function mailboxColumns() {
    return [
      {
        key: "selected",
        label: t("sources.table.picked"),
        type: "text",
        sortable: false,
        render: (box) => mailboxPickNode(box),
      },
      {
        key: "mailbox",
        label: t("sources.table.mailbox"),
        type: "text",
        value: (box) => mailboxTitle(box),
        render: (box) => mailboxNameCellNode(box),
      },
      {
        key: "kind",
        label: t("sources.table.kind"),
        type: "text",
        value: (box) => box.kind || "",
        render: (box) => box.kind || t("common.dash"),
      },
      {
        key: "folders",
        label: t("sources.table.folders"),
        type: "number",
        align: "num",
        value: (box) => box.folders >= 0 ? box.folders : "",
        render: (box) => box.folders >= 0 ? formatNumber(box.folders) : t("common.dash"),
      },
      {
        key: "messages",
        label: t("sources.table.messages"),
        type: "number",
        align: "num",
        value: (box) => box.items >= 0 ? box.items : "",
        render: (box) => box.items >= 0 ? formatNumber(box.items) : t("common.dash"),
      },
      {
        key: "state",
        label: t("sources.table.state"),
        type: "text",
        value: (box) => mailboxStateSortValue(box),
        render: (box) => mailboxStateBadges(box),
      },
      {
        key: "owner",
        label: t("sources.table.owner"),
        type: "text",
        value: (box) => mailboxIdentitySortValue(box),
        render: (box) => mailboxIdentityCellNode(box),
      },
    ];
  }
  // mailbox-row:end

  async function declareDialog(key) {
    const input = h("input", { class: "input", type: "email", autocomplete: "email", placeholder: "you@example.com", required: true });
    const errorLine = h("div", { class: "field-error", role: "alert", hidden: true });
    const body = h("div", null,
      h("p", { text: t("sources.declare.body") }),
      h("p", { class: "mono fine", text: key }),
      field(t("sources.declare.address"), input),
      errorLine);
    input.addEventListener("input", () => markFieldError(input, errorLine, ""));
    const ok = await openDialog({
      title: t("sources.declare.title"),
      body,
      confirm: t("sources.declare.confirm"),
      onConfirm: async () => {
        const address = input.value.trim();
        if (!address) {
          markFieldError(input, errorLine, t("sources.declare.required"));
          input.focus();
          return false;
        }
        if (typeof input.checkValidity === "function" && !input.checkValidity()) {
          markFieldError(input, errorLine, t("sources.declare.invalid"));
          input.focus();
          return false;
        }
        markFieldError(input, errorLine, "");
        try {
          const result = await request("/api/mailboxes/declare", { method: "POST", body: { key, address } });
          state.mailboxes = Array.isArray(result.mailboxes) ? result.mailboxes : [];
          toast(t("sources.identity.declared"), "info", address);
          return true;
        } catch (error) { reportActionError(error); return false; }
      },
    });
    if (ok) await afterBackgroundAction(["discovery", "gate"], { hard: true });
  }

  // ------------------------------------------------- source readiness (setup)
  // The first-run mailbox step and the Sources screen share the label and
  // readiness projection: the API owns the configured name and the live probe,
  // so the UI never rebuilds either from the stable id.

  function setupSources(value) {
    return Array.isArray(value && value.sources) ? value.sources : [];
  }

  function setupMailboxHint(sources, mailboxes) {
    const boxes = Array.isArray(mailboxes && mailboxes.mailboxes) ? mailboxes.mailboxes : [];
    if (boxes.length) return null;
    return h("p", { class: "fine", text: setupSources(sources).length ? t("setup.mailbox.empty") : t("setup.mailbox.no_source") });
  }

  function setupSourceName(source) {
    if (!source) return "";
    return source.display_name || source.kind || source.id || "";
  }

  function setupSourceReason(source) {
    if (!source) return "";
    if (source.cause && I18N.has("api.source_cause." + source.cause)) {
      return t("api.source_cause." + source.cause);
    }
    return source.detail || t("setup.mailbox.source_unavailable_generic");
  }

  function setupSelectedSource(sources) {
    const select = document.getElementById("setup-source");
    if (!select) return null;
    return sources.find((source) => source.id === select.value) || null;
  }

  function setupSourceSelect(value) {
    const sources = setupSources(value);
    if (sources.length === 0) return null;
    const usable = sources.find((source) => source.available !== false);
    const wanted = state.setupDraft.source;
    const selected = sources.find((source) => source.id === wanted && source.available !== false) || usable || sources[0];
    const select = h("select", {
      class: "select",
      id: "setup-source",
      "aria-label": t("setup.mailbox.source"),
      disabled: !usable,
    }, sources.map((source) => h("option", {
      value: source.id,
      selected: source.id === selected.id,
      disabled: source.available === false,
      text: setupSourceName(source),
    })));
    select.addEventListener("change", () => {
      state.setupDraft.source = select.value;
    });
    return h("div", { class: "setup-source" },
      select,
      sources.filter((source) => source.profile).map((source) => h("p", {
        class: "fine setup-source-state",
        text: t("setup.mailbox.source_profile", { name: setupSourceName(source), profile: source.profile }),
      })),
      sources.filter((source) => source.available === false).map((source) => h("p", {
        class: "fine setup-source-state",
        text: t("setup.mailbox.source_unavailable", { name: setupSourceName(source), reason: setupSourceReason(source) }),
      })));
  }

  function setupSourceActions(value) {
    const sources = setupSources(value);
    const selected = setupSelectedSource(sources);
    const usable = selected && selected.available !== false ? selected : sources.find((source) => source.available !== false);
    const first = selected || sources[0];
    const actions = [
      button(t("setup.mailbox.discover"), (event) => discoverMailboxes(event.currentTarget), { icon: "search", disabled: !usable }),
    ];
    if (!usable && first) {
      if (Array.isArray(first.actions) && first.actions.includes("start")) {
        actions.push(button(t("setup.mailbox.start_source", { name: setupSourceName(first) }),
          (event) => startSetupSource(first, event.currentTarget)));
      }
      actions.push(button(t("setup.mailbox.check_source"), (event) => checkSetupSource(event.currentTarget)));
    }
    return h("div", { class: "button-row" }, actions);
  }

  async function reloadSetupSources() {
    setSetupReadValue("sources", await request("/api/sources"));
  }

  async function startSetupSource(source, trigger) {
    await runSetupAction(trigger, t("setup.mailbox.starting_source", { name: setupSourceName(source) }), async () => {
      await request(`/api/sources/${encodeURIComponent(source.id)}/start`, { method: "POST" });
      toast(t("setup.mailbox.source_started", { name: setupSourceName(source) }), "info", t("setup.mailbox.source_started_body"));
      await reloadSetupSources();
    });
  }

  async function checkSetupSource(trigger) {
    await runSetupAction(trigger, t("setup.mailbox.checking_source"), reloadSetupSources);
  }

