//go:build windows

package oslocale

import "testing"

// Windows LANGIDs in the table are the stable primary-language identifiers
// the UI-language API returns. The fake resolver mirrors what
// LCIDToLocaleName returns for them; the second half of the test asks the
// real API when it is available.
func TestWindowsLANGIDTable(t *testing.T) {
	names := map[uint32]string{
		0x040C: "fr-FR",
		0x0409: "en-US",
		0x0407: "de-DE",
	}
	resolve := func(lcid uint32) string { return names[lcid] }
	cases := []struct {
		langid   uint16
		language string
		locale   string
	}{
		{0x040C, "fr", "fr-FR"},
		{0x0409, "en", "en-US"},
		{0x0407, "de", "de-DE"},
	}
	for _, tc := range cases {
		language, locale := Parse(localeForLANGID(tc.langid, resolve))
		if language != tc.language || locale != tc.locale {
			t.Errorf("LANGID %#04x = (%q, %q), want (%q, %q)", tc.langid, language, locale, tc.language, tc.locale)
		}
	}
	if got := localeForLANGID(0x040C, func(uint32) string { return "" }); got != "" {
		t.Errorf("failed resolver = %q, want empty", got)
	}
}

func TestWindowsRealLCIDNames(t *testing.T) {
	for langid, want := range map[uint16]string{0x040C: "fr-FR", 0x0409: "en-US", 0x0407: "de-DE"} {
		name := localeForLANGID(langid, lcidToLocaleName)
		if name == "" {
			t.Logf("LCIDToLocaleName unavailable for %#04x; regional fallback is used", langid)
			continue
		}
		language, locale := Parse(name)
		if language != want[:2] || locale != want {
			t.Errorf("LANGID %#04x = (%q, %q), want (%q)", langid, language, locale, want)
		}
	}
}

func TestDetectWindowsNeverPanics(t *testing.T) {
	_ = detect()
}
