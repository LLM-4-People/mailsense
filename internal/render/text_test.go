package render

import (
	"strings"
	"testing"
)

func TestTextPreservesWhitespaceAndQuotes(t *testing.T) {
	got := Text(fixture(t, "quoted.txt"))
	for _, want := range []string{
		`<pre class="mail-text" dir="auto">`,
		"  This line is indented by two spaces.",
		"<span class=\"mail-quoted\">&gt; On Monday, someone wrote:</span>",
		"<span class=\"mail-quoted\">&gt;</span>",
		"<span class=\"mail-quoted\">&gt;   quoted indented line</span>",
		"Cordialement,\nSignature",
		`<a href="https://example.test/docs" target="_blank" rel="noopener noreferrer nofollow">https://example.test/docs</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("text render lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n\n") < 2 {
		t.Fatalf("blank lines lost: %q", got)
	}
	if !strings.Contains(got, "</a> that was not clickable before.") {
		t.Fatalf("sentence punctuation moved into the link: %q", got)
	}
}

func TestTextEscapesMarkup(t *testing.T) {
	got := Text("<script>alert(1)</script> & <b>bold?</b>")
	if strings.Contains(got, "<script") || strings.Contains(got, "<b>") {
		t.Fatalf("markup passed through: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;b&gt;bold?&lt;/b&gt;") {
		t.Fatalf("escaping wrong: %q", got)
	}
}

func TestTextLinkifiesOnlySafeSchemes(t *testing.T) {
	got := Text("See https://example.test/a(b). Mail me at mailto:someone@example.test, not javascript:alert(1).")
	if !strings.Contains(got, `href="https://example.test/a(b)"`) {
		t.Fatalf("balanced parentheses lost: %q", got)
	}
	if !strings.Contains(got, `href="mailto:someone@example.test"`) {
		t.Fatalf("mailto not linked: %q", got)
	}
	if strings.Contains(got, `href="javascript`) || strings.Contains(got, `href="mailto:alert`) {
		t.Fatalf("unsafe link: %q", got)
	}
}

func TestTextRTLLeavesContentIntact(t *testing.T) {
	got := Text(fixture(t, "rtl-fr.txt"))
	if !strings.Contains(got, "مرحبا بالعالم") || !strings.Contains(got, "שלום עולם") || !strings.Contains(got, "éàçù") {
		t.Fatalf("RTL/French text changed: %q", got)
	}
	if !strings.HasPrefix(got, `<pre class="mail-text" dir="auto">`) {
		t.Fatalf("no dir=auto: %q", got)
	}
}

func TestTextEmptyAndLarge(t *testing.T) {
	if got := Text("   \n\t\n"); got != "" {
		t.Fatalf("blank body rendered %q", got)
	}
	large := strings.Repeat("a long line with a URL https://example.test/x and more words\n", 1400)
	got := Text(large)
	if !strings.HasPrefix(got, `<pre class="mail-text"`) || !strings.HasSuffix(got, `</pre>`) {
		t.Fatalf("large render truncated its wrapper")
	}
	if strings.Count(got, `https://example.test/x`) != 2800 {
		t.Fatalf("large render lost links")
	}
}

func TestBodyChoosesHTMLOnlyWhenItIsUsable(t *testing.T) {
	html := Body(Input{HTML: "<p>rich <b>body</b></p>", HTMLKnown: true, Plain: "plain body"})
	if html.Format != FormatHTML || !strings.Contains(html.HTML, "<b>body</b>") {
		t.Fatalf("html: %+v", html)
	}
	text := Body(Input{HTML: "not html but plain", HTMLKnown: false, Plain: "plain body"})
	if text.Format != FormatText || !strings.Contains(text.HTML, "plain body") {
		t.Fatalf("text: %+v", text)
	}
	empty := Body(Input{HTML: "", HTMLKnown: true, Plain: "plain body"})
	if empty.Format != FormatText || !strings.Contains(empty.HTML, "plain body") || empty.Truncated {
		t.Fatalf("empty html: %+v", empty)
	}
	// A stored HTML body can be empty and still recorded as cut (a very
	// small cap, or markup that leaves only whitespace); the badge must not
	// disappear with the fallback.
	cutEmpty := Body(Input{HTML: "", HTMLKnown: true, HTMLTruncated: true, Plain: "plain body"})
	if cutEmpty.Format != FormatText || !cutEmpty.Truncated || !strings.Contains(cutEmpty.HTML, "plain body") {
		t.Fatalf("truncated empty html: %+v", cutEmpty)
	}
	nothing := Body(Input{HTML: "", HTMLKnown: true, Plain: ""})
	if nothing.Format != FormatText || nothing.HTML != "" {
		t.Fatalf("empty body: %+v", nothing)
	}
	scriptOnly := Body(Input{HTML: "<script>alert(1)</script>", HTMLKnown: true, Plain: "plain body"})
	if scriptOnly.Format != FormatText || scriptOnly.Truncated || !strings.Contains(scriptOnly.HTML, "plain body") {
		t.Fatalf("script-only html: %+v", scriptOnly)
	}
}
