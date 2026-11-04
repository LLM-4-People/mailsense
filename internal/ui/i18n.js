"use strict";
/*
 * Mailsense's interface runtime: one translation lookup, one language
 * resolution rule and one set of Intl formatters. Catalogs are plain flat
 * objects loaded before this file; app.js never owns user-facing wording.
 *
 * Interpolation returns a string that every caller assigns through
 * textContent. No function here builds markup.
 */
(() => {
  const catalogs = window.MailsenseI18nCatalogs || {};
  const DEFAULT_LOCALE = { en: "en-US", fr: "fr-FR" };
  const SUPPORTED = ["en", "fr"];

  const state = {
    language: "en",
    formatLocale: "en-US",
    offerFrench: false,
    source: "fallback",
    ownerZone: "",
    zoneSource: "machine",
    zoneDiagnostic: "",
  };

  const numberFormats = new Map();
  const dateFormats = new Map();
  const relativeFormats = new Map();
  const pluralRules = new Map();
  const collators = new Map();

  function supportedBase(value) {
    const base = String(value == null ? "" : value).trim().toLowerCase().split(/[-_]/)[0];
    return SUPPORTED.includes(base) ? base : "";
  }

  // The English catalog loads with the page; every other catalog is fetched
  // only when the owner's language actually needs it. A missing catalog
  // resolves false and the caller keeps the English wording rather than
  // failing the whole interface.
  const catalogLoads = new Map();

  // assetURL adds the shell's content version to a lazily loaded asset. The
  // one version map lives in the meta tag every screen already receives; a
  // test harness without a document falls back to the plain path.
  function assetURL(name) {
    try {
      if (typeof document.querySelector !== "function") return "/" + name;
      const meta = document.querySelector('meta[name="mailsense-assets"]');
      const versions = meta ? JSON.parse(meta.getAttribute("content") || "{}") : {};
      return "/" + name + (versions[name] ? "?v=" + versions[name] : "");
    } catch {
      return "/" + name;
    }
  }

  function ensure(language) {
    const base = supportedBase(language) || "en";
    if (base === "en" || catalogs[base]) return Promise.resolve(true);
    if (catalogLoads.has(base)) return catalogLoads.get(base);
    const load = new Promise((resolve) => {
      const script = document.createElement("script");
      script.src = assetURL("i18n." + base + ".js");
      script.async = true;
      script.onload = () => resolve(!!catalogs[base]);
      script.onerror = () => resolve(false);
      document.head.append(script);
    });
    catalogLoads.set(base, load);
    return load;
  }

  // normalizeLocale accepts a BCP-47/POSIX tag and returns a form Intl
  // understands, or "" when it is not a usable language tag.
  function normalizeLocale(value) {
    let s = String(value == null ? "" : value).trim();
    if (!s) return "";
    if (s.includes(".")) s = s.slice(0, s.indexOf("."));
    if (s.includes("@")) s = s.slice(0, s.indexOf("@"));
    s = s.replace(/_/g, "-");
    const parts = s.split("-").filter(Boolean);
    if (!parts.length || !/^[A-Za-z]{2,3}$/.test(parts[0])) return "";
    const lang = parts[0].toLowerCase();
    let region = "";
    for (let i = 1; i < parts.length; i++) {
      const part = parts[i];
      if (/^[A-Za-z]{2}$/.test(part)) { region = part.toUpperCase(); break; }
      if (/^[0-9]{3}$/.test(part)) { region = part; break; }
    }
    return region ? lang + "-" + region : lang;
  }

  function formatLocaleFor(language, candidate) {
    const wanted = supportedBase(language) || "en";
    const normalized = normalizeLocale(candidate);
    if (normalized && supportedBase(normalized) === wanted) return normalized;
    return DEFAULT_LOCALE[wanted];
  }

  function navigatorLanguages() {
    try {
      if (Array.isArray(navigator.languages) && navigator.languages.length) return navigator.languages.slice();
      if (navigator.language) return [navigator.language];
    } catch { /* a test or a restricted webview may not expose navigator */ }
    return [];
  }

  // resolve implements the required precedence:
  // explicit setting -> OS language -> navigator.languages -> English.
  // A known but unsupported OS language (de, ja, ...) never turns into
  // French or English from the navigator; it gets English plus the offer.
  function resolve(options = {}) {
    const setting = options.setting === "en" || options.setting === "fr" ? options.setting : "auto";
    const rawSystem = String(options.systemLanguage == null ? "" : options.systemLanguage).trim();
    const systemBase = supportedBase(rawSystem);
    const systemLocale = normalizeLocale(options.systemLocale) ||
      (systemBase ? DEFAULT_LOCALE[systemBase] : "");

    if (setting !== "auto") {
      return {
        language: setting,
        formatLocale: formatLocaleFor(setting, systemLocale),
        offerFrench: false,
        source: "setting",
      };
    }
    if (systemBase) {
      return {
        language: systemBase,
        formatLocale: formatLocaleFor(systemBase, systemLocale),
        offerFrench: false,
        source: "system",
      };
    }
    if (rawSystem) {
      return { language: "en", formatLocale: "en-US", offerFrench: true, source: "unsupported-system" };
    }
    const list = Array.isArray(options.navigatorLanguages) && options.navigatorLanguages.length
      ? options.navigatorLanguages : navigatorLanguages();
    for (const candidate of list) {
      const base = supportedBase(candidate);
      if (!base) continue;
      return {
        language: base,
        formatLocale: formatLocaleFor(base, candidate),
        offerFrench: false,
        source: "navigator",
      };
    }
    return { language: "en", formatLocale: "en-US", offerFrench: true, source: "fallback" };
  }

  function documentTitle() {
    const screen = document.documentElement.dataset.screen || "";
    const screenTitle = screen ? t("screen." + screen + ".title", null, "") : "";
    return screenTitle ? screenTitle + " · " + t("app.document_title") : t("app.document_title");
  }

  function setLanguage(next) {
    const value = typeof next === "string" ? { language: next } : (next || {});
    const language = supportedBase(value.language) || state.language;
    const formatLocale = formatLocaleFor(language, value.formatLocale || state.formatLocale);
    state.language = language;
    state.formatLocale = formatLocale;
    state.offerFrench = !!value.offerFrench;
    state.source = value.source || state.source;
    try {
      document.documentElement.lang = formatLocale;
      document.title = documentTitle();
    } catch { /* document is absent only in a non-browser test harness */ }
    return snapshot();
  }

  function configure(options = {}) {
    if (Object.prototype.hasOwnProperty.call(options, "ownerTimeZone")) {
      const requested = String(options.ownerTimeZone == null ? "" : options.ownerTimeZone).trim();
      state.ownerZone = "";
      state.zoneSource = "machine";
      state.zoneDiagnostic = "";
      if (requested) {
        if (validZone(requested)) {
          state.ownerZone = requested;
          state.zoneSource = "owner";
        } else {
          state.ownerZone = "UTC";
          state.zoneSource = "utc-fallback";
          state.zoneDiagnostic = requested;
        }
      }
    }
    return snapshot();
  }

  function validZone(zone) {
    try {
      new Intl.DateTimeFormat("en-US", { timeZone: zone }).format(new Date(0));
      return true;
    } catch { return false; }
  }

  function machineZone() {
    try {
      const zone = new Intl.DateTimeFormat().resolvedOptions().timeZone;
      return zone || "UTC";
    } catch { return "UTC"; }
  }

  function zoneFor(kind) {
    if (kind === "local") return machineZone();
    if (kind === "utc") return "UTC";
    if (state.ownerZone && state.zoneSource === "owner") return state.ownerZone;
    if (state.ownerZone && state.zoneSource === "utc-fallback") return "UTC";
    return machineZone();
  }

  function zoneInfo() {
    return {
      zone: zoneFor("owner"),
      source: state.ownerZone ? state.zoneSource : "machine",
      diagnostic: state.zoneDiagnostic,
      machineZone: machineZone(),
    };
  }

  function snapshot() {
    return {
      language: state.language,
      formatLocale: state.formatLocale,
      offerFrench: state.offerFrench,
      source: state.source,
      zone: zoneInfo(),
    };
  }

  function autoNumberFormat() {
    const key = state.formatLocale + "|auto";
    let format = numberFormats.get(key);
    if (!format) {
      format = new Intl.NumberFormat(state.formatLocale, { maximumFractionDigits: 3 });
      numberFormats.set(key, format);
    }
    return format;
  }

  function interpolate(template, params) {
    if (typeof template !== "string") return "";
    if (!params) return template;
    return template.replace(/\{([A-Za-z0-9_]+)\}/g, (whole, name) => {
      if (!Object.prototype.hasOwnProperty.call(params, name)) return whole;
      const value = params[name];
      if (typeof value === "number" && Number.isFinite(value)) return autoNumberFormat().format(value);
      return String(value);
    });
  }

  function entryFor(key) {
    const selected = catalogs[state.language];
    if (selected && Object.prototype.hasOwnProperty.call(selected, key)) return selected[key];
    const english = catalogs.en;
    if (english && Object.prototype.hasOwnProperty.call(english, key)) return english[key];
    return undefined;
  }

  function pluralForm(entry, params) {
    const count = params && params.count !== undefined ? Number(params.count) : 0;
    let category = count === 1 ? "one" : "other";
    try {
      let rules = pluralRules.get(state.formatLocale);
      if (!rules) {
        rules = new Intl.PluralRules(state.formatLocale);
        pluralRules.set(state.formatLocale, rules);
      }
      category = rules.select(Number.isFinite(count) ? count : 0);
    } catch { /* fall back to the one/other rule */ }
    return entry[category] !== undefined ? entry[category] : (entry.other !== undefined ? entry.other : entry.one);
  }

  // t(key, params?, fallback?) is the only UI wording lookup. A plural
  // entry is an object with one/other (and an optional zero); its form is
  // selected from params.count with Intl.PluralRules.
  function t(key, params, fallback) {
    const entry = entryFor(key);
    if (entry === undefined) {
      if (fallback !== undefined && fallback !== null && fallback !== "") return String(fallback);
      if (fallback === "") return "";
      return String(key);
    }
    if (entry && typeof entry === "object") return interpolate(pluralForm(entry, params), params);
    return interpolate(entry, params);
  }

  function tOr(key, fallback, params) {
    return t(key, params, fallback);
  }

  function has(key) {
    return entryFor(key) !== undefined;
  }

  function numberFormat(digits) {
    const key = state.formatLocale + "|" + String(digits);
    let format = numberFormats.get(key);
    if (!format) {
      format = new Intl.NumberFormat(state.formatLocale, {
        maximumFractionDigits: digits,
        minimumFractionDigits: digits,
      });
      numberFormats.set(key, format);
    }
    return format;
  }

  function formatNumber(value, digits = 0) {
    if (value === null || value === undefined || value === "" || Number.isNaN(Number(value))) return t("common.dash");
    return numberFormat(digits).format(Number(value));
  }

  // A message id is an identifier, not a count: never group its digits.
  function formatId(value) {
    if (value === null || value === undefined || value === "") return t("common.dash");
    return String(value);
  }

  function formatPercent(value, digits = 0) {
    if (value === null || value === undefined || Number.isNaN(Number(value))) return t("common.dash");
    try {
      const key = state.formatLocale + "|p" + digits;
      let format = numberFormats.get(key);
      if (!format) {
        format = new Intl.NumberFormat(state.formatLocale, {
          style: "percent",
          maximumFractionDigits: digits,
          minimumFractionDigits: digits,
        });
        numberFormats.set(key, format);
      }
      return format.format(Number(value));
    } catch { return formatNumber(Number(value) * 100, digits) + " %"; }
  }

  const usdFormats = new Map();

  function formatUsd(value) {
    const n = Number(value);
    if (!Number.isFinite(n)) return t("common.unknown");
    const digits = n > 0 && n < 0.01 ? 4 : 2;
    const key = state.formatLocale + "|" + String(digits);
    let format = usdFormats.get(key);
    if (format === undefined) {
      try {
        format = new Intl.NumberFormat(state.formatLocale, {
          style: "currency",
          currency: "USD",
          currencyDisplay: "narrowSymbol",
          minimumFractionDigits: 2,
          maximumFractionDigits: digits,
        });
      } catch {
        try {
          format = new Intl.NumberFormat(state.formatLocale, {
            style: "currency",
            currency: "USD",
            minimumFractionDigits: 2,
            maximumFractionDigits: digits,
          });
        } catch { format = null; }
      }
      usdFormats.set(key, format);
    }
    if (format) return format.format(n);
    return "USD " + formatNumber(n, digits);
  }

  function formatDuration(ns) {
    const n = Number(ns);
    if (!Number.isFinite(n) || n < 0) return t("common.dash");
    const ms = n / 1e6;
    if (ms < 1) return t("unit.duration_less_than_ms");
    if (ms < 1000) return formatNumber(Math.round(ms)) + " " + t("unit.millisecond");
    const s = ms / 1000;
    if (s < 90) return formatNumber(s < 10 ? s : Math.round(s), s < 10 ? 1 : 0) + " " + t("unit.second");
    const m = s / 60;
    if (m < 90) return formatNumber(m < 10 ? m : Math.round(m), m < 10 ? 1 : 0) + " " + t("unit.minute");
    const h = m / 60;
    return formatNumber(h < 10 ? h : Math.round(h), h < 10 ? 1 : 0) + " " + t("unit.hour");
  }

  function formatBytes(value) {
    const n = Number(value);
    if (!Number.isFinite(n) || n <= 0) return "0 " + t("unit.byte");
    const units = ["unit.byte", "unit.kilobyte", "unit.megabyte", "unit.gigabyte", "unit.terabyte"];
    let i = 0;
    let x = n;
    while (x >= 1000 && i < units.length - 1) { x /= 1000; i++; }
    const digits = x >= 10 || i === 0 ? 0 : 1;
    return formatNumber(x, digits) + " " + t(units[i]);
  }

  function parseTime(value) {
    if (!value) return null;
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return null;
    // Go encodes an unset time.Time as 0001-01-01T00:00:00Z.
    if (d.getUTCFullYear() <= 1) return null;
    return d;
  }

  function dateFormat(key, options) {
    const fullKey = state.formatLocale + "|" + key;
    let format = dateFormats.get(fullKey);
    if (!format) {
      format = new Intl.DateTimeFormat(state.formatLocale, options);
      dateFormats.set(fullKey, format);
    }
    return format;
  }

  // parseCalendarKey reads a date-only bucket key as a UTC noon instant, so
  // formatting a month or a day never shifts it into a neighbouring period.
  function parseCalendarKey(value, withDay) {
    const match = (withDay ? /^(\d{4})-(\d{2})-(\d{2})$/ : /^(\d{4})-(\d{2})$/).exec(String(value == null ? "" : value).trim());
    if (!match) return null;
    const year = Number(match[1]);
    const month = Number(match[2]);
    const day = withDay ? Number(match[3]) : 15;
    if (month < 1 || month > 12 || day < 1 || day > 31) return null;
    const d = new Date(Date.UTC(year, month - 1, day, 12));
    if (!Number.isFinite(d.getTime()) || d.getUTCMonth() !== month - 1) return null;
    return d;
  }

  // formatMonth renders a YYYY-MM bucket key as a localized month and year.
  function formatMonth(value) {
    const d = parseCalendarKey(value, false);
    if (!d) return String(value == null ? "" : value);
    try {
      return dateFormat("month|" + state.formatLocale, {
        month: "long",
        year: "numeric",
        timeZone: "UTC",
      }).format(d);
    } catch { return String(value == null ? "" : value); }
  }

  // formatDay renders a YYYY-MM-DD bucket key as a localized calendar date
  // without inventing a time of day.
  function formatDay(value) {
    const d = parseCalendarKey(value, true);
    if (!d) return String(value == null ? "" : value);
    try {
      return dateFormat("day|" + state.formatLocale, {
        year: "numeric",
        month: "short",
        day: "numeric",
        timeZone: "UTC",
      }).format(d);
    } catch { return String(value == null ? "" : value); }
  }

  function formatDate(value, options = {}) {
    const d = parseTime(value);
    if (!d) return t("common.dash");
    const zone = zoneFor(options.zone || "owner");
    const withTime = options.time !== false;
    const key = "date|" + zone + "|" + (options.year === false ? "noyear" : "year") +
      "|" + (withTime ? "time" : "day");
    try {
      return dateFormat(key, {
        year: options.year === false ? undefined : "numeric",
        month: "short",
        day: "2-digit",
        hour: withTime ? "2-digit" : undefined,
        minute: withTime ? "2-digit" : undefined,
        timeZone: zone,
      }).format(d);
    } catch {
      try { return d.toISOString(); } catch { return t("common.dash"); }
    }
  }


  function formatClock(value) {
    const d = parseTime(value);
    if (!d) return t("common.dash");
    const zone = zoneFor("owner");
    try {
      return dateFormat("clock|" + zone, {
        month: "short",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        timeZone: zone,
      }).format(d);
    } catch { return d.toISOString(); }
  }

  function formatRelative(value) {
    const d = parseTime(value);
    if (!d) return t("common.dash");
    const seconds = Math.round((Date.now() - d.getTime()) / 1000);
    const ahead = seconds < 0;
    const n = Math.abs(seconds);
    if (!ahead && n < 10) return t("time.just_now");
    try {
      let format = relativeFormats.get(state.formatLocale);
      if (!format) {
        format = new Intl.RelativeTimeFormat(state.formatLocale, { numeric: "auto" });
        relativeFormats.set(state.formatLocale, format);
      }
      const sign = ahead ? 1 : -1;
      if (n < 90) return format.format(sign * n, "second");
      const minutes = Math.round(n / 60);
      if (minutes < 90) return format.format(sign * minutes, "minute");
      const hours = Math.round(minutes / 60);
      if (hours < 36) return format.format(sign * hours, "hour");
    } catch { /* fall through to the date */ }
    return formatDate(d);
  }

  function localeCompare(a, b) {
    try { return String(a).localeCompare(String(b), state.formatLocale); }
    catch { return String(a).localeCompare(String(b)); }
  }

  // compareText is the one locale-aware text ordering for tables: numeric
  // segments compare by value ("2" before "10") and case/diacritics are
  // ignored, so Élodie and Elodie tie. Callers append their own stable
  // tie-break; the collator never mutates or formats a value.
  function compareText(a, b) {
    const left = String(a == null ? "" : a);
    const right = String(b == null ? "" : b);
    let collator = collators.get(state.formatLocale);
    if (!collator) {
      try {
        collator = new Intl.Collator(state.formatLocale, { numeric: true, sensitivity: "base" });
      } catch {
        collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
      }
      collators.set(state.formatLocale, collator);
    }
    return collator.compare(left, right);
  }

  // metaSeparator is the one metadata separator: the catalog owns the string
  // (a middle dot with one space on each side, identical in both languages).
  function metaSeparator() {
    return t("common.meta_separator");
  }

  // metaLine joins the present parts of one metadata line: an absent value
  // drops out, so a caller never paints a dangling separator.
  function metaLine(parts) {
    return (parts || []).filter(Boolean).join(metaSeparator());
  }

  window.MailsenseI18n = {
    t,
    tOr,
    has,
    ensure,
    resolve,
    setLanguage,
    configure,
    snapshot,
    zoneInfo,
    documentTitle,
    supportedBase,
    normalizeLocale,
    formatLocaleFor,
    formatNumber,
    formatId,
    formatPercent,
    formatUsd,
    formatDuration,
    formatBytes,
    parseTime,
    formatMonth,
    formatDay,
    formatDate,
    formatClock,
    formatRelative,
    localeCompare,
    compareText,
    metaSeparator,
    metaLine,
    defaultLocale: (language) => DEFAULT_LOCALE[supportedBase(language) || "en"],
  };
})();
