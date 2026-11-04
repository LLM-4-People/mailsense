package goldset

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gold writes a small gold directory: id-a1, id-c3 train/dev; id-b2, id-d4 test.
func gold(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, SealedDir), 0o700) }) // so TempDir can remove it
	write(t, dir, SplitsFile, `{"seed": 1, "split": {"id-a1": "train", "id-b2": "test", "id-c3": "dev", "id-d4": "test"}}`)
	write(t, dir, LabelsFile, `{"id": "id-a1", "answers": {"q": 1}}
{"id": "id-b2", "answers": {"q": "secret-b"}}
{"id": "id-c3", "answers": {"q": 0}}
{"id": "id-d4", "answers": "@@ never decoded"}
{"id": "id-a1", "answers": {"q": 1}}
`)
	write(t, dir, "gold-input.jsonl", `{"id": "id-a1", "text": "x"}
{"id": "id-b2", "text": "secret input"}
`)
	write(t, dir, "batches/labels-A-0.jsonl", `{"id": "id-d4", "answers": {"q": 2}}
{"id": "id-c3", "answers": {"q": 0}}
`)
	write(t, dir, "batches/disagreements.json", `{"q": ["id-a1", "id-b2", "id-d4"], "r": ["id-c3"], "id-b2": {"x": 1}}`)
	write(t, dir, "NOTES.md", "aggregate notes, no id here\n")
	write(t, dir, "strata.json", `{"n": 4}`)
	return dir
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOpenRefusesAnUnsealedDirectory(t *testing.T) {
	dir := gold(t)
	if _, err := Open(dir); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("unsealed directory opened: %v", err)
	}
	// A sealed/ directory alone is not enough: a test line outside it is refused.
	if err := os.Mkdir(filepath.Join(dir, SealedDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("a test line outside sealed/ was accepted: %v", err)
	}
}

func TestSealMovesEveryTestMentionAndOpenReadsTrainAndDev(t *testing.T) {
	dir := gold(t)
	rep, err := Seal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TestIDs != 2 || len(rep.Files) != 4 || rep.Clean != 2 {
		t.Fatalf("report %+v", rep)
	}
	for _, name := range []string{LabelsFile, "gold-input.jsonl", "batches/labels-A-0.jsonl", "batches/disagreements.json"} {
		got := read(t, dir, name)
		if strings.Contains(got, `"id-b2"`) || strings.Contains(got, `"id-d4"`) || strings.Contains(got, "secret") {
			t.Errorf("%s still names a test message: %s", name, got)
		}
	}
	var dis map[string]any
	if err := json.Unmarshal([]byte(read(t, dir, "batches/disagreements.json")), &dis); err != nil || len(dis["q"].([]any)) != 1 || dis["id-b2"] != nil {
		t.Errorf("redacted json %v %v", dis, err)
	}
	st, err := os.Stat(filepath.Join(dir, SealedDir))
	if err != nil || st.Mode().Perm() != 0 {
		t.Fatalf("sealed/ mode %v %v", st.Mode(), err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Labels) != 2 || s.Labels["id-b2"] != nil || s.Used()[Train] != 1 || s.Used()[Dev] != 1 || len(s.IDs(Test)) != 2 {
		t.Fatalf("set %+v used %v", s.Labels, s.Used())
	}
	if len(s.Of(Dev)) != 1 || s.Of(Dev)["id-c3"] == nil {
		t.Errorf("dev labels %v", s.Of(Dev))
	}
	// Sealing again changes nothing.
	if rep, err := Seal(dir); err != nil || len(rep.Files) != 0 {
		t.Fatalf("second seal %+v %v", rep, err)
	}
	// A new judge file naming a test message is sealed by the next run.
	write(t, dir, "batches/labels-B-0.jsonl", `{"id": "id-b2", "answers": {"q": 3}}`+"\n")
	if rep, err := Seal(dir); err != nil || len(rep.Files) != 1 || rep.Files[0].Sealed != 1 {
		t.Fatalf("later seal %+v %v", rep, err)
	}
}

func TestSealRefusesWhatItCannotSplit(t *testing.T) {
	for name, tc := range map[string]struct{ file, data string }{
		"a text file naming a test id": {"notes.md", "see id-b2 and more\n"},
		"a line without an id":         {"x.jsonl", `{"answers": {}}` + "\n"},
		"an id in no split":            {"x.jsonl", `{"id": "zz"}` + "\n"},
		"junk jsonl":                   {"x.jsonl", "not json\n"},
		"a test id as a member value":  {"x.json", `{"who": "id-b2"}`},
		"junk json":                    {"x.json", `{`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := gold(t)
			write(t, dir, tc.file, tc.data)
			before := read(t, dir, LabelsFile)
			if _, err := Seal(dir); err == nil {
				t.Fatal("sealed")
			}
			if read(t, dir, LabelsFile) != before {
				t.Error("a refused seal changed a file")
			}
			if _, err := os.Stat(filepath.Join(dir, SealedDir)); !os.IsNotExist(err) {
				t.Error("a refused seal created sealed/")
			}
		})
	}
}

// Regression (shared primitives round 1): planJSON decoded one value with no
// trailing check, so two concatenated documents were silently accepted. When
// the first named no test id, the second was counted clean and its test ids
// were never sealed.
func TestSealRefusesTwoConcatenatedJSONDocuments(t *testing.T) {
	for name, data := range map[string]string{
		"two documents":                    `{"a": 1}{"b": 2}`,
		"a test id in the second document": `{"a": 1}{"id": "id-b2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := gold(t)
			write(t, dir, "two.json", data)
			before := read(t, dir, LabelsFile)
			if _, err := Seal(dir); err == nil {
				t.Fatal("sealed")
			}
			if read(t, dir, LabelsFile) != before {
				t.Error("a refused seal changed a file")
			}
			if _, err := os.Stat(filepath.Join(dir, SealedDir)); !os.IsNotExist(err) {
				t.Error("a refused seal created sealed/")
			}
			if _, err := Open(dir); err == nil {
				t.Error("Open accepted a directory with two concatenated documents")
			}
		})
	}
}

func TestSplitsMisuse(t *testing.T) {
	for name, data := range map[string]string{
		"an id twice":   `{"split": {"id-a1": "train", "id-a1": "dev"}}`,
		"unknown split": `{"split": {"id-a1": "holdout"}}`,
		"empty id":      `{"split": {" ": "train"}}`,
		"no split":      `{"seed": 1}`,
		"not an object": `{"split": ["id-a1"]}`,
		"junk":          `{`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, SplitsFile, data)
			if _, err := ReadSplits(dir); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestOpenRefusesConflictingOrUnknownLabels(t *testing.T) {
	for name, labels := range map[string]string{
		"two labels of one id": `{"id": "id-a1", "answers": {"q": 1}}` + "\n" + `{"id": "id-a1", "answers": {"q": 2}}` + "\n",
		"an id in no split":    `{"id": "zz", "answers": {}}` + "\n",
		"no id":                `{"answers": {}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := gold(t)
			if _, err := Seal(dir); err != nil {
				t.Fatal(err)
			}
			write(t, dir, LabelsFile, labels)
			if _, err := Open(dir); err == nil || errors.Is(err, ErrNotSealed) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestOpenTestUnsealsOnceForTheNamedModel(t *testing.T) {
	dir := gold(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	t.Setenv(UnsealEnv, "m1")
	if _, err := OpenTest(dir, "m1", now); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("unsealed directory: %v", err)
	}
	if _, err := Seal(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(UnsealEnv, "")
	if _, err := OpenTest(dir, "m1", now); err == nil {
		t.Fatal("unsealed without the variable")
	}
	t.Setenv(UnsealEnv, "other")
	if _, err := OpenTest(dir, "m1", now); err == nil {
		t.Fatal("unsealed for another model")
	}
	if _, err := OpenTest(dir, "", now); err == nil {
		t.Fatal("unsealed without a model digest")
	}
	t.Setenv(UnsealEnv, "m1")
	if _, err := OpenTest(dir, "m1", now); err == nil {
		// d's line is junk on purpose: the read fails, yet the ledger entry stays.
		t.Fatal("junk sealed line decoded")
	}
	if _, err := OpenTest(dir, "m1", now); !errors.Is(err, ErrAlreadyUnsealed) {
		t.Fatalf("second unseal: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, SealedDir)); st.Mode().Perm() != 0 {
		t.Errorf("sealed/ left open: %v", st.Mode())
	}
}

func TestOpenTestReadsTheSealedLabels(t *testing.T) {
	dir := gold(t)
	write(t, dir, LabelsFile, `{"id": "id-a1", "answers": {"q": 1}}`+"\n"+`{"id": "id-b2", "answers": {"q": 2}}`+"\n")
	if _, err := Seal(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(UnsealEnv, "m1")
	labels, err := OpenTest(dir, "m1", time.Now())
	if err != nil || len(labels) != 1 || string(labels["id-b2"]["q"]) != "2" {
		t.Fatalf("%v %v", labels, err)
	}
}

func TestAgreementKinds(t *testing.T) {
	dir := gold(t)
	write(t, dir, "batches/labels-B-0.jsonl", `{"id": "id-a1", "answers": {"q": 1}}`+"\n"+`{"id": "id-c3", "answers": {"q": 0}}`+"\n")
	write(t, dir, "batches/disagreements-AB.json", `{"q": ["id-c3", "id-b2"]}`)
	if _, err := Seal(dir); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	kinds, err := s.Agreement()
	if err != nil {
		t.Fatal(err)
	}
	if kinds["q"]["id-a1"] != Agreed || kinds["q"]["id-c3"] != Adjudicated || len(kinds["q"]) != 2 {
		t.Fatalf("%v", kinds)
	}
}

// Findings (bugs review #11): Open checked gold-labels.jsonl only (a
// judge file added after sealing that held test lines was accepted), and
// Seal kept a line naming a test id in another field.
func TestOpenRefusesAnyFileNamingATestMessage(t *testing.T) {
	dir := gold(t)
	if _, err := Seal(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	test := "id-b2"
	write(t, dir, "judge-late.jsonl", `{"id": "`+test+`", "answers": {}}`+"\n")
	if _, err := Open(dir); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("a late file with a test line: %v", err)
	}
	if _, err := Seal(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatalf("sealed again: %v", err)
	}
	other := "id-a1"
	write(t, dir, "pairs.jsonl", `{"id": "`+other+`", "twin": "`+test+`"}`+"\n")
	if _, err := Seal(dir); err == nil || !strings.Contains(err.Error(), "another field") {
		t.Fatalf("a kept line naming a test id elsewhere: %v", err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted a kept line naming a test id")
	}
}

// Finding (bugs review, plausible): two concurrent OpenTest calls could
// both read an empty ledger. The ledger is created exclusively.
func TestOpenTestOnceUnderConcurrency(t *testing.T) {
	dir := gold(t)
	if _, err := Seal(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(UnsealEnv, "digest-1")
	var wg sync.WaitGroup
	var refused atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// the fixture's sealed labels hold one undecodable line: the one
			// unsealing that reads them fails after its ledger entry
			_, err := OpenTest(dir, "digest-1", time.Now())
			if errors.Is(err, ErrAlreadyUnsealed) || (err != nil && strings.Contains(err.Error(), "in progress")) {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if refused.Load() != 7 {
		t.Fatalf("%d of 8 concurrent unsealings refused, want 7", refused.Load())
	}
}
