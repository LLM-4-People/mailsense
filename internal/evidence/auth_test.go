package evidence

import "testing"

func TestReportedAuthUsesTheTopmostHeader(t *testing.T) {
	block := "Received: from mx\n" +
		"Authentication-Results: mx.example.net; spf=pass smtp.mailfrom=example.org; dkim=fail header.d=example.org\n" +
		"Authentication-Results: forged.example; spf=fail; dkim=pass; dmarc=pass\n"
	got := ReportedAuth(block)
	if !got.Reported || got.Source != "mx.example.net" {
		t.Fatalf("report %+v", got)
	}
	if got.SPF != "pass" || got.DKIM != "fail" || got.DMARC != "" {
		t.Fatalf("first methods win: %+v", got)
	}
}

func TestReportedAuthAbsentOrMalformedMakesNoClaim(t *testing.T) {
	for _, block := range []string{"", "Subject: hello", "Authentication-Results: \n"} {
		got := ReportedAuth(block)
		if got.Reported || got.Source != "" || got.SPF != "" || got.DKIM != "" || got.DMARC != "" {
			t.Fatalf("%q: %+v", block, got)
		}
	}
	got := ReportedAuth("Authentication-Results: mx.example.net; DMARC=bestguesspass\n")
	if !got.Reported || got.Source != "mx.example.net" || got.DMARC != "bestguesspass" {
		t.Fatalf("reported values stay as reported: %+v", got)
	}
}

func TestReportedAuthCutBlockDropsTheFragment(t *testing.T) {
	block := "Received: from mx\nAuthentication-Results: mx.example.net; spf=pass; dmarc=fa"
	if got := ReportedAuthCut(block, true); got.Reported {
		t.Fatalf("the cut fragment became an authentication claim: %+v", got)
	}
	if got := ReportedAuthCut(block, false); !got.Reported || got.DMARC != "fa" {
		t.Fatalf("uncut block is read whole: %+v", got)
	}
}
