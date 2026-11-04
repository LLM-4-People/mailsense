package mail

import "testing"

func TestUTF16(t *testing.T) {
	cases := []struct {
		in        string
		n         int
		out       string
		truncated bool
	}{
		{"abc", 3, "abc", false}, {"abcd", 3, "abc", true}, {"é😀x", 2, "é", true}, {"é😀x", 3, "é😀", true},
		{"", 0, "", false}, {"a", 0, "", true}, {"a", -5, "", true}, {"\xffab", 2, "\xffa", true},
	}
	for _, c := range cases {
		if out, tr := TruncateUTF16(c.in, c.n); out != c.out || tr != c.truncated {
			t.Errorf("TruncateUTF16(%q, %d) = %q %v", c.in, c.n, out, tr)
		}
	}
	if UTF16Len("é😀x") != 4 || UTF16Len("") != 0 {
		t.Fatal("UTF16Len")
	}
}

func TestMergeStored(t *testing.T) {
	old := Message{Body: "full body", Headers: "h", References: "<r>", LocalCommitTime: "2026-10-01T00:00:00Z",
		Recipients: []Recipient{{Kind: To, Address: "a@b"}}, Subject: "the full subject", Stamp: "s1"}
	same := Message{Partial: true, Body: "preview", Subject: "the full", Stamp: "s1"}
	same.MergeStored(&old)
	if same.Partial || same.Body != "full body" || len(same.Recipients) != 1 || same.LocalCommitTime == "" || same.Subject != "the full subject" {
		t.Fatalf("unchanged stamp keeps the message complete: %+v", same)
	}
	moved := Message{Partial: true, Body: "preview", Stamp: "s2"}
	moved.MergeStored(&old)
	if !moved.Partial || moved.Body != "full body" {
		t.Fatalf("a moved stamp keeps the old details visible but needs a refresh: %+v", moved)
	}
	partialOld := old
	partialOld.Partial = true
	again := Message{Partial: true, Stamp: old.Stamp}
	again.MergeStored(&partialOld)
	if !again.Partial {
		t.Fatal("still partial when the stored copy was partial")
	}
	noStamp := Message{Partial: true}
	noStamp.MergeStored(&Message{})
	if !noStamp.Partial {
		t.Fatal("without a stamp nothing proves the details current")
	}
	complete := Message{Body: "x"}
	complete.MergeStored(&old)
	if complete.Body != "x" {
		t.Fatal("a complete message is written as is")
	}
	var m Message
	m.Complete(old.Details())
	if m.Partial || m.Body != "full body" || m.Details().Headers != "h" {
		t.Fatal("Complete")
	}
}

func TestStoreKinds(t *testing.T) {
	for _, k := range StoreKinds() {
		if !k.Valid() {
			t.Errorf("%q", k)
		}
	}
	// Misuse: the empty kind and the retired ones are not kinds.
	for _, k := range []StoreKind{"", "additional", "file", "Primary"} {
		if k.Valid() {
			t.Errorf("%q must not be valid", k)
		}
	}
}

func TestHeaderValues(t *testing.T) {
	block := "Microsoft Mail Internet Headers Version 2.0\r\nDelivered-To: a@example.org\r\n" +
		"delivered-to : b@example.org\r\nList-Id: Team\r\n <team.example.org>\r\nno colon line\r\nSubject: x: y"
	if got := HeaderValues(block, "Delivered-To"); len(got) != 2 || got[0] != "a@example.org" || got[1] != "b@example.org" {
		t.Fatalf("%q", got)
	}
	if got := HeaderValue(block, "LIST-ID"); got != "Team <team.example.org>" {
		t.Fatalf("folded: %q", got)
	}
	if HeaderValue(block, "Subject") != "x: y" || HeaderValue(block, "missing") != "" || HeaderValues("", "x") != nil {
		t.Fatal("values")
	}
}
