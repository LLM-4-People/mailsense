package site

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// scriptBetween returns the body of the one inline script in a document.
func scriptBetween(t *testing.T, document string) string {
	t.Helper()
	start := strings.Index(document, "<script>")
	end := strings.Index(document, "</script>")
	if start < 0 || end < 0 || end < start {
		t.Fatal("document has no inline script")
	}
	if strings.Contains(document[start:end], "src=") {
		t.Fatal("the inline script has a src attribute")
	}
	return document[start+len("<script>") : end]
}

// landingHead joins the generated landing front matter and the generated head
// override. Jekyll applies the head to a page only when language_switch is set,
// so the test checks both halves of that one mechanism.
func landingHead(t *testing.T, value Site, lang string) string {
	t.Helper()
	landing := string(value.Landing(lang))
	head := string(value.Head())
	for _, want := range []string{
		"language_switch: true\n",
		fmt.Sprintf("lang: %q\n", lang),
	} {
		if !strings.Contains(landing, want) {
			t.Fatalf("%s landing front matter is missing %q", lang, want)
		}
	}
	if !strings.Contains(head, "{%- if page.language_switch -%}") {
		t.Fatal("the head override does not gate the language block on the landing flag")
	}
	return landing + head
}

// TestLandingPagesCarryTheLanguageMetadata is the public contract: both
// landings opt in, the head carries both alternates, and the html lang comes
// from the page front matter the theme reads.
func TestLandingPagesCarryTheLanguageMetadata(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	head := string(value.Head())
	for _, lang := range []string{"en", "fr"} {
		url := value.landingURL(lang)
		if url == "" {
			t.Fatalf("the page table has no landing URL for %s", lang)
		}
		document := landingHead(t, value, lang)
		alternate := fmt.Sprintf(`<link rel="alternate" hreflang="%s" href="%s">`, lang, url)
		if !strings.Contains(document, alternate) {
			t.Errorf("%s landing does not carry %s", lang, alternate)
		}
	}
	scriptBetween(t, head)
	if !strings.Contains(head, "document.documentElement.lang") {
		t.Error("the script does not read the html lang the theme emits")
	}
	config := string(value.Config())
	for _, want := range []string{
		"defaults:\n",
		"    values:\n      lang: \"en\"\n",
		"  - scope:\n      path: \"fr\"\n    values:\n      lang: \"fr\"\n",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("the generated config does not set the html lang defaults: missing %q", want)
		}
	}
}

// TestLanguageScriptMakesNoRequest proves the script is self-contained: no
// external URL, no external script, no fetch and no import.
func TestLanguageScriptMakesNoRequest(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	head := string(value.Head())
	if got := strings.Count(head, "<script"); got != 1 {
		t.Errorf("the head has %d script tags, want one inline script", got)
	}
	if strings.Contains(strings.ToLower(head), "<script src") {
		t.Error("the head loads an external script")
	}
	script := scriptBetween(t, head)
	for _, banned := range []string{"http://", "https://", "fetch(", "XMLHttpRequest", "import(", ".src", "<script"} {
		if strings.Contains(script, banned) {
			t.Errorf("the language script names %q", banned)
		}
	}
}

// TestLanguageRedirectTargetComesFromThePageTable derives the two site paths
// independently from the page table and the baseurl, then proves the script
// contains neither path and reads the generated alternate instead.
func TestLanguageRedirectTargetComesFromThePageTable(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	var index Page
	for _, page := range value.Pages() {
		if page.Key == "index" {
			index = page
			break
		}
	}
	if index.Key == "" {
		t.Fatal("the page table has no index page")
	}
	expected := func(file string) string {
		rel := strings.TrimPrefix(file, "docs/")
		rel = strings.TrimSuffix(rel, "index.md")
		return strings.TrimSuffix(value.BaseURL, "/") + "/" + rel
	}
	enPath, frPath := expected(index.EN), expected(index.FR)
	if enPath == frPath {
		t.Fatalf("both landings resolve to %q", enPath)
	}
	if got := value.landingPath("en"); got != enPath {
		t.Errorf("landingPath(en) = %q, want %q", got, enPath)
	}
	if got := value.landingPath("fr"); got != frPath {
		t.Errorf("landingPath(fr) = %q, want %q", got, frPath)
	}
	for _, lang := range []string{"en", "fr"} {
		url := strings.TrimSuffix(value.siteOrigin(), "/") + value.landingPath(lang)
		if got := value.landingURL(lang); got != url {
			t.Errorf("landingURL(%s) = %q, want %q", lang, got, url)
		}
	}
	head := string(value.Head())
	for _, path := range []string{enPath, frPath} {
		if !strings.Contains(head, `href="`+strings.TrimSuffix(value.siteOrigin(), "/")+path+`"`) {
			t.Errorf("the head has no alternate for %s", path)
		}
	}
	script := scriptBetween(t, head)
	for _, path := range []string{enPath, frPath} {
		if strings.Contains(script, path) {
			t.Errorf("the script hard-codes the redirect path %q", path)
		}
	}
	for _, want := range []string{`getAttribute("href")`, "new URL("} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not derive its target from the alternate link: missing %q", want)
		}
	}
}

// TestLanguageStorageKeyHasOneWriterAndOneReader proves the preference key has
// a single literal and one owner, and that the guard is a separate session key.
func TestLanguageStorageKeyHasOneWriterAndOneReader(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	script := scriptBetween(t, string(value.Head()))
	if got := strings.Count(script, fmt.Sprintf("%q", LangStorageKey)); got != 1 {
		t.Errorf("the script names the preference key %d times, want exactly one", got)
	}
	if !strings.Contains(script, fmt.Sprintf("var KEY = %q;", LangStorageKey)) {
		t.Errorf("the script does not initialise KEY from LangStorageKey")
	}
	if got := strings.Count(script, "localStorage.getItem(KEY)"); got != 1 {
		t.Errorf("the preference has %d readers, want one", got)
	}
	if got := strings.Count(script, "localStorage.setItem(KEY, value)"); got != 1 {
		t.Errorf("the preference has %d writers, want one", got)
	}
	storeCall := regexp.MustCompile(`localStorage\.(?:getItem|setItem)\(([^)]*)`)
	for _, match := range storeCall.FindAllStringSubmatch(script, -1) {
		if !strings.HasPrefix(match[1], "KEY") {
			t.Errorf("localStorage is used with a second key: %s", match[0])
		}
	}
	if !strings.Contains(script, fmt.Sprintf("var GUARD = %q;", LangGuardKey)) {
		t.Error("the one-shot guard does not use LangGuardKey")
	}
}

// TestLanguageRedirectIsGuardedAndOneShot proves the redirect cannot loop and
// that an explicit switch clears the guard, so a later visit honors the
// visitor's choice instead of a stale session flag.
func TestLanguageRedirectIsGuardedAndOneShot(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	script := scriptBetween(t, string(value.Head()))
	for _, want := range []string{
		"sessionStorage.getItem(GUARD)",
		"sessionStorage.setItem(GUARD, String(Date.now()))",
		"sessionStorage.removeItem(GUARD)",
		"Date.now() - at < 5000",
		"!guarded()",
		"location.replace(target)",
		"navigator.languages",
		"navigator.language",
		`"a[data-lang-switch]"`,
		"addEventListener(\"click\"",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the language script is missing %q", want)
		}
	}
	if got := strings.Count(script, "location.replace"); got != 1 {
		t.Errorf("the script redirects on %d code paths, want one guarded path", got)
	}
	if !strings.Contains(script, "navigator.languages") {
		t.Error("the script does not try navigator.languages")
	}
	if languages, fallback := strings.Index(script, "navigator.languages"), strings.Index(script, "|| navigator.language"); languages < 0 || fallback < 0 || languages > fallback {
		t.Error("the script does not try navigator.languages before navigator.language")
	}
}

// TestGeneratedNavMarksTheLanguageSwitch proves every generated navigation,
// in both trees, carries the marked switch and a working link without
// JavaScript.
func TestGeneratedNavMarksTheLanguageSwitch(t *testing.T) {
	value, err := Load(root(t))
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"docs/index.md", "docs/fr/index.md"}
	files = append(files, value.ManagedPaths()...)
	anchor := regexp.MustCompile(`<a href="([^"]+)"[^>]*data-lang-switch="(en|fr)"[^>]*>`)
	checked := 0
	for _, name := range files {
		body := readDoc(t, root(t), name)
		match := anchor.FindStringSubmatch(body)
		if match == nil {
			t.Errorf("%s has no marked language switch", name)
			continue
		}
		checked++
		if strings.HasSuffix(match[1], ".md") {
			t.Errorf("%s links its language switch to a Markdown file: %s", name, match[1])
		}
	}
	if checked < len(files) {
		t.Fatalf("only %d of %d pages carry a marked language switch", checked, len(files))
	}
}
