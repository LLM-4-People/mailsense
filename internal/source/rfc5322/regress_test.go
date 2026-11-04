package rfc5322

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

const hdr = "From: a@example.org\nTo: " + owner + "\nDate: Mon, 2 Mar 2026 09:00:00 +0000\n"

// L0 #7: an unquoted "From " body line is body, not a new message; a
// client rewriting its status headers (a read flag) keeps the id.
func TestMboxSeparatorAndStableIDs(t *testing.T) {
	msg := func(status string) string {
		return "From a@example.org Mon Mar  2 09:00:00 2026\n" + hdr + "Message-ID: <x1@example.org>\nX-Mozilla-Status: " + status +
			"\nStatus: " + status + "\nSubject: s\n\nbody line\nFrom here on, unquoted body line\n\n" +
			"From b@example.org Mon Mar  2 10:00:00 2026\n" + hdr + "Subject: t\n\nsecond\n"
	}
	p := filepath.Join(t.TempDir(), "Inbox")
	write(t, p, msg("0000"))
	a := syncAll(t, p, nil)
	write(t, p, msg("0001"))
	b := syncAll(t, p, nil)
	if len(a.msgs) != 2 || !strings.Contains(a.msgs[0].Body, "From here on") {
		t.Fatalf("two messages, the body line kept: %d %q", len(a.msgs), a.msgs[0].Body)
	}
	if a.versions[0].ID != b.versions[0].ID || a.versions[0].Stamp == b.versions[0].Stamp {
		t.Fatalf("a status rewrite keeps the id and moves the stamp: %+v %+v", a.versions[0], b.versions[0])
	}
	if strings.HasSuffix(a.msgs[0].Body, "\n\n") {
		t.Fatalf("the blank line before a separator is framing: %q", a.msgs[0].Body)
	}
}

// An mbox whose writer left out the blank line before a separator is read
// by RFC 4155 (one message), and the joined separator is declared, never
// silent.
func TestASeparatorWithoutABlankLineIsDeclared(t *testing.T) {
	two := "From a@example.org Mon Mar  2 09:00:00 2026\n" + hdr + "Subject: s\n\nfirst\n" +
		"From b@example.org Mon Mar  2 10:00:00 2026\n" + hdr + "Subject: t\n\nsecond\n"
	p := filepath.Join(t.TempDir(), "box")
	write(t, p, two)
	got := syncAll(t, p, nil)
	if len(got.msgs) != 1 || !slices.Contains(got.msgs[0].Alterations, mail.Alteration{Code: mail.AltMboxUnsplit, Detail: "1"}) {
		t.Fatalf("%d messages, %+v", len(got.msgs), got.msgs)
	}
}

// L0 #7: identical messages in one mbox get distinct ids.
func TestIdenticalMboxMessagesGetDistinctIDs(t *testing.T) {
	m := "From a@example.org Mon Mar  2 09:00:00 2026\n" + hdr + "Subject: same\n\nsame\n\n"
	p := filepath.Join(t.TempDir(), "box")
	write(t, p, m+m)
	got := syncAll(t, p, nil)
	if len(got.versions) != 2 || got.versions[0].ID == got.versions[1].ID {
		t.Fatalf("%+v", got.versions)
	}
}

// L0 rules §1.8: same-named .eml files in two directories never collide.
func TestSameNamedEMLFilesDoNotCollide(t *testing.T) {
	root := filepath.Join(t.TempDir(), "x")
	write(t, filepath.Join(root, "a", "1.eml"), hdr+"Subject: a\n\na\n")
	write(t, filepath.Join(root, "b", "1.eml"), hdr+"Subject: b\n\nb\n")
	got := syncAll(t, root, nil)
	if len(got.msgs) != 2 || got.msgs[0].ID == got.msgs[1].ID {
		t.Fatalf("%+v", got.versions)
	}
}

// L0 #8 + #3: maildir flags move the stamp; the size cap holds for
// maildir and .eml files too (skipped, never cut).
func TestMaildirFlagsAndSizeCap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "md")
	must(t, os.MkdirAll(filepath.Join(root, "new"), 0o700))
	p := filepath.Join(root, "cur", "1.M1P1.host:2,")
	write(t, p, hdr+"Subject: s\n\nsmall\n")
	before := syncAll(t, root, nil)
	must(t, os.Rename(p, filepath.Join(root, "cur", "1.M1P1.host:2,S")))
	after := syncAll(t, root, nil)
	if before.versions[0].ID != after.versions[0].ID || before.versions[0].Stamp == after.versions[0].Stamp || !after.msgs[0].IsRead {
		t.Fatalf("flag S: %+v -> %+v", before.versions[0], after.versions[0])
	}
	write(t, filepath.Join(root, "cur", "2.M2P1.host:2,"), hdr+"Subject: big\n\n"+strings.Repeat("x", 3<<20)+"\n")
	big := syncAll(t, root, map[string]any{"rfc5322.max_message_mb": 1})
	if len(big.msgs) != 1 || len(big.skips) != 1 {
		t.Fatalf("the oversized maildir message is skipped: %d %v", len(big.msgs), big.skips)
	}
	eml := filepath.Join(t.TempDir(), "big.eml")
	write(t, eml, hdr+"Subject: big\n\n"+strings.Repeat("x", 3<<20)+"\n")
	if got := syncAll(t, eml, map[string]any{"rfc5322.max_message_mb": 1}); len(got.msgs) != 0 || len(got.skips) != 1 {
		t.Fatalf("the oversized .eml is skipped: %d %v", len(got.msgs), got.skips)
	}
}

// L0 #3: an oversized mbox message is skipped and the next one read; a
// truncated mbox (cut inside a header) is never a panic.
func TestOversizedAndTruncatedMbox(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box")
	write(t, p, "From a@example.org Mon Mar  2 09:00:00 2026\n"+hdr+"Subject: big\n\n"+strings.Repeat("x\n", 600<<10)+"\n"+
		"From a@example.org Mon Mar  2 10:00:00 2026\n"+hdr+"Subject: ok\n\nok\n\n"+
		"From a@example.org Mon Mar  2 11:00:00 2026\nFrom: trunc")
	got := syncAll(t, p, map[string]any{"rfc5322.max_message_mb": 1})
	if len(got.skips) != 1 || len(got.msgs) != 2 || got.msgs[0].Subject != "ok" {
		t.Fatalf("big skipped, ok read, the truncated tail parsed or skipped: %d %v", len(got.msgs), got.skips)
	}
}

// L0 #9: a multipart without its closing boundary keeps its text.
func TestUnterminatedMultipartKeepsItsText(t *testing.T) {
	m := one(t, hdr+"Subject: m\nContent-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nhello truncated")
	if !strings.Contains(m.Body, "hello truncated") || !altered(m, mail.AltMIMEUnterminated) {
		t.Fatalf("%q %+v", m.Body, m.Alterations)
	}
	none := one(t, hdr+"Subject: m\nContent-Type: multipart/mixed; boundary=b\n\nno boundary at all\n")
	if !strings.Contains(none.Body, "no boundary at all") || !altered(none, mail.AltMIMEUnterminated) {
		t.Fatalf("%q %+v", none.Body, none.Alterations)
	}
}

// L0 #9: raw 8-bit headers are decoded in a declared step; an address
// that still does not parse yields its angle-bracket address.
func TestEightBitHeadersAreDecodedAndDeclared(t *testing.T) {
	m := one(t, "From: Ren\xe9 <rene@example.fr>\nTo: "+owner+"\nSubject: R\xe9union\nDate: Mon, 2 Mar 2026 09:00:00 +0000\n\nb\n")
	if m.SenderAddress != "rene@example.fr" || m.SenderName != "René" || m.Subject != "Réunion" || m.Validate() != nil {
		t.Fatalf("%q %q %q", m.SenderName, m.SenderAddress, m.Subject)
	}
	if !slices.Contains(m.Alterations, mail.Alteration{Code: mail.AltHeaderBytes, Detail: "windows-1252"}) {
		t.Fatalf("%+v", m.Alterations)
	}
	sj := one(t, "From: a@example.jp\nTo: "+owner+"\nSubject: \x8c\xa9\x90\xcf\x8f\x91\nContent-Type: text/plain; charset=Shift_JIS\n\n\x8c\xa9\x90\xcf\n")
	if sj.Subject != "見積書" || !slices.Contains(sj.Alterations, mail.Alteration{Code: mail.AltHeaderBytes, Detail: "shift_jis"}) {
		t.Fatalf("the declared charset decodes the header: %q %+v", sj.Subject, sj.Alterations)
	}
	bad := one(t, hdr+"Cc: <<broken>> \"x\" <x@example.org>, y@\nSubject: c\n\nb\n")
	if !altered(bad, mail.AltAddressUnparsed) || !slices.ContainsFunc(bad.Recipients, func(r mail.Recipient) bool { return r.Address == "x@example.org" }) {
		t.Fatalf("%+v %+v", bad.Recipients, bad.Alterations)
	}
}

// L0 rules §1.7: an unknown charset, invalid bytes, a missing Received
// header and bad transfer encodings are declared.
func TestSubstitutionsAreDeclared(t *testing.T) {
	m := one(t, hdr+"Subject: =?x-unknown?Q?Caf=E9?=\nContent-Type: text/plain; charset=x-unknown\n\ncaf\xe9\n")
	if !altered(m, mail.AltCharsetUnknown) || !altered(m, mail.AltReceivedFromDate) || m.ReceivedAt != m.SentOn {
		t.Fatalf("%+v", m.Alterations)
	}
	inv := one(t, hdr+"Received: from x by y; Mon, 2 Mar 2026 09:05:00 +0000\nSubject: i\nContent-Type: text/plain; charset=utf-8\n\nbad \xff\n")
	if !altered(inv, mail.AltInvalidBytes) || altered(inv, mail.AltReceivedFromDate) || inv.ReceivedAt != "2026-03-02T09:05:00Z" {
		t.Fatalf("%+v %q", inv.Alterations, inv.ReceivedAt)
	}
	b64 := one(t, hdr+"Subject: b\nContent-Transfer-Encoding: base64\n\naGVsbG8=!!!\n")
	if !strings.HasPrefix(b64.Body, "hello") || !altered(b64, mail.AltTransferDecoding) {
		t.Fatalf("%q %+v", b64.Body, b64.Alterations)
	}
}

// L0 rules §7: deep nesting stops at rfc5322.mime.max_depth, declared.
func TestDeepNestingIsBoundedAndDeclared(t *testing.T) {
	var b strings.Builder
	b.WriteString(hdr + "Subject: deep\nContent-Type: multipart/mixed; boundary=b0\n\n")
	for i := range 30 {
		b.WriteString("--b" + strconv.Itoa(i) + "\nContent-Type: multipart/mixed; boundary=b" + strconv.Itoa(i+1) + "\n\n")
	}
	b.WriteString("--b30\nContent-Type: text/plain\n\ndeepest\n")
	m := one(t, b.String())
	if !altered(m, mail.AltMIMEDepth) || strings.Contains(m.Body, "deepest") || len(m.Attachments) != 0 {
		t.Fatalf("%q %+v %+v", m.Body, m.Attachments, m.Alterations)
	}
}

// L0 #10: a multipart invitation reply is a meeting response.
func TestCalendarMethodOfAPart(t *testing.T) {
	m := one(t, hdr+"Subject: Accepted\nContent-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\naccepted\n"+
		"--b\nContent-Type: text/calendar; charset=utf-8\n\nBEGIN:VCALENDAR\nMETHOD:REPLY\nEND:VCALENDAR\n--b--\n")
	if m.CalendarMethod != "REPLY" || mail.KindOf(&m) != mail.KindMeetingResponse {
		t.Fatalf("%q %q", m.CalendarMethod, mail.KindOf(&m))
	}
}

// L0 #11: Gmail labels are RFC 2047-decoded; system labels carry roles.
func TestGmailLabelsDecodedWithSystemRoles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "All mail.mbox")
	write(t, p, "From 1@xxx Mon Mar 02 09:00:00 +0000 2026\nX-Gmail-Labels: Spam,=?UTF-8?Q?Caf=C3=A9?=,Trash\n"+hdr+"Subject: g\n\nb\n")
	m := syncAll(t, p, nil).msgs[0]
	want := []mail.FolderRef{{Path: "Spam", Role: mail.RoleJunk}, {Path: "Café", Role: mail.RoleOther}, {Path: "Trash", Role: mail.RoleDeleted}}
	if !slices.Equal(m.Folders, want) {
		t.Fatalf("%+v", m.Folders)
	}
}

// L0 leftover: Gmail's Unread label (the API's UNREAD system label as a
// Takeout writes it) is the read state, not a folder; a Gmail message
// without it is read; "Opened" is no documented system label and stays
// a plain label; a message of another export keeps its Status flag.
func TestGmailUnreadLabelIsTheReadState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "All mail.mbox")
	write(t, p, "From 1@xxx Mon Mar 02 09:00:00 +0000 2026\nX-Gmail-Labels: Inbox,Unread\n"+hdr+"Subject: u\n\nb\n\n"+
		"From 2@xxx Mon Mar 02 09:00:00 +0000 2026\nX-Gmail-Labels: Inbox,Opened\n"+hdr+"Subject: o\n\nb\n\n"+
		"From 3@xxx Mon Mar 02 09:00:00 +0000 2026\nStatus: O\n"+hdr+"Subject: s\n\nb\n")
	msgs := syncAll(t, p, nil).msgs
	if len(msgs) != 3 || msgs[0].IsRead || !msgs[1].IsRead || msgs[2].IsRead {
		t.Fatalf("read states: %+v", msgs)
	}
	if want := []mail.FolderRef{{Path: "Inbox", Role: mail.RoleInbox}}; !slices.Equal(msgs[0].Folders, want) {
		t.Errorf("Unread is no folder: %+v", msgs[0].Folders)
	}
	if want := []mail.FolderRef{{Path: "Inbox", Role: mail.RoleInbox}, {Path: "Opened", Role: mail.RoleOther}}; !slices.Equal(msgs[1].Folders, want) {
		t.Errorf("Opened stays a plain label: %+v", msgs[1].Folders)
	}
}

// L0 rules §1.2: a case change that changes the byte length never
// misplaces (or panics) the end of a script element.
func TestHTMLTextWithLengthChangingCase(t *testing.T) {
	if got := htmlText("<style>" + strings.Repeat("Ⱥ", 9) + "</style>x"); got != "x" {
		t.Fatalf("%q", got)
	}
}

// Reviewer repros that already held: kept as regression tests.
func TestParseEdgeCasesThatHold(t *testing.T) {
	g := one(t, "From: John Q. Public <jqp@example.com>\nTo: undisclosed-recipients:;\nCc: Team: x@example.org, "+owner+";\nSubject: g\n\nb\n")
	if g.SenderName != "John Q. Public" || len(g.Recipients) != 2 || g.Recipients[1].Address != owner {
		t.Fatalf("obs-phrase and group syntax: %+v %+v", g.SenderName, g.Recipients)
	}
	n := one(t, hdr+"Subject: Fwd\nContent-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nsee attached\n--b\n"+
		"Content-Type: message/rfc822\n\nFrom: c@example.org\nSubject: Inner\n\ninner secret\n--b--\n")
	if n.Body != "see attached" || len(n.Attachments) != 1 || n.Attachments[0].Name != "message.eml" {
		t.Fatalf("a nested message is an attachment: %q %+v", n.Body, n.Attachments)
	}
}

// L0 #3: an mbox item holds offsets, not bytes: what is read is the
// file's content at read time.
func TestMboxItemsAreReadLazilyByOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box")
	write(t, p, "From a@example.org Mon Mar  2 09:00:00 2026\n"+hdr+"Subject: aaaa\n\nbody\n")
	items, err := mboxItems(p, "box", 1<<20)
	must(t, err)
	write(t, p, "From a@example.org Mon Mar  2 09:00:00 2026\n"+hdr+"Subject: bbbb\n\nbody\n")
	data, err := items[0].read()
	must(t, err)
	if !strings.Contains(string(data), "bbbb") {
		t.Fatalf("%q", data)
	}
}

func TestFormatsAreValid(t *testing.T) {
	f := formats()
	if len(f.status) == 0 || !f.status["x-mozilla-status"] || len(f.labels) == 0 {
		t.Fatalf("%+v", f)
	}
	for label, role := range f.labels {
		if _, err := mail.ParseRole(string(role)); err != nil {
			t.Errorf("%s: %v", label, err)
		}
	}
}

// Misuse: an unknown raw charset setting fails the folder (reported),
// never decodes silently; a long header block is cut and declared.
func TestRawCharsetSettingAndHeaderCut(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.eml")
	write(t, p, hdr+"Subject: s\n\nb\n")
	c, err := New("files", p, configtest.New(t, map[string]any{"rfc5322.headers.raw_charset": "no-such-charset"}))
	must(t, err)
	folders, err := c.Discover(t.Context())
	must(t, err)
	if _, err := c.Sync(t.Context(), folders[0], "", &capture{skips: map[string]error{}}); err == nil {
		t.Fatal("an unknown raw charset is an error")
	}
	got := syncAll(t, p, map[string]any{"mail.headers.max_chars": 20})
	if m := got.msgs[0]; len(m.Headers) != 20 || !altered(m, mail.AltHeadersTruncated) {
		t.Fatalf("%q %+v", m.Headers, m.Alterations)
	}
}

// Regression (review L0, bugs #6): the stored header block may be cut
// (mail.headers.max_chars); the labels and the client status are read
// from the whole block, so a label line past or across the cut is whole.
func TestLabelsAreReadFromTheWholeHeaderBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "All mail.mbox")
	long := "Received: from " + strings.Repeat("relay.example.net ", 20) + "\n"
	write(t, p, "From 1@xxx Mon Mar 02 09:00:00 +0000 2026\n"+hdr+long+"Status: RO\nX-Gmail-Labels: Inbox,Important\nSubject: g\n\nb\n")
	m := syncAll(t, p, map[string]any{"mail.headers.max_chars": 120}).msgs[0]
	if !slices.ContainsFunc(m.Alterations, func(a mail.Alteration) bool { return a.Code == mail.AltHeadersTruncated }) {
		t.Fatal("the stored block is cut")
	}
	want := []mail.FolderRef{{Path: "Inbox", Role: mail.RoleInbox}, {Path: "Important", Role: mail.RoleOther}}
	if !slices.Equal(m.Folders, want) {
		t.Fatalf("labels past the cut: %+v", m.Folders)
	}
}
