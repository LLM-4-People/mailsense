  // ---------------------------------------------------------------- API client

  class ApiError extends Error {
    constructor(status, payload) {
      const message = payload && payload.message ? payload.message : "";
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.code = payload && payload.code ? payload.code : "request_failed";
      this.params = payload && payload.params ? payload.params : {};
      this.problems = payload && Array.isArray(payload.problems) ? payload.problems : [];
      // A timed-out mutation is not a failed one: the server may still have
      // applied it. Callers that know the method keep it for that report.
      this.mutation = false;
    }
  }

  // A host resource name and the article its sentence needs belong
  // together: English says "the CPU", "memory" and "the disk", and French
  // needs the gender of each noun. One table owns the grammar; the catalog
  // owns the noun.
  const RESOURCE_ARTICLES = {
    en: { cpu: "the ", ram: "", disk: "the " },
    fr: { cpu: "le ", ram: "la ", disk: "le " },
  };

  function resourceName(resource) {
    const key = String(resource || "").trim();
    if (!key) return "";
    return tOr("resource.name." + key, key);
  }

  function resourceArticle(resource) {
    const key = String(resource || "").trim();
    const language = I18N.snapshot().language === "fr" ? "fr" : "en";
    const article = RESOURCE_ARTICLES[language][key];
    return article === undefined ? "" : article;
  }

  // resourceSentenceName writes an ordinary noun in lower case after its
  // article or mid-sentence. An all-capital acronym such as CPU keeps its
  // spelling.
  function resourceSentenceName(resource) {
    const name = resourceName(resource);
    if (!name || name === name.toUpperCase()) return name;
    return name.charAt(0).toLowerCase() + name.slice(1);
  }

  // One request bound at the HTTP owner. The slowest legitimate cold reads
  // measured live were the triage page (~8 s) and the priority page (~9 s),
  // so 30 s clears a cold cache with room for a busy local store without
  // leaving a hung API spinning forever. Paid generation and job starts
  // pass their own long bound through `timeoutMs`.
  const REQUEST_TIMEOUT_MS = 30000;
  const LONG_REQUEST_TIMEOUT_MS = 300000;

  function isMutationTimeout(error) {
    return error instanceof ApiError && error.code === "timeout" && error.mutation === true;
  }

  // removed-read:start
  // A message can be removed between the list that offered it and the read
  // that opens it. The transport (http_404/http_410) and the server
  // (not_found) spellings are the same fact, so every screen that reads a
  // message classifies removal through this one predicate instead of turning
  // a gone message into an empty one.
  function removedMessageError(error) {
    if (!(error instanceof ApiError)) return false;
    return error.status === 404 || error.status === 410 ||
      error.code === "not_found" || error.code === "http_404" || error.code === "http_410";
  }
  // removed-read:end

  function apiFailure(error) {
    if (!(error instanceof ApiError)) {
      return {
        title: t("error.unexpected"),
        message: t("error.unexpected_body"),
        diagnostic: error && error.message ? String(error.message) : String(error || ""),
        problems: [],
      };
    }
    const noResponse = isMutationTimeout(error);
    let message;
    if (noResponse) {
      message = t("error.no_response_body");
    } else if (error.code === "invalid_limit" && error.params && error.params.max && I18N.has("api.invalid_limit_range")) {
      message = t("api.invalid_limit_range", error.params);
    } else if (error.code === "resource_limit" && I18N.has("api.resource_limit")) {
      const params = error.params || {};
      message = t("api.resource_limit", {
        article: resourceArticle(params.resource),
        resource: resourceSentenceName(params.resource),
        percent: params.percent,
        limit: params.limit,
      });
    } else if (error.code === "source_unavailable" && error.params && error.params.cause &&
      I18N.has("api.source_cause." + error.params.cause)) {
      // A connector names the fix; the catalog owns its two languages, and
      // the server's English sentence stays the diagnostic.
      message = t("api.source_cause." + error.params.cause);
    } else if (I18N.has("api." + error.code)) {
      message = t("api." + error.code, error.params);
    } else if (I18N.has("error." + error.code)) {
      // Client-side failures (an unreadable body, a network error) have
      // their own catalog keys; the server codes keep the api.* namespace.
      message = t("error." + error.code, error.params);
    } else if (I18N.snapshot().language === "fr") {
      // An unknown server code still gets French prose; the English server
      // message stays in the diagnostic disclosure.
      message = t("error.unknown_code", { code: error.code });
    } else {
      message = error.message || t("error.request_failed");
    }
    const diagnostic = error.message && error.message !== message ? error.message : "";
    return {
      title: noResponse ? t("error.no_response") : t("error.title"),
      message,
      diagnostic,
      problems: error.problems,
    };
  }

  function problemLabel(code) {
    if (!code) return t("error.problem");
    if (I18N.has("api." + code)) return t("api." + code);
    if (I18N.has("blocker." + code + ".title")) return t("blocker." + code + ".title");
    if (I18N.snapshot().language === "fr") return t("api.problem", { code });
    return code;
  }

  async function request(path, options = {}) {
    const method = options.method || "GET";
    const mutation = method !== "GET" && method !== "HEAD";
    const timeoutMs = Number(options.timeoutMs) > 0 ? Number(options.timeoutMs) : REQUEST_TIMEOUT_MS;
    // One controller and one timer for this request. Aborting fetch stops the
    // wait, not the server: a mutation's outcome stays unknown, not failed.
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), timeoutMs);
    const timeoutError = () => {
      const error = new ApiError(0, { code: "timeout" });
      error.mutation = mutation;
      // The one transport owner knows the method, so it is also the one
      // place that can re-read the views a timed-out mutation may change.
      if (mutation) recoverUnknownMutation();
      return error;
    };
    const headers = Object.assign({ Authorization: "Bearer " + state.token }, options.headers || {});
    // as is the response type: "json" (the API's contract) or "blob" (an
    // attachment read, which skips text and JSON parsing). cache defaults to
    // no-store; an attachment version URL may use the browser cache instead.
    const as = options.as || "json";
    const init = { method, headers, cache: options.cache || "no-store", signal: controller.signal };
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(options.body);
    }
    try {
      let response;
      try {
        response = await fetch(path, init);
      } catch (error) {
        if (controller.signal.aborted) throw timeoutError();
        throw new ApiError(0, { code: "network_error" });
      }
      if (response.status === 401) {
        handleUnauthorized();
        throw new ApiError(401, { code: "unauthorized" });
      }
      if (as === "blob") {
        if (!response.ok) throw new ApiError(response.status, { code: "http_" + response.status, message: response.statusText || "" });
        return response.blob();
      }
      const raw = await response.text();
      let payload = null;
      if (raw) {
        try {
          payload = JSON.parse(raw);
        } catch {
          // A non-empty body that is not JSON is never a success, whatever the
          // status: returning null would let a caller cache and paint a failed
          // read as an empty one.
          throw new ApiError(response.status || 0, { code: "invalid_response" });
        }
      }
      if (!response.ok) {
        if (payload && payload.error) throw new ApiError(response.status, payload.error);
        throw new ApiError(response.status, { code: "http_" + response.status, message: response.statusText || "" });
      }
      // A GET success is a payload. Returning the null of an empty 2xx body
      // let a caller cache and paint a failed read as an empty message.
      if (method === "GET" && payload === null) {
        throw new ApiError(response.status, { code: "invalid_response" });
      }
      return payload;
    } catch (error) {
      if (error instanceof ApiError) throw error;
      if (controller.signal.aborted) throw timeoutError();
      throw new ApiError(0, { code: "network_error" });
    } finally {
      window.clearTimeout(timer);
    }
  }

  // settleRead keeps a failed supplementary read distinguishable from an
  // empty one. Panels paint their content from value and a small errorState
  // from error; they never turn error into an empty-shaped default.
  async function settleRead(promise) {
    try { return { value: await promise, error: null }; }
    catch (error) { return { value: null, error }; }
  }

  // INLINE_READ_ERROR is the one presentation of a failed supplementary
  // read: one sentence and its retry, not a full empty-state box. A panel
  // adds only its own title to this shared shape.
  const INLINE_READ_ERROR = {
    body: false,
    icon: false,
    heading: "p",
    hostClass: "inline-read-error",
    role: "status",
  };

  // ---------------------------------------------------------------- read cache
  //
  // Every menu read goes through one cache. `cached` is the synchronous
  // read used to paint a screen from data already in memory; requestCached
  // deduplicates concurrent reads and owns the TTL. A write bumps apiEpoch
  // so an older in-flight response can never repopulate an invalidated key.

  // read-cache:start
  function cacheKey(path, options = {}) {
    return (options.method || "GET") + " " + path;
  }

  // cacheValueBytes estimates the UTF-16 storage a cached payload keeps
  // alive. It walks the parsed value instead of serializing it: a message
  // body is already a string, and the cache must not build a second copy just
  // to measure the first.
  function cacheValueBytes(value) {
    if (value === null || value === undefined) return 0;
    if (typeof value === "string") return value.length * 2;
    if (typeof value === "number" || typeof value === "boolean") return 8;
    if (Array.isArray(value)) {
      let bytes = 16;
      for (const item of value) bytes += cacheValueBytes(item);
      return bytes;
    }
    if (typeof value === "object") {
      let bytes = 32;
      for (const key of Object.keys(value)) {
        bytes += key.length * 2 + cacheValueBytes(value[key]);
      }
      return bytes;
    }
    return 0;
  }

  // dropAPICache is the one removal point: the byte total follows every
  // deletion, whether it is an expiry, an eviction or an invalidation.
  function dropAPICache(key) {
    const entry = apiCache.get(key);
    if (!entry) return;
    apiCache.delete(key);
    apiCacheBytes = Math.max(0, apiCacheBytes - (Number(entry.bytes) || 0));
  }

  // storeAPICache is the one insertion point. An oversized response, or one
  // whose sender sheet is too large to keep beside its body, still answers
  // its caller but does not take a cache slot.
  function storeAPICache(path, options, value, ttl) {
    if (value && typeof value === "object" && typeof value.body_css === "string" &&
        value.body_css.length > MAX_CACHED_BODY_CSS) return false;
    const bytes = cacheValueBytes(value);
    if (bytes > MAX_API_CACHE_ENTRY_BYTES) return false;
    const key = cacheKey(path, options);
    dropAPICache(key);
    apiCache.set(cacheKey(path, options), { value, at: Date.now(), ttl });
    const entry = apiCache.get(key);
    entry.bytes = bytes;
    apiCacheBytes += bytes;
    trimAPICache();
    return true;
  }

  // trimAPICache sweeps expired entries first, then drops the least recently
  // used entry until both the entry count and the byte budget fit.
  function trimAPICache() {
    const now = Date.now();
    for (const [key, entry] of [...apiCache]) {
      if (now - entry.at >= entry.ttl) dropAPICache(key);
    }
    while (apiCache.size > MAX_API_CACHE || apiCacheBytes > MAX_API_CACHE_BYTES) {
      const oldest = apiCache.keys().next().value;
      if (oldest === undefined) break;
      dropAPICache(oldest);
    }
  }

  // cached, cachedStale and primeCache share cacheKey: one owner of the
  // method + path format means a read can never be stored under one key and
  // looked up under another. A hit is a use: it moves the entry to the MRU
  // end without extending the TTL.
  function cached(path, options = {}) {
    const key = cacheKey(path, options);
    const entry = apiCache.get(key);
    if (!entry) return undefined;
    if (Date.now() - entry.at >= entry.ttl) {
      dropAPICache(key);
      return undefined;
    }
    apiCache.delete(key);
    apiCache.set(key, entry);
    return entry.value;
  }

  function cachedStale(path, options = {}) {
    const entry = apiCache.get(cacheKey(path, options));
    return entry ? entry.value : undefined;
  }

  // primeCache installs a read that arrived as part of a batch. It uses the
  // same key, TTL owner and byte budget as requestCached, so a later single
  // read is a hit.
  function primeCache(path, value, ttl = 0, options = {}) {
    storeAPICache(path, options, value, ttl);
  }

  function warmCompanyProfile(key, profile) {
    companyProfileWarm.delete(key);
    companyProfileWarm.set(key, { profile, at: Date.now() });
    while (companyProfileWarm.size > 32) {
      const oldest = companyProfileWarm.keys().next().value;
      if (oldest === undefined) break;
      companyProfileWarm.delete(oldest);
    }
  }

  function warmedCompanyProfile(key) {
    const entry = companyProfileWarm.get(key);
    if (!entry) return undefined;
    if (Date.now() - entry.at >= CACHE_TTL.companies) {
      companyProfileWarm.delete(key);
      return undefined;
    }
    return entry.profile;
  }

  async function requestCached(path, options = {}, ttl = 0) {
    const key = cacheKey(path, options);
    if ((options.method || "GET") === "GET" && !options.force) {
      const hit = cached(path, options);
      if (hit !== undefined) return hit;
    }
    // The flight key is the cache key, not the epoch: an invalidation that
    // does not name this path must leave its concurrent readers sharing the
    // request they already started. A new flight is only opened when the
    // invalidation deleted this one.
    if (apiInflight.has(key)) return apiInflight.get(key);
    const epoch = apiEpoch;
    const clean = Object.assign({}, options);
    delete clean.force;
    const requestPromise = request(path, clean).then((value) => {
      if (epoch === apiEpoch) storeAPICache(path, options, value, ttl);
      return value;
    });
    apiInflight.set(key, requestPromise);
    try {
      return await requestPromise;
    } finally {
      // An invalidated flight may have been replaced by a newer one for the
      // same path; the old finally must not delete its successor.
      if (apiInflight.get(key) === requestPromise) apiInflight.delete(key);
    }
  }

  function invalidateCache(prefixes = []) {
    apiEpoch += 1;
    // One prefix covers a read when it is the exact path, when it names a
    // parent of the path, or when the dropped path itself is a parent (an
    // empty prefix list is the full clear).
    const invalidated = (path) => !prefixes.length || prefixes.some((prefix) =>
      path === prefix || path.startsWith(prefix) || prefix.startsWith(path));
    // Only the reads this invalidation names lose their in-flight promise.
    // Every other flight stays joinable, so a sync does not make the Hub or
    // a company profile send a second request to its waiting readers.
    for (const key of [...apiInflight.keys()]) {
      const path = key.slice(key.indexOf(" ") + 1);
      if (invalidated(path)) apiInflight.delete(key);
    }
    // The conversation page cache is derived from the API; when its read is
    // invalidated the derived first page and any in-flight load go with it.
    if (invalidated("/api/conversations")) {
      state.conversationPages.clear();
      state.conversationLoads.clear();
    }
    // Priority pages and the accumulated triage model are derived maps too:
    // their later pages keep a ranking and decisions from the snapshot the
    // dropped read served. Reset them at the same choke point so one event
    // cannot leave a screen painting rows its next read would remove.
    if (invalidated("/api/priority")) forgetPriorityPages();
    if (invalidated("/api/triage")) forgetTriagePage();
    // The warm profile buffer is derived from the same company reads. A sync
    // or triage that drops them must not leave a profile warm for the next
    // click; the choke point owns every company prefix, so no caller has to
    // remember the buffer beside the cache.
    if (invalidated("/api/companies")) companyProfileWarm.clear();
    if (!prefixes.length) {
      apiCache.clear();
      apiCacheBytes = 0;
      companyProfileWarm.clear();
      return;
    }
    for (const key of [...apiCache.keys()]) {
      const path = key.slice(key.indexOf(" ") + 1);
      if (prefixes.some((prefix) => path === prefix || path.startsWith(prefix))) dropAPICache(key);
    }
  }
  // read-cache:end

  function handleUnauthorized() {
    state.token = "";
    state.shuttingDown = true;
    try { sessionStorage.removeItem(TOKEN_KEY); } catch { /* storage may be unavailable */ }
    if (eventSource) { eventSource.close(); eventSource = null; }
    if (dom.modal.open) dom.modal.close();
    state.modalOpen = false;
    state.modalCloseFn = null;
    invalidateCache();
    routeCache.clear();
    showTokenError("token.rejected");
  }

  // A timed-out mutation may already have landed. Re-read the shared views
  // the action can change instead of leaving the screen on its pre-action
  // snapshot; the interactive route keeps its mounted task.
  function recoverUnknownMutation() {
    invalidateAllReads();
    Promise.allSettled([loadLearning(true), loadStatus(true), loadJobs(true)])
      .then(() => updateLearningChrome());
  }

  // reportFailure is the one error toast. reportError adds the background
  // prefetch suppression; reportActionError is the explicit user-action
  // entry point and must never be suppressed by an unrelated prefetch.
  function reportFailure(error) {
    if (error instanceof ApiError && error.code === "unauthorized") return;
    const failure = apiFailure(error);
    toast(failure.title, isMutationTimeout(error) ? "warn" : "error", failure.message, failure.problems, failure.diagnostic);
  }

  function reportError(error) {
    if (prefetchActive > 0 && routeRenderInFlight === 0 && Date.now() < prefetchErrorSuppressUntil) return;
    reportFailure(error);
  }

  function reportActionError(error) {
    reportFailure(error);
  }

  // Background reads share one ignorable-code policy. Unauthorized is owned
  // by the token screen; shutting_down is the expected reply while the app
  // is closing; network_error during shutdown is the same condition.
  function ignorableBackground(error) {
    if (error instanceof ApiError) {
      if (error.code === "unauthorized") return true;
      if (state.shuttingDown && (error.code === "shutting_down" || error.code === "network_error")) return true;
    }
    return false;
  }

  function reportBackgroundError(error) {
    if (ignorableBackground(error)) return;
    reportError(error); // keeps the prefetch suppression window
  }

  // problemDiagnostic is the one English diagnostic block of a failed problem:
  // the toast list and the error screen show the same fields and markup.
  function problemDiagnostic(problem) {
    const english = metaLine([problem.stage, problem.subject, problem.message]);
    return diagnosticDetails(english);
  }

  function problemItem(problem) {
    return h("li", null,
      h("span", { text: problemLabel(problem.code) }),
      problemDiagnostic(problem));
  }

  function toast(title, kind = "info", message = "", problems = [], diagnostic = "") {
    const close = iconButton("close", t("common.dismiss"), () => node.remove(), { small: true });
    const problemList = Array.isArray(problems) && problems.length
      ? h("ul", { class: "toast-problems" }, problems.slice(0, 8).map(problemItem))
      : null;
    // #toasts is the one live region. A role on each toast would nest live
    // regions, which can double-announce or suppress the message.
    const node = h("div", { class: "toast " + kind },
      h("div", { class: "toast-head" },
        h("div", null,
          h("div", { class: "toast-title", text: title }),
          message ? h("div", { class: "toast-message", text: message }) : null,
          diagnostic ? diagnosticDetails(diagnostic, { paragraphClass: "fine" }) : null),
        close),
      problemList);
    dom.toasts.append(node);
    while (dom.toasts.children.length > 3) dom.toasts.firstElementChild.remove();
    const timeout = kind === "error" ? 14000 : 7000;
    const dismiss = () => { if (node.isConnected) node.remove(); };
    let timer = window.setTimeout(dismiss, timeout);
    const pause = () => { window.clearTimeout(timer); };
    const resume = () => { pause(); timer = window.setTimeout(dismiss, timeout); };
    // A reader hovering or focusing a toast is still reading it; do not
    // remove the diagnostic under their pointer.
    node.addEventListener("mouseenter", pause);
    node.addEventListener("mouseleave", resume);
    node.addEventListener("focusin", pause);
    node.addEventListener("focusout", resume);
  }

