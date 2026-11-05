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

// headScript is the one inline script. It runs in the head of every page,
// before paint, and makes no network request. It owns the two stored
// preferences: the theme choice is applied before the first paint, and the
// language destinations come from the alternate links above it, so the script
// contains no path and no URL. The current language comes from the html
// element, which the layout fills from the page's front matter.
func headScript() string {
	return fmt.Sprintf(`(function () {
  "use strict";
  var KEY = %q;
  var GUARD = %q;
  var THEME = %q;
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
  function storedTheme() {
    try {
      var value = localStorage.getItem(THEME);
      return value === "light" || value === "dark" ? value : "";
    } catch (error) {
      return "";
    }
  }
  function rememberTheme(value) {
    try {
      if (value === "light" || value === "dark") {
        localStorage.setItem(THEME, value);
      } else {
        localStorage.removeItem(THEME);
      }
    } catch (error) {
      return;
    }
  }
  // The attribute is set before paint, so an explicit choice never flashes
  // the other palette. An absent choice and the system button both resolve to
  // "auto", which the stylesheet maps with the system preference.
  function applyTheme(value) {
    var stored = value === "light" || value === "dark" ? value : "";
    document.documentElement.dataset.theme = stored || "auto";
    var buttons = document.querySelectorAll("[data-theme-choice]");
    for (var index = 0; index < buttons.length; index += 1) {
      var button = buttons[index];
      var choice = button.getAttribute("data-theme-choice");
      var pressed = choice === "light" || choice === "dark" ? choice === stored : stored === "";
      button.setAttribute("aria-pressed", pressed ? "true" : "false");
    }
  }
  applyTheme(storedTheme());
  document.addEventListener("click", function (event) {
    if (!event.target || !event.target.closest) {
      return;
    }
    var choice = event.target.closest("[data-theme-choice]");
    if (choice) {
      var picked = choice.getAttribute("data-theme-choice");
      rememberTheme(picked === "light" || picked === "dark" ? picked : "");
      applyTheme(picked);
      return;
    }
    var link = event.target.closest("a[data-lang-switch]");
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
  // The control sits below this script. Once it is parsed, sync its pressed
  // state so a stored choice is shown on the buttons themselves.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () {
      applyTheme(storedTheme());
    });
  } else {
    applyTheme(storedTheme());
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
})();`, LangStorageKey, LangGuardKey, ThemeStorageKey)
}

// Head renders docs/_includes/head.html, the site override of the bundled
// theme's head. The theme is the version pinned by github-pages, so the base
// tags are stable; the generated block adds the two alternate links on the
// landing pages and the one inline script on every page, because the theme
// choice has to be applied before paint wherever the visitor lands.
func (s Site) Head() []byte {
	var out strings.Builder
	out.WriteString(generatedHeader)
	out.WriteString("\n")
	out.WriteString("<!-- Overrides the theme head. Keep in step with the pinned theme. -->\n")
	out.WriteString("<head>\n")
	out.WriteString("  <meta charset=\"utf-8\">\n")
	out.WriteString("  <meta http-equiv=\"X-UA-Compatible\" content=\"IE=edge\">\n")
	out.WriteString("  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("  <meta name=\"color-scheme\" content=\"light dark\">\n")
	out.WriteString("  {%- if page.language_switch -%}\n")
	fmt.Fprintf(&out, "  <link rel=\"alternate\" hreflang=\"en\" href=\"%s\">\n", html.EscapeString(s.landingURL("en")))
	fmt.Fprintf(&out, "  <link rel=\"alternate\" hreflang=\"fr\" href=\"%s\">\n", html.EscapeString(s.landingURL("fr")))
	out.WriteString("  {%- endif -%}\n")
	out.WriteString("  <script>\n")
	out.WriteString(headScript())
	out.WriteString("\n  </script>\n")
	out.WriteString("  {%- seo -%}\n")
	out.WriteString("  <!-- Every page declares the same icon. The SVG is the brand mark, and the\n")
	out.WriteString("       PNG pair covers browsers and touch screens that ignore SVG favicons. -->\n")
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
