package httpgzip

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAccepts(t *testing.T) {
	cases := map[string]bool{
		"":                    false,
		"gzip":                true,
		"GZip":                true,
		"br, gzip;q=0.5":      true,
		"gzip;q=0":            false,
		"gzip;q=0.0":          false,
		"br":                  false,
		"*":                   true,
		"*;q=0":               false,
		"deflate, br, gzip":   true,
		"gzip;q=0, *;q=0.5":   false,
		"*;q=0.5, gzip;q=1":   true,
		"gzip;q=0.5, *;q=0":   true,
		"*;q=0.5":             true,
		"br;q=1, gzip;q=0.01": true,
	}
	for header, want := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			req.Header.Set("Accept-Encoding", header)
		}
		if got := Accepts(req); got != want {
			t.Errorf("Accepts(%q) = %v, want %v", header, got, want)
		}
	}
	if Accepts(nil) {
		t.Error("a nil request accepted gzip")
	}
}

func TestCompressRoundTrip(t *testing.T) {
	body := []byte(strings.Repeat("mailsense ", 500))
	compressed, err := Compress(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) >= len(body) {
		t.Fatalf("compressed %d bytes, plain %d", len(compressed), len(body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("round trip changed the body")
	}
}

// TestCompressReusesWritersUnderConcurrency: the pooled writer is reset for
// every body, so concurrent responses must still round-trip independently.
func TestCompressReusesWritersUnderConcurrency(t *testing.T) {
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte(strings.Repeat(fmt.Sprintf("mailsense-%d ", i), 400))
			compressed, err := Compress(body)
			if err != nil {
				t.Errorf("compress %d: %v", i, err)
				return
			}
			zr, err := gzip.NewReader(bytes.NewReader(compressed))
			if err != nil {
				t.Errorf("reader %d: %v", i, err)
				return
			}
			got, err := io.ReadAll(zr)
			if err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			if !bytes.Equal(got, body) {
				t.Errorf("round trip %d changed the body", i)
			}
		}(i)
	}
	wg.Wait()
}
