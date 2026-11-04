package render

import (
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// The presentation compiler is intentionally smaller than a CSS engine. It
// keeps the declarations that make an email look like an email — typography,
// colour, spacing, borders, widths, backgrounds, simple display and table
// layout — and drops everything that can fetch, execute, escape the frame or
// make one message unbounded. The output is a per-message stylesheet linked
// by the iframe, never a style attribute: the frame's CSP still forbids
// inline style and inline script.
const (
	maxCSSBytes    = 256 << 10
	maxDeclBytes   = 800
	maxSelectorLen = 400
)

// styleSheet collects the compiled rules of one message. A declaration block
// is interned by content, so a repeated signature rule or table attribute is
// emitted once.
type styleSheet struct {
	rules strings.Builder
	seen  map[string]string
	bytes int
}

func newStyleSheet() *styleSheet {
	return &styleSheet{seen: map[string]string{}}
}

func (s *styleSheet) rule(selector, decls string) {
	if s == nil || decls == "" || selector == "" || s.bytes >= maxCSSBytes {
		return
	}
	key := selector + "\x00" + decls
	if _, ok := s.seen[key]; ok {
		return
	}
	rule := selector + "{" + decls + "}"
	if s.bytes+len(rule) > maxCSSBytes {
		return
	}
	s.seen[key] = selector
	s.rules.WriteString(rule)
	s.bytes += len(rule)
}

// raw appends an already-compiled stylesheet (a sanitized sender <style>
// block) ahead of the generated rules. It shares the byte budget so one
// message can never make the reader build an unbounded sheet.
func (s *styleSheet) raw(css string) {
	if s == nil || css == "" || s.bytes >= maxCSSBytes {
		return
	}
	if s.bytes+len(css) > maxCSSBytes {
		css = css[:maxCSSBytes-s.bytes]
	}
	s.rules.WriteString(css)
	s.bytes += len(css)
}

func (s *styleSheet) text() string {
	if s == nil {
		return ""
	}
	return s.rules.String()
}

// inline compiles one sender style attribute into a data-ms hook and returns
// the attribute. The hook is collision-proof: a sender class cannot select
// another element's compiled declarations.
func (s *styleSheet) inline(raw string) string {
	decls := sanitizeDeclarations(raw)
	if decls == "" {
		return ""
	}
	id := digest.Hex([]byte(decls), 16)
	s.rule(`[data-ms="`+id+`"]`, decls)
	return ` data-ms="` + id + `"`
}

// box returns a generated class that reserves the dimensions of an image
// placeholder. It is used for blocked remote images so layout tables do not
// collapse while the image is not loaded.
func (s *styleSheet) box(width, height string) string {
	if width == "" && height == "" {
		return ""
	}
	decls := "display:inline-block;vertical-align:middle;max-width:100%;"
	if width != "" {
		decls += "width:" + width + ";"
	}
	if height != "" {
		decls += "height:" + height + ";"
	}
	id := "mail-image-box-" + digest.Hex([]byte(decls), 12)
	s.rule("."+id, decls)
	return id
}

// sanitizeStylesheet rebuilds a sender <style> block. At-rules that can
// fetch or execute (@import, @font-face, @keyframes) are dropped; @media is
// kept with the same declaration policy. Selectors stay inside the message
// iframe, which contains nothing but this message.
func sanitizeStylesheet(raw string) string {
	raw = stripCSSComments(raw)
	var out strings.Builder
	i := 0
	for i < len(raw) {
		open := strings.IndexByte(raw[i:], '{')
		if open < 0 {
			break
		}
		open += i
		selector := strings.TrimSpace(raw[i:open])
		end, ok := matchingBrace(raw, open)
		if !ok {
			break
		}
		body := raw[open+1 : end]
		i = end + 1
		if selector == "" {
			continue
		}
		if strings.HasPrefix(selector, "@media") {
			query := strings.TrimSpace(strings.TrimPrefix(selector, "@media"))
			if !safeMediaQuery(query) {
				continue
			}
			inner := sanitizeStylesheet(body)
			if inner != "" && out.Len()+len(inner) < maxCSSBytes {
				out.WriteString("@media ")
				out.WriteString(query)
				out.WriteString("{")
				out.WriteString(inner)
				out.WriteString("}")
			}
			continue
		}
		if strings.HasPrefix(selector, "@") || !safeSelector(selector) {
			continue
		}
		decls := sanitizeDeclarations(body)
		if decls == "" {
			continue
		}
		rule := selector + "{" + decls + "}"
		if out.Len()+len(rule) > maxCSSBytes {
			break
		}
		out.WriteString(rule)
	}
	return out.String()
}

func matchingBrace(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func stripCSSComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				break
			}
			i += j + 4
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// dangerousCSS is everything no CSS fragment may contain: a fetch or call
// (url(), expression(, javascript:), a backslash or brace that could escape the
// declaration, a tag delimiter, or an at-rule. Selectors, media queries and
// declaration values all refuse it.
var dangerousCSS = []string{"@", "url(", "expression", "javascript:", "\\", "{", "}", "<", ">"}

// dangerousValueCSS is what only a declaration value can use to smuggle a
// computed value or a behaviour binding past the property vocabulary.
var dangerousValueCSS = []string{"behavior:", "-moz-binding", "var(", "attr(", "env(", "calc("}

// containsAny reports whether s contains any of the refused fragments.
func containsAny(s string, fragments []string) bool {
	for _, fragment := range fragments {
		if strings.Contains(s, fragment) {
			return true
		}
	}
	return false
}

func safeSelector(selector string) bool {
	if len(selector) == 0 || len(selector) > maxSelectorLen {
		return false
	}
	if containsAny(strings.ToLower(selector), dangerousCSS) {
		return false
	}
	for _, r := range selector {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(" .#[]=\"':>+~*,-_()%/&|^$!", r):
		default:
			return false
		}
	}
	return true
}

func safeMediaQuery(query string) bool {
	if query == "" || len(query) > 200 {
		return false
	}
	if containsAny(strings.ToLower(query), dangerousCSS) {
		return false
	}
	for _, r := range query {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(" ():,.-_/", r):
		default:
			return false
		}
	}
	return true
}

// allowedProperties is the presentation vocabulary. It is deliberately
// layout-oriented: no transform/filter/position, no custom properties (which
// could smuggle a url() through var()), no content, no cursor, no animation.
var allowedProperties = map[string]bool{
	"color": true, "background": true, "background-color": true,
	"background-repeat": true, "background-position": true, "background-size": true,
	"font": true, "font-family": true, "font-size": true, "font-weight": true,
	"font-style": true, "font-variant": true, "line-height": true,
	"letter-spacing": true, "word-spacing": true, "text-align": true,
	"text-decoration": true, "text-decoration-color": true, "text-transform": true,
	"text-indent": true, "white-space": true, "word-break": true,
	"overflow-wrap": true, "word-wrap": true, "vertical-align": true,
	"margin": true, "margin-top": true, "margin-right": true, "margin-bottom": true, "margin-left": true,
	"padding": true, "padding-top": true, "padding-right": true, "padding-bottom": true, "padding-left": true,
	"border": true, "border-top": true, "border-right": true, "border-bottom": true, "border-left": true,
	"border-color": true, "border-style": true, "border-width": true, "border-radius": true,
	"border-collapse": true, "border-spacing": true, "table-layout": true, "caption-side": true,
	"width": true, "min-width": true, "max-width": true, "height": true, "min-height": true, "max-height": true,
	"display": true, "float": true, "clear": true, "box-sizing": true, "opacity": true,
	"list-style": true, "list-style-type": true, "list-style-position": true,
	"object-fit": true, "aspect-ratio": true,
}

func sanitizeDeclarations(raw string) string {
	var out []string
	for _, part := range splitDeclarations(raw) {
		prop, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		prop = strings.ToLower(strings.TrimSpace(prop))
		value = trimImportant(strings.TrimSpace(value))
		if !allowedProperties[prop] || !safeValue(prop, value) {
			continue
		}
		out = append(out, prop+":"+value)
	}
	if len(out) == 0 {
		return ""
	}
	s := strings.Join(out, ";") + ";"
	if len(s) > maxDeclBytes {
		return ""
	}
	return s
}

// trimImportant removes a trailing priority marker. The compiled sheet keeps
// sender order and specificity, never a priority that could override the
// reader's own layout rules.
func trimImportant(value string) string {
	if strings.HasSuffix(strings.ToLower(value), "!important") {
		return strings.TrimSpace(value[:len(value)-len("!important")])
	}
	return value
}

func splitDeclarations(raw string) []string {
	var out []string
	start, depth := 0, 0
	var quote byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if quote != 0 {
			if c == quote && (i == 0 || raw[i-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 {
				out = append(out, strings.TrimSpace(raw[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(raw[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func safeValue(prop, value string) bool {
	if value == "" || len(value) > maxDeclBytes {
		return false
	}
	lower := strings.ToLower(value)
	if containsAny(lower, dangerousCSS) || containsAny(lower, dangerousValueCSS) {
		return false
	}
	if strings.Contains(value, "/*") || strings.Contains(value, "*/") {
		return false
	}
	switch prop {
	case "color", "background-color", "border-color", "text-decoration-color":
		return safeColor(value)
	case "background":
		return safeBackground(value)
	case "font-family":
		return safeFont(value)
	case "font-size", "width", "min-width", "max-width", "height", "min-height", "max-height",
		"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
		"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
		"border", "border-top", "border-right", "border-bottom", "border-left",
		"border-width", "border-radius", "border-spacing", "line-height", "letter-spacing", "word-spacing", "text-indent":
		return safeLengths(value)
	case "display":
		switch lower {
		case "none", "block", "inline", "inline-block", "table", "table-row", "table-cell",
			"table-header-group", "table-row-group", "table-footer-group", "list-item", "contents":
			return true
		}
		return false
	case "text-align":
		switch lower {
		case "start", "end", "left", "right", "center", "justify":
			return true
		}
		return false
	case "vertical-align":
		switch lower {
		case "top", "middle", "bottom", "baseline", "sub", "super", "text-top", "text-bottom":
			return true
		}
		return false
	case "font-weight":
		switch lower {
		case "normal", "bold", "bolder", "lighter":
			return true
		}
		n, err := strconv.Atoi(lower)
		return err == nil && n >= 100 && n <= 900
	case "opacity":
		n, err := strconv.ParseFloat(lower, 64)
		return err == nil && n >= 0 && n <= 1
	}
	return safeGeneric(value)
}

func safeGeneric(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(" .,#%()-_/+", r):
		default:
			return false
		}
	}
	return true
}

func safeColor(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "transparent" || v == "currentcolor" || v == "inherit" || v == "initial" || v == "unset" {
		return true
	}
	if strings.HasPrefix(v, "#") {
		n := len(v) - 1
		if n != 3 && n != 4 && n != 6 && n != 8 {
			return false
		}
		for _, r := range v[1:] {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(v, "rgb(") || strings.HasPrefix(v, "rgba(") ||
		strings.HasPrefix(v, "hsl(") || strings.HasPrefix(v, "hsla(") {
		return strings.HasSuffix(v, ")") && safeColorArgs(v)
	}
	if len(v) <= 30 {
		for _, r := range v {
			if !(r >= 'a' && r <= 'z') && r != '-' {
				return false
			}
		}
		return v != ""
	}
	return false
}

func safeColorArgs(v string) bool {
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case strings.ContainsRune("(),. %+/#", r):
		default:
			return false
		}
	}
	return true
}

func safeFont(value string) bool {
	if len(value) > 200 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(" ,'\"-_", r):
		default:
			return false
		}
	}
	return true
}

func safeBackground(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "none" || v == "transparent" || v == "inherit" || v == "initial" {
		return true
	}
	if strings.HasPrefix(v, "linear-gradient(") || strings.HasPrefix(v, "radial-gradient(") {
		return strings.HasSuffix(v, ")") && safeColorArgs(v)
	}
	return safeColor(value)
}

// safeLengths accepts a bounded set of lengths, percentages and keywords.
// A 600px table is allowed; a 100000px spacer is not, because it would make
// the message unscrollably wide inside the reader.
func safeLengths(value string) bool {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		if !safeLength(field) {
			return false
		}
	}
	return true
}

func safeLength(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "auto", "inherit", "initial", "unset", "normal", "none", "0":
		return true
	case "thin", "medium", "thick", "solid", "dashed", "dotted", "double", "hidden":
		return true
	}
	if strings.HasPrefix(v, "#") {
		return safeColor(v)
	}
	if strings.HasPrefix(v, "rgb(") || strings.HasPrefix(v, "rgba(") {
		return safeColor(v)
	}
	i := 0
	for i < len(v) && (v[i] == '-' || v[i] == '+' || v[i] == '.' || (v[i] >= '0' && v[i] <= '9')) {
		i++
	}
	if i == 0 || i == len(v) {
		return false
	}
	n, err := strconv.ParseFloat(v[:i], 64)
	if err != nil {
		return false
	}
	unit := v[i:]
	if unit == "" {
		return n >= 0 && n <= 10
	}
	switch unit {
	case "px", "pt":
		return n >= -200 && n <= 2000
	case "em", "rem", "ex", "ch":
		return n >= -20 && n <= 100
	case "%", "vw", "vh", "vmin", "vmax":
		return n >= -100 && n <= 1000
	}
	return false
}
