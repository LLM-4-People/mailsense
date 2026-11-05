package ui

import (
	"strings"
	"testing"
)

// The update screen has one owner: the Settings Updates tab reads the
// persisted status, and the first-open What's New overlay consumes the same
// snapshot. These checks keep a second reader or a second marker writer from
// appearing, and keep the tab wired to the learning-independent API.
func TestUpdateSurfaceOwnsItsStatusAndActions(t *testing.T) {
	updates := readUIModule(t, "42-updates.js")
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`request("/api/update/check"`,
		`request("/api/update/skip"`,
		`request("/api/update/seen"`,
		`function loadUpdateStatus()`,
		`function updatesPanel(`,
		`function maybeOpenWhatsNew(`,
		`if (info.notes_seen || info.skipped) return;`,
		`state.whatsNewPrompted === info.latest`,
	} {
		if !strings.Contains(updates, want) {
			t.Errorf("42-updates.js lacks %q", want)
		}
	}
	for _, want := range []string{
		`if (view && view.group === SETTINGS_UPDATES_GROUP) return "updates";`,
		`["updates", t("settings.tab.updates")]`,
		`tab === "updates" ? updatesPanel(update, settings, status)`,
		`item.group !== SETTINGS_RUNTIME_GROUP && item.group !== SETTINGS_UPDATES_GROUP`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	shell := readUIModule(t, "13-shell.js")
	if !strings.Contains(shell, `source.addEventListener("update.state", handleUpdateStateEvent);`) {
		t.Error("13-shell.js does not subscribe to update.state")
	}
	if !strings.Contains(readUIModule(t, "52-init.js"), `loadUpdateStatus().then(() => maybeOpenWhatsNew())`) {
		t.Error("52-init.js does not try the What's New overlay after the first paint")
	}
}

// TestUpdateCatalogCoversTheSurface pins the copy for both languages. The
// panel and the overlay are new surfaces; a missing translation would leave a
// language with an English fallback this project does not accept.
func TestUpdateCatalogCoversTheSurface(t *testing.T) {
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	for _, key := range []string{
		"settings.tab.updates",
		"settings.update.title",
		"settings.update.sub",
		"settings.update.current",
		"settings.update.latest",
		"settings.update.last_checked",
		"settings.update.never_checked",
		"settings.update.available",
		"settings.update.up_to_date",
		"settings.update.not_checked",
		"settings.update.check",
		"settings.update.checking",
		"settings.update.skip",
		"settings.update.skipping",
		"settings.update.skipped",
		"settings.update.failed",
		"settings.update.unavailable",
		"settings.update.notes",
		"settings.update.settings_title",
		"update.whats_new.title",
		"update.whats_new.eyebrow",
		"update.whats_new.got_it",
		"update.whats_new.current",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
}
