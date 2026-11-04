  // ------------------------------------------------------------ job strip
  // state.jobs is the one job model of the shell. jobs.update carries a full
  // jobs.Info snapshot, so the strip merges it without a fetch; /api/jobs
  // seeds the model at boot, after an event gap and when Sources needs an
  // authoritative snapshot. One aggregate meter sums the running work; a
  // finished job lingers briefly in the summary so a fast run is still
  // visible; a failure stays until dismissed.
  let jobRemovalTimers = new Map();
  let jobStripLanguage = "";
  let jobPaintQueued = false;

  function jobLinger(info) {
    // A failure is the one result the owner must act on: it stays visible
    // with its cause until dismissed, instead of vanishing after ten seconds
    // with no message.
    return info.status === "failed" ? Infinity : JOB_STRIP_LINGER_MS;
  }

  function jobVisible(info) {
    if (!info || !info.id) return false;
    if (info.status === "running") return true;
    const finished = Date.parse(info.finished_at || "");
    return Number.isFinite(finished) && Date.now() - finished < jobLinger(info);
  }

  // jobNameLabel is the owner-facing name of a producer's job name. The
  // server keeps stable names; the catalog owns their translation, and an
  // unknown source id is humanized rather than shown raw.
  function jobNameLabel(name) {
    const raw = String(name || "").trim();
    if (!raw) return "";
    const key = "jobs.name." + raw.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
    if (I18N.has(key)) return t(key);
    return raw.replace(/[._-]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
  }

  function forgetJob(id) {
    const timer = jobRemovalTimers.get(id);
    if (timer) {
      window.clearTimeout(timer);
      jobRemovalTimers.delete(id);
    }
    state.jobs.delete(id);
  }

  function announceJob(info, previous) {
    if (!dom.jobLive) return;
    const name = jobNameLabel(info.name || info.kind || info.id);
    if (!previous) {
      dom.jobLive.textContent = t("shell.job.announce_started", { name });
      return;
    }
    if (previous.status === info.status) return;
    if (info.status === "running") {
      dom.jobLive.textContent = t("shell.job.announce_started", { name });
      return;
    }
    const key = info.status === "failed" ? "shell.job.announce_failed"
      : info.status === "cancelled" ? "shell.job.announce_cancelled"
      : "shell.job.announce_done";
    dom.jobLive.textContent = t(key, { name });
  }

  function upsertJob(info, announce = true) {
    if (!info || !info.id) return;
    if (announce) announceJob(info, state.jobs.get(info.id));
    state.jobs.set(info.id, info);
    const previous = jobRemovalTimers.get(info.id);
    if (previous) {
      window.clearTimeout(previous);
      jobRemovalTimers.delete(info.id);
    }
    if (info.status !== "running") {
      const finished = Date.parse(info.finished_at || "") || Date.now();
      const wait = jobLinger(info) - (Date.now() - finished);
      // A failure has no expiry: it is dismissed by the owner, never by a
      // timer. setTimeout(Infinity) would fire at once, so schedule nothing.
      if (!Number.isFinite(wait)) {
        scheduleJobPaint();
        return;
      }
      jobRemovalTimers.set(info.id, window.setTimeout(() => {
        jobRemovalTimers.delete(info.id);
        const current = state.jobs.get(info.id);
        if (current && current.status === info.status && current.finished_at === info.finished_at) {
          state.jobs.delete(info.id);
          scheduleJobPaint();
        }
      }, wait + 50));
    }
    scheduleJobPaint();
  }

  async function loadJobs(force = false) {
    const view = await requestCached("/api/jobs", { force }, CACHE_TTL.jobs);
    const list = view && Array.isArray(view.jobs) ? view.jobs : [];
    for (const info of list) {
      // A snapshot is not a transition: the strip shows it, but the live
      // region only announces events that happen while the shell is open.
      if (jobVisible(info)) upsertJob(info, false);
    }
    // The snapshot is authoritative. A job that finished while the stream
    // was down has no terminal event, so its removal timer was never armed;
    // keeping it would leave a permanent "running" entry.
    const present = new Set(list.map((info) => String(info && info.id ? info.id : "")));
    for (const id of [...state.jobs.keys()]) {
      if (present.has(String(id))) continue;
      const timer = jobRemovalTimers.get(id);
      if (timer) {
        window.clearTimeout(timer);
        jobRemovalTimers.delete(id);
      }
      state.jobs.delete(id);
    }
    scheduleJobPaint();
    return list;
  }

  // jobSnapshot is the model's read surface for screens. They paint the same
  // map the strip does and never issue their own /api/jobs read; a screen
  // that genuinely needs a fresh authoritative snapshot awaits loadJobs(),
  // the only merger and the only place that removes a finished job.
  function jobSnapshot() {
    return Array.from(state.jobs.values());
  }

  async function cancelJob(id, trigger) {
    await withBusy(trigger, t("shell.job.cancelling"), async () => {
      try {
        const info = await request("/api/jobs/" + encodeURIComponent(id) + "/cancel", { method: "POST", body: {} });
        if (info && info.id) upsertJob(info);
      } catch (error) { reportActionError(error); }
    });
  }

  // jobAggregate folds the visible jobs into the one top-level meter. Only
  // running jobs contribute: a finished, failed or cancelled job lingers in
  // the summary text but must not pull the running bar backwards. The value
  // is weighted by each producer's total, exactly
  //   sum(min(max(done, 0), total)) / sum(total)
  // over the running jobs that report total > 0. With no such job the meter
  // is indeterminate. The overall ETA is the longest known remaining time:
  // parallel jobs are done when the last one is.
  function jobAggregate(jobs) {
    const list = Array.isArray(jobs) ? jobs : [];
    const running = list.filter((info) => info && info.status === "running");
    let done = 0;
    let total = 0;
    let allCounted = true;
    const units = new Set();
    for (const info of running) {
      const progress = info.progress || {};
      const jobTotal = Number(progress.total) || 0;
      if (!(jobTotal > 0)) {
        allCounted = false;
        continue;
      }
      done += Math.min(Math.max(Number(progress.done) || 0, 0), jobTotal);
      total += jobTotal;
      if (progress.unit) units.add(String(progress.unit));
    }
    let eta = 0;
    let etaKnown = running.length > 0;
    for (const info of running) {
      const value = Number(info.progress && info.progress.eta) || 0;
      if (!(value > 0)) {
        etaKnown = false;
        break;
      }
      eta = Math.max(eta, value);
    }
    return {
      running,
      done,
      total,
      indeterminate: running.length > 0 && total <= 0,
      percent: total > 0 ? done / total : null,
      // A unit only names the aggregate when every running job reports the
      // same one; mixed units would make the summed counts a false claim.
      unit: allCounted && units.size === 1 ? [...units][0] : "",
      eta: etaKnown ? eta : 0,
      etaKnown: etaKnown && eta > 0,
    };
  }

  // jobPauseNote names the ceiling that stopped a running job. A paused job
  // is still running: it keeps its place in the aggregate and its Cancel
  // action, and the note rides with its own name.
  function jobPauseNote(info) {
    const paused = info && info.paused;
    if (!info || info.status !== "running" || !paused || !paused.resource) return "";
    return t("jobs.paused", {
      name: jobNameLabel(info.name || info.kind || info.id),
      resource: resourceName(paused.resource),
      percent: paused.percent,
      limit: paused.limit,
    });
  }

  function jobSummaryName(info) {
    const name = jobNameLabel(info.name || info.kind || info.id);
    if (info.status === "running") return jobPauseNote(info) || name;
    if (info.status === "failed" && info.error) {
      // The producer's redacted error is the only cause the owner can see;
      // the strip must not report a failure with no reason. It is bounded:
      // a provider or connector failure can carry a multi-kilobyte URL, and
      // an unbounded line would grow the fixed strip until it squeezed the
      // route out of the window.
      return t("shell.job.failed_with_reason", { name, reason: boundedReason(info.error) });
    }
    return name + " (" + statusLabel(info.status) + ")";
  }

  // JOB_REASON_MAX_CHARS bounds the cause shown in the fixed strip. The full
  // text stays in the Logs screen, where the error was recorded.
  const JOB_REASON_MAX_CHARS = 200;

  function boundedReason(reason) {
    const plain = String(reason || "").replace(/\s+/g, " ").trim();
    const runes = Array.from(plain);
    return runes.length <= JOB_REASON_MAX_CHARS
      ? plain
      : runes.slice(0, JOB_REASON_MAX_CHARS).join("") + "…";
  }

  // jobProgressText is the one done/total sentence of the job strip and any
  // screen that mirrors a job. The unit is named only when the producer gave
  // one.
  function jobProgressText(done, total, unit = "") {
    return t("shell.job.progress", { done: formatNumber(done), total: formatNumber(total) }) +
      (unit ? " " + measureUnit(unit, total) : "");
  }

  function jobSummaryNode() {
    return h("div", { class: "job-summary" },
      h("div", { class: "job-summary-line" },
        h("span", { class: "job-summary-lead" }),
        h("span", { class: "job-summary-sep", "aria-hidden": "true" }, text(metaSeparator())),
        h("span", { class: "job-summary-names" }),
        h("span", { class: "job-summary-metrics-sep", "aria-hidden": "true" }, text(metaSeparator())),
        h("span", { class: "job-summary-metrics" })),
      h("progress", { class: "job-aggregate", max: 1 }),
      h("div", { class: "job-actions" }));
  }

  function jobActionLabel(info, kind) {
    const name = jobNameLabel(info.name || info.kind || info.id);
    return kind === "cancel"
      ? t("shell.job.cancel_job", { name })
      : t("shell.job.dismiss_job", { name });
  }

  function updateJobAction(action, info, kind) {
    const label = jobActionLabel(info, kind);
    action.setAttribute("aria-label", label);
    action.title = label;
    const textNode = action.querySelector("span");
    // withBusy owns the label while a cancel is in flight; a progress tick
    // must not replace "Cancelling…" with the idle text.
    if (textNode && !action.disabled) textNode.textContent = label;
  }

  function jobActionButton(info, kind) {
    const id = String(info.id);
    const action = kind === "cancel"
      ? button(t("common.cancel"), (event) => cancelJob(id, event.currentTarget), { small: true, kind: "quiet" })
      : button(t("common.dismiss"), () => {
        forgetJob(id);
        scheduleJobPaint();
      }, { small: true, kind: "quiet" });
    action.classList.add("job-action");
    action.dataset.jobId = id;
    action.dataset.action = kind;
    updateJobAction(action, info, kind);
    return action;
  }

  function paintJobActions(host, jobs) {
    const wanted = jobs.filter((info) => info.status === "running" || info.status === "failed");
    const keep = new Set();
    for (const info of wanted) {
      const id = String(info.id);
      const kind = info.status === "running" ? "cancel" : "dismiss";
      keep.add(id);
      let action = null;
      for (const node of host.children) {
        if (node.dataset.jobId === id && node.dataset.action === kind) {
          action = node;
          break;
        }
      }
      if (!action) {
        for (const node of Array.from(host.children)) {
          if (node.dataset.jobId === id) node.remove();
        }
        action = jobActionButton(info, kind);
        host.append(action);
      } else {
        updateJobAction(action, info, kind);
      }
    }
    for (const node of Array.from(host.children)) {
      if (!keep.has(node.dataset.jobId)) node.remove();
    }
    host.hidden = wanted.length === 0;
  }

  function paintJobSummary(summary, jobs) {
    const aggregate = jobAggregate(jobs);
    const lead = summary.querySelector(".job-summary-lead");
    const names = summary.querySelector(".job-summary-names");
    const metrics = summary.querySelector(".job-summary-metrics");
    const metricsSep = summary.querySelector(".job-summary-metrics-sep");
    const meter = summary.querySelector(".job-aggregate");
    const actions = summary.querySelector(".job-actions");
    lead.textContent = aggregate.running.length
      ? t("shell.job.active", { count: aggregate.running.length })
      : t("shell.job.background");
    names.textContent = jobs.map(jobSummaryName).join(", ");
    const parts = [];
    if (aggregate.total > 0) {
      parts.push(jobProgressText(aggregate.done, aggregate.total, aggregate.unit));
      parts.push(formatPercent(aggregate.percent));
    }
    if (aggregate.etaKnown) parts.push(t("common.eta", { duration: formatDuration(aggregate.eta) }));
    // A starting job with no total has no measurable work yet, but it is
    // still running: the summary says so instead of rendering an empty line.
    if (!parts.length && aggregate.running.length) parts.push(t("shell.job.running"));
    metrics.textContent = metaLine(parts);
    metrics.hidden = parts.length === 0;
    metricsSep.hidden = parts.length === 0;
    meter.hidden = aggregate.running.length === 0;
    if (meter.hidden) {
      meter.removeAttribute("value");
    } else if (aggregate.indeterminate) {
      meter.max = 1;
      meter.removeAttribute("value");
      meter.setAttribute("aria-label", t("shell.job.aggregate_aria", { count: aggregate.running.length }));
    } else {
      meter.max = Math.max(aggregate.total, 1);
      meter.value = Math.min(aggregate.done, aggregate.total);
      meter.setAttribute("aria-label", t("shell.job.aggregate_aria", { count: aggregate.running.length }));
    }
    paintJobActions(actions, jobs);
  }

  // A progress event arrives after every embed batch (8 messages by default)
  // and after every triage/judge message. Painting synchronously rebuilt the
  // whole strip per event; one frame of coalescing keeps the latest state
  // without the repeated layout.
  function scheduleJobPaint() {
    if (jobPaintQueued) return;
    jobPaintQueued = true;
    const run = () => {
      jobPaintQueued = false;
      paintJobStrip();
    };
    if (typeof window.requestAnimationFrame === "function") window.requestAnimationFrame(run);
    else window.setTimeout(run, 16);
  }

  function paintJobStrip() {
    if (!dom.jobStrip) return;
    const language = I18N.snapshot().language;
    if (language !== jobStripLanguage) {
      jobStripLanguage = language;
      clear(dom.jobStrip);
    }
    const jobs = jobSnapshot().filter(jobVisible);
    jobs.sort((a, b) => {
      const left = String(a.started_at || "");
      const right = String(b.started_at || "");
      if (left !== right) return left < right ? -1 : 1;
      return String(a.id) < String(b.id) ? -1 : 1;
    });
    dom.jobStrip.hidden = jobs.length === 0;
    if (!jobs.length) {
      clear(dom.jobStrip);
      return;
    }
    let summary = dom.jobStrip.querySelector(".job-summary");
    if (!summary) {
      summary = jobSummaryNode();
      dom.jobStrip.append(summary);
    }
    paintJobSummary(summary, jobs);
  }
