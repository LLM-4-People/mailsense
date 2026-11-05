  // ---------------------------------------------------------------- ask

  // The Ask composer is the one target of every cross-screen handoff: a
  // citation, a search row, a company action and the first-run prompt all
  // ask the shell to land here with a question. These helpers own that
  // intent so the router does not reimplement focus or replay a prefill.
  function focusAskInput(field) {
    const input = field || document.getElementById("ask-input");
    if (!input) return false;
    input.focus();
    // A fresh render consumes its prefill before the composer is mounted;
    // focusing a detached field is a no-op, so keep the intent pending for
    // restoreNavigationFocus to finish after the mount.
    if (document.activeElement !== input) {
      state.askFocusPending = true;
      return false;
    }
    input.select();
    state.askFocusPending = false;
    return true;
  }

  // consumeAskPrefill applies the one-shot question handed over by another
  // screen. It is a mount intent, not render state: a cached Ask node never
  // runs its render again, so the router must be able to consume it too.
  function consumeAskPrefill(field) {
    if (!state.askPrefill) return false;
    // A fresh Ask render is not mounted yet, so the caller hands its own
    // input. A cached mount has no live composer reference and looks it up.
    const input = field || document.getElementById("ask-input");
    if (!input) return false;
    input.value = state.askPrefill;
    state.askPrefill = "";
    focusAskInput(input);
    input.setSelectionRange(input.value.length, input.value.length);
    return true;
  }

  function requestAskFocus() {
    state.askFocusPending = true;
    if (state.route !== "ask") {
      navigate("ask");
      return;
    }
    window.requestAnimationFrame(() => {
      if (!focusAskInput()) state.askFocusPending = true;
    });
  }

  const ASK_SUGGESTIONS = [
    "ask.suggestion.urgent",
    "ask.suggestion.reply",
    "ask.suggestion.security",
    "ask.suggestion.summary",
  ];

  function structuredCount(view) {
    const counts = view && view.counts ? view.counts : {};
    if (view && (view.intent === "priority" || view.intent === "summary_today")) return Number(counts.total) || 0;
    if (view && view.intent === "sender") return Array.isArray(view.citations) ? view.citations.length : 0;
    return Number(counts[view && view.intent]) || 0;
  }

  function structuredSentence(view) {
    const intent = view && view.intent ? view.intent : "";
    const counts = view && view.counts ? view.counts : {};
    if (intent === "summary_today") {
      const total = Number(counts.total) || 0;
      return total === 0
        ? t("ask.sentence.summary_today.none")
        : t("ask.sentence.summary_today", { count: total });
    }
    if (intent === "topic") {
      const topic = view.topic || {};
      const label = subjectPathLabel(topic.topic, topic.subtopic);
      const count = Number(topic.count) || 0;
      return count === 0
        ? t("ask.sentence.topic.none", { topic: label })
        : t("ask.sentence.topic", { count, topic: label });
    }
    const count = structuredCount(view);
    const key = "ask.sentence." + intent;
    if (intent === "sender" && count === 0) return t("ask.sentence.sender.none");
    if (I18N.has(key + ".none") && count === 0) return t(key + ".none");
    if (I18N.has(key)) return t(key, { count });
    return t("ask.sentence.empty");
  }

  // answer-tables:start
  //
  // Generated answers are plain text, but a provider may still emit a
  // Markdown pipe table. Recognition is deliberately conservative: a header
  // line with a pipe must be followed by a separator row whose cell count and
  // `:?-{3,}:?` shape match. Anything doubtful stays as text. Cell content is
  // paged through askAnswerTextNode, so citations keep their chips and no
  // provider bytes ever become raw markup.

  const ASK_TABLE_MAX_COLUMNS = 12;
  const ASK_TABLE_MAX_ROWS = 200;

  function askLineHasPipe(line) {
    let inCode = false;
    for (let index = 0; index < line.length; index += 1) {
      const char = line[index];
      if (char === "\\") { index += 1; continue; }
      if (char === "`") { inCode = !inCode; continue; }
      if (char === "|" && !inCode) return true;
    }
    return false;
  }

  function askSplitTableRow(line) {
    let raw = String(line == null ? "" : line).trim();
    if (raw.startsWith("|")) raw = raw.slice(1);
    if (raw.endsWith("|") && !raw.endsWith("\\|")) raw = raw.slice(0, -1);
    const cells = [];
    let buffer = "";
    let inCode = false;
    for (let index = 0; index < raw.length; index += 1) {
      const char = raw[index];
      if (char === "\\" && index + 1 < raw.length) {
        const next = raw[index + 1];
        if (next === "|" || next === "`" || next === "\\") {
          buffer += next;
          index += 1;
          continue;
        }
      }
      if (char === "`") { inCode = !inCode; buffer += char; continue; }
      if (char === "|" && !inCode) { cells.push(buffer.trim()); buffer = ""; continue; }
      buffer += char;
    }
    cells.push(buffer.trim());
    return cells;
  }

  function askIsTableSeparator(cells) {
    return Array.isArray(cells) && cells.length > 0 &&
      cells.every((cell) => /^:?-{3,}:?$/.test(String(cell).trim()));
  }

  function askAnswerBlocks(value) {
    const source = String(value == null ? "" : value);
    if (!source) return [];
    const lines = source.split(/\r?\n/);
    const blocks = [];
    let textLines = [];
    let inFence = false;
    let fenceChar = "";
    let fenceLength = 0;

    const flushText = () => {
      if (!textLines.length) return;
      const joined = textLines.join("\n");
      if (joined.trim() !== "") blocks.push({ type: "text", text: joined });
      textLines = [];
    };

    for (let index = 0; index < lines.length; index += 1) {
      const line = lines[index];
      const trimmed = line.replace(/^\s+/, "");
      const fence = /^(`{3,}|~{3,})/.exec(trimmed);
      if (fence) {
        if (!inFence) {
          inFence = true;
          fenceChar = fence[1][0];
          fenceLength = fence[1].length;
        } else if (fence[1][0] === fenceChar && fence[1].length >= fenceLength) {
          inFence = false;
        }
        textLines.push(line);
        continue;
      }
      if (inFence || !askLineHasPipe(line) || index + 1 >= lines.length) {
        textLines.push(line);
        continue;
      }
      const header = askSplitTableRow(line);
      const separator = askSplitTableRow(lines[index + 1]);
      if (header.length === 0 ||
        header.length > ASK_TABLE_MAX_COLUMNS ||
        header.length !== separator.length ||
        !askIsTableSeparator(separator) ||
        !header.some((cell) => cell.trim() !== "")) {
        textLines.push(line);
        continue;
      }
      let end = index + 2;
      const data = [];
      const dataLines = [];
      let malformed = false;
      while (end < lines.length && askLineHasPipe(lines[end])) {
        const cells = askSplitTableRow(lines[end]);
        if (cells.length !== header.length) { malformed = true; break; }
        data.push(cells);
        dataLines.push(lines[end]);
        end += 1;
      }
      if (malformed) {
        textLines.push(line);
        continue;
      }
      flushText();
      const overflow = data.slice(ASK_TABLE_MAX_ROWS);
      const overflowLines = dataLines.slice(ASK_TABLE_MAX_ROWS);
      blocks.push({
        type: "table",
        header,
        rows: data.slice(0, ASK_TABLE_MAX_ROWS),
        truncated: overflow.length,
      });
      if (overflow.length) blocks.push({ type: "text", text: overflowLines.join("\n") });
      index = end - 1;
    }
    flushText();
    return blocks;
  }

  function askAnswerTableNode(block, citations) {
    const header = Array.isArray(block && block.header) ? block.header : [];
    const rowCells = Array.isArray(block && block.rows) ? block.rows : [];
    const columns = header.map((label, index) => ({
      key: "column-" + index,
      label: String(label || "").trim() || t("ask.table.column", { number: formatNumber(index + 1) }),
      type: "auto",
      value: (cells) => Array.isArray(cells) ? cells[index] : "",
      render: (cells) => askAnswerTextNode(Array.isArray(cells) ? cells[index] : "", citations),
    }));
    const table = sortableTable({
      caption: t("ask.table.caption"),
      wrapperClass: "ask-table-wrap answer-table-wrap",
      className: "answer-table",
      columns,
      rows: rowCells,
    }).node;
    if (!block || !block.truncated) return table;
    return h("div", { class: "ask-answer-table-block" },
      table,
      h("p", {
        class: "fine ask-table-note",
        text: t("ask.table.truncated", { count: ASK_TABLE_MAX_ROWS }),
      }));
  }

  function askAnswerContentNode(value, citations) {
    const blocks = askAnswerBlocks(value);
    if (!blocks.some((block) => block.type === "table")) {
      const plain = h("p", { class: "ask-answer-text", dir: "auto" });
      plain.append(askAnswerTextNode(value, citations));
      return plain;
    }
    const root = h("div", { class: "ask-answer-text ask-answer-rich", dir: "auto" });
    for (const block of blocks) {
      if (block.type === "table") {
        root.append(askAnswerTableNode(block, citations));
        continue;
      }
      const paragraph = h("p", { class: "ask-answer-paragraph", dir: "auto" });
      paragraph.append(askAnswerTextNode(block.text, citations));
      root.append(paragraph);
    }
    return root;
  }
  // answer-tables:end

  function askAnswerTextNode(value, citations) {
    const body = String(value || "");
    const fragment = document.createDocumentFragment();
    // One index or a grouped list ("[1, 2]"). The server splits a fresh
    // answer, but an answer stored before that fix still carries the grouped
    // form, so the renderer must chip each source instead of leaving the
    // bracket as dead prose.
    const re = /\[(\d+(?:\s*[,;]\s*\d+)*)\]/g;
    let last = 0;
    let match;
    while ((match = re.exec(body)) !== null) {
      if (match.index > last) fragment.append(document.createTextNode(body.slice(last, match.index)));
      const indices = match[1].split(/[,;]/).map((part) => Number(part.trim()))
        .filter((index) => Number.isInteger(index) && index > 0);
      indices.forEach((index, position) => {
        if (position > 0) fragment.append(document.createTextNode(", "));
        const citation = Array.isArray(citations) ? citations[index - 1] : null;
        if (citation) {
          fragment.append(h("a", {
            class: "citation-chip",
            href: citationHref(citation),
            "aria-label": t("ask.citation_aria", { index }),
          }, text("[" + index + "]")));
        } else {
          fragment.append(document.createTextNode("[" + index + "]"));
        }
      });
      last = match.index + match[0].length;
    }
    if (last < body.length) fragment.append(document.createTextNode(body.slice(last)));
    return fragment;
  }

  function askCitationRow(citation, index) {
    return h("a", {
      class: "citation-row",
      href: citationHref(citation),
      "aria-label": t("ask.citation_aria", { index }),
    },
    h("span", { class: "citation-index" }, text("[" + (index + 1) + "]")),
    h("span", { class: "citation-copy" },
      h("span", { class: "citation-subject", dir: "auto", text: citation.subject || t("mail.no_subject") }),
      h("span", { class: "citation-meta", dir: "auto", text: metaLine([citation.sender_name, formatRelative(citation.received_at)]) })),
    citation.reason ? reasonBadge(citation.reason) : null);
  }

  function askCitationList(view) {
    const citations = Array.isArray(view.citations) ? view.citations : [];
    if (!citations.length) return null;
    const initial = 4;
    const list = h("div", { class: "citation-list" });
    const show = (from, to) => {
      for (let index = from; index < to; index += 1) list.append(askCitationRow(citations[index], index));
    };
    show(0, Math.min(initial, citations.length));
    if (citations.length > initial) {
      const more = h("button", {
        type: "button",
        class: "ask-citations-more",
        onclick: () => {
          show(initial, citations.length);
          more.remove();
          // The trigger no longer exists, so focus moves to the first row
          // it revealed instead of falling back to the document.
          const first = list.children[initial];
          if (first && typeof first.focus === "function") first.focus({ preventScroll: true });
        },
      }, text(t("ask.citations.more", { count: citations.length })));
      list.append(more);
    }
    const title = t("ask.citations.title") + " (" + formatNumber(citations.length) + ")";
    return h("section", { class: "ask-citations", "aria-labelledby": "ask-sources-" + String(view.id || "view") },
      h("h4", { id: "ask-sources-" + String(view.id || "view"), text: title }), list);
  }

  // contactResultName resolves the one human name for a contact row. The
  // address stays secondary unless it is the only name the mailbox knows.
  function contactResultName(row) {
    const label = String(row && row.label || "").trim();
    const address = String(row && row.address || "").trim();
    const key = String(row && row.key || "").trim();
    if (label && label.toLocaleLowerCase() !== address.toLocaleLowerCase()) {
      return { name: label, address };
    }
    return { name: address || key || t("common.unknown"), address: "" };
  }

  function computedTable(caption, columns, rows, className) {
    return sortableTable({
      caption,
      columns,
      rows,
      className,
      wrapperClass: "ask-table-wrap",
    }).node;
  }

  function contactResultNode(view) {
    const result = view.result || {};
    const plan = view.plan || {};
    const topic = String(plan.text || "").trim();
    const rows = Array.isArray(result.rows) ? result.rows : [];
    if (!rows.length) {
      return emptyState(t("ask.computed.contacts.empty_title"), t("ask.computed.contacts.empty_body"), null, "search");
    }
    const count = rows.length;
    const total = Number(result.total);
    // A topic-filtered view ranks the people who mentioned the topic across
    // one message population, so the lead names the topic and the table drops
    // the per-direction sent/received/threads columns.
    const lead = topic
      ? h("p", { class: "ask-sentence", dir: "auto" },
        text(topic),
        text(metaSeparator()),
        text(t("ask.computed.contacts.topic_lead", {
          count,
          messages: Number.isFinite(total) ? total : 0,
        })))
      : h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.contacts.lead", { count }) });
    const activityColumns = topic ? [] : [
      {
        key: "sent",
        label: t("ask.computed.table.sent"),
        type: "number",
        align: "num",
        value: (row) => row.sent,
        render: (row) => formatNumber(row.sent),
      },
      {
        key: "received",
        label: t("ask.computed.table.received"),
        type: "number",
        align: "num",
        value: (row) => row.received,
        render: (row) => formatNumber(row.received),
      },
      {
        key: "threads",
        label: t("ask.computed.table.threads"),
        type: "number",
        align: "num",
        value: (row) => row.threads,
        render: (row) => formatNumber(row.threads),
      },
    ];
    const table = computedTable(
      topic ? t("ask.computed.contacts.topic_caption") : t("ask.computed.contacts.caption"),
      [
        {
          key: "rank",
          label: t("ask.computed.table.rank"),
          type: "number",
          align: "num",
          sortable: false,
          render: (row, index) => formatNumber(index + 1),
        },
        {
          key: "contact",
          label: t("ask.computed.table.contact"),
          type: "text",
          value: (row) => contactResultName(row).name,
          render: (row) => {
            const contact = contactResultName(row);
            return fragment(
              h("span", { class: "row-title ask-contact-name", dir: "auto", text: contact.name }),
              contact.address
                ? h("span", { class: "secondary ask-contact-address", dir: "auto", text: contact.address })
                : null);
          },
        },
        {
          key: "messages",
          label: t("ask.computed.table.messages"),
          type: "number",
          align: "num",
          value: (row) => row.messages,
          render: (row) => formatNumber(row.messages),
        },
        ...activityColumns,
        {
          key: "last_at",
          label: t("ask.computed.table.last"),
          type: "date",
          defaultDirection: "desc",
          value: (row) => row.last_at,
          render: (row) => h("span", {
            class: "ask-contact-last",
            title: formatDate(row.last_at),
            text: formatRelative(row.last_at),
          }),
        },
      ],
      rows,
      "ask-contact-table");
    const capped = topic && result.capped === true
      ? h("p", { class: "fine ask-contact-capped", text: t("ask.computed.contacts.topic_capped") })
      : null;
    return h("div", { class: "ask-computed" }, lead, table, capped);
  }

  // companyPeopleCount accepts the merged contact list or a count, so the
  // company result keeps rendering while the server reports people either way.
  function companyPeopleCount(row) {
    const merged = row && row.contacts;
    if (Array.isArray(merged)) return merged.length;
    if (merged && typeof merged === "object") return Object.keys(merged).length;
    if (merged !== null && merged !== undefined && merged !== "") {
      const count = Number(merged);
      if (Number.isFinite(count) && count >= 0) return count;
    }
    const fallback = Number(row && row.people);
    return Number.isFinite(fallback) && fallback >= 0 ? fallback : 0;
  }

  function companyResultNode(result) {
    const rows = Array.isArray(result.rows) ? result.rows : [];
    if (!rows.length) {
      return emptyState(t("ask.computed.companies.empty_title"), t("ask.computed.companies.empty_body"), null, "search");
    }
    const count = rows.length;
    const lead = h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.companies.lead", { count }) });
    const table = computedTable(t("ask.computed.companies.caption"), [
      {
        key: "rank",
        label: t("ask.computed.table.rank"),
        type: "number",
        align: "num",
        sortable: false,
        render: (row, index) => formatNumber(index + 1),
      },
      {
        key: "company",
        label: t("ask.computed.table.company"),
        type: "text",
        value: (row) => {
          const label = String(row && row.label || "").trim();
          const address = String(row && row.address || "").trim();
          const key = String(row && row.key || "").trim();
          return label || address || key || t("common.unknown");
        },
        render: (row) => {
          const label = String(row && row.label || "").trim();
          const address = String(row && row.address || "").trim();
          const key = String(row && row.key || "").trim();
          const name = label || address || key || t("common.unknown");
          const domain = address && address.toLocaleLowerCase() !== name.toLocaleLowerCase() ? address : "";
          const people = companyPeopleCount(row);
          return fragment(
            h("span", { class: "row-title ask-company-name", dir: "auto", text: name }),
            h("span", { class: "secondary ask-company-meta", dir: "auto" },
              domain ? h("span", { class: "ask-company-domain", dir: "auto", text: domain }) : null,
              domain ? text(metaSeparator()) : null,
              badge(t("ask.computed.companies.people", { count: people }), "plain ask-company-people")));
        },
      },
      {
        key: "people",
        label: t("ask.computed.table.people"),
        type: "number",
        align: "num",
        value: (row) => companyPeopleCount(row),
        render: (row) => {
          const people = companyPeopleCount(row);
          return formatNumber(people);
        },
      },
      {
        key: "messages",
        label: t("ask.computed.table.messages"),
        type: "number",
        align: "num",
        value: (row) => row.messages,
        render: (row) => formatNumber(row.messages),
      },
      {
        key: "sent",
        label: t("ask.computed.table.sent"),
        type: "number",
        align: "num",
        value: (row) => row.sent,
        render: (row) => formatNumber(row.sent),
      },
      {
        key: "received",
        label: t("ask.computed.table.received"),
        type: "number",
        align: "num",
        value: (row) => row.received,
        render: (row) => formatNumber(row.received),
      },
      {
        key: "threads",
        label: t("ask.computed.table.threads"),
        type: "number",
        align: "num",
        value: (row) => row.threads,
        render: (row) => formatNumber(row.threads),
      },
      {
        key: "last_at",
        label: t("ask.computed.table.last"),
        type: "date",
        defaultDirection: "desc",
        value: (row) => row.last_at,
        render: (row) => h("span", {
          class: "ask-company-last",
          title: formatDate(row.last_at),
          text: formatRelative(row.last_at),
        }),
      },
    ], rows, "ask-company-table");
    return h("div", { class: "ask-computed" }, lead, table);
  }

  function messageResultNode(view, result) {
    const plan = view.plan || {};
    const action = result.action || plan.action || "";
    const rows = Array.isArray(result.rows) ? result.rows : [];
    const citations = Array.isArray(view.citations) ? view.citations : [];
    if (action === "count") {
      const total = Number(result.total) || 0;
      return h("p", {
        class: "ask-sentence",
        dir: "auto",
        text: total ? t("ask.computed.messages.count", { count: total }) : t("ask.computed.messages.none"),
      });
    }
    if (action === "list") {
      const count = Number(result.total) || rows.length || citations.length;
      if (!count) return h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.messages.none") });
      return h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.messages.list", { count }) });
    }
    return h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.unknown") });
  }

  function timeBucketLabel(key, groupBy) {
    const value = String(key || "").trim();
    const group = String(groupBy || "");
    const weekday = value.toLocaleLowerCase();
    if (weekday && I18N.has("ask.weekday." + weekday)) return t("ask.weekday." + weekday);
    if (group === "week") return value || t("common.unknown");
    if (group === "month" && /^\d{4}-\d{2}$/.test(value)) return formatMonth(value);
    if (group === "day" && /^\d{4}-\d{2}-\d{2}$/.test(value)) return formatDay(value);
    if (/^\d{4}-\d{2}-\d{2}$/.test(value)) return formatDay(value);
    if (/^\d{4}-\d{2}$/.test(value)) return formatMonth(value);
    if (/^\d{4}-W\d{2}$/i.test(value)) return value;
    return value || t("common.unknown");
  }

  function timeResultNode(view, result) {
    const rows = Array.isArray(result.rows) ? result.rows : [];
    if (!rows.length) {
      return emptyState(t("ask.computed.time.empty_title"), t("ask.computed.time.empty_body"), null, "search");
    }
    const total = Number(result.total) || 0;
    const lead = h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.time.lead", { count: total }) });
    const groupBy = (view.plan && view.plan.group_by) || "";
    const table = computedTable(t("ask.computed.time.caption"), [
      {
        key: "period",
        label: t("ask.computed.time.period"),
        type: "date",
        value: (row) => row.key,
        render: (row) => h("span", { class: "ask-time-period", text: timeBucketLabel(row.key, groupBy) }),
      },
      {
        key: "messages",
        label: t("ask.computed.time.messages"),
        type: "number",
        align: "num",
        value: (row) => row.messages,
        render: (row) => formatNumber(row.messages),
      },
    ], rows, "ask-time-table");
    return h("div", { class: "ask-computed" }, lead, table);
  }

  function agendaResultNode(view) {
    const res = view.agenda || {};
    const items = Array.isArray(res.items) ? res.items : [];
    const backlog = Number(res.backlog) ? t("agenda.backlog", { count: Number(res.backlog) }) : "";
    const lead = h("p", { class: "ask-sentence", dir: "auto", text: t("agenda.summary", {
      events: Number(res.events) || 0,
      obligations: Number(res.obligations) || 0,
      deadlines: Number(res.deadlines) || 0,
    }) + backlog });
    if (!items.length) {
      return h("div", { class: "ask-computed" }, lead,
        emptyState(t("agenda.empty_title"), t("agenda.empty_body"), null, "mail"));
    }
    return h("div", { class: "ask-computed" }, lead,
      h("div", { class: "agenda-list ask-agenda-list" }, items.map((item) => agendaRow(item))),
      agendaTruncationNotice(res));
  }

  function computedAnswerCard(view) {
    if (view.agenda) return agendaResultNode(view);
    // A company answer is composed prose over the cached organization
    // profile. It carries no plan/result, so it renders before the typed
    // result dispatch instead of falling through to the unknown card.
    if (view.intent === "company" && view.answer) {
      return askAnswerContentNode(view.answer, view.citations);
    }
    const result = view.result || {};
    const plan = view.plan || {};
    const kind = result.kind || plan.kind || view.intent || "";
    if (kind === "companies") return companyResultNode(result);
    if (kind === "contacts") return contactResultNode(view);
    if (kind === "messages") return messageResultNode(view, result);
    if (kind === "time") return timeResultNode(view, result);
    return h("p", { class: "ask-sentence", dir: "auto", text: t("ask.computed.unknown") });
  }

  // clarifyCandidateCard shows the entity behind one clarification choice:
  // its name, secondary address or domain, and message count. The complete
  // follow-up question stays in data-question and is what a click submits,
  // so the card never repeats the prompt as visible text.
  function clarifyCandidateCard(candidate, options) {
    const entry = candidate && typeof candidate === "object" ? candidate : {};
    const label = String(entry.label || "").trim();
    const detail = String(entry.address || entry.domain || "").trim();
    const name = label || detail || t("common.unknown");
    const secondary = detail && detail.toLocaleLowerCase() !== name.toLocaleLowerCase() ? detail : "";
    const followUp = String(entry.question || "").trim();
    const messages = Number(entry.messages);
    return h("button", {
      type: "button",
      class: "button small ask-clarify-card",
      "data-question": followUp,
      onclick: () => {
        if (followUp && typeof options.onClarify === "function") options.onClarify(followUp);
      },
    },
    h("span", { class: "ask-clarify-card-name", dir: "auto", text: name }),
    secondary
      ? h("span", { class: "ask-clarify-card-address", dir: "auto", text: secondary })
      : null,
    h("span", { class: "ask-clarify-card-count", text: t("ask.clarify.candidate_messages", {
      count: Number.isFinite(messages) && messages >= 0 ? messages : 0,
    }) }));
  }

  function clarifyAnswerCard(view, options = {}) {
    const clarify = view.clarify || {};
    const question = String(clarify.question || view.answer || "").trim();
    const choices = Array.isArray(clarify.options)
      ? clarify.options.map((option) => String(option || "").trim()).filter(Boolean)
      : [];
    const candidates = Array.isArray(clarify.candidates) ? clarify.candidates : [];
    const buttons = candidates.length
      ? candidates.map((candidate) => clarifyCandidateCard(candidate, options))
      : choices.map((option) => button(option, () => {
        if (typeof options.onClarify === "function") options.onClarify(option);
      }, { small: true }));
    return h("div", { class: "ask-clarify" },
      question ? h("p", { class: "ask-clarify-question", dir: "auto", text: question }) : null,
      h("p", { class: "fine ask-clarify-hint", text: t("ask.clarify.hint") }),
      buttons.length
        ? h("div", { class: "ask-clarify-options", role: "group", "aria-label": t("ask.clarify.options_aria") }, buttons)
        : null);
  }

  // askNoticeCard renders the explicit retrieval fallback for one notice
  // code. Only qa_unavailable means no answer provider is configured, so
  // only it may link to provider settings; the other notices describe the
  // search index or the provider's response. Retry is an explicit click:
  // the composer never resubmits a question on its own.
  function askNoticeCard(view, options = {}) {
    const notice = String(view.notice || "");
    const identity = metaLine([String(view.provider || "").trim(), String(view.model || "").trim()]);
    const retry = typeof options.onRetry === "function" && String(view.question || "").trim()
      ? h("div", { class: "button-row" },
        button(t("common.try_again"), () => options.onRetry(view.question), { small: true, kind: "quiet", icon: "refresh" }))
      : null;
    const provider = identity ? h("p", { class: "fine ask-notice-provider", dir: "auto",
      text: t("ask.generation_failed.provider", { provider: identity }) }) : null;
    if (notice === "qa_sender_unknown") {
      return askNotice({ title: t("ask.sender_unknown.title"), body: t("ask.sender_unknown.body"), icon: "search" });
    }
    if (notice === "qa_local_no_answer") {
      return askNotice({
        title: t("ask.local.title"),
        body: t("ask.local.body"),
        icon: "search",
      });
    }
    if (notice === "qa_index_warming") {
      return askNotice({ title: t("ask.index_warming.title"), body: t("ask.index_warming.body"), action: retry });
    }
    if (notice === "qa_retrieval_timeout") {
      return askNotice({ title: t("ask.retrieval_slow.title"), body: t("ask.retrieval_slow.body"), action: retry });
    }
    if (notice === "qa_generation_failed") {
      return askNotice({
        title: t("ask.generation_failed.title"),
        body: t("ask.generation_failed.body"),
        extra: provider,
        action: retry,
      });
    }
    if (notice === "qa_no_grounded_answer") {
      return askNotice({
        title: t("ask.no_grounded.title"),
        body: t("ask.no_grounded.body"),
        extra: provider,
        action: retry,
      });
    }
    if (notice === "answer_language") {
      // Notice-only: the answer stays as written, so this card has no retry
      // action. A re-ask calls the provider again because the view was not
      // cached.
      return askNotice({ title: t("ask.answer_language.title"), body: t("ask.answer_language.body") });
    }
    if (notice === "qa_unavailable") {
      const state = options.providerState || "unknown";
      const settings = h("a", { href: routeHash("settings", { tab: "providers" }), text: t("ask.unavailable.keys") });
      if (state === "unknown") {
        return askNotice({ title: t("ask.provider.unknown.title"), body: t("ask.provider.unknown.body"), action: settings });
      }
      if (state === "unavailable") {
        return askNotice({ title: t("ask.provider.unusable.title"), body: t("ask.provider.unusable.body"), action: settings });
      }
      return askNotice({
        title: t("ask.unavailable.title"),
        body: t("ask.unavailable.body"),
        action: state === "available" ? null : settings,
      });
    }
    return askNotice({ title: t("ask.unavailable.title"), body: t("ask.unavailable.body") });
  }


  // askNotice is the one inline notice of the Ask transcript. The shared
  // warning banner owns the icon and live role; this owner carries the
  // title/body/extra/action hierarchy every notice used to repeat by hand.
  function askNotice(opts) {
    return warningBanner(
      h("div", null,
        h("strong", { text: opts.title }),
        h("p", { text: opts.body }),
        opts.extra || null,
        opts.action || null),
      {
        className: "ask-note " + (opts.className || "warning"),
        role: "status",
        icon: opts.icon || "alert",
      });
  }

  function askAnswerCard(view, options = {}) {
    const headingId = "ask-answer-" + String(view.id || Date.now()) + "-" + String(Math.round(Math.random() * 1e6));
    const heading = h("h3", { id: headingId, class: "ask-answer-title", tabIndex: -1, text: t("ask.answer.title") });
    const past = options.past === true;
    const pastTime = past && view.created_at ? formatRelative(view.created_at) : "";
    const noMatches = view.notice === "no_matches";
    const companyUnavailable = view.notice === "company_profile_unavailable";
    const clarify = view.mode === "clarify";
    const computed = view.mode === "computed";
    const blocks = [];
    if (noMatches) {
      blocks.push(askNotice({
        className: "ask-nomatch",
        icon: "search",
        title: t("ask.nomatch.title"),
        body: t("ask.nomatch.body"),
        extra: h("p", { class: "fine", text: t("ask.nomatch.hint") }),
      }));
    } else if (companyUnavailable) {
      blocks.push(askNotice({
        title: t("ask.company.unavailable.title"),
        body: t("ask.company.unavailable.body"),
      }));
    } else if (computed) {
      blocks.push(computedAnswerCard(view));
    } else if (clarify) {
      blocks.push(clarifyAnswerCard(view, options));
    } else if (view.mode === "generated" && view.answer) {
      blocks.push(askAnswerContentNode(view.answer, view.citations));
      if (view.notice === "answer_language") blocks.push(askNoticeCard(view, options));
    } else if (view.mode === "structured") {
      blocks.push(h("p", { class: "ask-sentence", dir: "auto", text: structuredSentence(view) }));
      if (view.intent === "topic" && view.topic && Number(view.topic.scanned) > 0) {
        blocks.push(h("p", { class: "fine ask-topic-coverage", text: t("ask.topic.coverage", {
          count: Number(view.topic.classified) || 0,
          classified: Number(view.topic.classified) || 0,
          total: Number(view.topic.scanned),
        }) }));
      }
    } else {
      blocks.push(askNoticeCard(view, options));
    }
    const citations = noMatches || clarify || companyUnavailable ? null : askCitationList(view);
    if (citations) blocks.push(citations);
    const generated = view.mode === "generated";
    const footerNodes = [];
    if (past) {
      footerNodes.push(h("span", {
        class: "fine ask-past-note",
        text: pastTime ? t("ask.past", { time: pastTime }) : t("ask.past.no_time"),
      }));
    }
    if (!noMatches && !companyUnavailable) {
      footerNodes.push(pill(askModeLabel(view.mode), generated || computed ? "accent" : "pending"));
      const route = String(view.route || "auto");
      if (route === "auto" || route === "local" || route === "remote") {
        footerNodes.push(pill(t("ask.route." + route), route === "remote" ? "accent" : "pending"));
      }
    }
    if (computed && !companyUnavailable) {
      footerNodes.push(h("span", { class: "fine ask-local-note", text: t("ask.footer.computed") }));
      if (view.provider && view.model) {
        footerNodes.push(h("span", { class: "fine", text: t("ask.footer.interpreted", { provider: view.provider, model: view.model }) }));
      } else {
        footerNodes.push(h("span", { class: "fine ask-local-note", text: t("ask.footer.local_query") }));
      }
    } else if (!clarify) {
      const providerCalled = generated || view.notice === "qa_generation_failed" || view.notice === "qa_no_grounded_answer";
      if (generated && view.provider && view.model) {
        footerNodes.push(h("span", { class: "fine", text: t("ask.footer.model", { provider: view.provider, model: view.model }) }));
      }
      if (!providerCalled) footerNodes.push(h("span", { class: "fine ask-local-note", text: t("ask.footer.local") }));
    }
    if (!noMatches && !clarify && Number.isFinite(Number(view.took_ms))) {
      footerNodes.push(h("span", { class: "fine", text: t("ask.footer.took", { duration: formatDuration(Number(view.took_ms) * 1e6) }) }));
    }
    if (view.preview) footerNodes.push(previewPill());
    const footer = h("div", { class: "ask-answer-foot" }, footerNodes);
    return h("article", { class: "ask-answer", "aria-labelledby": headingId, dataset: { answerId: String(view.id || "") } },
      heading, blocks, footer);
  }

  function askErrorCard(error) {
    const failure = apiFailure(error);
    const code = error instanceof ApiError ? error.code : "";
    if (code === "qa_disabled") {
      return h("article", { class: "ask-answer ask-answer-error", role: "alert" },
        h("h3", { class: "ask-answer-title", text: t("ask.disabled.title") }),
        h("p", { text: t("ask.disabled.body") }),
        link(t("ask.disabled.action"), routeHash("settings", { tab: "general", q: "qa.enabled" }), { small: true, kind: "primary" }));
    }
    if (code === "learning_locked") {
      return h("article", { class: "ask-answer ask-answer-error", role: "alert" },
        h("h3", { class: "ask-answer-title", text: t("ask.locked.title") }),
        h("p", { text: t("ask.locked.body") }),
        link(t("ask.locked.action"), "#/learning", { small: true, kind: "primary" }));
    }
    if (code === "qa_route_unavailable") {
      return h("article", { class: "ask-answer ask-answer-error", role: "alert" },
        h("h3", { class: "ask-answer-title", text: t("ask.route.unavailable.title") }),
        h("p", { text: t("ask.route.unavailable.body") }),
        link(t("ask.route.unavailable.action"), routeHash("settings", { tab: "providers" }), { small: true, kind: "primary" }));
    }
    // A timed-out send is not a failed answer: the provider may still have
    // produced and stored one. The card says what is known without claiming
    // a failure the client cannot observe.
    const noResponse = isMutationTimeout(error);
    return h("article", { class: "ask-answer ask-answer-error", role: noResponse ? "status" : "alert" },
      h("h3", { class: "ask-answer-title", text: noResponse ? t("error.no_response") : t("ask.error.title") }),
      h("p", { text: failure.message }),
      failure.diagnostic ? diagnosticDetails(failure.diagnostic, { paragraphClass: "fine" }) : null);
  }

  function askThinkingNode() {
    const label = h("span", { class: "thinking-label", text: t("ask.thinking.searching") });
    const node = h("div", { class: "thinking-card", role: "status" },
      h("span", { class: "thinking-pulse", "aria-hidden": "true" }),
      h("span", { class: "thinking-lines", "aria-hidden": "true" }));
    node.append(label);
    window.setTimeout(() => {
      if (node.isConnected) label.textContent = t("ask.thinking.writing");
    }, 1100);
    return node;
  }

  // createAskThreadHead is the thread identity above the transcript: a
  // current conversation, a new one, or a saved answer. It lives outside the
  // composer so identity and destination are never the same small strip.
  function createAskThreadHead(onNew) {
    const kicker = h("p", { class: "ask-thread-kicker" });
    const title = h("p", { class: "ask-thread-title", dir: "auto" });
    const meta = h("p", { class: "ask-thread-meta" });
    const action = button(t("ask.conversation.start_new"), onNew, {
      small: true,
      kind: "quiet",
      icon: "plus",
      title: t("ask.conversation.start_new") + " (Alt+N)",
    });
    action.setAttribute("aria-keyshortcuts", "Alt+N");
    const node = h("header", { class: "ask-thread-head", "aria-live": "polite" },
      h("div", { class: "ask-thread-copy" }, kicker, title, meta),
      action);
    function sync(mode, question, exchanges, busy) {
      node.dataset.askMode = mode;
      kicker.textContent = t(mode === "continue" ? "ask.thread.current"
        : mode === "saved" ? "ask.thread.saved" : "ask.thread.new");
      title.textContent = question || t("ask.thread.new_title");
      if (question) title.title = question;
      else title.removeAttribute("title");
      meta.textContent = mode === "continue"
        ? t("ask.thread.count", { count: exchanges })
        : mode === "saved" ? t("ask.thread.saved_hint") : t("ask.thread.new_hint");
      action.hidden = mode === "new";
      action.disabled = busy;
    }
    return { node, sync };
  }

  function askQuestionNode(question) {
    return h("div", { class: "ask-question ask-turn-question" },
      h("span", { class: "ask-kicker", text: t("ask.question_label") }),
      h("p", { class: "ask-question-text", dir: "auto", text: question }));
  }

  function focusAskAnswer(node) {
    const heading = node.querySelector(".ask-answer-title");
    if (heading) heading.focus({ preventScroll: false });
  }

  function scrollToAskNewest(node) {
    if (!node || typeof node.scrollIntoView !== "function") return;
    node.scrollIntoView({ block: "nearest" });
  }

  function askEarlierBar(onBack) {
    return h("div", { class: "ask-earlier", role: "status" }, icon("answers"),
      h("span", { class: "ask-earlier-text", text: t("ask.earlier.text") }),
      h("span", { class: "spacer" }),
      button(t("ask.earlier.back"), onBack, { small: true, kind: "quiet" }));
  }

  function createAskWorkspace(options = {}) {
    const variant = options.variant || "ask";
    const providerState = String(options.providerState || "unknown");
    const composer = createAskComposer(options, () => submit());
    const { input, errorLine, send, sendLabel, routeSelect, destination } = composer;
    const current = h("div", { class: "ask-current ask-thread", "aria-live": "polite", "aria-atomic": "false" });
    let busy = false;
    let currentView = null;
    let conversationId = "";
    let turns = [];
    let saved = false;

    const threadHead = createAskThreadHead(() => startNewQuestion());
    const root = h("div", { class: "ask-workspace ask-workspace-" + variant },
      h("h2", { class: "sr-only", text: t("ask.workspace.title") }),
      threadHead.node,
      current,
      composer.node);

    function syncConversation() {
      const active = conversationId !== "" && turns.length > 0;
      const mode = saved ? "saved" : active ? "continue" : "new";
      root.dataset.askMode = mode;
      composer.node.dataset.askMode = mode;
      sendLabel.textContent = t(active ? "ask.conversation.reply" : "ask.send");
      send.disabled = busy;
      if (mode === "continue") destination.textContent = t("ask.composer.continue");
      else if (mode === "saved") destination.textContent = t("ask.composer.saved");
      else destination.textContent = t("ask.composer.new");
      threadHead.sync(mode,
        mode === "saved" ? (currentView && currentView.question) || "" : active ? turns[0].question : "",
        turns.length, busy);
    }
    syncConversation();

    // resetThread empties the visible transcript but leaves the conversation
    // id to its caller: a new question and a new view both decide whether
    // the next turn continues the old thread.
    function resetThread() {
      clear(current);
      turns = [];
      currentView = null;
    }

    function answerCardFor(view, settings = {}) {
      return askAnswerCard(view, {
        onClarify: (question) => submit(question),
        onRetry: (question) => submit(question),
        past: settings.past === true,
        providerState,
      });
    }

    function rememberTurn(question, view, turn, settings = {}) {
      turns.push({ question, view, node: turn });
      syncConversation();
      if (settings.focus) focusAskAnswer(turn);
      if (settings.scroll !== false) scrollToAskNewest(turn);
    }

    function appendAnswerTurn(question, view, settings = {}) {
      const answerSlot = h("div", { class: "ask-answer-slot" });
      const turn = h("article", {
        class: "ask-turn",
        dataset: { answerId: String(view.id || "") },
      }, askQuestionNode(question), answerSlot);
      answerSlot.append(answerCardFor(view, settings));
      current.append(turn);
      rememberTurn(question, view, turn, settings);
      return turn;
    }

    function showThinking(question) {
      currentView = null;
      const answerSlot = h("div", { class: "ask-answer-slot" });
      const turn = h("article", { class: "ask-turn ask-turn-pending" },
        askQuestionNode(question), answerSlot);
      answerSlot.append(askThinkingNode());
      current.append(turn);
      scrollToAskNewest(turn);
      if (typeof options.onPending === "function") options.onPending(question);
      return { turn, answerSlot };
    }

    function showView(view, settings = {}) {
      if (!view) return null;
      resetThread();
      // A view loaded from history is displayed on its own, not resumed:
      // the first submit in this workspace always starts a fresh thread and
      // has no prior context to inherit from a possibly older turn.
      saved = true;
      conversationId = "";
      currentView = view;
      if (settings.earlier) current.append(askEarlierBar(() => { if (typeof options.onBackToLatest === "function") options.onBackToLatest(); }));
      const turn = appendAnswerTurn(view.question || t("common.unknown"), view, {
        focus: settings.focus,
        scroll: false,
        past: settings.past === true,
      });
      syncConversation();
      return turn;
    }

    function showEmpty() {
      resetThread();
      saved = false;
      conversationId = "";
      const suggestions = h("div", { class: "ask-empty-suggestions", role: "group", "aria-label": t("ask.empty.suggestions_aria") },
        ASK_SUGGESTIONS.map((key) => button(t(key), () => submit(t(key)), { small: true, kind: "quiet" })));
      current.append(emptyState(t("ask.empty.title"), t("ask.empty.body"), suggestions, "ask"));
      syncConversation();
    }

    // A new conversation preserves whatever the owner has already typed:
    // clearing the draft would discard work the owner did not ask to lose.
    function startNewQuestion() {
      if (busy) return;
      markFieldError(input, errorLine, "");
      showEmpty();
      input.focus();
      if (typeof options.onNewQuestion === "function") options.onNewQuestion();
    }

    async function submit(value) {
      if (busy) return;
      if (typeof value === "string" && value.trim()) input.value = value;
      const question = input.value.trim();
      markFieldError(input, errorLine, "");
      if (question.length < 3) {
        markFieldError(input, errorLine, t("ask.error.short"));
        input.focus();
        return;
      }
      // A follow-up only appends when the backend receives the same
      // conversation id. A saved answer, an empty panel, or a historical
      // answer whose conversation is unknown starts a fresh transcript
      // instead of implying chat context the planner never saw.
      const continuing = !saved && conversationId !== "" && turns.length > 0;
      if (!continuing) { saved = false; resetThread(); conversationId = ""; syncConversation(); }
      busy = true;
      syncConversation();
      input.disabled = true;
      const placement = showThinking(question);
      let answered = false;
      try {
        const language = I18N.snapshot().language === "fr" ? "fr" : "en";
        const payload = { question, language, route: routeSelect.value };
        if (conversationId) payload.conversation_id = conversationId;
        // A generated answer is the one paid call this screen makes: the
        // ordinary 30 s read bound would abort a legitimate cold generation.
        const view = await request("/api/ask", { method: "POST", body: payload, timeoutMs: LONG_REQUEST_TIMEOUT_MS });
        noteAskAnswer(view);
        conversationId = String(view.conversation_id || conversationId || "");
        saved = false;
        syncConversation();
        input.value = "";
        clear(placement.answerSlot);
        placement.turn.classList.remove("ask-turn-pending");
        placement.answerSlot.append(answerCardFor(view));
        rememberTurn(question, view, placement.turn, { focus: false });
        currentView = view;
        answered = true;
        // The answer is above the composer; focus returns to the input so a
        // follow-up is one keystroke away. aria-live announces the answer.
        if (input.isConnected) input.focus();
        if (typeof options.onAnswered === "function") options.onAnswered(view);
      } catch (error) {
        currentView = null;
        syncConversation();
        clear(placement.answerSlot);
        placement.turn.classList.remove("ask-turn-pending");
        placement.answerSlot.append(askErrorCard(error));
      } finally {
        busy = false;
        syncConversation();
        if (input.isConnected) {
          input.disabled = false;
          if (!answered) input.focus();
        }
      }
    }

    root.addEventListener("keydown", (event) => {
      if (!event.altKey || event.ctrlKey || event.metaKey || event.key.toLowerCase() !== "n") return;
      if (root.dataset.askMode === "new") return;
      event.preventDefault();
      startNewQuestion();
    });
    input.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
      event.preventDefault();
      submit();
    });
    input.addEventListener("input", () => markFieldError(input, errorLine, ""));

    return {
      node: root,
      input,
      current,
      currentView: () => currentView,
      focus: () => { input.focus(); input.select(); },
      showView,
      showEmpty,
      submit: (value) => submit(value),
    };
  }

  // askAutoView chooses what the composer shows on an initial Ask visit.
  // A saved answer that only describes a transient fallback (no provider, a
  // warming index, a failed call or an ungrounded answer) is history, not the
  // current state: the composer stays empty until the owner opens it.
  function askAutoView(view) {
    if (!view) return null;
    switch (view.notice) {
      case "qa_unavailable":
      case "qa_index_warming":
      case "qa_retrieval_timeout":
      case "qa_generation_failed":
      case "qa_no_grounded_answer":
        return null;
      default:
        return view;
    }
  }

  async function renderAsk(route, seq) {
    const [gateRead, provider, askHistoryRead] = await Promise.all([
      readGate(),
      readAnswerProvider(),
      loadAskHistory().catch((error) => {
        loadAskHistoryError = error;
        return [];
      }),
    ]);
    if (seq !== state.renderSeq) return h("div");
    // An unknown gate is unknown, not closed: withhold the permission pill
    // while the screen's own reads still paint. A known snapshot stays the
    // last answer even when this refresh failed.
    const permissionKnown = !!gateRead.value || gateKnown();
    const open = permissionKnown && gateOpen();
    const preview = permissionKnown && gatePreview();
    const providerState = provider.state;
    const providerName = provider.name;
    setTopbarMeta(permissionKnown ? gatePill(open) : null, permissionKnown && preview && !open ? previewPill() : null);
    if (!open && !preview) {
      return h("div", { class: "screen ask-screen" },
        emptyState(t("ask.locked.title"), t("ask.locked.body"), [
          link(t("ask.locked.action"), "#/learning", { kind: "primary", small: true }),
          button(t("preview.hub_action"), () => enablePreviewDialog(), { small: true }),
        ], "lock", { hostClass: "ask-locked surface" }));
    }
    let askHistory = null;
    const workspace = createAskWorkspace({
      variant: "ask",
      inputId: "ask-input",
      providerState,
      providerName,
      onPending: () => {
        askHistory.updateSelection();
        askHistory.syncRoute("");
      },
      onAnswered: () => {
        askHistory.render();
        askHistory.syncRoute("");
      },
      onNewQuestion: () => {
        askHistory.updateSelection();
        askHistory.syncRoute("");
      },
      onBackToLatest: async () => {
        const latest = await askHistory.latestFullView();
        if (latest) {
          workspace.showView(latest, { focus: true, earlier: false, past: true });
          askHistory.updateSelection();
          askHistory.syncRoute("");
        } else {
          workspace.showEmpty();
          askHistory.updateSelection();
          askHistory.syncRoute("");
        }
      },
    });
    askHistory = createAskHistoryRail(workspace);
    const requestedId = route.params.get("id") || "";
    const requested = await askHistory.selectRequested(requestedId);
    let selected = requested.view;
    const earlier = requested.earlier;
    if (!selected) selected = askAutoView(await askHistory.latestFullView());
    if (seq !== state.renderSeq) return h("div");
    if (selected) workspace.showView(selected, { focus: false, earlier, past: !!requestedId });
    else workspace.showEmpty();
    if (requestedId && !requested.found) askHistory.syncRoute("");
    askHistory.render();
    consumeAskPrefill(workspace.input);
    const main = h("div", { class: "ask-main section-scroll", dataset: { scrollKey: "ask-main" } }, workspace.node);
    return h("div", { class: "screen ask-screen" },
      h("div", { class: "ask-layout" }, main,
        h("aside", { class: "ask-history", "aria-label": t("ask.history.title") }, askHistory.node)));
  }

  let loadAskHistoryError = false;

