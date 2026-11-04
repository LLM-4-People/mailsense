  // ---------------------------------------------------------------- companies
  //
  // The explorer is a two-pane read of the same company truth the Ask company
  // result names: a ranked list and one deep profile. Company keys and thread
  // state enums never reach the DOM; labels and states always come from the
  // catalogs.

  // companies-view:start
  function companySearchText(value) {
    const raw = String(value == null ? "" : value).trim();
    try {
      return raw.normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLocaleLowerCase();
    } catch {
      return raw.toLocaleLowerCase();
    }
  }

  function companyDomains(value) {
    const source = value && Array.isArray(value.domains) ? value.domains : [];
    return source.map((domain) => String(domain == null ? "" : domain).trim()).filter(Boolean);
  }

  function companyRowLabel(value) {
    const label = String(value && value.label ? value.label : "").trim();
    if (label) return label;
    const domains = companyDomains(value);
    return domains.length ? domains[0] : t("companies.unknown_label");
  }

  function currentCompanyMonth() {
    const now = new Date();
    return String(now.getFullYear()) + "-" + String(now.getMonth() + 1).padStart(2, "0");
  }

  // companyMonthSeries always returns COMPANIES_MONTHS ascending buckets. The
  // service already zero-fills its window, but the explorer keeps that
  // invariant locally too so a sparse or older response still charts twelve
  // honest months.
  function companyMonthSeries(months, endMonth = "") {
    const source = Array.isArray(months) ? months : [];
    const byMonth = new Map();
    const pattern = /^\d{4}-(0[1-9]|1[0-2])$/;
    const count = (value) => {
      const parsed = Number(value);
      return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
    };
    for (const row of source) {
      const month = String(row && row.month ? row.month : "");
      if (!pattern.test(month)) continue;
      byMonth.set(month, {
        month,
        messages: count(row.messages),
        sent: count(row.sent),
        received: count(row.received),
      });
    }
    const end = pattern.test(String(endMonth || "")) ? String(endMonth) : currentCompanyMonth();
    let year = Number(end.slice(0, 4));
    let month = Number(end.slice(5, 7));
    const series = [];
    for (let index = 0; index < COMPANIES_MONTHS; index += 1) {
      const key = String(year) + "-" + String(month).padStart(2, "0");
      series.unshift(byMonth.get(key) || { month: key, messages: 0, sent: 0, received: 0 });
      month -= 1;
      if (month === 0) {
        month = 12;
        year -= 1;
      }
    }
    return series;
  }

  // companyThreadRank is the only company-specific thread helper: the state
  // vocabulary and labels are the shared detail.thread.state keys.
  function companyThreadRank(value) {
    const index = THREAD_STATES.indexOf(threadStateKey(value));
    return index === -1 ? THREAD_STATES.length : index;
  }

  // companyOpenCounts is the one arithmetic over the six-state map. The
  // profile reports the full counts even when the open list is capped, so the
  // ledger can state what it is not showing.
  function companyOpenCounts(profile) {
    const counts = profile && profile.thread_states && typeof profile.thread_states === "object"
      ? profile.thread_states : {};
    const count = (state) => Math.max(0, Number(counts[state]) || 0);
    const owner = count("awaiting_owner");
    const other = count("awaiting_other");
    const unknown = count("unknown");
    return { owner, other, unknown, total: owner + other + unknown };
  }

  // companySortedOpenThreads is newest-first within the state priority the
  // store and the profile already use.
  function companySortedOpenThreads(profile) {
    return (Array.isArray(profile && profile.open_threads) ? profile.open_threads.slice() : [])
      .sort((left, right) => {
        const rank = companyThreadRank(left && left.state) - companyThreadRank(right && right.state);
        if (rank !== 0) return rank;
        return String(right && right.last_at || "").localeCompare(String(left && left.last_at || ""));
      });
  }

  // companyNextAction derives one next step from the facts already on the
  // profile. It never predicts, scores or invents an obligation: the first
  // matching rule wins and every branch is backed by a stored field.
  function companyNextAction(profile) {
    const threads = companySortedOpenThreads(profile);
    const ownerReply = threads.find((entry) => entry && entry.state === "awaiting_owner");
    if (ownerReply) return { kind: "reply", thread: ownerReply };
    const deadlines = (Array.isArray(profile && profile.deadlines) ? profile.deadlines.slice() : [])
      .sort((left, right) => String(left && left.due || "").localeCompare(String(right && right.due || "")));
    if (deadlines.length) return { kind: "deadline", deadline: deadlines[0] };
    const events = (Array.isArray(profile && profile.events) ? profile.events.slice() : [])
      .sort((left, right) => String(left && left.start || "").localeCompare(String(right && right.start || "")));
    if (events.length) return { kind: "meeting", event: events[0] };
    const waiting = threads.find((entry) => entry && entry.state === "awaiting_other");
    if (waiting) return { kind: "waiting", thread: waiting };
    const recent = (Array.isArray(profile && profile.recent) ? profile.recent.slice() : [])
      .sort((left, right) => String(right && right.instant || "").localeCompare(String(left && left.instant || "")));
    const sent = Math.max(0, Number(profile && profile.sent) || 0);
    const received = Math.max(0, Number(profile && profile.received) || 0);
    if (sent === 0 && received > 0) {
      return { kind: "one_way_in", message: recent.find((item) => item && !item.sent) || recent[0] || null };
    }
    if (received === 0 && sent > 0) {
      return { kind: "one_way_out", message: recent.find((item) => item && item.sent) || recent[0] || null };
    }
    const lastInbound = String(profile && profile.last_inbound_at || "");
    const lastOutbound = String(profile && profile.last_outbound_at || "");
    if (lastOutbound && (!lastInbound || lastOutbound > lastInbound)) {
      return { kind: "awaiting_reply", message: recent.find((item) => item && item.sent) || recent[0] || null };
    }
    if (recent.length) return { kind: "read", message: recent[0] };
    return { kind: "start" };
  }

  // companyThreadReasonLabel names the resolver's evidence for an open
  // thread. An unknown reason stays empty instead of printing a raw enum.
  function companyThreadReasonLabel(value) {
    switch (String(value == null ? "" : value)) {
      case "inbound_last": return t("companies.reason.inbound_last");
      case "owner_last": return t("companies.reason.owner_last");
      case "visibility_gap": return t("companies.reason.visibility_gap");
      case "auto_reply": return t("companies.reason.auto_reply");
      case "no_reply_expected": return t("companies.reason.no_reply_expected");
      case "cc_only": return t("companies.reason.cc_only");
      default: return "";
    }
  }
  // companies-view:end

  // companies-render:start
  function companySectionNode(id, title, sub, ...body) {
    return h("section", { class: "companies-section", "aria-labelledby": id },
      sectionHead(title, { id, sub, level: "h3", className: "companies-section-head" }),
      ...body);
  }

  // companyIntelligenceSection states only what the profile already proves:
  // the date span, the direction of the last message, the 90-day activity and
  // the open-loop counts. No score, prediction or generated summary enters
  // this block.
  function companyIntelligenceSection(profile) {
    const messages = Math.max(0, Number(profile.messages) || 0);
    const sent = Math.max(0, Number(profile.sent) || 0);
    const received = Math.max(0, Number(profile.received) || 0);
    const activity = profile.last_90_days && typeof profile.last_90_days === "object"
      ? profile.last_90_days : {};
    const facts = [];
    if (!messages) {
      facts.push(t("companies.brief.no_mail"));
    } else {
      facts.push(t("companies.brief.span", {
        messages: formatNumber(messages),
        count: messages,
        sent: formatNumber(sent),
        received: formatNumber(received),
        since: profile.first_at ? formatDate(profile.first_at, { year: false, time: false }) : t("common.dash"),
      }));
      if (Number(activity.messages) > 0) {
        facts.push(t("companies.brief.activity", {
          messages: formatNumber(Number(activity.messages) || 0),
          count: Number(activity.messages) || 0,
          days: formatNumber(Number(activity.active_days) || 0),
          threads: formatNumber(Number(activity.threads) || 0),
        }));
      }
      if (profile.last_inbound_at && profile.last_outbound_at) {
        facts.push(t("companies.brief.direction", {
          inbound: formatRelative(profile.last_inbound_at),
          outbound: formatRelative(profile.last_outbound_at),
        }));
      } else if (profile.last_inbound_at) {
        facts.push(t("companies.brief.inbound_only", { when: formatRelative(profile.last_inbound_at) }));
      } else if (profile.last_outbound_at) {
        facts.push(t("companies.brief.outbound_only", { when: formatRelative(profile.last_outbound_at) }));
      }
      const open = companyOpenCounts(profile);
      facts.push(open.total
        ? t("companies.brief.open", {
          owner: formatNumber(open.owner),
          other: formatNumber(open.other + open.unknown),
        })
        : t("companies.brief.no_open"));
    }
    return companySectionNode(
      "companies-intelligence-title",
      t("companies.section.intelligence"),
      t("companies.intelligence.sub"),
      h("ul", { class: "companies-brief-list" }, facts.map((fact) => h("li", { text: fact }))),
      companyNextActionNode(profile));
  }

  // companyNextActionNode renders the one deterministic next step. A branch
  // without a stored message id stays text-only; it never invents a link.
  function companyNextActionNode(profile) {
    const action = companyNextAction(profile);
    let actionText = "";
    let meta = "";
    let href = "";
    switch (action.kind) {
      case "reply": {
        const thread = action.thread || {};
        const subject = thread.subject || t("mail.no_subject");
        actionText = t("companies.next.reply", { subject });
        meta = thread.actor
          ? t("companies.next.reply_meta", { name: thread.actor, when: formatRelative(thread.last_at) })
          : t("companies.next.reply_meta_anon", { when: formatRelative(thread.last_at) });
        if (thread.message_id) href = mailHref(thread.message_id, "important");
        break;
      }
      case "deadline": {
        const deadline = action.deadline || {};
        actionText = t("companies.next.deadline", { subject: deadline.subject || t("mail.no_subject") });
        meta = t("companies.next.due", { when: formatRelative(deadline.due) });
        if (deadline.message_id) href = mailHref(deadline.message_id, "important");
        break;
      }
      case "meeting": {
        const event = action.event || {};
        actionText = t("companies.next.meeting", { subject: event.subject || t("mail.no_subject") });
        meta = t("companies.next.meeting_meta", { when: formatDate(event.start, { year: false }) });
        break;
      }
      case "waiting": {
        const thread = action.thread || {};
        actionText = t("companies.next.waiting", { subject: thread.subject || t("mail.no_subject") });
        meta = t("companies.next.waiting_meta", { when: formatRelative(thread.last_at) });
        if (thread.message_id) href = mailHref(thread.message_id, "important");
        break;
      }
      case "one_way_in":
      case "one_way_out":
      case "awaiting_reply":
      case "read": {
        const message = action.message || {};
        const subject = message.subject || t("mail.no_subject");
        const sender = message.sender_name || message.sender_address || t("mail.sender_unknown");
        if (action.kind === "one_way_in") {
          actionText = t("companies.next.one_way_in", { subject });
          meta = t("companies.next.message_meta", { sender, when: formatRelative(message.instant) });
        } else if (action.kind === "one_way_out") {
          actionText = t("companies.next.one_way_out", { subject });
          meta = t("companies.next.sent_meta", { when: formatRelative(message.instant) });
        } else if (action.kind === "awaiting_reply") {
          actionText = t("companies.next.awaiting_reply", { subject });
          meta = t("companies.next.sent_meta", { when: formatRelative(message.instant) });
        } else {
          actionText = t("companies.next.read", { subject });
          meta = t("companies.next.message_meta", { sender, when: formatRelative(message.instant) });
        }
        if (message.message_id) href = mailHref(message.message_id, "important");
        break;
      }
      default:
        actionText = t("companies.next.start");
    }
    return h("div", { class: "companies-next" },
      h("div", { class: "companies-next-icon" }, icon("arrow-right", "small")),
      h("div", { class: "companies-next-body" },
        h("p", { class: "companies-next-label", text: t("companies.next.label") }),
        h("p", { class: "companies-next-text" },
          href
            ? h("a", { href, text: actionText })
            : h("span", { text: actionText })),
        meta ? h("p", { class: "companies-next-meta", text: meta }) : null));
  }

  function companyKpis(profile) {
    return statStrip([
      { label: t("companies.kpi.messages"), value: formatNumber(profile.messages) },
      { label: t("companies.kpi.sent"), value: formatNumber(profile.sent) },
      { label: t("companies.kpi.received"), value: formatNumber(profile.received) },
      { label: t("companies.kpi.threads"), value: formatNumber(profile.threads) },
      {
        label: t("companies.kpi.last_contact"),
        value: profile.last_at ? formatDate(profile.last_at, { time: false }) : t("common.dash"),
        title: formatDate(profile.last_at),
      },
    ], { className: "companies-kpis", role: "group", ariaLabel: t("companies.kpi.aria"), note: t("companies.kpi.all_time") });
  }

  function companyVolumeSection(profile) {
    // The server owns the month window; anchor the local series to its last
    // bucket so a browser in another time zone cannot drop or add a month.
    const months = Array.isArray(profile.months) ? profile.months : [];
    const lastMonth = months.length ? months[months.length - 1].month : "";
    const series = companyMonthSeries(months, lastMonth);
    const total = series.reduce((sum, month) => sum + month.messages, 0);
    const max = Math.max(1, ...series.map((month) => month.messages));
    const bars = h("div", {
      class: "companies-chart",
      role: "img",
      "aria-label": total
        ? t("companies.chart.aria", { months: COMPANIES_MONTHS, messages: formatNumber(total) })
        : t("companies.chart.empty"),
    }, series.map((month) => {
      const height = month.messages > 0 ? Math.max(2, Math.round((month.messages / max) * 100)) : 0;
      const sent = month.messages > 0 ? Math.min(100, Math.round((month.sent / month.messages) * 100)) : 0;
      const received = month.messages > 0 ? Math.min(100 - sent, Math.round((month.received / month.messages) * 100)) : 0;
      return h("div", { class: "companies-chart-col" },
        h("div", {
          class: "companies-bar-track",
          title: t("companies.chart.bar", {
            month: formatMonth(month.month),
            sent: formatNumber(month.sent),
            received: formatNumber(month.received),
          }),
        },
        h("div", { class: "companies-bar", style: "height:" + String(height) + "%" },
          h("span", { class: "companies-bar-sent", style: "height:" + String(sent) + "%" }),
          h("span", { class: "companies-bar-received", style: "height:" + String(received) + "%" }))));
    }));
    const legend = h("div", { class: "companies-chart-legend" },
      h("span", { class: "companies-legend-item" },
        h("span", { class: "companies-swatch sent", "aria-hidden": "true" }),
        text(t("companies.kpi.sent"))),
      h("span", { class: "companies-legend-item" },
        h("span", { class: "companies-swatch received", "aria-hidden": "true" }),
        text(t("companies.kpi.received"))));
    const axis = h("div", { class: "companies-chart-axis", "aria-hidden": "true" },
      h("span", { text: formatMonth(series[0].month) }),
      h("span", { text: formatMonth(series[series.length - 1].month) }));
    return companySectionNode(
      "companies-volume-title",
      t("companies.section.volume"),
      t("companies.section.volume_sub", { months: COMPANIES_MONTHS }),
      legend,
      bars,
      axis,
      total ? null : h("p", { class: "fine", text: t("companies.chart.empty") }));
  }

  function companyThreadRow(entry, side) {
    const subject = entry.subject || t("mail.no_subject");
    const meta = [];
    if (entry.topic) meta.push(subjectPathLabel(entry.topic, entry.subtopic));
    if (entry.actor) meta.push(t("companies.thread.actor", { name: entry.actor }));
    const reason = companyThreadReasonLabel(entry.reason);
    if (reason) meta.push(reason);
    if (Number(entry.messages) > 0) {
      meta.push(t("companies.thread.messages", { count: Number(entry.messages) }));
    }
    if (entry.last_at) meta.push(t("detail.thread.last_activity", { when: formatRelative(entry.last_at) }));
    return h("a", {
      class: "companies-loop-row " + side,
      href: entry.message_id ? mailHref(entry.message_id, "important") : undefined,
      title: entry.last_at ? formatDate(entry.last_at) : "",
      "aria-label": t("companies.thread.open_aria", { subject }),
    },
    h("span", { class: "companies-loop-main" },
      h("span", { class: "companies-loop-subject", dir: "auto", text: subject }),
      meta.length ? h("span", { class: "companies-loop-meta", text: metaLine(meta) }) : null));
  }

  function companyThreadsSection(profile) {
    const counts = profile.thread_states && typeof profile.thread_states === "object" ? profile.thread_states : {};
    const stateGrid = h("div", { class: "companies-state-grid", role: "group", "aria-label": t("companies.threads.states_aria") },
      THREAD_STATES.map((state) => h("div", { class: "companies-state" },
        h("span", { class: "companies-state-label", text: threadStateLabel(state) }),
        h("strong", { class: "companies-state-count", text: formatNumber(Number(counts[state]) || 0) }))));
    const threads = companySortedOpenThreads(profile);
    const open = companyOpenCounts(profile);
    const ownerThreads = threads.filter((entry) => entry && entry.state === "awaiting_owner");
    const waitingThreads = threads.filter((entry) => entry && (entry.state === "awaiting_other" || entry.state === "unknown"));
    const column = (key, title, sub, items, empty, count) => h("div", { class: "companies-loop-col " + key },
      h("div", { class: "companies-loop-head" },
        h("h4", { text: title }),
        h("span", { class: "companies-loop-count", text: formatNumber(count) })),
      h("p", { class: "companies-loop-sub", text: sub }),
      items.length
        ? h("div", { class: "companies-loop-list" }, items.map((entry) => companyThreadRow(entry, key)))
        : h("p", { class: "companies-none", text: empty }));
    const ledger = h("div", { class: "companies-ledger" },
      column("owner", t("companies.ledger.owner"), t("companies.ledger.owner_sub"), ownerThreads, t("companies.ledger.owner_empty"), open.owner),
      column("waiting", t("companies.ledger.waiting"), t("companies.ledger.waiting_sub"), waitingThreads, t("companies.ledger.waiting_empty"), open.other + open.unknown));
    const truncated = open.total > threads.length
      ? h("p", {
        class: "fine companies-truncation",
        text: t("companies.threads.showing", {
          shown: formatNumber(threads.length),
          total: formatNumber(open.total),
        }),
      })
      : null;
    return companySectionNode(
      "companies-threads-title",
      t("companies.section.threads"),
      t("companies.threads.sub"),
      stateGrid,
      ledger,
      truncated);
  }

  function companyRecentSection(profile) {
    const items = Array.isArray(profile.recent) ? profile.recent : [];
    const list = items.length
      ? h("div", { class: "companies-recent-list" }, items.map((item) => {
        const subject = item.subject || t("mail.no_subject");
        const sender = item.sender_name || item.sender_address || t("mail.sender_unknown");
        return h("a", {
          class: "companies-recent-row",
          href: mailHref(item.message_id, "important"),
          "aria-label": t("companies.recent.open_aria", { subject, sender }),
        },
        h("span", { class: "companies-recent-main" },
          h("span", { class: "companies-recent-subject", dir: "auto", text: subject }),
          h("span", { class: "companies-recent-from", dir: "auto", text: sender })),
        h("span", { class: "companies-recent-side" },
          item.has_attachments ? badge(t("companies.recent.attachment"), "plain") : null,
          badge(item.sent ? t("detail.thread.sent") : t("detail.thread.received"), item.sent ? "accent" : "plain"),
          h("span", {
            class: "companies-recent-date",
            title: formatDate(item.instant),
            text: formatRelative(item.instant),
          })));
      }))
      : h("p", { class: "companies-none", text: t("companies.recent.empty") });
    const truncated = Math.max(0, Number(profile.recent_total) || 0) > items.length
      ? h("p", {
        class: "fine companies-truncation",
        text: t("companies.list.showing", {
          shown: formatNumber(items.length),
          total: formatNumber(Number(profile.recent_total) || 0),
        }),
      })
      : null;
    return companySectionNode(
      "companies-recent-title",
      t("companies.section.recent"),
      t("companies.recent.sub"),
      list,
      truncated);
  }

  function companyPeopleSection(profile) {
    const people = Array.isArray(profile.people) ? profile.people : [];
    if (!people.length) {
      return companySectionNode(
        "companies-people-title",
        t("companies.section.people"),
        t("companies.people.sub"),
        h("p", { class: "companies-none", text: t("companies.people.empty") }));
    }
    const personRole = (person) => metaLine([person.department, person.job_title]
      .map((part) => String(part == null ? "" : part).trim()));
    const table = sortableTable({
      caption: t("companies.people.caption"),
      wrapperClass: "companies-table-wrap",
      className: "companies-people-table",
      columns: [
        {
          key: "name",
          label: t("companies.table.name"),
          type: "text",
          value: (person) => person.name || person.address || t("common.unknown"),
          render: (person) => {
            const name = person.name || person.address || t("common.unknown");
            return fragment(
              h("span", { class: "row-title", dir: "auto", text: name }),
              person.declared ? badge(t("companies.badge.address_book"), "plain") : null);
          },
        },
        {
          key: "address",
          label: t("companies.table.address"),
          type: "email",
          value: (person) => person.address || "",
          render: (person) => h("span", {
            class: "companies-person-address",
            dir: "auto",
            text: person.address || t("common.dash"),
          }),
        },
        {
          key: "phone",
          label: t("companies.table.phone"),
          type: "phone",
          value: (person) => person.phone || "",
          render: (person) => h("span", {
            class: "companies-person-phone",
            dir: "auto",
            text: person.phone || t("common.dash"),
          }),
        },
        {
          key: "messages",
          label: t("companies.table.messages"),
          type: "number",
          align: "num",
          value: (person) => person.messages,
          render: (person) => {
            const messages = Math.max(0, Number(person.messages) || 0);
            if (!messages && !person.last_at) {
              return badge(t("companies.person.no_mail"), "plain");
            }
            return fragment(
              h("span", { class: "companies-person-count", text: formatNumber(messages) }),
              h("span", {
                class: "companies-person-direction",
                text: t("companies.person.direction", {
                  sent: formatNumber(Math.max(0, Number(person.sent) || 0)),
                  received: formatNumber(Math.max(0, Number(person.received) || 0)),
                }),
              }));
          },
        },
        {
          key: "last_at",
          label: t("companies.table.last"),
          type: "date",
          defaultDirection: "desc",
          value: (person) => person.last_at,
          render: (person) => h("span", {
            class: "companies-person-last",
            title: formatDate(person.last_at),
            text: formatRelative(person.last_at),
          }),
        },
        {
          key: "role",
          label: t("companies.table.role"),
          type: "text",
          value: (person) => personRole(person),
          render: (person) => h("span", {
            class: "companies-person-role",
            text: personRole(person) || t("common.dash"),
          }),
        },
      ],
      rows: people,
    }).node;
    const known = Math.max(0, Number(profile.people_known) || 0);
    const truncated = known > people.length
      ? h("p", {
        class: "fine companies-truncation",
        text: t("companies.list.showing", {
          shown: formatNumber(people.length),
          total: formatNumber(known),
        }),
      })
      : null;
    return companySectionNode(
      "companies-people-title",
      t("companies.section.people"),
      t("companies.people.sub"),
      table,
      truncated);
  }

  function companyMeetingsSection(profile) {
    const events = (Array.isArray(profile.events) ? profile.events.slice() : [])
      .sort((left, right) => String(left && left.start || "").localeCompare(String(right && right.start || "")));
    const list = events.length
      ? h("div", { class: "companies-event-list" }, events.map((event) => {
        const meta = [];
        if (event.location) meta.push(h("span", { class: "companies-event-location", dir: "auto", text: event.location }));
        if (event.all_day) meta.push(badge(t("agenda.all_day"), "plain"));
        if (event.is_recurring) meta.push(badge(t("companies.meeting.recurring"), "plain"));
        if (event.response_status === 2) meta.push(badge(t("companies.meeting.tentative"), "warn"));
        const organizer = event.organizer_name || event.organizer_address || "";
        if (organizer) meta.push(h("span", { class: "companies-event-organizer", text: t("companies.meeting.organizer", { name: organizer }) }));
        const attendees = Math.max(0, Number(event.attendee_count) || 0);
        if (attendees) meta.push(h("span", { text: t("companies.meeting.attendees", { count: attendees }) }));
        if (!event.all_day && event.start && event.end) {
          const minutes = Math.round((new Date(event.end).getTime() - new Date(event.start).getTime()) / 60000);
          if (Number.isFinite(minutes) && minutes > 0) {
            meta.push(h("span", { text: t("companies.meeting.duration", { duration: formatDuration(minutes * 60 * 1e9) }) }));
          }
        }
        const online = /^https?:\/\//i.test(String(event.online_url || "")) ? String(event.online_url) : "";
        if (online) {
          meta.push(h("a", {
            class: "companies-event-online",
            href: online,
            target: "_blank",
            rel: "noopener noreferrer",
            text: t("companies.meeting.join"),
          }));
        }
        return h("div", { class: "companies-event-row" },
          h("span", { class: "companies-event-main" },
            h("span", { class: "companies-event-subject", dir: "auto", text: event.subject || t("mail.no_subject") }),
            meta.length ? h("span", { class: "companies-event-meta" }, meta) : null),
          h("span", {
            class: "companies-event-when",
            title: formatDate(event.start),
            text: formatDate(event.start, { year: false }),
          }));
      }))
      : h("p", { class: "companies-none", text: t("companies.meetings.empty") });
    const truncated = Math.max(0, Number(profile.events_total) || 0) > events.length
      ? h("p", {
        class: "fine companies-truncation",
        text: t("companies.list.showing", {
          shown: formatNumber(events.length),
          total: formatNumber(Number(profile.events_total) || 0),
        }),
      })
      : null;
    return companySectionNode(
      "companies-meetings-title",
      t("companies.section.meetings"),
      t("companies.meetings.sub", { days: COMPANIES_EVENT_DAYS }),
      list,
      truncated);
  }

  function companyDeadlinesSection(profile) {
    const deadlines = (Array.isArray(profile.deadlines) ? profile.deadlines.slice() : [])
      .sort((left, right) => String(left && left.due || "").localeCompare(String(right && right.due || "")));
    const list = deadlines.length
      ? h("div", { class: "companies-deadline-list" }, deadlines.map((deadline) => {
        const subject = deadline.subject || t("mail.no_subject");
        const sender = deadline.sender_name || t("mail.sender_unknown");
        return h("a", {
          class: "companies-deadline-row",
          href: mailHref(deadline.message_id, "important"),
          "aria-label": t("companies.deadline.open_aria", { subject, sender }),
        },
        h("span", { class: "companies-deadline-main" },
          h("span", { class: "companies-deadline-subject", dir: "auto", text: subject }),
          h("span", { class: "companies-deadline-from", dir: "auto", text: sender })),
        h("span", { class: "companies-deadline-side" },
          badge(t("agenda.kind.deadline"), "warn"),
          h("span", {
            class: "companies-deadline-when",
            title: formatDate(deadline.due),
            text: formatRelative(deadline.due),
          })));
      }))
      : h("p", { class: "companies-none", text: t("companies.deadlines.empty") });
    const truncated = Math.max(0, Number(profile.deadlines_total) || 0) > deadlines.length
      ? h("p", {
        class: "fine companies-truncation",
        text: t("companies.list.showing", {
          shown: formatNumber(deadlines.length),
          total: formatNumber(Number(profile.deadlines_total) || 0),
        }),
      })
      : null;
    return companySectionNode(
      "companies-deadlines-title",
      t("companies.section.deadlines"),
      t("companies.deadlines.sub"),
      list,
      truncated);
  }

  function companyProfileNode(profile, index = -1) {
    const label = companyRowLabel(profile);
    const domains = companyDomains(profile);
    const header = h("header", { class: "companies-profile-head" },
      h("p", { class: "eyebrow", text: t("companies.profile.eyebrow") }),
      h("div", { class: "companies-profile-heading" },
        h("h2", { id: "companies-profile-title", dir: "auto", text: label }),
        badge(
          profile.declared ? t("companies.badge.address_book") : t("companies.badge.domain"),
          profile.declared ? "accent" : "plain")),
      h("div", { class: "companies-domains", "aria-label": t("companies.profile.domains_aria") },
        domains.length
          ? domains.map((domain) => h("span", { class: "chip", dir: "auto", text: domain }))
          : h("span", { class: "fine", text: t("companies.profile.no_domain") })),
      h("div", { class: "companies-profile-summary" },
        h("p", { class: "companies-people-summary" },
          h("span", { text: t("companies.people.contacted", { count: Math.max(0, Number(profile.people_contacted) || 0) }) }),
          h("span", { class: "companies-summary-sep", "aria-hidden": "true" }),
          h("span", { text: t("companies.people.known", { count: Math.max(0, Number(profile.people_known) || 0) }) })),
        h("div", { class: "companies-profile-actions" },
          button(t("companies.action.ask"), () => {
            state.askPrefill = t("companies.action.ask_prefill", { company: label });
            navigate("ask");
          }, { small: true, kind: "quiet", icon: "ask" }))),
      profile.generated_at
        ? h("p", {
          class: "fine companies-generated",
          title: formatDate(profile.generated_at),
          text: t("companies.profile.generated", { when: formatRelative(profile.generated_at) }),
        })
        : null);
    const attrs = {
      class: "surface companies-profile section-scroll",
      "aria-labelledby": "companies-profile-title",
      dataset: { scrollKey: "companies-profile" },
    };
    if (Number.isInteger(index)) attrs["data-company-index"] = String(index);
    return h("section", attrs,
      header,
      companyIntelligenceSection(profile),
      companyKpis(profile),
      companyThreadsSection(profile),
      companyDeadlinesSection(profile),
      companyMeetingsSection(profile),
      companyRecentSection(profile),
      companyPeopleSection(profile),
      companyVolumeSection(profile));
  }

  // companyProfileStateNode is the profile pane's host: the section owns the
  // scroll and the centered layout, the shared state owns the content. The
  // pane is itself the box, so it asks for the content without a second
  // dashed wrapper.
  function companyProfileStateNode(iconName, title, body, actions) {
    return h("section", { class: "surface companies-profile companies-profile-state section-scroll", dataset: { scrollKey: "companies-profile" } },
      emptyState(title, body, actions, iconName, { wrap: false }));
  }

  function companyProfileLoadingNote() {
    return loadingState(t("common.loading"), { className: "companies-profile-update", role: "status" });
  }

  // syncCompanySelection moves the selected row without rebuilding any row.
  // The row index, not the company key, is the only selection identity in
  // the DOM.
  function syncCompanySelection(index) {
    for (const row of document.querySelectorAll(".companies-row")) {
      const selected = row.dataset.companyIndex === String(index);
      row.classList.toggle("selected", selected);
      if (selected) row.setAttribute("aria-current", "true");
      else row.removeAttribute("aria-current");
    }
  }

  // loadCompanyDetail paints one company into the existing profile host.
  // A prefetched profile is synchronous. A cold one keeps the previous
  // profile's height (with a quiet updating bar) so replacing the pane never
  // shrinks the page, clamps #main's scroll and jumps the explorer upward.
  async function loadCompanyDetail(key, host, index) {
    if (!key || !host) return;
    const seq = ++companyDetailSeq;
    const preview = cached(companyPath(key)) ?? warmedCompanyProfile(key);
    if (preview !== undefined) {
      state.companyDetailKey = key;
      state.companyDetailPendingKey = "";
      state.companyDetailFailedKey = "";
      const scroll = host.scrollTop;
      const node = companyProfileNode(preview, index);
      node.dataset.companySeq = String(seq);
      host.replaceWith(node);
      prepareScrollRegions(node);
      node.scrollTop = scroll;
      return;
    }
    state.companyDetailPendingKey = key;
    host.dataset.companySeq = String(seq);
    host.setAttribute("aria-busy", "true");
    host.classList.add("companies-profile-updating");
    if (!host.querySelector(".companies-profile-update")) host.prepend(companyProfileLoadingNote());
    try {
      const profile = await loadCompany(key);
      if (seq !== companyDetailSeq || !host.isConnected) {
        if (state.companyDetailPendingKey === key) state.companyDetailPendingKey = "";
        return;
      }
      state.companyDetailKey = key;
      state.companyDetailPendingKey = "";
      state.companyDetailFailedKey = "";
      const scroll = host.scrollTop;
      const node = companyProfileNode(profile, index);
      node.dataset.companySeq = String(seq);
      host.replaceWith(node);
      prepareScrollRegions(node);
      node.scrollTop = scroll;
    } catch (error) {
      if (seq !== companyDetailSeq || !host.isConnected) {
        if (state.companyDetailPendingKey === key) state.companyDetailPendingKey = "";
        return;
      }
      state.companyDetailKey = key;
      state.companyDetailPendingKey = "";
      state.companyDetailFailedKey = key;
      const node = companyProfileStateNode(
        "alert",
        t("companies.error.profile_title"),
        t("companies.error.profile"),
        [button(t("common.try_again"), () => loadCompanyDetail(key, node, index), { small: true, icon: "refresh" })]);
      node.dataset.companyIndex = String(index);
      node.dataset.companySeq = String(seq);
      host.replaceWith(node);
      reportError(error);
    }
  }

  // Visible company rows are prefetched as one server-side batch: the list
  // service already builds several profiles from one mailbox scan. Off-screen
  // rows are left to the observer, which fires when they enter the list.
  function observeCompanyRows(body, rows) {
    if (state.companyObserver) {
      state.companyObserver.disconnect();
      state.companyObserver = null;
    }
    if (!body || !rows.length) return;
    const keysOf = (list) => list.map((row) => state.companyKeys[Number(row.dataset.companyIndex)]).filter(Boolean);
    if (typeof window.IntersectionObserver !== "function") {
      queueCompanyPrefetch(keysOf(rows.slice(0, COMPANY_PREFETCH_BATCH)));
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      const keys = [];
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        const index = entry.target.dataset ? Number(entry.target.dataset.companyIndex) : -1;
        const key = Number.isInteger(index) ? state.companyKeys[index] : "";
        if (key) keys.push(key);
      }
      queueCompanyPrefetch(keys);
    }, { root: body, rootMargin: "160px 0px" });
    for (const row of rows) observer.observe(row);
    state.companyObserver = observer;
  }

  // reattachCompanyRows restores the visibility observer after a cached
  // Companies screen is painted. Leaving the route disconnects the observer;
  // the cached rows are the same DOM nodes, so without this a Back gesture
  // would keep the list but stop warming the profiles below the fold.
  function reattachCompanyRows() {
    if (state.route !== "companies" || state.companyObserver) return;
    const body = document.querySelector(".companies-list-body");
    if (!body) return;
    const rows = [...body.querySelectorAll(".companies-row")];
    if (rows.length) observeCompanyRows(body, rows);
  }

  // company-prefetch-queue:start
  // One prefetch queue: a row entering the viewport, a pointer resting on a
  // row and a keyboard focus are the same request. Each source used to fire
  // its own read, so moving the pointer down ten rows sent ten one-key
  // requests instead of one batch.
  let companyPrefetchPending = new Set();
  let companyPrefetchTimer = 0;

  function queueCompanyPrefetch(keys) {
    for (const key of keys) {
      if (key) companyPrefetchPending.add(key);
    }
    if (!companyPrefetchPending.size || companyPrefetchTimer) return;
    companyPrefetchTimer = window.setTimeout(() => {
      companyPrefetchTimer = 0;
      const wanted = [...companyPrefetchPending];
      companyPrefetchPending.clear();
      prefetchCompanyProfiles(wanted);
    }, 80);
  }
  // company-prefetch-queue:end

  function prefetchCompanyProfiles(keys) {
    const wanted = [];
    for (const key of keys) {
      if (wanted.length >= COMPANY_PREFETCH_BATCH) break;
      if (!key || cached(companyPath(key)) !== undefined || companyPrefetchInflight.has(key)) continue;
      companyPrefetchInflight.add(key);
      wanted.push(key);
    }
    if (!wanted.length) return;
    const epoch = apiEpoch;
    const query = new URLSearchParams();
    for (const key of wanted) query.append("key", key);
    query.set("months", String(COMPANIES_MONTHS));
    query.set("event_days", String(COMPANIES_EVENT_DAYS));
    request("/api/companies/profiles?" + query.toString()).then((view) => {
      const profiles = view && view.profiles && typeof view.profiles === "object" ? view.profiles : {};
      for (const key of wanted) {
        if (!profiles[key]) continue;
        // The warm buffer survives an unrelated jobs invalidation. The API
        // cache is primed too when no write raced the read.
        warmCompanyProfile(key, profiles[key]);
        if (epoch === apiEpoch) primeCache(companyPath(key), profiles[key], CACHE_TTL.companies);
      }
    }).catch(() => {
      // A prefetch never surfaces an error; the click path owns retry.
    }).finally(() => {
      for (const key of wanted) companyPrefetchInflight.delete(key);
    });
  }

  function companyListRow(row, selected, onSelect) {
    const label = companyRowLabel(row);
    const domains = companyDomains(row);
    const domain = domains.length ? metaLine(domains.slice(0, 2)) : t("companies.list.no_domain");
    const people = Math.max(0, Number(row.people) || 0);
    const messages = Math.max(0, Number(row.messages) || 0);
    const sent = Math.max(0, Number(row.sent) || 0);
    const received = Math.max(0, Number(row.received) || 0);
    const direction = sent === 0 && received > 0
      ? t("companies.list.inbound_only")
      : (received === 0 && sent > 0 ? t("companies.list.unanswered") : "");
    const last = formatRelative(row.last_at);
    return h("button", {
      type: "button",
      class: "companies-row" + (selected ? " selected" : ""),
      "aria-current": selected ? "true" : undefined,
      "aria-label": t("companies.list.row_aria", {
        label,
        messages: t("companies.list.messages", { count: messages }),
        when: last,
      }),
      onclick: onSelect,
    },
    h("span", { class: "companies-row-top" },
      h("span", { class: "companies-row-label", dir: "auto", text: label }),
      h("span", { class: "companies-row-time", title: formatDate(row.last_at), text: last })),
    h("span", { class: "companies-row-meta" },
      h("span", { class: "companies-row-domain", dir: "auto", text: domain }),
      direction ? h("span", { class: "companies-row-direction", text: direction }) : null,
      h("span", { class: "companies-row-people", text: t("companies.list.people", { count: people }) }),
      h("span", { class: "companies-row-messages", text: t("companies.list.messages", { count: messages }) })));
  }

  // companies-render:end

  function companiesSearchInput(query) {
    return h("input", {
      type: "search",
      class: "input",
      hidden: true,
      value: String(query || ""),
      placeholder: t("companies.search.placeholder"),
      autocomplete: "off",
    });
  }

  function companiesCoverageLine(coverage) {
    if (!coverage) return null;
    return h("p", { class: "companies-list-coverage", text: t("companies.list.coverage", {
      with_company: formatNumber(Math.max(0, Number(coverage.contacts_with_company) || 0)),
      contacts: formatNumber(Math.max(0, Number(coverage.contacts) || 0)),
      declared: formatNumber(Math.max(0, Number(coverage.declared_orgs) || 0)),
    }) });
  }

  function companiesCountText(query, shown, total, count) {
    if (query) return t("companies.list.search_count", { shown, total });
    if (total > count) return t("companies.list.showing", { shown: count, total });
    return t("companies.list.count", { count });
  }

  function companiesNoMatchNode(onClear) {
    return emptyState(t("companies.search.no_matches"), "", [
      button(t("companies.search.clear"), onClear, { small: true, kind: "quiet" }),
    ], "search", { hostClass: "companies-list-empty", body: false, hidden: true });
  }

  function companiesListBody(error, companies, query, list, noMatch) {
    if (error) {
      return errorState(error, () => refreshActiveRoute(), {
        title: t("companies.error.list_title"),
        body: t("companies.error.list"),
        actions: [button(t("common.try_again"), () => refreshActiveRoute(), { small: true, icon: "refresh" })],
        hostClass: "companies-list-empty",
        heading: "h3",
      });
    }
    if (!companies.length && !query) {
      return emptyState(t("companies.empty.title"), t("companies.empty.body"), null, "sources",
        { hostClass: "companies-list-empty", heading: "h3" });
    }
    return h("div", { class: "companies-list-body section-scroll", dataset: { scrollKey: "companies-list" } }, list, noMatch);
  }

  // The scoped search is server-side: the store filters the whole ranking
  // before its limit, so a company beyond the first page is reachable. The
  // local filter stays as instant feedback while the debounce waits.
  function bindCompaniesSearch(input, query, selectedKey, applyFilter) {
    let searchTimer = null;
    const renderToken = state.renderSeq;
    input.addEventListener("input", () => {
      state.companiesQuery = input.value;
      applyFilter();
      if (searchTimer) clearTimeout(searchTimer);
      searchTimer = setTimeout(() => {
        if (renderToken !== state.renderSeq) return;
        const value = input.value.trim();
        if (value === String(query || "").trim()) return;
        navigate("companies", { key: selectedKey, q: value || undefined });
      }, 300);
    });
  }

  function companiesListPane(view, error, selectedKey, query) {
    const companies = view && Array.isArray(view.companies) ? view.companies : [];
    const coverage = view && view.coverage && typeof view.coverage === "object" ? view.coverage : null;
    const total = Number(view && view.total) || companies.length;
    const hasMore = !!(view && view.has_more);
    const countNode = h("span", { class: "companies-list-count" });
    const input = companiesSearchInput(query);
    const list = h("div", { class: "companies-list" });
    const rows = [];
    companies.forEach((row, index) => {
      const key = String(row && row.key ? row.key : "");
      const node = companyListRow(row, key === selectedKey, () => {
        // The selected key is live state, not the value captured when these
        // rows were built: a cached screen or a Back gesture swaps the
        // profile without rebuilding the list. A failed profile is retried
        // by selecting its row again.
        if (!key) return;
        if (key === state.companyDetailKey && state.companyDetailFailedKey !== key) return;
        state.companiesQuery = input.value;
        navigate("companies", { key, q: input.value.trim() || undefined });
        if (window.matchMedia && window.matchMedia("(max-width: 1024px)").matches) {
          scrollCompaniesProfileSoon(index);
        }
      });
      node.dataset.companyIndex = String(index);
      node.dataset.search = companySearchText(companyRowLabel(row) + " " + companyDomains(row).join(" "));
      // A pointer or keyboard focus starts the prefetch before the click. The
      // observer owns rows that scroll into view; this covers a click that
      // lands before the observer's debounce.
      node.addEventListener("pointerenter", () => queueCompanyPrefetch([key]), { once: true });
      node.addEventListener("focus", () => queueCompanyPrefetch([key]), { once: true });
      rows.push(node);
      list.append(node);
    });
    const noMatch = companiesNoMatchNode(() => {
      input.value = "";
      state.companiesQuery = "";
      applyFilter();
      navigate("companies", { key: selectedKey });
      input.focus();
    });

    const selectedIndex = state.companyKeys.indexOf(selectedKey);
    const selectedLabel = selectedIndex >= 0 ? companyRowLabel(companies[selectedIndex]) : "";
    const outsideNote = h("p", { class: "companies-search-note", role: "status", hidden: true });
    const truncationNode = h("p", { class: "fine companies-truncation", hidden: true,
      text: t("companies.list.truncated", { shown: companies.length, total }) });

    function applyFilter() {
      const folded = companySearchText(input.value);
      let shown = 0;
      for (const row of rows) {
        const hit = !folded || String(row.dataset.search || "").includes(folded);
        row.hidden = !hit;
        if (hit) shown += 1;
      }
      countNode.textContent = companiesCountText(query, shown, total, companies.length);
      noMatch.hidden = !(folded && shown === 0);
      truncationNode.hidden = !hasMore || !!query;
      // The profile keeps showing the selected company even when the filter
      // hides its row. Say so instead of leaving two panes that disagree.
      const hiddenSelection = !!(folded && selectedIndex >= 0 && rows[selectedIndex] && rows[selectedIndex].hidden);
      outsideNote.hidden = !hiddenSelection;
      outsideNote.textContent = hiddenSelection
        ? t("companies.search.selected_outside", { label: selectedLabel })
        : "";
    }

    bindCompaniesSearch(input, query, selectedKey, applyFilter);

    const coverageLine = companiesCoverageLine(coverage);
    const body = companiesListBody(error, companies, query, list, noMatch);
    applyFilter();
    if (!error && rows.length) observeCompanyRows(body, rows);
    const pane = h("aside", { class: "surface companies-list-pane", "aria-label": t("companies.list.aria") },
      h("div", { class: "companies-list-head" },
        h("div", { class: "companies-list-title-row" },
          h("h2", { text: t("companies.list.title") }),
          countNode),
        outsideNote,
        truncationNode,
        coverageLine),
      body);
    pane.append(input);
    setScopedSearch("companies", (value) => {
      input.value = value;
      input.dispatchEvent(new Event("input"));
    }, "companies.search.placeholder", "companies.search.aria", () => state.companiesQuery);
    return pane;
  }

  // On the stacked layout the list sits above the profile. Move the selected
  // profile into view once it belongs to this key, so the list never becomes
  // a dead end and a previous profile is never the scroll target.
  function scrollCompaniesProfileSoon(index) {
    let attempts = 0;
    const tick = () => {
      const profile = [...document.querySelectorAll(".companies-profile")]
        .find((node) => node.isConnected && node.dataset.companyIndex === String(index) &&
          !node.classList.contains("companies-profile-skeleton-surface") &&
          !node.classList.contains("companies-profile-loading"));
      if (profile) {
        profile.scrollIntoView({ block: "start" });
        return;
      }
      attempts += 1;
      if (attempts < 90) window.requestAnimationFrame(tick);
    };
    window.requestAnimationFrame(tick);
  }

  async function renderCompanies(route, seq) {
    const [, learning] = await Promise.all([readGate(), loadLearning()]);
    if (seq !== state.renderSeq) return h("div");
    const open = gateOpen();
    const preview = gatePreview();
    setTopbarMeta(gatePill(open), preview && !open ? previewPill() : null);
    if (!open && !preview) return lockedScreen(learning, t("companies.locked_feature"));

    const query = String(route.params.get("q") || "").trim();
    state.companiesQuery = query;
    let view = null;
    let listError = null;
    try {
      view = await loadCompanies(query);
    } catch (error) {
      listError = error;
    }
    if (seq !== state.renderSeq) return h("div");
    const companies = view && Array.isArray(view.companies) ? view.companies : [];
    const listTotal = Number(view && view.total) || companies.length;
    state.companiesCount = listTotal;
    state.companyKeys = companies.map((company) => String(company && company.key ? company.key : ""));
    const requestedKey = String(route.params.get("key") || "").trim();
    const selectedKey = requestedKey || (companies[0] && companies[0].key ? String(companies[0].key) : "");
    // A deep link can name a company outside the ranked page; it is still a
    // real profile, but it has no row, so the index is the -1 sentinel.
    const selectedIndex = state.companyKeys.indexOf(selectedKey);
    let profile = null;
    let profileError = null;
    // A list failure does not invalidate a requested profile. The panes are
    // independent reads, so a deep link can still show its company while the
    // ranking shows its own error.
    if (selectedKey) {
      try {
        profile = await loadCompany(selectedKey);
      } catch (error) {
        profileError = error;
      }
    }
    if (seq !== state.renderSeq) return h("div");
    if (selectedKey) {
      state.companyDetailKey = selectedKey;
      state.companyDetailPendingKey = "";
      state.companyDetailFailedKey = profileError ? selectedKey : "";
    } else {
      state.companyDetailKey = "";
      state.companyDetailPendingKey = "";
      state.companyDetailFailedKey = "";
    }
    if (view) {
      setTopbarMeta(
        gatePill(open),
        preview && !open ? previewPill() : null,
        pill(t("companies.list.count", { count: listTotal })));
    }
    let profileNode;
    // Profile content wins over the list state: a real deep link is never
    // hidden behind an empty or failed ranking, and a profile error is more
    // specific than the list error it may accompany.
    if (profile) {
      profileNode = companyProfileNode(profile, selectedIndex);
    } else if (profileError) {
      profileNode = companyProfileStateNode(
        "alert",
        t("companies.error.profile_title"),
        t("companies.error.profile"),
        [button(t("common.try_again"), () => refreshActiveRoute(), { small: true, icon: "refresh" })]);
    } else if (listError) {
      profileNode = companyProfileStateNode(
        "alert",
        t("companies.error.list_title"),
        t("companies.error.list"),
        [button(t("common.try_again"), () => refreshActiveRoute(), { small: true, icon: "refresh" })]);
    } else if (!companies.length) {
      profileNode = companyProfileStateNode("sources", t("companies.empty.title"), t("companies.empty.body"), null);
    } else {
      profileNode = companyProfileStateNode(
        "sources",
        t("companies.profile.select"),
        t("companies.profile.select_body"),
        null);
    }
    const screen = h("div", { class: "screen companies-screen" },
      h("div", { class: "companies-layout" },
        companiesListPane(view, listError, selectedKey, query),
        profileNode));
    // A cached restore re-appends these same rows. The route cache calls this
    // hook to reconnect the visibility observer that leaving the route drops.
    screen.__routeAttach = reattachCompanyRows;
    // The error is transient: a cached copy would keep showing a failure
    // that a retry or the next navigation can already fix.
    if (listError) screen.dataset.routeError = "1";
    return screen;
  }

