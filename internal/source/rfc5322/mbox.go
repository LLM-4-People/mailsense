package rfc5322

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"regexp"
	"strconv"
)

// fromLine is an RFC 4155 separator: "From ", the envelope sender (any
// non-blank token, "-" or "MAILER-DAEMON" included), then a C asctime()
// date — weekday, month, day, time, and the year last (some writers put
// a zone before the year). Protocol structure only: no names are listed.
var fromLine = regexp.MustCompile(`^From \S*\s+[A-Za-z]{3}\s+[A-Za-z]{3}\s+\d{1,2}\s+\d{1,2}:\d{2}(:\d{2})?(\s+\S+)*?\s+\d{4}\s*$`)

// isSeparator: a "From " line at the start of the file or after a blank
// line, shaped like a separator. Any other "From " line (a body line an
// mbox writer did not quote) is part of the message.
func isSeparator(line []byte, afterBlank bool) bool {
	return afterBlank && bytes.HasPrefix(line, []byte("From ")) && fromLine.Match(bytes.TrimRight(line, "\r\n"))
}

// mboxEntry is one message being scanned: where its content starts, and
// two digests — the id without the client status headers, the stamp with
// everything.
type mboxEntry struct {
	start, end int64
	id, stamp  hash.Hash
	inHeader   bool
	inStatus   bool   // inside a status header (its continuation lines too)
	blank      []byte // a held blank line (framing if a separator follows)
	unsplit    int    // separator-shaped lines kept as content (no blank line before)
}

func newEntry(start int64) *mboxEntry {
	return &mboxEntry{start: start, end: start, id: sha256.New(), stamp: sha256.New(), inHeader: true}
}

// add hashes one chunk of content (start: it begins a line); a blank
// line is held until the next line shows it is content (the blank line
// before a separator is framing, RFC 4155).
func (e *mboxEntry) add(line []byte, start bool) {
	if e.blank != nil {
		e.write(e.blank, true)
		e.blank = nil
	}
	if start && len(bytes.TrimRight(line, "\r\n")) == 0 {
		e.blank = append([]byte(nil), line...)
		return
	}
	e.write(line, start)
}

func (e *mboxEntry) write(line []byte, start bool) {
	e.end += int64(len(line))
	e.stamp.Write(line)
	if start && len(bytes.TrimRight(line, "\r\n")) == 0 {
		e.inHeader, e.inStatus = false, false
	}
	if e.inHeader {
		if start && line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			e.inStatus = formats().status[string(bytes.ToLower(bytes.TrimSpace(name)))]
		}
		if e.inStatus {
			return
		}
	}
	e.id.Write(line)
}

// mboxItems lists an mbox (RFC 4155) by offsets: one pass computes each
// message's span and digests; reading a message later opens the file at
// its span (nothing is held in memory). The id is the folder path and the
// digest without status headers, numbered when identical messages repeat.
func mboxItems(path, folder string, maxBytes int64) ([]item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var out []item
	var cur *mboxEntry
	seen := map[string]int{}
	var off int64
	afterBlank, lineStart := true, true
	for {
		chunk, err := r.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull && err != io.EOF {
			return nil, err
		}
		if lineStart && isSeparator(chunk, afterBlank) && (err == nil || err == io.EOF) {
			out = appendEntry(out, cur, path, folder, maxBytes, seen)
			cur = newEntry(off + int64(len(chunk)))
		} else if cur != nil {
			if lineStart && isSeparator(chunk, true) {
				cur.unsplit++
			}
			cur.add(chunk, lineStart)
		}
		off += int64(len(chunk))
		if len(chunk) > 0 {
			afterBlank = lineStart && len(bytes.TrimRight(chunk, "\r\n")) == 0
			lineStart = chunk[len(chunk)-1] == '\n'
		}
		if err == io.EOF {
			break
		}
	}
	return appendEntry(out, cur, path, folder, maxBytes, seen), nil
}

func appendEntry(out []item, e *mboxEntry, path, folder string, maxBytes int64, seen map[string]int) []item {
	if e == nil {
		return out
	}
	sum := hex.EncodeToString(e.id.Sum(nil)[:16])
	id := folder + "#" + sum
	if n := seen[sum]; n > 0 {
		id += "." + strconv.Itoa(n)
	}
	seen[sum]++
	start, size := e.start, e.end-e.start
	return append(out, item{ID: id, Stamp: hex.EncodeToString(e.stamp.Sum(nil)[:16]), Size: size, unsplit: e.unsplit,
		read: func() ([]byte, error) {
			data, err := readSection(path, start, size, maxBytes)
			return unquoteFrom(data), err
		}})
}

// unquoteFrom undoes one level of mboxrd ">From " quoting per line.
func unquoteFrom(data []byte) []byte {
	if !bytes.Contains(data, []byte(">From ")) {
		return data
	}
	var b bytes.Buffer
	b.Grow(len(data))
	for line := range bytes.Lines(data) {
		if q := bytes.TrimLeft(line, ">"); len(q) < len(line) && bytes.HasPrefix(q, []byte("From ")) {
			line = line[1:]
		}
		b.Write(line)
	}
	return b.Bytes()
}
