package site

import (
	"fmt"
	"html"
	"strings"
)

// Layout renders docs/_layouts/default.html. It is the one page frame for both
// landings and every documentation page: a skip link, the generated masthead,
// the page-content landmark and the generated sitemap footer. The body class
// lets the stylesheet give the two surfaces their own measure.
func (s Site) Layout() []byte {
	var out strings.Builder
	out.WriteString("<!DOCTYPE html>\n")
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString(`<html lang="{{ page.lang | default: site.lang | default: "en" }}">` + "\n")
	out.WriteString("{%- include head.html -%}\n")
	out.WriteString(`<body class="{% if page.language_switch %}is-landing{% else %}is-doc{% endif %}">` + "\n")
	out.WriteString(`<a class="skip-link" href="#main">` + liquidText(s.Chrome.Skip) + `</a>` + "\n")
	out.WriteString("{%- include header.html -%}\n")
	out.WriteString(`<main class="page-content" id="main">` + "\n")
	out.WriteString("{%- if page.language_switch -%}\n")
	out.WriteString("{{ content }}\n")
	out.WriteString("{%- else -%}\n")
	out.WriteString(`  <div class="shell doc-head">` + "\n")
	out.WriteString(`    <nav class="breadcrumb" aria-label="` + liquidText(s.Chrome.Breadcrumb) + `">` + "\n")
	out.WriteString(`      <a href="{% if page.lang == 'fr' %}{{ '/fr/' | relative_url }}{% else %}{{ '/' | relative_url }}{% endif %}">` + html.EscapeString(s.docsLabel("en")) + `</a>` + "\n")
	out.WriteString(`      <span aria-hidden="true">/</span>` + "\n")
	out.WriteString(`      <span aria-current="page">{{ page.title }}</span>` + "\n")
	out.WriteString("    </nav>\n")
	out.WriteString("  </div>\n")
	out.WriteString(`  <div class="shell doc-layout">` + "\n")
	out.WriteString(`    <details class="doc-rail-compact">` + "\n")
	out.WriteString(`      <summary>` + liquidText(s.Chrome.OnThisPage) + `</summary>` + "\n")
	out.WriteString(`      <ol class="rail-list" data-rail-list></ol>` + "\n")
	out.WriteString("    </details>\n")
	out.WriteString(`    <div class="doc-main">` + "\n")
	out.WriteString("{{ content }}\n")
	out.WriteString("    </div>\n")
	out.WriteString(`    <aside class="doc-rail" aria-label="` + liquidText(s.Chrome.OnThisPage) + `">` + "\n")
	out.WriteString(`      <p class="rail-title" id="rail-title" hidden>` + liquidText(s.Chrome.OnThisPage) + `</p>` + "\n")
	out.WriteString(`      <ol class="rail-list" data-rail-list></ol>` + "\n")
	out.WriteString(`      <p class="rail-fallback"><a href="#main">` + liquidText(s.Chrome.BackToTop) + `</a></p>` + "\n")
	out.WriteString("    </aside>\n")
	out.WriteString("  </div>\n")
	out.WriteString("  <script>\n")
	out.WriteString(railScript())
	out.WriteString("\n  </script>\n")
	out.WriteString("{%- endif -%}\n")
	out.WriteString("</main>\n")
	out.WriteString("{%- include footer.html -%}\n")
	out.WriteString("</body>\n</html>\n")
	return []byte(out.String())
}

// liquidText writes one localized fact as a Liquid branch, so a single
// generated include serves both trees without a per-language copy.
func liquidText(value Localized) string {
	return `{% if page.lang == "fr" %}` + html.EscapeString(value.FR) + `{% else %}` + html.EscapeString(value.EN) + `{% endif %}`
}

// railScript fills the optional on-this-page rail from the page's h2 elements.
// It runs after the content, makes no request and leaves the page complete
// when JavaScript is off.
func railScript() string {
	return `(function () {
  "use strict";
  var headings = document.querySelectorAll(".doc-main h2");
  var lists = document.querySelectorAll("[data-rail-list]");
  var title = document.getElementById("rail-title");
  if (lists.length === 0) {
    return;
  }
  for (var listIndex = 0; listIndex < lists.length; listIndex += 1) {
    var list = lists[listIndex];
    for (var index = 0; index < headings.length; index += 1) {
      var heading = headings[index];
      if (!heading.id) {
        continue;
      }
      var item = document.createElement("li");
      var link = document.createElement("a");
      link.href = "#" + heading.id;
      link.textContent = heading.textContent;
      item.appendChild(link);
      list.appendChild(item);
    }
  }
  if (title && headings.length > 0) {
    title.hidden = false;
  }
  // The compact rail is generated before the content so it stays in the
  // layout without JavaScript. On a narrow screen it reads better directly
  // after the title and the document index, so move it there once the page
  // is parsed. With JavaScript off it remains a collapsed control above the
  // title and the page is still complete.
  var wrap = document.querySelector(".doc-index-wrap");
  var compact = document.querySelector(".doc-rail-compact");
  if (compact && wrap && wrap.parentNode) {
    wrap.parentNode.insertBefore(compact, wrap.nextSibling);
  }
  var index = wrap ? wrap.querySelector(".doc-index") : null;
  function syncIndexHeight() {
    var root = document.documentElement;
    if (!wrap) {
      root.style.removeProperty("--doc-index-h");
      return;
    }
    root.style.setProperty("--doc-index-h", wrap.offsetHeight + "px");
  }
  function syncIndex() {
    if (!wrap || !index) {
      return;
    }
    var more = index.scrollWidth - index.clientWidth > 2;
    var atEnd = index.scrollLeft + index.clientWidth >= index.scrollWidth - 2;
    wrap.classList.toggle("is-scrollable", more && !atEnd);
  }
  if (index) {
    index.addEventListener("scroll", syncIndex, { passive: true });
  }
  window.addEventListener("resize", function () {
    syncIndex();
    syncIndexHeight();
  });
  syncIndex();
  syncIndexHeight();
})();`
}

// Header renders docs/_includes/header.html. The masthead is sticky, carries
// the mark and the primary links, and collapses to a native details menu below
// the desktop breakpoint. The same links are written once per language branch
// and once for the menu, so no page body repeats the navigation.
func (s Site) Header() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString(`<header class="masthead" role="banner">` + "\n")
	out.WriteString(`  <div class="shell masthead-inner">` + "\n")
	out.WriteString(`    <a class="brand" href="{% if page.lang == 'fr' %}{{ '/fr/' | relative_url }}{% else %}{{ '/' | relative_url }}{% endif %}">` + "\n")
	out.WriteString(`      <img src="{{ '/assets/mark.svg' | relative_url }}" width="32" height="32" alt="">` + "\n")
	out.WriteString(`      <span class="brand-name">` + html.EscapeString(s.Product) + `</span>` + "\n")
	out.WriteString("    </a>\n")
	out.WriteString(`    <nav class="masthead-links" aria-label="` + liquidText(s.Chrome.Primary) + `">` + "\n")
	out.WriteString(s.headerLanguageBranch())
	out.WriteString("    </nav>\n")
	out.WriteString(`    <details class="masthead-menu">` + "\n")
	out.WriteString(`      <summary>` + liquidText(s.Chrome.Menu) + `</summary>` + "\n")
	out.WriteString(`      <nav class="masthead-menu-panel" aria-label="` + liquidText(s.Chrome.Primary) + `">` + "\n")
	out.WriteString(s.headerLanguageBranch())
	out.WriteString("      </nav>\n")
	out.WriteString("    </details>\n")
	out.WriteString("  </div>\n")
	out.WriteString("</header>\n")
	return []byte(out.String())
}

// headerLanguageBranch writes the French and English masthead links.
func (s Site) headerLanguageBranch() string {
	var out strings.Builder
	out.WriteString(`{% if page.lang == "fr" %}` + "\n")
	out.WriteString(s.headerLinks("fr", "      "))
	out.WriteString(`{% else %}` + "\n")
	out.WriteString(s.headerLinks("en", "      "))
	out.WriteString(`{% endif %}` + "\n")
	return out.String()
}

// headerLinks writes the five primary links for one language, the language
// switch and the theme control. The path is also the page URL the current-page
// condition compares, so the active state cannot drift from the destination.
func (s Site) headerLinks(lang, indent string) string {
	var out strings.Builder
	ask := "/ask.html"
	docs := "/"
	install := "/install.html"
	other := "fr"
	switchHref := `{{ '/fr' | append: page.url | relative_url }}`
	if lang == "fr" {
		ask = "/fr/ask.html"
		docs = "/fr/"
		install = "/fr/install.html"
		other = "en"
		switchHref = `{{ page.url | remove_first: '/fr' | relative_url }}`
	}
	out.WriteString(s.headerLink(lang, indent, ask, s.navLabelFor(lang, "ask"), ""))
	out.WriteString(s.headerLink(lang, indent, docs, s.Chrome.Docs.For(lang), ""))
	out.WriteString(s.headerLink(lang, indent, install, s.navLabelFor(lang, "install"), "masthead-cta"))
	fmt.Fprintf(&out, `%s<a class="masthead-link" href="%s">%s</a>`+"\n", indent, html.EscapeString(s.Repository), html.EscapeString(s.Chrome.GitHub.For(lang)))
	fmt.Fprintf(&out, `%s<a class="masthead-link masthead-switch" href="%s" hreflang="%s" lang="%s" data-lang-switch="%s">%s</a>`+"\n",
		indent, switchHref, other, other, other, html.EscapeString(s.Chrome.Language.For(lang)))
	out.WriteString(s.themeGroup(indent))
	return out.String()
}

// themeGroup writes the three-way theme control. The system choice is the
// pressed default; the head script rewrites the pressed state from the stored
// choice on click and after parse, so a stored choice is shown on the buttons.
// The labels are localized facts like every other chrome label.
func (s Site) themeGroup(indent string) string {
	var out strings.Builder
	fmt.Fprintf(&out, `%s<span class="masthead-theme" role="group" aria-label="%s">`+"\n", indent, liquidText(s.Chrome.Theme))
	for _, choice := range s.themeChoices() {
		pressed := "false"
		if choice.Value == themeSystem {
			pressed = "true"
		}
		fmt.Fprintf(&out, `%s  <button type="button" class="masthead-theme-button" data-theme-choice="%s" aria-pressed="%s">%s</button>`+"\n",
			indent, choice.Value, pressed, liquidText(choice.Label))
	}
	out.WriteString(indent + "</span>\n")
	return out.String()
}

// headerLink writes one link with a Liquid current-page state.
func (s Site) headerLink(lang, indent, path, label, extra string) string {
	condition := "{% if page.url == '" + path + "' %}"
	class := "masthead-link"
	if extra != "" {
		class += " " + extra
	}
	return indent + `<a class="` + class + condition + ` is-current{% endif %}" href="{{ '` + path + `' | relative_url }}"` + condition + ` aria-current="page"{% endif %}>` + html.EscapeString(label) + `</a>` + "\n"
}

// navLabelFor returns one nav label for the masthead.
func (s Site) navLabelFor(lang, key string) string {
	for _, item := range s.Nav {
		if item.Key == key {
			return item.Label.For(lang)
		}
	}
	return key
}

// Footer renders docs/_includes/footer.html. It is the dense sitemap, not a
// second navigation block inside a page: every page gets it once, outside the
// page-content landmark.
func (s Site) Footer() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString(`<footer class="site-footer">` + "\n")
	out.WriteString(`  <div class="shell footer-grid">` + "\n")
	out.WriteString(`    <div class="footer-brand">` + "\n")
	fmt.Fprintf(&out, `      <p class="footer-name">%s</p>`+"\n", html.EscapeString(s.Product))
	fmt.Fprintf(&out, `      <p class="footer-tagline">%s</p>`+"\n", liquidText(s.Hero.Banner))
	fmt.Fprintf(&out, `      <p class="footer-license">%s</p>`+"\n", html.EscapeString(LicenseID))
	out.WriteString("    </div>\n")
	out.WriteString(`{% if page.lang == "fr" %}` + "\n")
	out.WriteString(s.footerColumns("fr", "    "))
	out.WriteString(`{% else %}` + "\n")
	out.WriteString(s.footerColumns("en", "    "))
	out.WriteString(`{% endif %}` + "\n")
	out.WriteString("  </div>\n")
	out.WriteString(`  <div class="shell footer-bottom">` + "\n")
	fmt.Fprintf(&out, `    <p>%s · %s</p>`+"\n", html.EscapeString(s.Product), html.EscapeString(LicenseID))
	out.WriteString(`    <p>{% if page.lang == "fr" %}<a href="{{ page.url | remove_first: '/fr' | relative_url }}" hreflang="en" lang="en" data-lang-switch="en">` + html.EscapeString(s.Chrome.Language.FR) + `</a>{% else %}<a href="{{ '/fr' | append: page.url | relative_url }}" hreflang="fr" lang="fr" data-lang-switch="fr">` + html.EscapeString(s.Chrome.Language.EN) + `</a>{% endif %}</p>` + "\n")
	out.WriteString("  </div>\n")
	out.WriteString("</footer>\n")
	return []byte(out.String())
}

// footerColumns writes the three sitemap columns for one language.
func (s Site) footerColumns(lang, indent string) string {
	var out strings.Builder
	product := []string{"ask", "install", "first_run", "privacy"}
	docs := []string{"console", "mailboxes", "providers", "learning", "faq", "security", "troubleshooting", "contributing"}
	out.WriteString(indent + `<nav class="footer-col" aria-labelledby="footer-product">` + "\n")
	fmt.Fprintf(&out, `%s  <p class="footer-heading" id="footer-product">%s</p>`+"\n", indent, html.EscapeString(s.Chrome.FooterProduct.For(lang)))
	out.WriteString(indent + "  <ul>\n")
	for _, key := range product {
		out.WriteString(s.footerPageLink(lang, indent+"    ", key))
	}
	out.WriteString(indent + "  </ul>\n" + indent + "</nav>\n")
	out.WriteString(indent + `<nav class="footer-col" aria-labelledby="footer-docs">` + "\n")
	fmt.Fprintf(&out, `%s  <p class="footer-heading" id="footer-docs">%s</p>`+"\n", indent, html.EscapeString(s.Chrome.FooterDocs.For(lang)))
	out.WriteString(indent + "  <ul>\n")
	for _, key := range docs {
		out.WriteString(s.footerPageLink(lang, indent+"    ", key))
	}
	out.WriteString(indent + "  </ul>\n" + indent + "</nav>\n")
	out.WriteString(indent + `<nav class="footer-col" aria-labelledby="footer-project">` + "\n")
	fmt.Fprintf(&out, `%s  <p class="footer-heading" id="footer-project">%s</p>`+"\n", indent, html.EscapeString(s.Chrome.FooterProject.For(lang)))
	out.WriteString(indent + "  <ul>\n")
	fmt.Fprintf(&out, `%s    <li><a href="%s">%s</a></li>`+"\n", indent, html.EscapeString(s.Repository+"/releases"), html.EscapeString(s.Chrome.Releases.For(lang)))
	fmt.Fprintf(&out, `%s    <li><a href="%s">%s</a></li>`+"\n", indent, html.EscapeString(s.Repository), html.EscapeString(s.sourceLabel(lang)))
	fmt.Fprintf(&out, `%s    <li><a href="%s">%s</a></li>`+"\n", indent, html.EscapeString(s.Repository+"/LICENSE"), html.EscapeString(LicenseID))
	fmt.Fprintf(&out, `%s    <li><a href="%s">%s</a></li>`+"\n", indent, html.EscapeString(s.docsHome(lang)), html.EscapeString(s.docsLabel(lang)))
	out.WriteString(indent + "  </ul>\n" + indent + "</nav>\n")
	return out.String()
}

// footerPageLink writes one public page link from the page table.
func (s Site) footerPageLink(lang, indent, key string) string {
	for _, item := range s.Nav {
		if item.Key != key {
			continue
		}
		for _, page := range s.Pages() {
			if page.Key != key {
				continue
			}
			rel := strings.TrimPrefix(page.File(lang), "docs/")
			href := "/" + htmlHref(rel)
			return indent + `<li><a href="{{ '` + href + `' | relative_url }}">` + html.EscapeString(item.Label.For(lang)) + `</a></li>` + "\n"
		}
	}
	return ""
}
