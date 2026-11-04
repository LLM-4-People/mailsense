package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every Companies state goes through the shared state helpers. A hand-built
// icon/title/body block drifted from the other screens and, when nested in a
// pane that already draws its own box, produced a double frame. The served
// bundle is generated from this module, so the source is the owner to check.
func TestCompaniesStatesUseTheSharedHelpers(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "24-companies.js"))
	if err != nil {
		t.Fatal(err)
	}
	module := string(source)
	if strings.Contains(module, `class: "empty-icon"`) {
		t.Fatal("the Companies module still hand-builds an empty icon block")
	}
	if !strings.Contains(module, `emptyState(title, body, actions, iconName, { wrap: false })`) {
		t.Fatal("the profile pane does not delegate to emptyState")
	}
	if !strings.Contains(module, `hostClass: "companies-list-empty"`) {
		t.Fatal("the list pane does not keep its host class on the shared state")
	}
	if !strings.Contains(module, `loadingState(t("common.loading"), { className: "companies-profile-update", role: "status" })`) {
		t.Fatal("the profile update note does not use loadingState")
	}
	if strings.Contains(module, `class: "companies-profile-update", role: "status"`) {
		t.Fatal("the profile update note is still built by hand")
	}
}

// The profile pane is itself the box: the shared state must contribute its
// content without the dashed wrapper that would draw a second frame, and the
// list pane keeps its own host class instead of the default one.
func TestSharedStateOffersHostShapes(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "01-components.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(source)
	for _, required := range []string{`opts.wrap === false`, `opts.hostClass || "empty"`, `opts.heading || "h2"`, `opts.icon === false`, `opts.body === false`} {
		if !strings.Contains(js, required) {
			t.Fatalf("emptyState lacks the host shape %q", required)
		}
	}
	// One action node and a list of actions are both one row. Testing
	// `actions.length` alone dropped the Ask suggestion group (a single
	// node), which the Ask e2e spec caught.
	if !strings.Contains(js, `Array.isArray(actions) ? actions.filter(Boolean) : actions ? [actions] : []`) {
		t.Fatal("emptyState no longer accepts a single action node")
	}
	if !strings.Contains(js, `actionNodes.length ? h("div", { class: "button-row" }, actionNodes) : null`) {
		t.Fatal("emptyState does not render the normalized action row")
	}
}
