package sidecar

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// layout is where a manager keeps its files (sidecar.dir). Every name in
// a path comes from the validated catalog (plain file names).
type layout struct {
	root string
	mu   sync.Mutex
	// checked maps a file to the stamp (size, modification time) and
	// SHA-256 it had when it last hashed right: an unchanged file is not
	// hashed again (≈360 MB per launch otherwise).
	checked map[string]string
}

func (l *layout) downloads() string { return filepath.Join(l.root, "downloads") }

func (l *layout) build(b Build) string {
	return filepath.Join(l.root, b.Runtime+"-"+b.RuntimeSpec.Version+"-"+b.Variant)
}

func (l *layout) model(b Build) string { return filepath.Join(l.root, "models", b.ModelFile.Name) }

func (l *layout) exe(b Build) string {
	return filepath.Join(l.build(b), filepath.FromSlash(b.Spec.Exe))
}

func (l *layout) state() string { return filepath.Join(l.root, "state.json") }

// manifestName is the file inside a build directory listing what was
// unpacked from which pinned archives.
const manifestName = ".manifest.json"

type manifest struct {
	Artifacts []string             `json:"artifacts"` // pinned SHA-256s, in order
	Files     map[string]FileEntry `json:"files"`
}

// verify is the check before every launch: the model file against its
// pin, the build directory against its manifest (every listed file
// intact, no file that is not listed: a planted DLL is refused), and the
// manifest against the pins (a changed pin means a new install).
func (l *layout) verify(b Build) error {
	if err := l.verifyFile(l.model(b), b.ModelFile.SHA256, b.ModelFile.Size); err != nil {
		return err
	}
	dir := l.build(b)
	man, err := readManifest(dir)
	if err != nil {
		return err
	}
	if !slices.Equal(man.Artifacts, pins(b)) {
		return fmt.Errorf("%w: %s was installed from other artifacts", ErrHash, dir)
	}
	if _, ok := man.Files[b.Spec.Exe]; !ok {
		return fmt.Errorf("%w: %s has no %s", ErrHash, dir, b.Spec.Exe)
	}
	if err := unlisted(dir, man); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(man.Files)) {
		if err := l.verifyEntry(dir, name, man.Files[name]); err != nil {
			return err
		}
	}
	return nil
}

func readManifest(dir string) (manifest, error) {
	var man manifest
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return man, err
	}
	if err := jsonx.DecodeStrict(raw, &man); err != nil {
		return man, fmt.Errorf("%w: %s: %v", ErrHash, dir, err)
	}
	return man, nil
}

// unlisted refuses any file or link of the build that the manifest does
// not list.
func unlisted(dir string, man manifest) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if name := filepath.ToSlash(rel); name != manifestName {
			if _, ok := man.Files[name]; !ok {
				return fmt.Errorf("%w: %s is not part of the installed build", ErrHash, p)
			}
		}
		return nil
	})
}

func (l *layout) verifyEntry(dir, name string, e FileEntry) error {
	p, err := local(name)
	if err != nil {
		return err
	}
	p = filepath.Join(dir, p)
	if e.Link != "" {
		if got, err := os.Readlink(p); err != nil || got != e.Link {
			return fmt.Errorf("%w: link %s changed", ErrHash, p)
		}
		return nil
	}
	return l.verifyFile(p, e.SHA256, e.Size)
}

// verifyFile is verifyFile with the stamp cache.
func (l *layout) verifyFile(path, sha string, size int64) error {
	stamp := fsx.Stamp(path) + "|" + sha
	l.mu.Lock()
	ok := l.checked[path] == stamp
	l.mu.Unlock()
	if ok {
		return nil
	}
	if err := verifyFile(path, sha, size); err != nil {
		return err
	}
	l.mu.Lock()
	if l.checked == nil {
		l.checked = map[string]string{}
	}
	l.checked[path] = stamp
	l.mu.Unlock()
	return nil
}

func pins(b Build) []string {
	out := make([]string, len(b.Spec.Artifacts))
	for i, a := range b.Spec.Artifacts {
		out[i] = a.SHA256
	}
	return out
}

// install downloads what is missing and unpacks the build into a fresh
// directory that replaces the old one only once complete. Downloaded
// archives are removed after unpacking (the manifest keeps their hashes).
func (m *Manager) install(ctx context.Context, b Build, report func(done, total int64)) error {
	if err := m.require(b); err != nil {
		return err
	}
	total := b.ModelFile.Size
	for _, a := range b.Spec.Artifacts {
		total += a.Size
	}
	prog := &progress{total: total, report: report}
	if err := m.dl.fetch(ctx, b.ModelFile, m.layout.model(b), prog.at(0)); err != nil {
		return err
	}
	if err := m.layout.verify(b); err == nil {
		prog.done(total)
		return nil
	}
	offset := b.ModelFile.Size
	for _, a := range b.Spec.Artifacts {
		if err := m.dl.fetch(ctx, a, filepath.Join(m.layout.downloads(), a.Name), prog.at(offset)); err != nil {
			return err
		}
		offset += a.Size
	}
	if err := m.unpackBuild(b); err != nil {
		return err
	}
	m.log.Info("runtime installed", "runtime", b.Runtime, "version", b.RuntimeSpec.Version, "variant", b.Variant, "dir", m.layout.build(b))
	return m.layout.verify(b)
}

func (m *Manager) unpackBuild(b Build) error {
	dir := m.layout.build(b)
	tmp := dir + ".installing"
	if err := fsx.RemoveAll(tmp, m.replace()); err != nil {
		return err
	}
	man := manifest{Artifacts: pins(b), Files: map[string]FileEntry{}}
	for _, a := range b.Spec.Artifacts {
		files, err := unpack(filepath.Join(m.layout.downloads(), a.Name), a.Unpack, tmp)
		if err != nil {
			_ = fsx.RemoveAll(tmp, m.replace())
			return fmt.Errorf("sidecar: unpack %s: %w", a.Name, err)
		}
		maps.Copy(man.Files, files)
	}
	if err := fsx.WriteJSON(filepath.Join(tmp, manifestName), man, fsx.FilePerm, m.replace()); err != nil {
		return err
	}
	if err := fsx.RemoveAll(dir, m.replace()); err != nil {
		return err
	}
	if err := fsx.Promote(tmp, dir, m.replace()); err != nil {
		return err
	}
	for _, a := range b.Spec.Artifacts {
		_ = fsx.Remove(filepath.Join(m.layout.downloads(), a.Name), m.replace())
	}
	return nil
}

// progress turns per-artifact byte counts into one job progress, reported
// at most every half percent.
type progress struct {
	mu          sync.Mutex
	total, last int64
	report      func(done, total int64)
}

func (p *progress) at(offset int64) func(int64) { return func(n int64) { p.done(offset + n) } }

func (p *progress) done(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n == p.total || n-p.last >= p.total/200 {
		p.last = n
		p.report(n, p.total)
	}
}
