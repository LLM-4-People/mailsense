package update

import (
	"fmt"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// stateSchemaVersion is the on-disk version of state.json. A file with
// another version is treated as no state: the app never guesses at a format
// it does not know.
const stateSchemaVersion = 1

// stateRelPath is the state file inside the data directory.
const stateRelPath = "updates/state.json"

// State is the persisted update-check state: the one owner of the check
// cadence, the cached latest release, the observed release tags, the What's
// New markers and the last failure. CheckedAt is the last attempt (success
// or failure), so a failing check cannot turn into a hammering loop.
type State struct {
	SchemaVersion    int      `json:"schema_version"`
	CheckedAt        string   `json:"checked_at,omitempty"`
	ETag             string   `json:"etag,omitempty"`
	Latest           *Release `json:"latest,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	SkippedVersion   string   `json:"skipped_version,omitempty"`
	SkippedAt        string   `json:"skipped_at,omitempty"`
	NotesSeenVersion string   `json:"notes_seen_version,omitempty"`
	ErrorCode        string   `json:"error_code,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	CheckFailures    int      `json:"check_failures,omitempty"`
}

// stateFile guards the state and its file: a snapshot for handlers, one
// locked update that writes through fsx.WriteJSON with the configured
// replace retries (the sidecar's one state-file pattern).
type stateFile struct {
	mu      sync.Mutex
	path    string
	replace fsx.Replace
	data    State
}

// loadState reads the file. A missing or unreadable one starts empty: the
// error is reported so the caller can log it, but detection must never crash
// or refuse to start on a state file it can rebuild.
func loadState(path string, replace fsx.Replace) (*stateFile, error) {
	s := &stateFile{path: path, replace: replace}
	raw, ok, err := fsx.ReadOptional(path)
	if err != nil || !ok {
		return s, err
	}
	var st State
	if derr := jsonx.DecodeStrict(raw, &st); derr != nil {
		return s, fmt.Errorf("update: %s is unreadable (starting empty): %w", path, derr)
	}
	if st.SchemaVersion != stateSchemaVersion {
		return s, fmt.Errorf("update: %s has schema version %d, want %d (starting empty)", path, st.SchemaVersion, stateSchemaVersion)
	}
	s.data = st
	return s, nil
}

// snapshot returns a copy safe to read without the lock.
func (s *stateFile) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.clone()
}

// update changes the state and writes the file. A failed write keeps the
// in-memory change (the next check changes it again) and reports the error.
func (s *stateFile) update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
	s.data.SchemaVersion = stateSchemaVersion
	return fsx.WriteJSON(s.path, s.data, fsx.FilePerm, s.replace)
}

// clone deep-copies the state so a snapshot never shares the cached
// release's pointer, asset slice or tag list with the writer.
func (st State) clone() State {
	out := st
	if st.Latest != nil {
		release := *st.Latest
		release.Assets = append(make([]Asset, 0, len(st.Latest.Assets)), st.Latest.Assets...)
		out.Latest = &release
	}
	if len(st.Tags) > 0 {
		out.Tags = append(make([]string, 0, len(st.Tags)), st.Tags...)
	}
	return out
}
