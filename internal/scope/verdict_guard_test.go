package scope

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestEveryReasonIsValidAndUnique: the declared list is the validator, and no
// value appears twice.
func TestEveryReasonIsValidAndUnique(t *testing.T) {
	seen := map[Reason]bool{}
	for _, r := range allReasons {
		if !r.Valid() {
			t.Errorf("reason %q is listed but not valid", r)
		}
		if seen[r] {
			t.Errorf("reason %q is listed twice", r)
		}
		seen[r] = true
	}
	for _, v := range allVerdicts {
		if !v.Valid() {
			t.Errorf("verdict %q is listed but not valid", v)
		}
	}
	if Reason("sent_folder").Valid() || Verdict("mine").Valid() {
		t.Fatal("an undeclared value is valid")
	}
}

// TestReasonConstantsMatchTheList: the const block and allReasons cannot drift.
// A rule added to Decide with a new constant but no list entry would make every
// store write fail at runtime; this makes it a test failure instead.
func TestReasonConstantsMatchTheList(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "verdict.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	reasons := 0
	verdicts := 0
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := value.Type.(*ast.Ident)
			if !ok {
				continue
			}
			switch ident.Name {
			case "Reason":
				reasons += len(value.Names)
			case "Verdict":
				verdicts += len(value.Names)
			}
		}
	}
	if reasons != len(allReasons) {
		t.Fatalf("%d Reason constants, %d entries in allReasons: list the new rule", reasons, len(allReasons))
	}
	if verdicts != len(allVerdicts) {
		t.Fatalf("%d Verdict constants, %d entries in allVerdicts", verdicts, len(allVerdicts))
	}
}
