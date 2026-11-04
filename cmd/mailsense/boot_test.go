package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// TestMainStartsTheListenerBeforeTheWarm guards the boot order in the one
// function that owns it: the listener and descriptor must exist before the
// derived warm starts, the window must open while the warm still runs, and
// no blocking warm may remain on the startup path.
func TestMainStartsTheListenerBeforeTheWarm(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "runWithShutdownUI" {
			body = fn.Body
			break
		}
	}
	if body == nil {
		t.Fatal("main.go has no runWithShutdownUI function")
	}

	var (
		listen     token.Pos
		startWarm  token.Pos
		warmDirect token.Pos
		window     token.Pos
	)
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && id.Name == "srv" && fun.Sel.Name == "Start" && !listen.IsValid() {
				listen = call.Pos()
			}
		case *ast.Ident:
			switch fun.Name {
			case "startWarm":
				if !startWarm.IsValid() {
					startWarm = call.Pos()
				}
			case "warmDerivedTracked":
				if !startWarm.IsValid() {
					startWarm = call.Pos()
				}
			case "warmDerived":
				if !warmDirect.IsValid() {
					warmDirect = call.Pos()
				}
			case "desktopUI":
				if !window.IsValid() {
					window = call.Pos()
				}
			}
		}
		return true
	})

	if !listen.IsValid() {
		t.Fatal("runWithShutdownUI never calls srv.Start")
	}
	if warmDirect.IsValid() {
		t.Fatalf("runWithShutdownUI still calls the blocking warmDerived at %s", fset.Position(warmDirect))
	}
	if !startWarm.IsValid() {
		t.Fatal("runWithShutdownUI never starts the background warm")
	}
	if listen >= startWarm {
		t.Fatalf("the warm starts at %s before the listener at %s", fset.Position(startWarm), fset.Position(listen))
	}
	if !window.IsValid() {
		t.Fatal("runWithShutdownUI never opens the desktop window")
	}
	if startWarm >= window {
		t.Fatalf("the window opens at %s before the warm starts at %s", fset.Position(window), fset.Position(startWarm))
	}
}

// The first-open stop is registered after the store's defer, so LIFO runs it
// first: the discovery is joined before Store.Close.
func TestMainJoinsFirstOpenDiscoveryBeforeTheStoreCloses(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "runWithShutdownUI" {
			body = fn.Body
			break
		}
	}
	if body == nil {
		t.Fatal("main.go has no runWithShutdownUI function")
	}
	var start, stop, storeClose token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "startFirstOpenDiscovery" && !start.IsValid() {
				start = node.Pos()
			}
		case *ast.DeferStmt:
			switch fun := node.Call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "stopFirstOpen" && !stop.IsValid() {
					stop = node.Pos()
				}
			case *ast.FuncLit:
				// The store close is conditional on the joins, so it lives in
				// a deferred closure; a bare defer st.Close() would close the
				// store even when a producer was not joined.
				if storeClose.IsValid() {
					break
				}
				ast.Inspect(fun.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "st" && sel.Sel.Name == "Close" && !storeClose.IsValid() {
						storeClose = call.Pos()
					}
					return true
				})
			}
		}
		return true
	})
	if !start.IsValid() {
		t.Fatal("runWithShutdownUI never starts first-open discovery")
	}
	if !stop.IsValid() {
		t.Fatal("runWithShutdownUI never defers stopFirstOpen")
	}
	if !storeClose.IsValid() {
		t.Fatal("runWithShutdownUI never defers the conditional st.Close")
	}
	if start >= stop {
		t.Fatalf("first-open discovery starts at %s after its stop is deferred at %s",
			fset.Position(start), fset.Position(stop))
	}
	if storeClose >= stop {
		t.Fatalf("stopFirstOpen is deferred at %s before st.Close at %s; LIFO would close the store first",
			fset.Position(stop), fset.Position(storeClose))
	}
}

// The boot snapshot covers every warm view in the one declared order and
// reaches done only after the semantic stage finishes.
func TestBootStateReportsEveryWarmView(t *testing.T) {
	tr := newWarmTrace()
	boot := newBootState()
	warmDerivedTracked(t.Context(), tr.services(), boot, nil)
	boot.start(viewSemanticSearch)
	boot.finish(viewSemanticSearch, nil)
	boot.ready()

	snap := boot.snapshot()
	if snap.State != "ready" {
		t.Fatalf("state %q, want ready", snap.State)
	}
	if snap.Total != len(warmViewOrder) {
		t.Fatalf("total %d, want %d", snap.Total, len(warmViewOrder))
	}
	if snap.Done != snap.Total || snap.Failed != 0 {
		t.Fatalf("done %d, failed %d, total %d", snap.Done, snap.Failed, snap.Total)
	}
	if len(snap.Views) != len(warmViewOrder) {
		t.Fatalf("%d views in the snapshot, want %d", len(snap.Views), len(warmViewOrder))
	}
	for i, want := range warmViewOrder {
		got := snap.Views[i]
		if got.Name != string(want) {
			t.Fatalf("view %d is %q, want %q", i, got.Name, want)
		}
		if got.State != "done" {
			t.Fatalf("view %q is %q, want done", got.Name, got.State)
		}
	}
	if snap.Current != "" {
		t.Fatalf("current %q after ready, want empty", snap.Current)
	}
}

// A failed view stays visible after ready: readiness is not a claim that
// every warm succeeded.
func TestBootStateKeepsAFailedViewVisible(t *testing.T) {
	tr := newWarmTrace()
	tr.learningErr = errors.New("learning: no latch")
	boot := newBootState()
	warmDerivedTracked(t.Context(), tr.services(), boot, nil)
	boot.start(viewSemanticSearch)
	boot.finish(viewSemanticSearch, nil)
	boot.ready()

	snap := boot.snapshot()
	if snap.State != "ready" {
		t.Fatalf("state %q, want ready", snap.State)
	}
	if snap.Failed != 1 {
		t.Fatalf("failed %d, want 1: %+v", snap.Failed, snap.Views)
	}
	if snap.Done != snap.Total-1 {
		t.Fatalf("done %d, total %d: a failed view is not done", snap.Done, snap.Total)
	}
	for _, view := range snap.Views {
		want := "done"
		if view.Name == string(viewLearning) {
			want = "failed"
		}
		if view.State != want {
			t.Fatalf("view %q is %q, want %q", view.Name, view.State, want)
		}
	}
}

// A failed dependent view stays visible after ready while every other view
// completes: one read model's error never blocks readiness or its siblings.
func TestBootStateReachesReadyWithAFailedDependentView(t *testing.T) {
	tr := newWarmTrace()
	tr.profilesErr = errors.New("companies: the profile store failed")
	boot := newBootState()
	warmDerivedTracked(t.Context(), tr.services(), boot, nil)
	boot.start(viewSemanticSearch)
	boot.finish(viewSemanticSearch, nil)
	boot.ready()

	snap := boot.snapshot()
	if snap.State != "ready" {
		t.Fatalf("state %q, want ready", snap.State)
	}
	if snap.Failed != 1 {
		t.Fatalf("failed %d, want 1: %+v", snap.Failed, snap.Views)
	}
	if snap.Done != snap.Total-1 {
		t.Fatalf("done %d, total %d: a failed view is not done", snap.Done, snap.Total)
	}
	for _, view := range snap.Views {
		want := "done"
		if view.Name == string(viewCompanies) {
			want = "failed"
		}
		if view.State != want {
			t.Fatalf("view %q is %q, want %q: %+v", view.Name, view.State, want, snap.Views)
		}
	}
	for _, view := range []string{"agenda", "commitments"} {
		if got := tr.count(view); got != 1 {
			t.Fatalf("%s warmed %d times beside the failed view, want 1", view, got)
		}
	}
	if got := tr.count("conversations"); got != 1+len(threadintel.States) {
		t.Fatalf("conversations warmed %d pages beside the failed view, want %d", got, 1+len(threadintel.States))
	}
	if got := tr.count("brief"); got != 1 {
		t.Fatalf("Brief warmed %d times after a failed input, want 1", got)
	}
}

// A view that never ran is reported failed by ready, not left pending
// forever, and Done counts only the views that finished.
func TestBootStateMarksPendingViewsFailedOnReady(t *testing.T) {
	boot := newBootState()
	boot.start(viewLearning)
	boot.finish(viewLearning, nil)
	boot.ready()

	snap := boot.snapshot()
	if snap.State != "ready" || snap.Total != len(warmViewOrder) {
		t.Fatalf("state %q total %d", snap.State, snap.Total)
	}
	if snap.Done != 1 || snap.Failed != len(warmViewOrder)-1 {
		t.Fatalf("done %d, failed %d, want 1 and %d", snap.Done, snap.Failed, len(warmViewOrder)-1)
	}
	for _, view := range snap.Views {
		want := "failed"
		if view.Name == string(viewLearning) {
			want = "done"
		}
		if view.State != want {
			t.Fatalf("view %q is %q, want %q", view.Name, view.State, want)
		}
	}
}

// A skipped stage is neither done nor failed: it stays visible with the
// ceiling that refused it, and ready never claims it ran.
func TestBootStateReportsSkippedStagesHonestly(t *testing.T) {
	boot := newBootState()
	boot.start(viewMail)
	boot.finish(viewMail, nil)
	boot.skip(viewLearning, "resource: cpu at 95% is above the 80% limit")
	boot.start(viewSemanticSearch)
	boot.finish(viewSemanticSearch, nil)
	boot.ready()

	snap := boot.snapshot()
	if snap.State != "ready" {
		t.Fatalf("state %q, want ready", snap.State)
	}
	if snap.Skipped != 1 || snap.Done != 2 || snap.Failed != len(warmViewOrder)-3 {
		t.Fatalf("skipped %d done %d failed %d", snap.Skipped, snap.Done, snap.Failed)
	}
	for _, view := range snap.Views {
		switch view.Name {
		case string(viewLearning):
			if view.State != "skipped" {
				t.Fatalf("learning is %q, want skipped", view.State)
			}
			if want := "resource: cpu at 95% is above the 80% limit"; view.Error != want {
				t.Fatalf("learning reason %q, want %q", view.Error, want)
			}
			if view.DurationMS != 0 {
				t.Fatalf("a skipped stage reports %d ms", view.DurationMS)
			}
		case string(viewMail), string(viewSemanticSearch):
			if view.State != "done" {
				t.Fatalf("%s is %q, want done", view.Name, view.State)
			}
			if view.Error != "" {
				t.Fatalf("%s carries a reason although it built: %q", view.Name, view.Error)
			}
		}
	}
}
