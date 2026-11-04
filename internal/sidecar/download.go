package sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// downloader fetches pinned artifacts: into <name>.part, resumed with an
// HTTP Range after a failure, checked for size and SHA-256, then renamed
// into place. A part that fails its check is removed, never used. One
// fetch per path at a time: builds that share an artifact (every build
// shares the model) never write the same part concurrently.
type downloader struct {
	client   *http.Client
	attempts int
	replace  fsx.Replace
	log      *slog.Logger
	mu       sync.Mutex
	paths    map[string]*sync.Mutex
}

// lock serialises fetches of one path.
func (d *downloader) lock(path string) func() {
	d.mu.Lock()
	if d.paths == nil {
		d.paths = map[string]*sync.Mutex{}
	}
	m := d.paths[path]
	if m == nil {
		m = &sync.Mutex{}
		d.paths[path] = m
	}
	d.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// fetch makes path hold the artifact; progress gets the bytes held so far.
func (d *downloader) fetch(ctx context.Context, a Artifact, path string, progress func(int64)) error {
	defer d.lock(path)()
	if err := verifyFile(path, a.SHA256, a.Size); err == nil {
		progress(a.Size)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		d.log.Warn("artifact on disk is not the pinned one: downloading again", "artifact", a.Name, "err", err)
		if err := fsx.Remove(path, d.replace); err != nil {
			return err
		}
	}
	part := path + ".part"
	var err error
	for attempt := 1; attempt <= max(d.attempts, 1); attempt++ {
		if err = d.resume(ctx, a, part, progress); err == nil || ctx.Err() != nil {
			break
		}
		d.log.Warn("artifact download interrupted", "artifact", a.Name, "attempt", attempt, "err", err)
	}
	if err != nil {
		return fmt.Errorf("sidecar: download %s: %w", a.Name, err)
	}
	if err := verifyFile(part, a.SHA256, a.Size); err != nil {
		_ = fsx.Remove(part, d.replace)
		return fmt.Errorf("sidecar: download %s: %w", a.Name, err)
	}
	d.log.Info("artifact downloaded and verified", "artifact", a.Name, "bytes", a.Size, "sha256", a.SHA256)
	return fsx.Promote(part, path, d.replace)
}

// resume appends the missing bytes to part. A server that ignores the
// Range request (200), or answers another range than asked, makes the
// part start over.
func (d *downloader) resume(ctx context.Context, a Artifact, part string, progress func(int64)) error {
	have := sizeOf(part)
	if have > a.Size {
		if err := fsx.Remove(part, d.replace); err != nil {
			return err
		}
		have = 0
	}
	if have == a.Size {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && rangeStart(resp.Header.Get("Content-Range")) != have:
		return d.restart(part, fmt.Errorf("the server sent range %q for bytes %d-", resp.Header.Get("Content-Range"), have))
	case resp.StatusCode == http.StatusOK && have > 0:
		if err := fsx.Remove(part, d.replace); err != nil {
			return err
		}
		have = 0
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return d.append(part, resp.Body, have, a.Size, progress)
}

// restart drops a part whose continuation cannot be trusted.
func (d *downloader) restart(part string, why error) error {
	if err := fsx.Remove(part, d.replace); err != nil {
		return err
	}
	return why
}

// rangeStart is the first byte of a Content-Range ("bytes 100-199/200"),
// -1 when it cannot be read.
func rangeStart(h string) int64 {
	spec, ok := strings.CutPrefix(h, "bytes ")
	first, _, ok2 := strings.Cut(spec, "-")
	n, err := strconv.ParseInt(first, 10, 64)
	if !ok || !ok2 || err != nil {
		return -1
	}
	return n
}

func (d *downloader) append(part string, body io.Reader, have, size int64, progress func(int64)) error {
	f, err := fsx.OpenAppend(part)
	if err != nil {
		return err
	}
	w := &counting{w: f, n: have, report: progress}
	_, err = io.Copy(w, io.LimitReader(body, size-have+1))
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && w.n != size {
		err = fmt.Errorf("received %d of %d bytes", w.n, size)
	}
	return err
}

// counting reports the bytes written so far.
type counting struct {
	w      io.Writer
	n      int64
	report func(int64)
}

func (c *counting) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.report(c.n)
	return n, err
}

func sizeOf(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// verifyFile checks a file's size and SHA-256 (os.ErrNotExist when absent).
func verifyFile(path, sha string, size int64) error {
	got, n, err := digest.File(path)
	if err != nil {
		return err
	}
	if n != size || got != sha {
		return fmt.Errorf("%w: %s is %d bytes with SHA-256 %s, want %d bytes and %s", ErrHash, path, n, got, size, sha)
	}
	return nil
}
