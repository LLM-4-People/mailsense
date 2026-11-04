package prose

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// piiEntry is one parsed line of scripts/pii-patterns.tsv.
type piiEntry struct {
	line     int
	kind     string
	category string
	pattern  string
	sample   string
	note     string
}

// readPIIEntries reads the one machine-readable pattern file. The parser is
// deliberately strict: a line that is not a comment must carry all five
// columns.
func readPIIEntries(t *testing.T) []piiEntry {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "pii-patterns.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []piiEntry
	for index, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("scripts/pii-patterns.tsv:%d: got %d columns, want 5", index+1, len(fields))
		}
		entries = append(entries, piiEntry{
			line:     index + 1,
			kind:     fields[0],
			category: fields[1],
			pattern:  fields[2],
			sample:   fields[3],
			note:     fields[4],
		})
	}
	return entries
}

// TestPIIPatternFileIsWellFormed pins the shape and the one-owner rule: every
// line has five columns, each kind is known, and no pattern is repeated.
func TestPIIPatternFileIsWellFormed(t *testing.T) {
	entries := readPIIEntries(t)
	seen := map[string]bool{}
	denied, allowed := 0, 0
	for _, entry := range entries {
		switch entry.kind {
		case "deny":
			denied++
		case "allow":
			allowed++
			if entry.pattern != entry.sample {
				t.Errorf("line %d: an allow term is literal, pattern %q != sample %q",
					entry.line, entry.pattern, entry.sample)
			}
		default:
			t.Errorf("line %d: kind = %q, want deny or allow", entry.line, entry.kind)
		}
		if entry.category == "" || entry.pattern == "" || entry.sample == "" || entry.note == "" {
			t.Errorf("line %d: empty category, pattern, sample or note", entry.line)
		}
		key := entry.kind + "|" + entry.pattern
		if seen[key] {
			t.Errorf("line %d: duplicate %s pattern %q", entry.line, entry.kind, entry.pattern)
		}
		seen[key] = true
	}
	if denied == 0 || allowed == 0 {
		t.Fatalf("pattern file has %d deny and %d allow entries, want both", denied, allowed)
	}
}

// TestPIIPatternsBite proves every deny pattern matches its sample and every
// allow term survives every deny pattern. The same table drives both halves.
func TestPIIPatternsBite(t *testing.T) {
	entries := readPIIEntries(t)
	var deny []piiEntry
	var allow []piiEntry
	for _, entry := range entries {
		switch entry.kind {
		case "deny":
			deny = append(deny, entry)
		case "allow":
			allow = append(allow, entry)
		}
	}
	type compiled struct {
		entry piiEntry
		re    *regexp.Regexp
	}
	var expressions []compiled
	for _, entry := range deny {
		expression, err := regexp.Compile("(?i)" + entry.pattern)
		if err != nil {
			t.Errorf("line %d: pattern %q: %v", entry.line, entry.pattern, err)
			continue
		}
		expressions = append(expressions, compiled{entry: entry, re: expression})
		if !expression.MatchString(entry.sample) {
			t.Errorf("line %d: %s pattern %q does not match %q", entry.line, entry.category, entry.pattern, entry.sample)
		}
	}
	for _, allowed := range allow {
		for _, expression := range expressions {
			if expression.re.MatchString(allowed.sample) {
				t.Errorf("line %d: deny pattern %q rejects the allowed %s %q",
					expression.entry.line, expression.entry.pattern, allowed.category, allowed.sample)
			}
		}
	}
}

// TestPIIScriptHasNoInlinePatterns proves the pattern list has one owner: the
// script reads the file and does not repeat a deny stem.
func TestPIIScriptHasNoInlinePatterns(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "scripts", "check-pii.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	if !strings.Contains(script, "scripts/pii-patterns.tsv") {
		t.Errorf("scripts/check-pii.sh does not read scripts/pii-patterns.tsv")
	}
	for _, entry := range readPIIEntries(t) {
		if entry.kind != "deny" {
			continue
		}
		if strings.Contains(script, entry.sample) {
			t.Errorf("scripts/check-pii.sh repeats the deny sample %q inline", entry.sample)
		}
	}
}

// TestPIIScriptSelfTest runs the bounded self-test. It proves every deny
// pattern matches its sample, no allow term is rejected, and removing one deny
// pattern removes its sample match.
func TestPIIScriptSelfTest(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot(t), "scripts", "check-pii.sh"), "--self-test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("check-pii.sh --self-test: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "PII self-test passed") {
		t.Fatalf("unexpected self-test output:\n%s", output)
	}
}

// TestPIIGateIsWiredOnce proves each gate calls the scan exactly once and the
// Makefile exposes it.
func TestPIIGateIsWiredOnce(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"test-fast.sh", "test.sh"} {
		body, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(body), "check-pii.sh"); got != 1 {
			t.Errorf("scripts/%s calls check-pii.sh %d times, want once", name, got)
		}
	}
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "scripts/check-pii.sh") {
		t.Errorf("Makefile has no pii target for scripts/check-pii.sh")
	}
}
