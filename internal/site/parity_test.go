package site

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestPagePairsHaveTheSameStructure is the bilingual parity guard. It walks
// every Markdown pair in the page table and fails on the first structural
// difference: heading levels, fenced code blocks, outbound link hosts,
// in-page anchor targets, image kinds and table shapes. Prose is deliberately
// not compared, because a literal translation is not wanted.
//
// Mutation check: the first provider protocol table in docs/fr/providers.md was
// temporarily given a third column on its openai_chat row and
// TestPagePairsHaveTheSameStructure failed with "docs/providers.md vs
// docs/fr/providers.md: table 0 row 2 has 2 columns in en and 3 in fr". The
// edit was reverted.
func TestPagePairsHaveTheSameStructure(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, page := range value.Pages() {
		if !strings.HasSuffix(page.EN, ".md") || !strings.HasSuffix(page.FR, ".md") {
			continue
		}
		checked++
		t.Run(page.Key, func(t *testing.T) {
			en := readDoc(t, root(t), page.EN)
			fr := readDoc(t, root(t), page.FR)
			if difference := firstParityDifference(en, fr); difference != "" {
				t.Fatalf("%s vs %s: %s", page.EN, page.FR, difference)
			}
		})
	}
	if want := len(value.Nav) + 2; checked < want {
		t.Fatalf("checked %d Markdown pairs, want at least %d (the README, both landings and every nav page)", checked, want)
	}
}

// TestParityDifferenceBites proves each rule rejects a one-sided edit instead
// of only accepting the current tree.
func TestParityDifferenceBites(t *testing.T) {
	base := "# Title\n\nA paragraph.\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n\n```bash\nrun\n```\n\n[a](https://example.test) [b](https://example.test) [top](#title) ![img](a.png)\n"
	for _, tc := range []struct {
		name string
		edit string
		want string
	}{
		{"heading level", strings.Replace(base, "# Title", "## Title", 1), "heading 0"},
		{"code content", strings.Replace(base, "\nrun\n", "\nrun --fast\n", 1), "code block 0"},
		{"missing code", strings.Replace(base, "```bash\nrun\n```\n\n", "", 1), "code blocks = 1 in en and 0 in fr"},
		{"new host", base + "\n[c](https://other.test)\n", "outbound hosts"},
		{"missing anchor", strings.Replace(base, " [top](#title)", "", 1), "anchor targets"},
		{"remote image", strings.Replace(base, "![img](a.png)", "![img](https://example.test/a.png)", 1), "image 0"},
		{"table column", strings.Replace(base, "| 1 | 2 |", "| 1 | 2 | 3 |", 1), "table 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			difference := firstParityDifference(base, tc.edit)
			if difference == "" || !strings.Contains(difference, tc.want) {
				t.Fatalf("firstParityDifference = %q, want a difference naming %q", difference, tc.want)
			}
		})
	}
	if difference := firstParityDifference(base, base); difference != "" {
		t.Fatalf("identical documents differ: %s", difference)
	}
}

// docShape is the language-independent structure of one Markdown page.
type docShape struct {
	headings []int
	code     []string
	hosts    []string
	anchors  []string
	images   []string
	tables   [][]int
}

var (
	markdownHeadingRE = regexp.MustCompile(`^(#{1,6})\s+`)
	htmlHeadingRE     = regexp.MustCompile(`(?i)<h([1-6])[\s>]`)
	fenceRE           = regexp.MustCompile("^\\s*(`{3,}|~{3,})")
	tableRowRE        = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	linkRE            = regexp.MustCompile(`(!?)\[([^\]]*)\]\(([^)\s]+)`)
	hrefRE            = regexp.MustCompile(`(?i)\bhref\s*=\s*"([^"]*)"`)
	imageTagRE        = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc\s*=\s*"([^"]*)"`)
	frontMatterRE     = regexp.MustCompile(`(?s)\A---\n.*?\n---\n`)
)

// pageShape parses the rules the parity guard compares. Fenced blocks are
// removed from the prose before headings, links, images and tables are read, so
// a comment or a pipe inside a command is not mistaken for structure.
func pageShape(text string) docShape {
	text = frontMatterRE.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	var shape docShape
	var prose strings.Builder
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		match := fenceRE.FindStringSubmatch(line)
		if match == nil {
			prose.WriteString(line)
			prose.WriteString("\n")
			continue
		}
		fence := match[1]
		var body []string
		for index++; index < len(lines); index++ {
			closing := fenceRE.FindStringSubmatch(lines[index])
			if closing != nil && closing[1][0] == fence[0] && len(closing[1]) >= len(fence) {
				break
			}
			body = append(body, lines[index])
		}
		shape.code = append(shape.code, strings.Join(body, "\n"))
		prose.WriteString("\n")
	}
	body := prose.String()

	for _, line := range strings.Split(body, "\n") {
		if match := markdownHeadingRE.FindStringSubmatch(line); match != nil {
			shape.headings = append(shape.headings, len(match[1]))
		}
		for _, match := range htmlHeadingRE.FindAllStringSubmatch(line, -1) {
			level, _ := strconv.Atoi(match[1])
			shape.headings = append(shape.headings, level)
		}
	}

	hosts := map[string]bool{}
	anchors := map[string]bool{}
	addTarget := func(target string) {
		if strings.HasPrefix(target, "#") {
			anchors[strings.TrimPrefix(target, "#")] = true
			return
		}
		if parsed, err := url.Parse(target); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			hosts[parsed.Host] = true
		}
	}
	for _, match := range linkRE.FindAllStringSubmatch(body, -1) {
		if match[1] == "!" {
			continue
		}
		addTarget(match[3])
	}
	for _, match := range hrefRE.FindAllStringSubmatch(body, -1) {
		addTarget(match[1])
	}
	shape.hosts = sortedKeys(hosts)
	shape.anchors = sortedKeys(anchors)

	for _, match := range linkRE.FindAllStringSubmatch(body, -1) {
		if match[1] == "!" {
			shape.images = append(shape.images, imageKind(match[3]))
		}
	}
	for _, match := range imageTagRE.FindAllStringSubmatch(body, -1) {
		shape.images = append(shape.images, imageKind(match[1]))
	}

	var table []string
	flush := func() {
		if len(table) < 2 {
			table = nil
			return
		}
		var rows []int
		for index, line := range table {
			// The second line is the delimiter row, not a data row.
			if index == 1 {
				continue
			}
			rows = append(rows, strings.Count(line, "|")-1)
		}
		shape.tables = append(shape.tables, rows)
		table = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if tableRowRE.MatchString(line) {
			table = append(table, line)
			continue
		}
		flush()
	}
	flush()
	return shape
}

func imageKind(target string) string {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "//") {
		return "remote"
	}
	return "local"
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// firstParityDifference returns the first structural difference between the
// English and French pages, or an empty string when they match.
func firstParityDifference(en, fr string) string {
	a, b := pageShape(en), pageShape(fr)

	if len(a.headings) != len(b.headings) {
		return fmt.Sprintf("heading count = %d in en and %d in fr (%v vs %v)", len(a.headings), len(b.headings), a.headings, b.headings)
	}
	for index := range a.headings {
		if a.headings[index] != b.headings[index] {
			return fmt.Sprintf("heading %d has level %d in en and %d in fr", index, a.headings[index], b.headings[index])
		}
	}

	if len(a.code) != len(b.code) {
		return fmt.Sprintf("code blocks = %d in en and %d in fr", len(a.code), len(b.code))
	}
	for index := range a.code {
		if a.code[index] != b.code[index] {
			return fmt.Sprintf("code block %d differs: en %q, fr %q", index, a.code[index], b.code[index])
		}
	}

	if difference := setDifference("outbound hosts", a.hosts, b.hosts); difference != "" {
		return difference
	}
	if difference := setDifference("in-page anchor targets", a.anchors, b.anchors); difference != "" {
		return difference
	}

	if len(a.images) != len(b.images) {
		return fmt.Sprintf("image count = %d in en and %d in fr (%v vs %v)", len(a.images), len(b.images), a.images, b.images)
	}
	for index := range a.images {
		if a.images[index] != b.images[index] {
			return fmt.Sprintf("image %d is %s in en and %s in fr", index, a.images[index], b.images[index])
		}
	}

	if len(a.tables) != len(b.tables) {
		return fmt.Sprintf("table count = %d in en and %d in fr", len(a.tables), len(b.tables))
	}
	for index := range a.tables {
		if len(a.tables[index]) != len(b.tables[index]) {
			return fmt.Sprintf("table %d has %d rows in en and %d in fr", index, len(a.tables[index]), len(b.tables[index]))
		}
		for row := range a.tables[index] {
			if a.tables[index][row] != b.tables[index][row] {
				return fmt.Sprintf("table %d row %d has %d columns in en and %d in fr",
					index, row, a.tables[index][row], b.tables[index][row])
			}
		}
	}
	return ""
}

func setDifference(name string, en, fr []string) string {
	inFR := map[string]bool{}
	for _, value := range fr {
		inFR[value] = true
	}
	inEN := map[string]bool{}
	for _, value := range en {
		inEN[value] = true
	}
	var onlyEN, onlyFR []string
	for _, value := range en {
		if !inFR[value] {
			onlyEN = append(onlyEN, value)
		}
	}
	for _, value := range fr {
		if !inEN[value] {
			onlyFR = append(onlyFR, value)
		}
	}
	if len(onlyEN) == 0 && len(onlyFR) == 0 {
		return ""
	}
	return fmt.Sprintf("%s differ: only in en %v, only in fr %v", name, onlyEN, onlyFR)
}
