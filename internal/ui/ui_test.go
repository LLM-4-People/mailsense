package ui

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/subject"
)

func request(t *testing.T, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	return rec
}

func TestShellAndAssets(t *testing.T) {
	cases := []struct {
		path        string
		contentType string
		cache       string
		bodyHas     string
	}{
		{"/", "text/html; charset=utf-8", shellCache, "Mailsense"},
		{"/app.css", "text/css; charset=utf-8", assetCache, "--accent"},
		{"/app.js", "text/javascript; charset=utf-8", assetCache, "Authorization"},
		{"/mail.css", "text/css; charset=utf-8", assetCache, ".mail-text"},
		{"/favicon.svg", "image/svg+xml", assetCache, "<svg"},
		{"/i18n.en.js", "text/javascript; charset=utf-8", assetCache, "MailsenseI18nCatalogs.en"},
		{"/i18n.fr.js", "text/javascript; charset=utf-8", assetCache, "MailsenseI18nCatalogs.fr"},
		{"/i18n.js", "text/javascript; charset=utf-8", assetCache, "MailsenseI18n"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := request(t, http.MethodGet, tc.path, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Fatalf("content type %q, want %q", got, tc.contentType)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.cache {
				t.Fatalf("cache %q, want %q", got, tc.cache)
			}
			if got := rec.Header().Get("ETag"); got == "" {
				t.Fatal("no ETag")
			}
			if !strings.Contains(rec.Body.String(), tc.bodyHas) {
				t.Fatalf("body does not contain %q", tc.bodyHas)
			}
		})
	}
}

func TestAssetsNegotiateGzip(t *testing.T) {
	plain := request(t, http.MethodGet, "/app.js", nil)
	etag := plain.Header().Get("ETag")
	if got := plain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("a request without Accept-Encoding got %q", got)
	}
	if got := plain.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("vary %q", got)
	}
	if got := plain.Header().Get("Content-Length"); got != strconv.Itoa(plain.Body.Len()) {
		t.Fatalf("plain content length %q, body %d", got, plain.Body.Len())
	}

	compressed := request(t, http.MethodGet, "/app.js", map[string]string{"Accept-Encoding": "gzip"})
	if got := compressed.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("encoding %q, want gzip", got)
	}
	if got := compressed.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("vary %q", got)
	}
	gzipETag := compressed.Header().Get("ETag")
	if gzipETag == etag {
		t.Fatalf("the compressed representation reuses the identity ETag %q", etag)
	}
	compressedLen := compressed.Body.Len()
	if got := compressed.Header().Get("Content-Length"); got != strconv.Itoa(compressedLen) {
		t.Fatalf("compressed content length %q, body %d", got, compressedLen)
	}
	zr, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, plain.Body.Bytes()) {
		t.Fatal("the gzip asset differs from the plain asset")
	}
	if compressedLen >= plain.Body.Len() {
		t.Fatalf("gzip %d bytes, plain %d: no saving", compressedLen, plain.Body.Len())
	}

	head := request(t, http.MethodHead, "/app.js", map[string]string{"Accept-Encoding": "gzip"})
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD with gzip: %d with %d body bytes", head.Code, head.Body.Len())
	}
	if got := head.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("HEAD gzip encoding %q", got)
	}
	if got := head.Header().Get("ETag"); got != gzipETag {
		t.Fatalf("HEAD ETag %q, want the gzip validator %q", got, gzipETag)
	}

	notModified := request(t, http.MethodGet, "/app.js", map[string]string{
		"Accept-Encoding": "gzip", "If-None-Match": gzipETag})
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional gzip GET: %d with %d body bytes", notModified.Code, notModified.Body.Len())
	}

	// A client that cached the identity body must not be told it is fresh
	// when it now asks for the compressed representation.
	switched := request(t, http.MethodGet, "/app.js", map[string]string{
		"Accept-Encoding": "gzip", "If-None-Match": etag})
	if switched.Code != http.StatusOK || switched.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("a validator from the other encoding: %d %q", switched.Code, switched.Header().Get("Content-Encoding"))
	}
}

func TestIndexRedirectAndMethods(t *testing.T) {
	rec := request(t, http.MethodGet, "/index.html", nil)
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/" {
		t.Fatalf("index redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = request(t, http.MethodPost, "/", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("wrong method: %d %q", rec.Code, rec.Header().Get("Allow"))
	}
	rec = request(t, http.MethodHead, "/app.js", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestConditionalGET(t *testing.T) {
	first := request(t, http.MethodGet, "/app.js", nil)
	etag := first.Header().Get("ETag")
	rec := request(t, http.MethodGet, "/app.js", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("conditional GET: %d with %d body bytes", rec.Code, rec.Body.Len())
	}
	rec = request(t, http.MethodGet, "/app.js", map[string]string{"If-None-Match": `W/` + etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("weak conditional GET: %d", rec.Code)
	}
	rec = request(t, http.MethodGet, "/app.js", map[string]string{"If-None-Match": `"other"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("mismatched conditional GET: %d", rec.Code)
	}
}

func TestUnknownIs404(t *testing.T) {
	rec := request(t, http.MethodGet, "/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	rec = request(t, http.MethodGet, "/app.css/", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("trailing slash status %d, want 404", rec.Code)
	}
}

// TestShellRoutes checks the route inventory, the sidebar order and the new
// Ask workspace, all of which are easy to break without a compiler.
func TestShellRoutes(t *testing.T) {
	html, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	// The sidebar groups every screen exactly once; nav_contract_test.go owns
	// the detailed group, label and disclosure contract.
	order := []string{"hub", "brief", "mail", "companies", "ask", "answers", "labels", "learning", "sources", "settings", "logs"}
	previous := -1
	for _, name := range order {
		marker := `data-nav="` + name + `"`
		at := strings.Index(page, marker)
		if at < 0 {
			t.Fatalf("index.html lacks the %s nav item", name)
		}
		if at <= previous {
			t.Fatalf("nav item %s is out of order", name)
		}
		previous = at
	}
	for _, required := range []string{
		`<a class="brand" href="#/hub">`, `href="#/answers"`, `id="i-menu"`, `id="nav-labels-badge"`,
		`href="#/hub"`, `href="#/brief"`, `href="#/mail"`, `href="#/companies"`, `href="#/ask"`,
		`href="#/labels"`, `href="#/learning"`, `href="#/sources"`, `href="#/settings"`, `href="#/logs"`,
		`id="nav-toggle"`, `aria-controls="nav-menu"`, `id="nav-menu"`, `data-i18n="nav.answers"`, `data-i18n="nav.menu"`,
		`id="i-hub"`, `id="i-brief"`, `id="i-ask"`, `id="topbar-update"`,
		`id="global-search"`, `id="global-search-input"`, `id="global-search-results"`,
		`data-i18n-placeholder="search.global_placeholder"`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("index.html lacks %q", required)
		}
	}

	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const SCREEN_IDS = ["hub", "brief", "mail", "companies", "ask", "labels", "answers", "learning", "sources", "settings", "logs"]`,
		`base + "#/hub"`,
		`name: "hub"`,
		`case "hub": node = await renderHub(route, seq)`,
		`case "brief": node = renderBrief(route, seq); break;`,
		`case "companies": node = await renderCompanies(route, seq)`,
		`case "ask": node = await renderAsk(route, seq)`,
		`case "answers": node = await renderAnswers(route, seq)`,
		`case "learning": node = await renderLearning(route, seq)`,
		`function renderAsk(route, seq)`,
		`function createAskWorkspace(`,
		`state.route !== "ask"`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js lacks %q", required)
		}
	}
	if strings.Contains(js, `if (name === "ask") return { name: "answers"`) ||
		strings.Contains(js, `The retired Ask placeholder is the answer-history workspace now`) {
		t.Fatal("app.js still redirects Ask to Answers")
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{
		"nav.hub", "nav.brief", "nav.mail", "nav.companies", "nav.ask", "nav.labels", "nav.system",
		"nav.learning", "nav.sources", "nav.settings", "nav.logs", "nav.answers", "nav.menu",
		"nav.group.work", "nav.group.ask", "nav.group.teach", "nav.group.configure", "nav.group.system",
		"screen.ask.title", "screen.ask.eyebrow",
		"screen.companies.title", "screen.companies.eyebrow",
		"screen.hub.title", "screen.hub.eyebrow",
		"screen.brief.title", "screen.brief.eyebrow",
		"screen.answers.title", "screen.answers.eyebrow",
		"hub.closed.title", "hub.review_suggestions", "answers.source.you",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	if en["nav.hub"].text != "Home" || fr["nav.hub"].text != "Accueil" {
		t.Errorf("Home nav wording en=%q fr=%q", en["nav.hub"].text, fr["nav.hub"].text)
	}
	if en["nav.brief"].text != "Brief" || fr["nav.brief"].text != "Synthèse" {
		t.Errorf("Brief nav wording en=%q fr=%q", en["nav.brief"].text, fr["nav.brief"].text)
	}
	if en["nav.ask"].text != "Ask" || fr["nav.ask"].text != "Demander" {
		t.Errorf("Ask nav wording en=%q fr=%q", en["nav.ask"].text, fr["nav.ask"].text)
	}
	if en["nav.companies"].text != "Companies" || fr["nav.companies"].text != "Entreprises" {
		t.Errorf("Companies nav wording en=%q fr=%q", en["nav.companies"].text, fr["nav.companies"].text)
	}
	if en["nav.labels"].text != "Teach" || fr["nav.labels"].text != "Enseigner" {
		t.Errorf("Teach nav wording en=%q fr=%q", en["nav.labels"].text, fr["nav.labels"].text)
	}
}

// TestInstantLayer checks the read cache, the route DOM cache, prefetch and
// the background updating indicator. These are behavioral contracts the app
// cannot get from a framework here.
func TestInstantLayer(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const apiCache = new Map()`,
		`const apiInflight = new Map()`,
		`const routeCache = new Map()`,
		`const MAX_ROUTE_CACHE = 24`,
		`const ROUTE_FRESH_MS = 20000`,
		`function refreshCachedRoute(`,
		`function syncCachedMailDetail(`,
		`function syncCachedCompanyDetail(`,
		`if (route.name === "mail") params.delete("id")`,
		`if (route.name === "companies") params.delete("key")`,
		`function cached(path, options = {})`,
		`async function requestCached(`,
		`function invalidateCache(`,
		`function invalidateRouteCache(`,
		`function routeCacheKey(`,
		`async function refreshActiveRoute()`,
		`function prefetchCore()`,
		`window.requestIdleCallback`,
		`function showUpdating(`,
		`function loadAskHistory(`,
		`function loadPriority(`,
		`function loadMessage(`,
		`CACHE_TTL`,
		`showUpdating(true)`,
		`cachedEntry.node`,
		`entry.scroll`,
		`prefetchPriorityDetails`,
		`ask-current`,
		`if (!gateAllows()) return;`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js lacks the instant-layer step %q", required)
		}
	}
	// A cached Mail screen is reused for every message link. The detail and
	// the selected list row must move together, or Company/Home deep links
	// leave the previous row highlighted.
	start := strings.Index(js, "function syncCachedMailDetail(route)")
	end := strings.Index(js[start:], "\n  }")
	if start < 0 || end <= 0 {
		t.Fatal("syncCachedMailDetail was not found")
	}
	if body := js[start : start+end]; !strings.Contains(body, "syncMailListSelection(id)") {
		t.Fatal("syncCachedMailDetail does not sync the Mail list selection")
	}
	if strings.Contains(js, `clear(dom.main);\n    dom.main.append(loadingState());`) {
		t.Fatal("renderRoute still clears the screen and shows a generic spinner")
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".skeleton-panel", ".skeleton-line", ".topbar-update", ".mail-layout", ".ask-card", ".mail-row"} {
		if !strings.Contains(string(css), required) {
			t.Fatalf("app.css lacks %q", required)
		}
	}
}

// TestSectionScrollOwnership locks the shell contract: the window chrome and
// #main never scroll, every route exposes named bounded regions, and the
// route cache restores each region instead of a single page offset.
func TestSectionScrollOwnership(t *testing.T) {
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)
	if !strings.Contains(sheet, "html, body { height: 100%; overflow: hidden; }") {
		t.Fatal("the document can still scroll the whole app")
	}
	start := strings.Index(sheet, "#main {")
	if start < 0 {
		t.Fatal("#main was not found")
	}
	end := strings.Index(sheet[start:], "\n}")
	if end < 0 {
		t.Fatal("#main rule is unterminated")
	}
	mainRule := sheet[start : start+end]
	if !strings.Contains(mainRule, "overflow: hidden;") {
		t.Fatalf("#main no longer clips the shell: %s", mainRule)
	}
	if strings.Contains(mainRule, "overflow-y: auto") {
		t.Fatalf("#main still owns a scrollbar: %s", mainRule)
	}
	for _, required := range []string{
		".section-scroll {",
		".route-scroll {",
		"scrollbar-width: thin;",
		".screen { overflow-y: auto; overflow-x: clip; overscroll-behavior: contain; }",
	} {
		if !strings.Contains(sheet, required) {
			t.Fatalf("app.css lacks the section scroll rule %q", required)
		}
	}

	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function captureScrollState(`,
		`function applyScrollState(`,
		`function prepareScrollRegions(`,
		`[data-scroll-key]`,
		`entry.scrolls = captureScrollState(dom.main)`,
		`node.dataset.screenScrollBound`,
		`entry.scrolls.__screen`,
		`dataset: { scrollKey: id }`,
		`scrollKey: "home-brief"`,
		`scrollKey: "home-agenda"`,
		`scrollKey: "home-important"`,
		`dataset: { scrollKey: "mail-list" }`,
		`dataset: { scrollKey: "mail-detail" }`,
		`dataset: { scrollKey: "companies-list" }`,
		`dataset: { scrollKey: "companies-profile" }`,
		`dataset: { scrollKey: "ask-main" }`,
		`dataset: { scrollKey: "labels-message" }`,
		`dataset: { scrollKey: "labels-answer" }`,
		`dataset: { scrollKey: "sources-body" }`,
		`dataset: { scrollKey: "settings-list" }`,
		`dataset: { scrollKey: "learning-body" }`,
		`dataset: { scrollKey: "answers-body" }`,
		`dataset: { scrollKey: "logs" }`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the section scroll marker %q", required)
		}
	}
}

// TestLearningLiveProgressAndFreshness covers the live index contract in the
// shell: the embeddings stage paints index.progress in place, jobs.update
// still forces the authoritative /api/learning read, and every learning-
// changing event invalidates the screens that cache its data.
func TestLearningLiveProgressAndFreshness(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function stageProgressText(stage)`,
		`function stageProgressDetail(stage)`,
		`function updateLearningStageNode(stage)`,
		`function applyIndexProgress(payload)`,
		`dataset: { stageId: stage.id || "" }`,
		`class: "stage-progress-detail"`,
		`stageProgressText(stage)`,
		`stageProgressDetail(stage)`,
		`if (state.route === "learning") updateLearningStageNode(stage)`,
		`bar = document.createElement("progress")`,
		`source.addEventListener("index.progress"`,
		`source.addEventListener("index.finished"`,
		`source.addEventListener("sync.mailboxes"`,
		`source.addEventListener("evidence.finished"`,
		`source.addEventListener("layer1.finished"`,
		`source.addEventListener("feedback.label"`,
		`function refreshVisibleRoutes(`,
		`function afterLearningDataChanged(`,
		`refresh: ["learning", "hub", "brief", "answers", "companies"],`,
		`refresh: ["sources", "learning", "hub"],`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the live learning step %q", required)
		}
	}

	// One direct paint path: index.progress updates the cached stage and the
	// mounted node without fetching, so it cannot double jobs.update's read.
	progressStart := strings.Index(js, `source.addEventListener("index.progress"`)
	progressEnd := strings.Index(js, `source.addEventListener("index.finished"`)
	if progressStart < 0 || progressEnd <= progressStart {
		t.Fatal("the index.progress handler was not found")
	}
	progressBody := js[progressStart:progressEnd]
	if !strings.Contains(progressBody, `applyIndexProgress(payload)`) {
		t.Error("the index.progress handler does not apply the payload")
	}
	for _, forbidden := range []string{"refreshActiveRoute(", "loadLearning("} {
		if strings.Contains(progressBody, forbidden) {
			t.Errorf("the index.progress handler fetches again via %q", forbidden)
		}
	}

	// jobs.update remains the forced-read path, and its refresh invalidates
	// the jobs cache so the Learning job list is current too.
	jobsStart := strings.Index(js, `source.addEventListener("jobs.update"`)
	jobsEnd := strings.Index(js, `source.addEventListener("log.wake"`)
	if jobsStart < 0 || jobsEnd <= jobsStart {
		t.Fatal("the jobs.update handler was not found")
	}
	if !strings.Contains(js[jobsStart:jobsEnd], `scheduleLearningRefresh()`) {
		t.Error("jobs.update does not schedule a learning refresh")
	}
	refreshStart := strings.Index(js, "function scheduleLearningRefresh()")
	refreshEnd := strings.Index(js[refreshStart:], "\n  }")
	if refreshStart < 0 || refreshEnd <= 0 {
		t.Fatal("scheduleLearningRefresh was not found")
	}
	refreshBody := js[refreshStart : refreshStart+refreshEnd]
	for _, required := range []string{`loadLearning(true)`, `refreshActiveRoute()`, `invalidateTopics(["learningStatus"]`} {
		if !strings.Contains(refreshBody, required) {
			t.Errorf("scheduleLearningRefresh lacks %q", required)
		}
	}
	learningStatus := topicBody(t, js, "learningStatus")
	for _, required := range []string{`"/api/jobs"`, `"/api/threads"`} {
		if !strings.Contains(learningStatus, required) {
			t.Errorf("the learningStatus topic lacks %q", required)
		}
	}
	if strings.Contains(refreshBody, `"ask"`) {
		t.Error("the learning refresh path touches the Ask composer")
	}

	// config.changed refreshes an active Settings screen (Providers and
	// Runtime live there); focused inputs still block the DOM replacement.
	configStart := strings.Index(js, `source.addEventListener("config.changed"`)
	configEnd := strings.Index(js, `source.addEventListener("sidecar.state"`)
	if configStart < 0 || configEnd <= configStart {
		t.Fatal("the config.changed handler was not found")
	}
	if !strings.Contains(js[configStart:configEnd], `scheduleDataRefresh("settings", "brief", "hub", "learning")`) {
		t.Error("config.changed does not schedule the affected visible screens")
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"learning.progress_embedded", "learning.progress_unchanged", "learning.progress_failed"} {
		for _, catalog := range []map[string]catalogEntry{en, fr} {
			entry, ok := catalog[key]
			if !ok {
				t.Errorf("catalog lacks %q", key)
				continue
			}
			if !entry.isPlural || !strings.Contains(entry.plural["other"], "{count}") {
				t.Errorf("%q is not a plural count entry", key)
			}
		}
	}

	runUISandbox(t, "learning_live_progress", learningLiveProgressHarness, "learning live progress behavior: ok")
}

const learningLiveProgressHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.formatNumber = I.formatNumber.bind(I);
sandbox.formatDuration = () => "";
sandbox.measureUnit = (unit) => String(unit || "");
sandbox.stageTitle = (id) => String(id || "");
sandbox.statusLabel = (value) => String(value || "");
sandbox.statusKind = (value) => String(value || "");
sandbox.stageHint = () => "";
sandbox.statusPill = (value) => makeNode("span", { class: "pill " + String(value || ""), text: String(value || "") });
sandbox.badge = (value) => makeNode("span", { text: String(value) });
sandbox.blockerNode = () => makeNode("div");
sandbox.technicalRows = () => null;

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag,
    attrs: {}, children: [], text: "", dataset: {}, value: "", disabled: false,
    hidden: false, textContent: "", className: "",
    addEventListener() {}, focus() {}, setAttribute(name, value) { this.attrs[name] = value; },
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "class") { node.className = String(value); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); }
    if (key === "hidden") node.hidden = !!value;
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}

const learning = {
  status: {
    stages: [{ id: "embeddings", state: "running", progress: { done: 0, total: 34938, unit: "messages" } }],
  },
};
let updatedStage = null;
let refreshes = 0;
sandbox.state = { learning, route: "learning", composerValue: "draft question", token: "t", shuttingDown: false };
sandbox.scheduleLearningRefresh = () => { refreshes += 1; };

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const ps = app.indexOf("// percent:start"), pe = app.indexOf("// percent:end");
assert.ok(ps >= 0 && pe > ps, "percent markers are missing"); vm.runInContext(app.slice(ps, pe), sandbox, { filename: "app.js#percent" });
const start = app.indexOf("function stageProgressText(");
const end = app.indexOf("// ---------------------------------------------------------------- companies", start);
assert.ok(start >= 0 && end > start, "the learning stage helpers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#learning-live" });
// Replace the real DOM patcher with an observer: this harness checks the
// cached payload and the call, not browser layout.
sandbox.updateLearningStageNode = (value) => { updatedStage = value; };

const stage = sandbox.state.learning.status.stages[0];
sandbox.applyIndexProgress({ run_id: 1, done: 13696, total: 34938, embedded: 13650, unchanged: 46, failed: 0 });
assert.equal(stage.progress.done, 13696, "index.progress did not update done");
assert.equal(stage.progress.total, 34938, "index.progress did not update total");
assert.equal(stage.progress.embedded, 13650, "index.progress did not update embedded");
assert.equal(stage.progress.unchanged, 46, "index.progress did not update unchanged");
assert.equal(stage.state, "running", "index.progress did not mark the stage running");
assert.equal(updatedStage, stage, "the visible stage was not updated in place");
assert.equal(refreshes, 0, "the direct paint path fetched the status");
assert.equal(sandbox.state.composerValue, "draft question", "an index.progress event touched the Ask composer");

const card = sandbox.stageNode(stage, sandbox.state.learning);
const body = texts(card).join(" | ");
assert.ok(body.includes("13,696 / 34,938 messages"), "the live done/total is not rendered: " + body);
assert.ok(body.includes("13,650 embedded"), "the embedded count is not rendered: " + body);
assert.ok(body.includes("46 unchanged"), "the unchanged count is not rendered: " + body);
assert.ok(!body.includes("0 / 34,938"), "the stage still renders a static zero: " + body);

sandbox.state.learning = { status: { stages: [] } };
sandbox.applyIndexProgress({ done: 1, total: 2 });
assert.equal(refreshes, 1, "a payload without a cached stage did not force a status read");
assert.equal(sandbox.state.composerValue, "draft question", "forcing a status read touched the Ask composer");

console.log("learning live progress behavior: ok");
`

// TestPreviewModeShell checks the explicit uncertified preview: the
// dedicated warning pill and banner, the Hub toggle and the Mail gate
// condition. The preview must never reuse the open/ok style.
func TestPreviewModeShell(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{
		"preview.pill", "preview.banner", "preview.mail_note", "preview.learning_note", "preview.hub_body",
		"preview.hub_action", "preview.dialog.eyebrow", "preview.dialog.title",
		"preview.dialog.body", "preview.dialog.confirm", "preview.enabled_toast",
		"preview.enabled_toast_body", "preview.disabled_toast", "preview.disabled_toast_body",
		"preview.open_mail", "preview.disable", "preview.compact", "preview.home_body",
		"triage.uncertified_route", "triage.summary.preview_note",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	const (
		enBanner = "Test preview. Automatic answers are uncertified. The gate is still closed."
		frBanner = "Aperçu de test. Les réponses automatiques ne sont pas certifiées. Le verrou reste fermé."
	)
	if en["preview.banner"].text != enBanner {
		t.Errorf("English banner = %q", en["preview.banner"].text)
	}
	if fr["preview.banner"].text != frBanner {
		t.Errorf("French banner = %q", fr["preview.banner"].text)
	}
	if !strings.Contains(en["preview.mail_note"].text, "not certified") ||
		!strings.Contains(fr["preview.mail_note"].text, "pas certifiées") {
		t.Error("the Mail preview note does not say the values are uncertified")
	}
	if en["triage.uncertified_route"].text != "Uncertified preview" ||
		fr["triage.uncertified_route"].text != "Aperçu non certifié" {
		t.Errorf("uncertified route pill en=%q fr=%q", en["triage.uncertified_route"].text, fr["triage.uncertified_route"].text)
	}

	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function previewPill()`,
		`pill(t("preview.pill"), "preview")`,
		`function previewBanner(`,
		`role: "status"`,
		`if (!allowed)`,
		`previewBanner(t("preview.mail_note"), null, true)`,
		`previewBanner(t("preview.home_body")`,
		`function routePill(`,
		`pill(t("triage.uncertified_route"), "preview")`,
		`t("triage.summary.preview_note")`,
		`request("/api/settings/learning.test_mode"`,
		`t("preview.hub_action")`,
		`t("preview.disable")`,
		`t("preview.open_mail")`,
		`showPreview ? previewPill() : null`,
		`previewBanner(`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js lacks the preview step %q", required)
		}
	}
	if strings.Contains(js, `gatePill(!open || preview)`) || strings.Contains(js, `gatePill(preview`) {
		t.Fatal("app.js feeds the preview into the real gate pill")
	}
	mailStart, mailEnd := strings.Index(js, "async function renderMail"), strings.Index(js, "function parseMode")
	mailScreen := ""
	if mailStart >= 0 && mailEnd > mailStart {
		mailScreen = js[mailStart:mailEnd]
	}
	if !strings.Contains(mailScreen, `previewBanner(t("preview.mail_note"), null, true)`) ||
		!strings.Contains(mailScreen, "if (!allowed)") ||
		!strings.Contains(mailScreen, "const realOpen = !!gate.open;") ||
		!strings.Contains(mailScreen, "const open = allowed && (realOpen || !preview);") ||
		!strings.Contains(mailScreen, "gate.status") {
		t.Fatal("the Mail screen does not gate on the allowed gate answer, carry the locked status and the preview banner")
	}
	railStart, railEnd := strings.Index(js, "function hubRailNodes"), strings.Index(js, "async function renderLearning")
	if railStart < 0 || railEnd <= railStart || !strings.Contains(js[railStart:railEnd], `t("preview.hub_action")`) ||
		!strings.Contains(js[railStart:railEnd], `t("preview.disable")`) {
		t.Fatal("the Hub Learning rail lacks the test-preview action")
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".pill.preview", ".preview-banner", ".preview-banner-body"} {
		if !strings.Contains(string(css), required) {
			t.Fatalf("app.css lacks %q", required)
		}
	}
}

// TestNoDuplicateFunctions guards the single global function namespace inside
// app.js's IIFE. A later declaration silently wins, which once made the
// Answers list render like the triage answer table.
func TestNoDuplicateFunctions(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^  (?:async )?function ([A-Za-z0-9_]+)\(`)
	seen := map[string]int{}
	for _, match := range re.FindAllStringSubmatchIndex(string(script), -1) {
		name := string(script[match[2]:match[3]])
		seen[name]++
		if seen[name] > 1 {
			line := 1 + strings.Count(string(script[:match[0]]), "\n")
			t.Errorf("app.js:%d redeclares function %s", line, name)
		}
	}
}

// TestTextHelperIsNotShadowed guards the text(value) node helper. A local
// const/let/var named text shadows it, and citation chips then fail with
// "text is not a function" after the answer has already rendered.
func TestTextHelperIsNotShadowed(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "function text(value)") {
		t.Fatal("app.js no longer has the text(value) node helper")
	}
	re := regexp.MustCompile(`(?m)^\s+(?:const|let|var)\s+text\b`)
	for _, match := range re.FindAllStringIndex(string(script), -1) {
		line := 1 + strings.Count(string(script[:match[0]]), "\n")
		t.Errorf("app.js:%d shadows the text(value) helper with a local text variable", line)
	}
}

func TestEmbeddedFilesAndReferences(t *testing.T) {
	for _, name := range []string{"index.html", "app.css", "app.js", "mail.css", "favicon.svg", "i18n.en.js", "i18n.fr.js", "i18n.js"} {
		info, err := fs.Stat(Files, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	html, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`{{asset:app.css}}`, `{{asset:i18n.en.js}}`, `{{asset:i18n.js}}`, `{{asset:app.js}}`, `id="app"`, `id="token-screen"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("index.html lacks %q", want)
		}
	}
	if strings.Contains(page, "token=") {
		t.Fatal("index.html must not contain a token placeholder")
	}

	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"http://", "https://", "console.", "innerHTML", "document.write", "allow-scripts", "allow-popups", "allow-forms"} {
		if strings.Contains(string(script), forbidden) {
			t.Fatalf("app.js contains %q: the shell must stay offline, script-free and content-free", forbidden)
		}
	}
	for _, required := range []string{"srcdoc", `sandbox: "allow-same-origin"`, "body_html", "body_css", "data-inline", "/mail.css", "placeMailFrame", "openLinkDialog", "attachmentList"} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the message rendering step %q", required)
		}
	}
	for _, required := range []string{"history.replaceState", "sessionStorage", `Bearer `, "?token="} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the token handshake step %q", required)
		}
	}
	// The Mail screen is a server-page client: it reads the summary and
	// bounded pages, never the full triage view.
	for _, required := range []string{`requestCached("/api/triage/summary"`, `requestCached("/api/triage?limit="`, `request("/api/triage?limit="`, `t("common.load_more")`} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the paginated triage step %q", required)
		}
	}
	if strings.Contains(string(script), `request("/api/triage")`) {
		t.Fatal("app.js still fetches the full triage view")
	}
	// Labels has a review mode: it asks the queue for review and shows the
	// judge suggestion without changing the normal labelling flow.
	for _, required := range []string{`t("labels.mode.review")`, `t("labels.review.kicker")`, `t("labels.review.accept")`, "review=true", `route.params.get("review")`} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the review-mode step %q", required)
		}
	}
	// The home reads its own content-free view and the priority page; it
	// never invents important mail when the gate is closed.
	for _, required := range []string{`requestCached("/api/hub"`, `t("hub.closed.title")`, `t("hub.review_suggestions")`, `t("hub.rail.answers")`, `t("hub.rail.mailbox")`, `loadPriority("all", PRIORITY_HOME_SIZE)`} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the hub step %q", required)
		}
	}
	// Answers reads the label summary and current-label pages, then reuses
	// the shared message renderer for one message's detail.
	for _, required := range []string{`requestCached("/api/labels/summary"`, `t("answers.list.title")`, `t("answers.detail.review_in_labels")`, `route.params.get("message")`, `requestCached("/api/answers?message="`, "next_cursor"} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the answers step %q", required)
		}
	}
	// Ask is a real workspace: composer, local answer cards, citations and
	// an explicit retrieval fallback when no key is configured.
	for _, required := range []string{
		`function createAskWorkspace(`, `function askAnswerCard(`, `function structuredSentence(`,
		`t("ask.thinking.searching")`, `t("ask.thinking.writing")`, `t("ask.unavailable.title")`,
		`t("ask.disabled.title")`, `t("ask.history.clear")`, `t("ask.history.label_history")`,
		`request("/api/ask"`, `request("/api/ask/history"`, `route.params.get("id")`,
		`"aria-live": "polite"`,
	} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the ask step %q", required)
		}
	}
	// Mail has three tabs, filter chips, a cached priority list and the two
	// pane detail; the old triage mode stays compatible.
	for _, required := range []string{
		`mail.tab.important`, `mail.tab.recent`, `mail.tab.search`,
		`PRIORITY_FILTERS`, `function renderImportant(`, `function renderRecent(`, `function renderSearch(`,
		`function initGlobalSearch(`, `function parseGlobalQuery(`, `function syncGlobalSearch(`,
		`mail-layout`, `mail.filter.`, `mail.detail.preview_note`, `route.params.get("filter")`,
		`cachedPriority(filter)`, `function triageListRow(`, `function loadMoreTriage(`,
	} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("app.js lacks the mail step %q", required)
		}
	}
	if strings.Contains(string(script), `if (name === "ask") return { name: "answers"`) {
		t.Fatal("app.js still redirects Ask to Answers")
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"http://", "https://", "@import", "url(http", "url(https", "max-height: 420px"} {
		if strings.Contains(string(css), forbidden) {
			t.Fatalf("app.css contains a forbidden rule %q", forbidden)
		}
	}
	for _, required := range []string{
		".review-suggestion", ".mail-frame-wrap", ".mail-frame", ".mail-notice", ".backfill",
		"--bg: #f4f1e9", "--surface: #fdfbf6", "--accent: #1e5a46", "--font-display",
		".home-head", ".home-brief-grid", ".home-more", ".ask-answer", ".ask-current",
		".mail-layout", ".filter-chips", ".preview-banner.compact", ".thinking-card",
	} {
		if !strings.Contains(string(css), required) {
			t.Fatalf("app.css lacks %q", required)
		}
	}
	mailCSS, err := fs.ReadFile(Files, "mail.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"http://", "https://", "@import", "url(http", "url(https", "!important"} {
		if strings.Contains(string(mailCSS), forbidden) {
			t.Fatalf("mail.css contains a network or override dependency %q", forbidden)
		}
	}
	for _, required := range []string{".mail-text", ".mail-quoted", ".mail-image-placeholder", ".mail-inline-asset", ".mail-inline-missing", `table[data-mail-table="layout"]`, `[data-theme="dark"]`, "unicode-bidi: plaintext", "max-width: 760px"} {
		if !strings.Contains(string(mailCSS), required) {
			t.Fatalf("mail.css lacks %q", required)
		}
	}
}

// The subject hierarchy is owned by internal/subject; the catalogs own its
// copy. Every closed path and every action value must be displayable in both
// languages, so a new subtopic cannot ship as a raw identifier.
func TestSubjectAndActionCopyIsComplete(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, topic := range subject.Topics() {
		for _, sub := range subject.Subtopics(topic) {
			key := "subject." + topic + "." + sub
			if _, ok := en[key]; !ok {
				t.Errorf("i18n.en.js lacks %q", key)
			}
			if _, ok := fr[key]; !ok {
				t.Errorf("i18n.fr.js lacks %q", key)
			}
		}
	}
	for _, primary := range []string{"reply", "decide", "pay", "schedule", "send", "do",
		"verify", "follow_up", "wait", "read"} {
		key := "action.primary." + primary
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`function subjectPathLabel(`, `function actionBadge(`,
		`function rowActionBadge(`, `triageAboutFacet(values, triage)`, `source.subtopics`} {
		if !strings.Contains(string(script), required) {
			t.Errorf("app.js lacks %q", required)
		}
	}
}

// The Teach screen speaks human question language: every profile question
// the UI help map knows has a localized title and hint, and every common
// choice option has a localized name. The map is UI-owned; the profile stays
// the owner of the model-facing question data.
func TestLabelsHelpMapAndOptionNames(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	names := []string{
		"category", "topic", "reply_needed", "urgency", "suspicious", "is_question",
		"call_to_action", "decision_needed", "asks_for", "deadline", "relationship",
		"effort", "sentiment", "stance", "info_sufficiency", "request_count", "has_signature",
	}
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, name := range names {
		if !strings.Contains(js, name+`: { help: "`+name+`"`) {
			t.Errorf("QUESTION_HELP lacks %q", name)
		}
		for _, suffix := range []string{".title", ".hint"} {
			key := "labels.q." + name + suffix
			if _, ok := en[key]; !ok {
				t.Errorf("i18n.en.js lacks %q", key)
			}
			if _, ok := fr[key]; !ok {
				t.Errorf("i18n.fr.js lacks %q", key)
			}
		}
	}
	for _, name := range []string{"urgency", "deadline", "effort", "sentiment", "request_count"} {
		for _, end := range []string{".low", ".high"} {
			key := "labels.q." + name + end
			if _, ok := en[key]; !ok {
				t.Errorf("i18n.en.js lacks scale endpoint %q", key)
			}
			if _, ok := fr[key]; !ok {
				t.Errorf("i18n.fr.js lacks scale endpoint %q", key)
			}
		}
	}

	raw, err := os.ReadFile("../triage/taxonomy/default_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Questions map[string]struct {
			Criteria json.RawMessage `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"category", "topic", "asks_for", "relationship", "stance"} {
		spec, ok := doc.Questions[question]
		if !ok {
			t.Fatalf("profile lacks %q", question)
		}
		var options map[string]json.RawMessage
		if err := json.Unmarshal(spec.Criteria, &options); err != nil {
			t.Fatalf("%s criteria: %v", question, err)
		}
		for option := range options {
			key := "labels.option." + question + "." + option
			if _, ok := en[key]; !ok {
				t.Errorf("i18n.en.js lacks option %q", key)
			}
			if _, ok := fr[key]; !ok {
				t.Errorf("i18n.fr.js lacks option %q", key)
			}
		}
	}

	for _, required := range []string{
		`function questionTitle(name)`, `function questionHint(name)`, `function optionLabel(questionName, optionName)`,
		`labels-question-chips`, `labels.progress.your`, `labels.progress.needed`,
		`labels.task.position`, `labels.topbar.progress`, `t("labels.advanced.summary")`,
		`t("labels.how")`, `tOr("labels.option."`, `t("labels.review.other")`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the friendly labels step %q", required)
		}
	}
}

// Review mode leads with the judge's decoded value and one-click acceptance; the
// alternative answer form sits below it. Saving must advance locally, not
// rebuild the route.
func TestLabelsSuggestionFirstAndInstantAdvance(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "async function renderLabels(")
	end := strings.Index(js, "function radioName(")
	if start < 0 || end <= start {
		t.Fatal("renderLabels not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`if (review && task.suggestion !== undefined && task.suggestion !== null)`,
		`saveTask(event.currentTarget, suggestion)`,
		`answerValueText(question, suggestion)`,
		`class: "review-suggestion labels-suggestion"`,
		`const savedAt = tasks.findIndex((item) => item.message_id === savedId);`,
		`tasks.splice(savedAt, 1)`,
		`showLabelTask(savedAt, { focusHeading: true })`,
		`showComplete(true)`,
		`afterLabelWrite();`,
		`setLabelKeyHandler(handleKey)`,
		`/^[1-9]$/`,
		`event.key === "Enter"`,
		`const sameAsSuggestion`,
		`String(event.key || "").toLowerCase()`,
		`labels.complete.next`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderLabels lacks %q", required)
		}
	}
	if strings.Contains(body, "await afterLabelWrite()") || strings.Contains(body, "await refreshActiveRoute()") {
		t.Fatal("saving a label waits for a route rebuild instead of advancing locally")
	}
	if strings.Contains(body, "refreshActiveRoute()") {
		t.Fatal("the label save path still refreshes the whole active route")
	}
	en := readCatalog(t, "i18n.en.js")
	if en["labels.review.accept"].text != "Use suggestion" || en["labels.form.save"].text != "Save and advance" {
		t.Errorf("review/save wording en accept=%q save=%q", en["labels.review.accept"].text, en["labels.form.save"].text)
	}
	if en["labels.form.saved"].text != "Saved" {
		t.Errorf("save toast title = %q", en["labels.form.saved"].text)
	}
}

// The save action is gated on a real answer, the answer pane keeps clearance
// above the sticky action bar, and the question strip exposes its overflow.
func TestLabelsSaveGateAndAnswerPaneScroll(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "async function renderLabels(")
	end := strings.Index(js, "function radioName(")
	if start < 0 || end <= start {
		t.Fatal("renderLabels not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`function hasPrimaryAnswer()`,
		`{ node: saveButton, blocked: () => !hasPrimaryAnswer() }`,
		`function updateSaveState()`,
		`if (taskActions) taskActions.sync();`,
		`updateSaveState();`,
		`if (override === undefined && !hasPrimaryAnswer())`,
		`if (!hasPrimaryAnswer()) {`,
		`focusFirstAnswer();`,
		`answerScroll.scrollTop = 0`,
		`if (options.focusHeading) heading.focus({ preventScroll: true })`,
		`new ResizeObserver(syncActionHeight)`,
		`--labels-actions-h`,
		`function updateChipScrollState()`,
		`"aria-keyshortcuts": "ArrowLeft ArrowRight Home End"`,
		`event.key === "ArrowRight"`,
		`const inset = 34`,
		`updateChipScrollState();`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderLabels lacks %q", required)
		}
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)
	for _, required := range []string{
		`var(--labels-actions-h, 96px)`,
		`scroll-padding-bottom`,
		`.labels-chips.has-left::before`,
		`.labels-chips.has-right::after`,
		`.labels-qchip.selected { min-width: 232px`,
		`.labels-action-row .button.primary:disabled`,
		`.labels-action-row .button.primary[aria-disabled="true"]`,
	} {
		if !strings.Contains(sheet, required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"labels.question.scroll_hint"} {
		if en[key].text == "" {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if fr[key].text == "" {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
}

// Ask answers name the provider only when a model actually wrote the answer.
// Retrieval and structured answers stay local, and no-match answers show no
// citation or mode pill.
func TestAskFooterHonestyAndPrivacyNote(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function askAnswerCard(")
	end := strings.Index(js, "function askErrorCard(")
	if start < 0 || end <= start {
		t.Fatal("askAnswerCard not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`const generated = view.mode === "generated"`,
		`if (!noMatches && !companyUnavailable) {`,
		`if (generated && view.provider && view.model)`,
		`t("ask.footer.local")`,
		`t("ask.footer.computed")`,
		`t("ask.footer.interpreted",`,
		`const citations = noMatches || clarify || companyUnavailable ? null : askCitationList(view)`,
		`view.notice === "company_profile_unavailable"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("askAnswerCard lacks %q", required)
		}
	}
	if strings.Contains(body, `view.provider && view.model ? h("span"`) {
		t.Error("askAnswerCard still renders the model footer unconditionally")
	}
	for _, required := range []string{
		`class: "ask-privacy fine"`,
		`routeHash("settings", { tab: "providers" })`,
		`t("ask.privacy.text")`,
		`t("ask.privacy.keys")`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the ask privacy note %q", required)
		}
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"ask.footer.local", "ask.footer.computed", "ask.footer.interpreted", "ask.footer.local_query", "ask.privacy.text", "ask.privacy.keys"} {
		if strings.TrimSpace(en[key].text) == "" {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	for _, phrase := range []string{"answer provider", "excerpts"} {
		if !strings.Contains(en["ask.privacy.text"].text, phrase) {
			t.Errorf("English privacy note lacks %q: %q", phrase, en["ask.privacy.text"].text)
		}
	}
	for _, phrase := range []string{"fournisseur de réponses", "extraits"} {
		if !strings.Contains(fr["ask.privacy.text"].text, phrase) {
			t.Errorf("French privacy note lacks %q: %q", phrase, fr["ask.privacy.text"].text)
		}
	}
	for _, catalog := range []map[string]catalogEntry{en, fr} {
		for _, key := range []string{"ask.privacy.text", "ask.thinking.writing", "ask.unavailable.body"} {
			if strings.Contains(catalog[key].text, "Gemma") {
				t.Errorf("%q still hard-codes the judge brand: %q", key, catalog[key].text)
			}
		}
	}
	if got := en["ask.footer.local"].text; strings.Contains(strings.ToLower(got), "model") && !strings.Contains(strings.ToLower(got), "no model") {
		t.Errorf("English local-search note implies a model: %q", got)
	}
}

// TestAnswerProviderState protects the four-state answer-provider read: a
// failed read is unknown, an empty selection is unconfigured, a selected
// record without a key is unavailable, and only a usable record is available.
func TestAnswerProviderState(t *testing.T) {
	runUISandbox(t, "answer_provider_state", answerProviderStateHarness, "answer provider state: ok")
}

const answerProviderStateHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");
const sandbox = { console };
sandbox.window = sandbox;
vm.createContext(sandbox);
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("// answer-provider:start");
const end = app.indexOf("// answer-provider:end");
assert.ok(start >= 0 && end > start, "answer-provider markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#answer-provider" });

const role = (extra) => ({ setting: "qa.provider", ...extra });
const state = (view) => sandbox.answerProviderState(view);
assert.equal(state(null).state, "unknown");
assert.equal(state({ roles: null }).state, "unknown");
assert.equal(state({ roles: [] }).state, "unknown");
assert.equal(state({ roles: [role({ provider: "" })] }).state, "unconfigured");
assert.equal(state({ roles: [role({ provider: "gen", configured: false, detail: "unknown record" })] }).state, "unavailable");
const available = state({
  providers: [{ id: "gen", label: "Generator" }],
  roles: [role({ provider: "gen", configured: true, available: true })],
});
assert.equal(available.state, "available");
assert.equal(available.name, "Generator");
const noKey = state({
  providers: [{ id: "gen" }],
  roles: [role({ provider: "gen", configured: true, available: false, detail: "the provider's API key is not set" })],
});
assert.equal(noKey.state, "unavailable");
assert.ok(noKey.detail.includes("API key"));
// A server that predates the available field still reports a configured
// record as usable; the composer must not disable Remote on that upgrade.
assert.equal(state({ roles: [role({ provider: "gen", configured: true })] }).state, "available");
console.log("answer provider state: ok");
`

// TestAskInitialSelectionAndPastAnswers must not present a saved provider-unavailable
// as the current state. Explicitly opening a history item still renders it,
// marked as past, and keeps its citations.
func TestAskInitialSelectionAndPastAnswers(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function askAutoView(view)`,
		`case "qa_unavailable":`,
		`case "qa_index_warming":`,
		`case "qa_generation_failed":`,
		`case "qa_no_grounded_answer":`,
		`if (!selected) selected = askAutoView(await askHistory.latestFullView());`,
		`workspace.showView(latest, { focus: true, earlier: false, past: true });`,
		`workspace.showView(view, { focus: true, earlier, past: true });`,
		`readAnswerProvider()`,
		`answerProviderState`,
		`providerState`,
		`options.providerState`,
		`t("ask.past", { time: pastTime })`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the Ask history step %q", required)
		}
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"ask.past", "ask.past.no_time"} {
		if strings.TrimSpace(en[key].text) == "" || strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("missing Ask past-answer catalog key %q", key)
		}
	}
	runUISandbox(t, "ask_initial_selection", askInitialSelectionHarness, "ask initial selection behavior: ok")
}

const askCardHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.formatRelative = I.formatRelative.bind(I);

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag,
    attrs: {},
    children: [],
    text: "",
    dataset: {},
    value: "",
    disabled: false,
    hidden: false,
    textContent: "",
    addEventListener() {},
    focus() {},
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); }
    if (key === "hidden") node.hidden = !!value;
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.icon = () => makeNode("svg", { class: "icon" }, []);
sandbox.warningBanner = (content, opts = {}) => makeNode("div",
  { class: "warning-banner " + (opts.className || ""), role: opts.role },
  [makeNode("svg", { class: "icon" }, []), content]);
sandbox.button = (label, onClick) => {
  const node = makeNode("button", { class: "button", text: String(label) }, []);
  node.onclick = onClick;
  return node;
};
sandbox.askModeLabel = (mode) => String(mode || "");
sandbox.pill = (value, kind) => makeNode("span", { class: "pill " + (kind || ""), text: value }, []);
sandbox.formatDuration = () => "1 s";
sandbox.previewPill = () => makeNode("span", { text: "preview" }, []);
sandbox.askCitationList = () => makeNode("ul", { class: "ask-citations" }, [makeNode("li", { text: "citation" }, [])]);
sandbox.askAnswerTextNode = (value) => makeNode("span", { text: String(value) }, []);
sandbox.askAnswerContentNode = (value) => makeNode("span", { text: String(value) }, []);
sandbox.structuredSentence = () => "structured";
sandbox.computedAnswerCard = () => makeNode("p", { text: "computed" }, []);
sandbox.clarifyAnswerCard = () => makeNode("div", { text: "clarify" }, []);
sandbox.routeHash = () => "#/settings?tab=providers";

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const helperStart = app.indexOf("function askAutoView(");
const helperEnd = app.indexOf("async function renderAsk(", helperStart);
assert.ok(helperStart >= 0 && helperEnd > helperStart, "askAutoView is missing");
vm.runInContext(app.slice(helperStart, helperEnd), sandbox, { filename: "app.js#ask-selection" });
const cardStart = app.indexOf("function askNoticeCard(");
const cardEnd = app.indexOf("function askErrorCard(", cardStart);
assert.ok(cardStart >= 0 && cardEnd > cardStart, "askNoticeCard/askAnswerCard is missing");
vm.runInContext(app.slice(cardStart, cardEnd), sandbox, { filename: "app.js#ask-card" });

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function anchors(node) {
  return all(node, (entry) => entry.tag === "a");
}
`

const askInitialSelectionHarness = askCardHarness + `
assert.equal(sandbox.askAutoView({ notice: "qa_unavailable" }), null);
assert.equal(sandbox.askAutoView({ notice: "qa_index_warming" }), null);
assert.equal(sandbox.askAutoView({ notice: "qa_generation_failed" }), null);
assert.equal(sandbox.askAutoView({ notice: "qa_no_grounded_answer" }), null);
assert.equal(sandbox.askAutoView({ mode: "retrieval", notice: "qa_unavailable" }), null);
assert.equal(sandbox.askAutoView(null), null);
const generatedView = { mode: "generated", notice: "", answer: "fresh" };
assert.equal(sandbox.askAutoView(generatedView), generatedView);
const languageNoticed = { mode: "generated", notice: "answer_language", answer: "written in another language" };
assert.equal(sandbox.askAutoView(languageNoticed), languageNoticed,
  "a generated answer with the language notice was hidden from the composer");

const unavailable = {
  id: 7,
  question: "An older question",
  mode: "retrieval",
  notice: "qa_unavailable",
  created_at: "2020-01-01T00:00:00Z",
  citations: [{ message: 1 }],
  took_ms: 0,
  provider: "example-provider-deepseek-v4-1-flash",
  model: "deepseek-v4.1-flash",
};
const pastCard = sandbox.askAnswerCard(unavailable, { past: true, providerState: "available" });
const pastText = texts(pastCard).join(" | ");
assert.ok(pastText.includes("Past answer"), "the past-answer note is missing");
assert.ok(pastText.includes("citation"), "the past answer lost its citations");
assert.equal(anchors(pastCard).filter((node) => String(node.attrs.href || "").includes("settings")).length, 0,
  "a configured provider still shows the Settings call-to-action");

const unconfiguredCard = sandbox.askAnswerCard(unavailable, { past: true, providerState: "unconfigured" });
assert.ok(anchors(unconfiguredCard).some((node) => String(node.attrs.href || "").includes("settings")),
  "an unconfigured provider lost the Settings call-to-action");

const freshCard = sandbox.askAnswerCard({
  id: 8,
  question: "A fresh question",
  mode: "generated",
  answer: "fresh live answer",
  provider: "provider.example.test",
  model: "deepseek-v4.1-flash-standard",
  citations: [{ message: 2 }],
  took_ms: 2800,
}, { providerState: "available" });
const freshText = texts(freshCard).join(" | ");
assert.ok(freshText.includes("fresh live answer"), "a live generated answer no longer renders");
assert.ok(freshText.includes("deepseek-v4.1-flash-standard"), "the model identifier was not kept as-is");
assert.ok(!freshText.includes("Past answer"), "a live answer is marked as past");
assert.ok(!freshText.includes("Written answer unavailable"), "a live answer shows the unavailable card");

console.log("ask initial selection behavior: ok");
`

// TestAskFallbackNoticeCards proves each retrieval notice keeps its own card:
// a warming index and a failed generation never tell the owner to configure a
// provider, a failed generation shows the provider that was called, and only
// an unconfigured provider keeps the settings action. Citations stay visible
// on every card, and an unknown notice keeps the generic fallback.
func TestAskFallbackNoticeCards(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	copy := []struct {
		key, english, french string
	}{
		{"ask.index_warming.title", "Search index is still warming", "L’index de recherche se prépare"},
		{"ask.index_warming.body", "Your messages are shown below. Try the question again in a moment.", "Vos messages sont affichés ci-dessous. Réessayez dans un instant."},
	}
	for _, tc := range copy {
		if got := en[tc.key].text; got != tc.english {
			t.Errorf("English %s = %q, want %q", tc.key, got, tc.english)
		}
		if got := fr[tc.key].text; got != tc.french {
			t.Errorf("French %s = %q, want %q", tc.key, got, tc.french)
		}
	}
	for _, key := range []string{
		"ask.index_warming.title", "ask.index_warming.body",
		"ask.generation_failed.title", "ask.generation_failed.body", "ask.generation_failed.provider",
		"ask.no_grounded.title", "ask.no_grounded.body",
		"ask.answer_language.title", "ask.answer_language.body",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function askNoticeCard(view, options = {})`,
		`notice === "qa_index_warming"`,
		`notice === "qa_generation_failed"`,
		`notice === "qa_no_grounded_answer"`,
		`notice === "answer_language"`,
		`t("ask.answer_language.title")`,
		`options.providerState`,
		`t("ask.generation_failed.provider", { provider: identity })`,
		`onRetry: (question) => submit(question)`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the Ask fallback step %q", required)
		}
	}
	runUISandbox(t, "ask_fallback_notices", askFallbackNoticeHarness, "ask fallback notice behavior: ok")
}

const askFallbackNoticeHarness = askCardHarness + `
const question = "trouve moi le numéro et courriel du support lcm";
function settingsLinks(node) {
  return anchors(node).filter((entry) => String(entry.attrs.href || "").includes("settings"));
}
function retryOf(node) {
  return all(node, (entry) => entry.tag === "button" && texts(entry).includes("Try again"))[0];
}
const retried = [];
const actions = { providerState: "available", onRetry: (value) => retried.push(value) };
const citation = [{ message: 1 }];

const warming = sandbox.askAnswerCard({
  id: 11, question, mode: "retrieval", notice: "qa_index_warming", citations: citation,
  provider: "", model: "", took_ms: 0,
}, actions);
const warmingText = texts(warming).join(" | ");
assert.ok(warmingText.includes("Search index is still warming"), "the warming title is missing");
assert.ok(warmingText.includes("Your messages are shown below. Try the question again in a moment."),
  "the warming body is missing");
assert.ok(warmingText.includes("citation"), "the warming card lost its citations");
assert.ok(!warmingText.includes("Provider:"), "the warming card invented a provider line");
assert.equal(settingsLinks(warming).length, 0, "the warming card shows the provider settings action");
const warmingRetry = retryOf(warming);
assert.ok(warmingRetry, "the warming card offers no retry");
warmingRetry.onclick();
assert.deepEqual(retried, [question], "the warming retry did not resubmit the question");

const failed = sandbox.askAnswerCard({
  id: 12, question, mode: "retrieval", notice: "qa_generation_failed", citations: citation,
  provider: "provider.example.test", model: "deepseek-v4.1-flash-standard", took_ms: 2767,
}, actions);
const failedText = texts(failed).join(" | ");
assert.ok(failedText.includes("The written answer failed"), "the generation-failure title is missing");
assert.ok(failedText.includes("Provider: provider.example.test · deepseek-v4.1-flash-standard"),
  "the generation-failure card does not name the provider that was called");
assert.ok(failedText.includes("citation"), "the generation-failure card lost its citations");
assert.ok(!failedText.includes("no model was used"), "a failed provider call is described as local-only");
assert.equal(settingsLinks(failed).length, 0, "a failed call still asks for provider settings");
assert.ok(retryOf(failed), "the generation-failure card offers no retry");

const ungrounded = sandbox.askAnswerCard({
  id: 13, question, mode: "retrieval", notice: "qa_no_grounded_answer", citations: citation,
  provider: "provider.example.test", model: "deepseek-v4.1-flash-standard", took_ms: 2767,
}, actions);
const ungroundedText = texts(ungrounded).join(" | ");
assert.ok(ungroundedText.includes("No sufficiently grounded answer"), "the ungrounded title is missing");
assert.ok(ungroundedText.includes("Provider: provider.example.test · deepseek-v4.1-flash-standard"),
  "the ungrounded card does not name the provider that answered");
assert.ok(ungroundedText.includes("citation"), "the ungrounded card lost its citations");
assert.ok(!ungroundedText.includes("no model was used"), "an ungrounded provider answer is described as local-only");
assert.equal(settingsLinks(ungrounded).length, 0, "an ungrounded answer asks for provider settings");

const unconfigured = sandbox.askAnswerCard({
  id: 14, question, mode: "retrieval", notice: "qa_unavailable", citations: citation,
  provider: "", model: "", took_ms: 0,
}, { providerState: "unconfigured" });
assert.ok(texts(unconfigured).join(" | ").includes("Open provider settings"),
  "the unconfigured provider lost its settings action");
assert.ok(settingsLinks(unconfigured).length > 0, "the unconfigured provider has no settings link");
assert.equal(retryOf(unconfigured), undefined, "the missing-provider card gained a retry action");

const configured = sandbox.askAnswerCard({
  id: 15, question, mode: "retrieval", notice: "qa_unavailable", citations: citation,
  provider: "provider.example.test", model: "deepseek-v4.1-flash-standard", took_ms: 2767,
}, { providerState: "available" });
assert.equal(settingsLinks(configured).length, 0, "a configured provider still shows the settings action");

const unknown = sandbox.askAnswerCard({
  id: 16, question, mode: "retrieval", notice: "qa_future_notice", citations: citation,
  provider: "", model: "", took_ms: 0,
}, { providerState: "unconfigured" });
const unknownText = texts(unknown).join(" | ");
assert.ok(unknownText.includes("Written answer unavailable"), "the unknown notice lost the generic title");
assert.ok(unknownText.includes("citation"), "the unknown notice lost its citations");
assert.equal(settingsLinks(unknown).length, 0, "an unknown notice shows the provider settings action");
const unknownView = { mode: "retrieval", notice: "qa_future_notice" };
assert.equal(sandbox.askAutoView(unknownView), unknownView, "an unknown notice was hidden from the composer");

console.log("ask fallback notice behavior: ok");
`

// TestAskAnswerLanguageNotice proves a wrong-language answer keeps its own
// text, citations and provider/model footer beside a warning card with no
// retry action and no provider-settings link. The composer also accepts
// that answer as the current state.
func TestAskAnswerLanguageNotice(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"ask.answer_language.title", "ask.answer_language.body"} {
		if strings.TrimSpace(en[key].text) == "" || strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("missing answer-language catalog key %q", key)
		}
	}
	runUISandbox(t, "ask_answer_language", askAnswerLanguageHarness, "ask answer language notice behavior: ok")
}

const askAnswerLanguageHarness = askCardHarness + `
const answer = "The invoice was approved by the finance team and the payment will follow this week.";
const view = {
  id: 21,
  question: "Que s'est-il passé avec la facture ?",
  mode: "generated",
  notice: "answer_language",
  answer,
  provider: "provider.example.test",
  model: "deepseek-v4.1-flash-standard",
  citations: [{ message: 1 }],
  took_ms: 2800,
};
let retried = 0;
const card = sandbox.askAnswerCard(view, { providerState: "available", onRetry: () => { retried++; } });
const cardText = texts(card).join(" | ");
assert.ok(cardText.includes(answer), "the noticed answer text was hidden");
assert.ok(cardText.includes("The answer is in another language"), "the language title is missing");
assert.ok(cardText.includes("different language than the one you asked for"), "the language body is missing");
assert.ok(cardText.includes("provider.example.test · deepseek-v4.1-flash-standard"),
  "the provider/model footer was dropped from a noticed answer");
assert.ok(cardText.includes("citation"), "the noticed answer lost its citations");
const settings = anchors(card).filter((entry) => String(entry.attrs.href || "").includes("settings"));
assert.equal(settings.length, 0, "the language notice offers a provider-settings link");
const buttons = all(card, (entry) => entry.tag === "button");
assert.equal(buttons.length, 0, "the language notice gained an action button");
assert.equal(retried, 0, "the language notice retried the question");
assert.equal(sandbox.askAutoView(view), view, "askAutoView hid a noticed generated answer");

console.log("ask answer language notice behavior: ok");
`

func TestHomeBriefDashboard(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{
		"hub.learning.judge", "hub.learning.top_question", "hub.learning.question_progress",
		"hub.learning.no_question", "hub.learning.progress", "hub.brief.title",
		"hub.brief.replies", "hub.brief.deadlines", "hub.brief.meetings",
		"hub.brief.more", "hub.brief.more_hint", "hub.brief.signals", "hub.brief.signals_empty",
		"hub.brief.counts_hint",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function homeLearningSection(")
	end := strings.Index(js, "function homeMailboxSection(")
	if start < 0 || end <= start {
		t.Fatal("homeLearningSection not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`hub.learning.judge`, `hub.learning.top_question`,
		`hub.learning.question_progress`, `questionTitle(data.top.question)`,
		`hub.learning.no_question`, `hub.learning.review`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("homeLearningSection lacks %q", required)
		}
	}
	for _, forbidden := range []string{"blockerTitle(", "blockerDetail(", "blocker.code"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("homeLearningSection still shows the raw blocker via %q", forbidden)
		}
	}
	statStart := strings.Index(js, "function homeBriefStatNodes(")
	statEnd := strings.Index(js, "function homeBriefSignals(")
	if statStart < 0 || statEnd <= statStart {
		t.Fatal("homeBriefStatNodes not found")
	}
	stats := js[statStart:statEnd]
	for _, required := range []string{
		"agenda.obligations", "agenda.deadlines", "agenda.events", "hub.brief.stat_unavailable",
	} {
		if !strings.Contains(stats, required) {
			t.Errorf("homeBriefStatNodes lacks %q", required)
		}
	}
	hubStart := strings.Index(js, "async function renderHub(")
	hubEnd := strings.Index(js, "function lockedHome(")
	if hubStart < 0 || hubEnd <= hubStart {
		t.Fatal("renderHub not found")
	}
	hub := js[hubStart:hubEnd]
	for _, required := range []string{
		`class: "home-head"`,
		`class: "home-brief-grid"`,
		`homeBriefSection(`,
		`homeAgendaSection(`,
		`homeImportantSection(`,
		`homeRecentSection(`,
		`homeLearningSection(`,
		`homeMailboxSection(`,
		`h("details", { class: "surface home-more"`,
		`showPreview ? previewBanner(t("preview.home_body")`,
	} {
		if !strings.Contains(hub, required) {
			t.Errorf("renderHub lacks %q", required)
		}
	}
	for _, forbidden := range []string{`createAskWorkspace(`, `ask.node`, `home-ask`, `home-top`} {
		if strings.Contains(hub, forbidden) {
			t.Errorf("renderHub still renders the old Home Ask block via %q", forbidden)
		}
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".home-head", ".home-brief-grid", ".home-inset", ".home-brief-body",
		".home-more", ".mail-row-compact", ".preview-banner.compact .preview-banner-body",
		".labels-task-layout", ".labels-answer-pane", ".labels-question-chips", ".labels-choice",
		".labels-yesno", ".labels-scale", ".labels-actions", ".labels-complete",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
}

// TestCompactDensity keeps the measured density pass from drifting back:
// the shell, the tables and the message rows are compact by default, the
// muted ink stays readable, and the one-line snippet clamp is explicit.
func TestCompactDensity(t *testing.T) {
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(css)
	for _, required := range []string{
		"--muted: #5f6a63",
		"--display-size: clamp(28px",
		"padding: 14px 20px 13px",
		"padding: 14px 20px;",
		".data-table td { padding: 6px 10px",
		".mail-row {",
		"padding: 9px 12px",
		"-webkit-line-clamp: 1",
		".ask-sentence",
		"font-size: 20px",
	} {
		if !strings.Contains(styles, required) {
			t.Errorf("app.css lost the compact density value %q", required)
		}
	}
	if strings.Contains(styles, "--muted: #737c76") {
		t.Error("app.css still uses the old low-contrast muted ink")
	}
}

func TestAskNoMatchesAndUnavailableCopy(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	if got := en["ask.nomatch.body"].text; got != "I couldn't find anything relevant in your mail." {
		t.Errorf("English no-match body = %q", got)
	}
	if got := fr["ask.nomatch.body"].text; got != "Je n’ai rien trouvé de pertinent dans votre courrier." {
		t.Errorf("French no-match body = %q", got)
	}
	for _, key := range []string{"ask.nomatch.title", "ask.nomatch.body", "ask.nomatch.hint", "ask.history.repeat", "ask.history.label_history"} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	for _, catalog := range []struct {
		name    string
		entries map[string]catalogEntry
	}{
		{"i18n.en.js", en}, {"i18n.fr.js", fr},
	} {
		body := catalog.entries["ask.unavailable.body"].text
		namesProvider := strings.Contains(strings.ToLower(body), "provider") || strings.Contains(strings.ToLower(body), "fournisseur")
		namesKey := strings.Contains(strings.ToLower(body), "key") || strings.Contains(strings.ToLower(body), "clé")
		namesSettings := strings.Contains(body, "provider settings") || strings.Contains(body, "réglages des fournisseurs")
		if !namesProvider || !namesKey || !namesSettings {
			t.Errorf("%s unavailable copy does not name the provider, its key and provider settings: %q", catalog.name, body)
		}
		if strings.Contains(body, "Gemma") {
			t.Errorf("%s unavailable copy still hard-codes the judge brand: %q", catalog.name, body)
		}
		if strings.Contains(strings.ToLower(body), "local model") || strings.Contains(strings.ToLower(body), "modèle local") {
			t.Errorf("%s unavailable copy still blames the local classifier: %q", catalog.name, body)
		}
	}
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`view.notice === "no_matches"`,
		`const citations = noMatches || clarify || companyUnavailable ? null : askCitationList(view)`,
		`view.notice === "company_profile_unavailable"`,
		`uniqueByQuestion(items)`,
		`t("ask.history.repeat", { count })`,
		`t("ask.history.label_history")`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the Ask polish step %q", required)
		}
	}
}

// Ask keeps a short per-conversation transcript in the main column: a
// follow-up appends to the same visible thread and reuses its conversation
// id, while "New conversation" clears it. Older answers stay in the history rail,
// computed plans render typed results, and a clarification submits one of its
// complete questions through the composer.
func TestAskConversationTranscriptComputedAndClarify(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`class: "ask-current ask-thread"`,
		`"aria-live": "polite"`,
		`clear(current)`,
		`showThinking(question)`,
		`showView(selected, { focus: false, earlier, past: !!requestedId })`,
		`item.setAttribute("aria-current", "true")`,
		`syncAskRoute`,
		`workspace.showEmpty()`,
		`ask.earlier.back`,
		`ask.empty.title`,
		`const computed = view.mode === "computed";`,
		`function computedAnswerCard(`,
		`if (view.intent === "company" && view.answer)`,
		`ask.company.unavailable.title`,
		`function contactResultNode(`,
		`function companyResultNode(`,
		`function companyPeopleCount(`,
		`function messageResultNode(`,
		`function timeResultNode(`,
		`function clarifyAnswerCard(`,
		`clarify.candidates`,
		`ask-clarify-card`,
		`"data-question"`,
		`ask.clarify.candidate_messages`,
		`if (kind === "companies") return companyResultNode(result)`,
		`if (kind === "contacts")`,
		`contactResultNode(view)`,
		`ask.computed.contacts.topic_lead`,
		`ask.computed.contacts.topic_capped`,
		`label.toLocaleLowerCase() !== address.toLocaleLowerCase()`,
		`formatNumber(row.messages)`,
		`formatNumber(row.sent)`,
		`formatNumber(row.received)`,
		`formatNumber(row.threads)`,
		`formatRelative(row.last_at)`,
		`ask.computed.companies.caption`,
		`ask.computed.companies.people`,
		`ask.computed.table.company`,
		`ask.computed.table.people`,
		`ask.computed.contacts.caption`,
		`ask.computed.table.rank`,
		`ask.computed.messages.count`,
		`ask.computed.messages.list`,
		`ask.computed.time.lead`,
		`ask.weekday.`,
		`ask.clarify.options_aria`,
		`onClarify: (question) => submit(question)`,
		`let conversationId = ""`,
		`payload.conversation_id = conversationId`,
		`view.conversation_id`,
		`t("ask.conversation.start_new")`,
		`t(active ? "ask.conversation.reply" : "ask.send")`,
		`t("ask.thread.count", { count: exchanges })`,
		`class: "ask-thread-head"`,
		`class: "select compact ask-route-select"`,
		`t("ask.route.hint." + routeSelect.value`,
		`t("ask.composer.continue")`,
		`t("ask.composer.saved")`,
		`route: routeSelect.value`,
		`syncConversation()`,
		`rememberTurn(question, view, placement.turn, { focus: false })`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the conversation Ask step %q", required)
		}
	}
	for _, forbidden := range []string{`thread.append`, `MAX_ASK_TURNS`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js still grows a multi-turn thread via %q", forbidden)
		}
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"ask.mode.computed", "ask.mode.clarify",
		"ask.conversation.start_new", "ask.conversation.reply",
		"ask.thread.current", "ask.thread.new", "ask.thread.saved",
		"ask.thread.new_title", "ask.thread.new_hint", "ask.thread.count", "ask.thread.saved_hint",
		"ask.composer.new", "ask.composer.continue", "ask.composer.saved",
		"ask.route.label", "ask.route.aria", "ask.route.auto", "ask.route.local",
		"ask.route.remote", "ask.route.remote_named", "ask.route.remote_unconfigured",
		"ask.route.hint.auto", "ask.route.hint.local", "ask.route.hint.remote",
		"ask.route.unavailable.title", "ask.route.unavailable.body", "ask.route.unavailable.action",
		"ask.local.title", "ask.local.body",
		"ask.empty.title", "ask.empty.body", "ask.empty.suggestions_aria",
		"ask.earlier.text", "ask.earlier.back",
		"ask.computed.contacts.lead", "ask.computed.contacts.caption",
		"ask.computed.contacts.empty_title", "ask.computed.contacts.empty_body",
		"ask.computed.contacts.topic_lead", "ask.computed.contacts.topic_caption",
		"ask.computed.contacts.topic_capped",
		"ask.computed.companies.lead", "ask.computed.companies.caption",
		"ask.computed.companies.empty_title", "ask.computed.companies.empty_body",
		"ask.computed.companies.people",
		"ask.computed.messages.count", "ask.computed.messages.list", "ask.computed.messages.none",
		"ask.computed.time.lead", "ask.computed.time.caption", "ask.computed.time.period",
		"ask.computed.time.messages", "ask.computed.time.empty_title", "ask.computed.time.empty_body",
		"ask.computed.unknown",
		"ask.company.unavailable.title", "ask.company.unavailable.body",
		"ask.computed.table.rank", "ask.computed.table.contact", "ask.computed.table.company",
		"ask.computed.table.people", "ask.computed.table.messages",
		"ask.computed.table.sent", "ask.computed.table.received", "ask.computed.table.threads",
		"ask.computed.table.last",
		"ask.weekday.monday", "ask.weekday.tuesday", "ask.weekday.wednesday",
		"ask.weekday.thursday", "ask.weekday.friday", "ask.weekday.saturday", "ask.weekday.sunday",
		"ask.clarify.hint", "ask.clarify.options_aria",
		"ask.clarify.candidate_messages",
		"ask.footer.computed", "ask.footer.interpreted", "ask.footer.local_query",
		"reason.match", "reason.retrieved", "reason.company", "reason.agenda", "reason.priority",
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	if got := en["ask.mode.computed"].text; got != "Computed locally" {
		t.Errorf("English computed mode = %q", got)
	}
	if got := fr["ask.mode.computed"].text; got != "Calculé localement" {
		t.Errorf("French computed mode = %q", got)
	}
	if got := en["ask.mode.clarify"].text; got != "Needs a detail" {
		t.Errorf("English clarify mode = %q", got)
	}
	if got := fr["ask.mode.clarify"].text; got != "Précision requise" {
		t.Errorf("French clarify mode = %q", got)
	}
	for _, key := range []string{
		"ask.computed.contacts.lead", "ask.computed.messages.count",
		"ask.computed.messages.list", "ask.computed.time.lead",
		"ask.computed.companies.lead", "ask.computed.companies.people",
		"ask.computed.contacts.topic_lead",
	} {
		if !en[key].isPlural || !fr[key].isPlural {
			t.Errorf("%q must be a plural entry", key)
			continue
		}
		for _, form := range []string{"one", "other"} {
			if !strings.Contains(en[key].plural[form], "{count}") ||
				!strings.Contains(fr[key].plural[form], "{count}") {
				t.Errorf("%s.%s must carry the count placeholder", key, form)
			}
		}
	}
	if got := en["ask.computed.companies.lead"].plural["other"]; got != "{count} companies, grouped by organization." {
		t.Errorf("English company lead = %q", got)
	}
	if got := fr["ask.computed.companies.lead"].plural["other"]; got != "{count} entreprises, regroupées par organisation." {
		t.Errorf("French company lead = %q", got)
	}
	if got := en["ask.computed.companies.caption"].text; got != "External companies ranked by message volume" {
		t.Errorf("English company caption = %q", got)
	}
	if !en["ask.clarify.candidate_messages"].isPlural || !fr["ask.clarify.candidate_messages"].isPlural {
		t.Errorf("ask.clarify.candidate_messages must be a plural entry")
	}
	if got := en["ask.clarify.candidate_messages"].plural["one"]; got != "1 message" {
		t.Errorf("English candidate one = %q", got)
	}
	if got := fr["ask.clarify.candidate_messages"].plural["one"]; got != "1 message" {
		t.Errorf("French candidate one = %q", got)
	}
	if got := en["ask.computed.contacts.topic_lead"].plural["other"]; got != "{count} contacts · {messages} matching messages" {
		t.Errorf("English topic contact lead = %q", got)
	}
	if got := fr["ask.computed.contacts.topic_lead"].plural["other"]; got != "{count} contacts · {messages} messages correspondants" {
		t.Errorf("French topic contact lead = %q", got)
	}
	if got := en["ask.computed.contacts.topic_capped"].text; got != "The list is limited to the best-matching messages." {
		t.Errorf("English topic capped note = %q", got)
	}
	if strings.Contains(strings.ToLower(en["ask.footer.computed"].text), "interpreted") {
		t.Errorf("the computed footer still implies the model wrote the numbers: %q", en["ask.footer.computed"].text)
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".ask-current", ".ask-thread", ".ask-turn-question", ".ask-earlier", ".ask-computed", ".ask-contact-table", ".ask-company-table",
		".ask-clarify-options", ".ask-clarify-card", ".ask-contact-capped", `.history-item[aria-current="true"]`,
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
}

// Companies are one computed result kind: the table keeps the contact
// metrics and adds the organization domain plus the number of merged people.
func TestCompanyResults(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function companyResultNode(`,
		`function companyPeopleCount(`,
		`if (kind === "companies") return companyResultNode(result)`,
		`const people = companyPeopleCount(row)`,
		`Array.isArray(merged)`,
		`Object.keys(merged).length`,
		`row && row.label`,
		`row && row.address`,
		`row && row.key`,
		`badge(t("ask.computed.companies.people", { count: people }), "plain ask-company-people")`,
		`formatNumber(people)`,
		`formatNumber(row.messages)`,
		`formatNumber(row.sent)`,
		`formatNumber(row.received)`,
		`formatNumber(row.threads)`,
		`formatRelative(row.last_at)`,
		`ask.computed.companies.lead`,
		`ask.computed.companies.caption`,
		`ask.computed.companies.empty_title`,
		`ask.computed.companies.empty_body`,
		`ask.computed.table.company`,
		`ask.computed.table.people`,
		`"ask-company-table"`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the company result step %q", required)
		}
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"ask.computed.companies.lead", "ask.computed.companies.caption",
		"ask.computed.companies.empty_title", "ask.computed.companies.empty_body",
		"ask.computed.companies.people", "ask.computed.table.company", "ask.computed.table.people",
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	for _, key := range []string{"ask.computed.companies.lead", "ask.computed.companies.people"} {
		if !en[key].isPlural || !fr[key].isPlural {
			t.Errorf("%q must be a plural entry", key)
			continue
		}
		for _, form := range []string{"one", "other"} {
			if !strings.Contains(en[key].plural[form], "{count}") ||
				!strings.Contains(fr[key].plural[form], "{count}") {
				t.Errorf("%s.%s must carry the count placeholder", key, form)
			}
		}
	}
	if got := en["ask.computed.companies.caption"].text; got != "External companies ranked by message volume" {
		t.Errorf("English company caption = %q", got)
	}
	if got := fr["ask.computed.companies.empty_title"].text; got != "Aucune entreprise à classer" {
		t.Errorf("French company empty title = %q", got)
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".ask-company-table", ".ask-company-meta", ".ask-company-domain", ".ask-company-people"} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
}

// TestCompaniesExplorer covers the gated two-pane organization explorer:
// ranked list, deep profile, friendly thread states and deep links.
func TestCompaniesExplorer(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`companies: 20000,`,
		`function loadCompanies(search = "", limit = COMPANIES_LIST_LIMIT)`,
		`"/api/companies?limit=" + encodeURIComponent(String(limit))`,
		`function loadCompany(key, months = COMPANIES_MONTHS, eventDays = COMPANIES_EVENT_DAYS)`,
		`"/api/companies/" + encodeURIComponent(String(key)) +`,
		`"?months=" + encodeURIComponent(String(months)) +`,
		`"&event_days=" + encodeURIComponent(String(eventDays))`,
		`function renderCompanies(route, seq)`,
		`case "companies": node = await renderCompanies(route, seq)`,
		`if (name === "companies")`,
		`if (!open && !preview) return lockedScreen(learning, t("companies.locked_feature"));`,
		`route.params.get("key")`,
		`(companies[0] && companies[0].key ? String(companies[0].key) : "")`,
		`navigate("companies", { key, q: input.value.trim() || undefined })`,
		`scrollCompaniesProfileSoon(index)`,
		`function loadCompanyDetail(key, host, index)`,
		`function observeCompanyRows(body, rows)`,
		`function prefetchCompanyProfiles(keys)`,
		`node.dataset.companyIndex = String(index)`,
		`"aria-current": selected ? "true" : undefined`,
		`function companiesListPane(view, error, selectedKey, query)`,
		`node.dataset.search = companySearchText(`,
		`function companyProfileNode(profile, index = -1)`,
		`function companyProfileStateNode(`,
		`threadStateLabel(state)`,
		`companyThreadReasonLabel(entry.reason)`,
		`function companyNextAction(profile)`,
		`function companyIntelligenceSection(profile)`,
		`companyNextActionNode(profile)`,
		`THREAD_STATES.map((state)`,
		`role: "img"`,
		`"aria-label": total`,
		`companyMonthSeries(months, lastMonth)`,
		`mailHref(entry.message_id, "important")`,
		`mailHref(item.message_id, "important")`,
		`mailHref(deadline.message_id, "important")`,
		`function companiesCountText(query, shown, total, count)`,
		`t("companies.list.count", { count })`,
		`routeFailed(node)`,
		`t("companies.list.coverage"`,
		`t("companies.empty.title")`,
		`t("companies.empty.body")`,
		`t("companies.error.list")`,
		`t("companies.error.profile")`,
		`t("common.try_again")`,
		`t("companies.badge.address_book")`,
		`t("companies.badge.domain")`,
		`t("companies.kpi.last_contact")`,
		`t("companies.section.volume")`,
		`t("companies.section.threads")`,
		`t("companies.section.recent")`,
		`t("companies.section.people")`,
		`t("companies.section.meetings")`,
		`t("companies.section.deadlines")`,
		`t("companies.section.intelligence")`,
		`t("companies.ledger.owner")`,
		`t("companies.ledger.waiting")`,
		`t("companies.action.ask")`,
		`t("companies.next.label")`,
		`t("companies.person.direction"`,
		`t("companies.recent.attachment")`,
		`t("companies.meeting.organizer"`,
		`t("companies.meeting.attendees"`,
		`t("companies.meeting.duration"`,
		`t("companies.search.selected_outside"`,
		`t("companies.list.showing"`,
		`t("companies.threads.showing"`,
		`t("companies.meetings.empty")`,
		`t("companies.deadlines.empty")`,
		`t("companies.table.name")`,
		`t("companies.table.address")`,
		`t("companies.table.phone")`,
		`t("companies.table.role")`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the companies explorer step %q", required)
		}
	}
	for _, forbidden := range []string{
		`text: row.key`, `text: profile.key`, `text: company.key`,
		`text: entry.state`, `"aria-label": entry.state`,
		`text: "c:"`, `text: "d:"`, `replaceRoute("companies"`,
		`dataset.companyKey`, `data-company-key`,
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js exposes a raw company or state value via %q", forbidden)
		}
	}

	// The list always mounts; a profile failure only changes the right pane.
	start := strings.Index(js, "async function renderCompanies(route, seq)")
	end := strings.Index(js[start:], "\n  async function renderSources")
	if start < 0 || end <= 0 {
		t.Fatal("renderCompanies was not found")
	}
	body := js[start : start+end]
	for _, required := range []string{
		`try {`,
		`view = await loadCompanies(query);`,
		`profile = await loadCompany(selectedKey);`,
		`profileError = error;`,
		`if (profile) {`,
		`} else if (profileError) {`,
		`} else if (listError) {`,
		`companiesListPane(view, listError, selectedKey, query)`,
		`companyProfileStateNode(`,
		`state.companiesCount = listTotal;`,
		`screen.dataset.routeError = "1";`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderCompanies lacks %q", required)
		}
	}
	if strings.Contains(body, `if (!listError && selectedKey)`) {
		t.Error("renderCompanies still gates a valid deep link on the list read")
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"nav.companies", "screen.companies.title", "screen.companies.eyebrow",
		"companies.list.title", "companies.list.aria", "companies.list.count",
		"companies.list.filtered", "companies.list.showing", "companies.list.coverage",
		"companies.list.people", "companies.list.messages", "companies.list.row_aria",
		"companies.list.no_domain", "companies.list.inbound_only", "companies.list.unanswered",
		"companies.search.placeholder", "companies.search.aria",
		"companies.search.clear", "companies.search.no_matches", "companies.search.selected_outside",
		"companies.empty.title", "companies.empty.body", "companies.unknown_label",
		"companies.profile.eyebrow", "companies.profile.domains_aria",
		"companies.profile.no_domain", "companies.profile.select",
		"companies.profile.select_body", "companies.profile.generated",
		"companies.profile.actions_aria", "companies.action.ask", "companies.action.ask_prefill",
		"companies.people.contacted", "companies.people.known",
		"companies.badge.address_book", "companies.badge.domain",
		"companies.kpi.aria", "companies.kpi.all_time", "companies.kpi.messages",
		"companies.kpi.sent", "companies.kpi.received", "companies.kpi.threads",
		"companies.kpi.last_contact", "companies.section.intelligence",
		"companies.intelligence.sub", "companies.brief.no_mail", "companies.brief.span",
		"companies.brief.activity", "companies.brief.direction", "companies.brief.inbound_only",
		"companies.brief.outbound_only", "companies.brief.open", "companies.brief.no_open",
		"companies.next.label", "companies.next.reply", "companies.next.reply_meta",
		"companies.next.reply_meta_anon", "companies.next.deadline", "companies.next.due",
		"companies.next.meeting", "companies.next.meeting_meta", "companies.next.waiting",
		"companies.next.waiting_meta", "companies.next.one_way_in", "companies.next.one_way_out",
		"companies.next.awaiting_reply", "companies.next.read", "companies.next.message_meta",
		"companies.next.sent_meta", "companies.next.start",
		"companies.section.volume", "companies.section.volume_sub",
		"companies.chart.aria", "companies.chart.bar", "companies.chart.empty",
		"companies.section.threads", "companies.threads.sub",
		"companies.threads.states_aria", "companies.threads.showing",
		"companies.ledger.owner", "companies.ledger.owner_sub", "companies.ledger.owner_empty",
		"companies.ledger.waiting", "companies.ledger.waiting_sub", "companies.ledger.waiting_empty",
		"companies.thread.actor", "companies.thread.messages", "companies.thread.open_aria",
		"companies.reason.inbound_last", "companies.reason.owner_last",
		"companies.reason.visibility_gap", "companies.reason.auto_reply",
		"companies.reason.no_reply_expected", "companies.reason.cc_only",
		"companies.section.recent", "companies.recent.sub", "companies.recent.empty",
		"companies.recent.attachment", "companies.recent.open_aria",
		"companies.section.people", "companies.people.sub", "companies.people.empty",
		"companies.person.no_mail", "companies.person.direction",
		"companies.table.name", "companies.table.address", "companies.table.messages",
		"companies.table.last", "companies.table.role",
		"companies.section.meetings", "companies.meetings.sub", "companies.meetings.empty",
		"companies.meeting.recurring", "companies.meeting.tentative",
		"companies.meeting.organizer", "companies.meeting.attendees",
		"companies.meeting.duration", "companies.meeting.join",
		"companies.section.deadlines", "companies.deadlines.sub", "companies.deadlines.empty",
		"companies.deadline.open_aria",
		"companies.locked_feature", "companies.error.list_title", "companies.error.list",
		"companies.error.profile_title", "companies.error.profile",
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	for _, key := range []string{
		"companies.list.count", "companies.list.people", "companies.list.messages",
		"companies.people.contacted", "companies.people.known", "companies.meeting.attendees",
		"companies.thread.messages",
	} {
		if !en[key].isPlural || !fr[key].isPlural {
			t.Errorf("%q must be a plural entry", key)
			continue
		}
		for _, form := range []string{"one", "other"} {
			if !strings.Contains(en[key].plural[form], "{count}") ||
				!strings.Contains(fr[key].plural[form], "{count}") {
				t.Errorf("%s.%s must carry the count placeholder", key, form)
			}
		}
	}
	if got := en["companies.empty.title"].text; got != "No companies yet" {
		t.Errorf("English empty title = %q", got)
	}
	if got := fr["companies.empty.title"].text; got != "Aucune entreprise pour l’instant" {
		t.Errorf("French empty title = %q", got)
	}
	if got := en["companies.ledger.owner_empty"].text; got != "Nothing is waiting on your reply." {
		t.Errorf("English owner-ledger empty = %q", got)
	}
	if got := en["companies.meetings.empty"].text; got != "No upcoming meetings" {
		t.Errorf("English meetings empty = %q", got)
	}
	if got := en["companies.deadlines.empty"].text; got != "No due commitments" {
		t.Errorf("English deadlines empty = %q", got)
	}
	if got := en["companies.badge.address_book"].text; got != "Address book" {
		t.Errorf("English address-book badge = %q", got)
	}
	if got := en["companies.badge.domain"].text; got != "Domain" {
		t.Errorf("English domain badge = %q", got)
	}
	if got := en["companies.search.placeholder"].text; got != "Search all companies" {
		t.Errorf("English search placeholder = %q", got)
	}
	// Thread state wording is owned by the conversation view; the profile
	// reuses it instead of inventing a second set of state labels.
	if got := en["detail.thread.state.awaiting_owner"].text; got != "Waiting on you" {
		t.Errorf("English awaiting-owner label = %q", got)
	}
	if got := fr["detail.thread.state.awaiting_owner"].text; got != "En attente de vous" {
		t.Errorf("French awaiting-owner label = %q", got)
	}
	for key, entry := range en {
		if !strings.HasPrefix(key, "companies.") {
			continue
		}
		for _, value := range entryStrings(entry) {
			for _, raw := range []string{"awaiting_owner", "awaiting_other", "answered_by_owner", "answered_by_other", "no_action"} {
				if strings.Contains(value, raw) {
					t.Errorf("%s leaks the raw state %q", key, raw)
				}
			}
		}
	}
	if strings.Contains(js, `t("companies.state.`) {
		t.Error("the profile invented a parallel thread-state catalog")
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".companies-layout", ".companies-list-pane", ".companies-row",
		".companies-row.selected", ".companies-profile", ".companies-profile-head",
		".companies-kpis", ".companies-chart", ".companies-state-grid",
		".companies-people-table", ".companies-brief-list", ".companies-next",
		".companies-ledger", ".companies-loop-row", ".companies-truncation",
		".companies-person-direction", ".companies-search-note",
		".companies-list-body { max-height: 320px; overflow-y: auto; }",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}

	runCompaniesBehavior(t)
}

// TestBriefScreen covers the top-level Brief: its synchronous shell, the
// four must/waiting/changed/relationship sections, suggested topics, the
// persisted changed window and the invalidation contract shared with the
// ranked lists.
func TestBriefScreen(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const SCREEN_IDS = ["hub", "brief", "mail", "companies", "ask", "labels", "answers", "learning", "sources", "settings", "logs"]`,
		`brief: 15000,`,
		`const BRIEF_SINCE_KEY = "mailsense.brief.since"`,
		`function briefSinceValue()`,
		`localStorage.getItem(BRIEF_SINCE_KEY)`,
		`localStorage.setItem(BRIEF_SINCE_KEY, normalized)`,
		`function setBriefSince(value)`,
		`function briefPath(since)`,
		`return value ? "/api/brief?since=" + encodeURIComponent(value) : "/api/brief";`,
		`function loadBrief(since)`,
		`requestCached(briefPath(since || briefSinceValue()), {}, CACHE_TTL.brief)`,
		`function renderBrief(route, seq)`,
		`case "brief": node = renderBrief(route, seq); break;`,
		`if (name === "brief")`,
		`function briefSectionNode(`,
		`function briefSectionBodyNode(`,
		`function lockedScreen(learning, feature, title)`,
		`title || t("mail.locked_title"`,
		`lockedScreen(state.learning || {}, "", t("brief.locked_title"))`,
		`"brief-must"`,
		`"brief-waiting"`,
		`"brief-changed"`,
		`"brief-relationships"`,
		`"brief-topics"`,
		`function briefStatsNode(`,
		`href: "#" + stat.target`,
		`function markBriefSeen()`,
		`t("brief.mark_seen")`,
		`t("brief.mark_seen_aria")`,
		`invalidateCache(["/api/brief"])`,
		`invalidateRouteCache("brief")`,
		`error.code === "learning_locked"`,
		`function briefGateGuard()`,
		`t("brief.gate_unknown_title")`,
		`t("brief.topics.suggested")`,
		`function briefTopicScopeLabel(`,
		`function briefTopicSubLabel(`,
		`t("brief.topics.coverage"`,
		`threadStateLabel(source.state)`,
		`function briefDirectionLabel(`,
		`function briefKindLabel(`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the brief step %q", required)
		}
	}
	// Brief reuses the existing conversation and message labels instead of
	// maintaining a second wording for the same counts.
	briefStart := strings.Index(js, "// brief-view:start")
	briefEnd := strings.Index(js, "// brief-view:end")
	if briefStart < 0 || briefEnd <= briefStart {
		t.Fatal("the brief-view markers are missing")
	}
	for _, required := range []string{
		`t("mail.conversations.count"`,
		`t("detail.thread.message_count"`,
		`t("mail.conversations.due"`,
		`t("brief.org_open_aria"`,
	} {
		if !strings.Contains(js[briefStart:briefEnd], required) {
			t.Errorf("the brief view does not reuse %q", required)
		}
	}
	guardStart := strings.Index(js, "function briefGateGuard()")
	renderStart := strings.Index(js, "function renderBrief(route, seq)")
	if guardStart < 0 || renderStart < 0 || guardStart > renderStart {
		t.Fatal("the Brief gate guard or renderBrief was not found")
	}
	renderEnd := strings.Index(js[renderStart:], "\n  // ---------------------------------------------------------------- ask")
	if renderEnd <= 0 {
		t.Fatal("the end of renderBrief was not found")
	}
	guardBody := js[guardStart:renderStart]
	renderBody := js[renderStart : renderStart+renderEnd]
	lockedAt := strings.Index(guardBody, `lockedScreen(state.learning`)
	unknownAt := strings.Index(guardBody, "if (!gateKnown())")
	readAt := strings.Index(renderBody, "loadBrief(since)")
	guardAt := strings.Index(renderBody, "briefGateGuard()")
	if lockedAt < 0 || unknownAt < 0 || readAt < 0 || guardAt < 0 {
		t.Fatalf("the Brief lacks its gate guard or its data read")
	}
	if guardAt > readAt {
		t.Error("renderBrief reads the API before checking the gate")
	}
	// The Brief shares every write invalidation with the ranked lists: one
	// topic owns the list, and the write paths call it.
	listStart := strings.Index(js, "function invalidateLearningReads(")
	if listStart < 0 {
		t.Fatal("invalidateLearningReads not found")
	}
	listEnd := strings.Index(js[listStart:], "\n  }")
	if listEnd <= 0 {
		t.Fatal("the end of invalidateLearningReads was not found")
	}
	listBody := js[listStart : listStart+listEnd]
	if !strings.Contains(listBody, `invalidateTopics(["learning"]`) {
		t.Error("the shared read invalidation bypasses the topic graph")
	}
	learningReads := topicBody(t, js, "learning")
	for _, want := range []string{"/api/brief", "/api/commitments", "/api/companies", "/api/priority", "/api/labels", "/api/answers", "/api/triage"} {
		if !strings.Contains(learningReads, want) {
			t.Errorf("the shared invalidation list lacks %s", want)
		}
	}
	// Two shared read-invalidation owners exist: a learning-derived write
	// refreshes only the learning reads, every other write refreshes them
	// all. Both are topics, not per-save call sites, and the write paths route
	// through one of them instead of spelling their own prefixes.
	sharedOwners := []string{"invalidateLearningReads(", "invalidateAllReads(", "invalidateLearningDerived(", "invalidateAllData("}
	sharedCalls := 0
	for _, name := range sharedOwners {
		for at := 0; ; {
			i := strings.Index(js[at:], name)
			if i < 0 {
				break
			}
			at += i
			lineStart := strings.LastIndexByte(js[:at], '\n') + 1
			if !strings.Contains(js[lineStart:at], "function ") {
				sharedCalls++
			}
			at += len(name)
		}
	}
	if sharedCalls < 6 {
		t.Errorf("%d write paths use a shared read invalidation, want at least 6", sharedCalls)
	}
	for name, call := range map[string]string{
		"afterBackgroundAction":    "invalidateTopics(",
		"afterLabelWrite":          `invalidateTopics(["labels"]`,
		"invalidateProviderChange": "invalidateAllData()",
		"savePreview":              `afterBackgroundAction(["preview"]`,
	} {
		start := strings.Index(js, "function "+name+"(")
		if start < 0 {
			t.Fatalf("%s not found", name)
		}
		end := strings.Index(js[start:], "\n  }")
		if end <= 0 || !strings.Contains(js[start:start+end], call) {
			t.Errorf("%s does not use %s", name, call)
		}
	}
	// The write path drops the derived routes; a background data event only
	// marks them stale so the mounted snapshot keeps its scroll and focus and
	// revalidates after the next mount.
	derivedStart := strings.Index(js, "function invalidateLearningDerived(")
	if derivedStart < 0 {
		t.Fatal("invalidateLearningDerived not found")
	}
	derivedEnd := strings.Index(js[derivedStart:], "\n  }")
	if derivedEnd <= 0 || !strings.Contains(js[derivedStart:derivedStart+derivedEnd], `invalidateTopics(["learning"], { hard: true })`) {
		t.Error("the shared write invalidation does not drop the derived routes")
	}
	afterStart := strings.Index(js, "function afterLearningDataChanged(")
	if afterStart < 0 {
		t.Fatal("afterLearningDataChanged not found")
	}
	afterEnd := strings.Index(js[afterStart:], "\n  }")
	if afterEnd <= 0 || !strings.Contains(js[afterStart:afterStart+afterEnd], `invalidateTopics(["learning"])`) {
		t.Error("a background data event drops the derived routes instead of staling them")
	}
	if strings.Contains(js, `if (name === "brief") return { name: "hub"`) {
		t.Fatal("the Brief route falls back to Home")
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"nav.brief", "screen.brief.title", "screen.brief.eyebrow", "brief.heading",
		"brief.generated", "brief.generated_unknown", "brief.loading", "brief.stats_aria",
		"brief.stat_aria", "brief.stat.must", "brief.stat.waiting", "brief.stat.changed",
		"brief.stat.relationships", "brief.section.must.title", "brief.section.must.sub",
		"brief.section.waiting.title", "brief.section.waiting.sub",
		"brief.section.changed.title", "brief.section.changed.sub",
		"brief.section.changed.sub_window", "brief.section.relationships.title",
		"brief.section.relationships.sub", "brief.section.topics.title",
		"brief.section.count_aria", "brief.empty.must", "brief.empty.waiting",
		"brief.empty.changed", "brief.empty.relationships",
		"brief.kind.due", "brief.kind.reply", "brief.kind.decision", "brief.kind.commitment",
		"brief.kind.activity", "brief.kind.organization", "brief.kind.topic", "brief.kind.other",
		"brief.status.open", "brief.status.overdue", "brief.status.overdue_due",
		"brief.direction.owner", "brief.direction.other", "brief.evidence",
		"brief.evidence_aria", "brief.open_aria", "brief.org_open_aria",
		"brief.organization_unknown", "brief.mark_seen", "brief.mark_seen_aria",
		"brief.mark_seen_failed", "brief.error.title", "brief.error.body",
		"brief.locked_title",
		"brief.relationships.open_owner", "brief.relationships.open_other",
		"brief.topics.suggested",
		"brief.topics.scope.owner_mailbox", "brief.topics.scope_generic", "brief.topics.coverage",
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	for _, key := range []string{
		"brief.relationships.open_owner", "brief.relationships.open_other",
	} {
		if !en[key].isPlural || !fr[key].isPlural {
			t.Errorf("%q must be a plural entry", key)
			continue
		}
		for _, form := range []string{"one", "other"} {
			if !strings.Contains(en[key].plural[form], "{count}") ||
				!strings.Contains(fr[key].plural[form], "{count}") {
				t.Errorf("%s.%s must carry the count placeholder", key, form)
			}
		}
	}
	if got := en["nav.brief"].text; got != "Brief" {
		t.Errorf("English Brief nav = %q", got)
	}
	if got := fr["nav.brief"].text; got != "Synthèse" {
		t.Errorf("French Brief nav = %q", got)
	}
	if got := en["brief.status.overdue"].text; got != "Overdue" {
		t.Errorf("English overdue label = %q", got)
	}
	if got := fr["brief.status.overdue"].text; got != "En retard" {
		t.Errorf("French overdue label = %q", got)
	}
	if got := en["brief.topics.scope.owner_mailbox"].text; got != "Whole mailbox" {
		t.Errorf("English topic scope = %q", got)
	}
	if got := fr["brief.topics.scope.owner_mailbox"].text; got != "Toute la boîte" {
		t.Errorf("French topic scope = %q", got)
	}
	if got := en["brief.topics.coverage"].text; got != "{classified} of {total} messages classified" {
		t.Errorf("English topic coverage = %q", got)
	}
	if got := fr["brief.direction.other"].text; got != "en attente de leur réponse" {
		t.Errorf("French waiting direction = %q", got)
	}
	for key, entry := range en {
		if !strings.HasPrefix(key, "brief.") && key != "nav.brief" &&
			!strings.HasPrefix(key, "screen.brief.") {
			continue
		}
		for _, value := range entryStrings(entry) {
			for _, raw := range []string{
				"awaiting_owner", "awaiting_other", "answered_by_owner",
				"answered_by_other", "no_action", "inbound_last",
				"owner_mailbox", "c:",
			} {
				if strings.Contains(value, raw) {
					t.Errorf("%s leaks the raw value %q", key, raw)
				}
			}
		}
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".brief-screen", ".brief-head", ".brief-stats", ".brief-grid",
		".brief-section-must", ".brief-row", ".brief-row-title", ".brief-row-side",
		".brief-evidence", ".brief-changed-row", ".brief-rel-row", ".brief-topics",
		".brief-topic", ".brief-empty", ".brief-skeleton", ".brief-error",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}

	runBriefBehavior(t)
}

// runBriefBehavior renders the Brief section helpers under Node with the real
// catalogs and a fixture shaped like GET /api/brief. Friendly labels, links,
// counts and empty sections are data contracts, not visual details.
func runBriefBehavior(t *testing.T) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: source and catalog checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "brief_behavior_test.js")
	if err := os.WriteFile(script, []byte(briefBehaviorHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "UI_DIR="+dir, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("brief behavior timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("brief behavior failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "brief behavior: ok") {
		t.Fatalf("brief behavior produced no success marker:\n%s", out)
	}
}

const briefBehaviorHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.formatNumber = (value) => String(value);
sandbox.formatDate = () => "Oct 27, 2026";
sandbox.formatRelative = () => "2 days ago";
sandbox.formatDay = (value) => String(value);
sandbox.humanCode = (code) => code ? String(code).replace(/_/g, " ") : "";
sandbox.mailHref = (id, mode = "important") => "#/mail?mode=" + mode + "&id=" + String(id);
sandbox.routeHash = (name, params) => {
  const query = new URLSearchParams(params || {}).toString();
  return "#/" + name + (query ? "?" + query : "");
};

function h(tag, attrs, ...children) {
  const node = { tag, attrs: attrs || {}, children: [], text: "" };
  const add = (child) => {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push({ tag: "#text", text: String(child), attrs: {}, children: [] });
  };
  node.append = (...kids) => { kids.forEach(add); return node; };
  children.forEach(add);
  return node;
}
sandbox.h = h;
sandbox.text = (value) => ({ tag: "#text", text: String(value), attrs: {}, children: [] });
sandbox.badge = (value, kind = "", title = "") => ({
  tag: "span", attrs: { class: "badge " + kind, title }, text: String(value), children: [],
});
sandbox.pill = (value, kind = "") => ({
  tag: "span", attrs: { class: "pill " + kind }, text: String(value), children: [],
});

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const ts = app.indexOf("// thread-state:start"), te = app.indexOf("// thread-state:end");
assert.ok(ts >= 0 && te > ts, "thread-state markers are missing"); vm.runInContext(app.slice(ts, te), sandbox, { filename: "app.js#thread-state" });
const ss = app.indexOf("// stat-strip:start"), se = app.indexOf("// stat-strip:end");
assert.ok(ss >= 0 && se > ss, "stat-strip markers are missing"); vm.runInContext(app.slice(ss, se), sandbox, { filename: "app.js#stat-strip" });
const conversationStart = app.indexOf("// conversations-view:start");
const conversationEnd = app.indexOf("// conversations-view:end");
assert.ok(conversationStart >= 0 && conversationEnd > conversationStart, "conversations-view markers are missing");
vm.runInContext(app.slice(conversationStart, conversationEnd), sandbox, { filename: "app.js#conversations-view" });

const briefStart = app.indexOf("// brief-view:start");
const briefEnd = app.indexOf("// brief-view:end");
assert.ok(briefStart >= 0 && briefEnd > briefStart, "brief-view markers are missing");
vm.runInContext(app.slice(briefStart, briefEnd), sandbox, { filename: "app.js#brief-view" });

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function links(node) {
  return all(node, (entry) => entry.tag === "a");
}

const view = {
  generated_at: "2026-10-27T08:00:00Z",
  must: { count: 1, items: [{
    id: "due:1:42", kind: "due", title: "Re: Copilot", subject: "Re: Copilot",
    organization: { key: "c:atelier nord", label: "Atelier Nord" },
    person: "Alice Martin", due: "2026-10-30T09:00:00Z", status: "overdue",
    direction: "owner", conversation_id: "C1", message_id: 42, evidence: [42],
    last_at: "2026-10-29T09:00:00Z", state: "", state_reason: "",
    conversations: 0, messages: 0, awaiting_owner: 0, awaiting_other: 0,
    count: 0, suggested: false, action: "pay", waiting: "owner",
    topic: "project_work", subtopic: "approval",
  }]},
  waiting: { count: 1, items: [{
    id: "reply:1:43", kind: "reply", title: "Budget review", subject: "Budget review",
    organization: { key: "c:atelier nord", label: "Atelier Nord" }, person: "Bob",
    status: "open", direction: "other", message_id: 43, evidence: [43],
    last_at: "2026-10-28T09:00:00Z", state: "", state_reason: "",
    conversations: 0, messages: 0, awaiting_owner: 0, awaiting_other: 0,
    count: 0, suggested: false,
  }]},
  changed: { count: 1, since: "2026-10-20T08:00:00Z", items: [{
    id: "activity:C2", kind: "activity", title: "Contract", subject: "Contract",
    organization: { key: "c:atelier nord", label: "Atelier Nord" }, person: "Alice Martin",
    conversation_id: "C2", message_id: 44, evidence: [44],
    last_at: "2026-10-25T07:00:00Z", state: "awaiting_owner",
    state_reason: "inbound_last", count: 0, suggested: false,
  }]},
  relationships: { count: 1, items: [{
    id: "org:c:atelier nord", kind: "organization", title: "Atelier Nord",
    organization: { key: "c:atelier nord", label: "Atelier Nord" },
    conversations: 215, messages: 986, awaiting_owner: 12, awaiting_other: 12,
    count: 215, last_at: "2026-10-26T09:00:00Z", message_id: 42, evidence: [42],
    state: "", state_reason: "", suggested: false,
  }]},
  topics: { count: 87, total: 120, suggested: true, scope: "owner_mailbox", items: [{
    id: "topic:project_work", kind: "topic", title: "project_work", count: 87, suggested: true,
    evidence: [1, 2, 3], state: "", state_reason: "",
    subtopics: [
      { id: "subtopic:project_work/status_update", kind: "subtopic", title: "status_update",
        topic: "project_work", subtopic: "status_update", count: 60, evidence: [1, 2], suggested: true },
      { id: "subtopic:project_work/approval", kind: "subtopic", title: "approval",
        topic: "project_work", subtopic: "approval", count: 27, evidence: [3], suggested: true },
    ],
  }]},
};

const stats = sandbox.briefStatValues(view);
assert.equal(stats.map((stat) => stat.count).join(","), "1,1,1,1");
assert.equal(stats.map((stat) => stat.label).join(","), "Must,Waiting,Changed,Relationships");
const statsNode = sandbox.briefStatsNode(view, () => {});
const statsText = texts(statsNode).join(" | ");
for (const label of ["Must", "Waiting", "Changed", "Relationships"]) {
  assert.ok(statsText.includes(label), "missing stat label: " + label);
}
assert.equal(
  links(statsNode).map((entry) => entry.attrs.href).join("|"),
  "#brief-must|#brief-waiting|#brief-changed|#brief-relationships");

const mustBody = sandbox.briefSectionBodyNode("must", view.must);
const mustText = texts(mustBody).join(" | ");
assert.ok(mustText.includes("Re: Copilot"), "must title missing");
assert.ok(mustText.includes("Atelier Nord"), "must organization missing");
assert.ok(mustText.includes("Alice Martin"), "must person missing");
assert.ok(mustText.includes("Overdue"), "must status missing");
assert.ok(mustText.includes("Due"), "friendly kind label missing");
assert.ok(mustText.includes("Pay"), "action badge missing");
assert.ok(mustText.includes("Project work › Approval"), "subject path missing");
assert.ok(!mustText.includes("c:atelier nord"), "raw organization key reached must");
assert.ok(!mustText.includes("due:1:42"), "raw item id reached must");
assert.ok(!mustText.includes("awaiting_owner"), "raw state reached must");
const mustLinks = links(mustBody);
assert.ok(mustLinks.some((entry) => entry.attrs.href === "#/mail?mode=important&id=42"), "must message link missing");
assert.ok(mustLinks.some((entry) => entry.attrs.href === "#/companies?key=c%3Aatelier+nord"), "must company link missing");
assert.ok(mustLinks.some((entry) => String(entry.attrs.class).includes("citation-chip")), "must evidence chip missing");
const overdue = all(mustBody, (entry) => entry.tag === "span" && String(entry.text).startsWith("Overdue"))[0];
assert.ok(overdue && String(overdue.attrs.class).includes("danger"), "overdue must be red");

const waitingBody = sandbox.briefSectionBodyNode("waiting", view.waiting);
const waitingText = texts(waitingBody).join(" | ");
assert.ok(waitingText.includes("Budget review"), "waiting subject missing");
assert.ok(waitingText.includes("Reply"), "waiting kind missing");
assert.ok(waitingText.includes("waiting on them"), "waiting direction missing");
assert.ok(!waitingText.includes("awaiting_other"), "raw waiting direction reached the screen");
assert.ok(links(waitingBody).some((entry) => entry.attrs.href === "#/mail?mode=important&id=43"), "waiting message link missing");

const changedBody = sandbox.briefSectionBodyNode("changed", view.changed);
const changedText = texts(changedBody).join(" | ");
assert.ok(changedText.includes("Contract"), "changed subject missing");
assert.ok(changedText.includes("Waiting on you"), "changed friendly state missing");
assert.ok(changedText.includes("Atelier Nord"), "changed organization missing");
assert.ok(changedText.includes("Alice Martin"), "changed person missing");
assert.ok(changedText.includes("2 days ago"), "changed relative time missing");
assert.ok(!changedText.includes("awaiting_owner"), "raw changed state reached the screen");
assert.ok(!changedText.includes("inbound_last"), "raw state reason reached the screen");
assert.ok(links(changedBody).some((entry) => entry.attrs.href === "#/mail?mode=important&id=44"), "changed message link missing");

const relBody = sandbox.briefSectionBodyNode("relationships", view.relationships);
const relText = texts(relBody).join(" | ");
assert.ok(relText.includes("Atelier Nord"), "relationship organization missing");
assert.ok(relText.includes("215 conversations"), "relationship conversation count missing");
assert.ok(relText.includes("986 messages"), "relationship message count missing");
assert.ok(relText.includes("12 conversations waiting on you"), "relationship open count missing");
assert.ok(links(relBody).some((entry) => entry.attrs.href === "#/companies?key=c%3Aatelier+nord"), "relationship company link missing");
const openBadge = all(relBody, (entry) => entry.tag === "span" && String(entry.text).includes("waiting on you"))[0];
assert.ok(openBadge && String(openBadge.attrs.class).includes("accent"), "open count is not accented");

const topicsBody = sandbox.briefSectionBodyNode("topics", view.topics);
const topicsText = texts(topicsBody).join(" | ");
assert.ok(topicsText.includes("Project work"), "topic label missing");
assert.ok(topicsText.includes("87 messages"), "topic count missing");
assert.ok(topicsText.includes("Status update"), "subtopic label missing");
assert.ok(topicsText.includes("60"), "subtopic count missing");
assert.equal(sandbox.briefSectionBodyNode("topics", { count: 0, items: [] }), null);
assert.equal(sandbox.briefTopicScopeLabel("owner_mailbox"), "Whole mailbox");
assert.equal(sandbox.briefTopicScopeLabel("mystery_scope"), "From the mail in scope");
assert.ok(!sandbox.briefTopicScopeLabel("mystery_scope").includes("mystery_scope"));
assert.equal(
  sandbox.briefTopicSubLabel({ scope: "owner_mailbox", count: 87, total: 120 }),
  "Whole mailbox · 87 of 120 messages classified");
assert.equal(
  sandbox.briefTopicSubLabel({ scope: "owner_mailbox", count: 87, total: 0 }),
  "Whole mailbox");

for (const kind of ["must", "waiting", "changed", "relationships"]) {
  const empty = texts(sandbox.briefSectionBodyNode(kind, { count: 0, items: [] })).join(" ");
  assert.ok(empty.length > 0, "empty section has no copy: " + kind);
  assert.ok(!empty.includes("brief.empty"), "empty section leaks its catalog key: " + kind);
}
assert.ok(texts(sandbox.briefSectionBodyNode("must", { count: 0, items: [] })).join(" ").includes("Nothing must be done right now."));
assert.equal(sandbox.briefSectionCount({ count: 0, items: [] }), 0);
assert.equal(sandbox.briefSectionCount({ count: 12, items: [{}] }), 12);
assert.equal(sandbox.briefSectionCount({ items: [{}, {}] }), 2);
assert.equal(sandbox.briefKindLabel("due"), "Due");
assert.equal(sandbox.briefKindLabel("action"), "Action");
assert.equal(sandbox.briefKindLabel("commitment"), "Commitment");
assert.equal(sandbox.briefKindLabel("bogus_kind"), "Item");
assert.equal(sandbox.briefDirectionLabel("owner"), "waiting on you");
assert.equal(sandbox.briefDirectionLabel("other"), "waiting on them");

const overdueNode = sandbox.briefStatusNode({ status: "overdue", due: "2026-10-30T09:00:00Z" });
assert.ok(String(overdueNode.text).startsWith("Overdue"), "overdue badge text missing");
assert.ok(String(overdueNode.attrs.class).includes("danger"), "overdue badge is not red");

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
const frMustText = texts(sandbox.briefSectionBodyNode("must", view.must)).join(" | ");
assert.ok(frMustText.includes("Atelier Nord"), "French must organization missing");
assert.ok(frMustText.includes("En retard"), "French overdue label missing");
assert.ok(frMustText.includes("Échéance"), "French kind label missing");
assert.ok(frMustText.includes("Preuves"), "French evidence label missing");
assert.ok(!frMustText.includes("Overdue"), "English status leaked into French");
assert.ok(!frMustText.includes("awaiting_owner"), "raw state leaked into French");
assert.equal(sandbox.briefTopicScopeLabel("owner_mailbox"), "Toute la boîte");
assert.equal(
  sandbox.briefTopicSubLabel({ scope: "owner_mailbox", count: 87, total: 120 }),
  "Toute la boîte · 87 sur 120 messages classés");
const frTopicsText = texts(sandbox.briefSectionBodyNode("topics", view.topics)).join(" | ");
assert.ok(frTopicsText.includes("Travail de projet"), "French topic label missing");
assert.equal(sandbox.briefDirectionLabel("other"), "en attente de leur réponse");
const frWaitingText = texts(sandbox.briefSectionBodyNode("waiting", view.waiting)).join(" | ");
assert.ok(frWaitingText.includes("en attente de leur réponse"), "French waiting direction missing");
assert.ok(!frWaitingText.includes("waiting on them"), "English direction leaked into French");

console.log("brief behavior: ok");
`

// The mail detail leads with plain-language facets. Raw question names,
// percentages and model level numbers belong to the collapsed Model details.
func TestMailDetailTriageSummary(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function triageSummaryNode(`,
		`function triageAboutFacet(`,
		`function triageAsksFacet(`,
		`function triageWhenFacet(`,
		`function triageFromFacet(`,
		`function triageAttentionFacet(`,
		`function triageEnoughFacet(`,
		`function triageEffortFacet(`,
		`function triageModelDetails(`,
		`function advancedAnswerValue(`,
		`function triageListValue(`,
		`function triageQuestionNames(`,
		`triageListValue(firstQuestion, result.first_value)`,
		`questionTitle(first)`,
		`t("detail.summary.title")`,
		`t("detail.summary.about")`,
		`t("detail.summary.asks")`,
		`t("detail.summary.reply")`,
		`t("detail.summary.no_reply")`,
		`t("detail.summary.when")`,
		`t("detail.summary.from")`,
		`t("detail.summary.attention")`,
		`t("detail.summary.enough")`,
		`t("detail.summary.effort")`,
		`t("detail.summary.suspicious")`,
		`t("detail.summary.sufficient")`,
		`t("detail.summary.insufficient")`,
		`t("detail.summary.uncertain")`,
		`t("detail.summary.uncertain_suffix")`,
		`t("detail.summary.model_details")`,
		`t("detail.summary.uncertain_list"`,
		`t("detail.summary.missing_list"`,
		`answerValueText(question, reading.choice)`,
		`questionTitle(name)`,
		`triageQuestionNames(triage.uncertain)`,
		`triageQuestionNames(triage.missing)`,
		`h("details", { class: "triage-model-details" }`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the triage summary step %q", required)
		}
	}
	for _, forbidden := range []string{
		`function triageAnswerChips(`,
		`answer-chip-q`,
		`triage.uncertain.join(", ")`,
		`triage.missing.join(", ")`,
		`t("mail.detail.stored")`,
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js still exposes the raw signal rendering %q", forbidden)
		}
	}

	// The facet renderers carry no confidence percentage or question title;
	// those stay in the advanced formatter and the collapsed disclosure.
	start := strings.Index(js, "  function triageNoulReading(")
	end := strings.Index(js, "  function triageLevelValue(")
	if start < 0 || end <= start {
		t.Fatal("the triage facet renderers were not found")
	}
	main := js[start:end]
	for _, forbidden := range []string{"formatPercent(", "questionTitle(", "text: name"} {
		if strings.Contains(main, forbidden) {
			t.Errorf("the main triage summary still renders %q", forbidden)
		}
	}
	advStart := strings.Index(js, "  function advancedAnswerValue(")
	advEnd := strings.Index(js, "  function triageQuestionNames(")
	if advStart < 0 || advEnd <= advStart {
		t.Fatal("the advanced answer formatter was not found")
	}
	advanced := js[advStart:advEnd]
	for _, required := range []string{"formatPercent(", "questionTitle(name)", "advancedAnswerValue(q, value)", `t("detail.summary.model_details")`} {
		if !strings.Contains(advanced, required) {
			t.Errorf("the Model details disclosure lacks %q", required)
		}
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"detail.summary.title", "detail.summary.about", "detail.summary.asks", "detail.summary.reply",
		"detail.summary.no_reply", "detail.summary.reply_unclear", "detail.summary.when",
		"detail.summary.from", "detail.summary.attention", "detail.summary.enough",
		"detail.summary.effort", "detail.summary.normal", "detail.summary.likely",
		"detail.summary.uncertain", "detail.summary.uncertain_suffix", "detail.summary.model_details",
		"detail.summary.suspicious", "detail.summary.sufficient", "detail.summary.insufficient",
		"detail.summary.uncertain_list", "detail.summary.missing_list",
	}
	for _, category := range []string{"security", "spam", "finance", "notifications", "marketing", "action", "work"} {
		keys = append(keys, "detail.summary.category."+category)
	}
	for _, topic := range []string{
		"project_work", "support_issue", "sales_inquiry", "billing_account", "meeting_scheduling",
		"technical_integration", "hiring", "partnership", "internal_ops", "news_updates", "other",
	} {
		keys = append(keys, "detail.summary.topic."+topic)
	}
	for _, ask := range []string{
		"answer_question", "approval_decision", "information_document", "payment_money",
		"meeting_availability", "task_work", "nothing_fyi", "other",
	} {
		keys = append(keys, "detail.summary.asks_for."+ask)
	}
	for _, scale := range []struct {
		name string
		max  int
	}{
		{"urgency", 4}, {"deadline", 5}, {"effort", 3}, {"sentiment", 4}, {"request_count", 3},
	} {
		for level := 0; level <= scale.max; level++ {
			keys = append(keys, "detail.summary."+scale.name+"."+strconv.Itoa(level))
		}
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	if got := en["detail.summary.asks_for.nothing_fyi"].text; got != "Just an update. Nothing to do" {
		t.Errorf("English FYI sentence = %q", got)
	}
	if got := en["detail.summary.no_reply"].text; got != "No reply needed" {
		t.Errorf("English no-reply sentence = %q", got)
	}
	for level, want := range []string{"No date", "Months away", "Within a month", "This week", "Within 24 hours", "Overdue"} {
		key := "detail.summary.deadline." + strconv.Itoa(level)
		if got := en[key].text; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".triage-summary", ".triage-facets", ".triage-facet", ".triage-model-details"} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
}

// TestMailDetailConversation checks the compact conversation card: the
// thread API is cached, the section is painted after the message body, the
// state and participants are friendly, earlier messages exclude the open
// message, and a missing thread never becomes an error toast.
func TestMailDetailConversation(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`thread: 15000,`,
		`function loadThread(conversationID, before = 0, at = 0)`,
		`query.push("before=" + encodeURIComponent(String(before)))`,
		`query.push("at=" + encodeURIComponent(String(at)))`,
		`"/api/threads/" + encodeURIComponent(id) + (query.length ? "?" + query.join("&") : "")`,
		`function threadPresentation(`,
		`function mergeThreadPage(`,
		`function threadHostNode(`,
		`function threadSectionNode(`,
		`async function fillThreadSection(`,
		`loadThread(conversationID, 0, Number(currentId) || 0)`,
		`view.newer > 0`,
		`view.newestID`,
		`t("detail.thread.show_latest")`,
		`function hydrateMailThread(`,
		`function selectThreadMessage(`,
		`function syncMailListSelection(`,
		`replaceRoute("mail", params)`,
		`syncMailListSelection(messageId)`,
		`syncMailListSelection(id)`,
		`loadMailDetail(messageId, panel,`,
		`dataset: { messageId: String(id) }`,
		`.mail-list-pane .mail-row[data-message-id="`,
		`.search-result[data-id="`,
		`message.conversation_id`,
		`threadHostNode(message)`,
		`hydrateMailThread(node, id, message, context)`,
		`host.remove()`,
		`host.replaceWith(section)`,
		`t("detail.thread.title")`,
		`t("detail.thread.state."`,
		`t("detail.thread.message_count"`,
		`t("detail.thread.last_activity"`,
		`t("detail.thread.earlier_empty")`,
		`t("detail.thread.truncated")`,
		`t("detail.thread.load_older")`,
		`t("detail.thread.visibility_partial")`,
		`t("detail.thread.sent")`,
		`t("detail.thread.received")`,
		`t("detail.thread.error")`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the conversation step %q", required)
		}
	}

	// The section sits below the message card and before the triage summary,
	// so the reader sees the thread state before "About this email".
	nodeStart := strings.Index(js, "function mailDetailNode(")
	if nodeStart < 0 {
		t.Fatal("mailDetailNode not found")
	}
	nodeEnd := strings.Index(js[nodeStart:], "\n  // ---------------------------------------------------------------- settings")
	if nodeEnd <= 0 {
		t.Fatal("mailDetailNode has no end")
	}
	nodeBody := js[nodeStart : nodeStart+nodeEnd]
	cardAt := strings.Index(nodeBody, "messageCardNode(messageView, message)")
	threadAt := strings.Index(nodeBody, "threadHostNode(message)")
	summaryAt := strings.Index(nodeBody, `t("detail.summary.title")`)
	if cardAt < 0 || threadAt < 0 || summaryAt < 0 || !(cardAt < threadAt && threadAt < summaryAt) {
		t.Fatalf("conversation section order card=%d thread=%d summary=%d", cardAt, threadAt, summaryAt)
	}

	// The thread read starts after the message node is appended and is never
	// awaited, so the body never blocks on it.
	paintStart := strings.Index(js, "function appendMailDetailNode(")
	if paintStart < 0 {
		t.Fatal("appendMailDetailNode not found")
	}
	paintEnd := strings.Index(js[paintStart:], "\n  }")
	if paintEnd <= 0 {
		t.Fatal("appendMailDetailNode has no end")
	}
	paintBody := js[paintStart : paintStart+paintEnd]
	appendAt := strings.Index(paintBody, "panel.append(node)")
	hydrateAt := strings.Index(paintBody, "hydrateMailThread(node, id, message, context)")
	if appendAt < 0 || hydrateAt < 0 || hydrateAt < appendAt {
		t.Fatalf("the thread read does not start after paint: append=%d hydrate=%d", appendAt, hydrateAt)
	}
	if strings.Contains(paintBody, "await hydrateMailThread") {
		t.Fatal("the message body waits on the thread read")
	}

	// A missing thread is silent: no toast and no reportError in the fill.
	viewStart := strings.Index(js, "// thread-view:start")
	viewEnd := strings.Index(js, "// thread-view:end")
	if viewStart < 0 || viewEnd <= viewStart {
		t.Fatal("thread-view markers not found")
	}
	viewBody := js[viewStart:viewEnd]
	for _, forbidden := range []string{"toast(", "reportError("} {
		if strings.Contains(viewBody, forbidden) {
			t.Errorf("the thread view calls %q instead of failing quietly", forbidden)
		}
	}

	// Sync and configuration changes invalidate the thread cache; selecting a
	// message does not. The list itself lives in one topic, so the test pins
	// that owner's contents and that the write paths go through it.
	sharedStart := strings.Index(js, "function invalidateLearningReads(")
	if sharedStart < 0 {
		t.Fatal("invalidateLearningReads not found")
	}
	sharedEnd := strings.Index(js[sharedStart:], "\n  }")
	if sharedEnd <= 0 || !strings.Contains(js[sharedStart:sharedStart+sharedEnd], `invalidateTopics(["learning"]`) {
		t.Error("the shared invalidation path does not go through the topic graph")
	}
	if !strings.Contains(topicBody(t, js, "learning"), `"/api/threads"`) {
		t.Error("the learning topic does not include /api/threads")
	}
	for name, call := range map[string]string{
		"afterBackgroundAction": "invalidateTopics(",
		"afterLabelWrite":       `invalidateTopics(["labels"]`,
	} {
		start := strings.Index(js, "function "+name+"(")
		if start < 0 {
			t.Fatalf("%s not found", name)
		}
		end := strings.Index(js[start:], "\n  }")
		if end <= 0 || !strings.Contains(js[start:start+end], call) {
			t.Errorf("%s does not use %s", name, call)
		}
		if name == "afterLabelWrite" {
			if strings.Contains(js[start:start+end], "staleRouteCache(") || strings.Contains(js[start:start+end], "invalidateRouteCache(") {
				t.Error("a label write still spells its own route invalidation")
			}
			if !strings.Contains(js[start:start+end], "{ hard: true") {
				t.Error("a label write leaves the completed workbench cached")
			}
		}
	}
	configStart := strings.Index(js, `source.addEventListener("config.changed"`)
	if configStart < 0 {
		t.Fatal("config.changed handler not found")
	}
	configEnd := strings.Index(js[configStart:], `source.addEventListener("sidecar.state"`)
	if configEnd <= 0 {
		t.Fatal("the end of the config.changed handler was not found")
	}
	configBody := js[configStart : configStart+configEnd]
	if !strings.Contains(configBody, "invalidateAllReads()") || !strings.Contains(configBody, `invalidateTopics(["route"])`) {
		t.Error("the configuration handler does not invalidate reads and stale the mounted screens")
	}
	if strings.Contains(configBody, "invalidateAllData()") {
		t.Error("a remote configuration change still drops every cached screen")
	}
	selectStart := strings.Index(js, "function selectThreadMessage(")
	if selectStart < 0 {
		t.Fatal("selectThreadMessage not found")
	}
	selectEnd := strings.Index(js[selectStart:], "\n  }")
	if selectEnd <= 0 {
		t.Fatal("selectThreadMessage has no end")
	}
	if strings.Contains(js[selectStart:selectStart+selectEnd], "invalidateCache") {
		t.Error("opening another conversation message invalidates the cache")
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"detail.thread.title", "detail.thread.participants", "detail.thread.more_people",
		"detail.thread.message_count", "detail.thread.last_activity",
		"detail.thread.earlier_title", "detail.thread.earlier_empty",
		"detail.thread.truncated", "detail.thread.load_older",
		"detail.thread.visibility_partial", "detail.thread.sent", "detail.thread.received",
		"detail.thread.error", "detail.thread.loading",
	}
	for _, state := range []string{
		"awaiting_owner", "awaiting_other", "answered_by_owner",
		"answered_by_other", "no_action", "unknown",
	} {
		keys = append(keys, "detail.thread.state."+state)
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	labels := map[string]string{
		"detail.thread.state.awaiting_owner":    "Waiting on you",
		"detail.thread.state.awaiting_other":    "Waiting on them",
		"detail.thread.state.answered_by_owner": "You replied",
		"detail.thread.state.answered_by_other": "A colleague replied",
		"detail.thread.state.no_action":         "No reply needed",
		"detail.thread.state.unknown":           "Check the thread",
	}
	for key, want := range labels {
		if got := en[key].text; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := en["detail.thread.visibility_partial"].text; !strings.Contains(got, "shared mailbox") {
		t.Errorf("the visibility note does not explain the shared mailbox: %q", got)
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".thread-section", ".thread-head", ".thread-title", ".thread-participants",
		".thread-message-row", ".thread-message-sender", ".thread-message-snippet",
		".thread-message-marker", ".thread-message-date", ".thread-more",
		".thread-visibility", ".thread-skeleton",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}

	runThreadBehavior(t)
}

// runThreadBehavior extracts the thread-view block and executes it under
// Node with a fake DOM, fake API and the real catalogs. Node is not a build
// dependency of the desktop app, so environments without it skip.
func runThreadBehavior(t *testing.T) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: source and catalog checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "thread_behavior_test.js")
	if err := os.WriteFile(script, []byte(threadBehaviorHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "UI_DIR="+dir, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("thread behavior timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("thread behavior failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "thread behavior: ok") {
		t.Fatalf("thread behavior produced no success marker:\n%s", out)
	}
}

const threadBehaviorHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
// subjectPathLabel lives in the brief block in the app; the thread block
// only calls it. The harness supplies the same catalog lookup it uses.
sandbox.subjectPathLabel = (topic, subtopic) => {
  const parent = topic ? sandbox.tOr("labels.option.topic." + topic, topic) : "";
  const child = subtopic ? sandbox.tOr("subject." + topic + "." + subtopic, subtopic) : "";
  return child ? (parent ? parent + " › " + child : child) : parent;
};

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const ts = app.indexOf("// thread-state:start"), te = app.indexOf("// thread-state:end");
assert.ok(ts >= 0 && te > ts, "thread-state markers are missing"); vm.runInContext(app.slice(ts, te), sandbox, { filename: "app.js#thread-state" });
const start = app.indexOf("// thread-view:start");
const end = app.indexOf("// thread-view:end");
assert.ok(start >= 0 && end > start, "thread-view markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#thread-view" });

function h(tag, attrs, ...children) {
  const node = { tag, attrs: attrs || {}, children: [], text: "" };
  const add = (child) => {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push({ tag: "#text", text: String(child), attrs: {}, children: [] });
  };
  node.append = (...kids) => { kids.forEach(add); return node; };
  children.forEach(add);
  return node;
}
sandbox.h = h;
sandbox.clear = (node) => { node.children.length = 0; return node; };
sandbox.badge = (value, kind = "", title = "") => ({ tag: "badge", attrs: { class: kind, title }, text: String(value), children: [] });
sandbox.button = (label, onClick, opts = {}) => ({
  tag: "button",
  attrs: { class: ("button " + (opts.kind || "") + " " + (opts.small ? "small" : "")).trim(), disabled: !!opts.disabled },
  text: String(label),
  onClick,
  children: [],
});
sandbox.formatDate = () => "Jan 02, 2026";
sandbox.formatRelative = () => "2 hours ago";
sandbox.skeletonLine = (width) => ({ tag: "div", attrs: { class: "skeleton-line " + width }, text: "", children: [] });
let selectedMessages = [];
sandbox.selectThreadMessage = (id, context) => { selectedMessages.push([id, context && context.mode]); };
let toastCalls = 0;
sandbox.toast = () => { toastCalls += 1; };

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function buttonNamed(node, label) {
  return all(node, (entry) => entry.tag === "button" && entry.attrs && entry.attrs.class === "button quiet small" && entry.text === label)[0] || null;
}
function same(actual, expected, message) {
  assert.equal(JSON.stringify(actual), JSON.stringify(expected), message);
}

const fixture = {
  conversation_id: "C-thread",
  subject: "Latest subject",
  state: {
    state: "awaiting_owner", reason: "inbound_last", actor: "Marie",
    last_at: "2026-10-26T10:00:00Z", last_message_id: 42,
    reply_to_message_id: 42, visibility: "complete", confidence: 0.85,
    topic: "project_work", subtopic: "approval",
  },
  messages: [38, 39, 40, 41, 42].map((id) => ({
    id,
    subject: "Re: Budget review",
    sender_name: "Person " + id,
    sender_address: "person" + id + "@example.com",
    received_at: "2026-10-26T0" + (id % 10) + ":00:00Z",
    sent: id === 41,
    is_read: true,
    has_attachments: false,
    snippet: "Snippet " + id,
  })),
  participants: [
    { name: "Marie", address: "marie@example.com", messages: 2 },
    { name: "Paul", address: "paul@example.com", messages: 1 },
    { name: "Ana", address: "ana@example.com", messages: 1 },
    { name: "Luis", address: "luis@example.com", messages: 1 },
    { name: "Zoe", address: "zoe@example.com", messages: 1 },
  ],
  first_at: "2026-10-20T10:00:00Z",
  last_at: "2026-10-26T10:00:00Z",
  count: 5,
  truncated: true,
};

const view = sandbox.threadPresentation(fixture, 42);
assert.equal(view.stateLabel, "Waiting on you");
assert.equal(view.stateKind, "accent");
// The card and the list share one taxonomy: a thread answered by somebody
// else is "ok" in both, not plain in the card.
assert.equal(sandbox.threadStateKind("answered_by_other"), "ok");
assert.equal(sandbox.threadStateKind("answered_by_owner"), "ok");
same(view.names, ["Marie", "Paul", "Ana"], "participant names are capped at three");
assert.equal(view.morePeople, 2);
assert.equal(view.countLabel, "5 messages");
assert.equal(view.topic, "project_work");
assert.equal(view.subtopic, "approval");
same(view.earlier.map((message) => message.id), [39, 40, 41], "the current message is excluded and the latest three remain");
assert.equal(view.truncated, true);
assert.equal(view.oldestID, 38);
assert.equal(view.newer, 0, "a latest page has no newer note");
assert.ok(!JSON.stringify(view).includes("awaiting_owner"), "raw state enum leaked into the presentation");

const anchored = Object.assign({}, fixture, { newer: 2, newest_message_id: 44 });
const anchoredView = sandbox.threadPresentation(anchored, 42);
assert.equal(anchoredView.newer, 2);
assert.equal(anchoredView.newestID, 44);
const anchoredSection = sandbox.threadSectionNode(anchored, 42, {});
const anchoredText = texts(anchoredSection).join(" | ");
assert.ok(anchoredText.includes("2 newer messages"), "the anchored page discloses newer mail");
assert.ok(anchoredText.includes("Show latest"), "the anchored page can jump to the newest message");

const labels = {
  awaiting_owner: "Waiting on you",
  awaiting_other: "Waiting on them",
  answered_by_owner: "You replied",
  answered_by_other: "A colleague replied",
  no_action: "No reply needed",
  unknown: "Check the thread",
  made_up_state: "Check the thread",
};
for (const [state, label] of Object.entries(labels)) {
  const sample = Object.assign({}, fixture, { state: Object.assign({}, fixture.state, { state }) });
  const sampleView = sandbox.threadPresentation(sample, 42);
  assert.equal(sampleView.stateLabel, label, "state " + state + " has a friendly label");
  assert.ok(!JSON.stringify(sampleView).includes(state), "state " + state + " leaked its raw enum");
}

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(sandbox.threadPresentation(fixture, 42).stateLabel, "En attente de vous", "French state label");
I.setLanguage({ language: "en", formatLocale: "en-US" });

const section = sandbox.threadSectionNode(fixture, 42, { mode: "important", preview: false });
assert.ok(section && section.tag === "section", "the conversation section is painted");
const body = texts(section).join(" | ");
assert.ok(body.includes("Conversation"), "the title is present");
assert.ok(body.includes("Waiting on you"), "the friendly state label is present");
assert.ok(!body.includes("awaiting_owner"), "the main section never renders a raw state");
assert.ok(body.includes("Marie, Paul, Ana +2"), "participants render with the overflow count");
assert.ok(body.includes("5 messages"), "the message count renders");
assert.ok(body.includes("Project work › Approval"), "the subject path renders in the thread header");
assert.ok(body.includes("Last activity 2 hours ago"), "the last activity renders");
assert.ok(body.includes("Older messages not loaded"), "the truncated note renders");
assert.ok(body.includes("Load older"), "the load-older control renders");
const rows = all(section, (entry) => entry.tag === "button" && String(entry.attrs.class || "").startsWith("thread-message-row"));
same(rows.map((row) => row.attrs.dataset.messageId), ["39", "40", "41"], "earlier rows exclude the current message");
assert.ok(rows.every((row) => row.attrs["aria-label"].includes("Re: Budget review")), "rows carry a readable open label");
rows[0].attrs.onclick();
assert.equal(selectedMessages.length, 1, "an earlier row is clickable");
assert.equal(selectedMessages[0][0], 39, "the click targets the earlier message");
assert.equal(selectedMessages[0][1], "important", "the click keeps the mail mode");

const partial = Object.assign({}, fixture, {
  state: Object.assign({}, fixture.state, { state: "answered_by_other", visibility: "partial" }),
});
const partialText = texts(sandbox.threadSectionNode(partial, 42, {})).join(" | ");
assert.ok(partialText.includes("A colleague replied"), "answered_by_other is friendly");
assert.ok(!partialText.includes("answered_by_other"), "answered_by_other is never raw");
assert.ok(partialText.includes("Some replies are in a shared mailbox; only the state is shown."), "the partial visibility note renders");

const one = Object.assign({}, fixture, {
  messages: [fixture.messages[4]],
  participants: [{ name: "Marie", address: "marie@example.com", messages: 1 }],
  count: 1,
  truncated: false,
});
const oneSection = sandbox.threadSectionNode(one, 42, {});
const oneText = texts(oneSection).join(" | ");
assert.ok(oneText.includes("Conversation") && oneText.includes("Waiting on you"), "a one-message thread keeps the header and state");
assert.equal(all(oneSection, (entry) => entry.tag === "button" && String(entry.attrs.class || "").startsWith("thread-message-row")).length, 0, "a one-message thread has no earlier list");
assert.ok(!oneText.includes("No earlier messages are visible."), "a one-message thread has no empty-earlier line");
assert.ok(!oneText.includes("Load older"), "a complete thread has no load-older control");

const gap = Object.assign({}, fixture, { messages: [fixture.messages[4]], count: 3, truncated: false });
const gapText = texts(sandbox.threadSectionNode(gap, 42, {})).join(" | ");
assert.ok(gapText.includes("No earlier messages are visible."), "a gap with no visible earlier messages gets the fallback line");

assert.equal(sandbox.threadHostNode({ subject: "No conversation" }), null, "a message without conversation_id has no section");
const hostNode = sandbox.threadHostNode({ conversation_id: "C-thread" });
assert.equal(hostNode.attrs.dataset.conversationId, "C-thread", "the skeleton names its conversation");

(async () => {
  let olderCalls = 0;
  sandbox.loadThread = async (conversationID, before) => {
    olderCalls += 1;
    assert.equal(conversationID, "C-thread");
    assert.equal(before, 38, "the oldest returned id is the cursor");
    return {
      conversation_id: "C-thread",
      messages: [
        { id: 36, sender_name: "Older 36", subject: "Re: Budget review", received_at: "2026-10-24T10:00:00Z", snippet: "Snippet 36" },
        { id: 37, sender_name: "Older 37", subject: "Re: Budget review", received_at: "2026-10-25T10:00:00Z", snippet: "Snippet 37" },
        { id: 38, sender_name: "Older 38", subject: "Re: Budget review", received_at: "2026-10-25T11:00:00Z", snippet: "Snippet 38" },
      ],
      participants: fixture.participants,
      count: 5,
      truncated: true,
    };
  };
  const paged = sandbox.threadSectionNode(fixture, 42, {});
  const loadOlder = buttonNamed(paged, "Load older");
  assert.ok(loadOlder, "load older is a button");
  await loadOlder.onClick();
  assert.equal(olderCalls, 1);
  const pagedRows = all(paged, (entry) => entry.tag === "button" && String(entry.attrs.class || "").startsWith("thread-message-row"));
  same(pagedRows.map((row) => row.attrs.dataset.messageId), ["36", "37", "38", "39", "40", "41"], "older messages are prepended in chronological order and deduplicated");
  assert.ok(buttonNamed(paged, "Load older"), "a still-truncated history keeps the control");

  sandbox.loadThread = async () => ({
    conversation_id: "C-thread",
    messages: [{ id: 37, sender_name: "Older 37", subject: "Re: Budget review", received_at: "2026-10-25T10:00:00Z", snippet: "Snippet 37" }],
    count: 5,
    truncated: false,
  });
  const gone = sandbox.threadSectionNode(fixture, 42, {});
  await buttonNamed(gone, "Load older").onClick();
  assert.equal(buttonNamed(gone, "Load older"), null, "a complete history removes the control");

  sandbox.loadThread = async () => {
    const error = new Error("unknown thread");
    error.status = 404;
    error.code = "unknown_thread";
    throw error;
  };
  const host = {
    isConnected: true, removed: false, replaced: null,
    remove() { this.removed = true; this.isConnected = false; },
    replaceWith(node) { this.replaced = node; this.isConnected = false; },
  };
  await sandbox.fillThreadSection(host, "C-thread", 42, {});
  assert.equal(host.removed, true, "a 404 removes the section");
  assert.equal(toastCalls, 0, "a missing thread never raises a toast");

  let anchorArgs = null;
  sandbox.loadThread = async (conversationID, before, at) => {
    anchorArgs = [conversationID, before, at];
    return fixture;
  };
  const okHost = {
    isConnected: true, removed: false, replaced: null,
    remove() { this.removed = true; this.isConnected = false; },
    replaceWith(node) { this.replaced = node; this.isConnected = false; },
  };
  await sandbox.fillThreadSection(okHost, "C-thread", 42, {});
  assert.ok(okHost.replaced && okHost.replaced.tag === "section", "a found thread replaces the skeleton");
  same(anchorArgs, ["C-thread", 0, 42], "the first page is anchored at the opened message");

  const noThread = { querySelector: () => null };
  assert.equal(await sandbox.hydrateMailThread(noThread, 42, { conversation_id: "C-thread" }, {}), undefined, "no host means no request");

  process.stdout.write("thread behavior: ok\n");
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
`

// TestConversationsMailMode checks the thread-first Mail list: routing,
// cache, chips, row rendering, load-more append, invalidation and the
// friendly state/company/participant copy.
func TestConversationsMailMode(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`conversations: 15000,`,
		`const CONVERSATIONS_PAGE_SIZE = 30;`,
		`function loadConversations(limit = CONVERSATIONS_PAGE_SIZE, before = "", state = "", q = "")`,
		`"/api/conversations?"`,
		`function conversationPath(`,
		`function conversationViewFromPage(`,
		`function conversationMergePage(`,
		`function conversationRowNode(`,
		`function renderConversations(route, showPreview)`,
		`mode === "conversations"`,
		`t("mail.tab.conversations")`,
		`next_before`,
		`t("mail.conversations.filter_aria")`,
		`threadStateLabel(`,
		`conversationParams(route, { state: name, id: "" })`,
		`function selectConversationRow(`,
		`replaceRoute("mail", conversationParams(parseRoute(), { id: String(id) }))`,
		`syncMailListSelection(id)`,
		`loadMailDetail(id, panel, { preview: !!(view && view.preview), mode: "conversations", item: null })`,
		`loadConversations(CONVERSATIONS_PAGE_SIZE, currentView.nextBefore, filterState, query)`,
		`list.scrollTop = scroll`,
		`state.conversationPages.get(viewKey) !== currentView`,
		`conversationEmptyCopy(`,
		`conversationErrorCopy(`,
		`dir: "auto"`,
		`model.hasAttachments ? badge(model.attachmentLabel)`,
		`model.dueDate ? badge(model.dueLabel, "warn", model.dueDate)`,
		`conversation-unread`,
		`conversation-traffic`,
		`if (route.name === "mail") params.delete("id")`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the conversations step %q", required)
		}
	}

	// The Conversations tab follows Important and precedes Recent. The
	// segments are route navigation, so the group is a nav.
	tabsStart := strings.Index(js, `const tabs = h("nav", { class: "segmented mail-tabs"`)
	if tabsStart < 0 {
		t.Fatal("the Mail tabs were not found")
	}
	tabsEnd := strings.Index(js[tabsStart:], `const head = h("div", { class: "mail-head"`)
	if tabsEnd <= 0 {
		t.Fatal("the Mail tabs were not found")
	}
	tabs := js[tabsStart : tabsStart+tabsEnd]
	order := []string{
		`t("mail.tab.important")`, `t("mail.tab.conversations")`,
		`t("mail.tab.recent")`,
	}
	previous := -1
	for _, marker := range order {
		at := strings.Index(tabs, marker)
		if at < 0 {
			t.Fatalf("the Mail tabs lack %q", marker)
		}
		if at <= previous {
			t.Fatalf("the Mail tab %q is out of order", marker)
		}
		previous = at
	}
	// One search: the shell box owns the query, so the Mail screen has no
	// second Search tab.
	if strings.Contains(tabs, `t("mail.tab.search")`) {
		t.Fatal("the Mail tabs still contain a second Search entry")
	}

	// The conversation route keeps id, state and q; the cache key still
	// drops only the selected message id.
	paramsStart := strings.Index(js, "function conversationParams(")
	if paramsStart < 0 {
		t.Fatal("conversationParams not found")
	}
	paramsEnd := strings.Index(js[paramsStart:], "\n  }")
	if paramsEnd <= 0 {
		t.Fatal("conversationParams not found")
	}
	paramsBody := js[paramsStart : paramsStart+paramsEnd]
	for _, required := range []string{`current.get("id")`, `current.get("state")`, `current.get("q")`, `Object.assign(params, overrides)`} {
		if !strings.Contains(paramsBody, required) {
			t.Errorf("conversationParams lacks %q", required)
		}
	}

	// The list shell paints before any read: renderConversations contains no
	// await, and its rows use the cached first page immediately.
	renderStart := strings.Index(js, "function renderConversations(")
	if renderStart < 0 {
		t.Fatal("renderConversations not found")
	}
	renderEnd := strings.Index(js[renderStart:], "async function ensureTriageView(")
	if renderEnd <= 0 {
		t.Fatal("renderConversations not found")
	}
	renderBody := js[renderStart : renderStart+renderEnd]
	if strings.Contains(renderBody, "await ") {
		t.Error("renderConversations awaits before painting the Mail shell")
	}
	if !strings.Contains(renderBody, "cached(conversationPath(") {
		t.Error("renderConversations does not read the cached first page")
	}
	if !strings.Contains(renderBody, "paintSkeleton()") || !strings.Contains(renderBody, "return mailWorkspace(pane, detail)") {
		t.Error("renderConversations does not paint the shell before returning")
	}
	if strings.Contains(renderBody, "errorScreen(") {
		t.Error("conversations errors replace the shell instead of staying in the list pane")
	}
	errStart := strings.Index(renderBody, "function paintError(")
	if errStart < 0 {
		t.Fatal("paintError not found")
	}
	errEnd := strings.Index(renderBody[errStart:], "\n    }")
	if errEnd <= 0 {
		t.Fatal("paintError not found")
	}
	errBody := renderBody[errStart : errStart+errEnd]
	for _, required := range []string{`list.append(errorState(error, () => loadView()`, `copy.title`, `copy.body`, `loadView()`} {
		if !strings.Contains(errBody, required) {
			t.Errorf("paintError lacks %q", required)
		}
	}

	// Boot warms the shell and Home only. Triage, the conversation digest,
	// company profiles, Brief, Ask history and message bodies are first-visit
	// reads now, so boot does not ship a hidden screen before it is opened.
	coreStart := strings.Index(js, "function prefetchCore()")
	if coreStart < 0 {
		t.Fatal("prefetchCore not found")
	}
	coreEnd := strings.Index(js[coreStart:], "\n  // ---------------------------------------------------------------- hub")
	if coreEnd <= 0 {
		t.Fatal("prefetchCore not found")
	}
	core := js[coreStart : coreStart+coreEnd]
	for _, want := range []string{
		`loadHub()`,
		`loadPriority("all", PRIORITY_HOME_SIZE)`,
		`loadAgenda(state.agendaHours)`,
	} {
		if !strings.Contains(core, want) {
			t.Errorf("the boot warm lost the primary-screen read %q", want)
		}
	}
	for _, hidden := range []string{
		"loadConversations(30)",
		"loadCompanies()",
		"loadCompany(first.key)",
		"loadBrief()",
		"loadAskHistory()",
		`requestCached("/api/triage`,
		"prefetchPriorityDetails(",
		`scheduleRoutePrefetch("#/mail")`,
	} {
		if strings.Contains(core, hidden) {
			t.Errorf("the boot warm still ships a hidden screen: %q", hidden)
		}
	}

	// The derived first page is dropped with the API cache, and every topic
	// that clears priority clears conversations too.
	cacheStart := strings.Index(js, "function invalidateCache(")
	if cacheStart < 0 {
		t.Fatal("invalidateCache not found")
	}
	cacheEnd := strings.Index(js[cacheStart:], "\n  }")
	if cacheEnd <= 0 {
		t.Fatal("invalidateCache not found")
	}
	cacheBody := js[cacheStart : cacheStart+cacheEnd]
	for _, required := range []string{`state.conversationPages.clear()`, `state.conversationLoads.clear()`} {
		if !strings.Contains(cacheBody, required) {
			t.Errorf("invalidateCache lacks %q", required)
		}
	}
	topicsStart := strings.Index(js, "const INVALIDATION_TOPICS = {")
	topicsEnd := strings.Index(js, "// invalidation-topics:end")
	if topicsStart < 0 || topicsEnd <= topicsStart {
		t.Fatal("the invalidation topic graph was not found")
	}
	topicRows := regexp.MustCompile(`(?s)\n    ([a-z]+): \{(.*?)\n    \},`).
		FindAllStringSubmatch(js[topicsStart:topicsEnd], -1)
	checkedLists := 0
	for _, row := range topicRows {
		list := row[2]
		if !strings.Contains(list, `"/api/priority"`) {
			continue
		}
		checkedLists++
		if !strings.Contains(list, `"/api/conversations"`) {
			t.Errorf("the %s topic clears priority but not conversations", row[1])
		}
	}
	if checkedLists == 0 {
		t.Fatal("no priority invalidation topic was found")
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(css)
	for _, required := range []string{
		".conversations-title-row", ".conversations-total", ".conversations-coverage",
		".conversations-chips", ".conversations-list",
		".conversation-row", ".conversation-row-top", ".conversation-org",
		".conversation-when", ".conversation-unread", ".conversation-subject",
		".conversation-row-meta", ".conversation-people", ".conversation-snippet",
		".conversation-traffic", ".pager", ".conversation-error",
		".conversation-skeleton", "min-height: 58px",
		".conversations-chips .chip { flex: 0 0 auto; white-space: nowrap; }",
		".conversation-row { grid-template-columns: minmax(0, 1fr); gap: 1px; padding: 8px 10px; }",
		".conversations-pane .conversations-list { flex: 1 1 auto; max-height: none; min-height: 0; }",
	} {
		if !strings.Contains(styles, required) {
			t.Errorf("app.css lacks %q", required)
		}
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"mail.tab.conversations", "mail.conversations.title",
		"mail.conversations.count", "mail.conversations.coverage",
		"mail.conversations.search_placeholder", "mail.conversations.search_aria",
		"mail.conversations.filter_aria", "mail.conversations.list_aria",
		"mail.conversations.loading", "mail.conversations.empty_title",
		"mail.conversations.empty_body", "mail.conversations.no_match_title",
		"mail.conversations.no_match_body", "mail.conversations.clear_filters",
		"mail.conversations.error_title", "mail.conversations.error_body",
		"mail.conversations.refresh_error", "mail.conversations.load_more_error",
		"mail.conversations.loading_more",
		"mail.conversations.sent_received", "mail.conversations.unread",
		"mail.conversations.due", "mail.conversations.attachments",
		"mail.conversations.unknown_date", "mail.conversations.open_aria",
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	if got := en["mail.tab.conversations"].text; got != "Conversations" {
		t.Errorf("English conversations tab = %q", got)
	}
	if got := en["mail.conversations.title"].text; got != "Conversations" {
		t.Errorf("English conversations title = %q", got)
	}
	if got := en["mail.conversations.count"].plural["other"]; got != "{count} conversations" {
		t.Errorf("English conversation count = %q", got)
	}
	if got := en["mail.conversations.coverage"].text; got != "{conversations} · {messages}" {
		t.Errorf("English coverage = %q", got)
	}
	if got := en["mail.conversations.unread"].plural["one"]; got != "{count} unread" {
		t.Errorf("English unread = %q", got)
	}
	for key, want := range map[string]string{
		"mail.conversations.unknown_date":   "Date unknown",
		"mail.conversations.due":            "Due {date}",
		"mail.conversations.attachments":    "Attachments",
		"mail.conversations.empty_title":    "No conversations yet",
		"mail.conversations.no_match_title": "No conversations match",
		"mail.conversations.error_title":    "Conversations unavailable",
		"mail.conversations.open_aria":      "Open the latest message in {subject}",
	} {
		if got := en[key].text; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := fr["mail.tab.conversations"].text; got != "Conversations" {
		t.Errorf("French conversations tab = %q", got)
	}
	if got := fr["mail.conversations.unknown_date"].text; got != "Date inconnue" {
		t.Errorf("French unknown date = %q", got)
	}
	if got := fr["mail.conversations.unread"].plural["one"]; got != "{count} non lu" {
		t.Errorf("French unread = %q", got)
	}
	if got := fr["mail.conversations.due"].text; got != "Échéance {date}" {
		t.Errorf("French due = %q", got)
	}
	if got := fr["mail.conversations.empty_title"].text; got != "Aucune conversation pour le moment" {
		t.Errorf("French empty title = %q", got)
	}

	runConversationsBehavior(t)
}

// runConversationsBehavior executes the pure conversations block with the
// real catalogs: friendly states, organization, participant overflow, unread,
// due/attachment labels, load-more deduplication and empty/error copy.
func runConversationsBehavior(t *testing.T) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: source and catalog checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "conversations_behavior_test.js")
	if err := os.WriteFile(script, []byte(conversationsBehaviorHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "UI_DIR="+dir, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("conversations behavior timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("conversations behavior failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "conversations behavior: ok") {
		t.Fatalf("conversations behavior produced no success marker:\n%s", out)
	}
}

const conversationsBehaviorHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
// The conversations block calls the shared subject-path helper, which lives
// in the brief block in the app; supply the same catalog lookup here.
sandbox.subjectPathLabel = (topic, subtopic) => {
  const parent = topic ? I.tOr("labels.option.topic." + topic, topic) : "";
  const child = subtopic ? I.tOr("subject." + topic + "." + subtopic, subtopic) : "";
  return child ? (parent ? parent + " › " + child : child) : parent;
};
sandbox.formatDate = () => "Oct 26, 2026, 10:00";
sandbox.formatRelative = (value) => value
  ? (I.snapshot().language === "fr" ? "il y a 2 heures" : "2 hours ago")
  : I.t("common.dash");
sandbox.formatDay = () => I.snapshot().language === "fr" ? "10 nov. 2026" : "Nov 10, 2026";
sandbox.apiFailure = (error) => ({ message: error && error.message ? error.message : "" });

function h(tag, attrs, ...children) {
  const node = { tag, attrs: attrs || {}, children: [], text: "", dataset: {} };
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") node.dataset = value || {};
    if (key.startsWith("on") && typeof value === "function") node[key] = value;
  }
  node.addEventListener = (name, handler) => { node["on" + name] = handler; };
  const add = (child) => {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push({ tag: "#text", text: String(child), attrs: {}, children: [] });
  };
  node.append = (...kids) => { kids.forEach(add); return node; };
  children.forEach(add);
  return node;
}
sandbox.h = h;
sandbox.badge = (value, kind = "", title = "") => ({
  tag: "badge",
  attrs: { class: kind, title },
  text: String(value),
  children: [],
});

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const ts = app.indexOf("// thread-state:start"), te = app.indexOf("// thread-state:end");
assert.ok(ts >= 0 && te > ts, "thread-state markers are missing"); vm.runInContext(app.slice(ts, te), sandbox, { filename: "app.js#thread-state" });
const start = app.indexOf("// conversations-view:start");
const end = app.indexOf("// conversations-view:end");
assert.ok(start >= 0 && end > start, "conversations-view markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#conversations-view" });

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function same(actual, expected, message) {
  assert.equal(JSON.stringify(actual), JSON.stringify(expected), message);
}

assert.equal(sandbox.conversationFilterState("nope"), "");
assert.equal(sandbox.conversationFilterState("awaiting_owner"), "awaiting_owner");
assert.equal(sandbox.conversationViewKey("", " budget "), "all|budget");
assert.equal(sandbox.conversationPath(30, "abc", "awaiting_owner", "Budget review"),
  "/api/conversations?limit=30&before=abc&state=awaiting_owner&q=Budget%20review");
assert.equal(sandbox.conversationStateLabel("awaiting_owner"), "Waiting on you");
assert.equal(sandbox.conversationStateLabel("made_up_state"), "Check the thread");
assert.ok(!sandbox.conversationStateLabel("made_up_state").includes("made_up_state"));

const page = {
  generated_at: "2026-10-26T10:00:00Z",
  conversations: [
    {
      conversation_id: "C1", subject: "Budget review", sender_name: "Alice Martin",
      sender_address: "alice@example.com", last_message_id: 42,
      last_at: "2026-10-26T10:00:00Z", first_at: "2026-10-20T10:00:00Z",
      messages: 3, sent: 1, received: 2, unread: 1, last_sent: false, last_is_read: true,
      has_attachments: true, snippet: "Here is the budget", due_date: "2026-11-10",
      participant_count: 3,
      participants: [
        { name: "Alice", address: "alice@example.com", messages: 2, last_at: "2026-10-26T10:00:00Z" },
        { name: "Bob", address: "bob@example.com", messages: 1, last_at: "2026-10-26T09:00:00Z" },
        { name: "Carol", address: "carol@example.com", messages: 1, last_at: "2026-10-25T09:00:00Z" },
      ],
      organization: { key: "c:atelier nord", label: "Atelier Nord", declared: true },
      state: {
        state: "awaiting_owner", reason: "inbound_last", actor: "Alice",
        last_at: "2026-10-26T10:00:00Z", last_message_id: 42,
        reply_to_message_id: 41, visibility: "complete", confidence: 0.85,
        topic: "billing_account", subtopic: "payment",
      },
      state_source: "priority",
    },
    {
      conversation_id: "C2", subject: "", sender_name: "", sender_address: "dave@example.com",
      last_message_id: 43, last_at: "", first_at: "", messages: 1, sent: 0, received: 1,
      unread: 0, last_sent: false, last_is_read: true, has_attachments: false,
      snippet: "", due_date: "", participant_count: 1,
      participants: [{ name: "", address: "dave@example.com", messages: 1, last_at: "" }],
      state: {
        state: "made_up_state", reason: "unknown", actor: "", last_at: "",
        last_message_id: 43, reply_to_message_id: 0, visibility: "complete", confidence: 0,
      },
      state_source: "store",
    },
  ],
  total: 2,
  counts: { awaiting_owner: 1, awaiting_other: 0, answered_by_owner: 0, answered_by_other: 0, no_action: 0, unknown: 1 },
  next_before: "cursor-1",
  has_more: true,
  preview: false,
  coverage: { messages: 35000, conversations: 10000, unthreaded: 12 },
};

const view = sandbox.conversationViewFromPage(page, "", "budget");
assert.equal(view.state, "");
assert.equal(view.q, "budget");
assert.equal(view.rows.length, 2);
assert.equal(view.total, 2);
assert.equal(view.nextBefore, "cursor-1");
assert.equal(view.hasMore, true);
assert.equal(view.counts.awaiting_owner, 1);
assert.equal(view.counts.unknown, 1);
assert.equal(view.counts.all, 2);
assert.equal(view.coverage.messages, 35000);
assert.equal(view.coverage.conversations, 10000);
assert.equal(view.coverage.unthreaded, 12);
assert.equal(sandbox.conversationTotalLabel(view), "2 conversations");
assert.equal(sandbox.conversationCoverageLabel(view), "10,000 conversations · 35,000 messages");

const first = sandbox.conversationRowModel(page.conversations[0]);
assert.equal(first.stateLabel, "Waiting on you");
assert.equal(first.stateKind, "accent");
assert.equal(first.organization, "Atelier Nord");
assert.equal(first.people, "Alice, Bob +1");
assert.equal(first.countLabel, "3 messages");
assert.equal(first.sentReceived, "1 sent · 2 received");
assert.equal(first.unread, true);
assert.equal(first.unreadLabel, "1 unread");
assert.equal(first.when, "2 hours ago");
assert.ok(first.whenTitle.includes("Oct 26"));
assert.equal(first.hasAttachments, true);
assert.equal(first.attachmentLabel, "Attachments");
assert.equal(first.dueDate, "2026-11-10");
assert.equal(first.dueLabel, "Due Nov 10, 2026");
assert.equal(first.subjectPath, "Billing or account › Payment");
assert.ok(!JSON.stringify(first).includes("awaiting_owner"), "raw state enum leaked into the row model");
assert.ok(!JSON.stringify(first).includes("billing_account"), "raw subject identifier leaked into the row model");

const second = sandbox.conversationRowModel(page.conversations[1]);
assert.equal(second.subject, "(no subject)");
assert.equal(second.people, "dave@example.com");
assert.equal(second.stateLabel, "Check the thread");
assert.equal(second.stateKind, "warn");
assert.equal(second.when, "Date unknown");
assert.equal(second.whenTitle, "Date unknown");
assert.equal(second.unread, false);
assert.equal(second.dueLabel, "");
assert.ok(!JSON.stringify(second).includes("made_up_state"));

const overflowRow = Object.assign({}, page.conversations[0], { participant_count: 5 });
assert.equal(sandbox.conversationPeopleLabel(overflowRow), "Alice, Bob +3");

const picked = [];
const rowNode = sandbox.conversationRowNode(page.conversations[0], {
  selected: true,
  onSelect: (row) => picked.push(row.conversation_id),
});
assert.equal(rowNode.tag, "button");
assert.equal(rowNode.dataset.messageId, "42");
assert.equal(rowNode.dataset.conversationId, "C1");
assert.ok(String(rowNode.attrs.class).includes("conversation-row"));
assert.ok(String(rowNode.attrs.class).includes("selected"));
assert.ok(String(rowNode.attrs.class).includes("unread"));
assert.ok(rowNode.attrs["aria-label"].includes("Budget review"));
const rowText = texts(rowNode).join(" | ");
assert.ok(rowText.includes("Waiting on you"), "the friendly state label renders");
assert.ok(rowText.includes("Atelier Nord"), "the organization label renders");
assert.ok(rowText.includes("Alice, Bob +1"), "participant overflow renders");
assert.ok(rowText.includes("3 messages"), "the message count renders");
assert.ok(rowText.includes("1 sent · 2 received"), "the sent/received line renders");
assert.ok(rowText.includes("Due Nov 10, 2026"), "the due date badge renders");
assert.ok(rowText.includes("Billing or account › Payment"), "the subject path renders");
assert.ok(rowText.includes("Attachments"), "the attachment badge renders");
assert.ok(rowText.includes("2 hours ago"), "relative activity renders");
assert.ok(!rowText.includes("awaiting_owner"), "the row never renders a raw state enum");
assert.equal(all(rowNode, (entry) => entry.tag === "badge").length, 3, "state, attachment and due badges");
rowNode.onclick({ preventDefault() {} });
same(picked, ["C1"], "a row opens its conversation through the selection callback");

const page2 = {
  conversations: [
    Object.assign({}, page.conversations[0], { snippet: "duplicate" }),
    {
      conversation_id: "C3", subject: "Invoice", sender_name: "Eve", sender_address: "eve@example.com",
      last_message_id: 44, last_at: "2026-10-25T10:00:00Z", messages: 1, sent: 0, received: 1,
      unread: 1, has_attachments: false, snippet: "Invoice attached", due_date: "",
      participant_count: 1,
      participants: [{ name: "Eve", address: "eve@example.com", messages: 1, last_at: "" }],
      state: { state: "no_action" }, state_source: "priority",
    },
  ],
  total: 3,
  counts: { awaiting_owner: 1, awaiting_other: 0, answered_by_owner: 0, answered_by_other: 0, no_action: 1, unknown: 1 },
  next_before: "",
  has_more: false,
  preview: false,
  coverage: page.coverage,
};
const merged = sandbox.conversationMergePage(view, page2);
same(merged.rows.map((row) => row.conversation_id), ["C1", "C2", "C3"], "load more appends without duplicates");
assert.equal(merged.rows[0].snippet, "Here is the budget", "a duplicate keeps the existing row");
assert.equal(merged.total, 3);
assert.equal(merged.hasMore, false);
assert.equal(merged.counts.no_action, 1);

const stuck = sandbox.conversationMergePage(view, {
  conversations: [page.conversations[0]],
  total: 2,
  counts: page.counts,
  next_before: "cursor-2",
  has_more: true,
  preview: false,
  coverage: page.coverage,
});
assert.equal(stuck.rows.length, 2, "a page with no new rows cannot duplicate");
assert.equal(stuck.hasMore, false, "a page with no new rows stops paging");

assert.equal(sandbox.conversationEmptyCopy(false).title, "No conversations yet");
assert.equal(sandbox.conversationEmptyCopy(true).title, "No conversations match");
const failure = sandbox.conversationErrorCopy(new Error("boom"));
assert.equal(failure.title, "Conversations unavailable");
assert.equal(failure.body, "boom");

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
const french = sandbox.conversationRowModel(page.conversations[0]);
assert.equal(french.stateLabel, "En attente de vous");
assert.equal(french.unreadLabel, "1 non lu");
assert.equal(french.dueLabel, "Échéance 10 nov. 2026");
assert.equal(french.sentReceived, "Envoyés : 1 · Reçus : 2");
assert.equal(sandbox.conversationErrorCopy(new Error("boom")).title, "Conversations indisponibles");
assert.equal(sandbox.conversationEmptyCopy(false).title, "Aucune conversation pour le moment");
I.setLanguage({ language: "en", formatLocale: "en-US" });

process.stdout.write("conversations behavior: ok\n");
`

// clear() returns undefined; chaining .append onto it once crashed the Mail
// screen when no message was selected. Keep the explicit two-step paint.
func TestMailDetailEmptyStatePaint(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	if strings.Contains(js, "clear(detail).append") {
		t.Fatal("app.js chains .append onto clear(), which returns undefined")
	}
	if n := strings.Count(js, "clear(detail);"); n < 3 {
		t.Errorf("Mail, Recent and Search empty details paint with %d explicit clear(detail) calls, want 3", n)
	}
	for _, marker := range []string{
		`detail.append(emptyState(t("mail.select.title")`,
		`detail.append(emptyState(t("triage.select_message")`,
		`detail.append(emptyState(t("search.select_result")`,
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("app.js lacks the empty detail paint %q", marker)
		}
	}
}

// Internal navigation pushes history and renders synchronously, while nav
// hover/focus only warms the route cache.
func TestInstantNavigationAndNavPrefetch(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`function parseRouteFromHash(`,
		`function goToHash(`,
		`history.pushState(null, "", location.pathname + location.search + hash)`,
		`function navigate(name, params = {})`,
		`goToHash(routeHash(name, params))`,
		`function handleInternalLinkClick(`,
		`event.target.closest('a[href^="#/"]')`,
		`document.addEventListener("click", handleInternalLinkClick)`,
		`function renderHistoryRoute(`,
		`window.addEventListener("hashchange", renderHistoryRoute)`,
		`window.addEventListener("popstate", renderHistoryRoute)`,
		`function scheduleNavPrefetch(`,
		`navPrefetchLinks.has(href)`,
		`navPrefetchLinks.add(href)`,
		`prefetchQueue.then(`,
		`async function prefetchRoute(`,
		`routeRenderInFlight > 0`,
		`requestIdleCallback`,
		`window.setTimeout(run, 40)`,
		`pointerenter`,
		`function installNavPrefetch(`,
		`installNavPrefetch();`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the instant-navigation step %q", required)
		}
	}
	for _, forbidden := range []string{`location.hash = hash`, `else location.hash =`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js still waits for hashchange via %q", forbidden)
		}
	}
	if n := strings.Count(js, `document.addEventListener("click", handleInternalLinkClick)`); n != 1 {
		t.Errorf("the internal-link click handler is installed %d times", n)
	}
	// A cached route is painted synchronously, and an uncached one paints the
	// skeleton synchronously, before the first awaited render.
	start := strings.Index(js, "async function renderRouteNow(force = false)")
	end := strings.Index(js, "function scheduleNavPrefetch(")
	if start < 0 || end <= start {
		t.Fatal("renderRouteNow was not found")
	}
	body := js[start:end]
	cachedAt := strings.Index(body, "appendMain(cachedEntry.node")
	cachedAwait := -1
	if cachedAt >= 0 {
		if rel := strings.Index(body[cachedAt:], "await renderRouteNode"); rel >= 0 {
			cachedAwait = cachedAt + rel
		}
	}
	skeletonAt := strings.Index(body, "appendMain(routeSkeleton(route.name), 0, null, false)")
	skeletonAwait := -1
	if skeletonAt >= 0 {
		if rel := strings.Index(body[skeletonAt:], "await renderRouteNode"); rel >= 0 {
			skeletonAwait = skeletonAt + rel
		}
	}
	if cachedAt < 0 || cachedAwait < 0 || cachedAt > cachedAwait {
		t.Fatal("a cached route must be painted before its refresh awaits")
	}
	if skeletonAt < 0 || skeletonAwait < 0 || skeletonAt > skeletonAwait {
		t.Fatal("the skeleton must be painted before the uncached render awaits")
	}
}

// Settings opens in a curated simple view. The essential set is owned once,
// excludes secrets and paths, and the full catalog is still one click away.
func TestSettingsSimpleView(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "const SETTINGS_SIMPLE_GROUPS = [")
	end := strings.Index(js[start:], "const SETTINGS_SIMPLE_KEYS")
	if start < 0 || end <= 0 {
		t.Fatal("SETTINGS_SIMPLE_GROUPS not found")
	}
	block := js[start : start+end]
	essential := map[string]bool{
		"ui.language": true, "time.zone": true, "qa.enabled": true,
		"qa.max_messages": true, "sync.details.enabled": true,
		"triage.folder_roles": true, "threads.folder_roles": true,
		"learning.test_mode": true,
	}
	for key := range essential {
		if !strings.Contains(block, `"`+key+`"`) {
			t.Errorf("the simple setting set lacks %q", key)
		}
	}
	for _, forbidden := range []string{"secret", "http.", "store.", "secrets.", "file", "runtime", "embed_timeout"} {
		if strings.Contains(strings.ToLower(block), forbidden) {
			t.Errorf("the simple setting set includes the technical key %q", forbidden)
		}
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for key := range essential {
		catalogKey := "settings.simple.text." + key
		if strings.TrimSpace(en[catalogKey].text) == "" || strings.TrimSpace(fr[catalogKey].text) == "" {
			t.Errorf("missing everyday wording for %q", catalogKey)
		}
	}
	for key := range en {
		if strings.HasPrefix(key, "settings.simple.text.") && !essential[strings.TrimPrefix(key, "settings.simple.text.")] {
			t.Errorf("stale everyday wording %q", key)
		}
	}
	for _, required := range []string{
		`const SETTINGS_ADVANCED_KEY = "mailsense.settings.advanced"`,
		`window.sessionStorage.getItem(SETTINGS_ADVANCED_KEY)`,
		`window.sessionStorage.setItem(SETTINGS_ADVANCED_KEY`,
		`t("settings.simple.show_all")`,
		`t("settings.simple.back")`,
		`t("settings.simple.hidden"`,
		`t("settings.search.all"`,
		`t("settings.meta.simple")`,
		`t("settings.meta.advanced")`,
		`t("settings.meta.shown"`,
		`tOr("settings.simple.group." + groupId, groupId)`,
		`settingDescription(v)`,
		`h("code", { class: "setting-key", text: key })`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the settings simple-view step %q", required)
		}
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".settings-view-toggle", ".settings-simple-intro", ".settings-simple-note",
		".settings-search-note", ".setting-title", ".setting-copy",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
}

// The Providers screen is the single source of truth: one role row, only
// configured records, a six-field basic form and one collapsed Advanced
// disclosure. The Node half renders those shapes from the real catalogs.
func TestProviderEditorAndRoleSelects(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const PROVIDER_ROLE_ORDER = [`,
		`"qa.provider": "settings.providers.role.qa"`,
		`"judge.provider": "settings.providers.role.judge"`,
		`"triage.decider": "settings.providers.role.triage"`,
		`"embed.provider": "settings.providers.role.embed"`,
		`function applyProviderCatalog(view)`,
		`function providerKind(protocol)`,
		`function providerFormNode(`,
		`function providerRoleSelect(`,
		`function providersPanel(`,
		`function providerPayload(`,
		`function providerDiscoveryBody(`,
		`function providerModelValues(`,
		`function providerTestDetail(`,
		`request("/api/providers/discover", { method: "POST", body })`,
		`const providerRead = requestCached("/api/providers"`,
		`request(path, { method: isNew ? "POST" : "PUT", body: result.payload })`,
		`request("/api/secrets/" + encodeURIComponent(secretName), { method: "PUT", body: { value: result.keyValue } })`,
		`request("/api/providers/" + encodeURIComponent(record.id) + "/test", { method: "POST" })`,
		`request("/api/providers/" + encodeURIComponent(record.id), { method: "DELETE" })`,
		`settings.providers.role.unconfigured`,
		`settings.providers.roles.save`,
		`settings.providers.configured_title`,
		`settings.providers.discovery.fetch`,
		`detail_code`,
		`applyModel,`,
		`settings.tab.providers`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the provider screen step %q", required)
		}
	}
	for _, forbidden := range []string{
		`providerKeysPanel`, `secretDialog`, `settings.keys.`, `settings.keys.used_by`,
		`settings.keys.table`, `secret-table`, `providerRecord`, `providerRoleControl`,
		`auth_secret`, `entry.provider`, `disabled: provider.kind !== "decider"`,
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js still contains the old provider/key UI %q", forbidden)
		}
	}
	for _, vendor := range []string{"subq", "provider.example.test", "EXAMPLE_PROVIDER", "CLOUDFLARE_API_TOKEN", "OPENAI_API_KEY", "SUBQ_API_KEY"} {
		if strings.Contains(js, vendor) {
			t.Errorf("app.js ships a vendor preset name %q", vendor)
		}
	}
	if strings.Contains(js, "Gemma") {
		t.Error("app.js still hard-codes the judge brand")
	}

	advanced := []string{
		"context_tokens", "chars_per_token", "reserve_tokens_per_byte", "reserve_output_tokens",
		"max_questions", "probability_tolerance", "response_format", "reasoning_effort",
		"price_input_per_mtok", "price_output_per_mtok", "retry.max_attempts",
		"retry.statuses", "retry.quota_codes", "envelope", "noul_fold", "runtime",
		"embedding.dimensions", "embedding.matryoshka", "embedding.query_template",
		"embedding.document_template", "embedding.empty_title", "embedding.norm_tolerance",
		"embedding.max_batch", "embedding.reserved_tokens",
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, name := range append(advanced, "id", "path", "api_key_secret") {
		key := "settings.providers.field." + strings.ReplaceAll(name, ".", "_")
		if strings.TrimSpace(en[key].text) == "" {
			t.Errorf("i18n.en.js lacks the advanced field label %q", key)
		}
		if strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("i18n.fr.js lacks the advanced field label %q", key)
		}
	}
	for _, key := range []string{
		"settings.providers.add", "settings.providers.advanced", "settings.providers.roles.title",
		"settings.providers.roles.sub", "settings.providers.roles.save", "settings.providers.roles.saved",
		"settings.providers.role.unconfigured", "settings.providers.role.missing",
		"settings.providers.role.qa", "settings.providers.role.judge",
		"settings.providers.role.triage", "settings.providers.role.embed",
		"settings.providers.configured_title", "settings.providers.configured_sub",
		"settings.providers.group.record", "settings.providers.field.replace_key",
		"settings.providers.field.path_hint", "settings.providers.error.role",
		"settings.providers.error.save", "settings.providers.error.key", "settings.providers.error.delete",
		"settings.providers.test_failed", "settings.providers.test_result_ok",
		"settings.providers.test_result_fail", "settings.providers.test_detail",
		"settings.providers.test.detail.generator_answered",
		"settings.providers.test.detail.decider_answered",
		"settings.providers.test.detail.embedder_answered",
		"settings.providers.discovery.fetch", "settings.providers.discovery.fetching",
		"settings.providers.discovery.hint", "settings.providers.discovery.select_label",
		"settings.providers.discovery.select_placeholder", "settings.providers.discovery.applied",
		"settings.providers.discovery.empty", "settings.providers.discovery.auth",
		"settings.providers.discovery.no_endpoint", "settings.providers.discovery.use_note",
		"settings.providers.discovery.details.context", "settings.providers.discovery.details.max_output",
		"settings.providers.discovery.details.price_input", "settings.providers.discovery.details.price_output",
		"settings.providers.discovery.details.capabilities",
		"settings.providers.discovery.capability.structured_output",
		"settings.providers.discovery.capability.reasoning",
		"settings.providers.discovery.capability.tools",
	} {
		if strings.TrimSpace(en[key].text) == "" || strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("missing provider screen catalog key %q", key)
		}
	}
	for _, key := range []string{"settings.providers.discovery.loaded", "settings.providers.discovery.details.tokens"} {
		if !en[key].isPlural || !fr[key].isPlural {
			t.Errorf("%q must be a plural entry in both catalogs", key)
		}
	}
	for _, key := range []string{
		"api.provider_discovery_auth", "api.provider_discovery_not_found",
		"api.provider_discovery_failed", "api.provider_discovery_invalid",
		"api.provider_discovery_empty",
	} {
		if strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("i18n.fr.js lacks the discovery API message %q", key)
		}
	}
	for key := range en {
		if strings.HasPrefix(key, "settings.keys.") || strings.HasPrefix(key, "settings.keys_") {
			t.Errorf("i18n.en.js still carries the vendor key table copy %q", key)
		}
		if strings.HasPrefix(key, "settings.secret.") {
			t.Errorf("i18n.en.js still carries the standalone secret dialog copy %q", key)
		}
	}
	for _, key := range []string{"action.run_judge", "reason.review", "labels.review.kicker", "hub.judge", "answers.source.judge"} {
		for _, catalog := range []map[string]catalogEntry{en, fr} {
			for _, text := range entryStrings(catalog[key]) {
				if strings.Contains(text, "Gemma") {
					t.Errorf("%q still hard-codes the judge brand: %q", key, text)
				}
			}
		}
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".provider-form", ".provider-form-grid", ".provider-field-grid", ".provider-card",
		".provider-card-head", ".provider-card-actions", ".provider-roles",
		".provider-form-actions", ".provider-discovery", ".provider-discovery-select",
		".provider-discovery-details",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
	if strings.Contains(string(css), ".secret-table") || strings.Contains(string(css), ".provider-meta") {
		t.Error("app.css still styles the removed secret table or old provider meta grid")
	}
	runProviderEditorBehavior(t)
}

func runProviderEditorBehavior(t *testing.T) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: source and catalog checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "provider_editor_behavior_test.js")
	if err := os.WriteFile(script, []byte(providerEditorBehaviorHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "UI_DIR="+dir, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("provider editor behavior timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("provider editor behavior failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "provider editor behavior: ok") {
		t.Fatalf("provider editor behavior produced no success marker:\n%s", out)
	}
}

const providerEditorBehaviorHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
vm.runInContext(app.slice(app.indexOf("// finite-number:start"), app.indexOf("// finite-number:end")), sandbox, { filename: "app.js#finite-number" });
const start = app.indexOf("// provider-editor:start");
const end = app.indexOf("// provider-editor:end");
assert.ok(start >= 0 && end > start, "provider-editor markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#provider-editor" });

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag,
    attrs: {},
    children: [],
    text: "",
    dataset: {},
    value: "",
    disabled: false,
    hidden: false,
    textContent: "",
    classList: {
      add(...names) {
        const current = String(node.attrs.class || "").split(/\s+/).filter(Boolean);
        for (const name of names) if (name && !current.includes(name)) current.push(name);
        node.attrs.class = current.join(" ");
      },
      remove(...names) {
        const current = String(node.attrs.class || "").split(/\s+/).filter(Boolean);
        node.attrs.class = current.filter((name) => !names.includes(name)).join(" ");
      },
      toggle() {},
    },
    addEventListener() {},
    focus() {},
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); }
    if (key === "value") node.value = value === undefined || value === null ? "" : String(value);
    if (key === "disabled") node.disabled = !!value;
    if (key === "hidden") node.hidden = !!value;
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.text = (value) => textNode(value);
sandbox.field = (label, control, hint) => makeNode("div", { class: "field" }, [
  makeNode("label", {}, [textNode(label)]),
  control,
  hint ? makeNode("span", { class: "field-hint", text: hint }) : null,
]);
sandbox.button = (label, onClick, opts) => makeNode("button", { class: (opts && opts.kind) || "", text: label }, []);
sandbox.badge = (value, kind, title) => makeNode("span", { class: "badge " + (kind || ""), title, text: value }, []);
sandbox.withBusy = async (_trigger, _label, fn) => fn();
sandbox.clear = (node) => { node.children.length = 0; };
sandbox.formatUsd = I.formatUsd.bind(I);
sandbox.I18N = I;

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function hasClass(node, name) {
  return String((node.attrs && node.attrs.class) || "").split(/\s+/).includes(name);
}
function optionValues(select) {
  return select.children.filter((node) => node.tag === "option").map((node) => String(node.attrs.value));
}
function optionTexts(select) {
  return select.children.filter((node) => node.tag === "option").map((node) => String(node.text));
}
function control(root, name) {
  return all(root, (node) => node.attrs && node.attrs.name === name)[0];
}
function setControl(root, name, value) {
  const node = control(root, name);
  assert.ok(node, "missing form control " + name);
  node.value = String(value);
}

const providers = {
  protocols: [
    { name: "openai_chat", kind: "generator" },
    { name: "openai_embeddings", kind: "embedder" },
    { name: "openai_decisions", kind: "decider" },
    { name: "systemone", kind: "decider" },
  ],
  roles: [
    { setting: "qa.provider", label: "Q&A generator", kind: "generator" },
    { setting: "judge.provider", label: "judge generator", kind: "generator" },
    { setting: "triage.decider", label: "triage decider", kind: "decider" },
    { setting: "embed.provider", label: "semantic embedder", kind: "embedder" },
  ],
  providers: [
    { id: "gen-a", label: "Generator A", protocol: "openai_chat", kind: "generator", model: "model-a", key_present: true },
    { id: "dec-a", label: "Decider A", protocol: "systemone", kind: "decider", model: "model-d" },
    { id: "emb-a", label: "Embedder A", protocol: "openai_embeddings", kind: "embedder", model: "model-e" },
  ],
};
sandbox.applyProviderCatalog(providers);

assert.equal(sandbox.providerKind("openai_chat"), "generator");
assert.equal(sandbox.providerKind("openai_embeddings"), "embedder");
assert.equal(sandbox.providerKind("systemone"), "decider");
assert.equal(sandbox.providerKind("openai_decisions"), "decider");
assert.equal(sandbox.providerSlug("Mon Fournisseur !"), "mon-fournisseur");
assert.equal(sandbox.providerSlug("Café"), "cafe");
assert.equal(sandbox.providerSecretSuggestion("My Provider!"), "PROVIDER_MY_PROVIDER_API_KEY");

const expectedAdvanced = [
  "context_tokens", "chars_per_token", "reserve_tokens_per_byte", "reserve_output_tokens",
  "max_questions", "probability_tolerance", "response_format", "reasoning_effort",
  "price_input_per_mtok", "price_output_per_mtok", "retry.max_attempts",
  "retry.statuses", "retry.quota_codes", "envelope", "noul_fold", "runtime",
  "embedding.dimensions", "embedding.matryoshka", "embedding.query_template",
  "embedding.document_template", "embedding.empty_title", "embedding.norm_tolerance",
  "embedding.max_batch", "embedding.reserved_tokens",
];
const advancedGroups = vm.runInContext("PROVIDER_ADVANCED_GROUPS", sandbox);
const actualAdvanced = advancedGroups.flatMap((group) => group.fields.map((field) => field.name));
assert.equal(JSON.stringify(actualAdvanced.sort()), JSON.stringify(expectedAdvanced.slice().sort()));

const form = sandbox.providerFormNode({});
assert.ok(form.node, "the provider form did not render");
const basicGrid = all(form.node, (node) => hasClass(node, "provider-form-grid"))[0];
assert.ok(basicGrid, "the basic form grid is missing");
const basicNames = all(basicGrid, (node) => node.attrs && node.attrs.name).map((node) => node.attrs.name);
assert.equal(JSON.stringify(basicNames.sort()), JSON.stringify(["api_key", "base_url", "label", "model", "path", "protocol"]));
assert.ok(texts(form.node).includes("Fetch models"), "the discovery button is missing");
assert.ok(all(form.node, (node) => hasClass(node, "provider-discovery")).length, "the discovery row is missing");
assert.ok(all(form.node, (node) => hasClass(node, "provider-discovery-select")).length, "the model select is missing");
const details = all(form.node, (node) => node.tag === "details")[0];
assert.ok(details, "the Advanced disclosure is missing");
assert.ok(details.attrs.open !== true, "Advanced is open by default");
const advancedNames = all(details, (node) => node.attrs && node.attrs.name).map((node) => node.attrs.name);
for (const name of ["id", "api_key_secret"].concat(expectedAdvanced)) {
  assert.ok(advancedNames.includes(name), "Advanced lacks " + name);
}
assert.ok(texts(form.node).includes("Advanced"), "the Advanced label is missing");
assert.ok(form.validate().includes("Label"), "an empty form does not ask for the label first");
assert.ok(texts(form.node).includes("API key"), "a new form does not offer an API key field");

const editForm = sandbox.providerFormNode({ record: {
  id: "gen-a", label: "Writer", protocol: "openai_chat",
  base_url: "https://example.test/v1", path: "/chat", model: "model-a",
  api_key_secret: "PROVIDER_GEN_A_API_KEY", key_present: true,
  context_tokens: 100, chars_per_token: 4, reserve_tokens_per_byte: 1,
  reserve_output_tokens: 10, price_input_per_mtok: 1, price_output_per_mtok: 2,
} });
assert.ok(texts(editForm.node).includes("Replace key"), "an edit form does not offer Replace key");
assert.ok(texts(editForm.node).join(" ").includes("A key is stored"), "an edit form does not say the key is present");
assert.equal(control(editForm.node, "context_tokens").value, "100");
assert.equal(control(editForm.node, "id").value, "gen-a");
assert.equal(control(editForm.node, "path").value, "/chat");
assert.equal(editForm.validate(), "");
assert.equal(editForm.collect().payload.id, "gen-a");
assert.equal(editForm.collect().payload.api_key_secret, "PROVIDER_GEN_A_API_KEY");

const filled = sandbox.providerFormNode({});
setControl(filled.node, "label", "Generator A");
setControl(filled.node, "base_url", "https://example.test/v1");
setControl(filled.node, "model", "model-a");
setControl(filled.node, "id", "gen-a");
setControl(filled.node, "path", "/chat/completions");
setControl(filled.node, "api_key_secret", "PROVIDER_GEN_A_API_KEY");
assert.equal(filled.validate(), "");
const collected = filled.collect();
assert.equal(collected.payload.id, "gen-a");
assert.equal(collected.payload.protocol, "openai_chat");
assert.equal(collected.keyValue, "");
assert.equal(collected.payload.max_questions, 0);
assert.equal(collected.payload.embedding, null);
assert.equal(collected.payload.retry.max_attempts, undefined);

const sampleModel = {
  id: "provider.example.test/example-chat",
  name: "Example Chat",
  type: "chat",
  context_tokens: 1048576,
  max_output_tokens: 393216,
  price_input_per_mtok: 0.19,
  price_output_per_mtok: 0.6,
  structured_output: true,
  reasoning: true,
  tools: true,
  streaming: true,
  endpoints: ["chat/completions"],
  path: "/chat/completions",
};
const modelForm = sandbox.providerFormNode({});
modelForm.applyModel(sampleModel);
assert.equal(control(modelForm.node, "model").value, "provider.example.test/example-chat");
assert.equal(control(modelForm.node, "path").value, "/chat/completions");
assert.equal(control(modelForm.node, "context_tokens").value, "1048576");
assert.equal(control(modelForm.node, "reserve_output_tokens").value, "16384");
assert.equal(control(modelForm.node, "price_input_per_mtok").value, "0.19");
assert.equal(control(modelForm.node, "price_output_per_mtok").value, "0.6");
assert.equal(control(modelForm.node, "response_format").value, "json_object");
assert.equal(control(modelForm.node, "reasoning_effort").value, "low");
assert.equal(control(modelForm.node, "label").value, "Example Chat");
assert.equal(control(modelForm.node, "id").value, "example-chat");
assert.equal(control(modelForm.node, "api_key_secret").value, "PROVIDER_EXAMPLE_CHAT_API_KEY");
assert.equal(control(modelForm.node, "chars_per_token").value, "3");
assert.equal(control(modelForm.node, "reserve_tokens_per_byte").value, "1");

const sparseForm = sandbox.providerFormNode({});
setControl(sparseForm.node, "label", "Typed label");
setControl(sparseForm.node, "context_tokens", "1000");
setControl(sparseForm.node, "reserve_output_tokens", "10");
setControl(sparseForm.node, "price_input_per_mtok", "1");
setControl(sparseForm.node, "price_output_per_mtok", "2");
setControl(sparseForm.node, "chars_per_token", "4");
setControl(sparseForm.node, "reserve_tokens_per_byte", "2");
const sparseValues = sparseForm.applyModel({ id: "sparse/model", name: "Sparse", type: "chat" });
assert.ok(!("context_tokens" in sparseValues), "a missing context was invented");
assert.ok(!("reserve_output_tokens" in sparseValues), "a missing output ceiling was invented");
assert.ok(!("price_input_per_mtok" in sparseValues), "a missing input price was invented");
assert.ok(!("price_output_per_mtok" in sparseValues), "a missing output price was invented");
assert.equal(control(sparseForm.node, "label").value, "Typed label");
assert.equal(control(sparseForm.node, "context_tokens").value, "1000");
assert.equal(control(sparseForm.node, "reserve_output_tokens").value, "10");
assert.equal(control(sparseForm.node, "price_input_per_mtok").value, "1");
assert.equal(control(sparseForm.node, "price_output_per_mtok").value, "2");
assert.equal(control(sparseForm.node, "chars_per_token").value, "4");
assert.equal(control(sparseForm.node, "reserve_tokens_per_byte").value, "2");
assert.equal(control(sparseForm.node, "model").value, "sparse/model");

const plainDiscovery = sandbox.providerDiscoveryBody(
  "openai_chat", " https://example.test/v1 ", "test-key-value",
  { key_present: true, api_key_secret: "PROVIDER_STORED_API_KEY" });
assert.equal(plainDiscovery.base_url, "https://example.test/v1");
assert.equal(plainDiscovery.protocol, "openai_chat");
assert.equal(plainDiscovery.api_key, "test-key-value");
assert.ok(!("api_key_secret" in plainDiscovery), "the plaintext key did not win over the stored name");
const storedDiscovery = sandbox.providerDiscoveryBody(
  "openai_chat", "https://example.test/v1", "",
  { key_present: true, api_key_secret: "PROVIDER_STORED_API_KEY" });
assert.equal(storedDiscovery.api_key_secret, "PROVIDER_STORED_API_KEY");
assert.ok(!("api_key" in storedDiscovery), "a stored key name must not send a key value");
const publicDiscovery = sandbox.providerDiscoveryBody(
  "openai_chat", "https://example.test/v1", "", { key_present: false, api_key_secret: "PROVIDER_STORED_API_KEY" });
assert.ok(!("api_key" in publicDiscovery) && !("api_key_secret" in publicDiscovery),
  "a public discovery request must not send an unused stored key");

assert.equal(sandbox.providerTestDetail({
  detail_code: "generator_answered", detail: "generator answered", model: "deepseek-v4.1-flash-standard",
}), "The generator answered.");
assert.ok(sandbox.providerTestDetail({
  detail_code: "embedder_answered", detail_params: { dimensions: 768 }, detail: "embedder answered",
}).includes("768-dimensional"));
assert.equal(sandbox.providerTestDetail({ detail_code: "future_code", detail: "server detail" }), "server detail");
assert.equal(sandbox.providerTestDetail({ message: "message only" }), "message only");
I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(sandbox.providerTestDetail({
  detail_code: "generator_answered", detail: "generator answered",
}), "Le générateur a répondu.");
I.setLanguage({ language: "en", formatLocale: "en-US" });


const chatPayload = sandbox.providerPayload({
  id: "gen-a", label: "Generator A", protocol: "openai_chat",
  base_url: "https://example.test/v1", path: "/chat/completions", model: "model-a",
  api_key_secret: "PROVIDER_GEN_A_API_KEY", context_tokens: 100, chars_per_token: 4,
  reserve_tokens_per_byte: 1, reserve_output_tokens: 10, max_questions: 9,
  probability_tolerance: 0.5, response_format: "json_object", reasoning_effort: "low",
  price_input_per_mtok: 1, price_output_per_mtok: 2, envelope: "cloudflare", noul_fold: "x",
  runtime: "llama-server", "retry.max_attempts": 2, "retry.statuses": [429],
  "retry.quota_codes": ["quota"], "embedding.dimensions": 768,
}, "openai_chat");
assert.equal(chatPayload.max_questions, 0);
assert.equal(chatPayload.probability_tolerance, 0);
assert.equal(chatPayload.envelope, "");
assert.equal(chatPayload.noul_fold, "");
assert.equal(chatPayload.runtime, "");
assert.equal(chatPayload.embedding, null);
assert.equal(chatPayload.response_format, "json_object");
assert.equal(chatPayload.reasoning_effort, "low");
assert.equal(chatPayload.retry.max_attempts, 2);
assert.equal(JSON.stringify(chatPayload.retry.statuses), JSON.stringify([429]));

const embedPayload = sandbox.providerPayload({
  id: "emb-a", label: "Embedder A", protocol: "openai_embeddings",
  base_url: "", path: "/v1/embeddings", model: "model-e", api_key_secret: "",
  context_tokens: 0, chars_per_token: 5, runtime: "llama-server",
  "embedding.dimensions": 768, "embedding.matryoshka": [512, 256],
  "embedding.query_template": "query: {text}", "embedding.document_template": "doc: {text}",
  "embedding.empty_title": "none", "embedding.norm_tolerance": 0.01,
  "embedding.max_batch": 32, "embedding.reserved_tokens": 8,
  response_format: "json_object", max_questions: 9,
}, "openai_embeddings");
assert.equal(embedPayload.runtime, "llama-server");
assert.equal(embedPayload.embedding.dimensions, 768);
assert.equal(JSON.stringify(embedPayload.embedding.matryoshka), JSON.stringify([512, 256]));
assert.equal(embedPayload.response_format, "");
assert.equal(embedPayload.max_questions, 0);

const providerRoles = sandbox.orderedProviderRoles();
const roles = [
  ["qa.provider", "gen-a", "dec-a"],
  ["judge.provider", "gen-a", "dec-a"],
  ["triage.decider", "dec-a", "gen-a"],
  ["embed.provider", "emb-a", "gen-a"],
];
for (const [key, expected, forbidden] of roles) {
  const role = providerRoles.find((entry) => entry.setting === key);
  assert.ok(role, "no role definition for " + key);
  const select = sandbox.providerRoleSelect(role, "", providers);
  assert.ok(select, "no role select for " + key);
  assert.equal(select.attrs["data-role"], key);
  const values = optionValues(select);
  assert.ok(values.includes(""), key + " lacks the unconfigured option");
  assert.ok(values.includes(expected), key + " lacks " + expected);
  assert.ok(!values.includes(forbidden), key + " includes the wrong kind");
  assert.ok(optionTexts(select).includes("Unconfigured"), key + " does not label the unconfigured option");
}
const missing = sandbox.providerRoleSelect(
  providerRoles.find((entry) => entry.setting === "qa.provider"), "gone", providers);
assert.ok(optionValues(missing).includes("gone"), "a missing role record is not preserved");
assert.ok(optionTexts(missing).includes("Missing record: gone"), "a missing role record is not named");

const card = sandbox.providerCard(
  { id: "gen-a", label: "Writer", protocol: "openai_chat", model: "model-a", key_present: true },
  { test() {}, edit() {}, remove() {} });
const cardText = texts(card).join(" | ");
for (const want of ["Writer", "openai_chat", "model-a", "key present"]) {
  assert.ok(cardText.includes(want), "the provider row lacks " + want);
}
for (const forbidden of ["Used by", "calls", "CLOUDFLARE", "PROVIDER_EXAMPLE", "SUBQ"]) {
  assert.ok(!cardText.includes(forbidden), "the provider row still shows " + forbidden);
}

console.log("provider editor behavior: ok");
`

// markedSource returns the app.js region between `// name:start` and
// `// name:end`, so a test can assert on one screen region.
func markedSource(t *testing.T, js, name string) string {
	t.Helper()
	start := strings.Index(js, "// "+name+":start")
	if start < 0 {
		t.Fatalf("app.js has no %s:start marker", name)
	}
	end := strings.Index(js[start:], "// "+name+":end")
	if end < 0 {
		t.Fatalf("app.js has no %s:end marker", name)
	}
	return js[start : start+end]
}

// runUISandbox runs one Node harness against the package directory. The
// harness must print marker once every assertion passed; the UI files come
// from the current sources, never a copy.
func runUISandbox(t *testing.T, name, harness, marker string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: source and catalog checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), name+".js")
	if err := os.WriteFile(script, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "UI_DIR="+dir, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s timed out: %v\n%s", name, ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	if !strings.Contains(string(out), marker) {
		t.Fatalf("%s produced no success marker:\n%s", name, out)
	}
}

// The Sources cell is two lines: the mailbox name (or the record key when it
// has none) and `source_id · key`. The raw Exchange EntryID stays in the API
// payload and the search store filter but never reaches the table.
func TestSourcesMailboxRowShowsShortKey(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`// mailbox-subtitle:start`,
		`function mailboxTitle(box)`,
		`function mailboxSubtitleParts(box)`,
		`const subtitle = mailboxSubtitleParts(box);`,
		`text: mailboxTitle(box)`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the Sources row step %q", required)
		}
	}
	row := markedSource(t, js, "mailbox-row")
	if strings.Contains(row, "store_id") {
		t.Error("mailboxRow still renders store_id")
	}
	if strings.Contains(js, "declareDialog(box.store_id)") {
		t.Error("the declare dialog receives store_id")
	}
	// The search store filter is a mailbox store key the API validates, and
	// the server owns the match set: the screen never filters a returned page.
	for _, required := range []string{
		`if (!box.key || seenStores.has(box.key)) continue;`,
		`storeOptions.append(h("option", { value: box.key`,
		`params.push("store=" + encodeURIComponent(store));`,
		`params.push("sender=" + encodeURIComponent(sender));`,
		`params.push("role=" + encodeURIComponent(role));`,
		`params.push("attach=" + encodeURIComponent(attach));`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("the search filter contract lost %q", required)
		}
	}
	for _, banned := range []string{`hits.filter(`, `hit.store_id !== store`} {
		if strings.Contains(js, banned) {
			t.Errorf("the search screen still filters the page client-side: %q", banned)
		}
	}
	runUISandbox(t, "mailbox_row", mailboxRowHarness, "mailbox row behavior: ok")
}

const mailboxRowHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {};
vm.createContext(sandbox);
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("// mailbox-subtitle:start");
const end = app.indexOf("// mailbox-subtitle:end");
assert.ok(start >= 0 && end > start, "mailbox-subtitle markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#mailbox-subtitle" });
const mailboxTitle = vm.runInContext("mailboxTitle", sandbox);
const subtitleOf = (box) => mailboxSubtitleParts(box).map((part) => part.text).join(" \u00b7 ");
const mailboxSubtitleParts = vm.runInContext("mailboxSubtitleParts", sandbox);

const entryID = "0000000038A1BB1005E5101AA1BB08002B2A56C20000454D534D44422E444C4C".repeat(4);
const box = {
  name: "Abonnement",
  key: "store-dd4d3f1b643e",
  source_id: "outlook",
  store_id: entryID,
};
const subtitle = subtitleOf(box);
assert.equal(subtitle, "outlook \u00b7 store-dd4d3f1b643e", "the subtitle is not source plus key");
assert.ok(subtitle.includes("outlook"), "the subtitle lost the source id");
assert.ok(subtitle.includes("store-dd4d3f1b643e"), "the subtitle lost the record key");
assert.ok(!subtitle.includes(entryID), "the subtitle leaked the raw store_id");
assert.equal(mailboxTitle(box), "Abonnement", "the title lost the mailbox name");

const unnamed = { key: "store-abc", source_id: "outlook", store_id: entryID };
assert.equal(mailboxTitle(unnamed), "store-abc", "an unnamed mailbox does not fall back to its key");
assert.ok(!mailboxTitle(unnamed).includes(entryID), "the title fell back to store_id");
assert.equal(subtitleOf(unnamed), "outlook \u00b7 store-abc");

const parts = mailboxSubtitleParts(box);
assert.equal(parts.length, 2, "the subtitle is not two parts");
assert.equal(parts[0].mono, false, "the source id is styled as code");
assert.equal(parts[1].mono, true, "the record key is not styled as code");
assert.equal(parts[1].text, "store-dd4d3f1b643e");

const sourceOnly = { source_id: "imap", store_id: entryID };
assert.equal(subtitleOf(sourceOnly), "imap", "a keyless mailbox lost its source id");
assert.equal(subtitleOf({ key: "k" }), "k", "a sourceless mailbox lost its key");
assert.equal(subtitleOf({}), "", "an empty mailbox produced text");

console.log("mailbox row behavior: ok");
`

// Settings → Local runtime is one engine card: the single build the catalog
// starts automatically, its state, the model, and one Refresh. There is no
// variant picker, no install form, no CPU/CUDA row and no per-row Verify.
func TestRuntimePanelSingleEngine(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`// runtime-panel:start`,
		`function runtimePanel(sidecar, providers, settings, status)`,
		`function embedderChoice(settings, providers)`,
		`function runtimeBuild(sidecar, run)`,
		`request("/api/sidecar/install"`,
		`settings.runtime.repair`,
		`settings.runtime.advanced`,
		`settings.runtime.not_selected_link`,
		`settings.runtime.idle_stop`,
		`function runtimeIdleNote(settings)`,
		`RUNTIME_IDLE_KEY = "sidecar.idle_timeout"`,
		`build.unavailable`,
		`settings.runtime.unavailable.`,
		`runtimePanel(sidecar, providers, settings, status)`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the runtime panel step %q", required)
		}
	}
	for _, forbidden := range []string{
		`/api/sidecar/verify`, `t("settings.runtime.variant")`, `t("settings.runtime.install")`,
		`settings.runtime.cpu`, `settings.runtime.choose_build`, `settings.runtime.table.build`,
		`settings.runtime.status_title`, `run-time-form`, `verifiedPin`,
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js still contains the manual build UI %q", forbidden)
		}
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	automatic := "Installed, verified and started automatically when embeddings need it. No action required."
	if en["settings.runtime.sub"].text != automatic {
		t.Errorf("i18n.en.js automatic copy is %q", en["settings.runtime.sub"].text)
	}
	if strings.TrimSpace(fr["settings.runtime.sub"].text) == "" {
		t.Error("i18n.fr.js lacks the automatic copy")
	}
	for _, key := range []string{
		"settings.runtime.title", "settings.runtime.model_label", "settings.runtime.auto_first_use",
		"settings.runtime.idle_stop", "settings.runtime.idle_never",
		"settings.runtime.engine_size", "settings.runtime.not_selected",
		"settings.runtime.not_selected_provider", "settings.runtime.not_selected_link",
		"settings.runtime.advanced", "settings.runtime.advanced_hint", "settings.runtime.repair",
		"settings.runtime.repairing", "settings.runtime.repair_started", "settings.runtime.repair_body",
		"settings.runtime.repair_failed", "settings.runtime.repair_unavailable", "settings.runtime.no_builds",
		"settings.runtime.state.ready", "settings.runtime.state.not_installed", "settings.runtime.state.remote",
		"settings.runtime.unavailable.vulkan", "settings.runtime.unavailable.probe_failed",
	} {
		if strings.TrimSpace(en[key].text) == "" || strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("missing runtime catalog key %q", key)
		}
	}
	for _, key := range []string{"settings.runtime.cpu", "settings.runtime.variant", "settings.runtime.install_title"} {
		for _, catalog := range []map[string]catalogEntry{en, fr} {
			if _, ok := catalog[key]; ok {
				t.Errorf("catalog still carries the manual build copy %q", key)
			}
		}
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".runtime-card", ".runtime-identity", ".runtime-facts", ".runtime-advanced", ".runtime-note"} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
	if strings.Contains(string(css), ".run-time-form") {
		t.Error("app.css still styles the removed install form")
	}
	docs, err := os.ReadFile(filepath.Join("..", "..", "docs", "UI.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Vulkan build", "Reinstall / Repair", "no variant picker"} {
		if !strings.Contains(string(docs), required) {
			t.Errorf("docs/UI.md lacks %q", required)
		}
	}
	runUISandbox(t, "runtime_panel", runtimePanelHarness, "runtime panel behavior: ok")
}

// The Sources backfill row reads its label from the one job-summary owner:
// a second derivation would drift from the strip's paused and failed wording.
// The strip keeps the one name/status sentence for every screen.
func TestSourcesReuseTheJobSummaryOwner(t *testing.T) {
	sources := readUIModule(t, "25-sources.js")
	for _, want := range []string{
		`text: jobSummaryName(job)`,
		`name.textContent = jobSummaryName(info)`,
	} {
		if !strings.Contains(sources, want) {
			t.Errorf("25-sources.js lacks %q", want)
		}
	}
	for _, duplicate := range []string{
		`.replace(/[._-]+/g, " ")`,
		`+ " (" + statusLabel(`,
	} {
		if strings.Contains(sources, duplicate) {
			t.Errorf("25-sources.js re-derives a job label: %q", duplicate)
		}
	}
}

// One inline banner answers a failed read: the shared warning surface, the
// error sentence and the shared button primitive. Two screens spelled it by
// hand, and each kept a different action shape.
func TestRetryBannerIsShared(t *testing.T) {
	components := readUIModule(t, "01-components.js")
	for _, want := range []string{
		`function retryBanner(message, onRetry, extra)`,
		`warningBanner(fragment(`,
		`h("span", { text: message })`,
		`button(t("common.try_again"), onRetry, { small: true, icon: "refresh" })`,
	} {
		if !strings.Contains(components, want) {
			t.Errorf("01-components.js lacks %q", want)
		}
	}
	for _, name := range []string{"41-providers.js", "27-answers.js"} {
		source := readUIModule(t, name)
		if !strings.Contains(source, "retryBanner(") {
			t.Errorf("%s does not use the shared retry banner", name)
		}
		if strings.Contains(source, `h("span", { text: apiFailure(`) {
			t.Errorf("%s still rebuilds the retry banner", name)
		}
	}
	// Keyboard parity: the action is a native button through the shared
	// primitive, not a clickable span, so it keeps its place in the tab order.
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	sharedButton := functionBody(t, string(script), "function button(")
	if !strings.Contains(sharedButton, `type: "button"`) {
		t.Error("the shared button primitive is not a native button")
	}
}

// The two Vulkan API codes are French-only catalog entries: French shows
// the localized message and keeps the server text as the diagnostic;
// English has no api.* key, so it shows the server message.
func TestVulkanAPICodesAreLocalized(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"api.vulkan_unavailable", "api.vulkan_probe_failed"} {
		if _, ok := en[key]; ok {
			t.Errorf("i18n.en.js carries the French-only key %q", key)
		}
		if strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("i18n.fr.js is missing %q", key)
		}
	}
	runUISandbox(t, "api_failure_vulkan", apiFailureVulkanHarness, "api failure vulkan behavior: ok")
}

const apiFailureVulkanHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["fr-FR"], language: "fr-FR" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.I18N = I;

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("class ApiError");
const end = app.indexOf("function problemLabel");
assert.ok(start >= 0 && end > start, "the ApiError/apiFailure slice is missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#api-failure" });
const apiFailure = vm.runInContext("apiFailure", sandbox);
const ApiError = vm.runInContext("ApiError", sandbox);

for (const [code, needle] of [
  ["vulkan_unavailable", "Aucun pilote Vulkan"],
  ["vulkan_probe_failed", "vérification de l’appareil Vulkan"],
]) {
  const failure = apiFailure(new ApiError(422, { code, message: "server english" }));
  assert.ok(failure.message.includes(needle), code + " did not map: " + failure.message);
  assert.equal(failure.diagnostic, "server english", code + " lost the server diagnostic");
}

I.setLanguage({ language: "en", formatLocale: "en-US" });
const english = apiFailure(new ApiError(422, { code: "vulkan_unavailable", message: "server english" }));
assert.equal(english.message, "server english", "English did not fall back to the server message");

console.log("api failure vulkan behavior: ok");
`

const runtimePanelHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.formatBytes = I.formatBytes.bind(I);
sandbox.formatDuration = I.formatDuration.bind(I);
sandbox.formatDate = I.formatDate.bind(I);
sandbox.humanCode = (code) => String(code || "").replace(/_/g, " ");
sandbox.providerListFrom = (providers) => Array.isArray(providers) ? providers : (providers && providers.providers) || [];
sandbox.routeHash = (name, params) => "#/" + name + (params && params.tab ? "?tab=" + params.tab : "");
sandbox.renderRoute = () => {};
sandbox.request = async () => ({});
sandbox.toast = () => {};
sandbox.withBusy = async (_trigger, _label, fn) => fn();
sandbox.apiFailure = (error) => ({ message: String(error && error.message || error) });
sandbox.clear = (node) => { node.children.length = 0; };
let invalidated = [];
let refreshed = 0;
sandbox.invalidateCache = (prefixes) => { invalidated.push(...(prefixes || [])); };
sandbox.refreshActiveRoute = () => { refreshed += 1; };

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag,
    attrs: {},
    children: [],
    text: "",
    dataset: {},
    disabled: false,
    hidden: false,
    textContent: "",
    classList: { add() {}, remove() {}, toggle() {} },
    addEventListener() {},
    focus() {},
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); }
    if (key === "value") node.value = value === undefined || value === null ? "" : String(value);
    if (key === "disabled") node.disabled = !!value;
    if (key === "hidden") node.hidden = !!value;
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.text = (value) => textNode(value);
sandbox.badge = (value, kind, title) => makeNode("span", { class: "badge " + (kind || ""), text: value, title }, []);
sandbox.pill = (value, kind, className) => makeNode("span", { class: "pill " + (kind || "") + " " + (className || ""), text: value }, []);
sandbox.button = (label, onClick, opts) => {
  const node = makeNode("button", { class: (opts && opts.kind) || "", text: label }, []);
  node.onClick = onClick;
  return node;
};
sandbox.section = (title, sub, actions, ...body) => makeNode("section", { class: "section" }, [
  makeNode("h2", { text: title }, []),
  makeNode("p", { class: "section-sub", text: sub }, []),
  actions,
].concat(body));
sandbox.settingsList = (...children) => makeNode("div", { class: "settings-list section-scroll" }, children);

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("// runtime-panel:start");
const end = app.indexOf("// runtime-panel:end");
assert.ok(start >= 0 && end > start, "runtime-panel markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#runtime-panel" });
const runtimePanel = vm.runInContext("runtimePanel", sandbox);

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  else if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function countText(panel, needle) {
  return texts(panel).filter((value) => value === needle).length;
}

const providers = { providers: [
  { id: "emb-local", label: "Local embedder", kind: "embedder", protocol: "openai_embeddings", model: "embeddinggemma-2", runtime: "llama-server" },
  { id: "emb-remote", label: "Remote embedder", kind: "embedder", protocol: "openai_embeddings", model: "gemini-embedding-001", runtime: "" },
] };
const localSettings = { settings: [
  { key: "embed.provider", value: "emb-local" },
  { key: "sidecar.idle_timeout", value: "15m0s" },
] };
const remoteSettings = { settings: [{ key: "embed.provider", value: "emb-remote" }] };
const alwaysOnSettings = { settings: [
  { key: "embed.provider", value: "emb-local" },
  { key: "sidecar.idle_timeout", value: "0s" },
] };
const build = {
  runtime: "llama-server", version: "b11617", variant: "vulkan", gpu: true, auto: true,
  installed: false, size_bytes: 0, download_bytes: 32000000, pins: ["a", "b", "c"],
};
const idle = { builds: [build], runtimes: [] };
const running = {
  builds: [Object.assign({}, build, { installed: true, size_bytes: 33000000 })],
  runtimes: [{ runtime: "llama-server", model: "embeddinggemma-2", variant: "vulkan", state: "ready",
    pid: 4242, threads: 8, restarts: 1, since: "2026-10-27T09:00:00Z" }],
};

const automatic = "Installed, verified and started automatically when embeddings need it. No action required.";

// Idle managed embedder: one engine identity, automatic download, no pickers.
const idlePanel = runtimePanel(idle, providers, localSettings);
const idleText = texts(idlePanel).join(" | ");
assert.equal(all(idlePanel, (node) => node.tag === "select").length, 0, "the engine card still has a select");
assert.equal(countText(idlePanel, "llama-server"), 1, "the engine identity is duplicated");
assert.ok(idleText.includes("b11617"), "the engine version is missing");
assert.ok(idleText.includes("Vulkan"), "the variant badge is missing");
assert.ok(idleText.includes("Vulkan"), "the GPU badge is missing");
assert.ok(idleText.includes(automatic), "the automatic copy is missing");
assert.ok(idleText.includes("Not installed"), "the idle state is not shown");
assert.ok(idleText.includes("embeddinggemma-2"), "the selected embedder model is missing");
assert.ok(idleText.includes("It downloads and verifies automatically on first use"), "the first-use copy is missing");
assert.ok(idleText.includes("It stops after 15 min without a request and starts again on the next use."),
  "the idle stop is not explained");
assert.ok(idleText.includes("32 MB"), "the engine download size is missing");

// A zero idle window is the one setting that keeps the engine running: the
// same note says so instead of promising a stop that never comes.
const alwaysOnText = texts(runtimePanel(idle, providers, alwaysOnSettings)).join(" | ");
assert.ok(alwaysOnText.includes("It stays running between requests because the idle timeout is 0."),
  "a zero idle timeout is not explained");
assert.ok(!alwaysOnText.includes("It stops after"), "a zero idle timeout still promises a stop");
// An unreadable window says nothing rather than guessing.
const unknownIdleText = texts(runtimePanel(idle, providers,
  { settings: [{ key: "embed.provider", value: "emb-local" }, { key: "sidecar.idle_timeout", value: "" }] })).join(" | ");
assert.ok(!unknownIdleText.includes("It stops after"), "an empty idle timeout still promises a stop");
assert.ok(!unknownIdleText.includes("it stays running"), "an empty idle timeout claims the engine stays running");
for (const forbidden of ["CPU", "CUDA", "Install build", "Variant", "Choose a build", "Verify"]) {
  assert.ok(!idleText.includes(forbidden), "the idle card still shows " + forbidden);
}
const idleButtons = all(idlePanel, (node) => node.tag === "button").map((node) => node.text);
assert.deepEqual(idleButtons.sort(), ["Refresh", "Reinstall / Repair"], "the idle card actions changed");
assert.ok(idleButtons.includes("Reinstall / Repair"), "a normal build still offers the install action");
// Refresh must bypass the 15 s /api/sidecar cache, in every build state, or
// a driver installed while the app runs is never noticed.
const idleRefresh = all(idlePanel, (node) => node.tag === "button" && node.text === "Refresh")[0];
assert.equal(typeof idleRefresh.onClick, "function", "Refresh has no handler");
idleRefresh.onClick();
assert.deepEqual(invalidated, ["/api/sidecar"], "Refresh did not invalidate the sidecar status cache");
assert.equal(refreshed, 1, "Refresh did not refresh the route");

// A machine without Vulkan: the card says what is missing, offers no
// download of the 342 MB install and keeps Refresh (which re-probes).
const noVulkanPanel = runtimePanel(
  { builds: [Object.assign({}, build, { unavailable: "vulkan" })], runtimes: [] }, providers, localSettings);
const noVulkanText = texts(noVulkanPanel).join(" | ");
assert.ok(noVulkanText.includes("No usable Vulkan device was found"), "the missing Vulkan device is not explained");
assert.ok(noVulkanText.includes("Unavailable"), "the unavailable state pill is missing");
assert.ok(!noVulkanText.includes("Reinstall / Repair"), "an unavailable build still offers the install action");
assert.ok(!noVulkanText.includes("It downloads and verifies automatically on first use"), "an unavailable build still offers the first-use download");
assert.ok(!noVulkanText.includes("32 MB"), "an unavailable build still quotes the engine size");
assert.deepEqual(all(noVulkanPanel, (node) => node.tag === "button").map((node) => node.text), ["Refresh"],
  "an unavailable build must keep only Refresh");
const probeFailedText = texts(runtimePanel(
  { builds: [Object.assign({}, build, { unavailable: "probe_failed" })], runtimes: [] }, providers, localSettings)).join(" | ");
assert.ok(probeFailedText.includes("The Vulkan device check failed"), "a failed probe is not explained");
assert.ok(!probeFailedText.includes("Reinstall / Repair"), "a failed probe offers an install it cannot complete");
const idleDetails = all(idlePanel, (node) => node.tag === "details");
assert.equal(idleDetails.length, 1, "there is not exactly one Advanced disclosure");
assert.ok(idleDetails[0].attrs.open !== true, "Advanced is open by default");
assert.ok(all(idleDetails[0], (node) => node.tag === "button" && node.text === "Reinstall / Repair").length === 1,
  "Reinstall / Repair is not inside Advanced");
assert.equal(all(idlePanel, (node) => node.tag === "button" && node.text === "Refresh").length, 1,
  "there is not exactly one Refresh");

// The sidecar reports an idle entry before the engine ever ran: the install
// state wins, and a zero since date is not a fact.
const idleRuntime = {
  builds: [build],
  runtimes: [{ runtime: "llama-server", state: "idle", since: "0001-01-01T00:00:00Z" }],
};
const idleRuntimePanel = runtimePanel(idleRuntime, providers, localSettings);
const idleRuntimeText = texts(idleRuntimePanel).join(" | ");
assert.ok(idleRuntimeText.includes("Not installed"), "an idle entry hides a missing install");
assert.ok(!idleRuntimeText.includes("Since"), "a zero since date is shown as a fact");

// Running: the state pill and the live facts, still one engine identity.
const runningPanel = runtimePanel(running, providers, localSettings);
const runningText = texts(runningPanel).join(" | ");
assert.equal(countText(runningPanel, "llama-server"), 1, "the running engine identity is duplicated");
assert.ok(runningText.includes("Ready"), "the ready state pill is missing");
for (const fact of ["4242", "8", "1", "33 MB"]) {
  assert.ok(runningText.includes(fact), "the running card lacks the fact " + fact);
}
assert.ok(!runningText.includes("It downloads and verifies automatically on first use"), "a running engine still offers a download");

// Remote embedder: no local engine claim, no live facts, link to Providers.
const remotePanel = runtimePanel(running, providers, remoteSettings);
const remoteText = texts(remotePanel).join(" | ");
assert.ok(remoteText.includes("Embeddings use Remote embedder, so the local engine is not selected."),
  "the remote embedder is not named");
assert.ok(remoteText.includes("Not selected"), "the remote state pill is missing");
assert.ok(!remoteText.includes("Ready"), "a remote embedder still claims the engine is ready");
assert.ok(!remoteText.includes("4242"), "a remote embedder still shows a PID");
assert.ok(!remoteText.includes("It stops after"), "a remote embedder still explains a local idle stop");
const links = all(remotePanel, (node) => node.tag === "a");
assert.equal(links.length, 1, "the remote card lacks its Providers link");
assert.equal(links[0].attrs.href, "#/settings?tab=providers");
assert.ok(texts(remotePanel).includes("Choose the embedding provider"));

// No embedder setting at all: still says the engine is not selected.
const bareText = texts(runtimePanel(running, providers, { settings: [] })).join(" | ");
assert.ok(bareText.includes("The local engine is not selected for embeddings."), "a missing role is not explained");
assert.ok(!bareText.includes("Ready"), "a missing role still claims the engine is ready");

// A failed provider read is unknown, never "not selected".
const failedText = texts(runtimePanel(running, { providers: [], error: true }, localSettings)).join(" | ");
assert.ok(failedText.includes("The provider list could not be read"), "a failed provider read is silent");
assert.ok(!failedText.includes("not selected"), "a failed provider read claims the engine is not selected");
assert.ok(!failedText.includes("Ready"), "a failed provider read claims the engine is ready");

// A catalog with no build keeps the card honest and offers no install.
const emptyText = texts(runtimePanel({ builds: [], runtimes: [] }, providers, localSettings)).join(" | ");
assert.ok(emptyText.includes("The runtime catalog publishes no engine for this platform."), "an empty catalog is silent");
assert.ok(!emptyText.includes("Reinstall / Repair"), "an empty catalog still offers a repair");

console.log("runtime panel behavior: ok");
`

// Learning leads with plain language and keeps every raw stage and blocker
// code inside Technical details, while the real progress and actions remain.
func TestLearningPlainLanguage(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	stages := []string{"mailboxes", "machine", "sync", "evidence", "embeddings", "labels", "layer1", "calibration", "questions"}
	for _, stage := range stages {
		for _, suffix := range []string{".title", ".hint"} {
			key := "learning.stage." + stage + suffix
			if strings.TrimSpace(en[key].text) == "" || strings.TrimSpace(fr[key].text) == "" {
				t.Errorf("missing plain stage wording %q", key)
			}
		}
	}
	for _, key := range []string{
		"learning.state.open", "learning.state.partial", "learning.state.blocked", "learning.state.learned",
		"learning.title.open", "learning.title.partial", "learning.title.blocked", "learning.title.learned",
		"learning.meaning.open", "learning.meaning.partial", "learning.meaning.blocked", "learning.meaning.learned",
		"learning.certified_count",
		"learning.next.title", "learning.action.answer", "learning.questions.title",
		"learning.technical_stage", "learning.technical_subject",
		"common.technical_details",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "async function renderLearning(")
	end := strings.Index(js, "function stageNode(")
	if start < 0 || end <= start {
		t.Fatal("renderLearning not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`learningTitle(titleValue)`, `learningMeaning(titleValue, certificationParams)`,
		`readCertification()`, `learning.certified_count`,
		`t("learning.next.title")`, `t("learning.action.answer")`,
		`t("learning.action.review"`, `questionTitle(name)`,
		`question: reviewQuestion ? reviewQuestion.question : firstQuestion,`,
		`gatePill(open)`, `showPreview ? previewPill() : null`,
		`permissionKnown && gateOpen()`, `permissionKnown && gatePreview()`, `gateKnown()`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderLearning lacks %q", required)
		}
	}
	for _, forbidden := range []string{"view.open", "view.preview", "view.gate"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("renderLearning derives permission from the learning view via %q", forbidden)
		}
	}
	stageStart := strings.Index(js, "function stageNode(")
	stageEnd := strings.Index(js, "// ---------------------------------------------------------------- sources")
	if stageStart < 0 || stageEnd <= stageStart {
		t.Fatal("stageNode not found")
	}
	stageBody := js[stageStart:stageEnd]
	for _, required := range []string{`stageHint(stage.id)`, `technicalRows(`, `learning.technical_stage`, `common.diagnostic`} {
		if !strings.Contains(stageBody, required) {
			t.Errorf("stageNode lacks %q", required)
		}
	}
	if strings.Contains(stageBody, `class: "stage-note", lang: "en"`) {
		t.Error("stageNode still shows the raw server note outside Technical details")
	}
	blockerStart := strings.Index(js, "function blockerNode(")
	blockerEnd := strings.Index(js, "function section(")
	if blockerStart < 0 || blockerEnd <= blockerStart {
		t.Fatal("blockerNode not found")
	}
	blockerBody := js[blockerStart:blockerEnd]
	if strings.Contains(blockerBody, `class: "blocker-code"`) {
		t.Error("blockerNode still shows the raw code outside Technical details")
	}
	if !strings.Contains(blockerBody, "blockerTechnicalNode(") {
		t.Error("blockerNode has no Technical details disclosure")
	}
	if !strings.Contains(blockerBody, "blockerSubjectText(blocker)") {
		t.Error("blockerNode does not translate a question subject")
	}
}

// Answer history names questions the way Teach does, and its topbar keeps
// only a few meaningful counts.
func TestAnswersHumanQuestionTitles(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "async function renderAnswers(")
	end := strings.Index(js, "function appendAnswerRows(")
	if start < 0 || end <= start {
		t.Fatal("renderAnswers not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`questionTitle(q.name)`, `questionTitle(questionFilter)`,
		`t("answers.header.title")`, `t("answers.header.sub")`,
		`link(t("answers.review_button"), reviewHref, { kind: "primary"`,
		`reviewQuestion ? reviewQuestion.question : top ? top.question : ""`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderAnswers lacks %q", required)
		}
	}
	if strings.Contains(body, `pill(t("answers.meta.needed"`) {
		t.Error("renderAnswers still shows the needed-count topbar pill")
	}
	rowStart := strings.Index(js, "function createAnswersTable(")
	rowEnd := strings.Index(js, "async function loadMoreAnswers(")
	if rowStart < 0 || rowEnd <= rowStart {
		t.Fatal("createAnswersTable not found")
	}
	rowBody := js[rowStart:rowEnd]
	for _, required := range []string{
		`sortableTable({`,
		`answerQuestionLabel(item.question)`,
		`questionTitle(item.question)`,
		`persistKey: "answers.history"`,
		`t("answers.list.caption")`,
	} {
		if !strings.Contains(rowBody, required) {
			t.Errorf("createAnswersTable lacks %q", required)
		}
	}
	if !strings.Contains(body, `t("answers.sort_note")`) {
		t.Error("renderAnswers does not say that sorting applies to loaded answers")
	}
	groupStart := strings.Index(js, "function answerGroupsNode(")
	groupEnd := strings.Index(js, "// ---------------------------------------------------------------- mail body")
	if groupStart < 0 || groupEnd <= groupStart {
		t.Fatal("answerGroupsNode not found")
	}
	groupBody := js[groupStart:groupEnd]
	for _, required := range []string{`questionTitle(name)`, `class: "answer-question-key"`, `questionTypeLabel(question.type)`} {
		if !strings.Contains(groupBody, required) {
			t.Errorf("answerGroupsNode lacks %q", required)
		}
	}
	for _, key := range []string{"answers.header.title", "answers.header.sub"} {
		en := readCatalog(t, "i18n.en.js")
		fr := readCatalog(t, "i18n.fr.js")
		if strings.TrimSpace(en[key].text) == "" || strings.TrimSpace(fr[key].text) == "" {
			t.Errorf("missing answer-history wording %q", key)
		}
	}
}

func TestTeachReviewOrderAndChipChevrons(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const primaryHeading = questionHeading(question);`,
		`primaryHeading.node,`,
		`suggestionCard,`,
		`class: "labels-question labels-question-primary"`,
		`questionForm(question, { heading: false, headingId: primaryHeading.id })`,
		`function questionHeading(q, opts = {})`,
		`labels-chips-nav`,
		`chipPrev.hidden = !hasLeft`,
		`chipNext.hidden = !hasRight`,
		`t("labels.question.scroll_prev")`,
		`t("labels.question.scroll_next")`,
		`function scheduleChipScroll()`,
		`const visible = chipRect.left >= hostRect.left + inset && chipRect.right <= hostRect.right - inset`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks %q", required)
		}
	}
	heading := strings.Index(js, "const primaryHeading = questionHeading(question);")
	card := strings.Index(js, "primaryHeading.node,")
	if heading < 0 || card < 0 || card < heading {
		t.Fatal("the review heading is not placed before the suggestion card")
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".labels-chips-nav", ".labels-question-primary", ".labels-question-body",
		"@media (max-height: 800px)", ".labels-answer-pane { height: 100%; max-height: none; }", ".labels-toolbar-copy { display: none",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}
}

func TestPreviewNeverClaimsACertifiedRoute(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function routePill(")
	if start < 0 {
		t.Fatalf("app.js lacks routePill")
	}
	end := strings.Index(js[start:], "\n  }")
	if end < 0 {
		t.Fatalf("routePill has no end")
	}
	body := js[start : start+end]
	for _, required := range []string{
		"if (!routed)", "if (preview)",
		`t("triage.uncertified_route")`,
		"certification && certification.certified >= certification.total",
		`t("triage.certified_route")`,
		"if (certification)",
		`t("triage.partial_route"`, `t("triage.route_unknown")`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("routePill lacks %q", required)
		}
	}
	previewAt := strings.Index(body, "if (preview)")
	countAt := strings.Index(body, "certification.certified >= certification.total")
	if previewAt < 0 || countAt < 0 || previewAt > countAt {
		t.Fatal("routePill does not let the preview branch win over every count branch")
	}
	if n := strings.Count(js, `t("triage.certified_route")`); n != 1 {
		t.Fatalf("certified_route is rendered outside the one guarded helper (%d occurrences)", n)
	}
	start, end = strings.Index(js, "async function renderRecent"), strings.Index(js, "async function loadMoreTriage")
	if start < 0 || end <= start {
		t.Fatal("renderRecent not found")
	}
	body = js[start:end]
	for _, required := range []string{
		"routePill(view ? view.routed : null, preview, certification)",
		"loadMailDetail(selectedId, detail, { preview", "state.triagePreview",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("the recent-mail screen lacks the preview guard %q", required)
		}
	}
	if strings.Contains(body, `routed ? pill(t("triage.certified_route")`) {
		t.Fatal("the recent-mail topbar claims a certified route without the preview guard")
	}
	runUISandbox(t, "route_pill_certification", routePillCertificationHarness, "route pill certification: ok")
}

// TestSortableTableComponent guards the one sortable-table layer and every
// surface converted to it. The behavioral half runs the real component and
// the real answer-table parser under Node with a deterministic fake DOM.
func TestSortableTableComponent(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		"// sortable:start",
		"// sortable:end",
		"// answer-tables:start",
		"// answer-tables:end",
		"function sortableTable(",
		"function sortableInferType(",
		"function sortableOrder(",

		"function sortableReadPreference(",
		"tabindex: \"0\"",
		"role: \"region\"",
		"scope: \"col\"",
		"class: \"sort-button\"",
		"\"aria-sort\"",
		"mailsense.table-sort.",
		"function askAnswerBlocks(",
		"function askAnswerContentNode(",
		"ask.table.caption",
		"ask.table.truncated",
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the sortable-table step %q", required)
		}
	}
	if n := strings.Count(js, "sortableTable({"); n < 5 {
		t.Errorf("only %d surfaces build a sortable table; want at least 5", n)
	}
	for _, function := range []string{
		"function computedTable(",
		"function companyPeopleSection(",
		"function createAnswersTable(",
		"function errorScreen(",
	} {
		start := strings.Index(js, function)
		if start < 0 {
			t.Errorf("app.js lacks %s", function)
			continue
		}
		end := strings.Index(js[start:], "\n  }")
		if end < 0 {
			t.Errorf("%s has no end", function)
			continue
		}
		body := js[start : start+end]
		if !strings.Contains(body, "sortableTable({") {
			t.Errorf("%s is not wired to the sortable table", function)
		}
	}
	if !strings.Contains(js, "columns: mailboxColumns(),") {
		t.Error("the Sources mailbox table does not use the shared columns")
	}
	if strings.Contains(js, "innerHTML") {
		t.Error("app.js still references innerHTML")
	}
	if !strings.Contains(js, "return askAnswerContentNode(view.answer, view.citations);") {
		t.Error("company prose answers do not use the table-aware answer renderer")
	}
	if !strings.Contains(js, "blocks.push(askAnswerContentNode(view.answer, view.citations));") {
		t.Error("generated answers do not use the table-aware answer renderer")
	}
	if !strings.Contains(js, "appendAnswerRows(table, items)") ||
		!strings.Contains(js, "pageState.table") {
		t.Error("Answers pagination does not re-apply sorting through the table controller")
	}

	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range []string{
		"table.region_aria", "table.sort_by", "table.sort_ascending", "table.sort_descending",
		"sources.table.actions", "sources.mailboxes.caption", "sources.registry.caption",
		"answers.list.caption", "answers.sort_note", "companies.people.caption",
		"error.problems.caption", "ask.table.caption", "ask.table.column", "ask.table.truncated",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		".sort-button", ".sort-indicator", `.data-table th[aria-sort="ascending"]`,
		".table-wrap:focus-visible", ".ask-answer-rich", ".pager-note",
		".companies-people-table",
	} {
		if !strings.Contains(string(css), required) {
			t.Errorf("app.css lacks %q", required)
		}
	}

	docs, err := os.ReadFile(filepath.Join("..", "..", "docs", "UI.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"## Tables", "sortable", "aria-sort"} {
		if !strings.Contains(string(docs), required) {
			t.Errorf("docs/UI.md lacks %q", required)
		}
	}
	if strings.Contains(string(css), ".companies-people-table th {") {
		t.Error("the people table still owns a second header style instead of the .data-table modifier")
	}

	runUISandbox(t, "sortable_tables", sortableTableHarness, "sortable behavior: ok")
}

const sortableTableHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const store = new Map();
const storage = {
  throwGet: false,
  throwSet: false,
  getItem(key) {
    if (this.throwGet) throw new Error("storage unavailable");
    return store.has(key) ? store.get(key) : null;
  },
  setItem(key, value) {
    if (this.throwSet) throw new Error("storage unavailable");
    store.set(key, String(value));
  },
};

const documentStub = {
  documentElement: { lang: "", dataset: {} },
  title: "",
  activeElement: null,
};
const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: documentStub,
  localStorage: storage,
  console,
};
sandbox.window = sandbox;
sandbox.globalThis = sandbox;
vm.createContext(sandbox);

for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.compareText = I.compareText.bind(I);
sandbox.formatNumber = I.formatNumber.bind(I);
sandbox.formatDate = I.formatDate.bind(I);

function makeNode(tag, attrs, children) {
  const node = {
    tag,
    attrs: {},
    children: [],
    text: "",
    textContent: "",
    parentNode: null,
    listeners: {},
    append(...kids) { kids.forEach(add); },
    addEventListener(type, fn) { (node.listeners[type] = node.listeners[type] || []).push(fn); },
    setAttribute(name, value) { node.attrs[name] = String(value); },
    getAttribute(name) { return node.attrs[name]; },
    focus() { documentStub.activeElement = node; },
    click() { for (const fn of node.listeners.click || []) fn({ currentTarget: node }); },
    dispatch(type) { for (const fn of node.listeners[type] || []) fn({ currentTarget: node }); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) {
      if (child.tag === "#fragment") { child.children.slice().forEach(add); return; }
      if (child.parentNode) {
        const previous = child.parentNode.children;
        const at = previous.indexOf(child);
        if (at >= 0) previous.splice(at, 1);
      }
      child.parentNode = node;
      node.children.push(child);
      return;
    }
    node.children.push({ tag: "#text", text: String(child), attrs: {}, children: [], parentNode: node });
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === "class") { node.attrs.class = String(value); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); continue; }
    if (key.startsWith("on") && typeof value === "function") {
      node.addEventListener(key.slice(2).toLowerCase(), value);
      continue;
    }
    node.attrs[key] = value === true ? "" : String(value);
  }
  (children || []).forEach(add);
  return node;
}

sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.text = (value) => ({ tag: "#text", text: String(value == null ? "" : value), attrs: {}, children: [], parentNode: null });
sandbox.fragment = (...children) => {
  const node = makeNode("#fragment", null, []);
  children.forEach((child) => node.append(child));
  return node;
};
sandbox.document.createElement = (tag) => makeNode(tag, null, []);
sandbox.document.createTextNode = (value) => sandbox.text(value);
sandbox.document.createDocumentFragment = () => makeNode("#fragment", null, []);

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
function region(startMarker, endMarker, label) {
  const start = app.indexOf(startMarker);
  const end = app.indexOf(endMarker, start);
  assert.ok(start >= 0 && end > start, label + " markers are missing");
  return app.slice(start, end);
}
vm.runInContext(region("// sortable:start", "// sortable:end", "sortable"), sandbox, { filename: "app.js#sortable" });

const citationCalls = [];
sandbox.askAnswerTextNode = (value, citations) => {
  citationCalls.push({ value: String(value), citations });
  return makeNode("span", { class: "citation-text", text: String(value) }, []);
};
vm.runInContext(region("// answer-tables:start", "// answer-tables:end", "answer-tables"), sandbox, { filename: "app.js#answer-tables" });

function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}
function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function equalJSON(actual, expected, message) {
  assert.equal(JSON.stringify(actual), JSON.stringify(expected), message);
}
function dataRows(tbody) {
  return (tbody.children || []).filter((row) => row.tag === "tr" && !String(row.attrs.class || "").includes("detail"));
}
function headerFor(wrapper, label) {
  const ths = all(wrapper, (node) => node.tag === "th");
  return ths.find((th) => texts(th).join(" ").includes(label));
}

// ---------------------------------------------------------------- pure types

const infer = (values) => sandbox.sortableInferType(
  { key: "value", type: "auto", value: (row) => row.value },
  values.map((value) => ({ value })),
);
assert.equal(infer(["1", "2", "10"]), "number");
assert.equal(infer(["2026-10-27", "2025-12-01T10:00:00Z"]), "date");
assert.equal(infer(["2026-10", "2025-12"]), "date");
assert.equal(infer(["2026-W02", "2025-W52"]), "date");
assert.equal(infer(["monday", "friday"]), "date");
assert.equal(infer(["A@example.com", "b@example.org"]), "email");
assert.equal(infer(["+33 6 12 34 56 78", "06 11 22 33 44"]), "phone");
assert.equal(infer(["", "  ", "2"]), "number");
assert.equal(infer(["2", "alpha"]), "text");
assert.equal(sandbox.sortableInferType({ key: "x", type: "text", value: (row) => row.x }, [{ x: 2 }]), "text");

assert.equal(sandbox.sortableNumberValue("1 234,5"), 1234.5);
assert.equal(sandbox.sortableNumberValue("1.234,5"), 1234.5);
assert.equal(sandbox.sortableNumberValue("1,234.5"), 1234.5);
assert.equal(sandbox.sortableNumberValue("1.234"), 1234);
assert.equal(sandbox.sortableNumberValue("0,125"), 0.125);
assert.equal(sandbox.sortableNumberValue("2,5"), 2.5);
assert.equal(sandbox.sortableNumberValue("no"), null);

assert.equal(sandbox.compareText("Élodie", "Elodie"), 0);
assert.ok(sandbox.compareText("2", "10") < 0);
I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(sandbox.compareText("Élodie", "Elodie"), 0);
assert.ok(sandbox.compareText("2", "10") < 0);
I.setLanguage({ language: "en", formatLocale: "en-US" });
assert.equal(sandbox.sortablePhoneKey("12345"), null);
assert.equal(sandbox.sortablePhoneKey("+33 6 12 34 56 78"), "33612345678");
assert.equal(sandbox.sortableDateInstant("2026-10-27"), Date.UTC(2026, 9, 27));
assert.equal(sandbox.sortableDateInstant("2026-10"), Date.UTC(2026, 9, 1));
assert.ok(sandbox.sortableDateInstant("2025-W52") < sandbox.sortableDateInstant("2026-W02"));
assert.ok(sandbox.sortableDateInstant("monday") < sandbox.sortableDateInstant("friday"));
assert.ok(sandbox.sortableDateInstant("2026-10-27T11:00:00+02:00") > sandbox.sortableDateInstant("2026-10-27T08:30:00Z"));

const textColumn = { key: "value", type: "text", value: (row) => row.value };
const tieRows = [
  { value: "b", id: 1 },
  { value: null, id: 2 },
  { value: "a", id: 3 },
  { value: "b", id: 4 },
  { value: "", id: 5 },
];
equalJSON(sandbox.sortableOrder(tieRows, textColumn, "asc"), [2, 0, 3, 1, 4]);
equalJSON(sandbox.sortableOrder(tieRows, textColumn, "desc"), [0, 3, 2, 1, 4]);

const numberRows = [{ n: 10 }, { n: 2 }, { n: 2 }];
equalJSON(sandbox.sortableOrder(numberRows, { key: "n", type: "number", value: (row) => row.n }, "asc"), [1, 2, 0]);

equalJSON(
  sandbox.sortableOrder(
    [{ email: "B@example.com" }, { email: "a@example.com" }],
    { key: "email", type: "email", value: (row) => row.email },
    "asc",
  ),
  [1, 0],
);
equalJSON(
  sandbox.sortableOrder(
    [{ phone: "+33 6 12 34 56 78" }, { phone: "06 11 22 33 44" }],
    { key: "phone", type: "phone", value: (row) => row.phone },
    "asc",
  ),
  [1, 0],
);

// ---------------------------------------------------------------- DOM contract

function peopleTable(options = {}) {
  const columns = [
    {
      key: "name",
      label: "Name",
      type: "text",
      value: (row) => row.name,
      render: (row) => makeNode("span", { class: "name", text: row.name }, []),
    },
    {
      key: "rank",
      label: "Rank",
      type: "number",
      align: "num",
      sortable: false,
      render: (row) => String(row.rank),
    },
    {
      key: "pick",
      label: "Pick",
      type: "text",
      sortable: false,
      render: () => makeNode("input", { type: "checkbox" }, []),
    },
  ];
  return sandbox.sortableTable({
    caption: "People",
    columns,
    rows: [
      { name: "B", rank: 1 , detail: "first detail" },
      { name: "A", rank: 2, detail: "second detail" },
      { name: "C", rank: 3, detail: "third detail" },
    ],
    detail: (row) => makeNode("tr", { class: "detail" }, [makeNode("td", { colspan: "2", text: row.detail }, [])]),
    ...options,
  });
}

const controller = peopleTable();
const wrapper = controller.node;
assert.equal(wrapper.tag, "div");
assert.equal(wrapper.attrs.role, "region");
assert.equal(wrapper.attrs.tabindex, "0");
assert.equal(wrapper.attrs["aria-label"], "Table: People");
const table = all(wrapper, (node) => node.tag === "table")[0];
assert.ok(table, "table missing");
const caption = all(table, (node) => node.tag === "caption")[0];
assert.ok(caption && texts(caption).join("") === "People", "caption missing");
for (const th of all(table, (node) => node.tag === "th")) {
  assert.equal(th.attrs.scope, "col");
}
let nameHeader = headerFor(wrapper, "Name");
let rankHeader = headerFor(wrapper, "Rank");
assert.equal(nameHeader.attrs["aria-sort"], "none");
assert.equal(rankHeader.attrs["aria-sort"], undefined, "rank column is sortable");
const nameButton = all(nameHeader, (node) => node.tag === "button" && node.attrs.class === "sort-button")[0];
const rankButton = all(rankHeader, (node) => node.tag === "button")[0];
assert.ok(nameButton, "sort button missing");
assert.equal(rankButton, undefined, "rank column has a sort button");
assert.equal(nameButton.attrs.type, "button");
const indicator = all(nameButton, (node) => node.attrs["aria-hidden"] === "true");
assert.ok(indicator.length, "sort indicator is not hidden from assistive technology");

const tbody = all(wrapper, (node) => node.tag === "tbody")[0];
equalJSON(dataRows(tbody).map((row) => texts(row)[0]), ["B", "A", "C"]);
assert.equal(tbody.children.length, 6, "detail rows were not attached");
const originalRows = dataRows(tbody).slice();

nameButton.focus();
assert.equal(documentStub.activeElement, nameButton);
nameButton.click();
assert.equal(documentStub.activeElement, nameButton, "sorting moved focus");
const pickBoxes = () => all(tbody, (node) => node.tag === "input");
const pick = pickBoxes()[0];
let pickChanges = 0;
pick.addEventListener("change", () => { pickChanges += 1; });
pick.checked = true;
nameHeader = headerFor(wrapper, "Name");
assert.equal(nameHeader.attrs["aria-sort"], "ascending");
assert.equal(nameButton.attrs["aria-label"], "Sort by Name, ascending");
assert.equal(rankHeader.attrs["aria-sort"], undefined);
equalJSON(dataRows(tbody).map((row) => texts(row)[0]), ["A", "B", "C"]);
assert.ok(pickBoxes().includes(pick), "the checkbox node was rebuilt");
assert.equal(pick.checked, true, "the checkbox state was lost");
pick.checked = false;
pick.dispatch("change");
assert.equal(pickChanges, 1, "the row listener was lost");
assert.equal(dataRows(tbody)[0], originalRows[1], "row nodes were rebuilt instead of moved");
equalJSON(
  tbody.children.filter((row) => String(row.attrs.class || "").includes("detail")).map((row) => texts(row).join("")),
  ["second detail", "first detail", "third detail"],
);
nameButton.click();
assert.equal(nameHeader.attrs["aria-sort"], "descending");
assert.equal(nameButton.attrs["aria-label"], "Sort by Name, descending");
equalJSON(dataRows(tbody).map((row) => texts(row)[0]), ["C", "B", "A"]);

controller.append([{ name: "0", rank: 4, detail: "new detail" }]);
equalJSON(dataRows(tbody).map((row) => texts(row)[0]), ["C", "B", "A", "0"]);
assert.equal(tbody.children.length, 8, "appended detail row missing");

const persisted = peopleTable({ persistKey: "people" });
const pName = headerFor(persisted.node, "Name");
const pButton = all(pName, (node) => node.tag === "button")[0];
pButton.click();
assert.equal(storage.getItem("mailsense.table-sort.people"), JSON.stringify({ key: "name", direction: "asc" }));
const restored = peopleTable({ persistKey: "people" });
assert.equal(headerFor(restored.node, "Name").attrs["aria-sort"], "ascending");
equalJSON(dataRows(all(restored.node, (node) => node.tag === "tbody")[0]).map((row) => texts(row)[0]), ["A", "B", "C"]);

storage.setItem("mailsense.table-sort.corrupt", "{not json");
const corrupt = peopleTable({ persistKey: "corrupt" });
assert.equal(corrupt.state(), null);
assert.equal(headerFor(corrupt.node, "Name").attrs["aria-sort"], "none");

storage.throwGet = true;
storage.throwSet = true;
const unavailable = peopleTable({ persistKey: "unavailable" });
const unavailableButton = all(headerFor(unavailable.node, "Name"), (node) => node.tag === "button")[0];
unavailableButton.click();
assert.equal(headerFor(unavailable.node, "Name").attrs["aria-sort"], "ascending");
storage.throwGet = false;
storage.throwSet = false;

// ---------------------------------------------------------------- markdown

const basic = "Intro [1]\n\n| Name | Count |\n| --- | ---: |\n| Alpha | 10 |\n| Beta | 2 |\n\nAfter [2]";
const basicBlocks = sandbox.askAnswerBlocks(basic);
equalJSON(basicBlocks.map((block) => block.type), ["text", "table", "text"]);
const basicTable = basicBlocks[1];
equalJSON(basicTable.header, ["Name", "Count"]);
equalJSON(basicTable.rows, [["Alpha", "10"], ["Beta", "2"]]);
assert.equal(basicTable.truncated, 0);

equalJSON(
  sandbox.askAnswerBlocks("| A \\| B | C |\n| --- | --- |\n| x | y |")[0].header,
  ["A | B", "C"],
);
assert.equal(sandbox.askAnswerBlocks("\u0060\u0060\u0060\n| A | B |\n| --- | --- |\n| 1 | 2 |\n\u0060\u0060\u0060").some((block) => block.type === "table"), false);
const malformedTable = sandbox.askAnswerBlocks("| A | B |\n| no | separator |\n| 1 | 2 |");
assert.equal(malformedTable.some((block) => block.type === "table"), false);
assert.ok(malformedTable.map((block) => block.text || "").join("\n").includes("| 1 | 2 |"), "malformed table content was dropped");
assert.equal(sandbox.askAnswerBlocks("| A | B |\n| --- | --- |\n| only one |").some((block) => block.type === "table"), false);

const wideHeader = "| " + Array.from({ length: 13 }, (_, index) => "c" + index).join(" | ") + " |";
const wideSeparator = "| " + Array.from({ length: 13 }, () => "---").join(" | ") + " |";
assert.equal(sandbox.askAnswerBlocks(wideHeader + "\n" + wideSeparator + "\n" + wideHeader).some((block) => block.type === "table"), false);

const manyRows = ["| A | B |", "| --- | --- |"];
for (let index = 0; index < 201; index += 1) manyRows.push("| r" + index + " | " + index + " |");
const truncatedBlocks = sandbox.askAnswerBlocks(manyRows.join("\n"));
const truncatedTable = truncatedBlocks.find((block) => block.type === "table");
assert.equal(truncatedTable.rows.length, 200);
assert.equal(truncatedTable.truncated, 1);
assert.ok(truncatedBlocks.some((block) => block.type === "text" && block.text.includes("r200")), "overflow row was dropped");

const rich = sandbox.askAnswerContentNode(basic, [{ id: 1 }]);
assert.equal(rich.tag, "div");
assert.ok(String(rich.attrs.class).includes("ask-answer-rich"));
assert.ok(all(rich, (node) => node.tag === "table").length === 1, "answer table missing");
assert.ok(texts(rich).includes("Alpha"));
assert.ok(texts(rich).join(" ").includes("After [2]"));
const plain = sandbox.askAnswerContentNode("No table here [1]", [{ id: 1 }]);
assert.equal(plain.tag, "p");
assert.ok(String(plain.attrs.class).includes("ask-answer-text"));

const answerTableNode = sandbox.askAnswerTableNode(basicTable, [{ id: 1 }]);
const answerWrapper = all(answerTableNode, (node) => node.tag === "div" && String(node.attrs.class || "").includes("table-wrap"))[0];
const countHeader = headerFor(answerWrapper, "Count");
const countButton = all(countHeader, (node) => node.tag === "button")[0];
countButton.click();
const answerBody = all(answerWrapper, (node) => node.tag === "tbody")[0];
equalJSON(dataRows(answerBody).map((row) => texts(row)[0]), ["Beta", "Alpha"]);

const htmlCell = "<img src=x onerror=alert(1)>";
const htmlContent = sandbox.askAnswerContentNode("| X |\n| --- |\n| " + htmlCell + " |", []);
assert.ok(texts(htmlContent).join("").includes(htmlCell), "cell content was interpreted or dropped");
assert.ok(all(htmlContent, (node) => Object.prototype.hasOwnProperty.call(node, "innerHTML")).length === 0);
assert.ok(citationCalls.some((call) => call.value.includes("[1]") && Array.isArray(call.citations)), "citations did not reach the text renderer");

const truncatedNode = sandbox.askAnswerTableNode(truncatedTable, []);
assert.ok(texts(truncatedNode).join(" ").includes("Showing the first 200 rows."));

// ---------------------------------------------------------------- answer keys

const companyOrder = sandbox.sortableOrder(
  [
    { last_at: "2026-10-20T09:00:00Z", name: "old" },
    { last_at: "2026-10-27T11:30:00+02:00", name: "new" },
    { last_at: "2026-10-21T09:00:00Z", name: "middle" },
  ],
  { key: "last_at", type: "date", value: (row) => row.last_at },
  "desc",
);
equalJSON(companyOrder, [1, 2, 0]);

const timeOrder = (keys) => sandbox.sortableOrder(
  keys.map((key) => ({ key })),
  { key: "key", type: "date", value: (row) => row.key },
  "asc",
);
equalJSON(timeOrder(["2026-W02", "2025-W52"]), [1, 0]);
equalJSON(timeOrder(["2026-10", "2025-12"]), [1, 0]);
equalJSON(timeOrder(["friday", "monday", "sunday"]), [1, 0, 2]);

console.log("sortable behavior: ok");
`

// The All mail screen paints one real batch before the deferred rows. The
// initial batch, the deferred rest and Load more share one row builder; the
// route cache may still hold the captured list while the rest is appended.
func TestAllMailProgressiveFirstPaint(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`const ALL_MAIL_INITIAL_ROWS = 24;`,
		`const pendingAllMailRows = new WeakSet();`,
		// A cached route restores the model its own node was rendered with,
		// and an older load-more request can never mutate that restored model.
		`function restoreCachedAllMail(route, seq, entry) {`,
		`const snapshot = list ? list.__allMailModel : null;`,
		`if (!list || !more || !snapshot || !snapshot.loaded || snapshot.role !== role || snapshot.sender !== sender || snapshot.unread !== unread) return false;`,
		`unread: snapshot.unread,`,
		`if (state.allMail !== model || !list.isConnected || model.role !== role || model.sender !== sender || model.unread !== unread) return;`,
		`allMail: { role: "", sender: "", unread: false, rows: [], total: 0, counts: null, next: "", hasMore: false, capped: false, seen: new Set(), loaded: false, promise: null, loading: false, morePromise: null },`,
		// A bare Mail All prefetch never builds the default destination while
		// an All-Mail list is mounted: that render would replace state.allMail
		// and disconnect the mounted list's one observer.
		`if (route.name === "mail" && parseMode(route) === "all" &&`,
		`state.route === "mail" && parseMode(parseRoute()) === "all") return false;`,
		// The one-line initial model carries the transient promise too (the
		// block check below covers the restore and filter-change objects).
		`morePromise: null },`,
		`if (error) workspace.dataset.errorScreen = "1";`,
		`if (content && content.dataset && content.dataset.errorScreen) screen.dataset.errorScreen = "1";`,
	} {
		if !strings.Contains(js, required) {
			t.Errorf("app.js lacks the progressive first-paint constant %q", required)
		}
	}
	block := markedSource(t, js, "all-mail")
	for _, required := range []string{
		`function allMailSnapshot(model) {`,
		`function allMailUnread(value) {`,
		`model = state.allMail = {`,
		`if (state.allMail !== model || model.role !== requested || model.sender !== requestedSender || model.unread !== requestedUnread) return;`,
		`if (state.allMail.role !== role || state.allMail.sender !== sender || state.allMail.unread !== unread || state.allMail.capped || !state.allMail.hasMore || !more || more.hidden || typeof IntersectionObserver !== "function") return;`,
		`query.set("unread", "true");`,
		`unread: model.unread,`,
		`t("mail.filter.unread")`,
		`t("mail.filter.unread_clear")`,
		`t("mail.all.empty_unread_title")`,
		`h("div", { class: "filter-chip-group", role: "group", "aria-label": t("mail.role_aria") },`,
		`h("div", { class: "filter-chip-group", role: "group", "aria-label": t("mail.filter.state_aria") },`,
		`const unread = allMailUnread(route && route.params ? route.params.get("unread") : "");`,
		`if (model.role !== role || model.sender !== sender || model.unread !== unread) {`,
		`function allMailRowsFragment(rows, role, selectedId, seen) {`,
		`return { fragment, added: accepted.length, rows: accepted };`,
		`function appendAllMailRows(list, rows, more, role, selectedId, seen) {`,
		`appendAllMailRows(list, model.rows.slice(0, ALL_MAIL_INITIAL_ROWS), more, role, selectedId, seen);`,
		`appendAllMailRows(list, rest, more, role, 0, seen);`,
		`const result = appendAllMailRows(list, pageRows, more, role, 0, model.seen);`,
		`model.rows = model.rows.concat(result.rows);`,
		`model.hasMore = !!view.has_more && result.added > 0;`,
		`model.capped = model.rows.length >= ALL_MAIL_MAX_ROWS && (model.hasMore || rows.length > result.added);`,
		`t("mail.all.capped", { shown: formatNumber(model.rows.length), total: formatNumber(model.total) })`,
		`more: hasRows && !model.loading && model.hasMore && !capped,`,
		`model.seen = allMailSeenRows(model.rows);`,
		`const seen = new Set();`,
		`list.__allMailModel = allMailSnapshot(model);`,
		// A refresh that arrives during Load more waits for the same page
		// instead of rebuilding the list from the stale model. The owner
		// clears the transient promise in one finally that always settles.
		`morePromise: null,`,
		`model.morePromise = pending;`,
		`if (model.morePromise === pending) model.morePromise = null;`,
		`if (state.allMail === model && more && more.isConnected) {`,
		`while (model.loading && model.morePromise && state.allMail === model) {`,
		`if (model.morePromise === pending && model.loading) break;`,
		`window.requestAnimationFrame(() => window.requestAnimationFrame(finish));`,
		`window.setTimeout(finish, 2000);`,
		`if (more.parentNode === list) {`,
		`if (pendingAllMailRows.has(list)) return;`,
		`pendingAllMailRows.add(list);`,
		`pendingAllMailRows.delete(list);`,
		`syncMailListSelection(current);`,
	} {
		if !strings.Contains(block, required) {
			t.Errorf("the all-mail screen lacks the progressive first-paint step %q", required)
		}
	}
	// One row builder for the first batch, the deferred rest and Load more:
	// a second call site drifts selection, dedupe or row markup.
	if got := strings.Count(block, "allMailRowNode(row, {"); got != 1 {
		t.Fatalf("the all-mail screen has %d allMailRowNode(row, {) call sites, want 1", got)
	}
}

// The All-Mail list is capped before it is appended and the cap notice is a
// pager state, not a new control. The id set and the selected row live on
// state: a row click and a page append must not scan the mounted list.
func TestAllMailCapsAndTracksState(t *testing.T) {
	mail := readUIModule(t, "32-mail.js")
	for _, want := range []string{
		`const ALL_MAIL_MAX_ROWS = 300;`,
		`const budget = Math.max(0, ALL_MAIL_MAX_ROWS - model.rows.length);`,
		`model.capped = model.rows.length >= ALL_MAIL_MAX_ROWS && (model.hasMore || rows.length > result.added);`,
		`t("mail.all.capped", { shown: formatNumber(model.rows.length), total: formatNumber(model.total) })`,
		`more: hasRows && !model.loading && model.hasMore && !capped,`,
		`model.seen = allMailSeenRows(model.rows);`,
		`const result = appendAllMailRows(list, pageRows, more, role, 0, model.seen);`,
		`let selectedMailRow = null;`,
		`const row = selectedMailRow;`,
		`row.removeAttribute("aria-current");`,
	} {
		if !strings.Contains(mail, want) {
			t.Errorf("32-mail.js lacks the bounded-list step %q", want)
		}
	}
	if strings.Contains(functionBody(t, mail, "function syncMailListSelection("), "querySelectorAll") {
		t.Fatal("a row click still scans every mounted row")
	}
	if strings.Contains(functionBody(t, mail, "async function loadMoreAllMail("), `querySelectorAll("[data-message-id]")`) {
		t.Fatal("a page append still rebuilds the seen set from the DOM")
	}
	for _, catalog := range []string{"i18n.en.js", "i18n.fr.js"} {
		entry, ok := readCatalog(t, catalog)["mail.all.capped"]
		if !ok || entry.isPlural || strings.TrimSpace(entry.text) == "" {
			t.Fatalf("%s has no singular mail.all.capped", catalog)
		}
		for _, placeholder := range []string{"{shown}", "{total}"} {
			if !strings.Contains(entry.text, placeholder) {
				t.Errorf("%s: mail.all.capped lacks %s", catalog, placeholder)
			}
		}
	}
}

// Boot paints after status, language and the gate. Learning and the job strip
// update the chrome afterwards, and the first route mount consumes the gate
// snapshot the boot read just produced instead of sending /api/gate twice.
func TestBootGateSnapshotIsReusedOnce(t *testing.T) {
	init := readUIModule(t, "52-init.js")
	for _, want := range []string{
		`const [, language, gate] = await Promise.all([loadStatus(), languageReady, loadGate()]);`,
		`loadJobs().catch(() => null);`,
		`gateBootSnapshot = gateValue && state.gate && state.gate.known ? state.gate : null;`,
		`await renderRoute();`,
		`gateBootSnapshot = null;`,
	} {
		if !strings.Contains(init, want) {
			t.Errorf("52-init.js lacks the first-paint step %q", want)
		}
	}
	start := strings.Index(init, "const [, language, gate] = await Promise.all")
	if start < 0 {
		t.Fatal("the boot startup batch was not found")
	}
	line := init[start : start+strings.Index(init[start:], "\n")]
	if strings.Contains(line, "learningReady") || strings.Contains(line, "loadJobs") {
		t.Fatalf("the first paint still waits on chrome reads: %s", line)
	}
	renderAt := strings.Index(init, "await renderRoute();")
	zoneAt := strings.Index(init, "learningReady.then(() => {")
	if renderAt < 0 || zoneAt < 0 || zoneAt < renderAt {
		t.Fatal("the zone auto-confirm runs before the first content paint")
	}
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`let gateBootSnapshot = null;`,
		`if (gateBootSnapshot) {`,
		`if (gateInflight) return gateInflight;`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks the boot gate step %q", want)
		}
	}
}

// A finished sync rewrites search hits as well as the lists they came from.
func TestSyncInvalidatesSearch(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	if !containsString(jsConstList(t, shell, "SYNC_FINISHED_READS"), "/api/search") {
		t.Error("the sync read set does not drop /api/search")
	}
	if !containsString(jsStringArrayField(t, topicBody(t, shell, "mail"), "routes"), "search") {
		t.Error("the sync surface set does not stale Search")
	}
}

// The read cache is bounded by bytes, evicts the least recently used entry,
// touches a hit, drops expired entries, refuses a body_css too large to keep
// beside its message, and deletes only the in-flight reads a prefix names.
// The harness scales the byte budget down so eviction is observable with a
// few fixtures; production uses 24 MiB with an 8 MiB per-entry ceiling.
func TestReadCacheKeepsUnrelatedFlightsAndBoundsBytes(t *testing.T) {
	runUISandbox(t, "read_cache", readCacheHarness, "read cache behavior: ok")
}

const readCacheHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  MAX_API_CACHE: 240,
  MAX_API_CACHE_BYTES: 5000,
  MAX_API_CACHE_ENTRY_BYTES: 4000,
  MAX_CACHED_BODY_CSS: 64,
  apiCache: new Map(),
  apiInflight: new Map(),
  apiEpoch: 0,
  apiCacheBytes: 0,
  CACHE_TTL: { companies: 1000 },
  state: { conversationPages: new Map(), conversationLoads: new Map() },
  companyProfileWarm: new Map(),
  forgetPriorityPages() {},
  forgetTriagePage() {},
};
const calls = [];
const pending = new Map();
sandbox.request = (requestPath) => new Promise((resolve, reject) => {
  calls.push(requestPath);
  if (!pending.has(requestPath)) pending.set(requestPath, []);
  pending.get(requestPath).push({ resolve, reject });
});
vm.createContext(sandbox);
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("// read-cache:start");
const end = app.indexOf("// read-cache:end");
assert.ok(start >= 0 && end > start, "read-cache markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#read-cache" });
const run = (code) => vm.runInContext(code, sandbox);
const requestCached = run("requestCached");
const cached = run("cached");
const primeCache = run("primeCache");
const invalidateCache = run("invalidateCache");
const trimAPICache = run("trimAPICache");

const countOf = (prefix) => calls.filter((entry) => entry === prefix || entry.startsWith(prefix)).length;
const settle = (requestPath, value) => {
  for (const entry of pending.get(requestPath) || []) entry.resolve(value);
};

(async () => {
// A sync that names /api/messages must not drop the unrelated Hub flight.
const hub = requestCached("/api/hub");
const hubJoined = requestCached("/api/hub");
assert.equal(countOf("/api/hub"), 1, "the first Hub read was not shared");
const messages = requestCached("/api/messages?limit=100");
assert.equal(countOf("/api/messages"), 1, "the first message read was not shared");
invalidateCache(["/api/messages?"]);
assert.equal(run("apiInflight.has('GET /api/hub')"), true, "the sync dropped an unrelated flight");
assert.equal(run("apiInflight.has('GET /api/messages?limit=100')"), false, "the sync kept an invalidated flight");
const hubAfter = requestCached("/api/hub");
assert.equal(countOf("/api/hub"), 1, "an unrelated flight was restarted after the sync");
const messagesAfter = requestCached("/api/messages?limit=100");
assert.equal(countOf("/api/messages"), 2, "an invalidated flight was reused");
settle("/api/hub", { value: "hub" });
settle("/api/messages?limit=100", { value: "messages" });
assert.deepEqual(await hub, { value: "hub" });
assert.deepEqual(await hubJoined, { value: "hub" });
assert.deepEqual(await hubAfter, { value: "hub" });
assert.deepEqual(await messages, { value: "messages" });
assert.deepEqual(await messagesAfter, { value: "messages" });
// A read that started before an invalidation never repopulates the cache;
// a later read under the new epoch does.
const hubFresh = requestCached("/api/hub", {}, 100000);
settle("/api/hub", { value: "fresh" });
assert.deepEqual(await hubFresh, { value: "fresh" });
assert.deepEqual(cached("/api/hub"), { value: "fresh" });

// Byte budget: three 1200-character bodies exceed 5000 bytes, so the least
// recently used entry is evicted and the total stays inside the budget.
primeCache("/api/a", { body: "a".repeat(1200) }, 100000);
primeCache("/api/b", { body: "b".repeat(1200) }, 100000);
assert.deepEqual(cached("/api/a"), { body: "a".repeat(1200) }, "a hit lost its value");
primeCache("/api/c", { body: "c".repeat(1200) }, 100000);
assert.ok(run("apiCacheBytes") <= 5000, "the cache exceeded its byte budget");
assert.deepEqual(cached("/api/a"), { body: "a".repeat(1200) }, "the hit was evicted instead of the LRU entry");
assert.equal(cached("/api/b"), undefined, "the least recently used entry survived");
assert.deepEqual(cached("/api/c"), { body: "c".repeat(1200) });

// Expired entries are swept even when nothing reads them again.
primeCache("/api/d", { body: "d" }, 10);
run("apiCache.get('GET /api/d').at = Date.now() - 100");
trimAPICache();
assert.equal(run("apiCache.has('GET /api/d')"), false, "an expired entry lingered");

// A sender sheet that is too large to keep beside its message is excluded;
// a single response over the per-entry cap is not cached either.
primeCache("/api/messages/1", { body_css: "c".repeat(65), body: "x" }, 100000);
assert.equal(cached("/api/messages/1"), undefined, "a huge body_css was cached");
primeCache("/api/messages/2", { body: "x".repeat(5000) }, 100000);
assert.equal(cached("/api/messages/2"), undefined, "an oversized response was cached");

invalidateCache([]);
assert.equal(run("apiCache.size"), 0, "the full clear left entries");
assert.equal(run("apiCacheBytes"), 0, "the full clear left bytes behind");

console.log("read cache behavior: ok");
})().catch((error) => {
  console.error(error && error.stack ? error.stack : String(error));
  process.exitCode = 1;
});
`

// The shell must stay navigable and recoverable when a read fails or the
// window is narrow. These are source-level guards for behavior the Playwright
// suite exercises in a real Chromium with the real CSP.
func TestNavigationRecoveryAndCSPWiring(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		// A style declaration goes through CSSOM, never a CSP-blocked
		// style attribute.
		`else if (key === "style")`,
		`node.style.cssText = String(value);`,
		// Error screens retry by replacing the mounted screen, and are never
		// cached as a valid destination.
		`node = errorScreen(error, () => refreshActiveRoute());`,
		`function isErrorScreen(node)`,
		`function routeFailed(node)`,
		`if (!routeFailed(node)) {`,
		// A six-row Home page must not be painted as the Important page.
		`if (view && view.limit < PRIORITY_PAGE_SIZE) view = null;`,
		`loadPriority("all", PRIORITY_HOME_SIZE)`,
		// A successful mailbox pick re-enables the control in place.
		`if (input.isConnected) input.disabled = false;`,
		// A prefetch that lost a race retries instead of being marked done
		// forever; the unsafe Mail destination is no longer scheduled at boot.
		`if (!cached) navPrefetchLinks.delete(href);`,
		// Every filter's count arrives with the page: the chips are not
		// re-read as six single-row requests.
		`if (view.counts && typeof view.counts === "object")`,
		// Progress bursts coalesce to one strip paint per frame.
		`function scheduleJobPaint() {`,
		`let jobPaintQueued = false;`,
		`scheduleJobPaint();`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("app.js lacks the navigation/recovery step %q", required)
		}
	}
	for _, forbidden := range []string{
		`node = errorScreen(error, () => renderRoute());`,
		`button(t("common.try_again"), () => renderRoute()`,
	} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("app.js still has an inert retry: %q", forbidden)
		}
	}

	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	mobile := string(css)
	block := mobile[strings.Index(mobile, "@media (max-width: 760px) {"):]
	block = block[:strings.Index(block, "@media (max-width: 600px) {")]
	for _, required := range []string{
		`.sidebar {`, `display: flex;`, `grid-template-rows: auto minmax(0, 1fr);`,
		`.nav-toggle`, `transform: translateX(-100%);`, `.sidebar.drawer-open .nav`,
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("the 760px shell rule lacks %q", required)
		}
	}
	if strings.Contains(block, `.sidebar { display: none; }`) {
		t.Fatal("the narrow shell still removes the only navigation")
	}
}

// The shell names every asset with its current content version, and one meta
// tag carries the versions the runtime needs for a lazily loaded catalog.
// No consumer sees a placeholder or hard-codes a fingerprint.
func TestAssetsFingerprintTheShell(t *testing.T) {
	rec := request(t, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "{{asset") {
		t.Fatal("the served shell still carries an asset placeholder")
	}
	match := regexp.MustCompile(`name="mailsense-assets" content='([^']+)'`).FindStringSubmatch(body)
	if match == nil {
		t.Fatal("the shell carries no asset version map")
	}
	var versions map[string]string
	if err := json.Unmarshal([]byte(match[1]), &versions); err != nil {
		t.Fatalf("asset version map: %v", err)
	}
	for _, name := range []string{"app.js", "app.css", "i18n.en.js", "i18n.js", "i18n.fr.js", "favicon.svg"} {
		version := versions[name]
		if version == "" {
			t.Fatalf("the version map lacks %q", name)
		}
		if name != "i18n.fr.js" && !strings.Contains(body, "/"+name+"?v="+version) {
			t.Errorf("the shell does not reference %s at its current version", name)
		}
	}
	if _, ok := versions["index.html"]; ok {
		t.Fatal("the document fingerprinted itself")
	}
}

// A versioned URL names exactly the bytes this binary serves, so it can be
// immutable; an unversioned or stale URL must keep revalidating.
func TestFingerprintedAssetsAreImmutable(t *testing.T) {
	rec := request(t, http.MethodGet, "/", nil)
	match := regexp.MustCompile(`name="mailsense-assets" content='([^']+)'`).FindStringSubmatch(rec.Body.String())
	if match == nil {
		t.Fatal("the shell carries no asset version map")
	}
	var versions map[string]string
	if err := json.Unmarshal([]byte(match[1]), &versions); err != nil {
		t.Fatal(err)
	}
	version := versions["app.js"]
	plain := request(t, http.MethodGet, "/app.js", nil)
	versioned := request(t, http.MethodGet, "/app.js?v="+version, nil)
	if got := versioned.Header().Get("Cache-Control"); got != immutableCache {
		t.Fatalf("versioned cache %q, want %q", got, immutableCache)
	}
	if got, want := versioned.Header().Get("ETag"), plain.Header().Get("ETag"); got != want {
		t.Fatalf("versioned ETag %q, want %q", got, want)
	}
	for _, path := range []string{"/app.js", "/app.js?v=stale", "/app.js?v="} {
		rec := request(t, http.MethodGet, path, nil)
		if got := rec.Header().Get("Cache-Control"); got != assetCache {
			t.Fatalf("%s cache %q, want %q", path, got, assetCache)
		}
	}
}
