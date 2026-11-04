package ui

import (
	"strings"
	"testing"
)

func heldRemovalKeys() []string {
	return []string{
		"sources.held.title",
		"sources.held.body",
		"sources.held.applies",
		"sources.held.applies_confirm",
		"sources.held.meta",
		"sources.held.confirm",
		"sources.held.confirm_body",
		"sources.held.confirming",
		"sources.held.done",
		"sources.held.result",
		"read.held_removals_unavailable",
		"api.unavailable",
	}
}

// TestHeldRemovalCatalogsCoverEveryKey keeps the confirmation card and its
// dialog in step across both catalogs. The body and the dialog question are
// plural entries: they must keep their one and other forms and the same
// placeholders in French and English.
func TestHeldRemovalCatalogsCoverEveryKey(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range heldRemovalKeys() {
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
		if english.isPlural != french.isPlural {
			t.Errorf("%q changes shape between catalogs", key)
			continue
		}
		for _, entry := range []catalogEntry{english, french} {
			for _, value := range entryStrings(entry) {
				if strings.TrimSpace(value) == "" {
					t.Errorf("%q is empty", key)
				}
				if strings.Contains(value, "—") || strings.Contains(value, "--") {
					t.Errorf("%q uses a dash: %q", key, value)
				}
			}
		}
		if english.isPlural {
			want := strings.Join(placeholders(english.plural["one"]+english.plural["other"]), ",")
			got := strings.Join(placeholders(french.plural["one"]+french.plural["other"]), ",")
			if want != got {
				t.Errorf("%q placeholders differ: en %v, fr %v", key, want, got)
			}
			for _, form := range []string{"one", "other"} {
				if _, ok := french.plural[form]; !ok {
					t.Errorf("i18n.fr.js %q lacks the %q form", key, form)
				}
			}
			continue
		}
		want := strings.Join(placeholders(english.text), ",")
		got := strings.Join(placeholders(french.text), ",")
		if want != got {
			t.Errorf("%q placeholders differ: en %v, fr %v", key, want, got)
		}
	}
}

// The held card states the real removal policy: a folder with applies_at is
// applied automatically on that date, and a folder without it is applied only
// when the owner confirms. The old copy promised the messages stayed.
func TestHeldRemovalCopyStatesTheAutoApplyDeadline(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, tc := range []struct {
		name    string
		catalog map[string]catalogEntry
	}{
		{"i18n.en.js", en}, {"i18n.fr.js", fr},
	} {
		if stale := tc.catalog["sources.held.body"]; strings.Contains(strings.Join(entryStrings(stale), " "), "stays in place") ||
			strings.Contains(strings.Join(entryStrings(stale), " "), "restent en place") ||
			strings.Contains(strings.Join(entryStrings(stale), " "), "reste en place") {
			t.Errorf("%s sources.held.body still promises the removal stays: %v", tc.name, entryStrings(stale))
		}
		applies := tc.catalog["sources.held.applies"]
		if applies.isPlural || !containsPlaceholder(applies, "when") {
			t.Errorf("%s sources.held.applies must be a plain sentence with {when}: %v", tc.name, entryStrings(applies))
		}
		confirm := tc.catalog["sources.held.applies_confirm"]
		if confirm.isPlural || strings.Contains(confirm.text, "{") {
			t.Errorf("%s sources.held.applies_confirm must be one plain sentence: %v", tc.name, entryStrings(confirm))
		}
	}

	sources := readUIModule(t, "25-sources.js")
	for _, want := range []string{
		`const appliesAt = parseTime(folder && folder.applies_at);`,
		`? t("sources.held.applies", { when: formatDate(folder.applies_at) })`,
		`: t("sources.held.applies_confirm")`,
		`class: "held-removal-applies"`,
	} {
		if !strings.Contains(sources, want) {
			t.Errorf("25-sources.js lacks the applies_at rendering %q", want)
		}
	}
}

func containsPlaceholder(entry catalogEntry, name string) bool {
	for _, value := range entryStrings(entry) {
		if strings.Contains(value, "{"+name+"}") {
			return true
		}
	}
	return false
}

// A confirmation removes mail, so it must use the mail topic, and the held
// list itself gets one narrow topic that only stales Sources. Neither path
// may spell a broad cache clear that would also drop Settings, Logs or Ask.
func TestHeldRemovalConfirmationUsesTheMailInvalidation(t *testing.T) {
	sources := readUIModule(t, "25-sources.js")
	for _, want := range []string{
		`request("/api/held-removals")`,
		`"/api/held-removals/" + encodeURIComponent(folderID) + "/confirm"`,
		`afterBackgroundAction(["mail", "heldRemovals"], { hard: true })`,
		`t("sources.held.confirm_body"`,
		`if (!folders.length) return null;`,
		`action.dataset.heldFolder = folderID;`,
	} {
		if !strings.Contains(sources, want) {
			t.Errorf("25-sources.js lacks %q", want)
		}
	}

	shell := readUIModule(t, "13-shell.js")
	body := topicBody(t, shell, "heldRemovals")
	if reads := jsStringArrayField(t, body, "reads"); !sameStrings(reads, []string{"/api/held-removals"}) {
		t.Errorf("heldRemovals reads = %v, want the held list only", reads)
	}
	if routes := jsStringArrayField(t, body, "routes"); !sameStrings(routes, []string{"sources"}) {
		t.Errorf("heldRemovals routes = %v, want Sources only", routes)
	}
	if !strings.Contains(shell, `invalidateTopics(["heldRemovals"], { refresh: false });`) {
		t.Error("13-shell.js does not refresh the held list after a sync")
	}
	if !strings.Contains(shell, `scheduleSourcesRefresh();`) {
		t.Error("13-shell.js does not refresh the mounted Sources screen after a sync")
	}
}
