package sidecar

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
)

// The test binary doubles as a fake llama-server on every platform: the
// fake runtime archive holds this binary as fake/llama-server(.exe), and a
// variant whose env sets MAILSENSE_FAKE_LLAMA=1 makes TestMain serve
// embedtest's fake on the requested port instead of running tests.
// Knobs (variant env): FAKE_MODE corrupts vectors (a broken GPU build),
// FAKE_EXIT_AFTER ends it after that many health checks (a crash),
// FAKE_EXIT_AFTER_EMBED after that many embeddings requests, FAKE_DIE_ON
// when an input contains that text (an input that crashes it),
// FAKE_HEALTH_503 answers that many health checks with 503 (a runtime
// still loading), FAKE_SLOW_THREADS makes other thread counts slower,
// FAKE_DELAY_MS sets its per-input delay (a slower or faster build),
// FAKE_ENV_DUMP writes the names of its environment variables to that file.
func TestMain(m *testing.M) {
	if os.Getenv("MAILSENSE_FAKE_LLAMA") == "1" {
		os.Exit(fakeLlama(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeLlama(args []string) int {
	fs := flag.NewFlagSet("llama-server", flag.ContinueOnError)
	model := fs.String("model", "", "")
	host := fs.String("host", "", "")
	port := fs.Int("port", 0, "")
	threads := fs.Int("threads", 0, "")
	fs.Int("c", 0, "")
	fs.Bool("embeddings", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := os.Stat(*model); err != nil || *host != "127.0.0.1" || os.Getenv("LLAMA_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "fake: bad launch:", *model, *host, err)
		return 3
	}
	if os.Getenv("FAKE_FAIL_START") == "1" {
		// A build that cannot start for its own reason: the process exits
		// before binding, leaving its port free.
		fmt.Fprintln(os.Stderr, "fake: refusing to start")
		return 5
	}
	if os.Getenv("FAKE_NO_BIND") == "1" {
		// A process that stays alive without binding anything (the lost
		// port race: a foreign listener holds the port).
		fmt.Fprintln(os.Stderr, "fake: alive without binding")
		for {
			time.Sleep(time.Hour)
		}
	}
	dumpEnv()
	f := &embedtest.Fake{Key: os.Getenv("LLAMA_API_KEY"), Delay: fakeDelay(*threads)}
	f.SetMode(embedtest.Mode(os.Getenv("FAKE_MODE")))
	l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *host, *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake: listen:", err)
		return 5
	}
	fmt.Fprintf(os.Stderr, "fake llama-server listening on %s threads=%d\n", l.Addr(), *threads)
	_ = http.Serve(l, crashing(f))
	return 0
}

// crashing wraps the fake with the crash knobs.
func crashing(f *embedtest.Fake) http.Handler {
	health, _ := strconv.Atoi(os.Getenv("FAKE_EXIT_AFTER"))
	health503, _ := strconv.Atoi(os.Getenv("FAKE_HEALTH_503"))
	embeds, _ := strconv.Atoi(os.Getenv("FAKE_EXIT_AFTER_EMBED"))
	dieOn := os.Getenv("FAKE_DIE_ON")
	var checks, calls atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			n := checks.Add(1)
			if health > 0 && n > int64(health) {
				die("health checks")
			}
			if health503 > 0 && n <= int64(health503) {
				http.Error(w, `{"error":{"code":503,"message":"Loading model"}}`, http.StatusServiceUnavailable)
				return
			}
		}
		if r.URL.Path == "/v1/embeddings" {
			body, _ := io.ReadAll(r.Body)
			if (embeds > 0 && calls.Add(1) > int64(embeds)) || (dieOn != "" && bytes.Contains(body, []byte(dieOn))) {
				die("an embeddings request")
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		f.ServeHTTP(w, r)
	})
}

func die(after string) {
	fmt.Fprintln(os.Stderr, "fake: crashing on purpose after", after)
	os.Exit(4)
}

func dumpEnv() {
	path := os.Getenv("FAKE_ENV_DUMP")
	if path == "" {
		return
	}
	var names []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	_ = os.WriteFile(path, []byte(strings.Join(names, "\n")), 0o600)
}

// fakeDelay: FAKE_DELAY_MS, else FAKE_SLOW_THREADS's count is the fastest.
func fakeDelay(threads int) time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("FAKE_DELAY_MS")); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	best, _ := strconv.Atoi(os.Getenv("FAKE_SLOW_THREADS"))
	if best == 0 {
		return 0
	}
	return time.Duration(1+25*abs(threads-best)) * time.Millisecond
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// artifactServer serves pinned files and counts requests per file.
type artifactServer struct {
	*httptest.Server
	mu     sync.Mutex
	files  map[string][]byte
	hits   map[string]int
	ranges []string
	cut    map[string]int // first response of a file stops after this many bytes
	shift  map[string]int // a range answer that starts this many bytes early
	body   atomic.Int64   // response-body bytes actually written
}

// countingWriter counts response-body bytes and keeps Hijacker available
// (the cut path drops the connection mid-body).
type countingWriter struct {
	http.ResponseWriter
	body *atomic.Int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.body.Add(int64(n))
	return n, err
}

func (w countingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

// served is the response-body bytes served since the server started.
func (s *artifactServer) served() int64 { return s.body.Load() }

func newArtifactServer(t *testing.T) *artifactServer {
	s := &artifactServer{files: map[string][]byte{}, hits: map[string]int{}, cut: map[string]int{}, shift: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *artifactServer) serve(w http.ResponseWriter, r *http.Request) {
	w = countingWriter{ResponseWriter: w, body: &s.body}
	name := filepath.Base(r.URL.Path)
	s.mu.Lock()
	data, ok := s.files[name]
	s.hits[name]++
	cut, shift := s.cut[name], s.shift[name]
	delete(s.cut, name)
	rg := r.Header.Get("Range")
	if rg != "" {
		s.ranges = append(s.ranges, rg)
		delete(s.shift, name)
	}
	s.mu.Unlock()
	switch {
	case !ok:
		http.NotFound(w, r)
	case cut > 0:
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:cut])
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close() // the connection drops mid-body
		}
	case rg != "" && shift > 0:
		from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from-shift, len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[from-shift:])
	default:
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	}
}

func (s *artifactServer) add(name string, data []byte) Artifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = data
	sum := sha256.Sum256(data)
	unpack := UnpackNone
	switch filepath.Ext(name) {
	case ".zip":
		unpack = UnpackZip
	case ".gz":
		unpack = UnpackTarGz
	}
	return Artifact{Name: name, URL: s.URL + "/" + name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Unpack: unpack}
}

func (s *artifactServer) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[name]
}

// fakeExe is the runtime's executable inside the fake archive.
func fakeExe() string {
	if runtime.GOOS == "windows" {
		return "fake/llama-server.exe"
	}
	return "fake/llama-server"
}

// fakeRuntime is a zip holding this test binary as the runtime (built
// once per test process: the binary is large).
var fakeRuntime = sync.OnceValues(func() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: fakeExe(), Method: zip.Store}
	h.SetMode(0o755)
	f, err := zw.CreateHeader(h)
	if err == nil {
		_, err = f.Write(raw)
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	return buf.Bytes(), err
})

// platform is this machine's catalog key.
func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

type fakeVariant struct {
	gpu bool
	// requires is the catalog's requires value; "" writes no field (a
	// pre-round catalog and the round 25 control).
	requires string
	env      map[string]string
	// artifact overrides the shared runtime archive (a missing file proves
	// a build was never installed).
	artifact *Artifact
}

// writeCatalog writes sidecar.runtimes.file replacing llama-server with
// the fake runtime; variants maps a variant name to its gpu flag and env,
// auto lists the builds auto may try.
func writeCatalog(t *testing.T, dir string, rt, model Artifact, variants map[string]fakeVariant, auto ...string) {
	t.Helper()
	vs := map[string]any{}
	for name, v := range variants {
		env := map[string]string{"MAILSENSE_FAKE_LLAMA": "1"}
		for k, val := range v.env {
			env[k] = val
		}
		art := rt
		if v.artifact != nil {
			art = *v.artifact
		}
		variant := map[string]any{"exe": fakeExe(), "gpu": v.gpu, "args": []string{}, "env": env, "artifacts": []Artifact{art}}
		if v.requires != "" {
			variant["requires"] = v.requires
		}
		vs[name] = variant
	}
	if auto == nil {
		auto = []string{}
	}
	slices.Sort(auto)
	doc := map[string]any{"runtimes": map[string]any{"llama-server": map[string]any{
		"label": "fake", "version": "t1", "license": "test", "key_env": "LLAMA_API_KEY", "health_path": "/health",
		"slots": 2, "slot_context": 2048, "env_allow": []string{"PATH", "SYSTEMROOT", "TEMP", "TMP"},
		"args":      []string{"--model", "{model}", "--host", "{host}", "--port", "{port}", "--threads", "{threads}", "--embeddings", "-c", "{context}"},
		"platforms": map[string]any{platform(): map[string]any{"auto": auto, "variants": vs}}}},
		"models":         map[string]any{"embeddinggemma-2": model},
		"local_embedder": nil}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "runtimes.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
