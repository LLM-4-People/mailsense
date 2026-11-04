package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ErrURLRefused is the one named failure of the outbound URL policy. Every
// request the client would make first passes this policy, so a caller can
// recognise a refused destination instead of reporting a generic network
// error.
var ErrURLRefused = errors.New("httpx: url refused")

// metadataIPv4 is the cloud instance-metadata address. It is never a
// provider or an artifact mirror, so allowing it would turn a pasted or
// configured URL into a way to read instance credentials.
var metadataIPv4 = net.ParseIP("169.254.169.254")

// linkLocalIPv6 is fe80::/10: IPv6 link-local addresses, which include the
// cloud metadata endpoints of IPv6-only networks.
var linkLocalIPv6 = &net.IPNet{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)}

// ValidateURLString parses raw and applies ValidateURL. Control characters
// are refused before parsing, since some parsers silently drop them and
// that is exactly how a host check can be bypassed.
func ValidateURLString(raw string) error {
	if strings.ContainsFunc(raw, isControl) {
		return fmt.Errorf("%w: contains a control character", ErrURLRefused)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrURLRefused, err)
	}
	return ValidateURL(u)
}

// ValidateURL applies the outbound URL policy to u:
//
//   - only http and https are allowed (file, ftp, gopher, data and every
//     other scheme are refused);
//   - the URL must name a host and must not carry credentials (a bearer
//     key or password in a URL ends up in logs and error messages);
//   - the cloud metadata address and IPv6 link-local addresses are refused.
//
// Loopback and private ranges stay allowed on purpose: llama.cpp, Ollama
// and other local runtimes listen on 127.0.0.1, and a provider may live on
// the owner's LAN.
func ValidateURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: no URL", ErrURLRefused)
	}
	switch scheme := strings.ToLower(u.Scheme); scheme {
	case "http", "https":
	default:
		return fmt.Errorf("%w: scheme %q is not http or https", ErrURLRefused, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in the URL are not allowed", ErrURLRefused)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrURLRefused)
	}
	if isMetadataHost(host) {
		return fmt.Errorf("%w: the cloud metadata address is not allowed", ErrURLRefused)
	}
	return nil
}

// isMetadataHost reports whether host is an IP literal that the policy
// refuses. A name is not resolved here: the policy is deterministic and
// does not depend on DNS (resolution can change between the check and the
// connection).
func isMetadataHost(host string) bool {
	if i := strings.IndexByte(host, '%'); i >= 0 { // an IPv6 zone ("fe80::1%eth0")
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.Equal(metadataIPv4) || linkLocalIPv6.Contains(ip)
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// requestURLSyntax is the syntax half of the policy: an absolute http(s) URL
// with a host and no credentials or whitespace. The full rule (metadata,
// IPv6 link-local, control characters) is ValidateURL; this pattern is what
// the one request builder checks before it constructs a request.
var requestURLSyntax = regexp.MustCompile(`(?i)^https?://[^/?#@\s\x00-\x1f\x7f]+([/?#][^\x00-\x1f\x7f]*)?$`)

// NewRequest is the one request builder: no adapter, discovery path or
// sidecar download may construct an outbound request with http.NewRequest
// directly. It applies the URL syntax and policy before building the
// request, and the client's transport applies the policy again on every
// attempt and redirect.
func NewRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	if !requestURLSyntax.MatchString(rawURL) {
		return nil, fmt.Errorf("%w: not an absolute http(s) URL without credentials", ErrURLRefused)
	}
	if err := ValidateURLString(rawURL); err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, method, rawURL, body)
}

// urlGuard is the outermost transport layer: every attempt of every request
// (including a redirect) is checked before the retry policy or the network
// sees it. Being part of the client, rather than a call-site rule, is what
// keeps an adapter or a discovery path from bypassing the policy.
type urlGuard struct{ next http.RoundTripper }

func (g *urlGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := ValidateURL(req.URL); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(req)
}
