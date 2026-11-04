package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// A prefetch renders a destination before mount, with the live state. A screen
// whose render writes module-level state would therefore mutate the screen the
// owner is using. The policy check must run before renderRouteNode, and the one
// set must name every stateful destination.
func TestPrefetchNeverRendersStatefulScreens(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)

	declStart := strings.Index(js, "const PREFETCH_UNSAFE = new Set([")
	if declStart < 0 {
		t.Fatal("PREFETCH_UNSAFE is not declared")
	}
	stop := strings.Index(js[declStart:], "]);")
	if stop < 0 {
		t.Fatal("PREFETCH_UNSAFE has no end")
	}
	commentStart := strings.LastIndex(js[:declStart], "// PREFETCH_UNSAFE names")
	if commentStart < 0 {
		t.Fatal("PREFETCH_UNSAFE has no owner comment")
	}
	unsafe := js[declStart : declStart+stop]
	wantRoutes := []string{"labels", "companies", "answers", "settings", "mail", "search"}
	for _, name := range wantRoutes {
		if !strings.Contains(unsafe, `"`+name+`"`) {
			t.Errorf("PREFETCH_UNSAFE does not name %q", name)
		}
	}
	if got := strings.Count(unsafe, `"`) / 2; got != len(wantRoutes) {
		t.Errorf("PREFETCH_UNSAFE names %d routes, want %d", got, len(wantRoutes))
	}
	if !strings.Contains(js[commentStart:declStart+stop], "shadow context") {
		t.Error("PREFETCH_UNSAFE does not document how an entry leaves the set")
	}

	body := functionBody(t, js, "async function prefetchRoute(")
	check := strings.Index(body, "PREFETCH_UNSAFE.has(route.name)")
	render := strings.Index(body, "renderRouteNode(route, seq)")
	cache := strings.Index(body, "touchRouteCache(key, routeCacheEntry(route, node))")
	if check < 0 {
		t.Fatal("prefetchRoute does not consult PREFETCH_UNSAFE")
	}
	if render < 0 || cache < 0 {
		t.Fatal("prefetchRoute no longer renders and caches a destination")
	}
	if check > render || check > cache {
		t.Fatal("prefetchRoute consults PREFETCH_UNSAFE after rendering or caching")
	}
	tail := body[check:]
	returned := strings.Index(tail, "return true;")
	if returned < 0 || returned > strings.Index(tail, "renderRouteNode") {
		t.Fatal("an unsafe destination is not skipped before renderRouteNode")
	}
	// The existing All-Mail guard is separate from the policy and stays after
	// it: an unsafe route is skipped without any destination-specific check.
	if !strings.Contains(body, `route.name === "mail" && parseMode(route) === "all"`) {
		t.Fatal("prefetchRoute lost the All-Mail guard")
	}

	scheduler := functionBody(t, js, "function scheduleRoutePrefetch(")
	if !strings.Contains(scheduler, "navPrefetchLinks.add(href)") {
		t.Fatal("the scheduler lost its once-per-href marker")
	}
	if !strings.Contains(scheduler, "if (!cached) navPrefetchLinks.delete(href);") {
		t.Fatal("a warm attempt no longer keeps the once-per-href marker")
	}
}
