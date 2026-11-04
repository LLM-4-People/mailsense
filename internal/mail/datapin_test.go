package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// The generated data files are exactly what the pinned sources gave
// (scripts/update-unicode-data.sh, scripts/update-prefixes.sh): an edit
// by hand fails here until the script is re-run.
func TestShippedDataMatchesItsPins(t *testing.T) {
	for _, pinFile := range []string{"unicode-pin.json", "prefixes-pin.json"} {
		raw, err := os.ReadFile(pinFile)
		if err != nil {
			t.Fatal(err)
		}
		var pin struct {
			Outputs map[string]string `json:"outputs"`
		}
		if err := json.Unmarshal(raw, &pin); err != nil || len(pin.Outputs) == 0 {
			t.Fatalf("%s: %v %v", pinFile, err, pin.Outputs)
		}
		for name, want := range pin.Outputs {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
				t.Errorf("%s differs from %s: re-run its update script", name, pinFile)
			}
		}
	}
}

// The script data and Go's script tables (ScriptOf) come from the same
// Unicode version.
func TestUnicodePinMatchesGo(t *testing.T) {
	raw, err := os.ReadFile("unicode-pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		UCD struct {
			Version string `json:"version"`
		} `json:"ucd"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil || pin.UCD.Version != unicode.Version {
		t.Fatalf("UCD pin %q, Go's unicode tables %s (%v): pin the same version", pin.UCD.Version, unicode.Version, err)
	}
}

func TestShippedPrefixesAndQuestionMarks(t *testing.T) {
	p := SubjectPrefixes([]string{"svar"})
	for _, want := range []string{"Re", "AW", "回复", "회신", "svar"} {
		found := false
		for _, v := range p {
			found = found || v == want
		}
		if !found {
			t.Errorf("prefix %q missing from %v", want, p)
		}
	}
	if len(SubjectPrefixes(nil)) != len(p)-1 {
		t.Error("additions never replace the shipped list")
	}
	if n := QuestionMarks("Why? 为什么？ لماذا؟ ፧ ⸮ and ¿ no"); n != 5 {
		t.Fatalf("question marks: %d", n)
	}
}

// Review of the crash-safety round, #9 (failed first): any byte of
// clean.json — its _doc too — changed DataDigest, so the evidence keys,
// the triage states and the vectors' text versions all moved. A doc edit
// or a reformatting changes nothing; a value does.
func TestDataDigestIgnoresDocumentation(t *testing.T) {
	edit := func(raw []byte, f func(map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		out, err := json.MarshalIndent(m, "", "\t")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	files := dataFiles()
	files[2] = edit(cleanJSON, func(m map[string]any) { m["_doc"] = "rewritten" })
	if digest.Content(files...) != DataDigest() {
		t.Fatal("a documentation edit of clean.json changed the data digest")
	}
	files[2] = edit(cleanJSON, func(m map[string]any) { m["intro_max_runes"] = 201.0 })
	if digest.Content(files...) == DataDigest() {
		t.Fatal("a changed limit must change the data digest")
	}
}
