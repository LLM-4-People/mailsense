package archive

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

// readBufferSize is the read-ahead of the file and the scanner's initial
// line buffer (the line cap itself is archive.max_line_bytes).
const readBufferSize = 64 << 10

// countingReader counts bytes read from the underlying file (progress).
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// lineSource reads JSONL lines from a plain or gzip file (detected by its
// magic bytes, not its name).
type lineSource struct {
	file    *os.File
	counter *countingReader
	gz      *gzip.Reader
	scanner *bufio.Scanner
	size    int64
	maxLine int // the cap this scanner enforces
}

func openLines(path string, maxLine int) (*lineSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	ls := &lineSource{file: f, counter: &countingReader{r: f}, size: st.Size()}
	buffered := bufio.NewReaderSize(ls.counter, readBufferSize)
	var r io.Reader = buffered
	if magic, _ := buffered.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		if ls.gz, err = gzip.NewReader(buffered); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("gzip: %w", err)
		}
		r = ls.gz
	}
	ls.scanner = bufio.NewScanner(r)
	// The token limit is the larger of maxLine and the initial capacity.
	ls.scanner.Buffer(make([]byte, 0, min(readBufferSize, maxLine)), maxLine)
	ls.maxLine = maxLine
	return ls, nil
}

func (ls *lineSource) bytesRead() int64 { return ls.counter.n.Load() }

func (ls *lineSource) close() error {
	if ls.gz != nil {
		_ = ls.gz.Close()
	}
	return ls.file.Close()
}

// fingerprint identifies the file content version (size + mtime): taken
// before reading and compared after, so stale content is never marked fresh.
func fingerprint(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano()), nil
}

// EachLine calls fn with every non-blank line (trimmed) of a JSONL file,
// plain or gzip (by its magic bytes), numbered from 1; a line longer than
// maxLine, a read error or an error of fn stops it. The one JSONL reader
// of the repository (the importer, the gold export and the benchmark).
func EachLine(path string, maxLine int, fn func(n int, line []byte) error) error {
	ls, err := openLines(path, maxLine)
	if err != nil {
		return err
	}
	defer ls.close()
	for n := 1; ls.scanner.Scan(); n++ {
		if line := bytes.TrimSpace(ls.scanner.Bytes()); len(line) > 0 {
			if err := fn(n, line); err != nil {
				return fmt.Errorf("%s line %d: %w", filepath.Base(path), n, err)
			}
		}
	}
	return ls.scanner.Err()
}
