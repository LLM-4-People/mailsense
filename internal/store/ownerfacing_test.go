package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestOwnerFacingBoundaryHasOneSpelling keeps the owner-facing content
// boundary in one place. It is "visible AND owner scope AND a store kind that
// carries owner content AND no draft/deleted/junk folder"; a file that names
// both halves in code without ownerFacingWhere is building that predicate by
// hand, which is how one read silently leaks a shared store's subject or
// sender. Deliberate partial uses (attachments, identity, All mail,
// ThreadStates) name only one half and are unaffected, and comments that
// explain the split are not code.
func TestOwnerFacingBoundaryHasOneSpelling(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	const ownerContent, roleContent = "ownerContentWhere", "threadContentRoleWhere"
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "each.go" {
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
		uses := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				uses[id.Name] = true
			}
			return true
		})
		if uses[ownerContent] && uses[roleContent] {
			t.Errorf("%s combines the two halves by hand; call ownerFacingWhere(vis)", name)
		}
	}

	// The one owner is where the guard expects it.
	src, err := os.ReadFile("each.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "func ownerFacingWhere(vis string) string") {
		t.Fatal("each.go no longer owns ownerFacingWhere")
	}
}
