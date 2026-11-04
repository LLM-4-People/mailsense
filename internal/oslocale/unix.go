//go:build !windows

package oslocale

import (
	"os"
	"strings"
)

// detect reads the POSIX locale variables in their libc precedence order.
// A set LC_ALL wins even when it is C, because it overrides the lower
// variables for every program; an empty variable is skipped. LANGUAGE is a
// colon-separated preference list and is the last candidate.
func detect() Locale {
	return fromEnv(os.Getenv)
}

func fromEnv(getenv func(string) string) Locale {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			continue
		}
		language, locale := Parse(raw)
		if language != "" {
			return Locale{Language: language, Locale: locale}
		}
		// A set but unusable variable (C, POSIX, a malformed tag) is the
		// effective locale; lower-precedence variables must not override it.
		return Locale{}
	}
	for _, candidate := range strings.Split(getenv("LANGUAGE"), ":") {
		language, locale := Parse(candidate)
		if language != "" {
			return Locale{Language: language, Locale: locale}
		}
	}
	return Locale{}
}
