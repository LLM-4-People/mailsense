package sidecar

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
)

// child is one running server process.
type child struct {
	cmd     *exec.Cmd
	end     ender
	endOnce sync.Once
	done    chan struct{} // closed once the process has exited
	err     error         // its exit status (read after done)
	out     *tail
	url     string // http://127.0.0.1:port
	port    int
	key     string
	pid     int
}

// ender stops a child and releases what guards it: closing its job object
// (Windows, which also ends it) or a signal and the end of the grace
// period (elsewhere).
type ender interface {
	end(c *child, grace time.Duration, clk clock.Clock)
}

// launchSpec is everything one launch needs.
type launchSpec struct {
	exe      string
	model    string
	args     []string
	env      map[string]string
	envAllow []string
	keyEnv   string
	key      string
	threads  int
	slots    int
	slotCtx  int
	// waitDelay bounds cmd.Wait's wait for the stdout/stderr copy
	// goroutines once the process itself has exited or been ended. A
	// grandchild that inherited those pipes can otherwise hold them open
	// and block Wait (and so child.stop and Manager.Stop) indefinitely.
	// It is not a kill or restart bound: the normal path still waits for
	// the child process to die before a second start can have its port.
	waitDelay time.Duration
}

// commandFor builds one launch's command and environment. Setting
// WaitDelay here is what lets Wait return after an inherited stdout/stderr
// pipe outlives the child (Go's documented bound for its copy goroutines);
// zero leaves Go's default, which reads the pipes to EOF.
func commandFor(ls launchSpec, port int) *exec.Cmd {
	cmd := exec.Command(ls.exe, expand(ls.args, map[string]string{phModel: ls.model, phHost: loopback, phPort: strconv.Itoa(port),
		phThreads: strconv.Itoa(ls.threads), phSlots: strconv.Itoa(ls.slots), phSlotContext: strconv.Itoa(ls.slotCtx),
		phContext: strconv.Itoa(ls.slots * ls.slotCtx)})...)
	cmd.Dir = filepath.Dir(ls.exe)
	cmd.Env = childEnv(os.Environ(), ls.envAllow, ls.env, ls.keyEnv, ls.key)
	if ls.waitDelay > 0 {
		cmd.WaitDelay = ls.waitDelay
	}
	return cmd
}

// launch starts the server on a free loopback port with the manager's
// key in the environment (never on the command line) and only the
// allowed variables of the app's environment.
// choosePort is the loopback port a launch asks the OS for. It is a variable
// because a lost port race is the one failure a test must reproduce
// deterministically: nothing in production replaces it.
var choosePort = freePort

func launch(ls launchSpec, log *slog.Logger) (*child, error) {
	port, err := choosePort()
	if err != nil {
		return nil, err
	}
	cmd := commandFor(ls, port)
	out := &tail{log: log, max: tailLines}
	cmd.Stdout, cmd.Stderr = out, out
	end, err := start(cmd)
	if err != nil {
		return nil, fmt.Errorf("sidecar: start %s: %w", ls.exe, err)
	}
	c := &child{cmd: cmd, end: end, done: make(chan struct{}), out: out, url: "http://" + loopback + ":" + strconv.Itoa(port),
		port: port, key: ls.key, pid: cmd.Process.Pid}
	go func() {
		c.err = cmd.Wait()
		close(c.done)
	}()
	log.Info("runtime process started", "pid", c.pid, "port", port, "threads", ls.threads, "exe", ls.exe)
	return c, nil
}

// stop ends the child (if it still runs), releases its guard exactly once
// (a crashed child's job handle too) and waits for it. This still waits
// for the process itself to die; the launch's WaitDelay only bounds Wait's
// wait for a stdout/stderr pipe a grandchild inherited.
func (c *child) stop(grace time.Duration, clk clock.Clock) {
	c.endOnce.Do(func() { c.end.end(c, grace, clk) })
	<-c.done
}

// exited reports whether the process has ended.
func (c *child) exited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// owned checks that the child itself listens on its port: between
// freePort and the child's bind another local program could take the
// port and receive the key and the mail text.
func (c *child) owned() error {
	ok, err := ownsPort(c.pid, c.port)
	switch {
	case err != nil:
		return fmt.Errorf("sidecar: who listens on port %d: %w", c.port, err)
	case !ok:
		return fmt.Errorf("sidecar: port %d is not held by the runtime (pid %d)", c.port, c.pid)
	}
	return nil
}

// expand fills the {placeholders} of the runtime args.
func expand(args []string, values map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		for k, v := range values {
			a = strings.ReplaceAll(a, k, v)
		}
		out[i] = a
	}
	return out
}

// childEnv is the allowed variables of the app's environment (names
// compared case-insensitively, as Windows does), the variant's variables
// and the key: no MAILSENSE_* (secrets in development), no stray LLAMA_*
// setting that would silently change the server.
func childEnv(base, allow []string, extra map[string]string, keyEnv, key string) []string {
	var out []string
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if slices.ContainsFunc(allow, func(a string) bool { return strings.EqualFold(a, name) }) {
			out = append(out, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(extra)) {
		out = append(out, k+"="+extra[k])
	}
	return append(out, keyEnv+"="+key)
}

// freePort asks the OS for a free loopback port (another program taking
// it first is caught by owned; the next start picks another port).
func freePort() (int, error) {
	l, err := net.Listen("tcp", loopback+":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// randomKey is a fresh 32-byte key in hex.
func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// tailLines is how many output lines a child keeps for diagnostics.
const tailLines = 40

// tailPartialMax bounds an unterminated output fragment. A runtime that
// writes megabytes without a newline must not pin them in memory.
const tailPartialMax = 64 << 10

// tail forwards the server's output to the log (debug) and keeps the
// last lines, quoted when a start fails.
type tail struct {
	mu      sync.Mutex
	log     *slog.Logger
	max     int
	partial []byte
	lines   []string
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) > tailPartialMax {
		p = p[len(p)-tailPartialMax:]
	}
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(t.partial[:i]), "\r")
		t.partial = t.partial[i+1:]
		t.log.Debug("runtime output", "line", line)
		t.lines = append(t.lines, line)
		if len(t.lines) > t.max {
			t.lines = t.lines[len(t.lines)-t.max:]
		}
	}
	if len(t.partial) > tailPartialMax {
		t.partial = t.partial[len(t.partial)-tailPartialMax:]
	}
	return n, nil
}

// last returns up to n of the latest lines, joined.
func (t *tail) last(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines[max(0, len(t.lines)-n):], " | ")
}
