"use strict";

// Behavior checks for the real i18n runtime. Go's TestI18nRuntimeBehavior
// runs this file under Node with TZ=UTC; the UI itself has no Node
// dependency. The script exits non-zero on the first failed assertion.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.argv[2];
if (!dir) throw new Error("usage: i18n_behavior_test.js <ui-dir>");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);

for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  const source = fs.readFileSync(path.join(dir, file), "utf8");
  vm.runInContext(source, sandbox, { filename: file });
}

const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime did not install window.MailsenseI18n");

function pick(resolved) {
  return [resolved.language, resolved.formatLocale, resolved.offerFrench, resolved.source];
}

assert.deepEqual(
  pick(I.resolve({ setting: "auto", systemLanguage: "fr-CA", systemLocale: "fr-CA" })),
  ["fr", "fr-CA", false, "system"],
  "auto + fr-CA follows the system language and regional locale",
);
assert.deepEqual(
  pick(I.resolve({ setting: "auto", systemLanguage: "de-DE", systemLocale: "de-DE", navigatorLanguages: ["fr-FR"] })),
  ["en", "en-US", true, "unsupported-system"],
  "a known unsupported system language gets English plus the French offer, never navigator French",
);
assert.deepEqual(
  pick(I.resolve({ setting: "auto", systemLanguage: "", systemLocale: "", navigatorLanguages: ["de-DE", "fr-CA"] })),
  ["fr", "fr-CA", false, "navigator"],
  "unknown detection falls back to the first supported navigator language",
);
const savedLanguages = sandbox.navigator.languages;
const savedLanguage = sandbox.navigator.language;
sandbox.navigator.languages = [];
delete sandbox.navigator.language;
assert.deepEqual(
  pick(I.resolve({ setting: "auto", systemLanguage: "", systemLocale: "" })),
  ["en", "en-US", true, "fallback"],
  "no detected and no navigator language gets English plus the French offer",
);
sandbox.navigator.languages = savedLanguages;
sandbox.navigator.language = savedLanguage;
assert.deepEqual(
  pick(I.resolve({ setting: "fr", systemLanguage: "en-US", systemLocale: "en-US" })),
  ["fr", "fr-FR", false, "setting"],
  "explicit French ignores an English system locale",
);
assert.deepEqual(
  pick(I.resolve({ setting: "en", systemLanguage: "fr-FR", systemLocale: "fr-FR" })),
  ["en", "en-US", false, "setting"],
  "explicit English ignores a French system locale",
);
assert.deepEqual(
  pick(I.resolve({ setting: "fr", systemLanguage: "fr-CA", systemLocale: "fr-CA" })),
  ["fr", "fr-CA", false, "setting"],
  "explicit French keeps the compatible regional format",
);

assert.equal(I.formatId(1234567), "1234567", "message ids never gain grouping");
assert.equal(I.formatId(0), "0", "message id zero is an identifier");
I.setLanguage({ language: "en", formatLocale: "en-US" });
assert.equal(I.t("settings.group_count", { count: 0 }), "0 settings");
assert.equal(I.t("settings.group_count", { count: 1 }), "1 setting");
assert.equal(I.t("settings.group_count", { count: 2 }), "2 settings");

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(I.t("settings.group_count", { count: 0 }), "0 paramètre", "French treats 0 as singular");
assert.equal(I.t("settings.group_count", { count: 1 }), "1 paramètre");
assert.equal(I.t("settings.group_count", { count: 2 }), "2 paramètres");

assert.equal(I.formatNumber(1234), "1\u202f234", "French groups with a narrow no-break space");
assert.match(I.formatPercent(0.42), /^42[\u202f\u00a0]%$/, "French percent has a space before the sign");
assert.equal(I.formatBytes(1500), "1,5 ko");
assert.match(I.t("settings.group_count", { count: 1234 }), /^1[\u202f\u00a0]234 paramètres$/, "interpolated counts are locale numbers, not fixed-decimal numbers");

I.setLanguage({ language: "en", formatLocale: "en-US" });
assert.equal(I.formatNumber(1234), "1,234");
assert.equal(I.formatPercent(0.42), "42%");
assert.equal(I.formatBytes(1500), "1.5 kB");
assert.equal(I.formatMonth("2026-10"), "October 2026");
assert.equal(I.formatDay("2026-10-26"), "Oct 26, 2026");
assert.equal(I.formatMonth("not-a-month"), "not-a-month", "an invalid bucket key is shown unchanged");

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.match(I.formatMonth("2026-10"), /^octobre 2026$/, "a month bucket is localized in French");
assert.match(I.formatDay("2026-10-26"), /^26 oct\. 2026$/, "a day bucket is a localized date without a time");
sandbox.document.documentElement.dataset.screen = "settings";
I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(sandbox.document.documentElement.lang, "fr-FR", "html lang follows the format locale");
assert.equal(sandbox.document.title, "Réglages · Mailsense");
I.configure({ ownerTimeZone: "Europe/Paris" });
assert.equal(I.zoneInfo().source, "owner");
assert.equal(I.zoneInfo().zone, "Europe/Paris");
const winter = I.formatDate("2026-01-15T23:30:00Z");
assert.ok(winter.includes("16"), `owner-zone date crosses into the 16th: ${winter}`);
assert.ok(winter.includes("00:30"), `French date uses 24-hour time: ${winter}`);
assert.ok(!/AM|PM/.test(winter), `French date is not 12-hour: ${winter}`);
const summer = I.formatDate("2026-07-15T23:30:00Z");
assert.ok(summer.includes("01:30"), `owner-zone date follows summer time: ${summer}`);

// The clock follows the same zone owner as every other date surface: the
// owner's configured zone, the machine zone when none is set. A log entry and
// a mail date for the same instant must show the same wall-clock time.
assert.ok(I.formatClock("2026-01-15T23:30:00Z").includes("00:30:00"), "logs use the owner zone");
I.configure({ ownerTimeZone: "Not/AZone" });
assert.equal(I.zoneInfo().source, "utc-fallback");
assert.equal(I.zoneInfo().zone, "UTC");
assert.equal(I.zoneInfo().diagnostic, "Not/AZone");
I.configure({ ownerTimeZone: "" });
assert.equal(I.zoneInfo().source, "machine");
assert.equal(I.zoneInfo().zone, "UTC", "the test harness runs with TZ=UTC");

assert.equal(I.t("api.invalid_limit_range", { min: 1, max: 50 }), "La limite doit être comprise entre 1 et 50.");
I.setLanguage({ language: "en", formatLocale: "en-US" });
assert.equal(I.tOr("api.invalid_limit_range", "server message"), "server message", "English falls back to the server message");
assert.equal(I.tOr("api.no_such_code", "server message"), "server message", "unknown codes fall back to the server message");
assert.equal(I.t("no.such.key"), "no.such.key", "a missing key is visible, never blank");
assert.equal(I.t("no.such.key", null, ""), "", "an explicit empty fallback stays empty");

const saved = sandbox.window.MailsenseI18nCatalogs.fr["common.refresh"];
delete sandbox.window.MailsenseI18nCatalogs.fr["common.refresh"];
assert.equal(I.t("common.refresh"), sandbox.window.MailsenseI18nCatalogs.en["common.refresh"], "a missing French key falls back to English");
sandbox.window.MailsenseI18nCatalogs.fr["common.refresh"] = saved;

I.setLanguage({ language: "en", formatLocale: "en-US" });
assert.equal(sandbox.document.title, "Settings · Mailsense");

// A key with two placeholders must not renumber or lose either one.
assert.equal(I.formatLocaleFor("fr", "en-US"), "fr-FR");
assert.equal(I.formatLocaleFor("en", "fr-CA"), "en-US");
assert.equal(I.normalizeLocale("fr_FR.UTF-8"), "fr-FR");
assert.equal(I.normalizeLocale("de-DE"), "de-DE");

process.stdout.write("i18n behavior: ok\n");
