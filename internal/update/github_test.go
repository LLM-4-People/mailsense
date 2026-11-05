package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is the release endpoint for tests: it counts requests, records
// the conditional header of each and serves whatever handler is installed
// (a mutable handler lets one test switch a healthy answer into a failure).
type fakeGitHub struct {
	mu           sync.Mutex
	url          string
	requests     int // the latest-release endpoint
	listRequests int // the release-list endpoint
	seenETags    []string
	handler      func(w http.ResponseWriter, r *http.Request)
	listHandler  func(w http.ResponseWriter, r *http.Request)
}

func newFakeGitHub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*fakeGitHub, *httptest.Server) {
	t.Helper()
	f := &fakeGitHub{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f, srv
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	var handler func(w http.ResponseWriter, r *http.Request)
	switch r.URL.Path {
	case latestPath:
		f.requests++
		f.seenETags = append(f.seenETags, r.Header.Get("If-None-Match"))
		handler = f.handler
	case releasesListPath:
		f.listRequests++
		handler = f.listHandler
	default:
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	f.mu.Unlock()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	handler(w, r)
}

func (f *fakeGitHub) setHandler(handler func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	f.handler = handler
	f.mu.Unlock()
}

func (f *fakeGitHub) setListHandler(handler func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	f.listHandler = handler
	f.mu.Unlock()
}

func (f *fakeGitHub) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listRequests
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeGitHub) etags() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenETags...)
}

// releaseJSON renders the fields of a GitHub release the app reads.
func releaseJSON(tag, body string) string {
	return fmt.Sprintf(`{
		"tag_name": %q,
		"body": %q,
		"html_url": "https://github.com/LLM-4-People/mailsense/releases/tag/%s",
		"published_at": "2026-11-04T10:00:00Z",
		"assets": [
			{"name": "mailsense-0.2.0-setup.exe", "size": 12345678, "browser_download_url": "https://github.com/download/0.2.0"},
			{"name": "", "size": 1, "browser_download_url": "https://github.com/download/junk"}
		],
		"author": {"login": "ignored"},
		"draft": false
	}`, tag, body, tag)
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

func TestCheckerLatestDecodesRelease(t *testing.T) {
	f, _ := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		jsonOK(w, releaseJSON("v0.2.0", "The release notes"))
	})
	checker := NewChecker(&http.Client{Timeout: 5 * time.Second}, f.url)
	result, err := checker.Latest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.NotModified {
		t.Fatal("a 200 answer was reported as not modified")
	}
	if result.ETag != `"v1"` {
		t.Fatalf("etag = %q", result.ETag)
	}
	rel := result.Release
	if rel == nil {
		t.Fatal("no release decoded")
	}
	if rel.Tag != "v0.2.0" || rel.Version != "0.2.0" {
		t.Fatalf("tag/version = %q/%q", rel.Tag, rel.Version)
	}
	if rel.BodyText != "The release notes" {
		t.Fatalf("notes = %q", rel.BodyText)
	}
	if rel.HTMLURL != "https://github.com/LLM-4-People/mailsense/releases/tag/v0.2.0" || rel.PublishedAt != "2026-11-04T10:00:00Z" {
		t.Fatalf("url/published = %q/%q", rel.HTMLURL, rel.PublishedAt)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Name != "mailsense-0.2.0-setup.exe" || rel.Assets[0].Size != 12345678 {
		t.Fatalf("assets = %+v (the empty-named asset must be dropped)", rel.Assets)
	}
	if tags := f.etags(); len(tags) != 1 || tags[0] != "" {
		t.Fatalf("first request sent If-None-Match %v", tags)
	}
}

func TestCheckerLatestNotModified(t *testing.T) {
	f, _ := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		jsonOK(w, releaseJSON("v0.2.0", "notes"))
	})
	checker := NewChecker(&http.Client{Timeout: 5 * time.Second}, f.url)
	first, err := checker.Latest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.NotModified || first.Release == nil {
		t.Fatalf("first answer = %+v", first)
	}
	second, err := checker.Latest(context.Background(), first.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if !second.NotModified || second.Release != nil {
		t.Fatalf("second answer = %+v", second)
	}
	if second.ETag != `"v1"` {
		t.Fatalf("304 etag = %q", second.ETag)
	}
	if got := f.etags(); len(got) != 2 || got[1] != `"v1"` {
		t.Fatalf("conditional headers = %v", got)
	}
}

func TestCheckerLatestPlainTextFallback(t *testing.T) {
	f, _ := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, `{"tag_name":"v0.2.0","body":"<p>Hello <strong>world</strong> &amp; more</p><!-- comment -->\r\nsecond"}`)
	})
	checker := NewChecker(&http.Client{Timeout: 5 * time.Second}, f.url)
	result, err := checker.Latest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Release.BodyText, "Hello world & more\nsecond"; got != want {
		t.Fatalf("notes = %q, want %q", got, want)
	}
}

func TestCheckerLatestErrors(t *testing.T) {
	tooLarge := strings.Repeat("a", maxReleaseBytes+1)
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		check   func(t *testing.T, err error)
	}{
		{
			name:    "malformed json",
			handler: func(w http.ResponseWriter, r *http.Request) { jsonOK(w, `{"tag_name":`) },
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("err = %v, want ErrInvalidResponse", err)
				}
			},
		},
		{
			name:    "missing tag",
			handler: func(w http.ResponseWriter, r *http.Request) { jsonOK(w, `{"body":"x"}`) },
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("err = %v, want ErrInvalidResponse", err)
				}
			},
		},
		{
			name:    "oversized body",
			handler: func(w http.ResponseWriter, r *http.Request) { jsonOK(w, `{"tag_name":"v0.2.0","body":"`+tooLarge+`"}`) },
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrResponseTooLarge) {
					t.Fatalf("err = %v, want ErrResponseTooLarge", err)
				}
			},
		},
		{
			name: "rate limited primary",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", "1793832000")
				w.WriteHeader(http.StatusForbidden)
				jsonOK(w, `{"message":"API rate limit exceeded for 203.0.113.9."}`)
			},
			check: func(t *testing.T, err error) {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) {
					t.Fatalf("err = %v, want *HTTPError", err)
				}
				if httpErr.Status != http.StatusForbidden || httpErr.Remaining != 0 {
					t.Fatalf("http error = %+v", httpErr)
				}
				if httpErr.ResetAt.IsZero() || !strings.Contains(httpErr.Message, "rate limit exceeded") {
					t.Fatalf("http error = %+v", httpErr)
				}
			},
		},
		{
			name: "secondary rate limit with retry-after",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
				jsonOK(w, `{"message":"You have exceeded a secondary rate limit."}`)
			},
			check: func(t *testing.T, err error) {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Status != http.StatusTooManyRequests {
					t.Fatalf("err = %v", err)
				}
				if httpErr.RetryAfter != 30*time.Second {
					t.Fatalf("retry-after = %v", httpErr.RetryAfter)
				}
			},
		},
		{
			name:    "server error",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			check: func(t *testing.T, err error) {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Status != http.StatusInternalServerError {
					t.Fatalf("err = %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newFakeGitHub(t, tc.handler)
			checker := NewChecker(&http.Client{Timeout: 5 * time.Second}, f.url)
			_, err := checker.Latest(context.Background(), "")
			if err == nil {
				t.Fatal("no error")
			}
			tc.check(t, err)
		})
	}
}

func TestPlainText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tags dropped", "<p>Hello <b>world</b></p>", "Hello world"},
		{"entities decoded", "a &amp; b &lt;c&gt; &#65;", "a & b <c> A"},
		{"comments dropped", "before<!-- hidden -->after", "beforeafter"},
		{"crlf normalized", "one\r\ntwo\rthree", "one\ntwo\nthree"},
		{"unfinished tag drops the tail", "visible <div class=", "visible"},
		{"plain markdown kept", "### What's Changed\n- a fix", "### What's Changed\n- a fix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plainText(tc.in); got != tc.want {
				t.Fatalf("plainText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClampNotes(t *testing.T) {
	long := strings.Repeat("é", maxNotesBytes) // 2 bytes each: twice the bound
	got := clampNotes(long)
	if len(got) > maxNotesBytes {
		t.Fatalf("notes are %d bytes, bound is %d", len(got), maxNotesBytes)
	}
	if !strings.HasPrefix(got, "é") || strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("clamped notes split a rune")
	}
	small := "  keep me  "
	if got := clampNotes(small); got != "keep me" {
		t.Fatalf("clampNotes(%q) = %q", small, got)
	}
}
