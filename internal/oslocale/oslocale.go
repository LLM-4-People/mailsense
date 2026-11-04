// Package oslocale reports the operating-system interface language and
// regional locale once per process. It is the authority for the automatic
// interface language on Windows: WebView2's navigator.language can disagree
// with the Windows display language, so the client only uses it as a
// fallback when detection returns nothing.
//
// Detection never fails the app. An unsupported or unreadable locale is
// reported as an empty Locale, and the caller falls back to English.
package oslocale

import (
	"strings"
	"sync"
)

// Locale is one detected system language. Language is the two- or
// three-letter base used for catalog selection ("fr"); Locale is the
// normalized BCP-47 tag used for formatting ("fr-FR"). Both are empty when
// detection found nothing usable.
type Locale struct {
	Language string `json:"language"`
	Locale   string `json:"locale"`
}

// Detect returns the process-wide system locale. The result is computed
// once: a desktop-session language change takes effect at the next launch.
func Detect() Locale {
	detectOnce.Do(func() { detected = detect() })
	return detected
}

var (
	detectOnce sync.Once
	detected   Locale
)

// Parse normalizes one POSIX or Windows locale name:
//
//	fr_FR.UTF-8  -> fr, fr-FR
//	fr-CA        -> fr, fr-CA
//	en           -> en, en
//	zh_CN.UTF-8  -> zh, zh-CN
//	C / POSIX / empty / garbage -> "", ""
//
// It strips the encoding and modifier, accepts an optional script subtag,
// and keeps the first region. It never guesses a region: a bare "fr" stays
// "fr", so the formatter can use the language's default locale.
func Parse(name string) (language, locale string) {
	s := strings.TrimSpace(name)
	if s == "" {
		return "", ""
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "_", "-")
	if s == "" || strings.EqualFold(s, "C") || strings.EqualFold(s, "POSIX") {
		return "", ""
	}
	parts := strings.Split(s, "-")
	lang := strings.ToLower(parts[0])
	if !alpha(lang) || len(lang) < 2 || len(lang) > 3 {
		return "", ""
	}
	region := ""
	for _, part := range parts[1:] {
		switch {
		case len(part) == 2 && alpha(part):
			region = strings.ToUpper(part)
		case len(part) == 3 && digits(part):
			region = part
		}
		if region != "" {
			break
		}
	}
	if region == "" {
		return lang, lang
	}
	return lang, lang + "-" + region
}

func alpha(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return s != ""
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
