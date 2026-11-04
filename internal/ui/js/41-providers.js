  // invalidateProviderChange is the one invalidation of a provider, key or
  // role write. The three call sites each spelled a subset; the save/delete
  // pair forgot the Brief and Ask history, and the role list forgot the
  // mailbox, so a screen could keep serving the previous provider.
  function invalidateProviderChange() {
    invalidateAllData();
  }

  // The server's providers view is the one provider catalog. These three
  // module values are the loaded view, never a second declaration of it.
  let providerProtocolNames = [];
  let providerProtocolKinds = new Map();
  let providerRoles = [];

  function applyProviderCatalog(view) {
    const protocols = view && Array.isArray(view.protocols) ? view.protocols : [];
    providerProtocolNames = protocols.map((protocol) => String((protocol && protocol.name) || "")).filter(Boolean);
    providerProtocolKinds = new Map(protocols.map((protocol) => [protocol.name, protocol.kind]));
    if (!providerProtocolNames.length) {
      // An older/direct response may omit the catalog; the records still
      // name the protocols in use, so an edit form can round-trip them.
      const records = view && Array.isArray(view.providers) ? view.providers : [];
      providerProtocolNames = [...new Set(records.map((record) => record.protocol).filter(Boolean))];
    }
    providerRoles = view && Array.isArray(view.roles) ? view.roles : [];
  }

  function providerKind(protocol) {
    return providerProtocolKinds.get(protocol) || "";
  }

  function providerRoleLabel(role) {
    const key = PROVIDER_ROLE_LABEL_KEYS[role && role.setting];
    return key ? t(key) : String((role && role.label) || (role && role.setting) || "");
  }

  function orderedProviderRoles() {
    const bySetting = new Map(providerRoles.map((role) => [role.setting, role]));
    const out = [];
    for (const setting of PROVIDER_ROLE_ORDER) {
      const role = bySetting.get(setting);
      if (role) {
        out.push(role);
        bySetting.delete(setting);
      }
    }
    for (const role of bySetting.values()) out.push(role);
    return out;
  }

  function providerKindLabel(kind) {
    return tOr("settings.providers.kind." + kind, kind);
  }

  function providerFieldLabel(meta) {
    return tOr("settings.providers.field." + meta.name.replace(/\./g, "_"), meta.name);
  }

  function providerFieldsForKind(kind) {
    return PROVIDER_ADVANCED_GROUPS.flatMap((group) => group.fields).filter((field) => field.kinds.includes(kind));
  }

  function providerValue(record, path) {
    if (!record) return undefined;
    let value = record;
    for (const part of path.split(".")) {
      if (value === null || value === undefined || typeof value !== "object") return undefined;
      value = value[part];
    }
    return value;
  }

  function providerSlug(label) {
    return String(label || "").normalize("NFD").replace(/[\u0300-\u036f]/g, "")
      .toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  }

  function providerSecretSuggestion(id) {
    const slug = String(id || "").trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "");
    return slug ? "PROVIDER_" + slug + "_API_KEY" : "";
  }

  function providerListFrom(providers) {
    if (Array.isArray(providers)) return providers;
    return providers && Array.isArray(providers.providers) ? providers.providers : [];
  }

  function providerRoleSelect(role, current, providers) {
    const currentId = current === null || current === undefined ? "" : String(current);
    const list = providerListFrom(providers)
      .filter((record) => (record.kind || providerKind(record.protocol)) === role.kind);
    const hasCurrent = currentId !== "" && list.some((record) => (record.id || "") === currentId);
    const options = [h("option", { value: "", text: t("settings.providers.role.unconfigured"), selected: currentId === "" })];
    for (const record of list) {
      const id = record.id || "";
      if (!id) continue;
      options.push(h("option", { value: id, text: record.label || id, selected: id === currentId }));
    }
    if (currentId && !hasCurrent) {
      options.push(h("option", { value: currentId, text: t("settings.providers.role.missing", { id: currentId }), selected: true }));
    }
    const select = h("select", { class: "select", "data-role": role.setting, "data-kind": role.kind, "aria-label": providerRoleLabel(role) }, options);
    select.value = currentId;
    return select;
  }

  function providerPayload(values, protocol) {
    const kind = providerKind(protocol);
    const relevant = new Set(providerFieldsForKind(kind).map((field) => field.name));
    const numberValue = (name, fallback) => {
      if (!relevant.has(name)) return fallback;
      const value = values[name];
      return value === null || value === undefined ? fallback : value;
    };
    const textValue = (name) => {
      if (!relevant.has(name)) return "";
      const value = values[name];
      return value === null || value === undefined ? "" : String(value);
    };
    const priceValue = (name) => {
      if (!relevant.has(name)) return null;
      const value = values[name];
      return value === null || value === undefined ? null : value;
    };
    const listValue = (name) => {
      if (!relevant.has(name)) return [];
      const value = values[name];
      return Array.isArray(value) ? value : [];
    };
    const retry = {};
    const attempts = numberValue("retry.max_attempts", null);
    if (attempts !== null && attempts > 0) retry.max_attempts = attempts;
    const statuses = listValue("retry.statuses");
    if (statuses.length) retry.statuses = statuses;
    const quotaCodes = listValue("retry.quota_codes");
    if (quotaCodes.length) retry.quota_codes = quotaCodes;
    const payload = {
      id: String(values.id || "").trim(),
      label: String(values.label || "").trim(),
      protocol,
      base_url: String(values.base_url || "").trim(),
      path: String(values.path || "").trim(),
      model: String(values.model || "").trim(),
      api_key_secret: String(values.api_key_secret || "").trim(),
      runtime: textValue("runtime"),
      context_tokens: numberValue("context_tokens", 0),
      chars_per_token: numberValue("chars_per_token", 0),
      reserve_tokens_per_byte: numberValue("reserve_tokens_per_byte", 0),
      reserve_output_tokens: numberValue("reserve_output_tokens", 0),
      max_questions: numberValue("max_questions", 0),
      probability_tolerance: numberValue("probability_tolerance", 0),
      response_format: textValue("response_format"),
      reasoning_effort: textValue("reasoning_effort"),
      price_input_per_mtok: priceValue("price_input_per_mtok"),
      price_output_per_mtok: priceValue("price_output_per_mtok"),
      retry,
      envelope: textValue("envelope"),
      noul_fold: textValue("noul_fold"),
      embedding: null,
    };
    if (kind === "embedder") {
      payload.embedding = {
        dimensions: numberValue("embedding.dimensions", 0),
        matryoshka: listValue("embedding.matryoshka"),
        query_template: textValue("embedding.query_template"),
        document_template: textValue("embedding.document_template"),
        empty_title: textValue("embedding.empty_title"),
        norm_tolerance: numberValue("embedding.norm_tolerance", 0),
        max_batch: numberValue("embedding.max_batch", 0),
        reserved_tokens: numberValue("embedding.reserved_tokens", 0),
      };
    }
    return payload;
  }

  // providerDiscoveryBody sends the key the owner typed when there is one.
  // Otherwise an existing record may use its stored key name, never its
  // value, so a public endpoint works without credentials and a secret stays
  // out of the request body.
  function providerDiscoveryBody(protocol, baseUrl, keyValue, record) {
    const body = {
      base_url: String(baseUrl || "").trim(),
      protocol: String(protocol || "").trim(),
    };
    const key = String(keyValue == null ? "" : keyValue).trim();
    if (key) {
      body.api_key = key;
    } else if (record && record.key_present === true) {
      const secret = String(record.api_key_secret || "").trim();
      if (secret) body.api_key_secret = secret;
    }
    return body;
  }

  // providerModelValues maps one official model onto the form. It is pure so
  // the mapping can be checked without a browser, and it omits every field
  // the provider did not publish instead of inventing a value.
  function providerModelValues(model, current = {}) {
    const values = {};
    if (!model) return values;
    const id = String(model.id || "").trim();
    if (!id) return values;
    values.model = id;
    const path = String(model.path || "").trim();
    if (path) values.path = path;
    const context = finiteNumber(model.context_tokens);
    if (context !== null && context > 0) values.context_tokens = context;
    const maxOutput = finiteNumber(model.max_output_tokens);
    if (maxOutput !== null && maxOutput > 0) {
      values.reserve_output_tokens = Math.min(maxOutput, PROVIDER_RESERVE_OUTPUT_LIMIT);
    }
    const priceInput = finiteNumber(model.price_input_per_mtok);
    if (priceInput !== null) values.price_input_per_mtok = priceInput;
    const priceOutput = finiteNumber(model.price_output_per_mtok);
    if (priceOutput !== null) values.price_output_per_mtok = priceOutput;
    if (model.structured_output === true) values.response_format = "json_object";
    if (model.reasoning === true) values.reasoning_effort = "low";
    if (!String(current.label == null ? "" : current.label).trim()) {
      const name = String(model.name || "").trim();
      if (name) values.label = name;
    }
    if (!String(current.chars_per_token == null ? "" : current.chars_per_token).trim()) {
      values.chars_per_token = PROVIDER_DEFAULT_CHARS_PER_TOKEN;
    }
    if (!String(current.reserve_tokens_per_byte == null ? "" : current.reserve_tokens_per_byte).trim()) {
      values.reserve_tokens_per_byte = PROVIDER_DEFAULT_RESERVE_TOKENS_PER_BYTE;
    }
    return values;
  }

  // providerTestDetail prefers the backend's stable detail code so the
  // sentence follows the interface language. An unknown code, or an older
  // result without one, keeps the server's own detail unchanged.
  function providerTestDetail(result) {
    if (!result) return "";
    const code = String(result.detail_code || "").trim();
    const key = code ? "settings.providers.test.detail." + code : "";
    if (key && I18N.has(key)) return t(key, result.detail_params || {});
    return result.detail || result.message ? String(result.detail || result.message) : "";
  }

  function providerFormNode(options = {}) {
    const record = options.record || null;
    const protocol = record && record.protocol ? record.protocol : "openai_chat";
    const controls = new Map();
    const wrapper = h("div", { class: "provider-form" });
    const formTitle = record
      ? t("settings.providers.edit_title", { name: record.label || record.id || t("settings.providers.title") })
      : t("settings.providers.new_title");
    const kindBadge = h("span", { class: "badge", text: providerKindLabel(providerKind(protocol)) });
    const kindHint = h("p", { class: "fine provider-kind-hint", text: "" });
    const errorNode = h("div", { class: "field-error provider-form-error", role: "alert", hidden: true });

    function addControl(meta, value, extra = {}) {
      let control;
      if (meta.type === "select") {
        const options = (meta.options || []).map((option) => h("option", {
          value: option,
          text: option === "" ? t("settings.providers.field.none") : option,
          selected: String(value || "") === option,
        }));
        control = h("select", { class: "select", name: meta.name }, options);
        control.value = String(value || "");
      } else {
        control = h("input", {
          class: "input",
          name: meta.name,
          type: meta.type === "password" ? "password" : (meta.type === "int" || meta.type === "float" ? "number" : "text"),
          step: meta.type === "float" ? "any" : (meta.type === "int" ? "1" : null),
          min: meta.min === undefined ? null : String(meta.min),
          max: meta.max === undefined ? null : String(meta.max),
          value: value === null || value === undefined ? "" : String(value),
          autocomplete: meta.type === "password" ? "new-password" : "off",
          disabled: !!extra.disabled,
          required: !!meta.required,
        });
      }
      control.name = meta.name;
      const hint = meta.hintKey ? t(meta.hintKey) : "";
      const label = meta.labelKey ? t(meta.labelKey) : providerFieldLabel(meta);
      const fieldNode = field(label, control, hint);
      fieldNode.classList.add("provider-field");
      fieldNode.dataset.field = meta.name;
      if (extra.wide) fieldNode.classList.add("span-2");
      controls.set(meta.name, { meta, control, node: fieldNode, advanced: !!meta.advanced, record: !!meta.record });
      return fieldNode;
    }

    // Everyday fields, including the request path: hidden required inputs
    // inside a closed disclosure made the default form unsaveable. The record
    // id is generated from the label; limits and capability fields keep their
    // defaults behind Advanced.
    const basicFields = [
      { name: "label", type: "text", required: true },
      { name: "protocol", type: "select", options: providerProtocolNames },
      { name: "base_url", type: "text", hintKey: "settings.providers.field.base_url_hint" },
      { name: "path", type: "text", required: true, hintKey: "settings.providers.field.path_hint" },
      { name: "model", type: "text", required: true },
    ];
    const basicGrid = h("div", { class: "provider-form-grid" });
    for (const meta of basicFields) {
      const value = meta.name === "protocol" ? protocol : providerValue(record, meta.name);
      basicGrid.append(addControl(meta, value, { wide: meta.name === "base_url" }));
    }
    const keyPresent = !!(record && record.key_present);
    const keyMeta = {
      name: "api_key",
      type: "password",
      labelKey: keyPresent ? "settings.providers.field.replace_key" : "settings.providers.field.api_key",
      hintKey: keyPresent ? "settings.providers.field.api_key_present" : "settings.providers.field.api_key_hint",
    };
    basicGrid.append(addControl(keyMeta, "", {}));

    // The official model list fills capability fields that otherwise live
    // behind Advanced. Fetching and choosing a model never saves the record
    // or the key; the owner still reviews and saves the form.
    const discoveredModels = [];
    const discoveryStatus = h("p", { class: "provider-discovery-status", role: "status", "aria-live": "polite" });
    const discoverySelect = h("select", { class: "select provider-discovery-select", "aria-label": t("settings.providers.discovery.select_label") });
    const discoveryDetails = h("dl", { class: "provider-discovery-details", hidden: true });
    const discoveryNote = h("p", { class: "fine provider-discovery-note", hidden: true, text: t("settings.providers.discovery.use_note") });
    const discoveryResults = h("div", { class: "provider-discovery-results", hidden: true },
      field(t("settings.providers.discovery.select_label"), discoverySelect, null),
      discoveryDetails,
      discoveryNote);
    const discoveryButton = button(t("settings.providers.discovery.fetch"), async (event) => {
      await discoverModels(event.currentTarget);
    }, { small: true });
    const discovery = h("div", { class: "provider-discovery" },
      h("div", { class: "provider-discovery-row" },
        discoveryButton,
        h("p", { class: "field-hint provider-discovery-hint", text: t("settings.providers.discovery.hint") })),
      discoveryStatus,
      discoveryResults);

    const advanced = h("details", { class: "provider-advanced" },
      h("summary", null, t("settings.providers.advanced")),
      h("p", { class: "field-hint provider-advanced-hint", text: t("settings.providers.advanced_hint") }));
    const recordFields = [
      { name: "id", type: "text", required: true, disabled: !!record, hintKey: record ? "settings.providers.field.id_locked" : "settings.providers.field.id_hint" },
      { name: "api_key_secret", type: "text", hintKey: "settings.providers.field.api_key_secret_hint" },
    ];
    const recordGrid = h("div", { class: "provider-field-grid" });
    for (const meta of recordFields) {
      recordGrid.append(addControl(Object.assign({ advanced: true, record: true }, meta), providerValue(record, meta.name), { wide: meta.name === "path" }));
    }
    advanced.append(h("div", { class: "provider-field-group", "data-group": "record" },
      h("div", { class: "provider-group-title" }, h("h4", { text: t("settings.providers.group.record") })),
      recordGrid));

    const groupNodes = [];
    for (const group of PROVIDER_ADVANCED_GROUPS) {
      const title = h("div", { class: "provider-group-title" },
        h("h4", { text: tOr("settings.providers.group." + group.id, group.id) }),
        h("span", { class: "fine provider-group-inactive", text: t("settings.providers.group.inactive") }));
      const grid = h("div", { class: "provider-field-grid" });
      for (const meta of group.fields) {
        grid.append(addControl(Object.assign({ advanced: true }, meta), providerValue(record, meta.name), { wide: meta.type === "text" && meta.name.indexOf("template") >= 0 }));
      }
      const groupNode = h("div", { class: "provider-field-group", "data-group": group.id }, title, grid);
      groupNodes.push({ meta: group, node: groupNode });
      advanced.append(groupNode);
    }

    const idControl = controls.get("id");
    const secretControl = controls.get("api_key_secret");
    const labelControl = controls.get("label");
    if (secretControl) {
      secretControl.control.dataset.auto = record ? "0" : "1";
      secretControl.control.addEventListener("input", () => {
        secretControl.control.dataset.auto = "0";
      });
    }
    function syncSecretName() {
      if (!secretControl || !idControl || secretControl.control.dataset.auto === "0") return;
      secretControl.control.value = providerSecretSuggestion(idControl.control.value);
    }
    if (!record && idControl && secretControl) {
      idControl.control.dataset.auto = "1";
      idControl.control.addEventListener("input", () => {
        idControl.control.dataset.auto = "0";
        syncSecretName();
      });
      if (labelControl) {
        labelControl.control.addEventListener("input", () => {
          if (idControl.control.dataset.auto === "0") return;
          idControl.control.value = providerSlug(labelControl.control.value);
          syncSecretName();
        });
      }
    }

    function applyProtocol() {
      const protocolValue = controls.get("protocol") ? controls.get("protocol").control.value : protocol;
      const kind = providerKind(protocolValue);
      kindBadge.textContent = providerKindLabel(kind);
      kindHint.textContent = t("settings.providers.kind_hint", { kind: providerKindLabel(kind) });
      const relevant = new Set(providerFieldsForKind(kind).map((field) => field.name));
      for (const entry of controls.values()) {
        if (!entry.advanced || entry.record) continue;
        const active = relevant.has(entry.meta.name);
        entry.control.disabled = !active;
        entry.node.dataset.inactive = active ? "0" : "1";
      }
      for (const entry of groupNodes) {
        const active = entry.meta.fields.some((field) => relevant.has(field.name));
        entry.node.dataset.inactive = active ? "0" : "1";
      }
      const runtimeControl = controls.get("runtime");
      const managed = kind === "embedder" && runtimeControl && String(runtimeControl.control.value || "").trim() !== "";
      for (const name of ["base_url", "api_key_secret", "api_key", "context_tokens"]) {
        const entry = controls.get(name);
        if (!entry) continue;
        entry.control.disabled = !!managed;
        entry.node.dataset.inactive = managed ? "1" : "0";
      }
    }

    function setDiscoveryStatus(message, state) {
      discoveryStatus.textContent = message || "";
      discoveryStatus.dataset.state = state || "";
    }

    function clearDiscoveryResults() {
      discoveredModels.length = 0;
      clear(discoverySelect);
      clear(discoveryDetails);
      discoveryDetails.hidden = true;
      discoveryNote.hidden = true;
      discoveryResults.hidden = true;
    }

    function renderDiscoveryDetails(model) {
      clear(discoveryDetails);
      const facts = [];
      const context = finiteNumber(model.context_tokens);
      if (context !== null && context > 0) {
        facts.push(h("dt", { text: t("settings.providers.discovery.details.context") }));
        facts.push(h("dd", { text: t("settings.providers.discovery.details.tokens", { count: context }) }));
      }
      const maxOutput = finiteNumber(model.max_output_tokens);
      if (maxOutput !== null && maxOutput > 0) {
        facts.push(h("dt", { text: t("settings.providers.discovery.details.max_output") }));
        facts.push(h("dd", { text: t("settings.providers.discovery.details.tokens", { count: maxOutput }) }));
      }
      const priceInput = finiteNumber(model.price_input_per_mtok);
      if (priceInput !== null) {
        facts.push(h("dt", { text: t("settings.providers.discovery.details.price_input") }));
        facts.push(h("dd", { text: formatUsd(priceInput) }));
      }
      const priceOutput = finiteNumber(model.price_output_per_mtok);
      if (priceOutput !== null) {
        facts.push(h("dt", { text: t("settings.providers.discovery.details.price_output") }));
        facts.push(h("dd", { text: formatUsd(priceOutput) }));
      }
      const capabilities = [];
      if (model.structured_output === true) {
        capabilities.push(badge(t("settings.providers.discovery.capability.structured_output"), "ok"));
      }
      if (model.reasoning === true) {
        capabilities.push(badge(t("settings.providers.discovery.capability.reasoning"), "ok"));
      }
      if (model.tools === true) {
        capabilities.push(badge(t("settings.providers.discovery.capability.tools"), "ok"));
      }
      if (capabilities.length) {
        facts.push(h("dt", { text: t("settings.providers.discovery.details.capabilities") }));
        facts.push(h("dd", { class: "chips" }, capabilities));
      }
      discoveryDetails.append(...facts);
      discoveryDetails.hidden = facts.length === 0;
    }

    function renderDiscoveryModels(models) {
      clear(discoverySelect);
      discoverySelect.append(h("option", { value: "", text: t("settings.providers.discovery.select_placeholder"), selected: true }));
      models.forEach((model, index) => {
        discoverySelect.append(h("option", { value: String(index), text: String(model.name || model.id) }));
      });
      discoverySelect.value = "";
      discoveryDetails.hidden = true;
      discoveryNote.hidden = true;
      discoveryResults.hidden = false;
    }

    function applyModel(model) {
      const values = providerModelValues(model, {
        label: labelControl ? labelControl.control.value : "",
        chars_per_token: controls.get("chars_per_token") ? controls.get("chars_per_token").control.value : "",
        reserve_tokens_per_byte: controls.get("reserve_tokens_per_byte") ? controls.get("reserve_tokens_per_byte").control.value : "",
      });
      for (const [name, value] of Object.entries(values)) {
        const entry = controls.get(name);
        if (!entry) continue;
        entry.control.value = String(value);
      }
      // A generated label also generates the record id and key name through
      // the same rule a typed label uses. A manually edited id stays put.
      if (!record && values.label && labelControl && idControl && idControl.control.dataset.auto === "1") {
        idControl.control.value = providerSlug(labelControl.control.value);
        syncSecretName();
      }
      renderDiscoveryDetails(model);
      discoveryNote.hidden = !model;
      if (model) {
        setDiscoveryStatus(t("settings.providers.discovery.applied", { model: String(model.name || model.id || "") }), "ok");
      }
      return values;
    }

    async function discoverModels(trigger) {
      const baseUrl = String(controls.get("base_url") ? controls.get("base_url").control.value : "").trim();
      if (!baseUrl) {
        setError(t("settings.providers.error.required", { field: providerFieldLabel({ name: "base_url" }) }));
        setDiscoveryStatus("", "");
        return;
      }
      if (!/^https?:\/\/[^\s]+$/.test(baseUrl)) {
        setError(t("settings.providers.error.base_url"));
        setDiscoveryStatus("", "");
        return;
      }
      setError("");
      const protocolValue = controls.get("protocol") ? controls.get("protocol").control.value : protocol;
      const keyValue = controls.get("api_key") ? controls.get("api_key").control.value : "";
      const body = providerDiscoveryBody(protocolValue, baseUrl, keyValue, record);
      clearDiscoveryResults();
      setDiscoveryStatus(t("settings.providers.discovery.fetching"), "busy");
      await withBusy(trigger, t("settings.providers.discovery.fetching"), async () => {
        try {
          const response = await request("/api/providers/discover", { method: "POST", body });
          const models = response && Array.isArray(response.models)
            ? response.models.filter((model) => model && String(model.id || "").trim())
            : [];
          if (!models.length) {
            setDiscoveryStatus(t("settings.providers.discovery.empty"), "empty");
            return;
          }
          discoveredModels.push(...models);
          renderDiscoveryModels(models);
          setDiscoveryStatus(t("settings.providers.discovery.loaded", { count: models.length }), "ok");
        } catch (error) {
          const failure = apiFailure(error);
          const code = error && error.code ? String(error.code) : "";
          const hintKey = PROVIDER_DISCOVERY_ERROR_HINTS[code];
          setDiscoveryStatus(hintKey ? t(hintKey, { message: failure.message }) : failure.message, "error");
        }
      });
    }

    discoverySelect.addEventListener("change", () => {
      const index = Number(discoverySelect.value);
      if (!Number.isInteger(index) || index < 0 || index >= discoveredModels.length) {
        discoveryDetails.hidden = true;
        discoveryNote.hidden = true;
        setDiscoveryStatus("", "");
        return;
      }
      applyModel(discoveredModels[index]);
    });

    function readField(entry) {
      const type = entry.meta.type;
      const raw = String(entry.control.value === undefined || entry.control.value === null ? "" : entry.control.value).trim();
      if (type === "int" || type === "float") {
        if (raw === "") return null;
        const value = Number(raw);
        if (!Number.isFinite(value)) throw new Error(t("settings.providers.error.number", { field: providerFieldLabel(entry.meta) }));
        if (type === "int" && !Number.isInteger(value)) throw new Error(t("settings.providers.error.integer", { field: providerFieldLabel(entry.meta) }));
        if (entry.meta.min !== undefined && value < entry.meta.min) {
          throw new Error(t("settings.providers.error.range", { field: providerFieldLabel(entry.meta), min: entry.meta.min, max: entry.meta.max === undefined ? t("common.dash") : entry.meta.max }));
        }
        if (entry.meta.max !== undefined && value > entry.meta.max) {
          throw new Error(t("settings.providers.error.range", { field: providerFieldLabel(entry.meta), min: entry.meta.min === undefined ? t("common.dash") : entry.meta.min, max: entry.meta.max }));
        }
        return value;
      }
      if (type === "list_int" || type === "list_text") {
        const parts = raw === "" ? [] : raw.split(/[,\n]+/).map((part) => part.trim()).filter(Boolean);
        if (type === "list_text") return parts;
        return parts.map((part) => {
          const value = Number(part);
          if (!Number.isInteger(value)) throw new Error(t("settings.providers.error.integer", { field: providerFieldLabel(entry.meta) }));
          return value;
        });
      }
      return raw;
    }

    function readValues() {
      const values = {};
      for (const [name, entry] of controls) {
        if (name === "api_key") continue;
        values[name] = readField(entry);
      }
      return values;
    }

    function collect() {
      const values = readValues();
      const protocolValue = String(values.protocol || protocol);
      let keyValue = controls.get("api_key") ? String(controls.get("api_key").control.value || "") : "";
      const payload = providerPayload(values, protocolValue);
      if (providerKind(protocolValue) === "embedder" && payload.runtime) {
        // A managed runtime chooses its own port and key.
        payload.base_url = "";
        payload.api_key_secret = "";
        payload.context_tokens = 0;
        keyValue = "";
      } else if (!payload.api_key_secret && payload.id) {
        payload.api_key_secret = providerSecretSuggestion(payload.id);
      }
      return { payload, keyValue };
    }

    function validate() {
      let values;
      try {
        values = collect();
      } catch (error) {
        return error && error.message ? error.message : String(error);
      }
      const payload = values.payload;
      if (!payload.label) return t("settings.providers.error.required", { field: providerFieldLabel({ name: "label" }) });
      if (!payload.id) return t("settings.providers.error.required", { field: providerFieldLabel({ name: "id" }) });
      if (!/^[a-z0-9][a-z0-9_-]*$/.test(payload.id)) return t("settings.providers.error.id_format");
      if (!payload.path) return t("settings.providers.error.required", { field: providerFieldLabel({ name: "path" }) });
      if (!payload.path.startsWith("/")) return t("settings.providers.error.path");
      if (!payload.model) return t("settings.providers.error.required", { field: providerFieldLabel({ name: "model" }) });
      if (!payload.base_url && !payload.runtime) return t("settings.providers.error.required", { field: providerFieldLabel({ name: "base_url" }) });
      if (payload.base_url && !/^https?:\/\/[^\s]+$/.test(payload.base_url)) return t("settings.providers.error.base_url");
      if (!payload.api_key_secret && !payload.runtime) {
        return t("settings.providers.error.required", { field: providerFieldLabel({ name: "api_key_secret" }) });
      }
      if (values.keyValue && !payload.api_key_secret) return t("settings.providers.error.secret_name");
      for (const status of payload.retry.statuses || []) {
        if (status < 400 || status > 599) return t("settings.providers.error.retry_statuses");
      }
      return "";
    }

    function setError(message) {
      errorNode.textContent = message || "";
      errorNode.hidden = !message;
      // A server or validation error may name a field that only exists in
      // Advanced (the record id, a protocol limit). Reveal the section so the
      // owner can see and fix it instead of guessing where it lives.
      if (message) advanced.open = true;
    }

    const saveButton = button(t("settings.providers.save"), async (event) => {
      const problem = validate();
      if (problem) { setError(problem); return; }
      setError("");
      await withBusy(event.currentTarget, t("settings.providers.saving"), async () => {
        try {
          await options.onSave(collect());
        } catch (error) {
          setError(error && error.message ? error.message : String(error));
        }
      });
    }, { kind: "primary", small: true });
    const cancelButton = button(t("common.cancel"), () => {
      if (options.onCancel) options.onCancel();
    }, { small: true, kind: "quiet" });
    const protocolSelect = controls.get("protocol");
    if (protocolSelect) protocolSelect.control.addEventListener("change", applyProtocol);
    const runtimeInput = controls.get("runtime");
    if (runtimeInput) runtimeInput.control.addEventListener("input", applyProtocol);

    wrapper.append(
      h("div", { class: "provider-form-head" },
        h("div", null, h("h3", { text: formTitle }), kindHint),
        kindBadge),
      h("div", { class: "provider-group-title" }, h("h4", { text: t("settings.providers.basic") })),
      basicGrid,
      discovery,
      advanced,
      errorNode,
      h("div", { class: "button-row provider-form-actions" }, saveButton, cancelButton));
    applyProtocol();

    return {
      node: wrapper,
      validate,
      collect,
      applyModel,
      setError,
      focus: () => {
        const target = controls.get("label");
        if (target && target.control.focus) target.control.focus();
      },
    };
  }
  // providerHealthNodes renders the health the API already reports: whether the
  // last call succeeded, the counts, the last call time and the last error. A
  // failing provider links to the engine component in Logs.
  function providerHealthNodes(record) {
    const status = record && record.status && typeof record.status === "object" ? record.status : null;
    if (!status) return [];
    const calls = Number(status.calls) || 0;
    const failures = Number(status.failures) || 0;
    const healthy = status.healthy === true;
    const state = calls === 0
      ? badge(t("settings.providers.health.no_calls"), "plain")
      : healthy ? badge(t("settings.providers.health.ok"), "ok") : badge(t("settings.providers.health.failing"), "danger");
    const facts = [];
    if (calls > 0 || failures > 0) facts.push(t("settings.providers.health.counts", { calls: formatNumber(calls), failures: formatNumber(failures) }));
    if (status.last_call) facts.push(t("settings.providers.health.last_call", { when: formatRelative(status.last_call) }));
    const lastError = status.last_error ? boundedReason(status.last_error) : "";
    return [
      h("div", { class: "chips" }, state),
      facts.length ? h("p", { class: "fine", text: metaLine(facts) }) : null,
      lastError ? h("p", { class: "field-error", lang: "en", text: t("settings.providers.health.last_error", { message: lastError }) }) : null,
      calls > 0 && !healthy
        ? h("div", { class: "button-row" }, navigateWith(t("logs.open"), "logs", { component: "engine" }, { small: true, icon: "arrow-right" }))
        : null,
    ].filter(Boolean);
  }

  function providerCard(record, handlers) {
    const id = record.id || "";
    const keyPresent = record.key_present === true || !!(record.status && record.status.key_present === true);
    const resultHost = h("div", { class: "provider-card-result", "aria-live": "polite" });
    const errorNode = h("div", { class: "field-error provider-card-error", role: "alert", hidden: true });
    return h("article", { class: "provider-card", "data-provider": id },
      h("div", { class: "provider-card-head" },
        h("div", { class: "provider-card-title" },
          h("h3", { text: record.label || id || t("settings.providers.title") }),
          h("span", { class: "fine provider-model mono", text: record.model || t("common.dash") })),
        h("div", { class: "chips" },
          badge(record.protocol || t("common.dash")),
          keyPresent ? badge(t("settings.providers.key_present"), "ok") : badge(t("settings.providers.no_key"), "warn"))),
      providerHealthNodes(record),
      h("div", { class: "provider-card-actions" },
        button(t("settings.providers.test"), (event) => handlers.test(record, resultHost, event.currentTarget), { small: true, kind: "primary" }),
        button(t("settings.providers.edit"), () => handlers.edit(record), { small: true }),
        button(t("settings.providers.delete"), (event) => handlers.remove(record, errorNode, event.currentTarget), { small: true, kind: "quiet" })),
      resultHost,
      errorNode);
  }

  // provider-editor:end

    // saveProviderRoles writes the changed role settings in order. A partial
  // save is persisted work: it invalidates what the app serves and names the
  // roles stored before the failure. It returns false when a role failed.
  async function saveProviderRoles(changed, roleError) {
    let saved = 0;
    for (const entry of changed) {
      try {
        await request("/api/settings/" + encodeURIComponent(entry.role.setting), { method: "PUT", body: { value: entry.select.value } });
        entry.initial = entry.select.value;
        saved += 1;
      } catch (error) {
        if (saved > 0) invalidateProviderChange();
        const role = providerRoleLabel(entry.role);
        const message = apiFailure(error).message;
        roleError.textContent = saved > 0
          ? t("settings.providers.error.role_partial", { saved, count: saved, role, message })
          : t("settings.providers.error.role", { role, message });
        roleError.hidden = false;
        return false;
      }
    }
    invalidateProviderChange();
    return true;
  }

  // providerRoleRows builds the role rows through the shared settings row
  // builder and returns them with their selects, so the section saves the
  // changed ones together. A role's allowed values are the configured records,
  // so the select stays this tab's control, named by its short role label.
  function providerRoleRows(settingsByKey, providers) {
    const selects = [];
    const rows = orderedProviderRoles().map((role) => {
      const view = settingsByKey.get(role.setting);
      if (!view) return null;
      const select = providerRoleSelect(role, view.value || "", providers);
      // An environment layer wins over settings.json: an editable select would
      // save a value that never takes effect.
      select.disabled = (view.layer || "default") === "env";
      selects.push({ role, select, initial: select.value });
      const control = () => ({
        node: h("label", { class: "provider-role" }, h("span", { class: "field-label", text: providerRoleLabel(role) }), select),
        value: () => select.value,
      });
      return settingRow(view, null, { tab: "providers", control, actions: false });
    }).filter(Boolean);
    return { rows, selects };
  }

  function providersPanel(settings, providers) {
    applyProviderCatalog(providers);
    const providerList = providerListFrom(providers);
    let records = providerList.slice();
    let editing = null;
    let activeForm = null;

    const settingsByKey = new Map((settings && Array.isArray(settings.settings) ? settings.settings : [])
      .map((view) => [view.key, view]));
    const roleError = h("div", { class: "field-error provider-role-error", role: "alert", hidden: true });
    const roles = providerRoleRows(settingsByKey, providers);
    const roleSelects = roles.selects;
    const roleRows = roles.rows;
    const saveRoles = button(t("settings.providers.roles.save"), async (event) => {
      const changed = roleSelects.filter((entry) => entry.select.value !== entry.initial);
      if (!changed.length) {
        toast(t("settings.providers.roles.saved"), "info");
        return;
      }
      roleError.hidden = true;
      roleError.textContent = "";
      await withBusy(event.currentTarget, t("settings.providers.roles.saving"), async () => {
        if (await saveProviderRoles(changed, roleError)) {
          toast(t("settings.providers.roles.saved"), "info");
        }
      });
    }, { small: true, kind: "primary" });

    const rolesNode = h("section", { class: "provider-roles", "aria-labelledby": "provider-roles-title" },
      h("div", { class: "provider-roles-head" },
        h("div", null,
          h("h2", { id: "provider-roles-title", text: t("settings.providers.roles.title") }),
          h("p", { class: "section-sub", text: t("settings.providers.roles.sub") }))),
      roleRows,
      h("div", { class: "provider-role-actions" }, saveRoles, roleError));

    const formHost = h("div", { class: "provider-form-host" });
    const listHost = h("div", { class: "provider-list" });
    const addButton = button(t("settings.providers.add"), () => openForm(null), { small: true, kind: "primary", icon: "plus" });
    const loadError = providers && providers.error
      ? retryBanner(apiFailure(providers.error).message, () => refreshActiveRoute(), providerLoadProblems(providers.error))
      : null;

    function closeForm() {
      editing = null;
      activeForm = null;
      clear(formHost);
      addButton.disabled = false;
    }

    function openForm(record) {
      editing = record || null;
      clear(formHost);
      activeForm = providerFormNode({
        record: editing,
        onCancel: closeForm,
        onSave: saveItem,
      });
      formHost.append(activeForm.node);
      addButton.disabled = true;
      activeForm.focus();
    }

    async function saveItem(result) {
      const current = editing;
      const isNew = !current;
      const path = isNew ? "/api/providers" : "/api/providers/" + encodeURIComponent(current.id);
      let saved;
      try {
        saved = await request(path, { method: isNew ? "POST" : "PUT", body: result.payload });
      } catch (error) {
        if (activeForm) activeForm.setError(t("settings.providers.error.save", { message: apiFailure(error).message }));
        return;
      }
      const savedRecord = saved && saved.id ? saved : {};
      const merged = Object.assign({}, current || {}, result.payload, savedRecord);
      if (merged.key_present === undefined) merged.key_present = !!(current && current.key_present);
      let keyError = "";
      if (result.keyValue) {
        const secretName = merged.api_key_secret || result.payload.api_key_secret;
        if (!secretName) {
          keyError = t("settings.providers.error.secret_name");
        } else {
          try {
            await request("/api/secrets/" + encodeURIComponent(secretName), { method: "PUT", body: { value: result.keyValue } });
            merged.key_present = true;
          } catch (error) {
            merged.key_present = false;
            keyError = t("settings.providers.error.key", { message: apiFailure(error).message });
          }
        }
      }
      const at = records.findIndex((record) => record.id === (isNew ? merged.id : current.id));
      if (at >= 0) records[at] = merged;
      else records.push(merged);
      invalidateProviderChange();
      if (keyError) {
        drawList();
        openForm(merged);
        if (activeForm) activeForm.setError(keyError);
        return;
      }
      toast(t("settings.providers.saved"), "info", merged.label || merged.id);
      closeForm();
      await refreshActiveRoute();
    }

    async function testItem(record, resultHost, trigger) {
      const name = record.label || record.id || t("settings.providers.title");
      const ok = await confirmDialog(
        t("settings.providers.test_confirm_title", { name }),
        t("settings.providers.test_confirm_body"),
        t("settings.providers.test_confirm_action"));
      if (!ok) return;
      await withBusy(trigger, t("settings.providers.testing"), async () => {
        const started = performance.now();
        clear(resultHost);
        try {
          const result = await request("/api/providers/" + encodeURIComponent(record.id) + "/test", { method: "POST" });
          const elapsed = Math.round(performance.now() - started);
          const ms = result && result.latency_ms !== undefined ? result.latency_ms : elapsed;
          const duration = formatDuration(Number(ms) * 1e6);
          const passed = !result || result.ok !== false;
          const model = (result && result.model) || record.model || t("common.dash");
          const detail = providerTestDetail(result);
          const outcome = h("div", { class: "provider-result" + (passed ? "" : " error") },
            h("div", null, badge(passed ? t("state.ok") : t("settings.providers.test_failed"), passed ? "ok" : "danger"),
              text(" " + t(passed ? "settings.providers.test_result_ok" : "settings.providers.test_result_fail", { duration }))),
            h("div", { class: "fine", text: detail ? t("settings.providers.test_detail", { model, detail }) : t("settings.providers.test_detail_empty", { model }) }));
          // A failed test wrote a line in Logs. The link names the component
          // that recorded it, so the owner reads the failure instead of only
          // this sentence.
          if (!passed) {
            outcome.append(h("div", { class: "button-row" },
              navigateWith(t("logs.open"), "logs", { component: "engine" }, { small: true, icon: "arrow-right" })));
          }
          resultHost.append(outcome);
        } catch (error) {
          const duration = formatDuration(Math.round(performance.now() - started) * 1e6);
          resultHost.append(h("div", { class: "provider-result error" },
            h("div", null, badge(t("settings.providers.test_failed"), "danger"),
              text(" " + t("settings.providers.test_result_fail", { duration }))),
            h("div", { text: t("settings.providers.test_detail", { model: record.model || t("common.dash"), detail: apiFailure(error).message }) }),
            h("div", { class: "button-row" },
              navigateWith(t("logs.open"), "logs", { component: "engine" }, { small: true, icon: "arrow-right" }))));
        }
      });
    }

    async function removeItem(record, errorNode, trigger) {
      const name = record.label || record.id || t("settings.providers.title");
      const ok = await confirmDialog(
        t("settings.providers.delete_title", { name }),
        t("settings.providers.delete_body"),
        t("settings.providers.delete_action"), true);
      if (!ok) return;
      await withBusy(trigger, t("settings.providers.deleting"), async () => {
        try {
          await request("/api/providers/" + encodeURIComponent(record.id), { method: "DELETE" });
          if (editing && editing.id === record.id) closeForm();
          toast(t("settings.providers.deleted"), "info", name);
          invalidateProviderChange();
          await refreshActiveRoute();
        } catch (error) {
          errorNode.textContent = t("settings.providers.error.delete", { message: apiFailure(error).message });
          errorNode.hidden = false;
        }
      });
    }

    function drawList() {
      clear(listHost);
      if (!records.length) {
        // The load-error banner already is the list state; an empty card
        // under it would claim there are no providers when the list is
        // unknown. Its retry reloads the screen.
        if (loadError) return;
        listHost.append(emptyState(t("settings.providers.empty_title"), t("settings.providers.empty_body"), null, "settings"));
        return;
      }
      for (const record of records) {
        listHost.append(providerCard(record, { edit: openForm, test: testItem, remove: removeItem }));
      }
    }

    const providerSection = section(t("settings.providers.configured_title"), t("settings.providers.configured_sub"),
      addButton,
      loadError,
      formHost,
      listHost);
    drawList();
    return settingsList(rolesNode, providerSection);
  }


  // runtime-panel:start
  // durationNanos is the one reader of a Go duration string: the schema states
  // a default in its shortest form ("15m") while the effective value arrives
  // canonical ("15m0s"), so 40-settings.js compares the two through it too.
  const DURATION_UNITS = { ns: 1, us: 1e3, "µs": 1e3, ms: 1e6, s: 1e9, m: 60e9, h: 3600e9 };

  function durationNanos(value) {
    const raw = String(value === null || value === undefined ? "" : value).trim();
    const segments = raw.match(/[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)/g);
    if (!segments || segments.join("") !== raw) return null;
    let nanos = 0;
    for (const segment of segments) {
      const match = segment.match(/^([0-9]+(?:\.[0-9]+)?)(ns|us|µs|ms|s|m|h)$/);
      nanos += Number(match[1]) * DURATION_UNITS[match[2]];
    }
    return Number.isFinite(nanos) ? nanos : null;
  }

  // The Windows product ships one local engine: the build the catalog starts
  // automatically (Vulkan). This panel renders that build only: no variant
  // picker, no CPU/CUDA rows, no manual install form. The engine downloads,
  // verifies and starts on its own when embeddings need it.
  const RUNTIME_STATE_KINDS = {
    ready: "done",
    installing: "running",
    starting: "running",
    tuning: "running",
    probing: "running",
    canary: "running",
    restarting: "running",
    failed: "blocked",
    stopped: "closed",
    idle: "pending",
  };

  // singleSettingValue is the value of one /api/settings/{key} view. The list
  // reader below reuses it so the two response shapes cannot disagree about
  // what "unset" means.
  function singleSettingValue(view) {
    return view && view.value !== null && view.value !== undefined ? String(view.value) : "";
  }

  // answer-provider:start
  // answerProviderState is the one Ask read of the answer-provider role. It
  // keeps four states apart: a usable provider, an empty selection, a
  // selected record that cannot be used (unknown kind or missing key), and a
  // role read that failed. A failed read is unknown, never "not configured".
  function answerProviderState(view) {
    const roles = view && Array.isArray(view.roles) ? view.roles : null;
    if (!roles) return { state: "unknown", name: "", detail: "" };
    const role = roles.find((entry) => entry && entry.setting === "qa.provider");
    if (!role) return { state: "unknown", name: "", detail: "" };
    const id = String(role.provider || "").trim();
    const records = view && Array.isArray(view.providers) ? view.providers : [];
    const record = records.find((entry) => entry && entry.id === id);
    const name = (record && (record.label || record.id)) || id;
    if (!id) return { state: "unconfigured", name: "", detail: String(role.detail || "") };
    if (role.configured === false) return { state: "unavailable", name, detail: String(role.detail || "") };
    const available = role.available === true || (role.available === undefined && role.configured === true);
    return available
      ? { state: "available", name, detail: "" }
      : { state: "unavailable", name, detail: String(role.detail || "") };
  }

  async function readAnswerProvider() {
    const read = await settleRead(requestCached("/api/providers", {}, CACHE_TTL.providers));
    if (read.error || !read.value) return { state: "unknown", name: "", detail: "" };
    return answerProviderState(read.value);
  }
  // answer-provider:end

  function settingValue(settings, key) {
    const views = settings && Array.isArray(settings.settings) ? settings.settings : [];
    return singleSettingValue(views.find((entry) => entry && entry.key === key));
  }

  // embedderChoice is the embed.provider record. It is local when the record
  // names a runtime this machine manages; otherwise embeddings are remote and
  // the local engine is not selected.
  function embedderChoice(settings, providers) {
    const id = settingValue(settings, "embed.provider");
    if (!id) return null;
    const record = providerListFrom(providers).find((entry) => entry && entry.id === id) || null;
    return {
      id,
      record,
      label: (record && (record.label || record.id)) || id,
      local: !!(record && record.runtime),
      // A failed provider read cannot say whether this record is local, so
      // the card reports an unknown state instead of "not selected".
      failed: !record && !!(providers && providers.error),
    };
  }

  function runtimeStatus(sidecar, choice) {
    const runtimes = sidecar && Array.isArray(sidecar.runtimes) ? sidecar.runtimes : [];
    if (!runtimes.length) return null;
    if (choice && choice.record && choice.record.runtime) {
      const match = runtimes.find((entry) => entry.runtime === choice.record.runtime);
      if (match) return match;
    }
    return runtimes[0];
  }

  // runtimeBuild picks the one engine build: the running one, else the build
  // the catalog starts automatically, else the first published build.
  function runtimeBuild(sidecar, run) {
    const builds = sidecar && Array.isArray(sidecar.builds) ? sidecar.builds : [];
    if (!builds.length) return null;
    if (run) {
      const running = builds.find((build) => build.runtime === run.runtime && build.variant === run.variant)
        || builds.find((build) => build.runtime === run.runtime);
      if (running) return running;
    }
    return builds.find((build) => build.auto) || builds[0];
  }

  function runtimeModel(run, choice) {
    if (run && run.model) return run.model;
    if (choice && choice.record && choice.record.model) return choice.record.model;
    return choice ? choice.id : "";
  }

  function runtimeState(run, build, choice) {
    if (choice && choice.failed) return { code: "unavailable", kind: "blocked" };
    if (!choice || !choice.local) return { code: "remote", kind: "pending" };
    if (!build) return { code: "unavailable", kind: "blocked" };
    // A machine capability the build needs (Vulkan) is checked before any
    // download: the card reports it instead of offering an install.
    if (build.unavailable) return { code: "unavailable", kind: "blocked" };
    if (build.broken) return { code: "broken", kind: "blocked" };
    // An idle entry only means "never started here"; the install state is the
    // honest answer until the engine actually ran.
    const code = run && run.state && run.state !== "idle" ? run.state : "";
    if (code) return { code, kind: RUNTIME_STATE_KINDS[code] || "pending" };
    return build.installed ? { code: "installed", kind: "done" } : { code: "not_installed", kind: "pending" };
  }

  // runtimeSince drops the zero time an idle entry carries.
  function runtimeSince(run) {
    if (!run || !run.since) return "";
    const at = new Date(run.since);
    if (!Number.isFinite(at.getTime()) || at.getUTCFullYear() < 1970) return "";
    return String(run.since);
  }

  function runtimeStatePill(state) {
    return pill(tOr("settings.runtime.state." + state.code, humanCode(state.code)), state.kind);
  }

  // The runtime card explains the supervisor's idle stop in the owner's
  // units: the settings projection returns sidecar.idle_timeout as a Go
  // duration string ("15m0s", "0s", "1h30m0s"), never a number of seconds.
  const RUNTIME_IDLE_KEY = "sidecar.idle_timeout";

  // runtimeIdleNote is the one sentence that explains a stopped engine: it
  // stops after the configured idle window and starts again on the next use,
  // unless the window is 0. It reads the value the runtime tab already loads,
  // so no extra request is issued.
  function runtimeIdleNote(settings) {
    const views = settings && Array.isArray(settings.settings) ? settings.settings : [];
    const view = views.find((item) => item && item.key === RUNTIME_IDLE_KEY);
    if (!view) return null;
    const idle = durationNanos(view.value);
    if (idle === null) return null;
    return h("p", { class: "runtime-note" }, text(idle > 0
      ? t("settings.runtime.idle_stop", { duration: formatDuration(idle) })
      : t("settings.runtime.idle_never")));
  }

  // runtimeVariantLabel shows a catalog variant as a name: short identifiers
  // are acronyms (CPU, GPU), longer ones are title-cased (Vulkan). The label
  // comes from the catalog, so no build is named here.
  function runtimeVariantLabel(variant) {
    const value = String(variant || "");
    if (!value) return "";
    if (value.length <= 3) return value.toUpperCase();
    return value.charAt(0).toUpperCase() + value.slice(1);
  }

  // providerLoadProblems is the one refusal list of a rejected provider file.
  // A malformed providers.json reports each broken record, field and path: a
  // retry alone cannot repair the file, so the panel must show what to fix.
  function providerLoadProblems(error) {
    const problems = error && Array.isArray(error.problems) ? error.problems : [];
    if (!problems.length) return null;
    return h("ul", { class: "provider-load-problems" },
      problems.map((problem) => h("li", { text: problem && problem.message ? problem.message : String(problem || "") })));
  }

  function runtimeFact(labelKey, value) {
    return h("div", { class: "runtime-fact" },
      h("dt", { text: t(labelKey) }),
      h("dd", { class: "mono", text: value }));
  }

  function runtimePanel(sidecar, providers, settings, status) {
    const choice = embedderChoice(settings, providers);
    const local = !!(choice && choice.local);
    const run = local ? runtimeStatus(sidecar, choice) : null;
    const build = runtimeBuild(sidecar, run);
    const model = runtimeModel(run, choice);
    const state = runtimeState(run, build, choice);
    const pinned = build && Array.isArray(build.pins) ? build.pins.length : 0;
    const detail = run && run.detail ? String(run.detail) : "";
    const since = runtimeSince(run);

    const facts = h("dl", { class: "runtime-facts" },
      run && run.pid ? runtimeFact("settings.runtime.table.pid", String(run.pid)) : null,
      run && run.threads ? runtimeFact("settings.runtime.table.threads", String(run.threads)) : null,
      since ? runtimeFact("settings.runtime.table.since", formatDate(since)) : null,
      run && run.restarts ? runtimeFact("settings.runtime.table.restarts", String(run.restarts)) : null,
      build && build.installed && build.size_bytes ? runtimeFact("settings.runtime.table.on_disk", formatBytes(build.size_bytes)) : null);

    const repairError = h("div", { class: "field-error runtime-error", role: "alert", hidden: true });
    const repair = button(t("settings.runtime.repair"), async (event) => {
      if (!build || !model) {
        repairError.textContent = t("settings.runtime.repair_unavailable");
        repairError.hidden = false;
        return;
      }
      await withBusy(event.currentTarget, t("settings.runtime.repairing"), async () => {
        repairError.hidden = true;
        repairError.textContent = "";
        try {
          const response = await request("/api/sidecar/install", { method: "POST", body: { runtime: build.runtime, model, variant: build.variant } });
          invalidateCache(["/api/sidecar"]);
          toast(t("settings.runtime.repair_started"), "info", response && response.job ? jobNameLabel(response.job.name) : t("settings.runtime.repair_body"));
        } catch (error) {
          repairError.textContent = t("settings.runtime.repair_failed", { message: apiFailure(error).message });
          repairError.hidden = false;
        }
      });
    }, { small: true });

    const notices = [];
    if (!local) {
      notices.push(h("p", { class: "runtime-note" },
        text(choice && choice.failed
          ? t("settings.runtime.providers_unavailable")
          : choice
            ? t("settings.runtime.not_selected_provider", { provider: choice.label })
            : t("settings.runtime.not_selected")),
        // The runtime harness runs this block alone, so it consumes the router's
        // one hash builder directly instead of the link helper.
        h("a", { href: routeHash("settings", { tab: "providers" }), text: t("settings.runtime.not_selected_link") })));
    } else if (!build) {
      notices.push(h("p", { class: "runtime-note", text: t("settings.runtime.no_builds") }));
    } else if (build.unavailable) {
      // The machine cannot run this build: say what is missing, never offer
      // the download that would fail (or start the bundled fallback).
      notices.push(h("p", { class: "runtime-note warn-text",
        text: tOr("settings.runtime.unavailable." + build.unavailable, build.unavailable) }));
    } else {
      if (build.broken) {
        notices.push(h("p", { class: "runtime-note warn-text", text: t("settings.runtime.broken", { reason: build.broken.reason || build.broken.code || t("settings.runtime.probe_failed") }) }));
      }
      if (!build.installed) {
        notices.push(h("p", { class: "runtime-note", text: t("settings.runtime.auto_first_use") }));
        if (build.download_bytes > 0) notices.push(h("p", { class: "fine", text: t("settings.runtime.engine_size", { size: formatBytes(build.download_bytes) }) }));
      }
    }
    // A local engine is the only one this supervisor can stop: the note
    // rides with the other runtime notices instead of a second card.
    if (local) {
      const idleNote = runtimeIdleNote(settings);
      if (idleNote) notices.push(idleNote);
    }

    const advanced = h("details", { class: "runtime-advanced" },
      h("summary", { text: t("settings.runtime.advanced") }),
      build && build.unavailable ? null : h("p", { class: "fine", text: t("settings.runtime.advanced_hint") }),
      h("p", { class: "fine", text: pinned ? t("settings.runtime.pins", { count: pinned }) : t("settings.runtime.no_pins") }),
      build && !build.unavailable ? h("div", { class: "button-row" }, repair) : null,
      repairError);

    const card = h("article", { class: "runtime-card" },
      h("div", { class: "runtime-head" },
        h("div", { class: "runtime-identity" },
          h("span", { class: "runtime-name mono", text: build ? build.runtime : t("settings.runtime.no_runtime") }),
          build && build.version ? h("span", { class: "runtime-version fine mono", text: build.version }) : null,
          build && build.variant ? badge(runtimeVariantLabel(build.variant), "accent") : null,
          build && build.gpu ? badge(t("settings.runtime.gpu"), "accent") : null),
        runtimeStatePill(state)),
      local && model ? h("p", { class: "runtime-model" },
        h("span", { class: "field-label", text: t("settings.runtime.model_label") }),
        h("span", { class: "mono", text: model })) : null,
      facts.children.length ? facts : null,
      detail ? h("p", { class: "fine", text: detail }) : null,
      notices,
      advanced);

    const refresh = button(t("common.refresh"), () => {
      // A status read is cached for CACHE_TTL.sidecar; Refresh must always
      // re-read (and so re-probe a missing device), like the sidecar.state
      // event handler does.
      invalidateCache(["/api/sidecar"]);
      return refreshActiveRoute();
    }, { icon: "refresh", small: true });
    // The engine's settings belong to the engine's tab: status and controls
    // in one place, not split across General and a read-only card. They render
    // through the shared row builder like every other tab.
    const views = (settings && Array.isArray(settings.settings) ? settings.settings : [])
      .filter((view) => view && view.group === "Local runtime");
    const group = views.length
      ? settingsGroupNode(t("settings.runtime.settings_title"), views, status, { count: false, tab: "runtime" })
      : null;
    return settingsList(section(t("settings.runtime.title"), t("settings.runtime.sub"), refresh, card, group));
  }
  // runtime-panel:end

