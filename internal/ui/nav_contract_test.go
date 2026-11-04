package ui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// navGroupContract is one sidebar disclosure group and the destinations it
// owns, in reading order.
type navGroupContract struct {
	id    string
	order []string
}

var navGroupContracts = []navGroupContract{
	{"work", []string{"hub", "brief", "mail", "companies"}},
	{"ask", []string{"ask", "answers"}},
	{"teach", []string{"labels", "learning"}},
	{"configure", []string{"sources", "settings"}},
	{"system", []string{"logs"}},
}

// TestNavContract keeps the grouped sidebar honest: every screen appears
// exactly once, Answers is a real destination, the disclosure controls stay
// on the group summaries, and both catalogs name every group.
func TestNavContract(t *testing.T) {
	body, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)

	screenIDs := []string{"hub", "brief", "mail", "companies", "ask", "labels", "answers", "learning", "sources", "settings", "logs"}
	navRE := regexp.MustCompile(`data-nav="([^"]+)"`)
	counts := map[string]int{}
	var order []string
	for _, match := range navRE.FindAllStringSubmatch(page, -1) {
		counts[match[1]]++
		order = append(order, match[1])
	}
	for _, name := range screenIDs {
		if counts[name] != 1 {
			t.Errorf("nav destination %s appears %d times, want exactly once", name, counts[name])
		}
		delete(counts, name)
	}
	for name := range counts {
		t.Errorf("nav destination %q is not a SCREEN_ID", name)
	}
	if strings.Count(page, `data-nav="answers"`) != 1 {
		t.Fatal("index.html must carry exactly one Answers destination")
	}

	groupRE := regexp.MustCompile(`(?s)<details class="nav-group" data-nav-group="([^"]+)"[^>]*>(.*?)</details>`)
	groupMatches := groupRE.FindAllStringSubmatch(page, -1)
	if len(groupMatches) != len(navGroupContracts) {
		t.Fatalf("index.html has %d nav groups, want %d", len(groupMatches), len(navGroupContracts))
	}
	var grouped []string
	for index, group := range navGroupContracts {
		if groupMatches[index][1] != group.id {
			t.Errorf("nav group %d is %q, want %q", index, groupMatches[index][1], group.id)
		}
		groupBody := groupMatches[index][2]
		if !strings.Contains(groupBody, `<summary class="nav-group-summary" aria-expanded="true">`) {
			t.Errorf("nav group %s lacks an expanded native summary", group.id)
		}
		var got []string
		for _, match := range navRE.FindAllStringSubmatch(groupBody, -1) {
			got = append(got, match[1])
		}
		if strings.Join(got, ",") != strings.Join(group.order, ",") {
			t.Errorf("nav group %s holds %v, want %v", group.id, got, group.order)
		}
		grouped = append(grouped, got...)
	}
	if strings.Join(grouped, ",") != strings.Join(order, ",") {
		t.Fatalf("grouped destinations %v do not follow the sidebar order %v", grouped, order)
	}

	// The brand is a home link, and the narrow-window menu toggle names the
	// drawer it controls. The item links carry no disclosure state.
	if !strings.Contains(page, `<a class="brand" href="#/hub">`) {
		t.Error("the brand is not a link to Home")
	}
	if !strings.Contains(page, `id="nav-toggle"`) ||
		!strings.Contains(page, `aria-controls="nav-menu"`) ||
		!strings.Contains(page, `id="nav-menu"`) {
		t.Error("the menu toggle does not control the navigation drawer")
	}
	linkRE := regexp.MustCompile(`<a[^>]*data-nav="[^"]+"[^>]*>`)
	for _, tag := range linkRE.FindAllString(page, -1) {
		if strings.Contains(tag, "aria-expanded") {
			t.Errorf("nav item link carries aria-expanded: %s", tag)
		}
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, group := range navGroupContracts {
		key := "nav.group." + group.id
		english, ok := en[key]
		if !ok || strings.TrimSpace(english.text) == "" {
			t.Errorf("i18n.en.js lacks a label for the %s group", group.id)
			continue
		}
		french, ok := fr[key]
		if !ok || strings.TrimSpace(french.text) == "" {
			t.Errorf("i18n.fr.js lacks a label for the %s group", group.id)
			continue
		}
		for _, label := range []string{english.text, french.text} {
			if strings.Contains(label, "—") || strings.Contains(label, " – ") || strings.Contains(label, "--") {
				t.Errorf("nav group %s uses punctuation the copy rules forbid: %q", group.id, label)
			}
		}
	}
	for key, want := range map[string][2]string{
		"nav.menu":    {"Menu", "Menu"},
		"nav.answers": {"Answers", "Réponses"},
	} {
		if en[key].text != want[0] || fr[key].text != want[1] {
			t.Errorf("%s wording en=%q fr=%q, want %q and %q", key, en[key].text, fr[key].text, want[0], want[1])
		}
	}

	// The persisted disclosure key and the drawer lifecycle live in one shell
	// owner; a rename or a missing Escape handler breaks the feature silently.
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const NAV_GROUPS_KEY = "mailsense.nav.groups"`,
		`function syncNavGroups(name)`,
		`function installNav()`,
		`function setNavDrawer(open, options = {})`,
		`sidebar.classList.toggle("drawer-open", open);`,
		`stored[group.dataset.navGroup] = group.open;`,
		`if (event.key !== "Escape" || state.modalOpen || !navDrawerOpen()) return;`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js lacks the navigation contract %q", required)
		}
	}
}
