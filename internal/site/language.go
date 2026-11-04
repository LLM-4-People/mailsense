package site

import (
	"fmt"
	"html"
	"strings"
)

// The automatic language switch has one owner: this file. The two landing
// pages carry the alternate links and the script, the generated navigation
// marks its switch link, and the tests read the same constants.
const (
	// LangStorageKey is the localStorage entry the visitor's explicit language
	// choice is stored under. The writer and the reader both use it.
	LangStorageKey = "mailsense.lang"
	// LangGuardKey is the one-shot sessionStorage entry that stops a wrong
	// browser-language guess from redirecting a second time.
	LangGuardKey = "mailsense.lang.guard"
)

// landingPath is the site path of a landing page, derived from the page table
// and the one baseurl fact. It is never typed twice: the redirect, the
// alternates and the tests all call this function.
func (s Site) landingPath(lang string) string {
	for _, page := range s.Pages() {
		if page.Key != "index" {
			continue
		}
		rel := strings.TrimPrefix(page.File(lang), "docs/")
		rel = strings.TrimSuffix(rel, "index.md")
		return strings.TrimSuffix(s.BaseURL, "/") + "/" + rel
	}
	return ""
}

// landingURL is the absolute address of a landing page for the alternate link.
func (s Site) landingURL(lang string) string {
	path := s.landingPath(lang)
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(s.siteOrigin(), "/") + path
}

// languageScript is the one inline script. It runs in the head of both landing
// pages, before paint, and makes no network request.
//
// The destinations come from the alternate links above it, so the script
// contains no path and no URL. The current language comes from the html
// element, which the theme fills from the page's front matter.
func languageScript() string {
	return fmt.Sprintf(`(function () {
  "use strict";
  var KEY = %q;
  var GUARD = %q;
  function preference() {
    try {
      var value = localStorage.getItem(KEY);
      return value === "en" || value === "fr" ? value : "";
    } catch (error) {
      return "";
    }
  }
  function remember(value) {
    try {
      localStorage.setItem(KEY, value);
    } catch (error) {
      return;
    }
  }
  // The guard only stops a redirect loop. It expires after a few seconds, so
  // it cannot disable detection for the rest of the session, which would
  // ignore a language the visitor chose later on.
  function guarded() {
    try {
      var at = Number(sessionStorage.getItem(GUARD) || 0);
      return at > 0 && Date.now() - at < 5000;
    } catch (error) {
      return false;
    }
  }
  function guard() {
    try {
      sessionStorage.setItem(GUARD, String(Date.now()));
    } catch (error) {
      return;
    }
  }
  function unguard() {
    try {
      sessionStorage.removeItem(GUARD);
    } catch (error) {
      return;
    }
  }
  var here = String(document.documentElement.lang || "").slice(0, 2).toLowerCase();
  if (here !== "en" && here !== "fr") {
    return;
  }
  var other = here === "fr" ? "en" : "fr";
  var alternates = document.querySelectorAll('link[rel="alternate"][hreflang]');
  var target = "";
  for (var index = 0; index < alternates.length; index += 1) {
    var alternate = alternates[index];
    if (String(alternate.getAttribute("hreflang")).slice(0, 2).toLowerCase() === other) {
      try {
        target = new URL(alternate.getAttribute("href"), document.baseURI).pathname;
      } catch (error) {
        target = "";
      }
      break;
    }
  }
  var language = preference();
  if (!language) {
    var preferred = (navigator.languages && navigator.languages[0]) || navigator.language || "";
    language = String(preferred).toLowerCase().slice(0, 2) === "fr" ? "fr" : "en";
  }
  if (target && language !== here && !guarded()) {
    guard();
    location.replace(target);
    return;
  }
  document.addEventListener("click", function (event) {
    var link = event.target && event.target.closest ? event.target.closest("a[data-lang-switch]") : null;
    if (!link) {
      return;
    }
    var value = link.getAttribute("data-lang-switch");
    if (value === "en" || value === "fr") {
      remember(value);
      // An explicit choice wins now, not only after the guard window.
      unguard();
    }
  }, true);
})();`, LangStorageKey, LangGuardKey)
}

// Head renders docs/_includes/head.html, the site override of the bundled
// theme's head. The theme is the version pinned by github-pages, so the base
// tags are stable; the generated block adds the two alternate links and the
// script on the landing pages only.
func (s Site) Head() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<!-- Overrides the theme head. Keep in step with the pinned theme. -->\n")
	out.WriteString("<head>\n")
	out.WriteString("  <meta charset=\"utf-8\">\n")
	out.WriteString("  <meta http-equiv=\"X-UA-Compatible\" content=\"IE=edge\">\n")
	out.WriteString("  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("  {%- if page.language_switch -%}\n")
	fmt.Fprintf(&out, "  <link rel=\"alternate\" hreflang=\"en\" href=\"%s\">\n", html.EscapeString(s.landingURL("en")))
	fmt.Fprintf(&out, "  <link rel=\"alternate\" hreflang=\"fr\" href=\"%s\">\n", html.EscapeString(s.landingURL("fr")))
	out.WriteString("  <script>\n")
	out.WriteString(languageScript())
	out.WriteString("\n  </script>\n")
	out.WriteString("  {%- endif -%}\n")
	out.WriteString("  {%- seo -%}\n")
	// Every page declares the same icon. The SVG is the brand mark, and the
	// PNG pair covers browsers and touch screens that ignore SVG favicons.
	out.WriteString("  <link rel=\"icon\" type=\"image/svg+xml\" href=\"{{ \"/assets/mark.svg\" | relative_url }}\">\n")
	out.WriteString("  <link rel=\"icon\" type=\"image/png\" sizes=\"32x32\" href=\"{{ \"/assets/favicon-32.png\" | relative_url }}\">\n")
	out.WriteString("  <link rel=\"apple-touch-icon\" href=\"{{ \"/assets/apple-touch-icon.png\" | relative_url }}\">\n")
	out.WriteString("  <link rel=\"stylesheet\" href=\"{{ \"/assets/main.css\" | relative_url }}\">\n")
	out.WriteString("  {%- feed_meta -%}\n")
	out.WriteString("  {%- if jekyll.environment == 'production' and site.google_analytics -%}\n")
	out.WriteString("    {%- include google-analytics.html -%}\n")
	out.WriteString("  {%- endif -%}\n")
	out.WriteString("</head>\n")
	return []byte(out.String())
}
