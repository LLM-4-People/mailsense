package ui

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestSetupScreenStaysWiredOutsideTheRouter protects the first-run overlay.
// Setup is deliberately not a route: adding it to SCREEN_IDS changes the
// router contract and every screen test. It is a boot-time overlay over the
// existing screens, and this test keeps it that way.
func TestSetupScreenStaysWiredOutsideTheRouter(t *testing.T) {
	jsNames, err := moduleNames("js", ".js")
	if err != nil {
		t.Fatal(err)
	}
	cssNames, err := moduleNames("css", ".css")
	if err != nil {
		t.Fatal(err)
	}
	if !containsName(jsNames, "05-setup.js") {
		t.Fatal("05-setup.js is not in the js manifest")
	}
	if !containsName(cssNames, "05-setup.css") {
		t.Fatal("05-setup.css is not in the css manifest")
	}

	core := readModule(t, "js", "00-core.js")
	if strings.Contains(core, `"setup"`) {
		t.Fatal("setup became a router screen; it must stay an overlay")
	}

	init := readModule(t, "js", "52-init.js")
	openAt := strings.Index(init, "maybeOpenSetup()")
	renderAt := strings.Index(init, "await renderRoute()")
	if openAt < 0 || renderAt < 0 || openAt > renderAt {
		t.Fatal("the boot sequence must open setup before the first route paints")
	}

	setup := readModule(t, "js", "05-setup.js")
	for _, route := range []string{
		`"/api/setup"`,
		`"/api/mailboxes/discover"`,
		`"/api/mailboxes/pick"`,
		`"/api/settings/ui.setup.done"`,
	} {
		if !strings.Contains(setup, route) {
			t.Errorf("setup does not use the existing owner %s", route)
		}
	}
	if !strings.Contains(setup, `!view || !view.needed`) {
		t.Error("setup must open only when the backend says it is needed")
	}
}

// TestSetupCatalogCoversEveryKey keeps the overlay translatable. The two
// dynamic prefixes are checked against the literals the module itself uses,
// so a new step or language cannot ship without both catalogs.
func TestSetupCatalogCoversEveryKey(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	used := map[string]bool{}
	for _, match := range regexp.MustCompile(`t\("(setup\.[^"]+)"\)`).FindAllStringSubmatch(setup, -1) {
		used[match[1]] = true
	}
	for _, name := range []string{"welcome", "mailbox", "model", "finish"} {
		used["setup.step."+name] = true
	}
	for _, name := range []string{"auto", "en", "fr"} {
		used["setup.language."+name] = true
	}
	if len(used) == 0 {
		t.Fatal("no setup keys were found in the module")
	}
	for _, language := range []string{"en", "fr"} {
		catalog := readCatalog(t, "i18n."+language+".js")
		for key := range used {
			if _, ok := catalog[key]; !ok {
				t.Errorf("i18n.%s.js is missing %s", language, key)
			}
		}
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func readModule(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := fs.ReadFile(Files, path.Join(dir, name))
	if err != nil {
		t.Fatalf("%s/%s: %v", dir, name, err)
	}
	return string(body)
}
