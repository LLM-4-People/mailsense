package rfc5322

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Folder layouts, recognised by structure (never by a folder's name).
const (
	layoutMbox    = "mbox"    // one file, messages after "From " lines (RFC 4155)
	layoutMaildir = "maildir" // a directory with cur/ and new/ (one file per message)
	layoutEML     = "eml"     // a directory of .eml files, or one .eml file
)

// folderSpec is one folder of the source.
type folderSpec struct {
	Path   string // display path, "/" separated, relative to the source root
	Layout string
	FSPath string
}

// scanLayout lists the folders under root: a file is one mbox (or one
// .eml message); in a directory tree, every maildir (cur/ and new/), every
// directory holding .eml files and every mbox file is a folder. Maildir++
// subfolders (".Sub.Folder") are named with "/".
func scanLayout(root string) ([]folderSpec, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(root)
	if !st.IsDir() {
		if strings.EqualFold(filepath.Ext(root), ".eml") {
			return []folderSpec{{Path: base, Layout: layoutEML, FSPath: root}}, nil
		}
		if !isMbox(root) {
			return nil, errors.New("rfc5322: " + base + " is neither an mbox file nor an .eml message")
		}
		return []folderSpec{{Path: base, Layout: layoutMbox, FSPath: root}}, nil
	}
	var out []folderSpec
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		name := displayPath(base, rel)
		if d.IsDir() {
			switch {
			case isMaildir(p):
				out = append(out, folderSpec{Path: name, Layout: layoutMaildir, FSPath: p})
			case hasEML(p):
				out = append(out, folderSpec{Path: name, Layout: layoutEML, FSPath: p})
			}
			if b := d.Name(); p != root && (b == "cur" || b == "new" || b == "tmp") && isMaildir(filepath.Dir(p)) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && isMbox(p) {
			out = append(out, folderSpec{Path: name, Layout: layoutMbox, FSPath: p})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// displayPath is the folder name: the root's name, then the relative path
// with Maildir++ dots turned into levels.
func displayPath(base, rel string) string {
	if rel == "." {
		return base
	}
	parts := []string{base}
	for _, p := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(p, ".") && len(p) > 1 {
			parts = append(parts, strings.Split(p[1:], ".")...)
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/")
}

func isMaildir(dir string) bool {
	for _, sub := range []string{"cur", "new"} {
		if st, err := os.Stat(filepath.Join(dir, sub)); err != nil || !st.IsDir() {
			return false
		}
	}
	return true
}

func hasEML(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			return true
		}
	}
	return false
}

// isMbox: the file starts with an RFC 4155 "From " line.
func isMbox(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head, _ := bufio.NewReader(f).Peek(5)
	return bytes.Equal(head, []byte("From "))
}
