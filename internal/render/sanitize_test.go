package render

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewsletterKeepsStructureAndDropsFetchingCSS(t *testing.T) {
	f, err := build(fixture(t, "newsletter.html"), options{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.html
	for _, want := range []string{
		"<h1>Quarterly update</h1>",
		"<strong>world</strong>",
		`<table data-mail-table="data">`,
		"<th>Quarter</th>",
		"<td>A1</td>",
		"<blockquote>",
		`<a href="https://example.test/report?q=1" target="_blank" rel="noopener noreferrer nofollow">link to the report</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized newsletter lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"<style", "style=", "url(", "tracker.example.test", "tracking pixel", "<script", "alert(", `width="1"`, `height="1"`, "<img"} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitized newsletter kept %q:\n%s", bad, got)
		}
	}
	if strings.Contains(f.css, "url(") || strings.Contains(f.css, "tracker") || strings.Contains(f.css, "background") {
		t.Fatalf("a url()-only stylesheet must compile without it: %q", f.css)
	}
	if f.stats.RemoteImages != 1 || f.stats.BlockedImages != 1 {
		t.Fatalf("stats %+v", f.stats)
	}
	if a, b := strings.Index(got, "A1"), strings.Index(got, "A2"); a < 0 || b < a {
		t.Fatalf("table order lost: %q", got)
	}
}

func TestSenderPresentationIsCompiledNotInlined(t *testing.T) {
	raw := `<style>
@media (max-width: 600px) { .hero { width: 100% } }
.cta { background-color: #1e5a46; color: #fff; padding: 10px 14px; border-radius: 4px }
.bad { position: fixed; background: url(https://tracker.example.test/x.png); color: expression(alert(1)) }
</style>
<table role="presentation" width="600" cellpadding="0" cellspacing="0">
<tr><td width="300" bgcolor="#f5f5f5" style="padding: 0; color: #333" class="hero">Hi</td></tr>
</table>
<p id="anchor" class="cta">Button</p>
<font face="Georgia" color="#333333" size="4">Legacy</font>`
	f, err := build(raw, options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<table data-mail-table="layout" width="600" cellpadding="0" cellspacing="0"`,
		`<td width="300" bgcolor="#f5f5f5" class="hero"`,
		`data-ms="`,
		`<p id="anchor" class="cta">Button</p>`,
		`<font color="#333333" face="Georgia" size="4">Legacy</font>`,
	} {
		if !strings.Contains(f.html, want) {
			t.Fatalf("fragment lacks %q:\n%s", want, f.html)
		}
	}
	for _, bad := range []string{"style=", "position", "url(", "tracker.example.test", "expression"} {
		if strings.Contains(f.html, bad) {
			t.Fatalf("fragment kept %q:\n%s", bad, f.html)
		}
	}
	for _, want := range []string{
		".cta{background-color:#1e5a46;color:#fff;padding:10px 14px;border-radius:4px;}",
		"@media (max-width: 600px){.hero{width:100%;}}",
		`[data-ms="`,
	} {
		if !strings.Contains(f.css, want) {
			t.Fatalf("compiled sheet lacks %q:\n%s", want, f.css)
		}
	}
	for _, bad := range []string{"position", "url(", "expression", "tracker.example.test"} {
		if strings.Contains(f.css, bad) {
			t.Fatalf("compiled sheet kept %q:\n%s", bad, f.css)
		}
	}
}

func TestPresentationAttributesAreBounded(t *testing.T) {
	f, err := build(`<table width="100000" border="3"><tr><td width="0" colspan="0" bgcolor="not a color">x</td></tr></table>`, options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.html, `width="100000"`) || strings.Contains(f.html, `colspan="0"`) ||
		strings.Contains(f.html, `bgcolor="not a color"`) {
		t.Fatalf("unbounded presentation kept: %s", f.html)
	}
	if !strings.Contains(f.html, `border="3"`) || !strings.Contains(f.html, `data-mail-table="data"`) {
		t.Fatalf("safe table presentation lost: %s", f.html)
	}
}

func TestMaliciousMarkupIsRemoved(t *testing.T) {
	got, _, err := sanitize(fixture(t, "malicious.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"<script", "</script", "onclick", "onerror", "style=", "<style", "javascript:", "JaVaScRiPt",
		"<iframe", "<object", "<embed", "<form", "<input", "<button", "<meta", "<base", "<svg", "<math",
		"<link", "data-x", "srcdoc", "url(", "alert(1)", "alert(2)", "alert(3)", "alert(4)", "alert(5)", "alert(6)",
		"vector", ">math<",
	} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitized output kept %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{
		"javascript link",
		"obfuscated link",
		"data link",
		"file link",
		`<p dir="rtl" lang="ar">مرحبا</p>`,
		`<p dir="auto" lang="fr">Bonjour, voici un texte français : éàçù.</p>`,
		"behind it.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized output lacks %q:\n%s", want, got)
		}
	}
}

func TestCIDImageResolvesToTheStoredAsset(t *testing.T) {
	attachments := []mail.Attachment{{
		Name: "logo.png", ContentType: "image/png", Inline: true,
		ContentID: "logo@example.test", SHA256: "abc123",
	}}
	f, err := build(`<p><img src="cid:logo@example.test" alt="signature logo" width="120"></p>`, options{attachments: attachments})
	if err != nil {
		t.Fatal(err)
	}
	got := f.html
	for _, want := range []string{
		`class="mail-inline-asset`, `data-inline="0"`, `data-inline-v="abc123"`,
		`aria-label="signature logo"`, "signature logo</span>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inline asset lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "cid:") || strings.Contains(got, "<img") {
		t.Fatalf("cid or image leaked: %q", got)
	}
	if f.stats.InlineImages != 1 || f.stats.MissingInline != 0 {
		t.Fatalf("stats %+v", f.stats)
	}
}

func TestInlineImageWithoutBytesKeepsAPlaceholder(t *testing.T) {
	attachments := []mail.Attachment{{
		Name: "logo.png", ContentType: "image/png", Inline: true,
		ContentID: "logo@example.test", // no SHA256: old mail, dropped part
	}}
	f, err := build(`<p><img src="cid:logo@example.test" alt="signature logo"></p>`, options{attachments: attachments})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.html, "Inline image not available: signature logo") ||
		!strings.Contains(f.html, "mail-image-inline") {
		t.Fatalf("no unavailable placeholder: %q", f.html)
	}
	if f.stats.MissingInline != 1 || f.stats.InlineImages != 0 {
		t.Fatalf("stats %+v", f.stats)
	}
}

func TestContentLocationResolvesToTheStoredAsset(t *testing.T) {
	attachments := []mail.Attachment{{
		Name: "hero.jpg", ContentType: "image/jpeg", Inline: true,
		ContentLocation: "images/hero.jpg", SHA256: "def456",
	}}
	f, err := build(`<img src="images/hero.jpg" alt="hero">`, options{attachments: attachments})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.html, `data-inline="0"`) || !strings.Contains(f.html, `data-inline-v="def456"`) {
		t.Fatalf("content-location not resolved: %q", f.html)
	}
}

func TestRemoteSrcsetIsCountedAndBlocked(t *testing.T) {
	got, stats, err := sanitize(`<img src="https://example.test/a.png" srcset="https://tracker.example.test/b.png 2x" alt="chart">`)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RemoteImages != 1 || stats.BlockedImages != 1 {
		t.Fatalf("stats %+v", stats)
	}
	if !strings.Contains(got, "Remote image blocked: chart") {
		t.Fatalf("placeholder missing: %q", got)
	}
	if strings.Contains(got, "example.test") {
		t.Fatalf("remote URL leaked: %q", got)
	}
}

func TestOnlySafeRasterDataImagesLoad(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="
	got, stats, err := sanitize(`<img src="` + png + `" alt="dot"><img src="/local.png" alt="local"><img src="data:image/svg+xml,<svg onload=alert(1)>" alt="data">`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `<img src="`+png+`" alt="dot">`) {
		t.Fatalf("safe raster data image lost: %q", got)
	}
	if strings.Contains(got, "local.png") || strings.Contains(got, "svg") || strings.Contains(got, "<svg") {
		t.Fatalf("unsafe image kept: %q", got)
	}
	if !strings.Contains(got, "Image not shown: local") || !strings.Contains(got, "Image not shown: data") {
		t.Fatalf("placeholders missing: %q", got)
	}
	if stats.RemoteImages != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestStylesheetDropsFetchingAndEscapingConstructs(t *testing.T) {
	raw := `@import url(https://tracker.example.test/x.css);
@font-face { font-family: Evil; src: url(https://tracker.example.test/f.woff2) }
@keyframes spin { from { opacity: 0 } to { opacity: 1 } }
a { color: #123456 !important; }
a:hover { color: #654321 }
a { background: url(https://tracker.example.test/b.png); }
body { position: fixed; top: 0; left: 0; transform: translateX(10px) }
.x { width: 999999px; height: 12px }
`
	got := sanitizeStylesheet(raw)
	for _, bad := range []string{"@import", "@font-face", "@keyframes", "url(", "position", "transform", "999999", "tracker", "!important"} {
		if strings.Contains(got, bad) {
			t.Fatalf("stylesheet kept %q: %q", bad, got)
		}
	}
	for _, want := range []string{"a{color:#123456;}", "a:hover{color:#654321;}", ".x{height:12px;}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stylesheet lacks %q: %q", want, got)
		}
	}
}

func TestBodyReturnsTheCompiledSheetAndInlineIdentity(t *testing.T) {
	res := Body(Input{
		HTMLKnown: true,
		HTML:      `<style>.cta{color:#1e5a46}</style><p class="cta"><img src="cid:logo@x" alt="logo"></p>`,
		Attachments: []mail.Attachment{{
			Name: "logo.png", ContentType: "image/png", Inline: true,
			ContentID: "logo@x", SHA256: "abc123",
		}},
		Plain: "fallback",
	})
	if res.Format != FormatHTML || !strings.Contains(res.CSS, ".cta{color:#1e5a46;}") ||
		!strings.Contains(res.HTML, `data-inline="0"`) || res.InlineImages != 1 || res.CSS == "" {
		t.Fatalf("compiled body %+v", res)
	}
}

func TestHTMLFallbackOnOversizedAndDeepBodies(t *testing.T) {
	huge := "<p>" + strings.Repeat("a", MaxRawBytes+1) + "</p>"
	if res := Body(Input{HTML: huge, HTMLKnown: true, Plain: "fallback body"}); res.Format != FormatText || res.HTML != Text("fallback body") || !res.Truncated {
		t.Fatalf("huge fallback %+v", res)
	}
	deep := strings.Repeat("<div>", MaxDepth+2) + "deep" + strings.Repeat("</div>", MaxDepth+2)
	if res := Body(Input{HTML: deep, HTMLKnown: true, Plain: "plain deepest"}); res.Format != FormatText || !res.Truncated {
		t.Fatalf("deep fallback %+v", res)
	}
	many := strings.Repeat("<b>x</b>", MaxNodes+1)
	if res := Body(Input{HTML: many, HTMLKnown: true, Plain: "plain many"}); res.Format != FormatText || !res.Truncated {
		t.Fatalf("node fallback %+v", res)
	}
}

func TestUnsafeSchemesNeverBecomeLinks(t *testing.T) {
	cases := []struct {
		raw  string
		ok   bool
		want string
	}{
		{"https://example.test/a", true, "https://example.test/a"},
		{"HTTP://EXAMPLE.TEST/a", true, "http://EXAMPLE.TEST/a"},
		{"mailto:someone@example.test?subject=Hello", true, "mailto:someone@example.test?subject=Hello"},
		{"javascript:alert(1)", false, ""},
		{"data:text/html,x", false, ""},
		{"file:///etc/passwd", false, ""},
		{"ftp://example.test/x", false, ""},
		{"smb://host/share", false, ""},
		{"//example.test/x", false, ""},
		{"/relative", false, ""},
		{"https://user:pass@example.test/", false, ""},
		{"https://example.test/a b", false, ""},
		{"https://example.test/\\@evil.test", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		got, ok := safeLink(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("safeLink(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDirAndLangAreValidated(t *testing.T) {
	got, _, err := sanitize(`<p dir="AUTO" lang="fr-CA" title="OK">x</p><p dir="evil" lang="fr CA" title="x" onclick="y">y</p>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `<p dir="auto" lang="fr-CA" title="OK">x</p>`) {
		t.Fatalf("valid attrs lost: %q", got)
	}
	if !strings.Contains(got, `<p title="x">y</p>`) {
		t.Fatalf("invalid attrs kept: %q", got)
	}
}

func TestUnbalancedMarkupIsRepaired(t *testing.T) {
	got, _, err := sanitize(`<div><p>one<p>two`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<div><p>one</p><p>two</p></div>") {
		t.Fatalf("parser did not repair: %q", got)
	}
}
