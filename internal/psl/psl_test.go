package psl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The shipped data is exactly the pinned file (scripts/update-psl.sh).
func TestTheShippedListIsThePinnedOne(t *testing.T) {
	raw, err := os.ReadFile("pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Commit, SHA256 string
		Bytes          int
	}
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(listData))
	if hex.EncodeToString(sum[:]) != pin.SHA256 || len(listData) != pin.Bytes || len(pin.Commit) != 40 {
		t.Fatal("public_suffix_list.dat differs from pin.json: update it with scripts/update-psl.sh only")
	}
	// Review L0 rules §1.7: no rule is dropped silently.
	if u := Unusable(); len(u) != 0 {
		t.Fatalf("rules that cannot be read: %q", u)
	}
	if l := parse("com\n*.a..b\n"); len(l.unusable) != 1 || l.rules["com"] != ruleNormal {
		t.Fatalf("an unreadable rule is listed, not dropped: %+v", l)
	}
}

// Review L0 bugs #12 (failed first: "0.1", "1.10]", one label): an IP
// address literal is its own key, and the ideographic full stop separates
// labels (RFC 3490 §3.1).
func TestIPLiteralsAndIdeographicStops(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.1":           "10.0.0.1",
		"192.168.1.10":       "192.168.1.10",
		"[192.168.1.10]":     "[192.168.1.10]",
		"[IPv6:2001:DB8::1]": "[ipv6:2001:db8::1]",
		"例え。テスト":             Registrable("例え.テスト"),
		"例え｡テスト":             Registrable("例え.テスト"),
		"mail。example。co。uk": "example.co.uk",
		"[not an ip]":        "",
	} {
		if got := Registrable(in); got != want {
			t.Errorf("Registrable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegistrable(t *testing.T) {
	cases := map[string]string{
		"example.com":                 "example.com",
		"Mail.Example.COM.":           "example.com",
		"a.b.example.co.uk":           "example.co.uk",
		"gmail.com":                   "gmail.com",
		"co.uk":                       "co.uk", // a public suffix itself
		"com":                         "com",
		"localhost":                   "localhost",
		"foo.bar.ck":                  "foo.bar.ck", // *.ck: bar.ck is a suffix
		"www.ck":                      "www.ck",     // !www.ck: an exception
		"a.www.ck":                    "www.ck",
		"x.unknowntld":                "x.unknowntld", // default rule *
		"例子.中国":                       "xn--fsqu00a.xn--fiqs8s",
		"mail.例子.中国":                  "xn--fsqu00a.xn--fiqs8s",
		"mail.xn--fsqu00a.xn--fiqs8s": "xn--fsqu00a.xn--fiqs8s", // the Punycode spelling is the same domain
		"пример.рф":                   "xn--e1afmkfd.xn--p1ai",
		"ｅｘａｍｐｌｅ．ｃｏｍ":                 "example.com", // full-width letters and dot fold (NFKC)
		"":                            "",
		"a..b":                        "",
		".":                           "",
	}
	for in, want := range cases {
		if got := Registrable(in); got != want {
			t.Errorf("Registrable(%q) = %q, want %q", in, got, want)
		}
	}
}

// RFC 3492 §7.1 sample strings.
func TestPunycodeVectors(t *testing.T) {
	cases := map[string]string{
		"bücher":            "xn--bcher-kva",
		"中国":                "xn--fiqs8s",
		"ليهمابتكلموشعربي؟": "xn--egbpdaj6bu4bxfgehfvwxn",
		"他们为什么不说中文":         "xn--ihqwcrb4cv8a8dqg056pqjye",
		"example":           "example",
	}
	for in, want := range cases {
		if got, err := toASCII(in); err != nil || got != want {
			t.Errorf("toASCII(%q) = %q %v, want %q", in, got, err, want)
		}
		if back, ok := Unicode(want); !ok || back != in {
			t.Errorf("Unicode(%q) = %q %v, want %q", want, back, ok, in)
		}
	}
	if u, ok := Unicode("mail.xn--bcher-kva.example"); !ok || u != "mail.bücher.example" {
		t.Errorf("a host: %q %v", u, ok)
	}
	for _, bad := range []string{"xn--", "xn--ü-x", "xn--99999999999a", "xn--a-!"} {
		if u, ok := Unicode(bad); ok {
			t.Errorf("%q must not decode (%q)", bad, u)
		}
	}
}
