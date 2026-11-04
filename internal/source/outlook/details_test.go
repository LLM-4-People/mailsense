package outlook

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func syncedInbox(t *testing.T, o *fakeOutlook, overrides map[string]any) (*Connector, []mail.Message) {
	t.Helper()
	c, _ := testConnector(t, o, overrides)
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", sk); err != nil {
		t.Fatal(err)
	}
	return c, sk.msgs
}

func TestReadDetailsCapturesInlineAttachment(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.atts = []fakeAttachment{
		{name: "logo.png", size: 8, props: map[string]any{
			propAttachContentID:       "logo@example.test",
			propAttachContentLocation: "images/logo.png",
			propAttachMimeTag:         "image/png",
			propAttachSize:            int64(8),
			propAttachDataBin:         []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		}},
		{name: "facture.pdf", size: 120, props: map[string]any{
			propAttachMimeTag: "application/pdf",
		}},
	}
	c, msgs := syncedInbox(t, o, nil)
	var m mail.Message
	for _, x := range msgs {
		if x.ID == it.id() {
			m = x
		}
	}
	d, err := c.ReadDetails(t.Context(), refOf(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 2 {
		t.Fatalf("attachments: %+v", d.Attachments)
	}
	logo := d.Attachments[0]
	if !logo.Inline || logo.ContentID != "logo@example.test" || logo.ContentLocation != "images/logo.png" ||
		logo.ContentType != "image/png" || len(logo.Data) != 8 {
		t.Fatalf("inline attachment lost: %+v", logo)
	}
	pdf := d.Attachments[1]
	if pdf.Inline || pdf.ContentID != "" || len(pdf.Data) != 0 || pdf.ContentType != "application/pdf" {
		t.Fatalf("file attachment changed: %+v", pdf)
	}
}

func TestReadDetailsIdentifiesGenericInlinePictureBytes(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	it.atts = []fakeAttachment{
		{name: "image001.bin", size: int64(len(png)), props: map[string]any{
			propAttachContentID: "generic@example.test",
			propAttachMimeTag:   "application/octet-stream",
			propAttachSize:      int64(len(png)),
			propAttachDataBin:   png,
		}},
		{name: "notes.bin", size: 16, props: map[string]any{
			propAttachContentID: "text@example.test",
			propAttachMimeTag:   "application/octet-stream",
			propAttachSize:      int64(16),
			propAttachDataBin:   []byte("not a picture!!!"),
		}},
	}
	c, msgs := syncedInbox(t, o, nil)
	var m mail.Message
	for _, x := range msgs {
		if x.ID == it.id() {
			m = x
		}
	}
	d, err := c.ReadDetails(t.Context(), refOf(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 2 {
		t.Fatalf("attachments: %+v", d.Attachments)
	}
	got := d.Attachments[0]
	if got.ContentType != "image/png" || len(got.Data) != len(png) {
		t.Fatalf("generic inline picture not sniffed: %+v", got)
	}
	if other := d.Attachments[1]; len(other.Data) != 0 || other.ContentType != "application/octet-stream" {
		t.Fatalf("non-picture bytes kept: %+v", other)
	}
}

func TestReadDetailsHonoursTheInlineBudget(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.atts = []fakeAttachment{{name: "logo.png", size: 8, props: map[string]any{
		propAttachContentID: "logo@example.test",
		propAttachMimeTag:   "image/png",
		propAttachSize:      int64(8),
		propAttachDataBin:   []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
	}}}
	c, msgs := syncedInbox(t, o, map[string]any{"mail.inline_assets.max_part_bytes": 4})
	var m mail.Message
	for _, x := range msgs {
		if x.ID == it.id() {
			m = x
		}
	}
	d, err := c.ReadDetails(t.Context(), refOf(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 1 || len(d.Attachments[0].Data) != 0 || d.Attachments[0].Size != 8 {
		t.Fatalf("budget not applied: %+v", d.Attachments)
	}
}

func TestReadDetails(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.recips = append(it.recips,
		fakeRecip{typ: 2, name: "Lucas", email: "/o=x/cn=lucas", addrType: "EX", entry: "lucas"},
		fakeRecip{typ: 3, name: "Secret", email: "bcc@example.com", addrType: "SMTP", smtp: "bcc@example.com"},
		fakeRecip{typ: 0x10000000, name: "P1", email: "p1@example.com", addrType: "SMTP"})
	o.book["lucas"] = fakeEntry{propSMTP: "lucas@example.com"} // not an Exchange user: PR_SMTP_ADDRESS
	c, msgs := syncedInbox(t, o, nil)
	var m mail.Message
	for _, x := range msgs {
		if x.ID == it.id() {
			m = x
		}
	}
	d, err := c.ReadDetails(t.Context(), refOf(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != it.body || d.BodyTruncated || d.Headers != "Received: by example" || d.References != "<root@example.com>" {
		t.Fatalf("details %+v", d)
	}
	if d.BodyHTML != it.htmlBody || !d.BodyHTMLKnown || d.BodyHTMLTruncated {
		t.Fatalf("html body %+v", d)
	}
	want := []mail.Recipient{{Kind: mail.To, Name: "Amélie", Address: "amelie.durand@example.com"},
		{Kind: mail.Cc, Name: "Lucas", Address: "lucas@example.com"}, {Kind: mail.Bcc, Name: "Secret", Address: "bcc@example.com"}}
	if len(d.Recipients) != 3 || d.Recipients[0] != want[0] || d.Recipients[1] != want[1] || d.Recipients[2] != want[2] {
		t.Fatalf("recipients %+v", d.Recipients)
	}
	st := c.Stats().Details
	if st.Items != 1 || st.RecipientCount != 3 || st.RecipientSMTPRow != 2 || st.ByKind[mail.StorePrimary].Items != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestBodyAndHeaderCaps(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.body = strings.Repeat("é", 10) + "😀"
	it.props[propTransportHeaders] = strings.Repeat("h", 50)
	c, msgs := syncedInbox(t, o, map[string]any{"mail.body.max_chars": 11, "mail.headers.max_chars": 20})
	for _, m := range msgs {
		if m.ID != it.id() {
			continue
		}
		d, err := c.ReadDetails(t.Context(), refOf(m))
		if err != nil {
			t.Fatal(err)
		}
		// The emoji is 2 UTF-16 units: it does not fit in 11 and is not split.
		if d.Body != strings.Repeat("é", 10) || !d.BodyTruncated || len(d.Headers) != 20 {
			t.Fatalf("caps: %q %v %d", d.Body, d.BodyTruncated, len(d.Headers))
		}
		if st := c.Stats().Details; st.BodyTruncated != 1 || st.HeadersTruncated != 1 || st.MaxHeaderChars != 50 {
			t.Fatalf("stats %+v", st)
		}
		// L0 leftover: the cut is disclosed on the message itself, not only counted.
		if want := []mail.Alteration{{Code: mail.AltHeadersTruncated, Detail: "50"}}; !slices.Equal(d.Alterations, want) {
			t.Fatalf("alterations %+v", d.Alterations)
		}
	}
}

func TestHTMLBodyIsReadSeparatelyAndCapped(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.body = "plain stub"
	it.htmlBody = "<p>the <b>real</b> message</p>"
	c, msgs := syncedInbox(t, o, map[string]any{"mail.body.html_max_chars": 10})
	for _, m := range msgs {
		if m.ID != it.id() {
			continue
		}
		d, err := c.ReadDetails(t.Context(), refOf(m))
		if err != nil {
			t.Fatal(err)
		}
		if d.Body != "plain stub" || d.BodyHTML != "<p>the <b>" || !d.BodyHTMLKnown || !d.BodyHTMLTruncated {
			t.Fatalf("bodies %+v", d)
		}
		if want := []mail.Alteration{{Code: mail.AltBodyHTMLTruncated, Detail: "30"}}; !slices.Equal(d.Alterations, want) {
			t.Fatalf("alterations %+v", d.Alterations)
		}
	}
	// A member the item class lacks is an empty known state, not an error.
	post := o.stores[1].defaults[6].items[1]
	post.class = "post"
	for _, m := range msgs {
		if m.ID != post.id() {
			continue
		}
		d, err := c.ReadDetails(t.Context(), refOf(m))
		if err != nil || !d.BodyHTMLKnown || d.BodyHTML != "" {
			t.Fatalf("post class html: %+v %v", d, err)
		}
	}
}

func TestHTMLBodyFailureIsAnItemError(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.failHTMLBody = 0x80004005
	c, msgs := syncedInbox(t, o, nil)
	for _, m := range msgs {
		if m.ID != it.id() {
			continue
		}
		_, err := c.ReadDetails(t.Context(), refOf(m))
		var ie *source.ItemError
		if !errors.As(err, &ie) || ie.Code == "other" {
			t.Fatalf("html failure: %v", err)
		}
	}
}

func TestItemFailuresAreItemErrorsWithTheirHRESULT(t *testing.T) {
	o := profile()
	items := o.stores[1].defaults[6].items
	items[0].failOpen = hrNotFound
	items[1].failBody = 0x80004005
	c, msgs := syncedInbox(t, o, nil)
	byID := map[string]mail.Message{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	for _, it := range items[:2] {
		_, err := c.ReadDetails(t.Context(), refOf(byID[it.id()]))
		var ie *source.ItemError
		if !errors.As(err, &ie) || ie.Code == "other" {
			t.Fatalf("%v", err)
		}
	}
	if _, err := c.ReadDetails(t.Context(), refOf(byID[items[2].id()])); err != nil {
		t.Fatal(err)
	}
	st := c.Stats().Details
	if st.Failed != 2 || st.Failures["0x8004010F"] != 1 || st.Failures["0x80004005"] != 1 || st.Items != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestLostSessionDuringDetailsIsNotAnItemError(t *testing.T) {
	o := profile()
	c, msgs := syncedInbox(t, o, nil)
	o.lost = true
	_, err := c.ReadDetails(t.Context(), refOf(msgs[0]))
	var ie *source.ItemError
	if !errors.Is(err, ErrSessionLost) || errors.As(err, &ie) {
		t.Fatalf("%v", err)
	}
	o.lost = false
	if _, err := c.ReadDetails(t.Context(), refOf(msgs[0])); !errors.Is(err, ErrCoolingDown) || errors.As(err, &ie) {
		t.Fatalf("a refused reconnect is not an item failure: %v", err)
	}
	if err := c.Resume(); err != nil {
		t.Fatal(err)
	}
	o.elevated = true
	if _, err := c.ReadDetails(t.Context(), refOf(msgs[0])); !errors.Is(err, ErrElevated) || errors.As(err, &ie) {
		t.Fatalf("a connect failure is not an item failure: %v", err)
	}
}

func TestReadingDetailsNeverChangesTheItem(t *testing.T) {
	o := profile()
	c, msgs := syncedInbox(t, o, nil)
	for _, m := range msgs {
		if _, err := c.ReadDetails(t.Context(), refOf(m)); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range o.calls {
		class, name, _ := strings.Cut(call, ".")
		if _, ok := model[modelClass(class)][name]; !ok {
			t.Errorf("%s is not on the read-only list", call)
		}
	}
}

// modelClass maps the fake's kind names to model classes (identical).
func modelClass(kind string) string { return kind }

// Live finding: 2,209 items (non-mail classes) have no Recipients member;
// DISP_E_UNKNOWNNAME failed them all. A missing member is an empty value.
func TestItemClassWithoutRecipientsCompletes(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.class = "post"
	c, msgs := syncedInbox(t, o, nil)
	for _, m := range msgs {
		if m.ID != it.id() {
			continue
		}
		d, err := c.ReadDetails(t.Context(), refOf(m))
		if err != nil || len(d.Recipients) != 0 || d.Body != it.body {
			t.Fatalf("%v %+v", err, d)
		}
	}
	if st := c.Stats().Details; st.Absent["Recipients"] != 1 || st.Failed != 0 {
		t.Fatalf("%+v", st)
	}
}
