package ui

import (
	"strings"
	"testing"
)

// scopedSearchFunction returns the source of one top-level function in a
// module. The split bundle keeps every screen function at two-space
// indentation, so the shared helper can slice one definition precisely.
func scopedSearchFunction(t *testing.T, module, name string) string {
	t.Helper()
	src := readUIModule(t, module)
	lines := strings.Split(src, "\n")
	for _, span := range topLevelFunctions(t, src) {
		if span.name != name {
			continue
		}
		return strings.Join(lines[span.line-1:span.line+span.lines-1], "\n")
	}
	t.Fatalf("%s has no function %s", module, name)
	return ""
}

// The scoped search is one registration per route. A global binding that
// every screen overwrites loses the previous screen's binding on a cached
// Back, so the registry must survive clearScopedSearch.
func TestScopedSearchRegistrationIsPerRoute(t *testing.T) {
	search := readUIModule(t, "33-search.js")
	for _, want := range []string{
		`const scopedSearches = new Map();`,
		`function setScopedSearch(route, apply, placeholderKey, ariaKey, valueFn)`,
		`scopedSearches.set(route, { apply, placeholderKey, ariaKey, valueFn: valueFn || null });`,
		`function activateScopedSearch(route)`,
		`const scoped = scopedSearches.get(route);`,
	} {
		if !strings.Contains(search, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
	if strings.Contains(search, "scopedSearches.delete") || strings.Contains(search, "scopedSearches.clear") {
		t.Fatal("a scoped search registration is deleted; a cached mount needs it")
	}

	activate := scopedSearchFunction(t, "33-search.js", "activateScopedSearch")
	if !strings.Contains(activate, `dom.globalSearchInput.setAttribute("aria-label", t(scoped.ariaKey));`) {
		t.Error("activation does not give the shell bar the screen's accessible name")
	}
	if !strings.Contains(activate, `dom.globalSearchInput.placeholder = t(scoped.placeholderKey);`) {
		t.Error("activation does not set the screen's placeholder")
	}
	if !strings.Contains(activate, `dom.globalSearchClear.hidden = !value;`) {
		t.Error("activation does not show the clear button for a restored value")
	}
	if !strings.Contains(activate, `scoped.apply(value);`) {
		t.Error("activation does not let the mounted screen adopt a typed query")
	}

	set := scopedSearchFunction(t, "33-search.js", "setScopedSearch")
	if !strings.Contains(set, `if (state.route === route) activateScopedSearch(route);`) {
		t.Error("setScopedSearch does not activate a registration made on the current route")
	}

	clear := scopedSearchFunction(t, "33-search.js", "clearScopedSearch")
	if strings.Contains(clear, "scopedSearches") {
		t.Error("clearScopedSearch touches the registration map; it must only deactivate the shell bar")
	}
	if !strings.Contains(clear, `dom.globalSearchInput.setAttribute("aria-label", t("search.global_aria"));`) {
		t.Error("clearScopedSearch does not restore the global accessible name")
	}
	if !strings.Contains(clear, `dom.globalSearchInput.placeholder = t("search.global_placeholder");`) {
		t.Error("clearScopedSearch does not restore the global placeholder")
	}
}

// routerBlock returns the source between the first start marker and the next
// end marker. Bounding each router branch keeps a call in a later function
// from satisfying an earlier branch's contract.
func routerBlock(t *testing.T, router, start, end string) string {
	t.Helper()
	i := strings.Index(router, start)
	if i < 0 {
		t.Fatalf("router has no %q", start)
	}
	rest := router[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("router block %q has no %q", start, end)
	}
	return rest[:j]
}

// The router is the one mount point for the shell bar: a cached screen never
// runs its render again, so only the router can restore its registration.
func TestRouterActivatesScopedSearchOnEveryMount(t *testing.T) {
	router := readUIModule(t, "12-router.js")
	if got := strings.Count(router, "activateScopedSearch(route.name);"); got != 4 {
		t.Fatalf("router calls activateScopedSearch %d times, want history, same-route, cached and fresh mounts", got)
	}

	// Two same-route re-attach paths: Back/Forward inside one route identity
	// and clicking the active destination.
	offset := 0
	for {
		next := strings.Index(router[offset:], "attachCachedRoute();")
		if next < 0 {
			break
		}
		pos := offset + next
		block := router[pos:]
		if end := strings.Index(block, "return;"); end >= 0 {
			block = block[:end]
		}
		if !strings.Contains(block, "activateScopedSearch(route.name);") {
			t.Error("a same-route re-attach does not reactivate the registration")
		}
		offset = pos + len("attachCachedRoute();")
	}

	cached := routerBlock(t, router, "appendMain(cachedEntry.node", "refreshCachedRoute(route, key, cachedEntry);")
	if !strings.Contains(cached, "attachCachedRoute(key);") {
		t.Fatal("the cached mount block no longer attaches the route they cached")
	}
	if !strings.Contains(cached, "activateScopedSearch(route.name);") {
		t.Error("a cached mount does not reactivate the screen's registration")
	}

	fresh := routerBlock(t, router, "appendMain(node, 0, null, true, carriedFocus);", "restoreNavigationFocus();")
	if !strings.Contains(fresh, "activateScopedSearch(route.name);") {
		t.Error("a fresh mount does not reactivate the screen's registration")
	}
}

// Every scoped screen names its own visible label while the hidden proxy
// input keeps only the filter state. A missing aria key would silently fall
// back to the global "Search all mail" name.
func TestScopedSearchDescriptorsUseExistingAriaKeys(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, tc := range []struct {
		module string
		keys   []string
	}{
		{"24-companies.js", []string{"companies.search.placeholder", "companies.search.aria"}},
		{"40-settings.js", []string{"settings.filter_placeholder", "settings.filter_aria"}},
		{"50-logs.js", []string{"logs.component_placeholder", "logs.component_aria"}},
	} {
		src := readUIModule(t, tc.module)
		for _, key := range tc.keys {
			if _, ok := en[key]; !ok {
				t.Errorf("i18n.en.js lacks %q", key)
			}
			if _, ok := fr[key]; !ok {
				t.Errorf("i18n.fr.js lacks %q", key)
			}
			if !strings.Contains(src, `"`+key+`"`) {
				t.Errorf("%s does not name %q", tc.module, key)
			}
		}
	}

	companies := readUIModule(t, "24-companies.js")
	if !strings.Contains(companies, `"companies.search.placeholder", "companies.search.aria", () => state.companiesQuery);`) {
		t.Error("the Companies descriptor does not pass its aria key")
	}
	settings := readUIModule(t, "40-settings.js")
	if !strings.Contains(settings, `"settings.filter_placeholder", "settings.filter_aria", () => state.settingsQuery);`) {
		t.Error("the Settings descriptor does not pass its aria key")
	}
	logs := readUIModule(t, "50-logs.js")
	if !strings.Contains(logs, `"logs.component_placeholder", "logs.component_aria", () => state.logFilters.component);`) {
		t.Error("the Logs descriptor does not pass its aria key")
	}
	if _, ok := en["search.global_aria"]; !ok {
		t.Error("i18n.en.js lacks the global accessible name restored by clearScopedSearch")
	}
	if _, ok := fr["search.global_aria"]; !ok {
		t.Error("i18n.fr.js lacks the global accessible name restored by clearScopedSearch")
	}

	// The hidden proxy inputs are inert; the visible bar owns the name now.
	for _, tc := range []struct{ module, attr string }{
		{"24-companies.js", `"aria-label": t("companies.search.aria")`},
		{"40-settings.js", `"aria-label": t("settings.filter_aria")`},
		{"50-logs.js", `"aria-label": t("logs.component_aria")`},
	} {
		if strings.Contains(readUIModule(t, tc.module), tc.attr) {
			t.Errorf("%s still duplicates the scoped accessible name on a hidden input", tc.module)
		}
	}
}
