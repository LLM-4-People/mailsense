package goldexport

import (
	"path/filepath"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// File names of an export (and of the benchmark directory).
const (
	TaxonomyFile = "taxonomy.json"
	InputFile    = "gold-input.jsonl"
	StrataFile   = "strata.json"
	idChars      = 12
)

// ID is the export id of a message: a hash of its identity (the
// benchmark's ids, so earlier gold labels stay valid).
func ID(m *mail.Message) string {
	return digest.Hex([]byte(m.SourceID+"\x00"+m.StoreID+"\x00"+m.ID), idChars)
}

// Taxonomy is taxonomy.json: the questions the judge answers.
type Taxonomy struct {
	Fingerprint string            `json:"fingerprint"`
	Questions   []engine.Question `json:"questions"`
}

// Input is one line of gold-input.jsonl: the model view of a message.
type Input struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Write creates dir private (mode 700) and writes each file (mode 600).
func Write(dir string, files map[string][]byte, replace fsx.Replace) error {
	if err := fsx.EnsureDir(dir); err != nil {
		return err
	}
	for name, data := range files {
		if err := fsx.WriteFileAtomic(filepath.Join(dir, name), data, fsx.FilePerm, replace); err != nil {
			return err
		}
	}
	return nil
}
