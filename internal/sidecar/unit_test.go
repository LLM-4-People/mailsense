package sidecar

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/vector"
	"log/slog"
	"maps"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

func writeTar(t *testing.T, entries ...*tar.Header) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(h.Name))
		}
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(h.Name))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	_ = os.WriteFile(p, buf.Bytes(), 0o600)
	return p
}

func TestUnpackRefusesEscapes(t *testing.T) {
	for name, h := range map[string]*tar.Header{
		"dot-dot":       {Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"absolute":      {Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"link outside":  {Name: "lib.so", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
		"absolute link": {Name: "lib.so", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"device":        {Name: "dev", Typeflag: tar.TypeChar},
	} {
		if _, err := unpack(writeTar(t, h), UnpackTarGz, t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := unpack("x", "rar", t.TempDir()); err == nil {
		t.Fatal("unknown format")
	}
}

// Review 2b #4 (failed first: the archive wrote runtime/evil.txt): links
// are checked by name only, so a chain l1 → ., l1/l2 → .. made
// l1/l2/evil.txt land outside the install directory. Unpacking through
// an os.Root refuses it.
func TestUnpackRefusesASymlinkChainOutOfTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need a privilege on Windows (the Windows builds are zips)")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "runtime", "build.installing")
	arc := writeTar(t, &tar.Header{Name: "l1", Typeflag: tar.TypeSymlink, Linkname: "."},
		&tar.Header{Name: "l1/l2", Typeflag: tar.TypeSymlink, Linkname: ".."},
		&tar.Header{Name: "l1/l2/evil.txt", Typeflag: tar.TypeReg, Mode: 0o644})
	_, err := unpack(arc, UnpackTarGz, dir)
	if _, serr := os.Stat(filepath.Join(root, "runtime", "evil.txt")); serr == nil || err == nil {
		t.Fatalf("an entry escaped through links (unpack error %v)", err)
	}
}

func TestUnzipAndManifestVerification(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.CreateHeader(&zip.FileHeader{Name: "bin/llama-server.exe", Method: zip.Deflate})
	_, _ = f.Write([]byte("MZ fake exe"))
	f, _ = zw.Create("ggml.dll")
	_, _ = f.Write([]byte("dll"))
	_ = zw.Close()
	archive := filepath.Join(t.TempDir(), "a.zip")
	_ = os.WriteFile(archive, buf.Bytes(), 0o600)
	dir := t.TempDir()
	files, err := unpack(archive, UnpackZip, dir)
	if err != nil || len(files) != 2 || files["ggml.dll"].Size != 3 || files["ggml.dll"].SHA256 == "" {
		t.Fatalf("%v %+v", err, files)
	}
	l := &layout{root: dir}
	for name, e := range files {
		if err := l.verifyEntry(dir, name, e); err != nil {
			t.Fatal(err)
		}
	}
	// The stamp cache: an unchanged file is not hashed again, a changed one is.
	time.Sleep(10 * time.Millisecond) // a different modification time
	_ = os.WriteFile(filepath.Join(dir, "ggml.dll"), []byte("DLL"), 0o600)
	if err := l.verifyEntry(dir, "ggml.dll", files["ggml.dll"]); !errors.Is(err, ErrHash) {
		t.Fatalf("a changed file: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "ggml.dll"))
	if err := l.verifyEntry(dir, "ggml.dll", files["ggml.dll"]); err == nil {
		t.Fatal("a missing file")
	}
	if err := unlisted(dir, manifest{Files: map[string]FileEntry{"bin/llama-server.exe": {}}}); err != nil {
		t.Fatalf("listed files only: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "planted.dll"), []byte("x"), 0o600)
	if err := unlisted(dir, manifest{Files: map[string]FileEntry{"bin/llama-server.exe": {}}}); !errors.Is(err, ErrHash) {
		t.Fatalf("a planted file is refused: %v", err)
	}
}

func TestCatalogValidationListsEveryProblem(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatalf("the built-in pins are valid: %v", err)
	}
	win := c.Runtimes["llama-server"].Platforms["windows/amd64"]
	if len(win.Auto) != 1 || win.Auto[0] != "vulkan" {
		t.Fatalf("auto tries vulkan on Windows: %v", win.Auto)
	}
	if len(win.Variants) != 1 || !win.Variants["vulkan"].GPU {
		t.Fatalf("Windows has exactly one build, vulkan: %v", slices.Sorted(maps.Keys(win.Variants)))
	}
	if _, ok := win.Variants[VariantCPU]; ok {
		t.Fatal("Windows must not offer a cpu build")
	}
	if _, ok := win.Variants["cuda"]; ok {
		t.Fatal("Windows must not offer a cuda build")
	}
	if linux := c.Runtimes["llama-server"].Platforms["linux/amd64"]; linux.Variants[VariantCPU].GPU {
		t.Fatalf("Linux keeps its cpu dev/test build: %v", slices.Sorted(maps.Keys(linux.Variants)))
	}
	bad := `{"runtimes": {"llama-server": {"label": "x", "version": "../v", "license": "", "args": [], "key_env": "", "health_path": "health",
		"slots": 0, "slot_context": 0, "env_allow": [],
		"platforms": {"windows/amd64": {"auto": ["cpu", "nope"], "variants": {"auto": {"exe": "../x.exe", "gpu": true, "args": [], "env": {}, "artifacts": [
		{"name": "a/b.zip", "url": "ftp://x", "sha256": "ABC", "size": 0, "unpack": "rar"}]}}}}}},
		"models": {"embeddinggemma-2": {"name": "m.gguf", "url": "https://x/m", "sha256": "` + strings.Repeat("a", 64) + `", "size": 1, "unpack": "zip"}},
		"canary": " "}`
	_, err = overlaid(t, []byte(bad))
	for _, want := range []string{"plain-name version", "slots ≥ 1", `"cpu" must be a gpu variant`, `"nope" must be`,
		"names must be plain", "exe must be", "plain file name", "url", "sha256", "unpack must", "used as is", "canary and tune_text"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing problem %q in %v", want, err)
		}
	}
	if _, err := overlaid(t, []byte(`{"runtimes": {"x": {"surprise": 1}}}`)); err == nil {
		t.Fatal("unknown fields are refused")
	}
	c2, err := overlaid(t, []byte(`{"models": {"embeddinggemma-2": null}, "local_embedder": null}`))
	if err != nil {
		t.Fatalf("removing the local model must also remove its pin: %v", err)
	}
	if _, err := c2.Resolve("llama-server", VariantCPU, "windows/amd64", "embeddinggemma-2"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("null removes a model: %v", err)
	}
	if _, err := c.Resolve("llama-server", "cuda", "windows/amd64", "embeddinggemma-2"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("cuda is gone from the Windows catalog: %v", err)
	}
	if m := c.Models["embeddinggemma-2"]; !strings.Contains(m.URL, "/resolve/bfcd298762cc34d0357ece5ebdd31791a3a374d8/") {
		t.Fatalf("the model comes from a fixed commit: %s", m.URL)
	}
}

// The pinned reference makes the Windows canary independent of a cpu
// build: the vector is used verbatim, belongs to the current sentence and
// is a finite unit vector.
func TestEmbeddedCatalogPinsTheCanaryVector(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Models[modelName]
	if len(m.CanaryVector) != 768 {
		t.Fatalf("the pinned vector has %d values, want 768", len(m.CanaryVector))
	}
	for i, f := range m.CanaryVector {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			t.Fatalf("canary_vector[%d] is not finite", i)
		}
	}
	if got := m.CanarySentenceSHA256; got != digest.Hex([]byte(c.Canary), 0) {
		t.Fatalf("the pinned vector belongs to %q, not the current canary sentence %q", got, digest.Hex([]byte(c.Canary), 0))
	}
	if norm := vector.Norm(m.CanaryVector); math.Abs(norm-1) > 1e-5 {
		t.Fatalf("the pinned vector is a unit vector, norm %.6f", norm)
	}
	if m.CanaryVector[0] != float32(-0.014057746157050133) || m.CanaryVector[767] != float32(0.0068154288455843925) {
		t.Fatal("the pinned vector is not the measured one (it must never be regenerated or rescaled)")
	}
}

// The pinned local embedder is data next to the artifacts: one runtime, one
// model with the complete embedding spec and chars_per_token, resolved to
// the platform's default build without starting anything.
func TestEmbeddedCatalogPinsTheLocalEmbedder(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := c.LocalEmbedder("windows/amd64")
	if !ok {
		t.Fatal("the pinned local embedder has no Windows build")
	}
	if pin.Runtime != "llama-server" || pin.Model != modelName || pin.Build.Variant != "vulkan" {
		t.Fatalf("pinned local embedder: %+v", pin)
	}
	if pin.CharsPerToken != 5 {
		t.Fatalf("chars_per_token = %v", pin.CharsPerToken)
	}
	spec := pin.Embedding
	if spec.Dimensions != 768 || !slices.Equal(spec.Matryoshka, []int{512, 256, 128}) ||
		spec.QueryTemplate != "task: search result | query: {text}" ||
		spec.DocumentTemplate != "title: {title} | text: {text}" || spec.EmptyTitle != "none" ||
		spec.NormTolerance != 0.01 || spec.MaxBatch != 32 || spec.ReservedTokens != 8 {
		t.Fatalf("embedding spec: %+v", spec)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("the pinned spec must pass the provider rules: %v", err)
	}
	if linux, ok := c.LocalEmbedder("linux/amd64"); !ok || linux.Build.Variant != VariantCPU {
		t.Fatalf("Linux resolves its cpu build: %+v", linux)
	}
	if _, ok := c.LocalEmbedder("plan9/386"); ok {
		t.Fatal("a platform without a build has no local embedder")
	}
}

// The local selection is validated with the catalog: its runtime and model
// must exist, the model must carry a complete embedding spec and a positive
// chars_per_token.
func TestLocalEmbedderSelectionIsValidated(t *testing.T) {
	spec := engine.EmbeddingSpec{Dimensions: 768, Matryoshka: []int{512, 256, 128},
		QueryTemplate: "task: search result | query: {text}", DocumentTemplate: "title: {title} | text: {text}",
		EmptyTitle: "none", NormTolerance: 0.01, MaxBatch: 32, ReservedTokens: 8}
	model := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "m.gguf", "url": "https://example.invalid/m.gguf",
			"sha256": strings.Repeat("a", 64), "size": 1, "unpack": ""}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]struct {
		pin    map[string]any
		models map[string]any
		want   string
	}{
		"missing names":   {map[string]any{"runtime": "", "model": ""}, nil, "runtime and model are required"},
		"unknown runtime": {map[string]any{"runtime": "nope", "model": modelName}, nil, "unknown runtime"},
		"unknown model":   {map[string]any{"runtime": "llama-server", "model": "nope"}, nil, "unknown model"},
		"missing spec": {map[string]any{"runtime": "llama-server", "model": "no-spec"},
			map[string]any{"no-spec": model(nil)}, "has no embedding spec"},
		"invalid spec": {map[string]any{"runtime": "llama-server", "model": "bad-spec"},
			map[string]any{"bad-spec": model(map[string]any{"chars_per_token": 5, "embedding": engine.EmbeddingSpec{Dimensions: 0}})}, "embedding"},
		"zero chars": {map[string]any{"runtime": "llama-server", "model": "no-chars"},
			map[string]any{"no-chars": model(map[string]any{"embedding": spec})}, "chars_per_token"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			override, err := json.Marshal(map[string]any{"local_embedder": tc.pin, "models": tc.models})
			if err != nil {
				t.Fatal(err)
			}
			_, err = overlaid(t, override)
			if err == nil || !strings.Contains(err.Error(), "local_embedder") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error lacks %q:\n%v", tc.want, err)
			}
		})
	}
}

func TestPlatformBuildRulesAndDefault(t *testing.T) {
	variant := func(gpu bool) Variant {
		return Variant{GPU: gpu, Exe: "x", Artifacts: []Artifact{{Name: "x.zip", URL: "https://x/x.zip", SHA256: strings.Repeat("a", 64), Size: 1, Unpack: UnpackZip}}}
	}
	for _, c := range []struct {
		name string
		p    Platform
		want []string
	}{
		{"no variant", Platform{}, []string{"at least one variant", "a cpu variant is required"}},
		{"auto without cpu", Platform{Auto: []string{"vulkan"}, Variants: map[string]Variant{"vulkan": variant(true)}}, nil},
		{"empty auto needs cpu", Platform{Variants: map[string]Variant{"vulkan": variant(true)}}, []string{"a cpu variant is required"}},
		{"auto must be gpu", Platform{Auto: []string{"cpu"}, Variants: map[string]Variant{"cpu": variant(false)}}, []string{`"cpu" must be a gpu variant`}},
	} {
		got := strings.Join(c.p.problems("platform"), "\n")
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: missing %q in %q", c.name, want, got)
			}
		}
		if len(c.want) == 0 && got != "" {
			t.Errorf("%s: unexpected problems %q", c.name, got)
		}
	}
	for _, c := range []struct {
		name string
		p    Platform
		want string
		ok   bool
	}{
		{"cpu wins", Platform{Auto: []string{"vulkan"}, Variants: map[string]Variant{"cpu": {}, "vulkan": {}}}, VariantCPU, true},
		{"first auto", Platform{Auto: []string{"cuda", "vulkan"}, Variants: map[string]Variant{"cuda": {}, "vulkan": {}}}, "cuda", true},
		{"only variant", Platform{Auto: []string{"vulkan"}, Variants: map[string]Variant{"vulkan": {}}}, "vulkan", true},
		{"only variant without auto", Platform{Variants: map[string]Variant{"vulkan": {}}}, "vulkan", true},
		{"none", Platform{}, "", false},
	} {
		got, err := c.p.defaultVariant()
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("%s: default %q %v, want %q", c.name, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestCanaryPinValidation(t *testing.T) {
	base := Artifact{Name: "m.gguf", URL: "https://x/m", SHA256: strings.Repeat("a", 64), Size: 1}
	with := func(v []float32, hash string) Artifact {
		a := base
		a.CanaryVector, a.CanarySentenceSHA256 = v, hash
		return a
	}
	good := with([]float32{0.5, 0.5, 0.5, 0.5}, strings.Repeat("b", 64))
	if got := good.problems("model"); len(got) != 0 {
		t.Fatalf("a pinned vector is valid: %v", got)
	}
	for name, c := range map[string]struct {
		a    Artifact
		want string
	}{
		"hash without vector": {with(nil, strings.Repeat("b", 64)), "requires a canary_vector"},
		"vector without hash": {with([]float32{1}, ""), "canary_sentence_sha256"},
		"bad hash":            {with([]float32{1}, "ABC"), "canary_sentence_sha256"},
		"too long":            {with(make([]float32, 8193), strings.Repeat("b", 64)), "1 to 8192"},
		"not finite":          {with([]float32{float32(math.Inf(1))}, strings.Repeat("b", 64)), "not finite"},
	} {
		got := strings.Join(c.a.problems("model"), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: missing %q in %q", name, c.want, got)
		}
	}
}

func TestWindowsDefaultBuildIsVulkan(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.DefaultBuild("llama-server", "windows/amd64", modelName)
	if err != nil || b.Variant != "vulkan" || !b.Spec.GPU {
		t.Fatalf("Windows default build: %+v %v", b, err)
	}
	if b, err = c.DefaultBuild("llama-server", "linux/amd64", modelName); err != nil || b.Variant != VariantCPU {
		t.Fatalf("Linux default build: %+v %v", b, err)
	}
	if _, err := c.DefaultBuild("llama-server", "plan9/386", modelName); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an unsupported platform: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	// #12: the candidates follow the hardware (no fixed maximum).
	for _, c := range []struct {
		percents []int
		cpus     int
		want     []int
	}{
		{[]int{25, 50, 75, 100}, 12, []int{3, 6, 9, 12}},
		{[]int{25, 50, 75, 100}, 64, []int{16, 32, 48, 64}},
		{[]int{25, 50, 75, 100}, 2, []int{1, 2}},
		{[]int{100, 50, 50}, 1, []int{1}},
		{nil, 8, []int{8}},
		{[]int{0, -5}, 4, []int{4}}, // misuse: nonsense percentages
	} {
		if got := candidates(c.percents, c.cpus); !slices.Equal(got, c.want) {
			t.Errorf("candidates(%v, %d) = %v, want %v", c.percents, c.cpus, got, c.want)
		}
	}
	texts := tuneTexts("a b c", 5)
	seen := map[string]bool{}
	for _, s := range texts {
		seen[s] = true
	}
	if len(seen) != 5 || !strings.HasPrefix(texts[1], "1 b c a") {
		t.Fatalf("distinct rotated texts: %q", texts)
	}
	env := childEnv([]string{"Path=/bin", "LLAMA_ARG_THREADS=99", "MAILSENSE_SECRET_X=s", "HOME=/h"}, []string{"PATH", "TEMP"},
		map[string]string{"B": "2", "A": "1"}, "LLAMA_API_KEY", "k")
	if strings.Join(env, ",") != "Path=/bin,A=1,B=2,LLAMA_API_KEY=k" {
		t.Fatalf("only allowed variables (any case), the variant's and the key: %v", env)
	}
	if got := expand([]string{"--port", "{port}", "-m={model}"}, map[string]string{"{port}": "9", "{model}": "m"}); strings.Join(got, " ") != "--port 9 -m=m" {
		t.Fatalf("expand %v", got)
	}
	tl := &tail{log: logx.For("test"), max: 2}
	_, _ = tl.Write([]byte("one\ntwo\r\nthr"))
	_, _ = tl.Write([]byte("ee\n"))
	if tl.last(5) != "two | three" {
		t.Fatalf("tail %q", tl.last(5))
	}
	for h, want := range map[string]int64{"bytes 100-199/200": 100, "bytes 0-9/10": 0, "": -1, "items 1-2": -1} {
		if rangeStart(h) != want {
			t.Errorf("rangeStart(%q)", h)
		}
	}
}

// Review 2b #6 (port hijack): the runtime must hold its own port.
func TestOwnsPort(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("no ownership check on this platform")
	}
	l, err := net.Listen("tcp", loopback+":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if ok, err := ownsPort(os.Getpid(), port); !ok || err != nil {
		t.Fatalf("this process holds it: %v %v", ok, err)
	}
	if ok, _ := ownsPort(os.Getpid()+1_000_000, port); ok {
		t.Fatal("another process does not")
	}
	if ok, _ := ownsPort(os.Getpid(), port+1); ok && port+1 < 65536 {
		t.Fatal("a port nobody listens on is nobody's")
	}
}

type countingEnder struct{ n int }

func (c *countingEnder) end(*child, time.Duration, clock.Clock) { c.n++ }

// Rules review 2b §1 (Windows job handle leak): a child's guard is
// released exactly once, also when the child had already exited.
func TestAChildIsEndedExactlyOnce(t *testing.T) {
	e := &countingEnder{}
	c := &child{end: e, done: make(chan struct{})}
	close(c.done) // it crashed
	c.stop(time.Second, clock.Real())
	c.stop(time.Second, clock.Real())
	if e.n != 1 {
		t.Fatalf("ended %d times", e.n)
	}
}

// TestChildDiesWithTheApp: an "app" process (this binary in helper mode)
// launches a runtime child and then dies without stopping it; the child
// must not outlive it (Linux: the parent-death signal; Windows: the job
// object with KILL_ON_JOB_CLOSE).
func TestChildDiesWithTheApp(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	m := w.manager()
	if _, err := w.locate(m); err != nil { // installs the fake runtime
		t.Fatal(err)
	}
	b, _ := m.build(rtName, modelName, VariantCPU)
	m.Stop()
	self, _ := os.Executable()
	app := exec.Command(self, "-test.run=^TestAppHelper$")
	app.Env = append(os.Environ(), "MAILSENSE_APP_HELPER=1", "HELPER_EXE="+m.layout.exe(b), "HELPER_MODEL="+m.layout.model(b))
	out, err := app.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "CHILD ")))
	_ = app.Wait()
	if err != nil {
		t.Fatalf("helper said %q", line)
	}
	eventually(t, func() bool { return processGone(pid) })
}

// TestAppHelper is the "app" of TestChildDiesWithTheApp (skipped otherwise).
func TestAppHelper(t *testing.T) {
	if os.Getenv("MAILSENSE_APP_HELPER") != "1" {
		t.Skip("helper process only")
	}
	c, err := launch(launchSpec{exe: os.Getenv("HELPER_EXE"), model: os.Getenv("HELPER_MODEL"), threads: 1, keyEnv: "LLAMA_API_KEY", key: "k",
		args:      []string{"--model", "{model}", "--host", "{host}", "--port", "{port}", "--threads", "{threads}"},
		envAllow:  []string{"PATH", "SYSTEMROOT", "TEMP", "TMP"},
		env:       map[string]string{"MAILSENSE_FAKE_LLAMA": "1"},
		waitDelay: 100 * time.Millisecond}, logx.For("helper"))
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("CHILD " + strconv.Itoa(c.pid) + "\n")
	os.Exit(0) // dies without stopping its child
}

// overlaid is the pins with a user file merged (LoadCatalog's merge).
func overlaid(t *testing.T, user []byte) (*Catalog, error) {
	t.Helper()
	merged, err := jsonx.Overlay(runtimesJSON, user, runtimesDeep...)
	if err != nil {
		return nil, err
	}
	return parseCatalog(merged)
}

// The shipped catalog decides which builds a platform has: Windows ships
// vulkan only, Linux ships cpu only. ValidateVariant is the settings write's
// gate, so a value that would break the next launch never reaches the file.
func TestValidateVariantUsesTheCatalogPerPlatform(t *testing.T) {
	cfg := configtest.New(t, nil)
	cases := []struct {
		platform string
		variant  string
		ok       bool
	}{
		{"windows/amd64", "auto", true},
		{"windows/amd64", "vulkan", true},
		{"windows/amd64", "cpu", false},
		{"windows/amd64", "cuda", false},
		{"linux/amd64", "auto", true},
		{"linux/amd64", "cpu", true},
		{"linux/amd64", "vulkan", false},
		{"linux/amd64", "cuda", false},
	}
	for _, tc := range cases {
		m := &Manager{cfg: cfg, platform: tc.platform}
		err := m.ValidateVariant(tc.variant)
		if (err == nil) != tc.ok {
			t.Errorf("%s %q: err=%v, want ok=%v", tc.platform, tc.variant, err, tc.ok)
		}
	}
}

// A runtime that writes an unterminated stream must not pin all of it.
func TestTailBoundsAnUnterminatedFragment(t *testing.T) {
	tl := &tail{log: logx.For("test"), max: 2}
	blob := bytes.Repeat([]byte("x"), tailPartialMax*4)
	if n, err := tl.Write(blob); err != nil || n != len(blob) {
		t.Fatalf("Write = %d %v", n, err)
	}
	if len(tl.partial) > tailPartialMax {
		t.Fatalf("partial fragment kept %d bytes, over %d", len(tl.partial), tailPartialMax)
	}
}

// testManager is the lifecycle owner alone: the shortest stop timeout and
// the manager's cancel context, without catalog, artifacts or jobs.
func testManager(t *testing.T, log *slog.Logger) *Manager {
	t.Helper()
	if log == nil {
		log = logx.For("test")
	}
	cfg := configtest.New(t, map[string]any{"sidecar.stop_timeout": "100ms"})
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{cfg: cfg, clock: clock.Real(), log: log, ctx: ctx, cancel: cancel, inst: map[string]*instance{}}
	t.Cleanup(m.Stop)
	return m
}

// waitStopped waits until Stop closed the start gate.
func waitStopped(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m.workers.Closed() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Stop did not close the start gate")
		}
		time.Sleep(time.Millisecond)
	}
}

// The supervisor's WaitGroup Add and Stop's Wait are serialized: a racing
// startWorker either joins and is waited for, or is refused after the gate
// closed. Add concurrent with Wait must not panic, and no supervisor may
// start after Stop returned.
func TestStartWorkerIsSerializedWithStop(t *testing.T) {
	for round := 0; round < 20; round++ {
		m := testManager(t, nil)
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseAll)
		worker := func() { <-release }
		if !m.startWorker(worker) {
			t.Fatal("the first worker was refused")
		}

		const callers = 32
		var racing sync.WaitGroup
		for range callers {
			racing.Add(1)
			go func() {
				defer racing.Done()
				m.startWorker(worker) // accepted calls block on release
			}()
		}
		stopDone := make(chan struct{})
		go func() {
			m.Stop()
			close(stopDone)
		}()
		racing.Wait()
		waitStopped(t, m)
		if m.startWorker(worker) {
			t.Fatal("startWorker accepted a worker after Stop")
		}
		select {
		case <-stopDone:
			t.Fatal("Stop returned before its workers were released")
		default:
		}
		releaseAll()
		select {
		case <-stopDone:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not return after its workers were released")
		}
	}
}

// A supervisor that never returns must not hang shutdown: Stop gives up at
// sidecar.stop_timeout + supervisorJoinSlack, logs the unjoined count and
// returns.
func TestStopIsBoundedWhenAWorkerNeverFinishes(t *testing.T) {
	var logs bytes.Buffer
	m := testManager(t, slog.New(slog.NewTextHandler(&logs, nil)))
	release := make(chan struct{})
	done := make(chan struct{})
	if !m.startWorker(func() {
		<-release
		close(done)
	}) {
		t.Fatal("the worker was refused")
	}
	t.Cleanup(func() {
		close(release)
		<-done
	})

	budget := m.cfg.Duration("sidecar.stop_timeout") + supervisorJoinSlack
	start := time.Now()
	stopDone := make(chan struct{})
	go func() {
		m.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(budget + 5*time.Second):
		t.Fatal("Stop did not return within sidecar.stop_timeout + supervisorJoinSlack")
	}
	if elapsed := time.Since(start); elapsed+10*time.Millisecond < budget {
		t.Fatalf("Stop returned after %s, before its join bound %s", elapsed, budget)
	} else if elapsed > budget+2*time.Second {
		t.Fatalf("Stop returned after %s, over its join bound %s", elapsed, budget)
	}
	if got := logs.String(); !strings.Contains(got, "sidecar supervisors did not stop before shutdown") || !strings.Contains(got, "left=1") {
		t.Fatalf("the unjoined supervisor was not logged: %s", got)
	}
}

// ensure after Stop resolves the current latch with ErrStopped and never
// starts a supervisor; a later ensure rearms a fresh latch and does the
// same.
func TestEnsureAfterStopResolvesErrStopped(t *testing.T) {
	m := testManager(t, nil)
	m.Stop()
	in := newInstance(m, rtName, modelName)
	l := in.ensure()
	select {
	case <-l.ch:
	default:
		t.Fatal("ensure did not resolve the latch after the manager stopped")
	}
	if !errors.Is(l.err, ErrStopped) {
		t.Fatalf("latch error = %v, want ErrStopped", l.err)
	}
	in.mu.Lock()
	running := in.running
	in.mu.Unlock()
	if running {
		t.Fatal("ensure kept running=true without a supervisor")
	}
	if workers := m.workers.Active(); workers != 0 {
		t.Fatalf("%d supervisor(s) started after Stop", workers)
	}

	l2 := in.ensure()
	if l2 == l {
		t.Fatal("ensure returned the already resolved latch instead of rearming it")
	}
	select {
	case <-l2.ch:
	default:
		t.Fatal("the rearmed latch was not resolved")
	}
	if !errors.Is(l2.err, ErrStopped) {
		t.Fatalf("rearmed latch error = %v, want ErrStopped", l2.err)
	}
}

// A launch's WaitDelay comes from the spec; zero leaves Go's default.
func TestLaunchSetsWaitDelay(t *testing.T) {
	spec := launchSpec{exe: "runtime", model: "m", keyEnv: "KEY", key: "k", threads: 1, slots: 1, slotCtx: 1}
	if got := commandFor(spec, 1234).WaitDelay; got != 0 {
		t.Fatalf("zero WaitDelay leaves Go's default, got %s", got)
	}
	spec.waitDelay = 250 * time.Millisecond
	if got := commandFor(spec, 1234).WaitDelay; got != spec.waitDelay {
		t.Fatalf("WaitDelay = %s, want %s", got, spec.waitDelay)
	}
}

// A grandchild inheriting the runtime's stdout/stderr must not block
// child.stop: WaitDelay closes the copy pipes after the bound once the
// process itself is gone. Without it, Wait blocks until the grandchild
// exits (measured before the fix: the full 5 s sleep).
func TestWaitDelayBoundsInheritedStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable shell to background a pipe holder; the job object ends the tree and TestLaunchSetsWaitDelay pins the wiring")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	c, err := launch(launchSpec{exe: sh, args: []string{"-c", "sleep 5 & exit 0"}, envAllow: []string{"PATH"},
		keyEnv: "TEST_KEY", key: "k", threads: 1, slots: 1, slotCtx: 1, waitDelay: 200 * time.Millisecond}, logx.For("test"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c.stop(0, clock.Real())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stop waited %s for a pipe held by a grandchild; WaitDelay was not applied", elapsed)
	}
}
