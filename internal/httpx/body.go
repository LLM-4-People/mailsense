package httpx

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// ReadBody reads a response body up to http.max_response_mb (a memory
// safety limit for every adapter); a larger body is an error.
func ReadBody(cfg *config.Config, resp *http.Response) ([]byte, error) {
	limit := int64(cfg.Int("http.max_response_mb")) << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(raw)) > limit {
		err = fmt.Errorf("response larger than %d MiB (http.max_response_mb)", limit>>20)
	}
	return raw, err
}

// SetBearer sets the bearer Authorization of a request ("" = none).
func SetBearer(req *http.Request, key string) {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// ErrorText is a server's error body as an error quotes it: trimmed and
// at most http.error_text_chars code points (never a split character).
func ErrorText(cfg *config.Config, body []byte) string {
	return mail.Head(strings.TrimSpace(string(body)), cfg.Int("http.error_text_chars"))
}
