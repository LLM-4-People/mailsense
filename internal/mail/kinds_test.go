package mail

import (
	"strings"
	"testing"
)

func TestKindRulesAreValid(t *testing.T) {
	r := kinds()
	valid := map[Kind]bool{KindOrdinary: true, KindMeetingRequest: true, KindMeetingCancel: true, KindMeetingResponse: true,
		KindDeliveryReport: true, KindReadReceipt: true, KindAutoReply: true, KindRecall: true}
	for _, m := range []map[string]Kind{r.ClassPrefixes, r.ReportTypes, r.CalendarMethods, r.AutoSubmitted} {
		for k, v := range m {
			if !valid[v] || k == "" {
				t.Errorf("kinds.json: %q → %q", k, v)
			}
		}
	}
}

// #4: non-ordinary items are tagged from protocol data only.
func TestKindOf(t *testing.T) {
	cases := []struct {
		class, headers string
		want           Kind
	}{
		{"IPM.Note", "", KindOrdinary},
		{"IPM.Note.SMIME", "", KindOrdinary},
		{"ipm.schedule.meeting.resp.pos", "", KindMeetingResponse},
		{"IPM.Schedule.Meeting.Request", "", KindMeetingRequest},
		{"IPM.Schedule.Meeting.Canceled", "", KindMeetingCancel},
		{"REPORT.IPM.Note.NDR", "", KindDeliveryReport},
		{"REPORT.IPM.Note.IPNRN", "", KindReadReceipt},
		{"REPORT.IPM.Note.Something", "", KindDeliveryReport},
		{"IPM.Note.Rules.OofTemplate.Microsoft", "", KindAutoReply},
		{"", "Content-Type: multipart/report; report-type=delivery-status; boundary=x", KindDeliveryReport},
		{"", "Content-Type: multipart/report; report-type=\"disposition-notification\"", KindReadReceipt},
		{"", "Content-Type: text/calendar; method=REPLY; charset=utf-8", KindMeetingResponse},
		{"", "Auto-Submitted: auto-replied (vacation)", KindAutoReply},
		{"", "Auto-Submitted: auto-generated", KindOrdinary},
		{"", "Auto-Submitted: no", KindOrdinary},
		{"IPM.Note", "Content-Type: text/plain", KindOrdinary},
		{"", "Content-Type: garbage;;;", KindOrdinary},
	}
	for _, c := range cases {
		m := &Message{MessageClass: c.class, Headers: c.headers}
		if got := KindOf(m); got != c.want {
			t.Errorf("%q / %q: %s, want %s", c.class, c.headers, got, c.want)
		}
	}
	if !Kind("").Ordinary() || KindAutoReply.Ordinary() {
		t.Fatal("Ordinary")
	}
}

// #3: senders group by registrable domain (Public Suffix List), and a
// shared free-mail domain groups nobody.
func TestDomains(t *testing.T) {
	d := NewDomains([]string{"Mail.Example.NET", "例子.中国"})
	cases := map[string]string{
		"a@mail.example.co.uk":     "example.co.uk",
		"b@example.co.uk":          "example.co.uk",
		"c@example.net":            "", // shared
		"d@sub.example.net":        "", // shared, any subdomain
		"e@例子.中国":                  "", // shared, Unicode spelling
		"f@xn--fsqu00a.xn--fiqs8s": "", // shared, Punycode spelling
		"g@company.example.org":    "example.org",
		"no-domain":                "",
		"":                         "",
	}
	for in, want := range cases {
		if got := d.Of(in); got != want {
			t.Errorf("Of(%q) = %q, want %q", in, got, want)
		}
	}
	if RegistrableDomain("x@a.b.example.com") != "example.com" || RegistrableDomain("x") != "" {
		t.Fatal("RegistrableDomain")
	}
	if RegistrableName("a.b.example.com") != "example.com" || RegistrableName("") != "" ||
		RegistrableName("not a domain") != "" {
		t.Fatal("RegistrableName")
	}
}

// One rule for machine-sent mail (kinds.json automated), used by the gold
// export's sender kind; the Auto-Submitted keyword is read as KindOf reads it.
func TestAutomated(t *testing.T) {
	for headers, want := range map[string]bool{
		"":                                   false,
		"List-Id: <team.example.org>":        true,
		"List-Unsubscribe: <mailto:u@x.org>": true,
		"Precedence: Bulk":                   true,
		"Precedence: first-class":            false,
		"Auto-Submitted: no":                 false,
		"Auto-Submitted: No (a person)":      false,
		"Auto-Submitted: auto-generated":     true,
		"Auto-Submitted: auto-notified; x=1": true,
		"Content-Type: multipart/report; report-type=delivery-status": true,
		"X-Other: List-Id": false,
	} {
		if got := Automated(&Message{Headers: headers}); got != want {
			t.Errorf("%q: %v", headers, got)
		}
	}
	r := kinds().Automated
	for h, values := range r.Values {
		for _, v := range values {
			if v != strings.ToLower(strings.TrimSpace(v)) || h == "" {
				t.Errorf("kinds.json automated: %q=%q must be lower case", h, v)
			}
		}
	}
	if len(r.Present) == 0 || len(r.AutoSubmittedHuman) == 0 {
		t.Error("kinds.json automated is incomplete")
	}
}
