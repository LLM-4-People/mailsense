package triage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestTriagedFilterIsOnePopulation pins the base filter every triage read
// starts from: the triaged folder roles of owner-scope mail, and nothing else.
// The page window, the summary coverage, the detail read, the decision scope
// and the export add their own RowIDs or Fields on top of this value; none may
// spell the base itself.
func TestTriagedFilterIsOnePopulation(t *testing.T) {
	roles := []mail.FolderRole{mail.RoleInbox, mail.RoleSent}
	filter := triagedFilter(roles)
	if !filter.OwnerOnly || len(filter.Roles) != len(roles) || filter.Roles[0] != roles[0] || filter.Roles[1] != roles[1] {
		t.Fatalf("base filter %+v", filter)
	}
	if filter.RowIDs != nil || filter.Fields != (store.MessageFields{}) || filter.OwnerContent || filter.WithBodyHTML {
		t.Fatalf("the base filter carries a caller's own predicate: %+v", filter)
	}

	// A caller narrows its own copy: the base must not become shared state
	// that one read's RowIDs leak into the next.
	filter.RowIDs = []int64{7}
	filter.Fields = store.MessageFieldsOf()
	if again := triagedFilter(roles); again.RowIDs != nil || again.Fields != (store.MessageFields{}) {
		t.Fatalf("a narrowed filter changed the next one: %+v", again)
	}
}

// TestOnlyTriagedFilterBuildsThePopulation keeps the base filter in one place.
// It parses every non-test file and fails if any file other than triager.go
// builds a store.MessageFilter with Roles or OwnerOnly, so a new read cannot
// open a second, subtly different population.
func TestOnlyTriagedFilterBuildsThePopulation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "triager.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "MessageFilter" {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "Roles" || key.Name == "OwnerOnly") {
					t.Errorf("%s builds the triaged population by hand; call triagedFilter(roles)", name)
				}
			}
			return true
		})
	}
}
