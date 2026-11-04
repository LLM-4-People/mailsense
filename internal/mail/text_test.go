package mail

import "testing"

func TestPythonTextSemantics(t *testing.T) {
	nbsp := "a b\x1cc  d"
	if n := len(Words(nbsp)); n != 4 {
		t.Fatalf("Python \\S+ splits on NBSP and U+001C: %d words", n)
	}
	if Strip("  x \x1f") != "x" {
		t.Fatal("strip")
	}
	if Runes("é😀") != 2 || Head("é😀x", 2) != "é😀" || Head("ab", 5) != "ab" || Head("ab", 0) != "" || Head("ab", -1) != "" {
		t.Fatal("code points")
	}
	if Excerpt("  hi\nthere\n ", 7) != "hi ther" {
		t.Fatalf("%q", Excerpt("  hi\nthere\n ", 7))
	}
	if !IsWordRune('é') || !IsWordRune('_') || IsWordRune('×') || IsWordRune('-') {
		t.Fatal("word runes")
	}
}

func TestThreadIDs(t *testing.T) {
	m := &Message{InternetMessageID: " <self@x> ", InReplyTo: "<p@x>", References: "<root@x> <p@x>\r\n <self@x>"}
	self, parents := ThreadIDs(m)
	if self != "<self@x>" || len(parents) != 2 || parents[0] != "<p@x>" || parents[1] != "<root@x>" {
		t.Fatalf("%q %q", self, parents)
	}
	// Fields empty: read from the transport headers, folded References included.
	h := &Message{InternetMessageID: "bare@id", Headers: "Received: x\r\nIn-Reply-To: <a@x>\r\nReferences: <r@x>\r\n\t<a@x>\r\nSubject: s\r\n"}
	self, parents = ThreadIDs(h)
	if self != "<bare@id>" || len(parents) != 2 || parents[0] != "<a@x>" || parents[1] != "<r@x>" {
		t.Fatalf("headers: %q %q", self, parents)
	}
	// Junk never panics and yields nothing.
	for _, junk := range []string{"\x00\x01", ": : :", "no colon line\n\n", ""} {
		if s, p := ThreadIDs(&Message{Headers: junk, InternetMessageID: "has space@x"}); s != "" || len(p) != 0 {
			t.Fatalf("junk %q: %q %q", junk, s, p)
		}
	}
}

// Review 2a #10 (repro TestReviewHeaderParsing; failed first for every
// case but "folded refs": textproto stopped at the first malformed line,
// and a bare In-Reply-To was ignored). Fixture: 65 of 3,612 headers had
// lost their parents.
func TestThreadIDsSurviveMalformedHeaderBlocks(t *testing.T) {
	cases := map[string]Message{
		"exchange prefix": {InternetMessageID: "<b@x>", Headers: "Microsoft Mail Internet Headers Version 2.0\r\nReceived: from a\r\nIn-Reply-To: <a@x>\r\nReferences: <r@x>\r\n"},
		"bad line midway": {InternetMessageID: "<b@x>", Headers: "Received: from a\r\nX-Weird Header: v\r\nIn-Reply-To: <a@x>\r\n"},
		"no colon midway": {InternetMessageID: "<b@x>", Headers: "Received: from a\r\n=?utf-8?q?garbage?=\r\nIn-Reply-To: <a@x>\r\n"},
		"folded refs":     {InternetMessageID: "<b@x>", Headers: "References: <r1@x>\r\n\t<a@x>\r\nIn-Reply-To: <a@x>\r\n"},
		"bare in-reply":   {InternetMessageID: "b@x", InReplyTo: "a@x"},
		"leading space":   {InternetMessageID: "<b@x>", Headers: " Received: from a\r\nIn-Reply-To: <a@x>\r\n"},
		"lower case":      {InternetMessageID: "<b@x>", Headers: "in-reply-to:<a@x>\n"},
	}
	for name, m := range cases {
		self, parents := ThreadIDs(&m)
		if self != "<b@x>" || len(parents) == 0 || parents[0] != "<a@x>" {
			t.Errorf("%s: self=%q parents=%q", name, self, parents)
		}
	}
}

func TestInstant(t *testing.T) {
	sent := Message{FolderRole: RoleSent, ReceivedAt: "2026-01-01T09:00:00Z", SentOn: "2026-01-01T10:00:00Z"}
	inbox := Message{FolderRole: RoleInbox, ReceivedAt: "2026-01-01T09:00:00Z", SentOn: "2026-01-01T08:00:00Z"}
	if at, _ := sent.Instant(); at.Hour() != 10 {
		t.Fatal("sent: sent_on")
	}
	if at, _ := inbox.Instant(); at.Hour() != 9 {
		t.Fatal("inbox: received_at")
	}
	if _, ok := (&Message{FolderRole: RoleSent, ReceivedAt: "x"}).Instant(); ok {
		t.Fatal("junk")
	}
}
