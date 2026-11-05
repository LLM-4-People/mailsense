package ui

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is the mechanical guard for the split. The generated bundles must
// be current, every source module must be declared, no module or function may
// grow past its budget, and no new monolith may appear anywhere in the repo.
// A failure here is a design regression, not a formatting nit.

func TestGeneratedBundlesAreCurrent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func() ([]byte, error)
		onDisk string
	}{
		{"app.js", Bundle, "app.js"},
		{"app.css", CSSBundle, "app.css"},
	} {
		want, err := tc.build()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := fs.ReadFile(Files, tc.onDisk)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s is stale or hand-edited; run `go generate ./internal/ui`", tc.onDisk)
		}
		if !bytes.Contains(got[:min(len(got), 128)], []byte("Code generated")) {
			t.Fatalf("%s lacks the generated-file header", tc.onDisk)
		}
	}
}

func TestModuleManifestsAreComplete(t *testing.T) {
	for _, dir := range []struct{ name, ext string }{{"js", ".js"}, {"css", ".css"}} {
		names, err := moduleNames(dir.name, dir.ext)
		if err != nil {
			t.Fatalf("%s manifest: %v", dir.name, err)
		}
		if len(names) == 0 {
			t.Fatalf("%s manifest is empty", dir.name)
		}
	}
}

func TestModulesStaySmall(t *testing.T) {
	js, err := moduleNames("js", ".js")
	if err != nil {
		t.Fatal(err)
	}
	if len(js) > 32 {
		t.Fatalf("%d JS modules: merge related modules or raise the reviewed limit", len(js))
	}
	for _, name := range js {
		lines, funcs := moduleStats(t, "js", name)
		if lines > 1300 {
			t.Fatalf("js/%s: %d lines > 1300; split it by responsibility", name, lines)
		}
		if funcs > 100 {
			t.Fatalf("js/%s: %d top-level functions > 100; split it by responsibility", name, funcs)
		}
	}
	css, err := moduleNames("css", ".css")
	if err != nil {
		t.Fatal(err)
	}
	if len(css) > 16 {
		t.Fatalf("%d CSS modules: merge related modules", len(css))
	}
	for _, name := range css {
		lines, _ := moduleStats(t, "css", name)
		if lines > 600 {
			t.Fatalf("css/%s: %d lines > 600; split it by screen", name, lines)
		}
	}
}

// oversizedFunctions is the ratchet for the pre-split functions. Each entry is
// the current line count; the function may shrink but never grow. When it is
// split below the 120-line cap, delete the entry (the guard fails on a stale
// entry so the list cannot quietly become a permanent exception list).
var oversizedFunctions = map[string]int{
	"renderLabels":         799,
	"providerFormNode":     453,
	"renderConversations":  262,
	"createAskWorkspace":   234,
	"providersPanel":       198,
	"renderBrief":          181,
	"renderAllMail":        176,
	"generalSettingsPanel": 149,
	"sortableTable":        127,
}

func TestFunctionsStaySmall(t *testing.T) {
	body, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	spans := topLevelFunctions(t, string(body))
	seen := map[string]bool{}
	for _, span := range spans {
		limit := 120
		if budget, ok := oversizedFunctions[span.name]; ok {
			seen[span.name] = true
			if span.lines <= 120 {
				t.Fatalf("oversizedFunctions[%q] is stale (%d lines); delete the entry", span.name, span.lines)
			}
			limit = budget
		}
		if span.lines > limit {
			t.Fatalf("%s is %d lines > %d; split it or lower its reviewed budget", span.name, span.lines, limit)
		}
	}
	for name := range oversizedFunctions {
		if !seen[name] {
			t.Fatalf("oversizedFunctions[%q] is stale: no such top-level function", name)
		}
	}
}

// sourceLineBudget is the repo-wide monolith ratchet. A file may shrink but
// not grow; a new file above 1,500 lines must be split or added here after
// review. Generated bundles and embedded data are excluded by header/dir.
// The i18n catalogs are flat message data: their budget moves by the exact
// number of lines an intentional key addition needs, never by refactoring.
var sourceLineBudget = map[string]int{
	// Shared-tree round: these four owners grew while other writers were
	// active. They are reviewed exceptions; splits are scheduled after this
	// round. The QA owner gained the organization-facts helpers (+3 lines)
	// and the API wire gained the source-identity wiring (+20 lines); both
	// packages' tests are green.
	"cmd/mailsense/apiwire.go":               1336,
	"cmd/mailsense/warm.go":                  431,
	"internal/conversations/service_test.go": 1679,
	"internal/priority/priority.go":          1558,
	"internal/qa/ask.go":                     2400,
	"internal/config/schema.json":            2203,
	// The settings and diagnostics round added provider health, environment
	// lock, default-value, job-filter and cross-link wording: nine keys in
	// each catalog. The first-run round added the basic and advanced setup
	// wording in both catalogs; the budget moved by the exact added lines.
	// The topic-count round added the Ask topic sentence, its zero form and
	// the coverage line in both catalogs: three lines each. The update round
	// added the Updates group and its three setting descriptions in the
	// French catalog: four lines.
	"internal/ui/i18n.fr.js": 2617,
	"internal/ui/i18n.en.js": 2226,
	"internal/qa/qa_test.go": 4930,
	// The three Node behavior harnesses each run the shared thread-state
	// block before their view block: +2 lines per harness.
	"internal/ui/ui_test.go":             6957,
	"internal/priority/priority_test.go": 1456,
	// The Mail spec carries Important's cached-paging and completion-event
	// regressions; they belong with the screen's mocks.
	"e2e/specs/mail.spec.ts": 1759,
}

func TestNoNewMonolith(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const limit = 1500
	var oversize []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "out", "testdata", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".go", ".js", ".css", ".ts", ".sql":
		default:
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.HasPrefix(data, []byte("/* Code generated")) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		lines := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		if lines <= limit {
			return nil
		}
		budget, ok := sourceLineBudget[rel]
		if !ok {
			oversize = append(oversize, rel+": "+strconv.Itoa(lines)+" lines, no reviewed budget")
			return nil
		}
		if lines > budget {
			oversize = append(oversize, rel+": "+strconv.Itoa(lines)+" lines > budget "+strconv.Itoa(budget))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(oversize)
	for _, line := range oversize {
		t.Errorf("%s; split the file or lower the reviewed budget", line)
	}
}

func TestNoDuplicateBodies(t *testing.T) {
	body, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	spans := topLevelFunctions(t, string(body))
	seen := map[[32]byte]functionSpan{}
	for _, span := range spans {
		if span.lines < 3 {
			continue
		}
		raw := lines[span.line-1 : span.line-1+span.lines]
		var normalized strings.Builder
		for _, line := range raw {
			line = strings.TrimSpace(line)
			if i := strings.Index(line, "//"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line == "" {
				continue
			}
			normalized.WriteString(line)
			normalized.WriteByte('\n')
		}
		sum := sha256.Sum256([]byte(normalized.String()))
		if prev, ok := seen[sum]; ok {
			t.Fatalf("%s (line %d) and %s (line %d) have the same body; merge them",
				prev.name, prev.line, span.name, span.line)
		}
		seen[sum] = span
	}
}

func moduleStats(t *testing.T, dir, name string) (lines, funcs int) {
	t.Helper()
	body, err := fs.ReadFile(Files, path.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	lines = bytes.Count(body, []byte("\n"))
	if len(body) > 0 && body[len(body)-1] != '\n' {
		lines++
	}
	re := regexp.MustCompile(`(?m)^(?:  )?(?:async )?function [A-Za-z0-9_]+\(`)
	funcs = len(re.FindAll(body, -1))
	return lines, funcs
}

type functionSpan struct {
	name  string
	line  int
	lines int
}

func topLevelFunctions(t *testing.T, src string) []functionSpan {
	t.Helper()
	lines := strings.Split(src, "\n")
	re := regexp.MustCompile(`^  (?:async )?function ([A-Za-z0-9_]+)\(`)
	var out []functionSpan
	for i, line := range lines {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == "  }" {
				end = j
				break
			}
		}
		if end < 0 {
			t.Fatalf("%s at line %d has no closing brace at two-space indentation", m[1], i+1)
		}
		out = append(out, functionSpan{name: m[1], line: i + 1, lines: end - i + 1})
	}
	return out
}
