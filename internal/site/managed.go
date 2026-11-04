package site

import (
	"errors"
	"fmt"
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

// navLine is the one navigation line for one authored page. The current page is
// bold, every other destination is a link, and the language switch keeps the
// reader on the same page.
func (s Site) navLine(lang, current string) string {
	links := s.docLinks()
	parts := make([]string, 0, len(s.Nav)+2)
	if current != "index" {
		parts = append(parts, fmt.Sprintf("[%s](%s)", s.docsLabel(lang), "index.md"))
	}
	for _, item := range s.Nav {
		href, ok := links[item.Key]
		if !ok {
			continue
		}
		if item.Key == current {
			parts = append(parts, "**"+item.Label.For(lang)+"**")
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s](%s)", item.Label.For(lang), href))
	}
	parts = append(parts, s.docLanguageSwitch(lang, current))
	return strings.Join(parts, " · ")
}

func (s Site) docsLabel(lang string) string {
	return "Documentation"
}

// docLanguageSwitch points at the same page in the other language tree. The
// generated navigation is the one language switch, so it marks its link for the
// stored preference and the delegated click listener. The anchor is written as
// HTML because Markdown has no portable attribute syntax; the href is built
// with the same .md to .html rule as every generated HTML anchor.
func (s Site) docLanguageSwitch(lang, current string) string {
	if lang == "fr" {
		if current == "index" {
			return `<a href="../" hreflang="en" lang="en" data-lang-switch="en">English</a>`
		}
		return fmt.Sprintf(`<a href="%s" hreflang="en" lang="en" data-lang-switch="en">English</a>`,
			htmlHref("../"+pageFile(current)))
	}
	href := "fr/" + pageFile(current)
	if current == "index" {
		href = "fr/"
	}
	return fmt.Sprintf(`<a href="%s" hreflang="fr" lang="fr" data-lang-switch="fr">Français</a>`, htmlHref(href))
}

// footerLine is the one page footer for one authored page.
func (s Site) footerLine(lang string) string {
	links := s.docLinks()
	privacy := links["privacy"]
	if lang == "fr" {
		return fmt.Sprintf("[Code source](%s) · %s · [%s](%s) · [Confidentialité](%s)",
			s.Repository, LicenseID, s.docsLabel(lang), s.docsHome(lang), privacy)
	}
	return fmt.Sprintf("[Source](%s) · %s · [%s](%s) · [Privacy](%s)",
		s.Repository, LicenseID, s.docsLabel(lang), s.docsHome(lang), privacy)
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
