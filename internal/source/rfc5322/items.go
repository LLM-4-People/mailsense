package rfc5322

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// item is one message of a folder: an id unique within the source, a
// change stamp, read flags, and where its bytes are (read lazily: an item
// never holds message bytes).
type item struct {
	ID    string
	Stamp string
	Seen  bool
	Size  int64
	read  func() ([]byte, error)
	// unsplit counts separator-shaped lines inside the message (mbox:
	// not after a blank line), declared as mail.AltMboxUnsplit.
	unsplit int
}

// tooLarge is the error of a message over rfc5322.max_message_mb: it is
// skipped and counted, never cut.
func tooLarge(size, maxBytes int64) error {
	return fmt.Errorf("rfc5322: message of %d bytes is over rfc5322.max_message_mb (%d bytes); skipped", size, maxBytes)
}

// readSection reads n bytes at off of a file, refusing more than maxBytes.
func readSection(path string, off, n, maxBytes int64) ([]byte, error) {
	if n > maxBytes {
		return nil, tooLarge(n, maxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, n)
	if _, err := io.ReadFull(io.NewSectionReader(f, off, n), data); err != nil {
		return nil, fmt.Errorf("rfc5322: %s changed while read: %w", filepath.Base(path), err)
	}
	return data, nil
}

// listItems lists a folder's messages; maxBytes caps one message.
func listItems(f folderSpec, maxBytes int64) ([]item, error) {
	switch f.Layout {
	case layoutMbox:
		return mboxItems(f.FSPath, f.Path, maxBytes)
	case layoutMaildir:
		var out []item
		for _, sub := range []string{"cur", "new"} {
			items, err := fileItems(filepath.Join(f.FSPath, sub), maildirID, maxBytes)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		return out, nil
	default:
		if st, err := os.Stat(f.FSPath); err == nil && !st.IsDir() {
			return []item{fileItem(f.FSPath, f.Path, st, maxBytes)}, nil
		}
		return fileItems(f.FSPath, emlID(f.Path), maxBytes)
	}
}

// maildirID is a maildir message's unique name without its ":2,flags"
// info. The maildir specification makes that name unique across every
// folder of the tree (time, delivery id, host), and a client moving a
// message to another folder keeps it: so the id is the name alone and a
// move keeps the message's labels and decisions. Files that are not
// messages (no maildir name) return "".
func maildirID(name string) string {
	id, _, _ := strings.Cut(name, ":2,")
	return id
}

// emlID names an .eml file by its folder path and file name: file names
// alone repeat across directories (1.eml, message.eml). Other files of
// the directory are not messages ("").
func emlID(folder string) func(string) string {
	return func(name string) string {
		if !strings.EqualFold(filepath.Ext(name), ".eml") {
			return ""
		}
		return folder + "/" + name
	}
}

// fileItems are one-file-per-message items of dir; id names a file ("" =
// not a message).
func fileItems(dir string, id func(string) string, maxBytes int64) ([]item, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []item
	for _, e := range entries {
		name := id(e.Name())
		if !e.Type().IsRegular() || name == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed meanwhile: the next snapshot no longer lists it
		}
		out = append(out, fileItem(filepath.Join(dir, e.Name()), name, info, maxBytes))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// fileItem is one message file. Its stamp is its size, modification time
// and maildir flags (a rename to ":2,S" moves no time); flag S marks it
// seen.
func fileItem(path, id string, info os.FileInfo, maxBytes int64) item {
	_, flags, _ := strings.Cut(filepath.Base(path), ":2,")
	size := info.Size()
	return item{ID: id, Stamp: fmt.Sprintf("%d-%d-%s", size, info.ModTime().UnixNano(), flags), Seen: strings.Contains(flags, "S"),
		Size: size, read: func() ([]byte, error) { return readSection(path, 0, size, maxBytes) }}
}

// fingerprint is a folder's change marker: an unchanged folder is not
// read again.
func fingerprint(f folderSpec) (string, error) {
	var parts []string
	paths := []string{f.FSPath}
	if f.Layout == layoutMaildir {
		paths = []string{filepath.Join(f.FSPath, "cur"), filepath.Join(f.FSPath, "new")}
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		parts = append(parts, strconv.FormatInt(st.Size(), 10)+"-"+strconv.FormatInt(st.ModTime().UnixNano(), 10))
		if st.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return "", err
			}
			parts = append(parts, strconv.Itoa(len(entries)))
		}
	}
	return strings.Join(parts, "|"), nil
}
