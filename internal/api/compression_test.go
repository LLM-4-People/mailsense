package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestWriteJSONCompressesLargeBodies(t *testing.T) {
	srv := &Server{log: slog.Default()}
	payload := map[string]string{"body": strings.Repeat("x", 4096)}

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.writeJSON(rec, req, http.StatusOK, payload)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("encoding %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("vary %q", got)
	}
	// The recorder body is consumed by the gzip reader, so the wire length
	// must be captured first.
	compressedLen := rec.Body.Len()
	if n := rec.Header().Get("Content-Length"); n != strconv.Itoa(compressedLen) {
		t.Fatalf("content length %q, compressed body %d", n, compressedLen)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["body"] != payload["body"] {
		t.Fatal("the compressed response changed the JSON")
	}

	plainReq := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	plain := httptest.NewRecorder()
	srv.writeJSON(plain, plainReq, http.StatusOK, payload)
	if enc := plain.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("a request without Accept-Encoding got %q", enc)
	}
	if n := plain.Header().Get("Content-Length"); n != strconv.Itoa(plain.Body.Len()) {
		t.Fatalf("plain content length %q, body %d", n, plain.Body.Len())
	}
	if plain.Body.Len() <= compressedLen {
		t.Fatalf("plain %d bytes, compressed %d: gzip did not shrink the body", plain.Body.Len(), compressedLen)
	}

	smallReq := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	smallReq.Header.Set("Accept-Encoding", "gzip")
	small := httptest.NewRecorder()
	srv.writeJSON(small, smallReq, http.StatusOK, map[string]int{"n": 1})
	if enc := small.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("a small body was compressed with %q", enc)
	}
}

func TestAPIRouteCompressesALargeList(t *testing.T) {
	srv, ts, w := newTestServer(t)
	rows := make([]MessageListRow, 100)
	for i := range rows {
		rows[i] = MessageListRow{
			RowID: int64(i + 1), Subject: "Synthetic subject", SenderName: "Élodie Martin",
			SenderAddress: "elodie@example.org", Instant: "2026-03-01T09:00:00Z",
			ReceivedAt: "2026-03-01T09:00:00Z", Role: "inbox", Snippet: strings.Repeat("snippet ", 8),
		}
	}
	w.messages.list = MessageListView{Messages: rows, Total: len(rows)}

	status, headers, plain := do(t, ts, srv, request{method: http.MethodGet, path: "/api/messages?limit=100"})
	mustStatus(t, status, http.StatusOK, plain)
	if got := headers.Get("Content-Encoding"); got != "" {
		t.Fatalf("an unnegotiated request got %q", got)
	}
	if headers.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("vary %q", headers.Get("Vary"))
	}

	status, headers, compressed := do(t, ts, srv, request{method: http.MethodGet, path: "/api/messages?limit=100",
		headers: map[string]string{"Accept-Encoding": "gzip"}})
	mustStatus(t, status, http.StatusOK, compressed)
	if got := headers.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("encoding %q, want gzip", got)
	}
	if got := headers.Get("Content-Length"); got != strconv.Itoa(len(compressed)) {
		t.Fatalf("content length %q, body %d", got, len(compressed))
	}
	if len(compressed) >= len(plain) {
		t.Fatalf("gzip %d bytes, plain %d", len(compressed), len(plain))
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var got MessageListView
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != len(rows) || got.Messages[0].Subject != rows[0].Subject {
		t.Fatalf("the compressed list changed: %d rows", len(got.Messages))
	}
}
