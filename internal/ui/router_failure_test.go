package ui

import (
	"io/fs"
	"path"
	"strings"
	"testing"
)

// A route render can fail before it produces a screen element: the Brief gate
// guard used to return a DocumentFragment, whose missing dataset made the
// router throw after the screen was built. The router owns one null-safe
// failure test, and no call site reads the marker directly.
func TestRouteFailureCheckHasOneNullSafeOwner(t *testing.T) {
	router := readUIModule(t, "12-router.js")

	predicate := functionBody(t, router, "function routeFailed(node)")
	for _, want := range []string{
		"node && node.dataset",
		`node.dataset.errorScreen === "1"`,
		`node.dataset.routeError === "1"`,
	} {
		if !strings.Contains(predicate, want) {
			t.Errorf("routeFailed lacks %q", want)
		}
	}
	if got := strings.Count(router, "dataset.routeError"); got != 1 {
		t.Fatalf("12-router.js reads dataset.routeError %d time(s), want only routeFailed", got)
	}
	for _, want := range []string{
		"if (isErrorScreen(node) && !isErrorScreen(dom.main.firstElementChild))",
		"if (routeFailed(node)) {",
		"if (!node || routeFailed(node) || !canReplaceMain()) {",
		"if (!routeFailed(node)) {",
		"if (routeFailed(node)) return false;",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("12-router.js lacks the route failure step %q", want)
		}
	}
	for _, forbidden := range []string{
		"isErrorScreen(node) ||",
		"!isErrorScreen(node) &&",
	} {
		if strings.Contains(router, forbidden) {
			t.Errorf("12-router.js still tests the route markers directly with %q", forbidden)
		}
	}
}

// The Brief gate guard is a route root. It must return an Element, not a
// fragment, and only its retry surface may be the route node there; the inline
// read failure inside the Brief keeps its own host and may skip the wrapper.
func TestBriefGateGuardReturnsARouteElement(t *testing.T) {
	brief := readUIModule(t, "21-brief.js")
	guard := functionBody(t, brief, "function briefGateGuard()")
	if strings.Contains(guard, "wrap: false") {
		t.Fatal("briefGateGuard still returns a fragment; a route node must be an Element")
	}
	if !strings.Contains(guard, "errorState(null, () => loadGate().then(() => refreshActiveRoute())") {
		t.Error("briefGateGuard lost the shared error surface with its gate retry")
	}
	render := functionBody(t, brief, "function renderBrief(route, seq)")
	if !strings.Contains(render, "briefGateGuard()") {
		t.Error("renderBrief no longer paints the gate guard")
	}
}

// wrap: false leaves the shared state wrapper to an inline surfaces' own host.
// Only these four owners may opt out; a new one could turn a route root into a
// fragment again, so it must be reviewed here.
func TestStateWrapperOptOutStaysInline(t *testing.T) {
	allowed := map[string]string{
		"briefErrorBlock":         "21-brief.js",
		"labelsMoreNode":          "26-labels.js",
		"labelsCompleteNode":      "26-labels.js",
		"companyProfileStateNode": "24-companies.js",
	}
	names, err := moduleNames("js", ".js")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, name := range names {
		body, err := fs.ReadFile(Files, path.Join("js", name))
		if err != nil {
			t.Fatalf("js/%s: %v", name, err)
		}
		source := string(body)
		if !strings.Contains(source, "wrap: false") {
			continue
		}
		spans := topLevelFunctions(t, source)
		for i, line := range strings.Split(source, "\n") {
			if !strings.Contains(line, "wrap: false") {
				continue
			}
			owner := ""
			for _, span := range spans {
				if i+1 >= span.line && i+1 < span.line+span.lines {
					owner = span.name
					break
				}
			}
			want, ok := allowed[owner]
			if !ok {
				t.Errorf("%s line %d: wrap: false inside %q; an inline host must wrap it", name, i+1, owner)
				continue
			}
			if want != name {
				t.Errorf("%s: wrap: false moved out of %s", name, want)
			}
			found[owner] = name
		}
	}
	for owner, name := range allowed {
		if found[owner] != name {
			t.Errorf("%s: %s no longer opts out of the wrapper", name, owner)
		}
	}

	// The Brief's remaining opt-out is the inline section failure, whose host
	// is the surface that supplies the frame the wrapper would duplicate.
	brief := readUIModule(t, "21-brief.js")
	inline := functionBody(t, brief, "function briefErrorBlock(error)")
	if !strings.Contains(inline, `class: "surface brief-error"`) || !strings.Contains(inline, "wrap: false") {
		t.Fatal("briefErrorBlock is no longer an inline surface hosted by its own element")
	}
}
