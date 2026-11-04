// Package goldset is the dev-time access to a gold sample (a directory
// written by the gold export and the judges: splits.json, gold-labels.jsonl
// and the judges' working files) with a SEALED test split.
//
// Seal moves every line and value naming a test-split message out of
// every file of the directory into sealed/ (a directory of mode 000),
// once; Open then reads the train and dev labels only and refuses a
// directory that is not sealed or still names a test message outside
// sealed/ in any file (it runs Seal's planner read-only: any pending plan
// or problem refuses). Test ids stay available (they are needed to exclude the test
// messages from training); their labels and inputs are read only by
// OpenTest, once: it needs GOLD_UNSEAL to name the frozen model's digest
// and refuses when the ledger already holds an entry.
//
// Test support for the fixture harnesses (evidence, layer 1): no
// production code imports it.
package goldset

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Splits of a gold sample.
const (
	Train = "train"
	Dev   = "dev"
	Test  = "test"
)

// File names inside a gold directory.
const (
	SplitsFile = "splits.json"
	LabelsFile = "gold-labels.jsonl"
	SealedDir  = "sealed"
	LedgerFile = "ledger.jsonl"
)

// ErrNotSealed is returned by Open for a directory whose test split was
// never sealed or that names a test message outside sealed/.
var ErrNotSealed = errors.New("goldset: the test split is not sealed (scripts/gold-seal.sh)")

// Answers are one message's gold answers by question, as the judge wrote
// them (option name, true/false or probability, level).
type Answers map[string]json.RawMessage

// Set is a sealed gold sample: every id's split, the train and dev
// labels. It never holds a test label.
type Set struct {
	Dir    string
	Splits map[string]string  // id → split, the test ids included
	Labels map[string]Answers // train and dev ids only
}

// Open reads a sealed gold directory: the splits and the train and dev
// labels. A test id in gold-labels.jsonl (an unsealed directory) is
// ErrNotSealed; an id the splits do not name, a line without an id or two
// lines of one id with different answers is an error. A test line is never
// decoded beyond its id.
func Open(dir string) (*Set, error) {
	splits, err := ReadSplits(dir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(filepath.Join(dir, SealedDir)); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%w: %s has no %s directory", ErrNotSealed, dir, SealedDir)
	}
	_, plans, problems, err := planDir(dir)
	if err != nil {
		return nil, err
	}
	if len(plans) > 0 {
		var files []string
		for _, p := range plans {
			files = append(files, p.rel)
		}
		return nil, fmt.Errorf("%w: %d file(s) still name a test message (%s)", ErrNotSealed, len(plans), strings.Join(files, ", "))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("goldset: %s", strings.Join(problems, "; "))
	}
	s := &Set{Dir: dir, Splits: splits, Labels: map[string]Answers{}}
	err = eachLine(filepath.Join(dir, LabelsFile), func(n int, line []byte) error {
		id, err := lineID(line)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", LabelsFile, n, err)
		}
		switch splits[id] {
		case "":
			return fmt.Errorf("%s:%d: id %s is in no split", LabelsFile, n, id)
		case Test:
			return fmt.Errorf("%w: %s:%d names a test message", ErrNotSealed, LabelsFile, n)
		}
		var l struct {
			Answers Answers `json:"answers"`
		}
		if err := json.Unmarshal(line, &l); err != nil {
			return fmt.Errorf("%s:%d: %w", LabelsFile, n, err)
		}
		if prev, dup := s.Labels[id]; dup && !sameAnswers(prev, l.Answers) {
			return fmt.Errorf("%s:%d: id %s has two different labels", LabelsFile, n, id)
		}
		s.Labels[id] = l.Answers
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// IDs lists the ids of a split, sorted.
func (s *Set) IDs(split string) []string {
	var out []string
	for id, sp := range s.Splits {
		if sp == split {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Used counts the labelled ids per split (train and dev only).
func (s *Set) Used() map[string]int {
	out := map[string]int{}
	for id := range s.Labels {
		out[s.Splits[id]]++
	}
	return out
}

// Of returns the labels of one split.
func (s *Set) Of(split string) map[string]Answers {
	out := map[string]Answers{}
	for id, a := range s.Labels {
		if s.Splits[id] == split {
			out[id] = a
		}
	}
	return out
}

// ReadSplits reads splits.json ({"split": {id: train|dev|test}}). An id
// written twice, an empty id or another split name is an error.
func ReadSplits(dir string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, SplitsFile))
	if err != nil {
		return nil, err
	}
	var top struct {
		Split json.RawMessage `json:"split"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("%s: %w", SplitsFile, err)
	}
	if len(top.Split) == 0 {
		return nil, fmt.Errorf("%s: no split object", SplitsFile)
	}
	dec := json.NewDecoder(bytes.NewReader(top.Split))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%s: split is not an object", SplitsFile)
	}
	out := map[string]string{}
	var problems []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", SplitsFile, err)
		}
		id, _ := tok.(string)
		var split string
		if err := dec.Decode(&split); err != nil {
			return nil, fmt.Errorf("%s: id %q: %w", SplitsFile, id, err)
		}
		switch {
		case strings.TrimSpace(id) == "":
			problems = append(problems, "an empty id")
		case out[id] != "":
			problems = append(problems, fmt.Sprintf("id %s is listed twice", id))
		case split != Train && split != Dev && split != Test:
			problems = append(problems, fmt.Sprintf("id %s: unknown split %q", id, split))
		default:
			out[id] = split
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %s", SplitsFile, strings.Join(problems, "; "))
	}
	return out, nil
}

// lineID decodes a JSONL line's id and nothing else.
func lineID(line []byte) (string, error) {
	var l struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return "", err
	}
	if l.ID == nil || strings.TrimSpace(*l.ID) == "" {
		return "", errors.New("a line without an id")
	}
	return *l.ID, nil
}

// eachLine calls fn with every non-blank line (1-based numbers).
func eachLine(path string, fn func(n int, line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return scanLines(f, fn)
}

func scanLines(r io.Reader, fn func(n int, line []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<26)
	n := 0
	for sc.Scan() {
		n++
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		if err := fn(n, sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

func sameAnswers(a, b Answers) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !bytes.Equal(bytes.TrimSpace(v), bytes.TrimSpace(w)) {
			return false
		}
	}
	return true
}
