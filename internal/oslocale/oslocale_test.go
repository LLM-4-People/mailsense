package oslocale

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in       string
		language string
		locale   string
	}{
		{"fr_FR.UTF-8", "fr", "fr-FR"},
		{"fr_FR.utf8", "fr", "fr-FR"},
		{"fr-FR", "fr", "fr-FR"},
		{"fr_CA", "fr", "fr-CA"},
		{"en_US.UTF-8@euro", "en", "en-US"},
		{"de_DE", "de", "de-DE"},
		{"sr_RS@latin", "sr", "sr-RS"},
		{"zh_CN.UTF-8", "zh", "zh-CN"},
		{"es_419", "es", "es-419"},
		{"fr", "fr", "fr"},
		{"  fr_FR  ", "fr", "fr-FR"},
		{"", "", ""},
		{"C", "", ""},
		{"C.UTF-8", "", ""},
		{"POSIX", "", ""},
		{"x", "", ""},
		{"1234", "", ""},
		{"not a locale", "", ""},
	}
	for _, tc := range cases {
		language, locale := Parse(tc.in)
		if language != tc.language || locale != tc.locale {
			t.Errorf("Parse(%q) = (%q, %q), want (%q, %q)", tc.in, language, locale, tc.language, tc.locale)
		}
	}
}

func TestDetectNeverPanics(t *testing.T) {
	_ = Detect()
	_ = Detect() // cached
}
