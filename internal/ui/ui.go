// Package ui serves Mailsense's embedded desktop shell: one HTML document,
// two stylesheets, the English and French message catalogs, the interface
// runtime, the application script and one vector icon, with no build step
// and no network dependency. The API's static route delegates to Handler;
// Files is the embedded tree for anything that needs to read an asset
// directly.
package ui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/httpgzip"
)

// Files is the embedded web root. It is the one owner of the asset set.
//
//go:generate go run ./gen
//go:embed index.html app.css app.js mail.css favicon.svg i18n.en.js i18n.fr.js i18n.js js/*.js js/manifest.txt css/*.css css/manifest.txt
var Files embed.FS

const (
	// shellCache keeps the document out of every cache: it names unhashed
	// assets and must change with the binary that embeds it.
	shellCache = "no-store"
	// assetCache revalidates an unversioned or stale asset on every use, so
	// an upgraded binary never serves a previous release's script or style.
	assetCache = "no-cache"
	// immutableCache is safe only for a URL whose ?v= names the bytes this
	// binary serves. The shell is no-store, so a document from an older
	// binary cannot keep pointing at a version this one no longer matches.
	immutableCache = "public, max-age=31536000, immutable"
)

type asset struct {
	name        string
	contentType string
	cache       string
	body        []byte
	gzip        []byte
	etag        string
	gzipETag    string
	version     string // the content fingerprint in ?v=
}

var assets = loadAssets()

func loadAssets() map[string]asset {
	out := map[string]asset{}
	for _, spec := range []struct {
		name        string
		contentType string
		cache       string
	}{
		{"index.html", "text/html; charset=utf-8", shellCache},
		{"app.css", "text/css; charset=utf-8", assetCache},
		{"app.js", "text/javascript; charset=utf-8", assetCache},
		{"mail.css", "text/css; charset=utf-8", assetCache},
		{"favicon.svg", "image/svg+xml", assetCache},
		{"i18n.en.js", "text/javascript; charset=utf-8", assetCache},
		{"i18n.fr.js", "text/javascript; charset=utf-8", assetCache},
		{"i18n.js", "text/javascript; charset=utf-8", assetCache},
	} {
		body, err := fs.ReadFile(Files, spec.name)
		if err != nil {
			panic("ui: embedded asset " + spec.name + " is missing: " + err.Error())
		}
		out["/"+spec.name] = newAsset(spec.name, spec.contentType, spec.cache, body)
	}
	out["/index.html"] = resolveAssetRefs(out)
	return out
}

// newAsset is the one asset constructor: bytes in, strong validators, gzip
// representation and content version out.
func newAsset(name, contentType, cache string, body []byte) asset {
	sum := sha256.Sum256(body)
	version := hex.EncodeToString(sum[:8])
	a := asset{
		name:        name,
		contentType: contentType,
		cache:       cache,
		body:        body,
		etag:        `"` + version + `"`,
		version:     version,
	}
	if len(body) >= httpgzip.MinBytes {
		if compressed, err := httpgzip.Compress(body); err == nil {
			a.gzip = compressed
			// A content coding is part of the representation, so the
			// compressed variant needs its own strong validator. A cache
			// or proxy that mixes the two would otherwise revalidate one
			// body with the other's tag.
			a.gzipETag = `"` + version + `-gzip"`
		}
	}
	return a
}

// resolveAssetRefs fills the shell's asset placeholders from the one asset
// map: each reference gets the current ?v=, and one meta tag carries every
// version (including catalogs the runtime fetches lazily). No consumer
// hard-codes a generated filename or hash.
func resolveAssetRefs(assets map[string]asset) asset {
	versions := make(map[string]string, len(assets))
	for name, a := range assets {
		if name == "/index.html" {
			continue
		}
		versions[strings.TrimPrefix(name, "/")] = a.version
	}
	meta, err := json.Marshal(versions)
	if err != nil {
		panic("ui: asset versions: " + err.Error())
	}
	page := string(assets["/index.html"].body)
	for name, version := range versions {
		page = strings.ReplaceAll(page, "{{asset:"+name+"}}", "/"+name+"?v="+version)
	}
	page = strings.ReplaceAll(page, "{{asset-versions}}", string(meta))
	if strings.Contains(page, "{{asset") {
		panic(fmt.Sprintf("ui: unresolved asset placeholder in index.html"))
	}
	return newAsset("index.html", "text/html; charset=utf-8", shellCache, []byte(page))
}

// Handler serves the shell and its assets. "/" is the document,
// "/index.html" redirects there, unknown paths are plain 404s, and every
// other method is 405 with an explicit Allow header.
func Handler() http.Handler { return handler{} }

type handler struct{}

func (handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/":
		serveAsset(w, r, assets["/index.html"])
	case "/index.html":
		w.Header().Set("Cache-Control", shellCache)
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusPermanentRedirect)
	default:
		if a, ok := assets[r.URL.Path]; ok {
			serveAsset(w, r, a)
			return
		}
		http.NotFound(w, r)
	}
}

func serveAsset(w http.ResponseWriter, r *http.Request, a asset) {
	compressed := httpgzip.Accepts(r) && len(a.gzip) > 0
	body, etag := a.body, a.etag
	if compressed {
		body, etag = a.gzip, a.gzipETag
	}
	cache := a.cache
	if a.cache == assetCache && r.URL.Query().Get("v") == a.version {
		cache = immutableCache
	}
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", cache)
	h.Set("ETag", etag)
	h.Set("Vary", "Accept-Encoding")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if compressed {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// etagMatches implements the weak comparison a conditional GET needs: a
// comma-separated list, "*", or the asset's tag with an optional W/ prefix.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}
