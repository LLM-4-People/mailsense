package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strings"
	"testing"
)

// wireLearningStatusCalls returns every `<receiver>.Status(...)` call inside
// wireLearning in src. The guard is source-level because wireLearning has no
// seam for observing whether it builds the first learning status, and the
// regression is one blocking call in that function.
func wireLearningStatusCalls(src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		return nil, err
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if ok && f.Name.Name == "wireLearning" {
			fn = f
			break
		}
	}
	if fn == nil {
		return nil, fmt.Errorf("wireLearning declaration not found in main.go")
	}
	var calls []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Status" {
			return true
		}
		calls = append(calls, types.ExprString(sel))
		return true
	})
	return calls, nil
}

// The first learning status belongs to warmDerived so it can overlap the
// priority warm. Re-adding the old blocking m.Status call to wireLearning
// restores the 312–354 ms serial pre-warm (590–606 ms supplied) while every
// behavioral warm test still passes, because those tests call warmDerived
// directly and never enter wireLearning.
func TestWireLearningLeavesTheFirstStatusToTheWarm(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	calls, err := wireLearningStatusCalls(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) > 0 {
		t.Fatalf("wireLearning calls %s; the first learning status belongs to warmDerived so it can overlap the priority warm (Round 32 regression)",
			strings.Join(calls, ", "))
	}

	// Negative control: the same detector must reject the exact regression.
	// Parsing only, so the fixture needs no imports or type checking.
	const regressed = `package main

func wireLearning() (*learning.Machine, func(), error) {
	m, err := learning.New()
	if err != nil {
		return nil, nil, err
	}
	if _, err := m.Status(context.Background()); err != nil {
		return nil, nil, err
	}
	return m, nil, nil
}
`
	calls, err = wireLearningStatusCalls([]byte(regressed))
	if err != nil {
		t.Fatalf("the guard cannot parse its regression fixture: %v", err)
	}
	if len(calls) != 1 || calls[0] != "m.Status" {
		t.Fatalf("the guard did not detect a re-added m.Status call: %v", calls)
	}
	t.Logf("regression detector rejects a re-added %s call; production wireLearning has none", calls[0])
}
