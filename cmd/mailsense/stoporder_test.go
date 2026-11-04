package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// a.stop is the one closure that joins every store reader before the store
// closes: the triage trigger, the priority ranking, the event-driven workers
// and the API's read models. The order is a contract, not a detail — the
// workers stop first so no new rebuild starts behind the read-model join.
func TestStopClosureJoinsTheWorkersThenTheReadModels(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var assignment *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assignment != nil {
			return true
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "stop" {
			return true
		}
		if lit, ok := assign.Rhs[0].(*ast.FuncLit); ok {
			assignment = lit
		}
		return true
	})
	if assignment == nil {
		t.Fatal("main.go has no a.stop closure")
	}

	pos := map[string]token.Pos{}
	ast.Inspect(assignment.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if _, seen := pos[fun.Name]; !seen {
				pos[fun.Name] = call.Pos()
			}
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok {
				if _, seen := pos[id.Name+"."+fun.Sel.Name]; !seen {
					pos[id.Name+"."+fun.Sel.Name] = call.Pos()
				}
			}
		}
		return true
	})

	for _, name := range []string{"stopTriage", "pr.Close", "cleanup"} {
		if !pos[name].IsValid() {
			t.Fatalf("a.stop never calls %s", name)
		}
	}
	join := pos["joinReadModels"]
	if !join.IsValid() {
		t.Fatal("a.stop never joins the read models")
	}
	for _, name := range []string{"stopTriage", "pr.Close", "cleanup"} {
		if pos[name] >= join {
			t.Fatalf("a.stop joins the read models at %s before %s at %s", fset.Position(join),
				name, fset.Position(pos[name]))
		}
	}
	if closePos := pos["registry.Close"]; !closePos.IsValid() || closePos < join {
		t.Fatalf("a.stop closes the connectors at %s before the read-model join at %s",
			fset.Position(closePos), fset.Position(join))
	}
	// The join uses the existing shutdown bound, never a second constant.
	if !pos["context.WithTimeout"].IsValid() || pos["context.WithTimeout"] > join {
		t.Fatal("a.stop does not bound the read-model join with a context timeout")
	}
}
