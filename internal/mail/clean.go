package mail

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// CleanBody is the text of a message that is its own: quoted replies,
// forwarded/replied-to header blocks and signatures are removed, so an
// embedding describes what this message says rather than the thread it
// carries. The rules are structural and language-neutral (Outlook's
// localized "De :", "Envoyé :", "Von:" all look alike): no phrase lists.
// Limits are data (clean.json).
//
//   - the text is cut at the first reply boundary after some content of
//     its own: a separator with a caption ("-----Original Message-----"),
//     a bare rule (________, ======) followed within rule_lookahead_lines
//     by a header or quoted line, a header block (header_min_lines
//     consecutive "Label: value" lines, one carrying an address and one a
//     digit, not right under a heading line ending in ":"), the
//     "… wrote:" line of a bottom quote (only quoted lines after it), or
//     an attribution line — never a quoted line; at most intro_max_runes,
//     ending in ":", naming an address and holding a digit (the date):
//     "Le 14 oct. 2026 à 10:00, Ann <ann@x.org> a écrit :" in any
//     language — whose quote runs to the end: every following non-blank
//     line, up to the end or a recognised boundary (a separator, a header
//     block, a signature delimiter), is quoted or indented. Outlook's
//     plain-text Body shows a quoted HTML block (<blockquote>, as Gmail
//     and Apple Mail quote) indented by a tab, not with ">", the
//     attribution often indented with it. An own line of that shape before
//     an indented list followed by more text of its own is not one;
//   - a line quoted with ">" before that is dropped, and so is an
//     attribution line right before one or before a quote running to the
//     end — wherever it is, the first line included: it is never the
//     message's own text (its date is not the message's). Interleaved
//     replies keep their answers;
//   - the text is cut at an RFC 3676 signature delimiter ("-- ");
//   - a short closing paragraph of contact lines is dropped;
//   - runs of blank lines collapse to one.
//
// Every rule looks at a line's folded view (CompatView: NFKC, so the
// full-width colon "：", the small colon, NBSP and narrow NBSP before a
// colon and full-width digits read as their plain forms; format controls
// such as right-to-left marks dropped), labels may carry combining marks
// (Thai, Devanagari) and digits are any script's; the output keeps the
// original text.
//
// When nothing of the message's own is left (a bare forward), the stripped
// original — without its attribution lines — is returned: an embedding of
// the forwarded text beats none.
func CleanBody(body string) string {
	clean, _ := CleanParts(body)
	return clean
}

// CleanParts is CleanBody and Signature of one body in one pass.
func CleanParts(body string) (clean, signature string) {
	o := ownLines(body)
	if out := collapseBlank(o.lines); out != "" {
		return out, o.sig
	}
	return Strip(o.whole), o.sig
}

// Signature kinds (how CleanBody found a message's signature).
const (
	SignatureNone      = "none"
	SignatureDelimiter = "delimiter"     // the RFC 3676 delimiter line "-- "
	SignatureContacts  = "contact_block" // a short closing paragraph of contact lines
)

// Signature is how CleanBody found the signature of the message's own
// text (after the reply boundary and the quoted lines are gone).
func Signature(body string) string { return ownLines(body).sig }

// SignatureText is the signature block CleanBody cuts off, with quoted
// replies and forwarded headers already removed. It is empty when CleanBody
// found no delimiter or contact block; callers that need a bounded evidence
// window use EvidenceText instead.
func SignatureText(body string) string { return ownLines(body).sigText }

// EvidenceText is the bounded block a local reader may inspect for sender
// evidence: the signature when CleanBody found one, otherwise the last few
// non-blank lines of the message's own text. A business signature often has
// no phone, address or link, so CleanBody does not classify it as a contact
// block; its closing lines are still evidence. The fallback may include the
// last body line, so callers must apply their own field filters and never
// treat the block as a signature claim.
func EvidenceText(body string) string {
	o := ownLines(cutMinimalReplyHeader(body))
	if o.sigText != "" {
		return o.sigText
	}
	return collapseBlank(o.tail)
}

// cutMinimalReplyHeader truncates a raw body at a two-line From/De + Sent
// header with no address. CleanBody deliberately requires a full header
// block, so its global cleaning rules stay unchanged; the evidence reader
// needs the stricter cut to avoid reading a quoted owner's signature.
func cutMinimalReplyHeader(body string) string {
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		label, ok := replyLabel(lines[i])
		if !ok || (label != "de" && label != "from") {
			continue
		}
		if i > 0 && Strip(lines[i-1]) != "" {
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+3; j++ {
			next, ok := replyLabel(lines[j])
			if !ok {
				break
			}
			if next == "envoye" || next == "sent" || next == "date" {
				return strings.Join(lines[:i], "\n")
			}
		}
	}
	return body
}

// own is what CleanBody's steps make of a body: the lines of the
// message's own text, how its signature was cut, the signature text that
// was cut, and the whole body without its attribution lines (the fallback
// when nothing is its own).
type own struct {
	lines   []string
	tail    []string
	sig     string
	sigText string
	whole   string
}

// ownLines runs CleanBody's steps.
func ownLines(body string) own {
	r := cleanLimits()
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	views := make([]string, len(lines))
	for i, l := range lines {
		views[i] = CompatView(l)
	}
	heads := quoteHeaders(views, r)
	o := own{whole: body, sig: SignatureNone}
	if len(heads) > 0 {
		o.whole = strings.Join(dropLines(lines, heads), "\n")
	}
	cut := replyBoundary(views, r)
	lines, views = lines[:cut], views[:cut]
	for i, v := range views {
		heads[i] = heads[i] || quoted(v)
	}
	lines, views = dropLines(lines, heads), dropLines(views, heads)
	allLines := lines
	o.tail = trailingBlock(allLines)
	sigAt := len(lines)
	if cut = signatureStart(views); cut < len(lines) {
		o.sig = SignatureDelimiter
		sigAt = cut
	}
	lines, views = lines[:cut], views[:cut]
	if cut = contactBlockStart(views, r); cut < len(lines) && o.sig == SignatureNone && collapseBlank(lines[cut:]) != "" {
		o.sig = SignatureContacts
		sigAt = cut
	}
	if sigAt < len(allLines) {
		start := sigAt
		if strings.TrimRight(allLines[start], " \t") == "--" {
			start++
		}
		end := signatureEnd(allLines, start)
		o.sigText = collapseBlank(allLines[start:end])
	}
	o.lines = lines[:cut]
	return o
}

// trailingBlock is the last few non-blank lines of the message's own text.
// A blank line before a social or website block must not hide the phone and
// title above it. Six lines and a length bound keep the evidence window
// small; the extractor still rejects greetings, roles and prose.
func trailingBlock(lines []string) []string {
	out := make([]string, 0, 6)
	for index := len(lines) - 1; index >= 0 && len(out) < 6; index-- {
		line := lines[index]
		if Strip(line) == "" || Runes(line) > 120 {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

// signatureEnd bounds a signature block at the next signature delimiter or
// reply header. A plain-text Outlook quote often starts with "De : Name"
// without an address, so the generic reply-boundary detector cannot see it;
// the signature reader must still stop before the quoted owner signature.
func signatureEnd(lines []string, start int) int {
	for index := start; index < len(lines); index++ {
		if strings.TrimRight(lines[index], " \t") == "--" {
			return index
		}
		if replyHeaderLine(lines[index]) {
			return index
		}
	}
	return len(lines)
}

// replyHeaderLabels are the labels a mail client writes in a reply header,
// folded. They are used to bound a signature before a plain-text quote and
// to recognize a minimal two-line quote header; both readers share one list.
var replyHeaderLabels = map[string]bool{
	"de": true, "from": true, "envoye": true, "sent": true,
	"a": true, "to": true, "objet": true, "subject": true,
	"cc": true, "cci": true, "bcc": true, "date": true,
	"reply-to": true, "repondre a": true,
}

func replyLabel(line string) (string, bool) {
	line = strings.TrimSpace(line)
	index := strings.IndexAny(line, ":：")
	if index <= 0 || index > 24 {
		return "", false
	}
	return strings.TrimSpace(Fold(line[:index])), true
}

func replyHeaderLine(line string) bool {
	label, ok := replyLabel(line)
	return ok && replyHeaderLabels[label]
}

//go:embed clean.json
var cleanJSON []byte

// cleanRules are CleanBody's limits (clean.json).
type cleanRules struct {
	Doc             string `json:"_doc"`
	RulesVersion    int    `json:"rules_version"`
	HeaderMinLines  int    `json:"header_min_lines"`
	RuleLookahead   int    `json:"rule_lookahead_lines"`
	IntroMaxRunes   int    `json:"intro_max_runes"`
	ContactMaxLines int    `json:"contact_max_lines"`
	ContactMaxRunes int    `json:"contact_max_runes"`
	ContactMinHits  int    `json:"contact_min_hits"`
	RuleMinChars    int    `json:"rule_min_chars"`
	CaptionMinRule  int    `json:"caption_min_rule_chars"`
	CaptionMaxRunes int    `json:"caption_max_runes"`
	LabelMaxRunes   int    `json:"label_max_runes"`
	PhoneMinChars   int    `json:"phone_min_chars"`

	caption, rule, labelLine, phone *regexp.Regexp
}

// cleanLimits decodes the embedded limits once (TestCleanLimitsAreValid
// guards the file; a broken file is a build defect).
var cleanLimits = sync.OnceValue(func() cleanRules {
	var r cleanRules
	if err := jsonx.DecodeStrict(cleanJSON, &r); err != nil {
		panic("mail: clean.json: " + err.Error())
	}
	r.caption = regexp.MustCompile(fmt.Sprintf(`^\s*[-_=*]{%d,}[^-_=*]{1,%d}[-_=*]{%d,}\s*$`, r.CaptionMinRule, r.CaptionMaxRunes, r.CaptionMinRule))
	r.rule = regexp.MustCompile(fmt.Sprintf(`^\s*[-_=*]{%d,}\s*$`, r.RuleMinChars))
	r.labelLine = regexp.MustCompile(fmt.Sprintf(`^\s*\*?\p{L}[\p{L}\p{M} .'’-]{0,%d}\*?\s?:\s*\S`, r.LabelMaxRunes-1))
	r.phone = regexp.MustCompile(fmt.Sprintf(`\+?\(?\p{Nd}[\p{Nd} ().-]{%d,}\p{Nd}`, r.PhoneMinChars-2))
	return r
})

var (
	addressRE = regexp.MustCompile(`[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+`)
	digitRE   = regexp.MustCompile(`\p{Nd}`)
	linkRE    = regexp.MustCompile(`(?i)https?://|www\.`)
)

// Links counts the links of a text (http://, https://, www.): the one
// link rule (triage facts count them, CleanBody spots contact lines).
func Links(s string) int { return len(linkRE.FindAllStringIndex(s, -1)) }

// PhonePattern is the phone-shaped line rule CleanBody already uses to spot
// a contact block. Signature readers reuse it so there is one phone grammar.
func PhonePattern() *regexp.Regexp { return cleanLimits().phone }

func quoted(l string) bool { return strings.HasPrefix(strings.TrimLeft(l, " \t"), ">") }

// dropLines is lines without the indexes in drop.
func dropLines(lines []string, drop map[int]bool) []string {
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return out
}

// replyBoundary is the index of the first reply boundary preceded by text
// of the message's own (len(lines) when there is none; separators and
// quoted lines are not text of its own).
func replyBoundary(lines []string, r cleanRules) int {
	bottom := bottomQuote(lines)
	own := false
	for i, l := range lines {
		if own && (r.caption.MatchString(l) || ruleBoundary(lines, i, r) || headerBlockAt(lines, i, r) || introAt(lines, i, bottom, r) ||
			attributionAt(lines, i, r)) {
			return i
		}
		own = own || (Strip(l) != "" && !quoted(l) && !r.rule.MatchString(l) && !r.caption.MatchString(l))
	}
	return len(lines)
}

// ruleBoundary: a bare rule followed by a header or quoted line.
func ruleBoundary(lines []string, i int, r cleanRules) bool {
	if !r.rule.MatchString(lines[i]) {
		return false
	}
	seen := 0
	for _, l := range lines[i+1:] {
		if Strip(l) == "" {
			continue
		}
		if r.labelLine.MatchString(l) || quoted(l) {
			return true
		}
		if seen++; seen >= r.RuleLookahead {
			return false
		}
	}
	return false
}

func headerBlockAt(lines []string, i int, r cleanRules) bool {
	if i > 0 && strings.HasSuffix(Strip(lines[i-1]), ":") {
		return false // a list under a heading ("Status:"), not a reply header
	}
	n, address, digit := 0, false, false
	for ; i+n < len(lines) && r.labelLine.MatchString(lines[i+n]); n++ {
		address = address || addressRE.MatchString(lines[i+n])
		digit = digit || digitRE.MatchString(lines[i+n])
	}
	return n >= r.HeaderMinLines && address && digit
}

// bottomQuote is the index after which every non-blank line is quoted
// (len(lines) when the text does not end in a quote).
func bottomQuote(lines []string) int {
	at := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		switch {
		case Strip(lines[i]) == "":
		case quoted(lines[i]):
			at = i
		default:
			return at
		}
	}
	return at
}

// introAt: the "… wrote:" line right before the bottom quote.
func introAt(lines []string, i, bottom int, r cleanRules) bool {
	l := Strip(lines[i])
	if i >= bottom || quoted(lines[i]) || l == "" || !strings.HasSuffix(l, ":") || Runes(l) > r.IntroMaxRunes {
		return false
	}
	for _, rest := range lines[i+1 : bottom] {
		if Strip(rest) != "" {
			return false
		}
	}
	return true
}

// indented: a line inside a quoted HTML block as Outlook's plain-text Body
// shows it (indented by a tab).
func indented(l string) bool { return strings.HasPrefix(l, "\t") }

// attributionLine: the shape of an attribution line, its own indent aside —
// at most intro_max_runes, ending in ":", naming an address and holding a
// digit: a date and an address on a line ending in a colon, with no word
// of any language. Never a quoted line (inside a quote it belongs to the
// quoted message).
func attributionLine(l string, r cleanRules) bool {
	if quoted(l) {
		return false
	}
	l = Strip(strings.TrimLeft(l, " \t"))
	return strings.HasSuffix(l, ":") && Runes(l) <= r.IntroMaxRunes && addressRE.MatchString(l) && digitRE.MatchString(l)
}

// attributionAt: an attribution line whose quote runs to the end: every
// following non-blank line, up to the end or a recognised boundary, is
// quoted or indented (at least one). An own line of the same shape before
// an indented list and more text of its own is none.
func attributionAt(lines []string, i int, r cleanRules) bool {
	if !attributionLine(lines[i], r) {
		return false
	}
	quote := false
	for j := i + 1; j < len(lines); j++ {
		switch l := lines[j]; {
		case Strip(l) == "":
		case quoted(l) || indented(l):
			quote = true
		case quote && quoteEndsAt(lines, j, r):
			return true
		default:
			return false
		}
	}
	return quote
}

// quoteEndsAt: a line that ends a quote for attributionAt — a captioned
// separator, a bare rule before a header or quoted line, a reply header
// block, a signature delimiter.
func quoteEndsAt(lines []string, i int, r cleanRules) bool {
	return r.caption.MatchString(lines[i]) || ruleBoundary(lines, i, r) || headerBlockAt(lines, i, r) ||
		strings.TrimRight(lines[i], " \t") == "--"
}

// quoteHeaders are the indexes of the attribution lines that are not the
// message's own text: right before a ">" quoted line, or before a quote
// running to the end (attributionAt).
func quoteHeaders(lines []string, r cleanRules) map[int]bool {
	out := map[int]bool{}
	for i, l := range lines {
		if !attributionLine(l, r) {
			continue
		}
		next := i + 1
		for next < len(lines) && Strip(lines[next]) == "" {
			next++
		}
		if next < len(lines) && quoted(lines[next]) || attributionAt(lines, i, r) {
			out[i] = true
		}
	}
	return out
}

func signatureStart(lines []string) int {
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == "--" {
			return i
		}
	}
	return len(lines)
}

// contactBlockStart is where the text ends once a closing contact
// paragraph (and trailing blank lines) is dropped.
func contactBlockStart(lines []string, r cleanRules) int {
	end := len(lines)
	for end > 0 && Strip(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && Strip(lines[start-1]) != "" {
		start--
	}
	if start == 0 || end-start > r.ContactMaxLines {
		return end
	}
	hits := 0
	for _, l := range lines[start:end] {
		if Runes(l) > r.ContactMaxRunes {
			return end
		}
		if r.phone.MatchString(l) || addressRE.MatchString(l) || Links(l) > 0 {
			hits++
		}
	}
	if hits >= r.ContactMinHits {
		return start
	}
	return end
}

func collapseBlank(lines []string) string {
	var b strings.Builder
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if Strip(l) == "" {
			blank = b.Len() > 0
			continue
		}
		if blank {
			b.WriteString("\n")
			blank = false
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(l)
	}
	return Strip(b.String())
}
