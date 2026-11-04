// Package httpgzip is the one content-encoding policy of the app: whether a
// request accepts gzip and how a buffered body is compressed. The API and the
// embedded UI assets both use it, so the negotiation cannot drift between
// them.
package httpgzip

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// writers reuses gzip writers across responses. Each writer owns a 32 KiB
// window; allocating one per response is the bulk of the compression cost on
// a screen that fetches several JSON views at once.
var writers = sync.Pool{New: func() any { return gzip.NewWriter(&bytes.Buffer{}) }}

// MinBytes is the smallest body worth compressing. Below it the gzip header
// and the CPU cost outweigh the saved bytes.
const MinBytes = 1024

// Accepts reports whether the request accepts gzip: an explicit gzip entry
// decides (including q=0), and a wildcard applies only when gzip is not named.
// A missing or malformed header is refused.
func Accepts(r *http.Request) bool {
	if r == nil {
		return false
	}
	gzipQuality, wildcardQuality := -1.0, -1.0
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(part, ";")
		encoding := strings.ToLower(strings.TrimSpace(fields[0]))
		if encoding != "gzip" && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, field := range fields[1:] {
			field = strings.TrimSpace(field)
			if !strings.HasPrefix(field, "q=") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimPrefix(field, "q="), 64); err == nil {
				quality = q
			}
			break
		}
		if encoding == "gzip" {
			gzipQuality = quality
		} else {
			wildcardQuality = quality
		}
	}
	if gzipQuality >= 0 {
		return gzipQuality > 0
	}
	return wildcardQuality > 0
}

// Compress returns the gzip form of body.
func Compress(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := writers.Get().(*gzip.Writer)
	zw.Reset(&buf)
	_, err := zw.Write(body)
	if err == nil {
		err = zw.Close()
	} else {
		// Close still writes the trailer; the buffer is discarded either way.
		_ = zw.Close()
	}
	writers.Put(zw)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
