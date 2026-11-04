package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

// newToken returns a 256-bit fragment-safe bearer token.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("api: cannot generate the launch token")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newLaunchID returns the non-secret id that guards descriptor deletion.
func newLaunchID() (string, error) {
	b := make([]byte, launchBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("api: cannot generate the launch id")
	}
	return hex.EncodeToString(b), nil
}

func sameToken(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// authenticate accepts the bearer header everywhere and ?token= only on
// the SSE endpoint. When both are present they must be the same token.
func (s *Server) authenticate(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	headerToken := ""
	if header != "" {
		scheme, value, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || value == "" {
			return false
		}
		headerToken = value
	}
	var queryToken string
	var queryPresent bool
	if r.URL.Path == "/api/events" {
		if values, ok := r.URL.Query()["token"]; ok {
			queryPresent = true
			if len(values) > 0 {
				queryToken = values[0]
			}
		}
	}
	if headerToken != "" {
		if queryPresent && !sameToken(headerToken, queryToken) {
			return false
		}
		return sameToken(headerToken, s.token)
	}
	if queryPresent {
		return sameToken(queryToken, s.token)
	}
	return false
}

// registerToken puts the launch token in the redaction table before any
// other component can log it.
func registerToken(token string) { logx.RegisterSecret(token) }
