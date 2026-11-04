package rfc5322

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestMultipartAlternativeKeepsHTMLBesideThePlainStub(t *testing.T) {
	m := one(t, hdr+"Subject: alternative\nMIME-Version: 1.0\n"+
		"Content-Type: multipart/alternative; boundary=b\n\n"+
		"--b\nContent-Type: text/plain; charset=utf-8\n\nView this message in HTML.\n"+
		"--b\nContent-Type: text/html; charset=utf-8\n\n<p>Real <b>message</b>.</p>\n--b--\n")
	if strings.TrimSpace(m.Body) != "View this message in HTML." {
		t.Fatalf("plain body is the learning body: %q", m.Body)
	}
	if !m.BodyHTMLKnown || strings.TrimSpace(m.BodyHTML) != "<p>Real <b>message</b>.</p>" || m.BodyHTMLTruncated {
		t.Fatalf("html alternative lost: %+v", m)
	}
}

func TestHTMLOnlyWithoutHeaderCharsetUsesMeta(t *testing.T) {
	m := one(t, hdr+"Subject: meta\nMIME-Version: 1.0\n"+
		"Content-Type: text/html\n\n"+`<html><head><meta charset="windows-1252"></head><body><p>caf`+"\xe9"+`</p></body></html>`+"\n")
	if !strings.Contains(m.BodyHTML, "café") {
		t.Fatalf("meta charset ignored: %q", m.BodyHTML)
	}
	if !m.BodyHTMLKnown {
		t.Fatalf("html state not declared: %+v", m)
	}
}

func TestHTMLBOMIsDecoded(t *testing.T) {
	m := one(t, hdr+"Subject: bom\nMIME-Version: 1.0\nContent-Type: text/html\n\n"+
		"\xef\xbb\xbf"+`<p>Élodie</p>`+"\n")
	if !strings.Contains(m.BodyHTML, "Élodie") {
		t.Fatalf("BOM not used: %q", m.BodyHTML)
	}
}

func TestInlineCIDIsKeptForTheRenderer(t *testing.T) {
	m := one(t, hdr+"Subject: inline\nMIME-Version: 1.0\n"+
		"Content-Type: multipart/related; boundary=b\n\n"+
		"--b\nContent-Type: text/html; charset=utf-8\n\n"+
		`<p><img src="cid:logo@example.test" alt="Signature"></p>`+"\n"+
		"--b\nContent-Type: image/png; name=logo.png\nContent-Transfer-Encoding: base64\nContent-ID: <logo@example.test>\nContent-Disposition: inline\n\n"+
		"iVBORw0KGgo=\n--b--\n")
	if !strings.Contains(m.BodyHTML, `src="cid:logo@example.test"`) {
		t.Fatalf("cid reference lost: %q", m.BodyHTML)
	}
	if len(m.Attachments) != 1 || !m.Attachments[0].Inline || m.Attachments[0].Name != "logo.png" {
		t.Fatalf("inline part metadata: %+v", m.Attachments)
	}
	att := m.Attachments[0]
	if att.ContentID != "logo@example.test" {
		t.Fatalf("content id lost: %+v", att)
	}
	if want := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}; !bytes.Equal(att.Data, want) {
		t.Fatalf("inline bytes lost: %x", att.Data)
	}
	if att.Size != int64(len(att.Data)) || att.ContentType != "image/png" {
		t.Fatalf("inline identity: %+v", att)
	}
	if m.HasAttachments {
		t.Fatal("an inline-only part is not an attachment badge")
	}
}

func TestInlineImageBudgetDropsBytesButKeepsMetadata(t *testing.T) {
	raw := hdr + "Subject: inline budget\nMIME-Version: 1.0\n" +
		"Content-Type: multipart/related; boundary=b\n\n" +
		"--b\nContent-Type: text/html; charset=utf-8\n\n" +
		`<p><img src="cid:logo@example.test"></p>` + "\n" +
		"--b\nContent-Type: image/png; name=logo.png\nContent-Transfer-Encoding: base64\nContent-ID: <logo@example.test>\nContent-Disposition: inline\n\n" +
		"iVBORw0KGgo=\n--b--\n"
	m := oneWith(t, raw, map[string]any{"mail.inline_assets.max_part_bytes": 4})
	if len(m.Attachments) != 1 {
		t.Fatalf("metadata lost: %+v", m.Attachments)
	}
	if len(m.Attachments[0].Data) != 0 || m.Attachments[0].Size != 8 {
		t.Fatalf("oversized part bytes kept: %+v", m.Attachments[0])
	}
	if !altered(m, mail.AltInlineAssetDropped) {
		t.Fatalf("dropped asset not declared: %+v", m.Alterations)
	}
}

func TestContentLocationIsCapturedForRelativeImages(t *testing.T) {
	raw := hdr + "Subject: location\nMIME-Version: 1.0\n" +
		"Content-Type: multipart/related; boundary=b\n\n" +
		"--b\nContent-Type: text/html; charset=utf-8\n\n" +
		`<p><img src="images/hero.jpg"></p>` + "\n" +
		"--b\nContent-Type: image/jpeg; name=hero.jpg\nContent-Transfer-Encoding: base64\nContent-Location: images/hero.jpg\n\n" +
		"/9j/2Q==\n--b--\n"
	m := one(t, raw)
	if len(m.Attachments) != 1 || m.Attachments[0].ContentLocation != "images/hero.jpg" ||
		!m.Attachments[0].Inline || len(m.Attachments[0].Data) == 0 {
		t.Fatalf("content location lost: %+v", m.Attachments)
	}
}

func TestHTMLIsTruncatedAtATokenBoundary(t *testing.T) {
	m := oneWith(t, hdr+"Subject: long\nMIME-Version: 1.0\nContent-Type: text/html\n\n<p>one</p><p>two</p>\n",
		map[string]any{"mail.body.html_max_chars": 12})
	if !m.BodyHTMLTruncated || m.BodyHTML != "<p>one</p>" {
		t.Fatalf("truncation %q, %v", m.BodyHTML, m.BodyHTMLTruncated)
	}
	if !altered(m, mail.AltBodyHTMLTruncated) {
		t.Fatalf("truncation not declared: %+v", m.Alterations)
	}
	if !m.BodyHTMLKnown {
		t.Fatal("truncated html is still a known html state")
	}
}

func TestPlainOnlyDeclaresAKnownEmptyHTML(t *testing.T) {
	m := one(t, hdr+"Subject: plain\nContent-Type: text/plain\n\nJust text.\n")
	// The connector did look: a plain-only message is not a backfill candidate.
	if !m.BodyHTMLKnown || m.BodyHTML != "" || m.BodyHTMLTruncated {
		t.Fatalf("plain-only %+v", m)
	}
}

func TestHTMLEntitiesAndRTLSurvive(t *testing.T) {
	m := one(t, hdr+"Subject: rtl\nMIME-Version: 1.0\nContent-Type: text/html; charset=utf-8\n\n"+
		`<p dir="rtl" lang="ar">مرحبا &amp; أهلا</p><p>Bonjour éàçù</p>`+"\n")
	if !strings.Contains(m.BodyHTML, `dir="rtl"`) || !strings.Contains(m.BodyHTML, "مرحبا &amp; أهلا") || !strings.Contains(m.BodyHTML, "éàçù") {
		t.Fatalf("RTL/French html changed: %q", m.BodyHTML)
	}
}
