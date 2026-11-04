package api

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

func TestMessageViewRendersBodyFieldsAndHidesRawHTML(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.messages.view = MessageView{
		RowID: 7, Revision: 3, StoreKind: mail.StorePrimary,
		Message:    &MessageWire{ID: "synthetic-1", Subject: "synthetic", Body: "plain body"},
		BodyFormat: "html", BodyHTML: `<p>safe <b>fragment</b></p>`, BodyCSS: `body{background:#fff;}`,
		BodyHTMLTruncated: true, BodyRemoteImages: 2, BodyBlockedImages: 2,
		BodyInlineImages: 1, BodyMissingInline: 0,
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/7?for=label"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if body["body_format"] != "html" || body["body_html"] != `<p>safe <b>fragment</b></p>` ||
		body["body_css"] != `body{background:#fff;}` ||
		body["body_html_truncated"] != true || body["body_remote_images"] != float64(2) ||
		body["body_blocked_images"] != float64(2) || body["body_inline_images"] != float64(1) ||
		body["body_missing_inline"] != float64(0) {
		t.Fatalf("body fields %#v", body)
	}
	msg, _ := body["message"].(map[string]any)
	// The HTML fragment is the display body; the duplicate plain text is
	// not on the wire.
	if _, ok := msg["body"]; ok {
		t.Fatalf("the HTML view duplicated its plain body: %#v", msg)
	}
	if strings.Contains(string(data), "stored raw html") || strings.Contains(string(data), "<script>") {
		t.Fatalf("raw html crossed the API: %s", data)
	}
}

// A plain message keeps its canonical text: the HTML fragment is a display
// rendering, not a replacement for the stored body.
func TestPlainMessageKeepsItsCanonicalBody(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.messages.view = MessageView{RowID: 2, Revision: 1, Message: &MessageWire{ID: "m2", Body: "plain"},
		BodyFormat: "text", BodyHTML: `<pre class="mail-text" dir="auto">plain</pre>`}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/2?for=label"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	msg, _ := body["message"].(map[string]any)
	if msg["body"] != "plain" {
		t.Fatalf("the plain body was dropped: %#v", msg)
	}
}

func TestMessageAttachmentServesOnlyItsStoredImage(t *testing.T) {
	srv, ts, w := newTestServer(t)
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	w.messages.asset = AttachmentAsset{Position: 1, Name: "logo.png", ContentType: "image/png",
		Size: int64(len(png)), Inline: true, SHA256: "abc123", Bytes: png}
	status, header, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/7/attachments/1?v=abc123"})
	mustStatus(t, status, 200, data)
	if header.Get("Content-Type") != "image/png" || header.Get("Content-Disposition") != "inline" ||
		header.Get("Cache-Control") != "private, max-age=31536000, immutable" ||
		!strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("asset headers %v", header)
	}
	if string(data) != string(png) || w.messages.assetCalls != 1 || w.messages.lastAttachment != 1 {
		t.Fatalf("asset bytes or call %q %+v", data, w.messages)
	}
	// A guessed variant cannot turn an old address into a cache hit.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/7/attachments/1?v=other"})
	mustStatus(t, status, 404, data)
	// A missing blob is a stable, distinct answer, not a server failure.
	w.messages.assetErr = store.ErrAssetUnavailable
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/7/attachments/1"})
	mustStatus(t, status, 404, data)
	if code := errorCode(t, data); code != "asset_unavailable" {
		t.Fatalf("code %q", code)
	}
	// Only the four safe raster types leave the API.
	w.messages.assetErr = nil
	w.messages.asset.ContentType = "image/svg+xml"
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/7/attachments/1"})
	mustStatus(t, status, 415, data)
	if code := errorCode(t, data); code != "unsupported_asset" {
		t.Fatalf("code %q", code)
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/7/attachments/x"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_attachment" {
		t.Fatalf("code %q", code)
	}
}

func TestMessageBackfillStartsAJob(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.messages.backfillInfo = jobs.Info{ID: "job-html", Kind: syncer.BackfillJobKind, Name: "outlook", Status: runstate.Running}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/actions/backfill_html", body: `{"source":"outlook"}`})
	mustStatus(t, status, 202, data)
	body := decodeMap(t, data)
	job, _ := body["job"].(map[string]any)
	if job["id"] != "job-html" || job["kind"] != syncer.BackfillJobKind {
		t.Fatalf("job %#v", job)
	}
	if w.messages.backfillCalls != 1 || w.messages.backfillSource != "outlook" {
		t.Fatalf("service calls %d source %q", w.messages.backfillCalls, w.messages.backfillSource)
	}
	// An empty body is valid: the service picks the only source or refuses.
	if status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/actions/backfill_html", body: `{}`}); status != 202 {
		t.Fatalf("empty request: %d %s", status, data)
	}
}

func TestMessageBackfillBusyAndMethod(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.messages.backfillErr = syncer.ErrBackfillBusy
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/actions/backfill_html", body: `{}`})
	mustStatus(t, status, 409, data)
	if code := errorCode(t, data); code != "busy" {
		t.Fatalf("code %q", code)
	}
	status, header, data := do(t, ts, srv, request{method: "GET", path: "/api/actions/backfill_html"})
	mustStatus(t, status, 405, data)
	if header.Get("Allow") != "POST" {
		t.Fatalf("Allow %q", header.Get("Allow"))
	}
}

func TestLabelReadKeepsBodyFieldsBehindAClosedGate(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = lockedGate()
	w.messages.view = MessageView{RowID: 1, Revision: 1, Message: &MessageWire{ID: "m1", Body: "plain"},
		BodyFormat: "text", BodyHTML: `<pre class="mail-text" dir="auto">plain</pre>`}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=label&question=category&limit=20"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if body["body_format"] != "text" || !strings.Contains(body["body_html"].(string), "mail-text") {
		t.Fatalf("pre-gate body %#v", body)
	}
}

func TestSenderViewDistinguishesFromSenderAndAuthentication(t *testing.T) {
	m := &mail.Message{
		FolderRole: mail.RoleInbox, SenderName: "Assistant", SenderAddress: "assistant@example.org",
		SentRepresentingAddress: "boss@example.com",
		Headers: "Authentication-Results: mx.example.net; spf=pass smtp.mailfrom=example.com; dkim=fail header.d=example.com; dmarc=pass\n" +
			"Reply-To: Replies <replies@example.org>\nReturn-Path: <bounce@example.org>",
	}
	v := SenderOf(m)
	if v.FromAddress != "boss@example.com" || v.SenderAddress != "assistant@example.org" || !v.OnBehalfOf {
		t.Fatalf("identities %+v", v)
	}
	if v.ReplyTo != "Replies <replies@example.org>" || v.ReturnPath != "<bounce@example.org>" {
		t.Fatalf("headers %+v", v)
	}
	if !v.Authentication.Reported || v.Authentication.Source != "mx.example.net" ||
		v.Authentication.SPF != "pass" || v.Authentication.DKIM != "fail" || v.Authentication.DMARC != "pass" {
		t.Fatalf("authentication %+v", v.Authentication)
	}

	// The same two identities on the owner's sent copy are not an
	// on-behalf-of label, and a draft is not received mail either.
	sent := *m
	sent.FolderRole = mail.RoleSent
	if got := SenderOf(&sent); got.OnBehalfOf {
		t.Fatalf("sent item labelled on behalf: %+v", got)
	}
	draft := *m
	draft.FolderRole = mail.RoleDrafts
	if got := SenderOf(&draft); got.OnBehalfOf {
		t.Fatalf("draft labelled on behalf: %+v", got)
	}
}

func TestSenderViewNormalSenderMissingAuthAndFallbacks(t *testing.T) {
	m := &mail.Message{FolderRole: mail.RoleInbox, SenderName: "Élodie", SenderAddress: "Elodie@Example.org",
		SentRepresentingAddress: " elodie@example.org "}
	v := SenderOf(m)
	if v.FromAddress != "elodie@example.org" || v.SenderAddress != "Elodie@Example.org" || v.OnBehalfOf {
		t.Fatalf("normal sender %+v", v)
	}
	if v.Authentication.Reported || v.Authentication.Source != "" || v.Authentication.SPF != "" ||
		v.Authentication.DKIM != "" || v.Authentication.DMARC != "" {
		t.Fatalf("absent auth must make no claim: %+v", v.Authentication)
	}
	if v.ReplyTo != "" || v.ReturnPath != "" {
		t.Fatalf("absent headers became claims: %+v", v)
	}

	// No representing address: From falls back to the actual sender.
	fallback := &mail.Message{FolderRole: mail.RoleInbox, SenderAddress: "elodie@example.org"}
	if got := SenderOf(fallback); got.FromAddress != "elodie@example.org" || got.OnBehalfOf {
		t.Fatalf("fallback %+v", got)
	}

	// A header block cut inside its last line never reads the fragment as
	// a Reply-To, Return-Path or authentication claim.
	cut := &mail.Message{FolderRole: mail.RoleInbox, SenderAddress: "elodie@example.org",
		Headers:     "Reply-To: replies@example.org\nReturn-Path: <bounce@example.org",
		Alterations: []mail.Alteration{{Code: mail.AltHeadersTruncated}}}
	if got := SenderOf(cut); got.ReplyTo != "replies@example.org" || got.ReturnPath != "" {
		t.Fatalf("cut block leaked a header fragment: %+v", got)
	}
	cutAuth := &mail.Message{FolderRole: mail.RoleInbox, SenderAddress: "elodie@example.org",
		Headers:     "Reply-To: replies@example.org\nAuthentication-Results: mx.example.net; spf=pa",
		Alterations: []mail.Alteration{{Code: mail.AltHeadersTruncated}}}
	if got := SenderOf(cutAuth); got.Authentication.Reported {
		t.Fatalf("cut block leaked an authentication fragment: %+v", got.Authentication)
	}
}

func TestMessageViewCarriesTheSenderContract(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.messages.view = MessageView{
		RowID: 4, Revision: 1, Message: &MessageWire{ID: "m4", Subject: "synthetic"},
		Sender: SenderView{
			FromAddress: "boss@example.com", SenderAddress: "assistant@example.org", OnBehalfOf: true,
			ReplyTo: "replies@example.org", ReturnPath: "bounce@example.org",
			Authentication: SenderAuthenticationView{Reported: true, SPF: "pass", Source: "mx.example.net"},
		},
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/4"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	sender, _ := body["sender"].(map[string]any)
	if sender["from_address"] != "boss@example.com" || sender["sender_address"] != "assistant@example.org" ||
		sender["on_behalf_of"] != true || sender["reply_to"] != "replies@example.org" ||
		sender["return_path"] != "bounce@example.org" {
		t.Fatalf("sender %#v", sender)
	}
	auth, _ := sender["authentication"].(map[string]any)
	if auth["reported"] != true || auth["spf"] != "pass" || auth["source"] != "mx.example.net" {
		t.Fatalf("authentication %#v", auth)
	}
	if _, ok := auth["verified"]; ok {
		t.Fatalf("authentication claims verification: %#v", auth)
	}
	if _, ok := body["message"]; !ok {
		t.Fatalf("the stored message was removed: %s", data)
	}
}
