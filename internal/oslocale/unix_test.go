//go:build !windows

package oslocale

import "testing"

func TestFromEnv(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		language string
		locale   string
	}{
		{"LC_ALL wins", map[string]string{"LC_ALL": "fr_FR.UTF-8", "LC_MESSAGES": "de_DE", "LANG": "en_US"}, "fr", "fr-FR"},
		{"LC_MESSAGES over LANG", map[string]string{"LC_MESSAGES": "fr_CA.UTF-8", "LANG": "de_DE"}, "fr", "fr-CA"},
		{"LANG", map[string]string{"LANG": "de_DE.UTF-8"}, "de", "de-DE"},
		{"LANGUAGE list", map[string]string{"LANGUAGE": "fr:en:de"}, "fr", "fr"},
		{"LANGUAGE skips garbage", map[string]string{"LANGUAGE": "C:fr_FR.UTF-8"}, "fr", "fr-FR"},
		{"C overrides lower variables", map[string]string{"LC_ALL": "C", "LANG": "fr_FR.UTF-8"}, "", ""},
		{"POSIX", map[string]string{"LANG": "POSIX"}, "", ""},
		{"empty", map[string]string{}, "", ""},
		{"unset is ignored", map[string]string{"LC_ALL": "  ", "LANG": "fr_FR.UTF-8"}, "fr", "fr-FR"},
		{"unsupported is still reported", map[string]string{"LANG": "ja_JP.UTF-8"}, "ja", "ja-JP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fromEnv(func(key string) string { return tc.env[key] })
			if got.Language != tc.language || got.Locale != tc.locale {
				t.Fatalf("fromEnv = %+v, want (%q, %q)", got, tc.language, tc.locale)
			}
		})
	}
}
