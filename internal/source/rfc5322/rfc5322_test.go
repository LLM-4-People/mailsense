package rfc5322

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

const owner = "amelie.durand@example.com"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(content), 0o600))
}

// corpus writes a synthetic multilingual tree: a Gmail-style mbox, a
// maildir, an .eml directory. Every name and address is synthetic.
func corpus(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "export")
	jp, _ := japanese.ISO2022JP.NewEncoder().String("会議の議事録を送ってください。")
	gbSubject, _ := simplifiedchinese.GBK.NewEncoder().String("项目进度报告")
	mbox := "From x@example.org Mon Mar  2 09:00:00 2026\n" +
		"From: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("李娜")) + "?= <li.na@例子.测试>\n" +
		"To: Amélie <" + owner + ">\nSubject: =?GBK?B?" + base64.StdEncoding.EncodeToString([]byte(gbSubject)) + "?=\n" +
		"Date: Mon, 2 Mar 2026 17:00:00 +0800\nMessage-ID: <zh1@example.org>\nX-Gmail-Labels: Inbox,\"Clients, Asia\",Important\n" +
		"Content-Type: text/plain; charset=utf-8\n\n请在星期五之前发送项目进度报告。\n>From the archive, quoted\n\n" +
		"From y@example.org Mon Mar  2 10:00:00 2026\n" +
		"From: Sato <sato@example.jp>\nTo: " + owner + "\nSubject: =?ISO-2022-JP?B?" + base64.StdEncoding.EncodeToString([]byte(jp)) + "?=\n" +
		"Date: Mon, 2 Mar 2026 18:00:00 +0900\nMIME-Version: 1.0\nContent-Type: text/plain; charset=ISO-2022-JP\n\n" + jp + "\n\n" +
		"From z@example.org Mon Mar  2 11:00:00 2026\n" +
		"From: Omar <omar@example.org>\nTo: list@lists.example.org\nList-Id: <team.lists.example.org>\nDelivered-To: " + owner + "\n" +
		"Subject: =?UTF-8?Q?=D8=B1=D8=AF:_=D8=A7=D9=84=D9=81=D8=A7=D8=AA=D9=88=D8=B1=D8=A9?=\nDate: Mon, 2 Mar 2026 12:00:00 +0300\n" +
		"MIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=\"b1\"\n\n--b1\nContent-Type: multipart/alternative; boundary=\"b2\"\n\n" +
		"--b2\nContent-Type: text/html; charset=windows-1256\nContent-Transfer-Encoding: quoted-printable\n\n<p>=E5=E1 <b>=ED=E3=DF=E4=DF</b>?</p><script>x()</script>\n--b2--\n" +
		"--b1\nContent-Type: application/pdf; name=\"=?UTF-8?Q?facture_=C3=A9t=C3=A9.pdf?=\"\nContent-Disposition: attachment\nContent-Transfer-Encoding: base64\n\nJVBERi0xLjQK\n--b1--\n"
	write(t, filepath.Join(root, "gmail.mbox"), mbox)
	write(t, filepath.Join(root, "Maildir", "cur", "1700000000.M1P1.host:2,S"),
		"From: Jonas <jonas@beispiel.example>\nTo: "+owner+"\nSubject: AW: Angebot\nDate: Tue, 3 Mar 2026 09:00:00 +0100\n"+
			"Content-Type: text/plain; charset=iso-8859-1\nContent-Transfer-Encoding: quoted-printable\n\nK=F6nnten Sie das Angebot pr=FCfen?\n")
	write(t, filepath.Join(root, "Maildir", "new", "1700000001.M2P1.host"),
		"From: "+owner+"\nTo: noa@example.org\nSubject: =?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte("השב: פגישה"))+"?=\nDate: Tue, 3 Mar 2026 10:00:00 +0200\n\nתודה\n")
	must(t, os.MkdirAll(filepath.Join(root, "Maildir", "tmp"), 0o700))
	write(t, filepath.Join(root, "Maildir", ".Archives.2025", "cur", "1600000000.M3P1.host:2,"),
		"From: Olivia <olivia@example.com>\nTo: "+owner+"\nSubject: Re: Budget\nDate: Wed, 4 Jun 2025 09:00:00 -0400\n\nOK.\n")
	must(t, os.MkdirAll(filepath.Join(root, "Maildir", ".Archives.2025", "new"), 0o700))
	write(t, filepath.Join(root, "eml", "ndr.eml"),
		"From: postmaster@example.org\nTo: "+owner+"\nSubject: Undeliverable\nDate: Thu, 5 Mar 2026 09:00:00 +0000\n"+
			"Content-Type: multipart/report; report-type=delivery-status; boundary=r\n\n--r\nContent-Type: text/plain\n\nfailed\n--r--\n")
	write(t, filepath.Join(root, "eml", "broken.eml"), "not a mail at all")
	return root
}

func rig(t *testing.T, root string, settings map[string]any) (*Connector, *syncer.Syncer, *store.Store, *config.Config) {
	t.Helper()
	cfg := configtest.New(t, settings)
	st, err := store.Open(t.Context(), cfg, nil, nil)
	must(t, err)
	t.Cleanup(func() { _ = st.Close() })
	c, err := New("files", root, cfg)
	must(t, err)
	return c, syncer.New(st, cfg, nil, nil), st, cfg
}

func TestLayoutsAreRecognisedByStructure(t *testing.T) {
	specs, err := scanLayout(corpus(t))
	must(t, err)
	var got []string
	for _, s := range specs {
		got = append(got, s.Path+":"+s.Layout)
	}
	want := []string{"export/Maildir:maildir", "export/Maildir/Archives/2025:maildir", "export/eml:eml", "export/gmail.mbox:mbox"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if _, err := scanLayout(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing path is an error")
	}
	plain := filepath.Join(t.TempDir(), "notes.txt")
	write(t, plain, "hello")
	if _, err := scanLayout(plain); err == nil {
		t.Fatal("a file that is neither an mbox nor an .eml is refused")
	}
}

// #9: mbox + maildir + .eml through the real syncer and store: charsets,
// encoded words, MIME, attachments, Gmail labels as further folders,
// read flags; nothing syncs before the pick; the owner is declared.
func TestSyncAMultilingualTree(t *testing.T) {
	root := corpus(t)
	key := source.StoreKey("files", storeID)
	c, s, st, cfg := rig(t, root, map[string]any{"rfc5322.folder_roles": []any{"export/eml=inbox", "Important=other"},
		"sync.skip_ratio_fatal": 0.6})
	if _, err := s.Run(t.Context(), c, nil); err == nil {
		t.Fatal("nothing is picked: the run says so")
	}
	if n, _ := st.CountByRole(t.Context()); len(n) != 0 {
		t.Fatal("nothing syncs before the pick")
	}
	must(t, source.Pick(cfg, c.Mailboxes(), key))
	rep, err := s.Run(t.Context(), c, nil)
	must(t, err)
	if rep.Totals.Written != 7 || rep.Totals.Skipped != 1 {
		t.Fatalf("7 messages, the broken .eml skipped: %+v", rep.Totals)
	}
	if rep.Scope.Stores[0].Blocked == "" {
		t.Fatal("files name no owner: blocked until declared")
	}
	checkTree(t, st)
	if res, _ := st.Search(t.Context(), "进度报告", 5); res.Total != 1 {
		t.Fatal("Chinese mail is searchable")
	}
	// The owner declared: every message judged; the list mail is the owner's.
	must(t, cfg.Set(source.SettingDeclared, []any{key + "=" + owner}))
	rep, err = s.Run(t.Context(), c, nil)
	must(t, err)
	if rep.Totals.Seen != 0 || rep.Scope.Stores[0].Blocked != "" || rep.Scope.Counts[scope.Owner] != 7 {
		t.Fatalf("unchanged files are not read again; all judged owner: %+v %+v", rep.Totals, rep.Scope)
	}
	// A deleted file is removed (complete snapshot).
	must(t, os.Remove(filepath.Join(root, "Maildir", "new", "1700000001.M2P1.host")))
	rep, err = s.Run(t.Context(), c, nil)
	must(t, err)
	if rep.Removed.Deleted != 1 {
		t.Fatalf("removed: %+v", rep.Removed)
	}
}

// checkTree checks the multilingual tree's messages as stored.
func checkTree(t *testing.T, st *store.Store) {
	t.Helper()
	byID := map[string]mail.Message{}
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error { byID[r.Message.Subject] = r.Message; return nil })
	zh, ok := byID["项目进度报告"]
	if !ok || zh.SenderName != "李娜" || zh.SenderAddress != "li.na@例子.测试" || !strings.Contains(zh.Body, "From the archive") {
		t.Fatalf("GBK encoded word, UTF-8 address, mboxrd unquoting: %+v", zh)
	}
	full, _ := st.GetMessage(t.Context(), "files", storeID, zh.ID)
	if len(full.Folders) != 3 || full.Folders[0].Path != "Clients, Asia" || full.Folders[2].Path != "Inbox" {
		t.Fatalf("Gmail labels are further folders: %+v", full.Folders)
	}
	if jp := byID["会議の議事録を送ってください。"]; !strings.Contains(jp.Body, "金曜日") && !strings.Contains(jp.Body, "議事録") {
		t.Fatalf("ISO-2022-JP subject and body: %+v", jp)
	}
	ar := byID["رد: الفاتورة"]
	arFull, _ := st.GetMessage(t.Context(), "files", storeID, ar.ID)
	if !strings.Contains(ar.Body, "هل") || strings.Contains(ar.Body, "x()") || len(arFull.Attachments) != 1 ||
		arFull.Attachments[0].Name != "facture été.pdf" || arFull.Attachments[0].Size != 9 || !ar.HasAttachments {
		t.Fatalf("windows-1256 HTML only, script dropped, attachment metadata: %q %+v", ar.Body, arFull.Attachments)
	}
	if de := byID["AW: Angebot"]; !de.IsRead || !strings.Contains(de.Body, "Könnten") || de.FolderPath != "export/Maildir" {
		t.Fatalf("maildir flag S, ISO-8859-1 quoted-printable: %+v", de)
	}
	if he := byID["השב: פגישה"]; he.IsRead || he.SentOn != "2026-03-03T08:00:00Z" {
		t.Fatalf("new/ is unread; the Date header is UTC in the store: %+v", he)
	}
	if ndr := byID["Undeliverable"]; mail.KindOf(&ndr) != mail.KindDeliveryReport || ndr.FolderRole != mail.RoleInbox {
		t.Fatalf("a delivery report from its headers; the role from rfc5322.folder_roles: %+v", ndr)
	}
}

func TestMisuse(t *testing.T) {
	if _, err := New("", "x", configtest.New(t, nil)); err == nil {
		t.Fatal("an id is required")
	}
	c, _, _, _ := rig(t, corpus(t), nil)
	if _, err := c.Sync(t.Context(), mail.Folder{Path: "nope"}, "", nil); err == nil {
		t.Fatal("a folder that was not discovered is refused")
	}
	if got := gmailLabels("a, \"b, c\" ,,d", newWordDecoder(&mail.Message{})); !slices.Equal(got, []string{"a", "b, c", "d"}) {
		t.Fatalf("%q", got)
	}
	if htmlText("<>x<br/>y<p class=a>z</p><style>p{}</style>&amp;<unclosed") != "x\ny\nz\n&" {
		t.Fatalf("%q", htmlText("<>x<br/>y<p class=a>z</p><style>p{}</style>&amp;<unclosed"))
	}
}
