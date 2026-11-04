package goldset

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// UnsealEnv names the environment variable OpenTest requires: the digest
// of the frozen model the test split is scored for, written on purpose.
const UnsealEnv = "GOLD_UNSEAL"

// ErrAlreadyUnsealed is returned by OpenTest when the ledger holds an
// entry: the test split is scored once.
var ErrAlreadyUnsealed = errors.New("goldset: the test split was already unsealed once (sealed/ledger.jsonl)")

// unsealLock is the file an unsealing holds (created exclusively).
const unsealLock = ".unsealing"

// LedgerEntry is one unsealing.
type LedgerEntry struct {
	At          time.Time `json:"at"`
	ModelDigest string    `json:"model_digest"`
}

// OpenTest reads the sealed test labels, ONCE: GOLD_UNSEAL must equal
// modelDigest (the frozen model's digest, non-empty), and the ledger must
// not exist yet (it is created exclusively). The entry is written (and
// synced) before any label is read, so a failure after it still counts as
// the one unsealing. sealed/ is closed again whatever happens. now stamps
// the entry.
func OpenTest(dir, modelDigest string, now time.Time) (map[string]Answers, error) {
	if strings.TrimSpace(modelDigest) == "" {
		return nil, errors.New("goldset: OpenTest needs the frozen model's digest")
	}
	if got := os.Getenv(UnsealEnv); got != modelDigest {
		return nil, fmt.Errorf("goldset: %s must name the model digest %s to unseal the test split", UnsealEnv, modelDigest)
	}
	sealed := filepath.Join(dir, SealedDir)
	if st, err := os.Stat(sealed); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%w: %s has no %s directory", ErrNotSealed, dir, SealedDir)
	}
	// one unsealing at a time: a concurrent one never opens sealed/
	lock := filepath.Join(dir, unsealLock)
	lf, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsx.FilePerm)
	if err != nil {
		return nil, fmt.Errorf("goldset: an unsealing is in progress (%s): %w", unsealLock, err)
	}
	_ = lf.Close()
	defer os.Remove(lock)
	if err := os.Chmod(sealed, fsx.DirPerm); err != nil {
		return nil, err
	}
	defer closeSealed(sealed)
	line, err := json.Marshal(LedgerEntry{At: now.UTC(), ModelDigest: modelDigest})
	if err != nil {
		return nil, err
	}
	// the ledger is created exclusively: of two concurrent unsealings one
	// only creates it (bugs review, plausible: a double read)
	f, err := os.OpenFile(filepath.Join(sealed, LedgerFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsx.FilePerm)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrAlreadyUnsealed
	}
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(append(line, '\n'))
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	out := map[string]Answers{}
	err = eachLine(filepath.Join(sealed, LabelsFile), func(n int, line []byte) error {
		var l struct {
			ID      string  `json:"id"`
			Answers Answers `json:"answers"`
		}
		if err := json.Unmarshal(line, &l); err != nil {
			return fmt.Errorf("sealed %s:%d: %w", LabelsFile, n, err)
		}
		out[l.ID] = l.Answers
		return nil
	})
	return out, err
}
