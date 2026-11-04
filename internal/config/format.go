package config

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/clock"
)

var pairNameRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)

// compileFormat returns the validator for a schema "format":
//
//	regexp      the value must compile as a Go regular expression
//	http_url    empty, or an absolute http/https URL with a host
//	time_zone   empty (not confirmed yet) or an IANA zone name
//	pair:<key>  name=value where value is valid for setting <key>
//	one_of:<v>  one value of the schema's vocabulary <v>
//	keyed:<v>   <anything without "=">=<a value of vocabulary v>
func compileFormat(format string, s *Schema) (func(string) error, error) {
	switch {
	case strings.HasPrefix(format, "one_of:") || strings.HasPrefix(format, "keyed:"):
		kind, name, _ := strings.Cut(format, ":")
		vocab, ok := s.vocabularies[name]
		if !ok {
			return nil, fmt.Errorf("format %q names no vocabulary", format)
		}
		return func(v string) error {
			if kind == "keyed" {
				k, value, found := strings.Cut(v, "=")
				if !found || k == "" {
					return fmt.Errorf("must be <name>=<%s>, got %q", name, v)
				}
				v = value
			}
			if !slices.Contains(vocab, v) {
				return fmt.Errorf("%q is not a %s (%s)", v, name, strings.Join(vocab, ", "))
			}
			return nil
		}, nil
	case format == "regexp":
		return func(v string) error {
			if _, err := regexp.Compile(v); err != nil {
				return fmt.Errorf("invalid regular expression: %v", err)
			}
			return nil
		}, nil
	case format == "http_url":
		return checkHTTPURL, nil
	case format == "time_zone":
		return func(v string) error {
			if v == "" {
				return nil
			}
			_, err := clock.Zone(v)
			return err
		}, nil
	case strings.HasPrefix(format, "pair:"):
		ref, ok := s.byKey[strings.TrimPrefix(format, "pair:")]
		if !ok || ref.Type == TypeList {
			return nil, fmt.Errorf("format %q must reference a scalar setting", format)
		}
		return func(v string) error {
			name, value, found := strings.Cut(v, "=")
			if !found || !pairNameRE.MatchString(name) {
				return fmt.Errorf("must be name=value with a lower-case name, got %q", v)
			}
			if _, err := ref.rule.fromEnv(value); err != nil {
				return fmt.Errorf("value of %q: %v", name, err)
			}
			return nil
		}, nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

func checkHTTPURL(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be empty or an absolute http(s) URL")
	}
	return nil
}
