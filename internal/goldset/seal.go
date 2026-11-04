package goldset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// FileSeal is what Seal did to one file: counts only (lines or values
// kept and moved to sealed/).
type FileSeal struct {
	File   string `json:"file"`
	Kind   string `json:"kind"` // jsonl, json
	Kept   int    `json:"kept"`
	Sealed int    `json:"sealed"`
}

// SealReport is Seal's result: counts only, never a label or an input.
type SealReport struct {
	TestIDs int        `json:"test_ids"`
	Files   []FileSeal `json:"files"` // files that named a test message
	Clean   int        `json:"clean"` // files that named none
}

// sealPlan is one file's change: the kept content and the sealed part.
type sealPlan struct {
	rel          string
	kept, sealed []byte
	whole        bool // a JSON file: the original is sealed whole
	count        FileSeal
}

// Seal moves everything naming a test-split message out of every file of
// dir (recursively, sealed/ and splits.json aside) into sealed/<same
// path>, then makes sealed/ unreadable (mode 000):
//   - a JSONL file: the lines whose id is a test id;
//   - a JSON file: the original whole, the kept copy without the array
//     items and object members that name a test id (any other place a
//     test id appears in is refused);
//   - any other file naming a test id is refused (it cannot be split
//     structurally: move it out by hand).
//
// Every file is checked before anything is written; a line without an id
// or with an id the splits do not name is refused. A sealed part is
// appended to its sealed file, never read. Sealing again moves only what
// was added since (none: nothing changes).
func Seal(dir string) (SealReport, error) {
	rep, plans, problems, err := planDir(dir)
	if err != nil {
		return rep, err
	}
	if len(problems) > 0 {
		return rep, fmt.Errorf("goldset: nothing sealed: %s", strings.Join(problems, "; "))
	}
	if err := apply(dir, plans); err != nil {
		return rep, err
	}
	for _, p := range plans {
		rep.Files = append(rep.Files, p.count)
	}
	return rep, nil
}

// planDir plans the sealing of every file of dir without writing
// anything: the plans (files that still name a test message) and the
// problems (files that cannot be sealed). Open runs it read-only: a
// directory with a pending plan or a problem is not sealed.
func planDir(dir string) (SealReport, []sealPlan, []string, error) {
	splits, err := ReadSplits(dir)
	if err != nil {
		return SealReport{}, nil, nil, err
	}
	test := map[string]bool{}
	for id, sp := range splits {
		if sp == Test {
			test[id] = true
		}
	}
	rep := SealReport{TestIDs: len(test)}
	var plans []sealPlan
	var problems []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case d.IsDir() && rel == SealedDir:
			return filepath.SkipDir
		case d.IsDir() || rel == SplitsFile:
			return nil
		case !d.Type().IsRegular():
			problems = append(problems, rel+": not a regular file")
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		p, err := planFile(rel, data, splits, test)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case p == nil:
			rep.Clean++
		default:
			plans = append(plans, *p)
		}
		return nil
	})
	return rep, plans, problems, err
}

// planFile decides one file's change (nil: it names no test message).
func planFile(rel string, data []byte, splits map[string]string, test map[string]bool) (*sealPlan, error) {
	switch filepath.Ext(rel) {
	case ".jsonl":
		return planLines(rel, data, splits, test)
	case ".json":
		return planJSON(rel, data, test)
	}
	for id := range test {
		if bytes.Contains(data, []byte(id)) {
			return nil, fmt.Errorf("%s names a test message and is not JSON: move it out by hand", rel)
		}
	}
	return nil, nil
}

func planLines(rel string, data []byte, splits map[string]string, test map[string]bool) (*sealPlan, error) {
	p := &sealPlan{rel: rel, count: FileSeal{File: rel, Kind: "jsonl"}}
	var kept, sealed bytes.Buffer
	err := scanLines(bytes.NewReader(data), func(n int, line []byte) error {
		id, err := lineID(line)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", rel, n, err)
		}
		if splits[id] == "" {
			return fmt.Errorf("%s:%d: id %s is in no split", rel, n, id)
		}
		if test[id] {
			sealed.Write(line)
			sealed.WriteByte('\n')
			p.count.Sealed++
			return nil
		}
		if other := namedTestID(line, test); other != "" {
			// a kept line naming a test message in another field (a pair, a
			// reference) would leak it: refused (bugs review #11)
			return fmt.Errorf("%s:%d: a kept line names a test message in another field: move it out by hand", rel, n)
		}
		kept.Write(line)
		kept.WriteByte('\n')
		p.count.Kept++
		return nil
	})
	if err != nil || p.count.Sealed == 0 {
		return nil, err
	}
	p.kept, p.sealed = kept.Bytes(), sealed.Bytes()
	return p, nil
}

// namedTestID is a test id the line contains anywhere ("" if none).
func namedTestID(line []byte, test map[string]bool) string {
	for id := range test {
		if bytes.Contains(line, []byte(id)) {
			return id
		}
	}
	return ""
}

func planJSON(rel string, data []byte, test map[string]bool) (*sealPlan, error) {
	v, err := jsonx.DecodeOne(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	p := &sealPlan{rel: rel, whole: true, count: FileSeal{File: rel, Kind: "json"}}
	kept, err := redact(v, test, &p.count)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if p.count.Sealed == 0 {
		return nil, nil
	}
	if p.kept, err = json.MarshalIndent(kept, "", "  "); err != nil {
		return nil, err
	}
	p.kept = append(p.kept, '\n')
	p.sealed = data
	return p, nil
}

// redact drops the array items and object members naming a test id;
// a test id anywhere else (a value of an object member) is refused.
func redact(v any, test map[string]bool, c *FileSeal) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if test[k] {
				c.Sealed++
				continue
			}
			if s, ok := x[k].(string); ok && test[s] {
				return nil, fmt.Errorf("member %q holds a test id: move the file out by hand", k)
			}
			r, err := redact(x[k], test, c)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := []any{}
		for _, item := range x {
			if s, ok := item.(string); ok && test[s] {
				c.Sealed++
				continue
			}
			if s, ok := item.(string); ok && s != "" {
				c.Kept++
			}
			r, err := redact(item, test, c)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	case string:
		if test[x] {
			return nil, fmt.Errorf("a test id outside an array or a key")
		}
	}
	return v, nil
}

// apply writes the plans: every sealed part first (appended), then the
// kept files (atomic), then sealed/ is closed (mode 000).
func apply(dir string, plans []sealPlan) error {
	sealed := filepath.Join(dir, SealedDir)
	if err := openSealed(sealed); err != nil {
		return err
	}
	defer closeSealed(sealed)
	for _, p := range plans {
		dst := filepath.Join(sealed, p.rel)
		if p.whole {
			dst = uniqueName(dst)
		}
		f, err := fsx.OpenAppend(dst)
		if err != nil {
			return err
		}
		_, werr := f.Write(p.sealed)
		serr := f.Sync()
		if cerr := f.Close(); werr == nil && serr == nil {
			werr = cerr
		} else if werr == nil {
			werr = serr
		}
		if werr != nil {
			return fmt.Errorf("goldset: sealing %s: %w", p.rel, werr)
		}
	}
	for _, p := range plans {
		if err := fsx.WriteFileAtomic(filepath.Join(dir, p.rel), p.kept, fsx.FilePerm, fsx.Replace{}); err != nil {
			return err
		}
	}
	return nil
}

// uniqueName is path, or path with a number before its extension when a
// sealed copy of that name exists (a JSON file sealed again).
func uniqueName(path string) string {
	ext := filepath.Ext(path)
	base, name := strings.TrimSuffix(path, ext), path
	for i := 1; ; i++ {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s.%d%s", base, i, ext)
	}
}

// openSealed makes sealed/ (and creates it) writable for this process.
func openSealed(dir string) error {
	if err := os.MkdirAll(dir, fsx.DirPerm); err != nil && !os.IsPermission(err) {
		return err
	}
	return os.Chmod(dir, fsx.DirPerm)
}

// closeSealed makes sealed/ unreadable again.
func closeSealed(dir string) { _ = os.Chmod(dir, 0) }
