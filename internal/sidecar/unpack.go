package sidecar

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// FileEntry is one unpacked file as the manifest records it: its SHA-256
// and size, or the target of a symbolic link (tar archives).
type FileEntry struct {
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
	Link   string `json:"link,omitempty"`
}

// unpack extracts an archive into dir (created) through an os.Root, so no
// entry can write outside it whatever links earlier entries made (a link
// chain l1 → ., l1/l2 → .. then l1/l2/x is refused by the root). Names
// must be local, a link's target must stay inside by name too, and
// executables get FilePerm plus the owner's execute bit.
func unpack(archive, format, dir string) (map[string]FileEntry, error) {
	if err := fsx.EnsureDir(dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	switch format {
	case UnpackZip:
		return unzip(archive, root)
	case UnpackTarGz:
		return untar(archive, root)
	}
	return nil, fmt.Errorf("sidecar: cannot unpack %q archives", format)
}

func unzip(archive string, root *os.Root) (map[string]FileEntry, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out := map[string]FileEntry{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("sidecar: %s: %s is not a regular file", archive, f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		entry, err := writeEntry(root, f.Name, rc, f.Mode())
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		out[f.Name] = entry
	}
	return out, nil
}

func untar(archive string, root *os.Root) (map[string]FileEntry, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	out := map[string]FileEntry{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if err := tarEntry(root, h, tr, out); err != nil {
			return nil, fmt.Errorf("sidecar: %s: %w", archive, err)
		}
	}
}

func tarEntry(root *os.Root, h *tar.Header, r io.Reader, out map[string]FileEntry) error {
	switch h.Typeflag {
	case tar.TypeDir:
		return nil
	case tar.TypeReg:
		entry, err := writeEntry(root, h.Name, r, h.FileInfo().Mode())
		out[h.Name] = entry
		return err
	case tar.TypeSymlink:
		if err := writeLink(root, h.Name, h.Linkname); err != nil {
			return err
		}
		out[h.Name] = FileEntry{Link: h.Linkname}
		return nil
	}
	return fmt.Errorf("%s has unsupported type %c", h.Name, h.Typeflag)
}

// local is an archive name as a root-relative path, refusing escapes.
func local(name string) (string, error) {
	p := filepath.FromSlash(name)
	if !filepath.IsLocal(p) {
		return "", fmt.Errorf("sidecar: archive entry %q escapes the install directory", name)
	}
	return p, nil
}

func writeEntry(root *os.Root, name string, r io.Reader, mode fs.FileMode) (FileEntry, error) {
	p, err := local(name)
	if err != nil {
		return FileEntry{}, err
	}
	perm := fsx.FilePerm
	if mode&0o111 != 0 {
		perm |= 0o100
	}
	h := digest.NewHasher()
	n, err := fsx.WriteStreamIn(root, p, io.TeeReader(r, h), perm)
	return FileEntry{SHA256: h.Hex(), Size: n}, err
}

func writeLink(root *os.Root, name, target string) error {
	p, err := local(name)
	if err != nil {
		return err
	}
	if _, err := local(path.Join(path.Dir(name), target)); err != nil || path.IsAbs(target) {
		return fmt.Errorf("sidecar: link %q → %q leaves the install directory", name, target)
	}
	return fsx.SymlinkIn(root, target, p)
}
