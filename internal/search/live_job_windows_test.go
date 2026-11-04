//go:build windows && embedlive

package search

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

// diesWithApp: a helper "app" process starts the managed runtime
// (Vulkan on Windows) and exits without stopping it; the job object must
// end the runtime with it.
func diesWithApp(t *testing.T, dir string) string {
	self, err := os.Executable() // os.Args[0] is relative: exec refuses the current directory
	if err != nil {
		return "helper did not start: " + err.Error()
	}
	cmd := exec.Command(self, "-test.run=^TestEmbedLiveHelper$")
	cmd.Env = append(os.Environ(), "MAILSENSE_EMBEDLIVE_HELPER="+dir)
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return "helper did not start: " + err.Error()
	}
	sc := bufio.NewScanner(out)
	pid := 0
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "CHILD "); ok {
			pid, _ = strconv.Atoi(p)
		}
	}
	_ = cmd.Wait()
	if pid == 0 {
		return "helper reported no runtime"
	}
	for range 50 {
		if gone(pid) {
			return "yes: runtime pid " + strconv.Itoa(pid) + " ended with its app"
		}
		time.Sleep(200 * time.Millisecond) // a real process ending
	}
	return "NO: runtime pid " + strconv.Itoa(pid) + " outlived its app"
}

// TestEmbedLiveJobObject runs only the job-object check against the
// installed managed runtime. The device-relay wrapper is not part of the
// public tree; run this test binary directly with the live flags.
func TestEmbedLiveJobObject(t *testing.T) {
	cfg, _ := liveConfig(t, "embed-live-job", nil)
	got := diesWithApp(t, cfg.Paths().DataDir)
	t.Logf("LIVE dies_with_app=%s", got)
	if !strings.HasPrefix(got, "yes") {
		t.Fatal(got)
	}
}

// TestEmbedLiveHelper is the "app" of diesWithApp (skipped otherwise).
func TestEmbedLiveHelper(t *testing.T) {
	dir := os.Getenv("MAILSENSE_EMBEDLIVE_HELPER")
	if dir == "" {
		t.Skip("helper process only")
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	must(t, cfg.Set("sidecar.variant", sidecar.VariantAuto))
	m, err := sidecar.New(sidecar.Options{Config: cfg, Jobs: jobs.NewManager(cfg, events.New(), nil), Probe: embed.Probe(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Locate(t.Context(), liveRuntime, liveModel); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("CHILD " + strconv.Itoa(m.Status()[0].PID) + "\n")
	os.Exit(0) // dies without stopping its runtime
}

func gone(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code != 259 // STILL_ACTIVE
}

// gpu reads the NVIDIA GPU's memory (read-only query; nothing changed).
func gpu() []string {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total,memory.used,memory.free,driver_version", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return []string{"nvidia-smi: " + err.Error()}
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}
