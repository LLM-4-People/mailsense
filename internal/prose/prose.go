// Package prose owns the public-copy style rules.
//
// The rules exist so a reader meets the same plain voice everywhere: no em
// dash, no spaced en dash used as punctuation, no double hyphen used as
// punctuation, no stock phrase that makes a page read like generated
// marketing, no rhetorical question used as a transition, no closing
// paragraph that only repeats the opening, and no Title Case heading where
// sentence case is required. The documentation test and the UI catalog test
// both call this package, so the rule has one owner.
//
// The check is deliberately narrow. It runs on public prose, never on code,
// legal text, or engineering logs. Code fences, inline code, URLs, command
// flags, identifiers, numeric ranges and Markdown table separators stay
// allowed.
package prose

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule names returned by Check and reported by Finding.
const (
	RuleEmDash             = "em dash"
	RuleEnDash             = "spaced en dash"
	RuleDoubleDash         = "double hyphen"
	RuleStockPhrase        = "stock phrase"
	RuleTitleCase          = "title case heading"
	RuleRhetoricalQuestion = "rhetorical question"
	RuleClosingRestatement = "closing restates opening"
)

// lineRule is one rule that judges a single line or catalog value.
type lineRule struct {
	name  string
	match func(text string, heading bool) bool
}

// lineRules is the canonical line rule set. Check and CheckDocument both read
// it, so a rule cannot be enforced in one place and forgotten in the other.
var lineRules = []lineRule{
	{RuleEmDash, hasEmDash},
	{RuleEnDash, hasSpacedEnDash},
	{RuleDoubleDash, hasPunctuationDoubleDash},
	{RuleStockPhrase, hasStockPhrase},
	{RuleTitleCase, hasTitleCase},
	{RuleRhetoricalQuestion, hasRhetoricalQuestion},
}

// stockPhrase is one generated-prose tell. The label names the marker in a
// test failure; the expression does the matching.
type stockPhrase struct {
	label string
	re    *regexp.Regexp
}

func phrase(label, expression string) stockPhrase {
	return stockPhrase{label: label, re: regexp.MustCompile(`(?i)` + expression)}
}

// stockPhrases are the tells this project refuses in public copy. Add a phrase
// only when it is a reliable signal, not a word that technical writing needs.
// The list is the one owner for both languages.
var stockPhrases = []stockPhrase{
	phrase("delve", `\bdelv(?:e|es|ed|ing)\b`),
	phrase("seamless", `\bseamless(?:ly)?\b`),
	phrase("unleash", `\bunleash(?:es|ed|ing)?\b`),
	phrase("elevate", `\belevat(?:e|es|ed|ing)\b`),
	phrase("empower", `\bempower(?:s|ed|ing)?\b`),
	// "leverage" is a verb tell. The inflections are always verbs. The base
	// form is a verb after a modal or "to", or before an object determiner;
	// the noun ("the leverage of a small team") stays allowed.
	phrase("leverage", `(?:\b(?:to|i|we|you|he|she|it|they|one|can|could|will|would|should|must|may|might|help|helps|helped|team|teams|company|companies|user|users|people)\s+(?:also\s+)?leverag(?:e|es|ed|ing)\b|\bleverag(?:es|ed|ing)\b|\bleverage\s+(?:the|a|an|your|our|their|its|this|that|these|those|data|technology|tools|ai|automation|mail|email|insights|value|resources|existing|new|models|model|providers|provider|customers|staff|workflows|workflow|systems|system|processes|process|infrastructure|knowledge|content|assets|capabilities)\b)`),
	phrase("game-changer", `\bgame[- ]changer\b`),
	phrase("cutting-edge", `\bcutting[- ]edge\b`),
	phrase("state-of-the-art", `\bstate[- ]of[- ]the[- ]art\b`),
	phrase("revolutionize", `\brevolutioniz(?:e|es|ed|ing)\b`),
	phrase("transformative", `\btransformative\b`),
	phrase("tapestry", `\btapestry\b`),
	phrase("in today's", `\bin today's\b`),
	phrase("look no further", `\blook no further\b`),
	phrase("deep dive", `\bdeep dive\b`),
	phrase("whether you're", `\bwhether you're\b`),
	phrase("it's not just", `\bit's not just\b`),
	phrase("more than just", `\bmore than just\b`),
	phrase("in conclusion", `\bin conclusion\b`),
	phrase("furthermore", `\bfurthermore\b`),
	phrase("moreover", `\bmoreover\b`),
	phrase("importantly", `\bimportantly\b`),
	phrase("it is worth noting", `\bit(?:'s| is) worth noting\b`),
	phrase("not only ... but also", `\bnot only\b[^.!?\n]{0,120}\bbut also\b`),
	phrase("it is not X, it is Y", `\bit(?:'s| is) not\b[^.!?\n]{0,80}[,;]\s*it(?:'s| is)\b`),
	phrase("plongez", `\bplongez\b`),
	phrase("véritable", `\bv[ée]ritable\b`),
	phrase("incontournable", `\bincontournable\b`),
	phrase("révolutionnaire", `\br[ée]volutionnaire\b`),
	phrase("clé en main", `\bcl[ée] en main\b`),
	phrase("dans un monde où", `dans un monde o[ùu]`),
	phrase("à l'ère du", `[àa] l'[èe]re du`),
	phrase("il est important de noter", `il est important de noter`),
	phrase("il convient de noter", `il convient de noter`),
	phrase("il vaut la peine de noter", `il vaut la peine de noter`),
	phrase("non seulement ... mais aussi", `non seulement[^.!?\n]{0,120}mais aussi`),
	phrase("ce n'est pas X, c'est Y", `ce n'est pas[^.!?\n]{0,80}[,;]\s*c'est\b`),
	phrase("en conclusion", `\ben conclusion\b`),
	phrase("par ailleurs", `\bpar ailleurs\b`),
}

// rhetoricalQuestions are transition questions, not genuine FAQ headings.
// CheckDocument applies them to body lines only, so a heading such as
// "Why is the inbox empty on Linux?" stays allowed.
var rhetoricalQuestions = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(?:but |so )?what(?:'s| is) the catch\?`),
	regexp.MustCompile(`(?i)^(?:so )?what does (?:this|that|it) mean(?: for you)?\?`),
	regexp.MustCompile(`(?i)^why (?:should|does) (?:you|this|that|it)\b.*\?`),
	regexp.MustCompile(`(?i)^(?:ever )?wondered\b.*\?`),
	regexp.MustCompile(`(?i)^ready to\b.*\?`),
	regexp.MustCompile(`(?i)^sound familiar\?`),
	regexp.MustCompile(`(?i)^imagine\b.*\?`),
	regexp.MustCompile(`(?i)^what if\b.*\?`),
	regexp.MustCompile(`(?i)^et si\b.*\?`),
	regexp.MustCompile(`(?i)^envie de\b.*\?`),
	regexp.MustCompile(`(?i)^pr[êe]t(?:e)? [àa]\b.*\?`),
	regexp.MustCompile(`(?i)^vous vous demandez\b.*\?`),
	regexp.MustCompile(`(?i)^pourquoi pas\?`),
}

// closingMarkerRE recognises a summary opener. It does not fail on its own: a
// closing paragraph is only a defect when it also repeats the opening.
var closingMarkerRE = regexp.MustCompile(`(?i)^(?:in short|in summary|to sum up|in closing|to conclude|the bottom line|as we have seen|as noted above|as described above|en bref|en r[ée]sum[ée]|pour r[ée]sumer|en somme)\b`)

// properNounWords are the names a heading may capitalise in sentence case.
// The set is deliberately small and mechanical: an acronym, a digit, a path
// separator or a product name is allowed by shape or by this list.
var properNounWords = map[string]bool{
	"Mailsense": true, "Outlook": true, "Windows": true, "Linux": true,
	"Microsoft": true, "WebView2": true, "GitHub": true, "GitLab": true,
	"Gmail": true, "Google": true, "Apple": true, "Inno": true, "Setup": true,
	"Evergreen": true, "Edge": true, "Firefox": true, "Chrome": true,
	"Gemma": true, "Llama": true, "Ollama": true, "OpenAI": true,
	"Claude": true, "DeepSeek": true, "Anthropic": true, "Vulkan": true,
	"CUDA": true, "Docker": true, "Podman": true, "WSL": true,
	"PowerShell": true, "Bash": true, "Go": true, "Python": true,
	"JavaScript": true, "TypeScript": true, "Node": true, "Playwright": true,
	"CodeQL": true, "Dependabot": true, "Pages": true, "Actions": true,
	"Jekyll": true, "Markdown": true, "English": true, "French": true,
	"Français": true, "France": true, "Canada": true, "Québec": true,
	"Montréal": true, "Europe": true, "Paris": true, "I": true,
	"Kubernetes": true, "PostgreSQL": true, "SQLite": true, "Redis": true,
	"Nginx": true, "Apache": true, "Ubuntu": true, "Debian": true,
	"Fedora": true, "Arch": true, "Nix": true, "Homebrew": true,
	"Android": true, "iOS": true, "macOS": true, "Safari": true,
	"Exchange": true, "SharePoint": true, "OneDrive": true, "Azure": true,
	"AWS": true, "GCP": true, "OAuth": true, "JWT": true, "Gemini": true,
	"Mistral": true, "Qwen": true, "Hugging": true, "Face": true,
	"OpenRouter": true, "Groq": true, "Together": true, "Rust": true,
	"Java": true, "Ruby": true, "PHP": true,
}

// urlRE masks a bare URL before the dash rules run. A URL may contain a double
// hyphen and is not prose.
var urlRE = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)

// htmlCommentRE masks a generator marker such as <!-- nav:start -->. The
// marker is machine text, not public prose.
var htmlCommentRE = regexp.MustCompile(`<!--.*?-->`)

// orderedItemRE matches an ordered list marker at the start of a line.
var orderedItemRE = regexp.MustCompile(`^\d+[.)]\s`)

// paragraph is one run of consecutive prose lines, used by the closing rule.
type paragraph struct {
	start int
	lines []string
}

// document is the scanned shape CheckDocument judges.
type document struct {
	lines      []docLine
	paragraphs []paragraph
}

type docLine struct {
	number  int
	text    string
	heading bool
}

// Check returns the rule names violated by one piece of prose, in a stable
// order with duplicates removed. It operates on a single line or a single
// catalog value; callers strip code before calling it. Document-only rules
// (a closing restatement) are not part of this answer.
func Check(text string) []string {
	return checkWithRules(text, false, lineRules)
}

// checkWithRules applies one explicit rule set. Tests use it to prove that
// removing a rule removes the matching finding.
func checkWithRules(text string, heading bool, rules []lineRule) []string {
	var found []string
	add := func(rule string) {
		for _, existing := range found {
			if existing == rule {
				return
			}
		}
		found = append(found, rule)
	}
	for _, rule := range rules {
		if rule.match(text, heading) {
			add(rule.name)
		}
	}
	return found
}

func hasEmDash(text string, _ bool) bool {
	return strings.ContainsRune(text, '\u2014')
}

// hasSpacedEnDash flags an en dash with whitespace on both sides, except a
// numeric range such as "1,3 – 1,8x". A token range such as "{from}–{to}" is
// not spaced and stays allowed.
func hasSpacedEnDash(text string, _ bool) bool {
	runes := []rune(text)
	for index, runeValue := range runes {
		if runeValue != '\u2013' {
			continue
		}
		if index == 0 || index+1 >= len(runes) || !unicode.IsSpace(runes[index-1]) || !unicode.IsSpace(runes[index+1]) {
			continue
		}
		left, leftOK := previousNonSpace(runes, index-1)
		right, rightOK := nextNonSpace(runes, index+1)
		if leftOK && rightOK && unicode.IsDigit(left) && unicode.IsDigit(right) {
			continue
		}
		return true
	}
	return false
}

func previousNonSpace(runes []rune, from int) (rune, bool) {
	for index := from; index >= 0; index-- {
		if !unicode.IsSpace(runes[index]) {
			return runes[index], true
		}
	}
	return 0, false
}

func nextNonSpace(runes []rune, from int) (rune, bool) {
	for index := from; index < len(runes); index++ {
		if !unicode.IsSpace(runes[index]) {
			return runes[index], true
		}
	}
	return 0, false
}

// hasPunctuationDoubleDash scans every double hyphen and allows the shapes
// that are not punctuation: a command flag, an identifier-like token, a
// numeric range, and a Markdown table separator or rule.
func hasPunctuationDoubleDash(text string, _ bool) bool {
	for index := 0; index+1 < len(text); index++ {
		if text[index] != '-' || text[index+1] != '-' {
			continue
		}
		start := index
		for start > 0 && text[start-1] == '-' {
			start--
		}
		end := index + 1
		for end+1 < len(text) && text[end+1] == '-' {
			end++
		}
		// Three or more hyphens are a table separator or a horizontal rule.
		if end-start+1 >= 3 {
			index = end
			continue
		}
		if doubleDashAllowed(text, start, end) {
			index = end
			continue
		}
		return true
	}
	return false
}

// doubleDashAllowed reports whether the run text[start:end+1] is an allowed
// use of "--". The run is exactly two hyphens.
func doubleDashAllowed(text string, start, end int) bool {
	left, leftOK := byteBefore(text, start)
	right, rightOK := byteAfter(text, end)
	// Numeric ranges, with or without spaces: 1--2, 1 -- 2.
	leftNonSpace, leftNonSpaceOK := byteBeforeNonSpace(text, start)
	rightNonSpace, rightNonSpaceOK := byteAfterNonSpace(text, end)
	if leftNonSpaceOK && rightNonSpaceOK && isDigit(leftNonSpace) && isDigit(rightNonSpace) {
		return true
	}
	// A command flag starts with "--" at the start of a token: --flag.
	if rightOK && isASCIILetter(right) && (!leftOK || isSpace(left)) {
		return true
	}
	// Markdown table separators and rules.
	if (leftOK && (left == '|' || left == ':')) || (rightOK && (right == '|' || right == ':')) {
		return true
	}
	// An identifier-shaped token carries a digit, a path separator, a dot, an
	// underscore or a colon on one side of the run.
	if leftOK && rightOK && isIdentifierByte(left) && isIdentifierByte(right) {
		if tokenIsIdentifier(text, start, end) {
			return true
		}
	}
	return false
}

func tokenIsIdentifier(text string, start, end int) bool {
	from := start
	for from > 0 && isIdentifierByte(text[from-1]) {
		from--
	}
	to := end
	for to+1 < len(text) && isIdentifierByte(text[to+1]) {
		to++
	}
	token := strings.Trim(text[from:to+1], ".:")
	// A bare identifier line such as "foo--bar" is code, not a sentence.
	if strings.TrimSpace(text) == token {
		return true
	}
	// A word--another is punctuation. An identifier carries a structural
	// character that plain prose does not.
	return strings.ContainsAny(token, "0123456789/._:@$")
}

func isIdentifierByte(value byte) bool {
	return isASCIILetter(value) || isDigit(value) || value == '-' || value == '_' || value == '.' || value == '/' || value == ':' || value == '@' || value == '$'
}

func byteBefore(text string, index int) (byte, bool) {
	if index <= 0 {
		return 0, false
	}
	return text[index-1], true
}

func byteAfter(text string, index int) (byte, bool) {
	if index+1 >= len(text) {
		return 0, false
	}
	return text[index+1], true
}

func byteBeforeNonSpace(text string, index int) (byte, bool) {
	for cursor := index - 1; cursor >= 0; cursor-- {
		if !isSpace(text[cursor]) {
			return text[cursor], true
		}
	}
	return 0, false
}

func byteAfterNonSpace(text string, index int) (byte, bool) {
	for cursor := index + 1; cursor < len(text); cursor++ {
		if !isSpace(text[cursor]) {
			return text[cursor], true
		}
	}
	return 0, false
}

func isSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isASCIILetter(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func hasStockPhrase(text string, _ bool) bool {
	return stockPhraseHit(text, stockPhrases)
}

// stockPhraseHit applies one explicit phrase list. Tests use it to prove that
// removing a phrase removes the matching finding.
func stockPhraseHit(text string, phrases []stockPhrase) bool {
	for _, phrase := range phrases {
		if phrase.re.MatchString(text) {
			return true
		}
	}
	return false
}

// hasTitleCase flags a Markdown heading that capitalises ordinary words. It
// is called only for heading lines; Check passes heading=false.
func hasTitleCase(text string, heading bool) bool {
	if !heading {
		return false
	}
	words := headingWords(text)
	if len(words) < 2 {
		return false
	}
	capitalised := 0
	for index, word := range words {
		if index == 0 {
			continue
		}
		if word == "" || properNounWords[word] || isAcronymOrCode(word) {
			continue
		}
		first, _ := utf8.DecodeRuneInString(word)
		if unicode.IsUpper(first) {
			capitalised++
		}
	}
	return capitalised >= 1
}

// headingWords strips the heading marker and inline markup, then returns the
// words with surrounding punctuation removed.
func headingWords(text string) []string {
	line := strings.TrimSpace(text)
	line = strings.TrimLeft(line, "#")
	line = strings.TrimSpace(line)
	line = strings.TrimRight(line, "#")
	line = stripInlineCode(line)
	line = urlRE.ReplaceAllString(line, " ")
	line = strings.NewReplacer("*", "", "_", "", "~", "", "`", "").Replace(line)
	var words []string
	for _, field := range strings.Fields(line) {
		word := strings.Trim(field, ".,:;!?()[]{}<>\"'«»“”‘’")
		word = strings.TrimSuffix(word, "'s")
		word = strings.TrimSuffix(word, "’s")
		if word != "" {
			words = append(words, word)
		}
	}
	return words
}

// isAcronymOrCode allows a shape no sentence-case rule should touch: an
// acronym, a version, a path, a host name, or a placeholder.
func isAcronymOrCode(word string) bool {
	if strings.ContainsAny(word, "0123456789/._@<>{}[]\\$#%&+=") {
		return true
	}
	letters, upper := 0, 0
	for _, runeValue := range word {
		if !unicode.IsLetter(runeValue) {
			continue
		}
		letters++
		if unicode.IsUpper(runeValue) {
			upper++
		}
	}
	return letters >= 2 && letters == upper
}

// hasRhetoricalQuestion flags the transition question shapes, not a genuine
// question heading.
func hasRhetoricalQuestion(text string, heading bool) bool {
	if heading {
		return false
	}
	line := strings.TrimSpace(text)
	line = strings.TrimLeft(line, "> ")
	line = strings.TrimPrefix(line, "- ")
	line = strings.TrimPrefix(line, "* ")
	line = strings.TrimPrefix(line, "+ ")
	line = orderedItemRE.ReplaceAllString(line, "")
	for _, expression := range rhetoricalQuestions {
		if expression.MatchString(line) {
			return true
		}
	}
	return false
}

// Finding is one violation in one document.
type Finding struct {
	Path string
	Line int
	Rule string
	Text string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.Path, f.Line, f.Rule, strings.TrimSpace(f.Text))
}

// documentRule is one rule that judges the whole document.
type documentRule struct {
	name  string
	check func(path string, doc *document) []Finding
}

// documentRules is the canonical document rule set.
var documentRules = []documentRule{
	{RuleClosingRestatement, closingRestatesOpening},
}

// CheckDocument checks Markdown or plain prose. Fenced code blocks and inline
// code spans are skipped: a flag, a SQL comment, or a shell example may use
// hyphens freely. The returned findings carry the original line number.
func CheckDocument(path string, body []byte) []Finding {
	return checkDocumentWith(path, body, lineRules, documentRules)
}

// checkDocumentWith applies one explicit rule set. Tests use it to prove that
// removing a document rule removes the matching finding.
func checkDocumentWith(path string, body []byte, lines []lineRule, documents []documentRule) []Finding {
	doc := scanDocument(body)
	var findings []Finding
	for _, line := range doc.lines {
		for _, rule := range checkWithRules(line.text, line.heading, lines) {
			findings = append(findings, Finding{
				Path: path,
				Line: line.number,
				Rule: rule,
				Text: line.text,
			})
		}
	}
	for _, rule := range documents {
		findings = append(findings, rule.check(path, doc)...)
	}
	return findings
}

// scanDocument reads Markdown lines once and records the prose the rules see,
// the headings, and the paragraphs the closing rule compares.
func scanDocument(body []byte) *document {
	doc := &document{}
	inFence := false
	fenceMarker := ""
	frontMatter := false
	var current *paragraph
	flush := func() {
		if current != nil {
			doc.paragraphs = append(doc.paragraphs, *current)
			current = nil
		}
	}
	rawLines := strings.Split(string(body), "\n")
	for index, raw := range rawLines {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if index == 0 && trimmed == "---" {
			frontMatter = true
			flush()
			continue
		}
		if frontMatter {
			if trimmed == "---" {
				frontMatter = false
			}
			continue
		}
		if !inFence {
			if marker, ok := fenceOpen(trimmed); ok {
				inFence = true
				fenceMarker = marker
				flush()
				continue
			}
		} else {
			if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		if isHeadingLine(trimmed) {
			flush()
			doc.lines = append(doc.lines, docLine{
				number:  index + 1,
				text:    cleanLine(line),
				heading: true,
			})
			continue
		}
		if isStructuralLine(trimmed) {
			flush()
			continue
		}
		proseLine := cleanLine(line)
		if proseLine == "" {
			flush()
			continue
		}
		doc.lines = append(doc.lines, docLine{
			number: index + 1,
			text:   proseLine,
		})
		if current == nil {
			current = &paragraph{start: index + 1}
		}
		current.lines = append(current.lines, proseLine)
	}
	flush()
	return doc
}

func isHeadingLine(trimmed string) bool {
	hashes := 0
	for hashes < len(trimmed) && trimmed[hashes] == '#' {
		hashes++
	}
	return hashes >= 1 && hashes <= 6 && hashes < len(trimmed) && trimmed[hashes] == ' '
}

// isStructuralLine marks lines that are not a closing prose paragraph: a
// table row, a list item, or a thematic break.
func isStructuralLine(trimmed string) bool {
	if strings.HasPrefix(trimmed, "|") {
		return true
	}
	for _, prefix := range []string{"- ", "* ", "+ ", "> "} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	if orderedItemRE.MatchString(trimmed) {
		return true
	}
	if len(trimmed) >= 3 && (strings.Trim(trimmed, "-") == "" || strings.Trim(trimmed, "=") == "") {
		return true
	}
	return false
}

// cleanLine removes the parts of a line the prose rules must not judge: inline
// code, generator comments and bare URLs.
func cleanLine(line string) string {
	line = htmlCommentRE.ReplaceAllString(stripInlineCode(line), " ")
	return strings.TrimSpace(urlRE.ReplaceAllString(line, " "))
}

// closingRestatesOpening flags a final paragraph whose content words are
// almost all already in the opening paragraph. A summary that adds a fact
// stays allowed; a paragraph that only rewrites the opening fails.
func closingRestatesOpening(path string, doc *document) []Finding {
	if len(doc.paragraphs) < 2 {
		return nil
	}
	opening := doc.paragraphs[0]
	closing := doc.paragraphs[len(doc.paragraphs)-1]
	openingTokens := contentTokens(strings.Join(opening.lines, " "))
	closingTokens := contentTokens(strings.Join(closing.lines, " "))
	if len(openingTokens) < 8 || len(closingTokens) < 8 {
		return nil
	}
	threshold := 0.8
	if closingMarkerRE.MatchString(strings.TrimSpace(strings.Join(closing.lines, " "))) {
		threshold = 0.6
	}
	seen := make(map[string]bool, len(openingTokens))
	for _, token := range openingTokens {
		seen[token] = true
	}
	repeated := 0
	for _, token := range closingTokens {
		if seen[token] {
			repeated++
		}
	}
	if float64(repeated)/float64(len(closingTokens)) < threshold {
		return nil
	}
	return []Finding{{
		Path: path,
		Line: closing.start,
		Rule: RuleClosingRestatement,
		Text: strings.Join(closing.lines, " "),
	}}
}

// stopWords are the function words the closing comparison ignores, in both
// languages.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"if": true, "then": true, "than": true, "that": true, "this": true,
	"these": true, "those": true, "it": true, "its": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true,
	"being": true, "to": true, "of": true, "in": true, "on": true,
	"for": true, "with": true, "at": true, "by": true, "from": true,
	"as": true, "so": true, "we": true, "you": true, "they": true,
	"he": true, "she": true, "i": true, "not": true, "no": true,
	"yes": true, "do": true, "does": true, "did": true, "can": true,
	"could": true, "will": true, "would": true, "should": true,
	"may": true, "might": true, "must": true, "have": true, "has": true,
	"had": true, "there": true, "their": true, "your": true, "our": true,
	"my": true, "me": true, "us": true, "them": true, "his": true,
	"her": true, "one": true, "two": true, "also": true, "only": true,
	"more": true, "most": true, "some": true, "any": true, "all": true,
	"each": true, "every": true, "other": true, "same": true, "new": true,
	"use": true, "uses": true, "using": true, "used": true,
	"le": true, "la": true, "les": true, "un": true, "une": true,
	"des": true, "du": true, "de": true, "et": true, "ou": true,
	"mais": true, "si": true, "que": true, "qui": true, "quoi": true,
	"dont": true, "où": true, "ce": true, "cet": true, "cette": true,
	"ces": true, "il": true, "elle": true, "ils": true, "elles": true,
	"nous": true, "vous": true, "je": true, "tu": true,
	"te": true, "se": true, "son": true, "sa": true, "ses": true,
	"leur": true, "leurs": true, "notre": true, "nos": true,
	"votre": true, "vos": true, "mon": true, "ma": true, "mes": true,
	"ton": true, "ta": true, "tes": true, "au": true, "aux": true,
	"en": true, "dans": true, "sur": true, "sous": true, "avec": true,
	"sans": true, "pour": true, "par": true, "comme": true, "plus": true,
	"moins": true, "très": true, "tout": true, "tous": true,
	"toute": true, "toutes": true, "est": true, "sont": true,
	"été": true, "être": true, "avoir": true, "ont": true, "fait": true,
	"faire": true, "peut": true, "peuvent": true, "ne": true,
	"pas": true, "y": true, "l": true, "d": true, "s": true,
	"c": true, "j": true, "m": true, "n": true, "qu": true,
}

// contentTokens returns the lower-case content words of a paragraph.
func contentTokens(text string) []string {
	var tokens []string
	for _, field := range strings.Fields(strings.ToLower(text)) {
		word := strings.Trim(field, ".,:;!?()[]{}<>\"'«»“”‘’*_`")
		if word == "" || stopWords[word] {
			continue
		}
		hasLetter := false
		for _, runeValue := range word {
			if unicode.IsLetter(runeValue) || unicode.IsDigit(runeValue) {
				hasLetter = true
				break
			}
		}
		if hasLetter {
			tokens = append(tokens, word)
		}
	}
	return tokens
}

// fenceOpen recognises a Markdown fenced code block opener and returns the
// marker to look for when it closes.
func fenceOpen(trimmed string) (string, bool) {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```", true
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~", true
	default:
		return "", false
	}
}

// stripInlineCode removes `code spans` and leaves the surrounding prose. A
// span that is not closed is left alone: a stray backtick is a copy defect of
// its own, and hiding the rest of the line would hide real prose.
func stripInlineCode(line string) string {
	var out strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			out.WriteString(line)
			return out.String()
		}
		end := strings.IndexByte(line[start+1:], '`')
		if end < 0 {
			out.WriteString(line)
			return out.String()
		}
		out.WriteString(line[:start])
		line = line[start+end+2:]
	}
}
