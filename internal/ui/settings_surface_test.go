package ui

import (
	"regexp"
	"strings"
	"testing"
)

// The settings screen has one row builder, one search over the whole schema,
// and one honest answer per row state. These tests read the sources because
// the guarantees are structural: a second renderer is how the default, the
// reset state, the environment lock and the restart badge drift apart.

// settingsModules are the tabs plus the router that carries their search.
// Every row is built in the first one.
var settingsModules = []string{"40-settings.js", "41-providers.js", "42-updates.js"}

func TestSettingsTabsShareOneRowBuilder(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`function settingRow(view, status, opts = {})`,
		`function settingRowBadges(view, matched)`,
		`function settingOwnerRow(view, owner, opts = {})`,
		`if (owner !== tab) return settingOwnerRow(`,
		`function settingsGroupNode(title, views, status, opts = {})`,
		`settingsGroupNode(t("settings.runtime.settings_title")`,
		`return settingRow(view, null, {`,
	} {
		if !strings.Contains(settings, want) && !strings.Contains(readUIModule(t, "41-providers.js"), want) {
			t.Errorf("the shared settings row builder lacks %q", want)
		}
	}
	// One owner of the row markup: the providers and runtime tabs may not
	// re-spell the badges or the row itself.
	for _, name := range settingsModules[1:] {
		js := readUIModule(t, name)
		for _, banned := range []string{`class: "setting-row"`, `class: "setting-badges"`, `class: "setting-control"`} {
			if strings.Contains(js, banned) {
				t.Errorf("%s builds its own settings row markup (%q)", name, banned)
			}
		}
	}
	// Every tab renders rows: General through its groups, Runtime through the
	// engine group, Providers through the role rows.
	if got := strings.Count(readUIModule(t, "41-providers.js"), "settingsGroupNode("); got != 1 {
		t.Errorf("41-providers.js calls settingsGroupNode %d times, want the one runtime group", got)
	}
	if got := strings.Count(readUIModule(t, "41-providers.js"), "return settingRow("); got != 1 {
		t.Errorf("41-providers.js builds %d role row sets, want the one shared builder", got)
	}
}

// TestSettingsSearchReachesEveryOwner: search reads the whole schema, and a
// match owned by another tab is a pointer to that tab, never a second editor.
func TestSettingsSearchReachesEveryOwner(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`function settingOwnerTab(view)`,
		`if (PROVIDER_ROLE_SETTINGS.has(key)) return "providers";`,
		`if (view && view.group === SETTINGS_RUNTIME_GROUP) return "runtime";`,
		`const general = generalSettingsList(view);`,
		`search: { list: all, byKey: new Map(all.map((item) => [item.key, item])) },`,
		`const scopeOf = () => searching && fullLoaded ? searchScope`,
		`applySettingsTabSearch(content, tab)`,
		`bindSettingsTabSearch(tab)`,
		`navigateWith(t("settings.search.open_owned", { tab: label }), "settings", { tab: owner, q: key },`,
		`function revealSettingMatch(seq)`,
		`if (matched) row.dataset.match = "1";`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	// The Advanced list still hides the rows another tab owns: search is the
	// only mode that reaches them, and it hands them to their owner.
	if strings.Contains(settings, `fullList = generalSettingsList`) {
		t.Error("Advanced renders the rows the Providers and Runtime tabs own")
	}
}

// TestSettingsRowsShowTheirDefault: every row names the schema default, and
// Reset is off when the effective value already is that default.
func TestSettingsRowsShowTheirDefault(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`function settingDefaultText(view)`,
		`function settingValuesEqual(view, left, right)`,
		`if (view && view.type === "duration") {`,
		`function settingIsDefault(view)`,
		`h("span", { text: t("settings.default_label") }), text(" "),`,
		`h("code", { class: "mono", text: settingDefaultText(view) })));`,
		`const atDefault = settingIsDefault(view);`,
		`disabled: atDefault, title: atDefault ? t("settings.reset_at_default") : ""`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	// The schema owns the default; the row never invents one.
	if strings.Contains(settings, `view.default ||`) {
		t.Error("the row substitutes its own default")
	}
}

// TestMailboxSettingsAreManagedInSources: the two mailbox keys are written by
// the mailbox endpoints, which answer 409 use_mailbox_endpoint. The row says
// where they are managed and offers no Apply or Reset.
func TestMailboxSettingsAreManagedInSources(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`const SETTINGS_MAILBOX_SOURCE = "sources";`,
		`const SETTINGS_MAILBOX_KEYS = new Set(["mailboxes.selected", "mailboxes.declared_addresses"]);`,
		`const mailbox = SETTINGS_MAILBOX_KEYS.has(key);`,
		`? { node: h("p", { class: "field-hint", text: t("settings.mailbox_hint") }), value: () => view.value }`,
		`navigateWith(t("settings.managed_in_sources"), SETTINGS_MAILBOX_SOURCE, {}, { small: true, icon: "arrow-right" })`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	if got := strings.Count(settings, `"mailboxes.selected"`); got != 1 {
		t.Errorf("mailboxes.selected is named %d times, want only the owner set", got)
	}
	// The two actions are built in the non-mailbox branch only: a mailbox row
	// cannot reach Apply or Reset.
	actions := jsLimb(t, settings, `const actions = mailbox`, `: opts.actions === false`)
	if strings.Contains(actions, "settings.apply") || strings.Contains(actions, "settings.reset") {
		t.Error("a mailbox row still offers Apply or Reset")
	}
	if !strings.Contains(actions, "settings.managed_in_sources") {
		t.Error("a mailbox row does not link to Sources")
	}
}

// TestEnvLayerCannotPretendToSave: an effective value from the environment
// wins over settings.json, so Apply is disabled and the hint names the exact
// MAILSENSE_ variable.
func TestEnvLayerCannotPretendToSave(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`return "MAILSENSE_" + String(key || "").split(".").join("_").toUpperCase();`,
		`const envLocked = layer === "env";`,
		`const envHint = envLocked ? t("settings.env_hint", { env: settingEnvName(key) }) : "";`,
		`if (envHint) right.append(h("div", { class: "field-hint", text: envHint }));`,
		`disabled: envLocked, title: envHint`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	readTab := readUIModule(t, "41-providers.js")
	if !strings.Contains(readTab, `select.disabled = (view.layer || "default") === "env";`) {
		t.Error("a provider role selected by the environment stays editable")
	}
}

// TestRestartCopyNamesWhatReloadDoes: Reload re-reads settings.json and cannot
// apply a restart-required value. The banner says which action does what.
func TestRestartCopyNamesWhatReloadDoes(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`h("p", { class: "field-hint", text: t("settings.restart_explain") }),`,
		`"warn", t("settings.restart_explain"))`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	for _, tc := range []struct{ name, text, want string }{
		{"en", en["settings.restart_explain"].text, "does not apply"},
		{"fr", fr["settings.restart_explain"].text, "n’applique pas"},
		{"en", en["settings.restart_banner"].plural["other"], "Restart Mailsense"},
		{"fr", fr["settings.restart_banner"].plural["other"], "Redémarrage de Mailsense"},
	} {
		if !strings.Contains(tc.text, tc.want) {
			t.Errorf("%s restart copy = %q, want %q", tc.name, tc.text, tc.want)
		}
	}
}

// TestProviderHealthIsShown: the card reads the health the API already
// returns instead of only key_present, and a failed test links to Logs.
func TestProviderHealthIsShown(t *testing.T) {
	providers := readUIModule(t, "41-providers.js")
	for _, want := range []string{
		`const status = record && record.status && typeof record.status === "object" ? record.status : null;`,
		`const healthy = status.healthy === true;`,
		`Number(status.calls) || 0`,
		`Number(status.failures) || 0`,
		`boundedReason(status.last_error)`,
		`t("settings.providers.health.no_calls")`,
		`t("settings.providers.health.ok")`,
		`t("settings.providers.health.failing")`,
		`t("settings.providers.health.counts", { calls: formatNumber(calls), failures: formatNumber(failures) })`,
		`t("settings.providers.health.last_call", { when: formatRelative(status.last_call) })`,
		`t("settings.providers.health.last_error", { message: lastError })`,
		`providerHealthNodes(record),`,
	} {
		if !strings.Contains(providers, want) {
			t.Errorf("41-providers.js lacks %q", want)
		}
	}
	if got := strings.Count(providers, `navigateWith(t("logs.open"), "logs", { component: "engine" }`); got < 3 {
		t.Errorf("a failed provider test has %d Logs links, want the card and both test outcomes", got)
	}
}

// TestRouterEscapesAttributeSelectors: the focus-restore selector escaped a
// quote but not a backslash, so a value carrying both selected another row.
// CSS.escape is the one escaping owner now.
func TestRouterEscapesAttributeSelectors(t *testing.T) {
	router := readUIModule(t, "12-router.js")
	if !strings.Contains(router, `dom.main.querySelector("[" + attribute + "=" + CSS.escape(String(value)) + "]")`) {
		t.Error("the focus-restore selector does not use CSS.escape")
	}
	if regexp.MustCompile(`replace\(\s*/"/g`).MatchString(router) {
		t.Error("the hand-rolled quote escape is still in 12-router.js")
	}
	script := readAsset(t, "app.js")
	if !strings.Contains(script, "CSS.escape(String(value))") {
		t.Error("the generated bundle lost the CSS.escape selector")
	}
	if regexp.MustCompile(`replace\(\s*/"/g`).MatchString(script) {
		t.Error("the generated bundle still builds a selector by hand")
	}
}

// jsLimb returns the source between two markers, or fails when a marker is
// missing. It keeps a structural claim about one branch readable.
func jsLimb(t *testing.T, source, start, end string) string {
	t.Helper()
	from := strings.Index(source, start)
	if from < 0 {
		t.Fatalf("missing marker %q", start)
	}
	to := strings.Index(source[from:], end)
	if to < 0 {
		t.Fatalf("missing end marker %q", end)
	}
	return source[from : from+to]
}

// TestSettingsSurfaceCatalog: every label the round added exists in both
// languages with the same placeholders.
func TestSettingsSurfaceCatalog(t *testing.T) {
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	keys := []string{
		"settings.default_label",
		"settings.current_value",
		"settings.reset_at_default",
		"settings.env_hint",
		"settings.managed_in_sources",
		"settings.mailbox_hint",
		"settings.restart_explain",
		"settings.search.match",
		"settings.search.open_owned",
		"settings.search.tab_shown",
		"settings.search.show_tab",
		"settings.providers.health.ok",
		"settings.providers.health.failing",
		"settings.providers.health.no_calls",
		"settings.providers.health.counts",
		"settings.providers.health.last_call",
		"settings.providers.health.last_error",
		"logs.open",
		"logs.filter.job",
		"logs.job_clear",
		"logs.empty_job",
		"sources.link.sync_settings",
		"sources.link.outlook_filters",
		"sources.link.failed_job",
		"learning.link.settings",
	}
	for _, key := range keys {
		english, ok := en[key]
		if !ok {
			t.Errorf("i18n.en.js lacks %q", key)
			continue
		}
		french, ok := fr[key]
		if !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
			continue
		}
		if english.isPlural != french.isPlural {
			t.Errorf("%q changes shape between catalogs", key)
			continue
		}
		want := placeholders(strings.Join(entryValues(english), " "))
		got := placeholders(strings.Join(entryValues(french), " "))
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%q placeholders differ: en %v, fr %v", key, want, got)
		}
	}
}

func entryValues(entry catalogEntry) []string {
	if !entry.isPlural {
		return []string{entry.text}
	}
	out := make([]string, 0, len(entry.plural))
	for _, text := range entry.plural {
		out = append(out, text)
	}
	return out
}

// TestSettingsRowBehavior runs the one row builder against the four states the
// round made honest: a mailbox row, an environment-locked row, a row at its
// default, and a search match owned by another tab.
func TestSettingsRowBehavior(t *testing.T) {
	runUISandbox(t, "settings_row", settingsRowHarness, "settings row behavior: ok")
}

const settingsRowHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I);
sandbox.tOr = I.tOr.bind(I);
sandbox.I18N = I;
sandbox.metaLine = I.metaLine;
sandbox.metaSeparator = I.metaSeparator;
sandbox.formatNumber = (value) => String(value);
sandbox.state = { settingsQuery: "" };
sandbox.SETTINGS_SIMPLE_KEY_LIST = [];
sandbox.jsonValue = (value) => value === null || value === undefined ? ""
  : typeof value === "string" ? value : JSON.stringify(value);
sandbox.joinPath = (...parts) => parts.filter(Boolean).join("/");
sandbox.routeHash = (name, params) => "#/" + name + (params && Object.keys(params).length
  ? "?" + Object.keys(params).map((key) => key + "=" + encodeURIComponent(params[key])).join("&")
  : "");

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, dataset: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag, attrs: {}, children: [], text: "", dataset: {}, value: "",
    disabled: false, hidden: false, textContent: "",
    classList: { add() {}, remove() {}, toggle() {} },
    addEventListener() {}, focus() {},
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); }
    if (key === "value") node.value = value === undefined || value === null ? "" : String(value);
    if (key === "disabled") node.disabled = !!value;
    if (key === "hidden") node.hidden = !!value;
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.text = (value) => textNode(value);
sandbox.badge = (value, kind, title) => makeNode("span", { class: "badge " + (kind || ""), text: value, title }, []);
sandbox.pill = (value, kind, className) => makeNode("span", { class: "pill " + (kind || ""), text: value }, []);
sandbox.button = (label, onClick, opts) => makeNode("button", {
  text: label,
  disabled: opts && opts.disabled,
  title: opts && opts.title,
}, []);
sandbox.link = (label, href, opts) => makeNode("a", {
  href,
  class: (opts && opts.class) || "",
  title: (opts && opts.title) || "",
}, [makeNode("span", { text: label }, [])]);
sandbox.navigateWith = (label, name, params, opts) => {
  const anchor = sandbox.link(label, sandbox.routeHash(name, params), opts);
  anchor.dataset.crosslink = name;
  return anchor;
};
sandbox.field = (label, control) => makeNode("div", { class: "field" }, [control]);
sandbox.withBusy = async (_trigger, _label, fn) => fn();
sandbox.markFieldError = () => {};
sandbox.request = async () => { throw new Error("the row wrote before a click"); };
sandbox.toast = () => {};
sandbox.ApiError = class extends Error {};
sandbox.apiFailure = (error) => ({ message: String((error && error.message) || error) });
sandbox.invalidateAllReads = () => {};
sandbox.invalidateSettingsReads = () => {};
sandbox.staleRouteCache = () => {};
sandbox.refreshResourceUsage = () => {};
sandbox.updateLearningChrome = () => {};
sandbox.loadLearning = async () => ({});
sandbox.applyLanguageLive = async () => {};
sandbox.cached = () => null;
sandbox.setScopedSearch = () => {};
sandbox.replaceRoute = () => {};
sandbox.renderRoute = () => {};
sandbox.emptyState = () => makeNode("div", {}, []);

const providersModule = fs.readFileSync(path.join(dir, "js", "41-providers.js"), "utf8");
vm.runInContext(
  providersModule.slice(providersModule.indexOf("// runtime-panel:start"), providersModule.indexOf("// runtime-panel:end")),
  sandbox,
  { filename: "js/41-providers.js#runtime-panel" },
);
const settingsModule = fs.readFileSync(path.join(dir, "js", "40-settings.js"), "utf8");
vm.runInContext(settingsModule, sandbox, { filename: "js/40-settings.js" });
const settingRow = vm.runInContext("settingRow", sandbox);

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function buttons(row) {
  return all(row, (node) => node.tag === "button");
}
function buttonNamed(row, label) {
  return buttons(row).find((node) => node.text === label) || null;
}
function anchorTo(row, screen) {
  return all(row, (node) => node.tag === "a" && node.dataset.crosslink === screen)[0] || null;
}

const mailbox = { key: "mailboxes.selected", group: "Mailboxes", type: "list", item_type: "string",
  value: ["store-aaaaaaaaaaaa"], default: [], layer: "file", description: "Picked stores." };
const mailboxRow = settingRow(mailbox, null, { tab: "general" });
const mailboxText = texts(mailboxRow).join(" | ");
assert.ok(!buttonNamed(mailboxRow, "Apply"), "a mailbox row still offers Apply");
assert.ok(!buttonNamed(mailboxRow, "Reset"), "a mailbox row still offers Reset");
assert.ok(mailboxText.includes("Manage in Sources"), "a mailbox row does not point at Sources");
assert.ok(mailboxText.includes("Mailboxes are picked"), "a mailbox row does not say where it is managed");
assert.equal(anchorTo(mailboxRow, "sources").attrs.href, "#/sources", "the mailbox link does not open Sources");

const envView = { key: "log.level", group: "Logging", type: "enum", enum: ["debug", "info", "warn"],
  value: "warn", default: "debug", layer: "env", description: "Log level." };
const envRow = settingRow(envView, null, { tab: "general" });
const envApply = buttonNamed(envRow, "Apply");
assert.ok(envApply && envApply.disabled, "an environment-locked row still offers Apply");
assert.ok(String(envApply.attrs.title || "").includes("MAILSENSE_LOG_LEVEL"),
  "the disabled Apply does not name the environment variable: " + envApply.attrs.title);
assert.ok(texts(envRow).join(" | ").includes("MAILSENSE_LOG_LEVEL"),
  "the row does not show which variable sets it");
assert.ok(!buttonNamed(envRow, "Reset").disabled, "a value that differs from its default cannot be reset");

const plainRow = settingRow({ key: "log.level", group: "Logging", type: "enum", enum: ["debug", "info", "warn"],
  value: "debug", default: "debug", layer: "default", description: "Log level." }, null, { tab: "general" });
assert.ok(buttonNamed(plainRow, "Reset").disabled, "Reset is enabled at the default value");
assert.ok(String(buttonNamed(plainRow, "Reset").attrs.title || "").length > 0,
  "a disabled Reset does not say why");
assert.ok(!buttonNamed(plainRow, "Apply").disabled, "Apply is disabled without an environment layer");
const plainText = texts(plainRow).join(" | ");
assert.ok(plainText.includes("Default") && plainText.includes("debug"), "the row does not show its default");

const changedRow = settingRow({ key: "log.level", group: "Logging", type: "enum", enum: ["debug", "warn"],
  value: "warn", default: "debug", layer: "file", description: "Log level." }, null, { tab: "general" });
assert.ok(!buttonNamed(changedRow, "Reset").disabled, "Reset is disabled for a value that differs from its default");

const runtimeView = { key: "sidecar.idle_timeout", group: "Local runtime", type: "duration",
  value: "15m0s", default: "15m", layer: "default", description: "Idle stop." };
const stub = settingRow(runtimeView, null, { tab: "general" });
assert.ok(!buttonNamed(stub, "Apply"), "a row owned by another tab still offers Apply");
const stubLink = anchorTo(stub, "settings");
assert.ok(stubLink, "a row owned by another tab has no link to its owner");
assert.equal(stubLink.attrs.href, "#/settings?tab=runtime&q=sidecar.idle_timeout",
  "the owner link does not carry the key: " + stubLink.attrs.href);
assert.ok(texts(stub).join(" | ").includes("Local runtime"), "the owner link does not name the tab");

sandbox.state.settingsQuery = "sidecar.idle_timeout";
const matchedRow = settingRow(runtimeView, null, { tab: "runtime" });
assert.equal(matchedRow.dataset.match, "1", "a typed search does not mark the row it matched");
assert.ok(texts(matchedRow).join(" | ").includes("Matches this search"), "the match has no badge");

console.log("settings row behavior: ok");
`
