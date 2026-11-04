package render

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// The policy is an allowlist, not a blocklist. The element set is what a
// mail reader needs to stay readable: paragraphs, headings, lists, tables,
// quotes, basic emphasis and links. Everything else is dropped, except for
// the containers below whose content is dropped with the tag (a <style> or
// <script> body must never surface as text).
//
// Email presentation survives only through the compiler: a <style> block is
// rebuilt rule by rule into a per-message sheet, and a style attribute becomes
// a data-ms hook. Classes and ids are kept because they are the selectors that
// sheet uses; the iframe contains nothing but the message. The compiler drops
// url(), @import, @font-face and every escaping property.
var allowedElements = map[string]string{
	"p": "p", "div": "div", "span": "span", "br": "br", "hr": "hr",
	"h1": "h1", "h2": "h2", "h3": "h3", "h4": "h4", "h5": "h5", "h6": "h6",
	"ul": "ul", "ol": "ol", "li": "li", "dl": "dl", "dt": "dt", "dd": "dd",
	"blockquote": "blockquote", "pre": "pre", "code": "code", "tt": "code",
	"b": "b", "strong": "strong", "i": "i", "em": "em", "u": "u", "s": "s", "strike": "s",
	"del": "del", "ins": "ins", "sub": "sub", "sup": "sup", "small": "small", "big": "big",
	"mark": "mark", "font": "font",
	"q": "q", "address": "address", "center": "div",
	"article": "article", "section": "section", "header": "header", "footer": "footer",
	"main": "main", "nav": "nav", "aside": "aside", "figure": "figure", "figcaption": "figcaption",
	"table": "table", "caption": "caption", "colgroup": "colgroup", "col": "col",
	"thead": "thead", "tbody": "tbody", "tfoot": "tfoot", "tr": "tr", "td": "td", "th": "th",
	"a": "a",
}

// dropElements remove their whole subtree. Their text is not message text:
// scripts, style sheets, embedded documents, forms and media.
var dropElements = map[string]bool{
	"script": true, "style": true, "title": true, "head": true, "meta": true, "link": true, "base": true,
	"noscript": true, "template": true, "svg": true, "math": true,
	"iframe": true, "frame": true, "frameset": true, "noframes": true, "object": true, "embed": true,
	"applet": true, "param": true,
	"form": true, "input": true, "button": true, "select": true, "option": true, "optgroup": true,
	"textarea": true, "fieldset": true, "legend": true, "datalist": true, "output": true,
	"progress": true, "meter": true,
	"video": true, "audio": true, "source": true, "track": true, "canvas": true,
	"map": true, "area": true, "picture": true,
}

// globalAttributes are safe on every allowed element. class and id are kept
// because they are the selectors a sender stylesheet uses; the stylesheet is
// itself rebuilt by the presentation compiler. style is compiled into a
// data-ms hook, never emitted as an inline style attribute.
var globalAttributes = map[string]bool{"dir": true, "lang": true, "title": true, "class": true, "id": true, "role": true}

// elementAttributes are the extra attributes of one element. Presentation
// attributes are bounded and validated in attrs; the renderer keeps the ones
// an email layout depends on (table/cell widths, alignment, backgrounds and
// image dimensions) instead of letting mail.css impose a data-table look on
// every layout table.
var elementAttributes = map[string]map[string]bool{
	"a": {"href": true},
	"table": {"width": true, "height": true, "align": true, "bgcolor": true, "border": true,
		"cellpadding": true, "cellspacing": true},
	"td": {"colspan": true, "rowspan": true, "width": true, "height": true, "align": true,
		"valign": true, "bgcolor": true, "nowrap": true},
	"th": {"colspan": true, "rowspan": true, "scope": true, "width": true, "height": true,
		"align": true, "valign": true, "bgcolor": true, "nowrap": true},
	"tr":       {"align": true, "valign": true, "bgcolor": true},
	"col":      {"span": true, "width": true, "align": true, "valign": true},
	"colgroup": {"span": true, "width": true, "align": true, "valign": true},
	"img":      {"width": true, "height": true, "align": true, "border": true, "hspace": true, "vspace": true, "alt": true, "title": true},
	"font":     {"color": true, "face": true, "size": true},
	"ol":       {"start": true},
}

// voidElements never get a closing tag.
var voidElements = map[string]bool{"br": true, "hr": true, "col": true}

// safeLink returns the canonical form of a link the renderer may emit. Only
// http, https and mailto are allowed; credentials, relative URLs, protocol-
// relative URLs and every other scheme are refused.
func safeLink(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 4096 || strings.ContainsRune(s, '\\') {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return "", false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" || u.Opaque != "" {
			return "", false
		}
	case "mailto":
		if u.Opaque == "" && u.Path == "" {
			return "", false
		}
	default:
		return "", false
	}
	return u.String(), true
}

// srcsetURLs is the URL part of every srcset candidate. It is only used to
// classify the image (remote/cid); the attribute itself is never emitted.
func srcsetURLs(srcset string) []string {
	if strings.TrimSpace(srcset) == "" {
		return nil
	}
	parts := strings.Split(srcset, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if fields := strings.Fields(part); len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}

// visibleText is a short, escaped label for an image placeholder: the one rune
// truncation over the trimmed alt text.
func visibleText(alt string) string {
	return truncateRunes(strings.TrimSpace(alt), 160)
}

// boundedInt parses a structural numeric attribute and bounds it.
func boundedInt(raw string, min, max int) (string, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < min || n > max {
		return "", false
	}
	return strconv.Itoa(n), true
}

// safeLang accepts a BCP 47-shaped tag without allowing markup or spaces.
func safeLang(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 64 {
		return "", false
	}
	part := false
	for _, r := range s {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
			part = true
		case r == '-':
			if !part {
				return "", false
			}
			part = false
		default:
			return "", false
		}
	}
	return s, part
}

// safeDir accepts only the three values that matter to a mail body.
func safeDir(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ltr", "rtl", "auto":
		return strings.ToLower(strings.TrimSpace(raw)), true
	}
	return "", false
}
