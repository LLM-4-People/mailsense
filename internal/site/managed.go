package site

import (
	"errors"
	"fmt"
	"html"
	"strings"
)

// The authored public pages keep their content and let gensite own two blocks:
// the navigation after the title and the footer at the end. The markers are
// the contract; a page without them fails the check instead of silently
// shipping a hand-copied navigation.
const (
	navStart    = "<!-- nav:start -->"
	navEnd      = "<!-- nav:end -->"
	footerStart = "<!-- footer:start -->"
	footerEnd   = "<!-- footer:end -->"
)

// navLine is the one documentation index for one authored page. It becomes a
// sticky index under the page title: it wraps to a second row when the width
// allows and scrolls horizontally where space is tight. The current page is
// marked with aria-current and is not a second link to itself. The language
// switch lives in the masthead, so it is not repeated here.
func (s Site) navLine(lang, current string) string {
	links := s.docLinks()
	parts := make([]string, 0, len(s.Nav)+5)
	parts = append(parts, `<div class="doc-index-wrap">`)
	parts = append(parts, `<nav class="doc-index" aria-label="`+html.EscapeString(s.docsLabel(lang))+`">`)
	if current != "index" {
		parts = append(parts, fmt.Sprintf(`<a href="%s">%s</a>`,
			htmlHref(pageFile("index")), html.EscapeString(s.docsLabel(lang))))
	}
	for _, item := range s.Nav {
		href, ok := links[item.Key]
		if !ok {
			continue
		}
		if item.Key == current {
			parts = append(parts, `<span class="doc-index-current" aria-current="page">`+html.EscapeString(item.Label.For(lang))+`</span>`)
			continue
		}
		parts = append(parts, fmt.Sprintf(`<a href="%s">%s</a>`,
			htmlHref(href), html.EscapeString(item.Label.For(lang))))
	}
	parts = append(parts, "</nav>")
	parts = append(parts, `<span class="doc-index-hint" aria-hidden="true"></span>`)
	parts = append(parts, "</div>")
	return strings.Join(parts, "\n  ")
}

func (s Site) docsLabel(lang string) string {
	return "Documentation"
}

// footerLine is the one page footer for one authored page. It is raw HTML so
// the generated block markers stay outside the paragraph, and so the page meta
// line has one class the stylesheet can own.
func (s Site) footerLine(lang string) string {
	links := s.docLinks()
	privacy := htmlHref(links["privacy"])
	if lang == "fr" {
		return fmt.Sprintf(`<p class="doc-meta"><a href="%s">Code source</a> · %s · <a href="%s">%s</a> · <a href="%s">Confidentialité</a></p>`,
			html.EscapeString(s.Repository), LicenseID, html.EscapeString(s.docsHome(lang)), html.EscapeString(s.docsLabel(lang)), privacy)
	}
	return fmt.Sprintf(`<p class="doc-meta"><a href="%s">Source</a> · %s · <a href="%s">%s</a> · <a href="%s">Privacy</a></p>`,
		html.EscapeString(s.Repository), LicenseID, html.EscapeString(s.docsHome(lang)), html.EscapeString(s.docsLabel(lang)), privacy)
}

// pageKeyFor finds the page table key that owns a file.
func (s Site) pageKeyFor(name string) (string, string, bool) {
	for _, page := range s.Pages() {
		for _, lang := range []string{"en", "fr"} {
			if page.File(lang) == name {
				return page.Key, lang, true
			}
		}
	}
	return "", "", false
}

// applyManaged replaces the two generated blocks in one authored page.
func (s Site) applyManaged(name string, body []byte) ([]byte, error) {
	key, lang, ok := s.pageKeyFor(name)
	if !ok {
		return nil, fmt.Errorf("managed page %s is not in the page table", name)
	}
	text := string(body)
	for _, block := range []struct {
		name  string
		start string
		end   string
		body  string
	}{
		{"navigation", navStart, navEnd, s.navLine(lang, key)},
		{"footer", footerStart, footerEnd, s.footerLine(lang)},
	} {
		start := strings.Index(text, block.start)
		if start < 0 {
			return nil, fmt.Errorf("%s: missing %s marker for the generated %s", name, block.start, block.name)
		}
		rest := text[start+len(block.start):]
		end := strings.Index(rest, block.end)
		if end < 0 {
			return nil, fmt.Errorf("%s: missing %s marker for the generated %s", name, block.end, block.name)
		}
		text = text[:start] + block.start + "\n" + strings.TrimSpace(block.body) + "\n" + block.end + rest[end+len(block.end):]
	}
	if strings.Count(text, navStart) != 1 || strings.Count(text, footerStart) != 1 {
		return nil, errors.New(name + ": duplicate generated block marker")
	}
	return []byte(text), nil
}
