package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// The boot contract is one small set of names shared by the shell, the
// script and both catalogs. The identifiers are the warm view names the API
// reports; a missing one would leave a raw key on screen.
var bootContractViews = []string{
	"mail", "learning", "priority", "agenda", "triage_summary", "triage_page",
	"home", "conversations", "commitments", "companies", "brief",
	"semantic_search",
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	body, err := fs.ReadFile(Files, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(body)
}

func TestBootContractAssets(t *testing.T) {
	shell := readAsset(t, "index.html")
	if !strings.Contains(shell, `id="boot-strip" role="status" aria-live="polite"`) {
		t.Fatal("index.html has no announced #boot-strip")
	}
	script := readAsset(t, "app.js")
	if !strings.Contains(script, `request("/api/boot")`) {
		t.Fatal(`app.js does not read /api/boot through request("/api/boot")`)
	}
	if !strings.Contains(script, "bootProgressNode") {
		t.Fatal("app.js has no bootProgressNode")
	}
	if !strings.Contains(script, "bootWarningNode") {
		t.Fatal("app.js has no bootWarningNode")
	}
	for _, fragment := range []string{`t("boot.ready_warning"`, `t("boot.failed_views"`, `t("common.dismiss")`} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("app.js does not use %s", fragment)
		}
	}
	if !strings.Contains(script, "prefetchStarted") {
		t.Fatal("app.js no longer guards the one core prefetch")
	}

	keys := []string{"boot.preparing", "boot.progress", "boot.current", "boot.progress_aria", "boot.failed_views"}
	for _, view := range bootContractViews {
		keys = append(keys, "boot.view."+view)
	}
	for _, catalog := range []string{"i18n.en.js", "i18n.fr.js"} {
		entries := readCatalog(t, catalog)
		for _, key := range keys {
			entry, ok := entries[key]
			if !ok {
				t.Fatalf("%s has no %q", catalog, key)
			}
			if entry.isPlural || strings.TrimSpace(entry.text) == "" {
				t.Fatalf("%s: %q is not a non-empty string", catalog, key)
			}
		}
		progress := entries["boot.progress"]
		if !strings.Contains(progress.text, "{done}") || !strings.Contains(progress.text, "{total}") {
			t.Fatalf("%s: boot.progress %q needs {done} and {total}", catalog, progress.text)
		}
		current := entries["boot.current"]
		if !strings.Contains(current.text, "{view}") {
			t.Fatalf("%s: boot.current %q needs {view}", catalog, current.text)
		}
		failedViews := entries["boot.failed_views"]
		if !strings.Contains(failedViews.text, "{views}") {
			t.Fatalf("%s: boot.failed_views %q needs {views}", catalog, failedViews.text)
		}
		warning := entries["boot.ready_warning"]
		if !warning.isPlural {
			t.Fatalf("%s: boot.ready_warning is not a plural entry", catalog)
		}
		for _, form := range []string{"one", "other"} {
			text, ok := warning.plural[form]
			if !ok || !strings.Contains(text, "{failed}") {
				t.Fatalf("%s: boot.ready_warning.%s %q needs {failed}", catalog, form, text)
			}
		}
	}
}

// The boot strip and the warm derive from one ordered list. Parsing the
// production declaration keeps the UI catalog from drifting when a view is
// added, renamed or reordered.
func TestBootContractViewsMatchWarmOrder(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../cmd/mailsense/boot.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/mailsense/boot.go: %v", err)
	}
	consts := map[string]string{}
	var order []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range values.Names {
				if i >= len(values.Values) {
					continue
				}
				switch value := values.Values[i].(type) {
				case *ast.BasicLit:
					if value.Kind == token.STRING {
						text, err := strconv.Unquote(value.Value)
						if err != nil {
							t.Fatalf("%s: %v", name.Name, err)
						}
						consts[name.Name] = text
					}
				case *ast.CompositeLit:
					if name.Name != "warmViewOrder" {
						continue
					}
					for _, element := range value.Elts {
						switch entry := element.(type) {
						case *ast.Ident:
							view, ok := consts[entry.Name]
							if !ok {
								t.Fatalf("warm view %s has no string constant", entry.Name)
							}
							order = append(order, view)
						case *ast.BasicLit:
							view, err := strconv.Unquote(entry.Value)
							if err != nil {
								t.Fatalf("warm view: %v", err)
							}
							order = append(order, view)
						default:
							t.Fatalf("warmViewOrder entry %T is not a view constant", element)
						}
					}
				}
			}
		}
	}
	if len(order) == 0 {
		t.Fatal("no warmViewOrder entries parsed from cmd/mailsense/boot.go")
	}
	if len(order) != len(bootContractViews) {
		t.Fatalf("warmViewOrder has %d views %v, UI contract has %d %v",
			len(order), order, len(bootContractViews), bootContractViews)
	}
	for i, want := range bootContractViews {
		if order[i] != want {
			t.Fatalf("warm view %d is %q, UI contract says %q", i, order[i], want)
		}
	}
}
