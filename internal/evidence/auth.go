package evidence

import (
	"regexp"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

var authMethod = regexp.MustCompile(`(?i)(?:^|[;\s])(spf|dkim|dmarc)\s*=\s*([a-z]+)`)

// AuthReport is what the first (topmost) Authentication-Results header
// reports: the receiving domain's authserv-id and its first result for SPF,
// DKIM and DMARC (RFC 8601). Reported is false when the message carries no
// usable header, in which case no pass/fail claim exists. The values are
// shown as reported: this type never turns a display name or an absent
// result into a verification.
type AuthReport struct {
	Reported bool
	Source   string
	SPF      string
	DKIM     string
	DMARC    string
}

// ReportedAuth parses the authentication a receiving domain reported for a
// raw header block. It is the one parser of Authentication-Results: the
// auth_* evidence signals read their value through the same header parse,
// so the sender view and evidence cannot disagree.
func ReportedAuth(headerBlock string) AuthReport {
	return ReportedAuthCut(headerBlock, false)
}

// ReportedAuthCut is ReportedAuth for a block the source cut at an
// arbitrary point: the cut's last logical line is dropped before parsing,
// exactly as the evidence Input does.
func ReportedAuthCut(headerBlock string, cut bool) AuthReport {
	var headers mail.Headers
	if cut {
		headers = mail.ParseCutHeaders(headerBlock)
	} else {
		headers = mail.ParseHeaders(headerBlock)
	}
	vals := headers.Values(hdrAuthResults)
	if len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
		return AuthReport{}
	}
	parsed := parseAuthHeader(vals[0])
	return AuthReport{Reported: true, Source: parsed.source,
		SPF: parsed.results["spf"], DKIM: parsed.results["dkim"], DMARC: parsed.results["dmarc"]}
}

// authHeader is the parsed form of one Authentication-Results value: the
// reporting authserv-id and the first result per method.
type authHeader struct {
	source  string
	results map[string]string
}

// parseAuthHeader is the one reader of an Authentication-Results value.
func parseAuthHeader(value string) authHeader {
	out := authHeader{results: map[string]string{}}
	if id, _, ok := strings.Cut(value, ";"); ok {
		out.source = strings.TrimSpace(id)
	} else {
		out.source = strings.TrimSpace(value)
	}
	for _, m := range authMethod.FindAllStringSubmatch(value, -1) {
		method, result := strings.ToLower(m[1]), strings.ToLower(m[2])
		if _, ok := out.results[method]; !ok {
			out.results[method] = result
		}
	}
	return out
}

// authResult is the result of one method in the first (topmost)
// Authentication-Results header — the one the receiving domain added
// (RFC 8601 §5: earlier ones can be forged by the sender): its first
// result for the method, "absent" when the method or the header is
// missing (the header absent from a cut block: partial).
func authResult(in *Input, method string) Value {
	vals, missing, ok := in.Header(hdrAuthResults)
	if !ok {
		return missing
	}
	if len(vals) == 0 {
		return Str("absent").Cut(in.HeadersCut())
	}
	if result := parseAuthHeader(vals[0]).results[strings.ToLower(method)]; result != "" {
		return Str(inVocabulary(result, authValues[1:9]))
	}
	return Str("absent")
}

func authSignal(method, doc string) Signal {
	return Signal{Unsure: map[string][]string{"absent": nil}, Name: "auth_" + method, Version: 1, Type: TypeEnum, Values: authValues, Needs: []string{"headers"}, Causal: true,
		Group: GroupHeaders, Doc: doc, Extract: func(in *Input) Value { return authResult(in, method) }}
}
