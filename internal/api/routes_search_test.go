package api

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// noEmbedder keeps this endpoint test on the full-text path: the semantic
// leg degrades at once instead of loading a model.
type noEmbedder struct{}

func (noEmbedder) Embedder() (engine.Embedder, error) { return nil, errors.New("test: no embedder") }

func TestSearchEndpointMapsEveryFilter(t *testing.T) {
	srv, ts, w := newTestServer(t)
	key := source.StoreKey("src", "store-1")
	w.search.result = search.Result{
		Hits: []search.Hit{}, Total: 2, Capped: true,
		TextMatches: 2, Terms: []store.TermMatch{{Term: "invoice", Mode: store.ModeWord}},
		Semantic: search.Semantic{Used: true, Covered: 2, Eligible: 3}, Notice: "notice",
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/search?q=invoice&scope=owner&limit=5&offset=2&store=" +
		key + "&role=sent&sender=example.org&attach=yes"})
	mustStatus(t, status, 200, data)
	if w.search.calls != 1 {
		t.Fatalf("service calls %d", w.search.calls)
	}
	q := w.search.query
	if q.Text != "invoice" || q.Limit != 5 || q.Offset != 2 {
		t.Fatalf("query bounds %+v", q)
	}
	if !q.Filter.OwnerOnly || len(q.Filter.Stores) != 1 || q.Filter.Stores[0] != key ||
		len(q.Filter.Roles) != 1 || q.Filter.Roles[0] != mail.RoleSent || q.Filter.Sender != "example.org" ||
		q.Filter.HasAttachments == nil || !*q.Filter.HasAttachments {
		t.Fatalf("filter %+v", q.Filter)
	}
	body := decodeMap(t, data)
	for _, key := range []string{"hits", "total", "capped", "text_matches", "semantic", "notice", "terms"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("response misses %q: %s", key, data)
		}
	}

	// The old q-only request is unchanged: all scope, no filter, no
	// client limit or offset.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello"})
	mustStatus(t, status, 200, data)
	if q := w.search.query; q.Text != "hello" || q.Limit != 0 || q.Offset != 0 || !q.Filter.IsZero() {
		t.Fatalf("q-only query %+v", q)
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello&attach=no"})
	mustStatus(t, status, 200, data)
	if got := w.search.query.Filter.HasAttachments; got == nil || *got {
		t.Fatalf("attach=no filter %+v", w.search.query.Filter)
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello&attach=any"})
	mustStatus(t, status, 200, data)
	if got := w.search.query.Filter.HasAttachments; got != nil {
		t.Fatalf("attach=any filter %+v", w.search.query.Filter)
	}

	// The omnibox asks for text-only suggestions; the Search page keeps the
	// semantic default.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello&semantic=off"})
	mustStatus(t, status, 200, data)
	if !w.search.query.TextOnly {
		t.Fatalf("semantic=off did not reach the service: %+v", w.search.query)
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello&semantic=on"})
	mustStatus(t, status, 200, data)
	if w.search.query.TextOnly {
		t.Fatalf("semantic=on reached the service as text-only")
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello&semantic=maybe"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_semantic" {
		t.Fatalf("code %q", code)
	}

	// The one sender normalization is shared with the all-mail page.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/search?q=hello&sender=Demo%40Example.Test"})
	mustStatus(t, status, 200, data)
	if got := w.search.query.Filter.Sender; got != "demo@example.test" {
		t.Fatalf("search sender %q, want the normalized address", got)
	}
}

// TestSearchEndpointRejectsEmptyFoldedQuery runs the real store and search
// service: a query that is only quotes (ASCII or fullwidth), NUL or a
// zero-width space folds to nothing and is a 400 empty_query, never a 500;
// a quote inside a word is still a valid query.
func TestSearchEndpointRejectsEmptyFoldedQuery(t *testing.T) {
	w := newWorld()
	cfg, st := fixturetest.OpenStore(t, nil)
	m := mailtest.Message("src", "1")
	m.Subject = "bonjour merci"
	if err := st.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	svc, err := search.New(cfg, search.Deps{Store: st, Embedder: noEmbedder{}, Gate: learningtest.Open{}})
	if err != nil {
		t.Fatal(err)
	}
	w.services.Search = svc
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"ASCII quote", "/api/search?q=%22"},
		{"fullwidth quote", "/api/search?q=%EF%BC%82"},
		{"NUL", "/api/search?q=%00"},
		{"zero-width space", "/api/search?q=%E2%80%8B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != "empty_query" {
				t.Fatalf("code %q, want empty_query: %s", code, data)
			}
		})
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{"quote inside a word", "/api/search?q=a%EF%BC%82b"},
		{"quoted phrase", "/api/search?q=%22bonjour%20merci%22"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, 200, data)
		})
	}
}

func TestSearchEndpointValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	long := strings.Repeat("a", mail.MaxSenderQueryRunes+1)
	cases := []struct {
		name string
		path string
		code string
	}{
		{"empty query", "/api/search?q=", "empty_query"},
		{"bad scope", "/api/search?q=x&scope=shared", "invalid_scope"},
		{"bad limit", "/api/search?q=x&limit=0", "invalid_limit"},
		{"large limit", "/api/search?q=x&limit=51", "invalid_limit"},
		{"text limit", "/api/search?q=x&limit=two", "invalid_limit"},
		{"negative offset", "/api/search?q=x&offset=-1", "invalid_offset"},
		{"huge offset", "/api/search?q=x&offset=1001", "invalid_offset"},
		{"text offset", "/api/search?q=x&offset=two", "invalid_offset"},
		{"bad store", "/api/search?q=x&store=store-1", "invalid_store"},
		{"empty store", "/api/search?q=x&store=", "invalid_store"},
		{"bad role", "/api/search?q=x&role=nope", "invalid_role"},
		{"role all", "/api/search?q=x&role=all", "invalid_role"},
		{"empty role", "/api/search?q=x&role=", "invalid_role"},
		{"empty sender", "/api/search?q=x&sender=", "invalid_sender"},
		{"blank sender", "/api/search?q=x&sender=%20%20", "invalid_sender"},
		{"lone at sender", "/api/search?q=x&sender=%40", "invalid_sender"},
		{"long sender", "/api/search?q=x&sender=" + url.QueryEscape(long), "invalid_sender"},
		{"control sender", "/api/search?q=x&sender=" + url.QueryEscape("a\x00b@example.org"), "invalid_sender"},
		{"bad attach", "/api/search?q=x&attach=maybe", "invalid_attach"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
			if w.search.calls != 0 {
				t.Fatalf("invalid request reached the service")
			}
		})
	}
}
