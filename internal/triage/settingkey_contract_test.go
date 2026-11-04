package triage

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// settingKeyOwner is one setting key that has a Go owner constant: the
// constant is the only place its value may be spelled, every other mention
// reads the constant, so a re-spelled key cannot drift from its owner.
type settingKeyOwner struct {
	key  string
	name string
	// inScope says the constant is declared inside settingKeyTrees: the scan
	// must then find exactly one definition, which also proves it really read
	// the owner's file instead of passing over an empty walk. The provider
	// key is declared in internal/engine/catalog.go, outside these trees.
	inScope bool
}

// settingKeyOwners are the owned keys read by the triage, judge and layer-1
// packages. Every key these trees stamp a cache with has an owner here.
var settingKeyOwners = []settingKeyOwner{
	{key: DeciderSetting, name: "DeciderSetting", inScope: true},
	{key: engine.ProvidersFileSetting, name: "ProvidersFileSetting"},
	{key: taxonomy.ProfileFileSetting, name: "ProfileFileSetting", inScope: true},
	{key: RulesFileSetting, name: "RulesFileSetting", inScope: true},
}

// settingKeyTrees are the package trees the contract scans, relative to this
// package's directory and mutually exclusive: every Go file under
// internal/triage (taxonomy included), internal/judge and internal/layer1,
// tests and build-tagged files included, because a stale key in a test or a
// benchmark drifts just the same.
var settingKeyTrees = []string{".", filepath.Join("..", "judge"), filepath.Join("..", "layer1")}

// A setting key has one owner: the constant. Re-spelling the value at a call
// site is how two spellings silently appear and one of them rots, so the
// literal is only allowed where the constant declares it. This walks the
// syntax, not the text, so a comment or a field name cannot trip it.
func TestSettingKeyLiteralsLiveOnlyInTheirOwner(t *testing.T) {
	owners := make(map[string]settingKeyOwner, len(settingKeyOwners))
	for _, o := range settingKeyOwners {
		if o.key == "" || o.name == "" {
			t.Fatalf("incomplete owner row %+v", o)
		}
		if _, dup := owners[o.key]; dup {
			t.Fatalf("key %q has two owner rows", o.key)
		}
		owners[o.key] = o
	}
	definitions := make(map[string]int, len(settingKeyOwners))
	var problems []string
	for _, tree := range settingKeyTrees {
		files := goFilesIn(t, tree)
		if len(files) == 0 {
			t.Fatalf("%s: no Go file to scan", tree)
		}
		fset := token.NewFileSet()
		for _, path := range files {
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			allowed := ownerDefinitions(file, owners)
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				owner, ok := owners[value]
				if !ok {
					return true
				}
				if allowed[lit.Pos()] {
					definitions[value]++
					return true
				}
				problems = append(problems, fmt.Sprintf("%s: %q must be %s",
					fset.Position(lit.Pos()), value, owner.name))
				return true
			})
		}
	}
	slices.Sort(problems)
	for _, p := range problems {
		t.Error(p)
	}
	for _, o := range settingKeyOwners {
		if o.inScope && definitions[o.key] != 1 {
			t.Errorf("found %d definitions of %s (%q) in %v, want 1",
				definitions[o.key], o.name, o.key, settingKeyTrees)
		}
	}
}

// ownerDefinitions returns the positions of the key literals that are an
// owner constant's own declaration: a const spec whose name is the constant
// and whose value is the key.
func ownerDefinitions(file *ast.File, owners map[string]settingKeyOwner) map[token.Pos]bool {
	out := map[token.Pos]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				if owner, ok := owners[value]; ok && owner.name == vs.Names[i].Name {
					out[lit.Pos()] = true
				}
			}
		}
		return true
	})
	return out
}

// goFilesIn lists a package's Go files in lexical order, skipping testdata and
// dot directories so the walk is bounded and deterministic.
func goFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	slices.Sort(files)
	return files
}
