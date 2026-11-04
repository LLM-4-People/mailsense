package outlook

import (
	"encoding/json"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// folderKey identifies a discovered folder (the syncer's folder identity).
type folderKey struct{ storeID, path string }

// folderState is what Discover learned about a folder: how to open it and
// its change state, read before any of its rows (so the cursor never marks
// stale data fresh).
type folderState struct {
	storeID, entryID string
	kind             mail.StoreKind
	count, unread    int64
	commitMax        string    // PR_LOCAL_COMMIT_TIME_MAX, "" when the store does not keep it
	newest           string    // newest LastModificationTime, read only without commitMax
	readAt           time.Time // when Discover read this state
}

// cursorState is the persisted change state of a folder, to the
// millisecond. A folder is unchanged when its item and unread counts and
// its commit time (or, where the store keeps no commit time, the newest
// modification time) are equal — and its last snapshot is younger than
// outlook.sync.recheck_after: no folder-level property reveals a read flip
// pair or a change replicated with an older modification time, so every
// folder is re-snapshotted that often (the backstop).
type cursorState struct {
	Count     int64  `json:"count"`
	Unread    int64  `json:"unread"`
	CommitMax string `json:"commit_max,omitempty"`
	Newest    string `json:"newest,omitempty"`
	Checked   string `json:"checked"` // when this state was read (before the snapshot)
}

func (st folderState) state() cursorState {
	return cursorState{Count: st.count, Unread: st.unread, CommitMax: st.commitMax, Newest: st.newest,
		Checked: st.readAt.UTC().Format(exactLayout)}
}

// known: a non-empty folder without commit or newest time cannot be compared.
func (st folderState) known() bool { return st.commitMax != "" || st.newest != "" || st.count == 0 }

// cursor encodes the state.
func (st folderState) cursor() source.Cursor {
	b, _ := json.Marshal(st.state())
	return source.Cursor(b)
}

// unchanged reports whether stored (the cursor of the last sync) says the
// folder needs no snapshot now.
func (st folderState) unchanged(stored source.Cursor, recheck time.Duration) bool {
	var old cursorState
	if !st.known() || json.Unmarshal([]byte(stored), &old) != nil {
		return false
	}
	checked, err := time.Parse(exactLayout, old.Checked)
	now := st.state()
	now.Checked = old.Checked
	return err == nil && now == old && st.readAt.Sub(checked) < recheck
}
