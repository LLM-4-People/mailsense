  // ---------------------------------------------------------------- mail body

  // message-view:start
  // A message view is a usable read only when it carries its message and the
  // row that message belongs to. A 2xx response without both is not an empty
  // message; the detail must say the read failed instead of painting a stub.
  function validMessageView(view) {
    return !!view && typeof view === "object" &&
      !!view.message && typeof view.message === "object" &&
      Number(view.row_id) > 0;
  }
  // message-view:end

  // messageCardNode is the shared message header plus the one safe body
  // renderer. Labels and Answers both use it, so a message looks and behaves
  // the same wherever it is read.
  function messageCardNode(view, message) {
    return h("article", { class: "mail-card" },
      h("div", { class: "mail-card-head" },
        h("h2", { class: "mail-subject", dir: "auto", text: message.subject || t("mail.no_subject") }),
        h("div", { class: "mail-meta" },
          h("span", { dir: "auto", text: metaLine([message.sender_name, message.sender_address]) || t("mail.sender_unknown") }),
          h("span", { text: formatDate(message.received_at || message.sent_on) }),
          h("span", { text: message.folder_path || message.folder_role || "" }),
          h("span", { text: t("mail.message_number", { id: formatId(view.row_id) }) })),
        h("div", { class: "chips" },
          view && view.sender && view.sender.on_behalf_of
            ? badge(t("labels.mail.on_behalf_of", { address: view.sender.from_address }), "warn",
              t("labels.mail.on_behalf_of_hint", { address: view.sender.from_address, sender: view.sender.sender_address }))
            : null,
          message.partial ? badge(t("labels.mail.partial"), "warn") : null,
          message.body_truncated ? badge(t("labels.mail.body_truncated"), "warn") : null,
          view.body_html_truncated ? badge(t("labels.mail.html_truncated"), "warn") : null,
          message.has_attachments ? badge(t("labels.mail.attachments")) : null)),
      attachmentList(message.attachments),
      mailBodyNode(view, message));
  }

  // mail-frame:start
  // One resize listener serves every live mail frame. A listener per message
  // outlived its detached iframe until the next resize, so a session kept one
  // closure and document per message opened.
  const mailFrames = new Set();
  let mailResizeBound = false;

  function trackMailFrame(frame, place) {
    for (const entry of mailFrames) {
      if (!entry.frame.isConnected) mailFrames.delete(entry);
    }
    mailFrames.add({ frame, place });
    if (mailResizeBound) return;
    mailResizeBound = true;
    window.addEventListener("resize", () => {
      for (const entry of mailFrames) {
        if (!entry.frame.isConnected) { mailFrames.delete(entry); continue; }
        entry.place();
      }
    });
  }
  // mail-frame:end

  // body_html is produced by internal/render and is the only HTML string the
  // shell inserts. It goes into a sandboxed, script-free iframe's srcdoc.
  // The base sheet (mail.css) and the message's compiled sender sheet are the
  // only stylesheets; the sender sheet is a blob URL built from body_css, so
  // no inline style attribute ever exists. Inline images are spans carrying
  // their attachment position; the shell fetches each one with the bearer
  // token and swaps in an ephemeral blob <img>. The parent keeps same-origin
  // access to measure the frame and intercept links, but the frame has no
  // script, form, popup or top-navigation permission.
  // mailBodySeq keeps each rendered body's frame id unique, so a
  // disclosure button points at the frame it actually controls.
  let mailBodySeq = 0;

  function mailBodyNode(view, message) {
    const fragment = view && typeof view.body_html === "string" ? view.body_html : "";
    if (!fragment) {
      if (message && message.body) {
        return h("div", { class: "mail-body", dir: "auto", text: String(message.body) });
      }
      return h("div", { class: "mail-body empty", text: t("mail.no_body") });
    }
    const rowID = Number(view && view.row_id);
    const wrap = h("div", { class: "mail-frame-wrap" });
    mailBodySeq += 1;
    const frameID = "mail-body-" + String(rowID || "x") + "-" + String(mailBodySeq);
    const frame = h("iframe", {
      id: frameID,
      class: "mail-frame",
      sandbox: "allow-same-origin",
      title: t("mail.message_body_title"),
      referrerpolicy: "no-referrer",
    });
    frame.addEventListener("load", () => {
      const doc = frame.contentDocument;
      if (!doc) return;
      // A key event inside this frame never reaches the shell document, so
      // the shared shortcut handler listens here too ("/" and Ctrl/Cmd+K).
      doc.addEventListener("keydown", handleGlobalShortcut);
      doc.addEventListener("click", (event) => {
        const anchor = event.target && event.target.closest ? event.target.closest("a[href]") : null;
        if (!anchor) return;
        event.preventDefault();
        openLinkDialog(anchor.getAttribute("href"));
      });
      const place = () => placeMailFrame(frame, wrap, toggle);
      applyMailCSS(doc, view && view.body_css, place);
      hydrateInlineAssets(doc, rowID, place);
      place();
      if (doc.fonts && doc.fonts.ready) doc.fonts.ready.then(place).catch(() => {});
      trackMailFrame(frame, place);
    });
    frame.srcdoc = mailDocument(fragment);
    const toggle = button(t("mail.show_full"), () => {
      const expanded = wrap.classList.toggle("expanded");
      wrap.classList.toggle("clipped", !expanded);
      toggle.setAttribute("aria-expanded", expanded ? "true" : "false");
      const label = toggle.querySelector("span");
      if (label) label.textContent = expanded ? t("mail.collapse") : t("mail.show_full");
    }, { small: true, kind: "quiet" });
    // The disclosure state is announced, not only painted; the controlled
    // frame is the one this node actually wraps.
    toggle.setAttribute("aria-controls", frameID);
    toggle.setAttribute("aria-expanded", "false");
    toggle.hidden = true;
    wrap.append(frame);
    const host = h("div", { class: "mail-body-host" }, wrap, h("div", { class: "mail-frame-actions" }, toggle));
    const blocked = Number(view && view.body_blocked_images) || 0;
    if (blocked > 0) {
      host.append(h("div", { class: "mail-notice" }, icon("alert"),
        h("span", { text: t("mail.blocked_images", { count: blocked }) })));
    }
    return host;
  }

  // mailDocument wraps the sanitized fragment in a document with a strict
  // policy of its own. The parent CSP also applies to srcdoc; this is the
  // second layer when the frame is opened outside the main page.
  function mailDocument(fragment) {
    return "<!doctype html><html data-theme=\"light\"><head><meta charset=\"utf-8\">" +
      "<meta name=\"color-scheme\" content=\"light\">" +
      "<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'self' blob:; img-src 'self' data: blob:; script-src 'none'; form-action 'none'; base-uri 'none'\">" +
      "<link rel=\"stylesheet\" href=\"/mail.css\"></head><body dir=\"auto\">" + fragment + "</body></html>";
  }

  // applyMailCSS links the compiled sender sheet after mail.css, so the
  // message's own typography and layout win at equal specificity. The blob
  // URL is revoked once the sheet has loaded: the rules are already applied,
  // and a rerender of the same message builds a fresh one.
  function applyMailCSS(doc, raw, place) {
    if (!doc.head || typeof raw !== "string" || !raw) return;
    const url = URL.createObjectURL(new Blob([raw], { type: "text/css" }));
    const link = doc.createElement("link");
    link.rel = "stylesheet";
    link.href = url;
    const done = () => { URL.revokeObjectURL(url); place(); };
    link.addEventListener("load", done, { once: true });
    link.addEventListener("error", done, { once: true });
    doc.head.appendChild(link);
  }

  // hydrateInlineAssets swaps each data-inline span for a real image. The
  // bytes travel over the ordinary bearer API and become a blob URL scoped to
  // this document, so the launch token never appears in an image URL and a
  // message can only load the assets stored with it.
  function hydrateInlineAssets(doc, rowID, place) {
    if (!Number.isInteger(rowID) || rowID < 1 || !state.token) return;
    const nodes = Array.prototype.slice.call(doc.querySelectorAll("[data-inline]"));
    for (const node of nodes) {
      const position = Number(node.getAttribute("data-inline"));
      if (!Number.isInteger(position) || position < 0) continue;
      const sha = String(node.getAttribute("data-inline-v") || "");
      const alt = String(node.getAttribute("aria-label") || node.textContent || "");
      inlineAssetURL(rowID, position, sha).then((url) => {
        if (!node.isConnected) { URL.revokeObjectURL(url); return; }
        const img = doc.createElement("img");
        img.alt = alt;
        img.decoding = "async";
        img.addEventListener("load", () => { URL.revokeObjectURL(url); place(); }, { once: true });
        img.addEventListener("error", () => { URL.revokeObjectURL(url); markInlineMissing(doc, node); place(); }, { once: true });
        img.src = url;
        node.removeAttribute("role");
        node.removeAttribute("aria-label");
        node.replaceChildren(img);
      }).catch(() => { markInlineMissing(doc, node); place(); });
    }
  }

  async function inlineAssetURL(rowID, position, sha) {
    const query = sha ? "?v=" + encodeURIComponent(sha) : "";
    // The attachment read uses the one transport owner: its timeout, abort,
    // token refresh and error mapping are the same as every other read.
    const blob = await request(
      "/api/messages/" + encodeURIComponent(rowID) + "/attachments/" + encodeURIComponent(position) + query,
      { as: "blob", cache: "default" });
    if (["image/png", "image/jpeg", "image/gif", "image/webp"].indexOf(blob.type) === -1) {
      throw new Error("unsupported asset");
    }
    return URL.createObjectURL(blob);
  }

  function markInlineMissing(doc, node) {
    if (!node.isConnected) return;
    node.setAttribute("data-inline-missing", "1");
    node.classList.add("mail-inline-missing");
    node.textContent = t("mail.inline_unavailable");
  }

  function placeMailFrame(frame, wrap, toggle) {
    const doc = frame.contentDocument;
    if (!doc) return;
    // documentElement.scrollHeight is at least the iframe viewport (150px on
    // a fresh frame); the body is the content and keeps short mail compact.
    const content = doc.body ? Math.ceil(doc.body.getBoundingClientRect().height)
      : (doc.documentElement ? doc.documentElement.scrollHeight : 0);
    // The CSP allows no inline style: size the frame with its height
    // attribute, which is a presentational attribute, not a style rule.
    frame.height = String(Math.min(Math.max(content, 48), 15000));
    const tall = content > Math.round(window.innerHeight * 0.7);
    toggle.hidden = !tall;
    if (!tall) {
      wrap.classList.remove("clipped", "expanded");
      return;
    }
    wrap.classList.toggle("clipped", !wrap.classList.contains("expanded"));
  }

  // A click on a message link never navigates the app or a frame: it shows
  // the exact destination and offers Copy. There is no OS opener seam in
  // this build, so the user opens the copied address in their own browser.
  function openLinkDialog(raw) {
    let target;
    try { target = new URL(String(raw)); } catch { return; }
    if (target.protocol !== "http:" && target.protocol !== "https:" && target.protocol !== "mailto:") return;
    const body = h("div", { class: "link-dialog" },
      h("p", { text: t("action_dialog.link.body") }),
      h("p", { class: "mono wrap link-target", text: target.href }),
      h("p", { class: "fine", text: t("action_dialog.link.note") }));
    openDialog({
      title: t("action_dialog.link.title"),
      eyebrow: t("action_dialog.link.eyebrow"),
      body,
      confirm: t("action_dialog.link.copy"),
      cancel: t("action_dialog.link.close"),
      onConfirm: async () => {
        try {
          await navigator.clipboard.writeText(target.href);
          toast(t("action_dialog.link.copied"), "info", t("action_dialog.link.copied_body"));
        } catch {
          toast(t("action_dialog.link.copy_failed"), "warn", t("action_dialog.link.copy_failed_body"));
        }
        return true;
      },
    });
  }

  function attachmentList(attachments) {
    if (!attachments || !attachments.length) return null;
    return h("div", { class: "attachments", title: t("mail.attachment_title") },
      attachments.map((att) => badge(metaLine([
        att.inline ? t("mail.attachment_inline") : "",
        att.name || t("mail.attachment_unnamed"),
        att.size >= 0 ? formatBytes(att.size) : "",
      ]), att.inline ? "plain" : "", att.content_type || "")));
  }

