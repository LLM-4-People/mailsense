  // createAskComposer owns the input, the per-send route selector, the
  // destination line and the send button. The workspace owns thread state;
  // the composer only renders it and reports the selected route.
  function createAskComposer(options, onSubmit) {
    const variant = options.variant || "ask";
    const inputId = options.inputId || "ask-input";
    const providerName = String(options.providerName || "").trim();
    const providerState = String(options.providerState || "unknown");
    const input = h("textarea", {
      id: inputId,
      class: "textarea ask-input",
      rows: "2",
      maxlength: "500",
      placeholder: t("ask.placeholder"),
      "aria-label": t("ask.input_aria"),
      dir: "auto",
      spellcheck: "true",
    });
    const errorLine = h("div", { class: "field-error", role: "alert", hidden: true });
    const send = button(t("ask.send"), () => onSubmit(), { kind: "primary", icon: "ask" });
    const sendLabel = send.querySelector("span");
    const routeSelect = h("select", {
      class: "select compact ask-route-select",
      "aria-label": t("ask.route.aria"),
    },
    h("option", { value: "auto", text: t("ask.route.auto") }),
    h("option", { value: "local", text: t("ask.route.local") }),
    h("option", {
      value: "remote",
      text: providerState === "available"
        ? (providerName ? t("ask.route.remote_named", { provider: providerName }) : t("ask.route.remote"))
        : providerState === "unconfigured" ? t("ask.route.remote_unconfigured")
          : providerState === "unavailable" ? t("ask.route.remote_unavailable")
            : t("ask.route.remote_unknown"),
      disabled: providerState !== "available",
    }));
    const routeHint = h("p", { class: "ask-route-hint" });
    const destination = h("p", { class: "ask-destination" });
    const remoteHintKeys = {
      unconfigured: "ask.route.hint.remote_unconfigured",
      unavailable: "ask.route.hint.remote_unavailable",
      unknown: "ask.route.hint.remote_unknown",
    };
    function syncRouteHint() {
      if (routeSelect.value === "remote" && providerState !== "available") {
        routeHint.textContent = t(remoteHintKeys[providerState] || "ask.route.hint.remote_unknown");
        return;
      }
      routeHint.textContent = t("ask.route.hint." + routeSelect.value, {
        provider: providerName || t("ask.route.remote"),
      });
    }
    routeSelect.addEventListener("change", syncRouteHint);
    syncRouteHint();

    const chips = h("div", { class: "ask-suggestions", role: "group", "aria-label": t("ask.suggestions_aria") },
      ASK_SUGGESTIONS.map((key) => {
        const label = t(key);
        return h("button", { type: "button", class: "chip chip-button", text: label, onclick: () => onSubmit(label) });
      }));

    const privacy = variant === "ask"
      ? h("p", { class: "ask-privacy fine" },
          h("span", { text: t("ask.privacy.text") + " " }),
          h("a", { href: routeHash("settings", { tab: "providers" }), text: t("ask.privacy.keys") }))
      : null;

    const card = h("div", { class: "ask-card" },
      h("div", { class: "ask-field" },
        h("label", { class: "sr-only", for: inputId, text: t("ask.input_aria") }),
        input),
      h("div", { class: "ask-actions" },
        h("label", { class: "ask-route" },
          h("span", { class: "ask-route-label", text: t("ask.route.label") }),
          routeSelect),
        destination,
        h("span", { class: "spacer" }),
        send),
      routeHint,
      errorLine,
      variant === "ask" ? null : chips,
      privacy);
    return { node: h("div", { class: "ask-composer" }, card), input, errorLine, send, sendLabel, routeSelect, destination };
  }

