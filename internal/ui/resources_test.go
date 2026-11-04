package ui

import (
	"strings"
	"testing"
)

// resourceCopyKeys are the wording the resource guard adds: the settings
// group and its five rows, the resource nouns shared by the API error and the
// job strip, and the two sentences that name a ceiling.
func resourceCopyKeys() []string {
	return []string{
		"settings.group.Resources",
		"settings.resource_usage.now",
		"settings.resource_usage.limits",
		"settings.resource_usage.disabled",
		"settings.resource_usage.paused",
		"settings.resource.guard.description",
		"settings.resource.cpu.max_percent.description",
		"settings.resource.ram.max_percent.description",
		"settings.resource.disk.max_percent.description",
		"settings.resource.sample_interval.description",
		"resource.name.cpu",
		"resource.name.ram",
		"resource.name.disk",
		"api.resource_limit",
		"jobs.paused",
	}
}

// TestResourceCatalogsCoverEveryKey keeps the two catalogs in step with the
// schema group. The placeholders must match exactly too: a sentence that
// loses {limit} would still render, just without the ceiling.
func TestResourceCatalogsCoverEveryKey(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range resourceCopyKeys() {
		english, ok := en[key]
		if !ok {
			t.Errorf("i18n.en.js is missing %q", key)
			continue
		}
		french, ok := fr[key]
		if !ok {
			t.Errorf("i18n.fr.js is missing %q", key)
			continue
		}
		if english.isPlural || french.isPlural {
			t.Errorf("%q changed to a plural entry; the resource wording is a sentence", key)
			continue
		}
		for _, value := range []string{english.text, french.text} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%q is empty", key)
			}
			if strings.Contains(value, "—") || strings.Contains(value, "--") {
				t.Errorf("%q uses a dash: %q", key, value)
			}
		}
		want := strings.Join(placeholders(english.text), ",")
		got := strings.Join(placeholders(french.text), ",")
		if want != got {
			t.Errorf("%q placeholders differ: en %v, fr %v", key, want, got)
		}
	}
}

// The protective default and the ceiling behavior are the point of the five
// descriptions. The shared catalog guard already forbids dashes, but these
// rows must also stay explanatory rather than becoming bare labels.
func TestResourceDescriptionsExplainTheGuard(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, tc := range []struct {
		key    string
		needle string
	}{
		{"settings.resource.guard.description", "refused"},
		{"settings.resource.cpu.max_percent.description", "pauses"},
		{"settings.resource.ram.max_percent.description", "pauses"},
		{"settings.resource.disk.max_percent.description", "refused"},
		{"settings.resource.sample_interval.description", "paused job"},
	} {
		if !strings.Contains(strings.ToLower(en[tc.key].text), tc.needle) {
			t.Errorf("i18n.en.js %q does not explain %q", tc.key, tc.needle)
		}
	}
	for _, tc := range []struct {
		key    string
		needle string
	}{
		{"settings.resource.guard.description", "refusé"},
		{"settings.resource.cpu.max_percent.description", "pause"},
		{"settings.resource.ram.max_percent.description", "pause"},
		{"settings.resource.disk.max_percent.description", "refusé"},
		{"settings.resource.sample_interval.description", "travail en pause"},
	} {
		if !strings.Contains(strings.ToLower(fr[tc.key].text), tc.needle) {
			t.Errorf("i18n.fr.js %q does not explain %q", tc.key, tc.needle)
		}
	}
	for key, want := range map[string]string{
		"settings.group.Resources": "Resources",
		"resource.name.cpu":        "CPU",
		"resource.name.ram":        "Memory",
		"resource.name.disk":       "Disk",
	} {
		if got := en[key].text; got != want {
			t.Errorf("i18n.en.js %q = %q; want %q", key, got, want)
		}
	}
	for key, want := range map[string]string{
		"settings.group.Resources": "Ressources",
		"resource.name.cpu":        "Processeur",
		"resource.name.ram":        "Mémoire",
		"resource.name.disk":       "Disque",
	} {
		if got := fr[key].text; got != want {
			t.Errorf("i18n.fr.js %q = %q; want %q", key, got, want)
		}
	}
}

// A resource_limit refusal is the one place the UI names a host resource in
// an error card. The resource noun must be localized before the sentence is
// composed, and the article must make each noun grammatical.
func TestResourceLimitErrorIsLocalized(t *testing.T) {
	runUISandbox(t, "resource_limit_error", resourceLimitHarness, "resource limit error: ok")
}

const resourceLimitHarness = `"use strict";

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
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.I18N = I;

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("class ApiError");
const end = app.indexOf("function problemLabel");
assert.ok(start >= 0 && end > start, "the ApiError/apiFailure slice is missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#api-failure" });
const apiFailure = vm.runInContext("apiFailure", sandbox);
const ApiError = vm.runInContext("ApiError", sandbox);

function check(language, locale, cases) {
  I.setLanguage({ language, formatLocale: locale });
  for (const [resource, percent, limit, want] of cases) {
    const failure = apiFailure(new ApiError(503, {
      code: "resource_limit",
      message: "server english",
      params: { resource, percent, limit },
    }));
    assert.equal(failure.message, want, language + "/" + resource + " did not map: " + failure.message);
    assert.equal(failure.diagnostic, "server english", language + "/" + resource + " lost the server diagnostic");
  }
}

check("en", "en-US", [
  ["cpu", 92, 80, "Background work is paused: the CPU is at 92 %, above your 80 % limit. It resumes automatically."],
  ["ram", 74, 85, "Background work is paused: memory is at 74 %, above your 85 % limit. It resumes automatically."],
  ["disk", 95, 90, "Background work is paused: the disk is at 95 %, above your 90 % limit. It resumes automatically."],
]);

check("fr", "fr-FR", [
  ["cpu", 92, 80, "Le travail en arrière-plan est en pause : le processeur est à 92 %, au-dessus de votre limite de 80 %. Il reprend automatiquement."],
  ["ram", 74, 85, "Le travail en arrière-plan est en pause : la mémoire est à 74 %, au-dessus de votre limite de 85 %. Il reprend automatiquement."],
  ["disk", 95, 90, "Le travail en arrière-plan est en pause : le disque est à 95 %, au-dessus de votre limite de 90 %. Il reprend automatiquement."],
]);

console.log("resource limit error: ok");
`
