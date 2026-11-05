package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The release endpoint. Detection is one unauthenticated conditional GET of
// the latest published full release (60 requests/hour per IP); the repository
// is the app's own.
const (
	defaultBaseURL = "https://api.github.com"
	releaseRepo    = "LLM-4-People/mailsense"
	latestPath     = "/repos/" + releaseRepo + "/releases/latest"
	// releasesListPath is the one-page release list used to count how far a
	// build is behind when an update exists.
	releasesListPath = "/repos/" + releaseRepo + "/releases"

	// maxReleaseTags bounds the tags kept from one release list page.
	maxReleaseTags = 100
	// maxReleaseBytes bounds one release answer before it is decoded. Release
	// bodies are markdown and can be large, but never unbounded.
	maxReleaseBytes = 1 << 20
	// maxNotesBytes bounds the cached plain-text notes (~64 KiB).
	maxNotesBytes = 64 << 10
	// maxErrorMessageBytes bounds the message kept from a failed answer.
	maxErrorMessageBytes = 300
	// maxErrorBodyBytes bounds the GitHub error body read for its message.
	maxErrorBodyBytes = 8 << 10
)

// ErrResponseTooLarge marks a release answer past the read bound.
var ErrResponseTooLarge = errors.New("update: release answer is too large")

// ErrInvalidResponse marks a 200 answer that is not a usable release.
var ErrInvalidResponse = errors.New("update: invalid release answer")

// Release is one GitHub release as the app keeps it: the detected version,
// the bounded plain-text notes and the assets the later install step reads.
// BodyText never carries remote HTML: it is GitHub's own plain text, or a
// local extraction of the markdown body made here at the one boundary.
type Release struct {
	Tag         string  `json:"tag"`
	Version     string  `json:"version"`
	BodyText    string  `json:"body_text"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Asset is one release asset.
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

// CheckResult is one release endpoint answer. Release is nil on NotModified
// (the caller keeps its cached release); ETag is what the next conditional
// request sends.
type CheckResult struct {
	Release     *Release
	NotModified bool
	ETag        string
}

// Checker reads the latest release from the GitHub API. baseURL is injectable
// for tests ("" = the public API); client is the shared outbound client.
type Checker struct {
	client  *http.Client
	baseURL string
}

// NewChecker returns a checker over client. A nil client is refused by the
// first request, never replaced with a hidden default.
func NewChecker(client *http.Client, baseURL string) *Checker {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Checker{client: client, baseURL: strings.TrimRight(baseURL, "/")}
}

// Latest returns the latest full release. etag is the previous answer's
// ETag; when GitHub answers 304 the result is NotModified and the caller
// keeps its cached release. Every non-200/304 answer is an *HTTPError; the
// caller decides how quiet the state stays.
func (c *Checker) Latest(ctx context.Context, etag string) (CheckResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+latestPath, nil)
	if err != nil {
		return CheckResult{}, fmt.Errorf("update: release request: %w", err)
	}
	// User-Agent comes from the shared client (GitHub requires one).
	req.Header.Set("Accept", "application/vnd.github+json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{}, fmt.Errorf("update: release check: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return CheckResult{NotModified: true, ETag: firstNonEmpty(resp.Header.Get("ETag"), etag)}, nil
	case http.StatusOK:
	default:
		return CheckResult{}, newHTTPError(resp)
	}
	data, err := readBounded(resp.Body, maxReleaseBytes)
	if err != nil {
		return CheckResult{}, err
	}
	release, err := decodeRelease(data)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Release: release, ETag: resp.Header.Get("ETag")}, nil
}

// Tags returns the repository's release tags (newest first, one bounded
// page) so the status can count how far the running build is behind. It is a
// second request only when an update was detected: a check that finds no
// newer release never spends it.
func (c *Checker) Tags(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+releasesListPath+"?per_page="+strconv.Itoa(maxReleaseTags), nil)
	if err != nil {
		return nil, fmt.Errorf("update: release list request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: release list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, newHTTPError(resp)
	}
	data, err := readBounded(resp.Body, maxReleaseBytes)
	if err != nil {
		return nil, err
	}
	var page []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	tags := make([]string, 0, min(len(page), maxReleaseTags))
	for _, entry := range page {
		tag := strings.TrimSpace(entry.TagName)
		if tag == "" {
			continue
		}
		tags = append(tags, tag)
		if len(tags) == maxReleaseTags {
			break
		}
	}
	return tags, nil
}

// HTTPError is a release answer other than 200/304. Remaining, ResetAt and
// RetryAfter carry the GitHub rate-limit signals so the service can wait
// instead of retrying into the limit.
type HTTPError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
	// Remaining is X-RateLimit-Remaining (-1 when the header is absent).
	Remaining int
	// ResetAt is X-RateLimit-Reset (zero when absent).
	ResetAt time.Time
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("update: release check: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("update: release check: HTTP %d", e.Status)
}

// githubRelease is the subset of the GitHub release payload the app reads.
// encoding/json ignores every other field of the answer; only these cross
// into Release.
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	BodyText    string        `json:"body_text"`
	HTMLURL     string        `json:"html_url"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func decodeRelease(data []byte) (*Release, error) {
	var raw githubRelease
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	tag := strings.TrimSpace(raw.TagName)
	if tag == "" {
		return nil, fmt.Errorf("%w: the release has no tag", ErrInvalidResponse)
	}
	notes := raw.BodyText
	if strings.TrimSpace(notes) == "" {
		notes = plainText(raw.Body)
	}
	release := &Release{
		Tag:         tag,
		Version:     versionOfTag(tag),
		BodyText:    clampNotes(notes),
		HTMLURL:     strings.TrimSpace(raw.HTMLURL),
		PublishedAt: strings.TrimSpace(raw.PublishedAt),
		Assets:      make([]Asset, 0, len(raw.Assets)),
	}
	for _, a := range raw.Assets {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		release.Assets = append(release.Assets, Asset{Name: name, Size: a.Size, DownloadURL: strings.TrimSpace(a.BrowserDownloadURL)})
	}
	return release, nil
}

// versionOfTag strips the release's optional v prefix. A tag the version
// parser rejects stays as it is: the status then never claims an update from
// a version it cannot order.
func versionOfTag(tag string) string {
	if len(tag) > 1 && tag[0] == 'v' && tag[1] >= '0' && tag[1] <= '9' {
		return tag[1:]
	}
	return tag
}

// plainText returns the visible text of a markdown/HTML release body: tags
// and comments are dropped and entities decoded. It is a bounded local
// conversion, never a renderer, so no remote HTML can reach a UI.
func plainText(raw string) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(raw), maxNotesBytes))
	for i := 0; i < len(raw); {
		if raw[i] != '<' {
			b.WriteByte(raw[i])
			i++
			continue
		}
		if strings.HasPrefix(raw[i:], "<!--") {
			if j := strings.Index(raw[i:], "-->"); j >= 0 {
				i += j + 3
				continue
			}
			break
		}
		if j := strings.IndexByte(raw[i:], '>'); j >= 0 {
			i += j + 1
			continue
		}
		break // an unfinished tag: everything after it is markup
	}
	text := html.UnescapeString(b.String())
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

// clampNotes bounds cached notes to maxNotesBytes without splitting a rune.
func clampNotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxNotesBytes {
		return s
	}
	cut := maxNotesBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \t\n")
}

// clampText bounds a one-line message to max bytes without splitting a rune.
func clampText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func newHTTPError(resp *http.Response) *HTTPError {
	body, _ := readBounded(resp.Body, maxErrorBodyBytes)
	message := githubMessage(body)
	if message == "" {
		message = strings.ToLower(http.StatusText(resp.StatusCode))
	}
	return &HTTPError{
		Status:     resp.StatusCode,
		Message:    clampText(message, maxErrorMessageBytes),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		Remaining:  headerInt(resp.Header.Get("X-RateLimit-Remaining"), -1),
		ResetAt:    headerUnix(resp.Header.Get("X-RateLimit-Reset")),
	}
}

func githubMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Message)
}

// parseRetryAfter accepts the seconds form GitHub sends and, defensively,
// the HTTP-date form. An unparseable or past value asks for no wait.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}

func headerInt(raw string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return n
}

func headerUnix(raw string) time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// readBounded reads at most max bytes and fails when the reader has more.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("update: read release answer: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w (%d bytes)", ErrResponseTooLarge, len(data))
	}
	return data, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
