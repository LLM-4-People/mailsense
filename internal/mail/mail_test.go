package mail

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

func TestAddressHelpers(t *testing.T) {
	if NormalizeAddress("  amelie.durand@example.com ") != "amelie.durand@example.com" {
		t.Fatal("normalize")
	}
	if NormalizeAddress("INFO@X.TR") != "info@x.tr" {
		t.Fatal("locale-independent lowering (no Turkish dotless i)")
	}
	for s, want := range map[string]bool{
		"/O=EXCHANGELABS/OU=EXCHANGE/CN=RECIPIENTS/CN=X": true, " /cn=x": true, "/ou=y": true,
		"amelie@x.com": false, "o=x": false, "": false, "/x=y": false,
	} {
		if IsLegacyDN(s) != want {
			t.Errorf("IsLegacyDN(%q) != %v", s, want)
		}
	}
	for s, want := range map[string]string{"A@B.com": "b.com", "a@b@c.org": "c.org", "/o=x": "", "": ""} {
		if Domain(s) != want {
			t.Errorf("Domain(%q) = %q", s, Domain(s))
		}
	}
	got := SplitAddresses(" a@x.com, B@y.com;;c@z.com ,")
	if strings.Join(got, "|") != "a@x.com|B@y.com|c@z.com" || SplitAddresses("") != nil {
		t.Fatalf("split: %q", got)
	}
}

func TestNormalizeSenderQuery(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{"exact", "  Demo@Example.Test ", "demo@example.test", false},
		{"unicode", " ÉLODIE@EXEMPLE.TEST ", "élodie@exemple.test", false},
		{"domain at", " @Example.Test ", "@example.test", false},
		{"bare domain", "Example.Test", "example.test", false},
		{"plus tag", "elodie+tag@example.org", "elodie+tag@example.org", false},
		{"max runes", strings.Repeat("a", MaxSenderQueryRunes), strings.Repeat("a", MaxSenderQueryRunes), false},
		{"over max runes", strings.Repeat("a", MaxSenderQueryRunes+1), "", true},
		{"empty", "", "", true},
		{"blank", "   ", "", true},
		{"lone at", "@", "", true},
		{"control", "a\x00b@example.org", "", true},
		{"leading control", "\x00a@example.org", "", true},
		{"trailing control", "a@example.org\x00", "", true},
		{"newline", "a\nb@example.org", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSenderQuery(tc.in)
			if tc.bad {
				if !errors.Is(err, ErrInvalidSenderQuery) {
					t.Fatalf("%q: error %v, want ErrInvalidSenderQuery", tc.in, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("%q: got %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestSubjectNormalizerLegacyAndConfigured(t *testing.T) {
	n, err := NewSubjectNormalizer(1, []string{"re", "fw", "fwd", "aw", "tr", "sv", "vs", "ref", "enc", "rif", "vb"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"RE: Hello  World":  "hello world",
		"  tr : Réunion":    "réunion",
		"Re: Fwd: Thing":    "fwd: thing", // max 1: ONE prefix
		"Ref:x":             "x",
		"Reply: no":         "reply: no",
		"":                  "",
		"Regarding: budget": "regarding: budget",
	}
	for in, want := range cases {
		if got := n.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if !n.HasPrefix("Fw: x") || n.HasPrefix("x") {
		t.Fatal("HasPrefix")
	}
	stacked, _ := NewSubjectNormalizer(5, []string{"re", "fwd"})
	if got := stacked.Normalize("Re: Fwd: RE: Thing"); got != "thing" {
		t.Fatalf("stacked: %q", got)
	}
	if _, err := NewSubjectNormalizer(1); err == nil {
		t.Fatal("empty prefixes must be rejected")
	}
	if _, err := NewSubjectNormalizer(0, []string{"re"}); err == nil {
		t.Fatal("max 0 must be rejected")
	}
	odd, _ := NewSubjectNormalizer(1, []string{"a.b"})
	if odd.Normalize("axb: y") != "axb: y" {
		t.Fatal("prefixes are literal, not regex")
	}
}

// #7: multilingual prefixes are data; matching folds width (NFKC: ＲＥ：,
// the full-width colon), ignores bidi marks, accepts counters and removes
// stacked prefixes; learned prefixes join the shipped ones.
func TestSubjectNormalizerMultilingual(t *testing.T) {
	shipped := []string{"re", "aw", "wg", "rv", "odp", "tr", "ΑΠ", "رد", "השב", "回复", "转发", "회신", "fw", "fwd"}
	n, err := NewSubjectNormalizer(10, shipped, []string{"svar"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"WG: AW: Angebot":           "angebot",
		"RV: Factura":               "factura",
		"Odp: Raport":               "raport",
		"ΑΠ: Συνάντηση":             "συνάντηση",
		"رد: الفاتورة":              "الفاتورة",
		"‏رد: الفاتورة":             "الفاتورة", // a right-to-left mark before the prefix
		"השב: פגישה":                "פגישה",
		"回复：项目进度报告":                 "项目进度报告",
		"转发: 回复: 报告":                "报告",
		"회신: 견적서":                   "견적서",
		"ＲＥ：Ｂｕｄｇｅｔ":                 "budget", // full-width letters and colon
		"RE[2]: AW(3): Re^4: Thing": "thing",
		"svar: learned":             "learned",
		"Re: Fwd: TR: RE: x":        "x",
		"Regarding: budget":         "regarding: budget",
		"回复您的问题":                    "回复您的问题", // no colon, not a prefix
	}
	for in, want := range cases {
		if got := n.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if !n.HasPrefix("回复：x") || n.HasPrefix("回复x") {
		t.Fatal("HasPrefix")
	}
	if got := LeadingWord("Ｖｓ： kokous", 12); got != "vs" {
		t.Fatalf("LeadingWord %q", got)
	}
	if LeadingWord("no colon here", 12) != "" || LeadingWord("a very long leading phrase indeed: x", 12) != "" {
		t.Fatal("LeadingWord candidates are short words before a colon")
	}
}

func TestTimes(t *testing.T) {
	r := TimesFromConfig(configtest.New(t, nil).Snapshot())
	cases := map[string]string{
		"2026-10-12T15:48:34Z":          "2026-10-12T15:48:34Z",
		"2026-10-12T11:48:34-04:00":     "2026-10-12T15:48:34Z",
		"2026-10-12T15:48:34.999+00:00": "2026-10-12T15:48:34Z",
		"4501-01-01T00:00:00Z":          "",
		"4500-01-01T00:00:00Z":          "",
		"1900-12-31T23:59:59Z":          "",
		"1901-01-01T00:00:00Z":          "1901-01-01T00:00:00Z",
		"":                              "",
		"  ":                            "",
	}
	for in, want := range cases {
		got, err := r.Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"12/10/2026", "2026-10-12", "2026-13-01T00:00:00Z", "1402-07-20T10:00:00", "garbage"} {
		if _, err := r.Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) must fail", bad)
		}
	}
	if r.Format(time.Time{}) != "" {
		t.Fatal("zero time is no date")
	}
	if _, ok := Parse("2026-10-12T15:48:34+00:00"); ok {
		t.Fatal("Parse accepts only the stored form")
	}
}

func TestOptInt(t *testing.T) {
	for in, want := range map[string]OptInt{"": {}, " 12 ": Int(12), "-1": Int(-1)} {
		got, err := ParseOptInt(in)
		if err != nil || got != want {
			t.Errorf("ParseOptInt(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"1.5", "abc", "0x10", "true"} {
		if _, err := ParseOptInt(bad); err == nil {
			t.Errorf("ParseOptInt(%q) must fail", bad)
		}
	}
	var o OptInt
	if err := o.Scan(int64(5)); err != nil || o != Int(5) {
		t.Fatal("scan")
	}
	if err := o.Scan(nil); err != nil || o.Valid {
		t.Fatal("scan nil")
	}
	if err := o.Scan("x"); err == nil {
		t.Fatal("scan of a string must fail")
	}
	b, _ := json.Marshal([]OptInt{{}, Int(3)})
	if string(b) != "[null,3]" {
		t.Fatalf("json: %s", b)
	}
	var back []OptInt
	if err := json.Unmarshal(b, &back); err != nil || back[0].Valid || back[1] != Int(3) {
		t.Fatalf("json back: %v %v", back, err)
	}
	if v, _ := (OptInt{}).Value(); v != nil {
		t.Fatal("unset must be NULL")
	}
}

func TestValidate(t *testing.T) {
	ok := Message{SourceID: "s", ID: "1", FolderPath: "inbox", FolderRole: RoleInbox, ReceivedAt: "2026-01-01T00:00:00Z",
		Recipients: []Recipient{{Kind: To, Address: "a@b"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := Message{FolderRole: "spam", ReceivedAt: "2026-01-01", Recipients: []Recipient{{Kind: "x"}}}
	err := bad.Validate()
	for _, want := range []string{"source id", "message id", "folder role", "folder path", "recipient 0", "received_at"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if _, err := ParseRole("inbox"); err != nil {
		t.Fatal(err)
	}
	roles := Roles()
	roles[0] = "mutated"
	if Roles()[0] != RoleInbox {
		t.Fatal("Roles must return a fresh slice")
	}
	if f := FolderOf(&ok); f.Path != "inbox" || f.ItemCount != -1 {
		t.Fatalf("FolderOf: %+v", f)
	}
}
