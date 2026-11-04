package store

import (
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInReadTxRunsTheBodyOnceAndReturnsItsValue pins the wrapper's contract:
// the body runs in one transaction, its value is returned unchanged, and its
// error is returned without a retry.
func TestInReadTxRunsTheBodyOnceAndReturnsItsValue(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	calls := 0
	got, err := inReadTx(ctx, s.db, "test", func(tx *sql.Tx) (int, error) {
		calls++
		var one int
		if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			return 0, err
		}
		return one, nil
	})
	if err != nil || got != 1 || calls != 1 {
		t.Fatalf("value %d calls %d err %v", got, calls, err)
	}

	boom := errors.New("body failed")
	got, err = inReadTx(ctx, s.db, "test", func(*sql.Tx) (int, error) { return 0, boom })
	if !errors.Is(err, boom) || got != 0 {
		t.Fatalf("failure value %d err %v", got, err)
	}
}

// TestOnlyStoreOwnsTheReadOnlyTransaction keeps the one place a read-only
// transaction is opened. A second hand-written BeginTx(ReadOnly) is how a
// multi-statement read loses its snapshot: the next one forgets the option, or
// forgets the rollback, and only some reads see one revision.
func TestOnlyStoreOwnsTheReadOnlyTransaction(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "store.go" {
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
			if !ok || sel.Sel.Name != "TxOptions" {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "ReadOnly" {
					t.Errorf("%s opens its own read-only transaction; call inReadTx", filepath.Base(name))
				}
			}
			return true
		})
	}
}
