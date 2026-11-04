package scope

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

const dn = "/O=EXAMPLE ORG/OU=EXCHANGE ADMINISTRATIVE GROUP/CN=RECIPIENTS/CN=ABC-AMELIE"

func owner() Identity {
	return NewIdentity([]string{"+"}, []string{"Amelie.Durand@example.com", "amelie@example.org", dn}, []string{"李娜@例子.中国"})
}

func TestIdentity(t *testing.T) {
	id := owner()
	if !id.Known() || id.Primary() != "amelie.durand@example.com" || len(id.Addresses) != 4 {
		t.Fatalf("%+v", id)
	}
	// Review L0 rules §7 (IDN declared address; failed first for the
	// Punycode and full-width spellings): one address in every spelling.
	for _, a := range []string{" AMELIE.DURAND@example.com", "amelie+news@example.org", " " + dn, "李娜@例子.中国", "李娜+x@例子.中国",
		"李娜@xn--fsqu00a.xn--fiqs8s", "李娜@例子。中国", "李娜@ＸＮ--fsqu00a.xn--fiqs8s"} {
		if !id.Owns(a) {
			t.Errorf("must own %q", a)
		}
	}
	// Misuse: other people, a tag on someone else, an empty base, the empty address.
	for _, a := range []string{"", "  ", "amelie@example.net", "bob+amelie@example.org", "+x@example.org", "amelie.durand@example.com.evil"} {
		if id.Owns(a) {
			t.Errorf("must not own %q", a)
		}
	}
	if NewIdentity(nil, []string{"amelie+x@example.org"}).Owns("amelie@example.org") {
		t.Fatal("a subaddress never makes the base address the owner's")
	}
	if NewIdentity(nil, []string{"a@example.org"}).Owns("a+b@example.org") {
		t.Fatal("no separators: exact addresses only")
	}
	if NewIdentity(nil, nil, []string{"", " "}).Known() {
		t.Fatal("blank addresses are no identity")
	}
	if d := NewIdentity(nil, []string{"A@x.org"}, []string{"a@X.org", "b@x.org"}); len(d.Addresses) != 2 {
		t.Fatalf("de-duplicated: %v", d.Addresses)
	}
}

// Digest names the persisted identity of a verdict: the normalized
// addresses and the subaddress separators, nothing else. Its bytes are a
// stored format, so the pinned value keeps every existing verdict current.
func TestIdentityDigestNamesAddressesAndSeparators(t *testing.T) {
	id := NewIdentity([]string{"+"}, []string{"Amelie.Durand@example.com", "amelie@example.org"})
	if got := id.Digest(); got != "092c11ca3c5091e3" {
		t.Fatalf("digest %q, want the persisted value 092c11ca3c5091e3", got)
	}
	if got := NewIdentity([]string{"+"}, []string{"amelie.durand@example.com", "AMELIE@example.org"}); got.Digest() != id.Digest() {
		t.Fatalf("one identity in another spelling must keep its digest: %q", got.Digest())
	}
	for name, other := range map[string]Identity{
		"another address":   NewIdentity([]string{"+"}, []string{"amelie.durand@example.com", "other@example.org"}),
		"another separator": NewIdentity([]string{"-"}, []string{"amelie.durand@example.com", "amelie@example.org"}),
		"no separators":     NewIdentity(nil, []string{"amelie.durand@example.com", "amelie@example.org"}),
	} {
		if other.Digest() == id.Digest() {
			t.Errorf("%s must change the digest", name)
		}
	}
}

func TestDecide(t *testing.T) {
	id := owner()
	in := func(ev Evidence) Evidence { ev.Role = mail.RoleInbox; return ev }
	cases := []struct {
		name   string
		ev     Evidence
		want   Verdict
		reason Reason
	}{
		{"sent by owner", Evidence{Role: mail.RoleSent, SenderAddress: "amelie.durand@example.com"}, Owner, ReasonSentByOwner},
		{"sent as DN", Evidence{Role: mail.RoleSent, SenderAddress: dn}, Owner, ReasonSentByOwner},
		{"sent on behalf of the owner", Evidence{Role: mail.RoleSent, SenderAddress: "assistant@example.com", SentRepresenting: "amelie@example.org"}, Owner, ReasonSentByOwner},
		{"sent by someone else", Evidence{Role: mail.RoleSent, SenderAddress: "site-web@example.com"}, Shared, ReasonSentByOther},
		{"sent without sender", Evidence{Role: mail.RoleSent, SenderAddress: " "}, Unattributed, ReasonSentNoSender},
		{"owner writing to self lands in the inbox", in(Evidence{SenderAddress: "amelie@example.org"}), Owner, ReasonSentByOwner},
		{"alias recipient", in(Evidence{Recipients: []string{"x@y.com", " AMELIE@example.org"}}), Owner, ReasonRecipientMatch},
		{"plus address", in(Evidence{Recipients: []string{"amelie+facturas@example.org"}}), Owner, ReasonRecipientMatch},
		{"IDN recipient", in(Evidence{Recipients: []string{"李娜@例子.中国"}}), Owner, ReasonRecipientMatch},
		{"DN recipient", in(Evidence{Recipients: []string{dn}}), Owner, ReasonRecipientMatch},
		{"flag bcc", in(Evidence{AddressedToMe: " BCC ", Recipients: []string{"team@example.com"}}), Owner, ReasonAddressedFlag},
		{"envelope recipient", in(Evidence{Recipients: []string{"liste@example.fr"}, DeliveredTo: []string{"amelie@example.org"}}), Owner, ReasonDeliveredTo},
		{"mailing list", in(Evidence{Recipients: []string{"liste@example.fr"}, ListID: true}), Owner, ReasonMailingList},
		// Review L0 rules §1.5 (failed first: owner by List-Id): the
		// source's explicit "not addressed to me" outranks a List-Id
		// (list mail forwarded through someone else's alias).
		{"list mail not addressed to the owner", in(Evidence{Recipients: []string{"liste@example.fr"}, ListID: true, AddressedToMe: "no"}), Shared, ReasonNotAddressed},
		{"flag no", in(Evidence{AddressedToMe: "no", Recipients: []string{"support@example.com"}}), Shared, ReasonNotAddressed},
		{"others", in(Evidence{Recipients: []string{"wordpress@example.com"}, DeliveredTo: []string{"wordpress@example.com"}}), Shared, ReasonRecipientsOther},
		{"nothing", in(Evidence{Recipients: []string{"", " "}}), Unattributed, ReasonNoRecipients},
		{"unknown flag value", in(Evidence{AddressedToMe: "maybe"}), Unattributed, ReasonNoRecipients},
		{"archive role judged as incoming", Evidence{Role: mail.RoleArchive, AddressedToMe: "to"}, Owner, ReasonAddressedFlag},
	}
	for _, c := range cases {
		v, r := Decide(c.ev, id)
		if v != c.want || r != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, v, r, c.want, c.reason)
		}
		if !v.Valid() || !r.Valid() {
			t.Errorf("%s: invalid result", c.name)
		}
	}
}

func TestEvidenceOf(t *testing.T) {
	m := mail.Message{FolderRole: mail.RoleInbox, SenderAddress: "s", SentRepresentingAddress: "r", AddressedToMe: "cc",
		Recipients: []mail.Recipient{{Kind: mail.To, Address: "a"}, {Kind: mail.Bcc, Address: "b"}},
		Headers:    "Received: x\r\nDelivered-To: <amelie@example.org>\r\nX-Original-To: a@x.org, b@x.org\r\nList-Id: <l.example.org>\r\n"}
	ev := EvidenceOf(&m)
	if ev.Role != mail.RoleInbox || len(ev.Recipients) != 2 || ev.AddressedToMe != "cc" || ev.SentRepresenting != "r" {
		t.Fatalf("%+v", ev)
	}
	if len(ev.DeliveredTo) != 3 || ev.DeliveredTo[0] != "amelie@example.org" || !ev.ListID {
		t.Fatalf("headers: %+v", ev)
	}
	if e := EvidenceOf(&mail.Message{}); e.ListID || e.DeliveredTo != nil {
		t.Fatal("no headers, no evidence")
	}
}

func TestValidity(t *testing.T) {
	if !Owner.Valid() || Verdict("mine").Valid() || !ReasonNoRecipients.Valid() || Reason("sent_folder").Valid() {
		t.Fatal("Valid")
	}
}
